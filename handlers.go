package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxArtifactSize = 512 << 20 // 512 MB

var allowedRedirectHosts = map[string]bool{
	"maven.pkg.github.com":                 true,
	"objects.githubusercontent.com":        true,
	"pkg-containers.githubusercontent.com": true,
}

type visibilityCacheEntry struct {
	isPublic  bool
	found     bool
	expiresAt time.Time
}

type App struct {
	Fetch           func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error)
	GetToken        func(ctx context.Context, name string) (string, error)
	Storage         Storage
	Logger          *slog.Logger
	visibilityMu    sync.RWMutex
	visibilityCache map[string]*visibilityCacheEntry
}

func NewDefaultApp(storage Storage, logger *slog.Logger) *App {
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &App{
		Fetch: func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, method, url, body)
			if err != nil {
				return nil, err
			}
			req.Header = headers
			return client.Do(req)
		},
		GetToken:        realGetToken(storage),
		Storage:         storage,
		Logger:          logger,
		visibilityCache: make(map[string]*visibilityCacheEntry),
	}
}

func realGetToken(storage Storage) func(ctx context.Context, name string) (string, error) {
	return func(ctx context.Context, tokenName string) (string, error) {
		data, err := os.ReadFile(tokenName)
		if err == nil {
			return strings.TrimSpace(string(data)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		reader, err := storage.File("credentials/" + tokenName).NewReader(ctx)
		if err != nil {
			return "", err
		}
		defer reader.Close()

		data, err = io.ReadAll(reader)
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(string(data)), nil
	}
}

func (app *App) isPackagePublic(ctx context.Context, token string, parsed Artifact, repo string) (isPublic bool, found bool, err error) {
	packageName := parsed.GroupID + "." + parsed.ArtifactID
	query := `query($name: [String!]!) { organization(login:"navikt"){ packages(first:1,names:$name){ nodes{ repository{ name isPrivate } } } } }`
	payload, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"name": []string{packageName}},
	})
	body := bytes.NewReader(payload)
	headers := http.Header{}
	headers.Set("Authorization", "bearer "+token)
	headers.Set("Accept", "application/vnd.github.packages-preview+json")

	resp, err := app.Fetch(ctx, "https://api.github.com/graphql", http.MethodPost, headers, body)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, false, fmt.Errorf("GitHub GraphQL API returned status %d", resp.StatusCode)
	}

	type graphQLResponse struct {
		Data *struct {
			Organization *struct {
				Packages struct {
					Nodes []struct {
						Repository *struct {
							Name      string `json:"name"`
							IsPrivate bool   `json:"isPrivate"`
						} `json:"repository"`
					} `json:"nodes"`
				} `json:"packages"`
			} `json:"organization"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	var result graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, false, err
	}

	if len(result.Errors) > 0 {
		return false, false, fmt.Errorf("GitHub GraphQL error: %s", result.Errors[0].Message)
	}

	if result.Data == nil {
		return false, false, nil
	}
	if result.Data.Organization == nil {
		return false, false, nil
	}
	if len(result.Data.Organization.Packages.Nodes) == 0 {
		return false, false, nil
	}
	node := result.Data.Organization.Packages.Nodes[0]
	if node.Repository == nil {
		return false, false, nil
	}
	if !strings.EqualFold(node.Repository.Name, repo) {
		app.Logger.Warn("package repository mismatch", "expected", repo, "got", node.Repository.Name)
		return false, false, nil
	}
	if node.Repository.IsPrivate {
		return false, true, nil
	}

	return true, true, nil
}

const visibilityCacheTTL = 5 * time.Minute

func (app *App) isPackagePublicCached(ctx context.Context, token string, parsed Artifact, repo string) (bool, bool, error) {
	cacheKey := repo + ":" + parsed.GroupID + "." + parsed.ArtifactID

	app.visibilityMu.RLock()
	entry, ok := app.visibilityCache[cacheKey]
	app.visibilityMu.RUnlock()

	if ok {
		if time.Now().Before(entry.expiresAt) {
			return entry.isPublic, entry.found, nil
		}
		app.visibilityMu.Lock()
		delete(app.visibilityCache, cacheKey)
		app.visibilityMu.Unlock()
	}

	isPublic, found, err := app.isPackagePublic(ctx, token, parsed, repo)
	if err != nil {
		return false, false, err
	}

	app.visibilityMu.Lock()
	app.visibilityCache[cacheKey] = &visibilityCacheEntry{
		isPublic:  isPublic,
		found:     found,
		expiresAt: time.Now().Add(visibilityCacheTTL),
	}
	app.visibilityMu.Unlock()

	return isPublic, found, nil
}

func containsPathTraversal(path string) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return true
		}
	}
	return false
}

func (app *App) recoverPanic(w http.ResponseWriter) {
	if recovered := recover(); recovered != nil {
		app.Logger.Error("handler panicked", "panic", recovered)
		http.Error(w, "Server error", http.StatusInternalServerError)
	}
}

func (app *App) authorizeArtifact(w http.ResponseWriter, r *http.Request, repo, path string) (string, bool) {
	token, err := app.GetToken(r.Context(), "github-token")
	if err != nil {
		app.Logger.Error("failed to get github token", "error", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return "", false
	}

	parsed, err := ParsePathAsArtifact(path)
	if err != nil {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return "", false
	}

	if !IsValidMavenCoordinate(parsed.GroupID) || !IsValidMavenCoordinate(parsed.ArtifactID) ||
		(parsed.Version != "" && !IsValidPathSegment(parsed.Version)) ||
		(parsed.File != "" && !IsValidPathSegment(parsed.File)) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return "", false
	}

	if !IsNavPackage(parsed.GroupID) {
		http.Error(w, "GroupId does not start with an accepted prefix. Assuming a non NAV package", http.StatusNotFound)
		return "", false
	}

	isPublic, _, err := app.isPackagePublicCached(r.Context(), token, parsed, repo)
	if err != nil {
		app.Logger.Error("failed to get package visibility", "repo", repo, "path", path, "error", err)
	}
	if err != nil || !isPublic {
		http.Error(w, fmt.Sprintf("Could not get metadata for the Github repo %q in the navikt organization - it may not exist, or perhaps it's a private repository?", repo), http.StatusNotFound)
		return "", false
	}

	return token, true
}

func (app *App) handleUpstreamError(w http.ResponseWriter, statusCode int, artifactURL string) {
	switch statusCode {
	case http.StatusBadRequest:
		http.Error(w, "500 Server error: Could not authenticate with the Github Package Registry. This is probably due to a misconfiguration in Github Package Registry Mirror.", http.StatusInternalServerError)
	case http.StatusNotFound:
		http.Error(w, "404 Not Found: Looks like this package doesn't on Github Package Registry.", http.StatusNotFound)
	case http.StatusUnprocessableEntity:
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
	default:
		http.Error(w, fmt.Sprintf("Got an unexpected response from Github Package Registry %s", artifactURL), http.StatusInternalServerError)
	}
}

func (app *App) streamAndCache(w http.ResponseWriter, ctx context.Context, body io.Reader, cacheKey string) {
	cacheFile := app.Storage.File(cacheKey)
	writer, err := cacheFile.NewWriter(ctx)
	if err != nil {
		app.Logger.Error("failed to create cache writer", "cacheKey", cacheKey, "error", err)
		if _, copyErr := io.Copy(w, io.LimitReader(body, maxArtifactSize)); copyErr != nil {
			app.Logger.Error("failed streaming artifact", "cacheKey", cacheKey, "error", copyErr)
		}
		return
	}
	tee := io.TeeReader(io.LimitReader(body, maxArtifactSize), writer)
	_, copyErr := io.Copy(w, tee)
	closeErr := writer.Close()
	if copyErr != nil {
		app.Logger.Error("failed streaming artifact", "cacheKey", cacheKey, "error", copyErr)
		_ = cacheFile.Delete(ctx)
	} else if closeErr != nil {
		app.Logger.Error("failed writing cached artifact", "cacheKey", cacheKey, "error", closeErr)
		_ = cacheFile.Delete(ctx)
	}
}

func (app *App) existsInCache(ctx context.Context, cacheKey string) (bool, error) {
	file := app.Storage.File(cacheKey)
	exists, err := file.Exists(ctx)
	if err != nil {
		app.Logger.Error("failed checking cache existence", "cacheKey", cacheKey, "error", err)
		return false, err
	}

	if !exists {
		return false, nil
	}

	if !IsMavenMetadataXml(cacheKey) {
		return true, nil
	}

	metadata, err := file.GetMetadata(ctx)
	if err != nil {
		app.Logger.Error("failed getting cache metadata", "cacheKey", cacheKey, "error", err)
		return false, err
	}

	age := time.Since(metadata.TimeCreated)
	if age > 15*time.Minute {
		if err := file.Delete(ctx); err != nil {
			app.Logger.Error("failed deleting stale cache file", "cacheKey", cacheKey, "error", err)
			return false, err
		}
		return false, nil
	}

	return true, nil
}

func (app *App) handleSimple(w http.ResponseWriter, r *http.Request, repo, path string) {
	defer app.recoverPanic(w)

	if containsPathTraversal(path) || containsPathTraversal(repo) || !IsValidPathSegment(repo) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return
	}

	token, ok := app.authorizeArtifact(w, r, repo, path)
	if !ok {
		return
	}

	artifactURL := "https://maven.pkg.github.com/navikt/" + repo + "/" + path
	resp, err := app.Fetch(r.Context(), artifactURL, http.MethodGet, ModifiedHeadersWithAuth(token), nil)
	if err != nil {
		app.Logger.Error("failed to fetch artifact", "url", artifactURL, "error", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxArtifactSize {
		app.Logger.Error("artifact too large", "url", artifactURL, "size", resp.ContentLength)
		http.Error(w, "Server error", http.StatusBadGateway)
		return
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusMovedPermanently:
		for _, key := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified", "Location"} {
			if values := resp.Header.Values(key); len(values) > 0 {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
		}
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, io.LimitReader(resp.Body, maxArtifactSize)); err != nil {
			app.Logger.Error("failed streaming simple response", "url", artifactURL, "error", err)
		}
	default:
		app.handleUpstreamError(w, resp.StatusCode, artifactURL)
	}
}

func (app *App) handleCached(w http.ResponseWriter, r *http.Request, repo, path string) {
	defer app.recoverPanic(w)

	if containsPathTraversal(path) || containsPathTraversal(repo) || !IsValidPathSegment(repo) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return
	}

	cacheKey := "cache/" + repo + "/" + path
	app.Logger.Info("Handle cached artifact", "repo", repo, "path", path)

	exists, err := app.existsInCache(r.Context(), cacheKey)
	if err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	if exists {
		app.Logger.Info("serving from cache", "cacheKey", cacheKey)
		reader, err := app.Storage.File(cacheKey).NewReader(r.Context())
		if err != nil {
			app.Logger.Error("failed reading cached artifact", "cacheKey", cacheKey, "error", err)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}
		defer reader.Close()

		if _, err := io.Copy(w, reader); err != nil {
			app.Logger.Error("failed streaming cached artifact", "cacheKey", cacheKey, "error", err)
		}
		return
	}

	token, ok := app.authorizeArtifact(w, r, repo, path)
	if !ok {
		return
	}

	artifactURL := "https://maven.pkg.github.com/navikt/" + repo + "/" + path
	resp, err := app.Fetch(r.Context(), artifactURL, http.MethodGet, ModifiedHeadersWithAuth(token), nil)
	if err != nil {
		app.Logger.Error("failed to fetch artifact", "url", artifactURL, "error", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxArtifactSize {
		app.Logger.Error("artifact too large", "url", artifactURL, "size", resp.ContentLength)
		http.Error(w, "Server error", http.StatusBadGateway)
		return
	}

	switch resp.StatusCode {
	case http.StatusOK:
		app.streamAndCache(w, r.Context(), resp.Body, cacheKey)
	case http.StatusMovedPermanently, http.StatusFound:
		locationURL, err := resp.Location()
		if err != nil {
			app.Logger.Error("failed to get redirect location", "error", err)
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}
		if locationURL.Scheme != "https" {
			app.Logger.Error("redirect to non-HTTPS URL", "location", locationURL.String())
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}
		if !allowedRedirectHosts[locationURL.Hostname()] {
			app.Logger.Warn("redirect to unexpected host", "location", locationURL.String())
		}
		_ = resp.Body.Close()

		resp2, err := app.Fetch(r.Context(), locationURL.String(), http.MethodGet, http.Header{}, nil)
		if err != nil {
			app.Logger.Error("failed following redirect", "location", locationURL.String(), "error", err)
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}

		if resp2.ContentLength > maxArtifactSize {
			app.Logger.Error("redirected artifact too large", "location", locationURL.String(), "size", resp2.ContentLength)
			http.Error(w, "Server error", http.StatusBadGateway)
			return
		}

		app.streamAndCache(w, r.Context(), resp2.Body, cacheKey)
	default:
		app.handleUpstreamError(w, resp.StatusCode, artifactURL)
	}
}
