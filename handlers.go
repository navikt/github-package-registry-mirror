package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const defaultMaxArtifactSize = 512 << 20 // 512 MB

var allowedRedirectHosts = map[string]bool{
	"maven.pkg.github.com":                 true,
	"objects.githubusercontent.com":        true,
	"pkg-containers.githubusercontent.com": true,
}

var (
	errArtifactTooLarge      = errors.New("artifact exceeds size limit")
	errCouldNotFetchArtifact = errors.New("could not fetch the artifact from Github Package Registry")
)

type upstreamError struct {
	url        string
	statusCode int
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream %s returned status %d", e.url, e.statusCode)
}

type visibilityCacheEntry struct {
	isPublic  bool
	expiresAt time.Time
}

type App struct {
	Fetch           func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error)
	Storage         Storage
	Logger          *slog.Logger
	token           string
	maxArtifactSize int64
	visibilityMu    sync.RWMutex
	visibilityCache map[string]*visibilityCacheEntry
	inflight        singleflight.Group
}

func NewDefaultApp(token string, storage Storage, logger *slog.Logger) *App {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
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
		Storage:         storage,
		Logger:          logger,
		token:           token,
		maxArtifactSize: defaultMaxArtifactSize,
		visibilityCache: make(map[string]*visibilityCacheEntry),
	}
}

func (app *App) isPackagePublic(ctx context.Context, token string, parsed Artifact, repo string) (bool, error) {
	packageName := parsed.GroupID + "." + parsed.ArtifactID
	query := `query($name: [String!]!) { organization(login:"navikt"){ packages(first:1,names:$name){ nodes{ repository{ name isPrivate } } } } }`
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"name": []string{packageName}},
	})
	if err != nil {
		return false, fmt.Errorf("marshal GraphQL request: %w", err)
	}

	headers := http.Header{}
	headers.Set("Authorization", "bearer "+token)
	headers.Set("Accept", "application/vnd.github+json")

	resp, err := app.Fetch(ctx, "https://api.github.com/graphql", http.MethodPost, headers, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read GraphQL response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GitHub GraphQL API returned status %d", resp.StatusCode)
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
	if err := json.Unmarshal(data, &result); err != nil {
		return false, err
	}

	if len(result.Errors) > 0 {
		return false, fmt.Errorf("GitHub GraphQL error: %s", result.Errors[0].Message)
	}

	if result.Data == nil {
		app.Logger.Warn("GraphQL response has no data field", "package", packageName)
		return false, nil
	}

	if result.Data.Organization == nil {
		app.Logger.Warn("GraphQL response has no organization — token may lack org access", "package", packageName)
		return false, nil
	}

	nodes := result.Data.Organization.Packages.Nodes
	if len(nodes) == 0 {
		return false, nil
	}

	node := nodes[0]
	if node.Repository == nil {
		return false, nil
	}

	if !strings.EqualFold(node.Repository.Name, repo) {
		app.Logger.Warn("package repository mismatch", "expected", repo, "got", node.Repository.Name)
		return false, nil
	}

	return !node.Repository.IsPrivate, nil
}

const visibilityCacheTTL = 5 * time.Minute

func (app *App) isPackagePublicCached(ctx context.Context, token string, parsed Artifact, repo string) (bool, error) {
	cacheKey := repo + ":" + parsed.GroupID + "." + parsed.ArtifactID

	app.visibilityMu.RLock()
	entry, ok := app.visibilityCache[cacheKey]
	app.visibilityMu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.isPublic, nil
	}

	isPublic, err := app.isPackagePublic(ctx, token, parsed, repo)
	if err != nil {
		return false, err
	}

	now := time.Now()
	app.visibilityMu.Lock()
	for k, e := range app.visibilityCache {
		if now.After(e.expiresAt) {
			delete(app.visibilityCache, k)
		}
	}
	app.visibilityCache[cacheKey] = &visibilityCacheEntry{
		isPublic:  isPublic,
		expiresAt: now.Add(visibilityCacheTTL),
	}
	app.visibilityMu.Unlock()

	return isPublic, nil
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

	isPublic, err := app.isPackagePublicCached(r.Context(), app.token, parsed, repo)
	if err != nil {
		app.Logger.Error("failed to get package visibility", "repo", repo, "path", path, "error", err)
	}
	if err != nil || !isPublic {
		http.Error(w, fmt.Sprintf("Could not get metadata for the Github repo %q in the navikt organization - it may not exist, or perhaps it's a private repository?", repo), http.StatusNotFound)
		return "", false
	}

	return app.token, true
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

func (app *App) writeToCache(ctx context.Context, body io.Reader, cacheKey string) error {
	cacheFile := app.Storage.File(cacheKey)
	writer, err := cacheFile.NewWriter(ctx)
	if err != nil {
		return fmt.Errorf("create cache writer for %s: %w", cacheKey, err)
	}

	lr := &io.LimitedReader{R: body, N: app.maxArtifactSize}
	_, copyErr := io.Copy(writer, lr)
	closeErr := writer.Close()

	if copyErr != nil {
		_ = cacheFile.Delete(context.Background())
		return fmt.Errorf("write to cache %s: %w", cacheKey, copyErr)
	}
	if closeErr != nil {
		_ = cacheFile.Delete(context.Background())
		return fmt.Errorf("close cache writer %s: %w", cacheKey, closeErr)
	}

	// Detect body overflow: artifact was larger than maxArtifactSize
	if lr.N == 0 {
		var probe [1]byte
		if n, _ := body.Read(probe[:]); n > 0 {
			_ = cacheFile.Delete(context.Background())
			return fmt.Errorf("%w: body exceeds %d bytes", errArtifactTooLarge, app.maxArtifactSize)
		}
	}

	return nil
}

func (app *App) fetchAndCache(ctx context.Context, repo, path, cacheKey string) error {
	artifactURL := "https://maven.pkg.github.com/navikt/" + repo + "/" + path

	resp, err := app.Fetch(ctx, artifactURL, http.MethodGet, ModifiedHeadersWithAuth(app.token), nil)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", artifactURL, err)
	}
	defer resp.Body.Close()

	if resp.ContentLength > app.maxArtifactSize {
		return fmt.Errorf("%w: %s Content-Length %d", errArtifactTooLarge, artifactURL, resp.ContentLength)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return app.writeToCache(ctx, resp.Body, cacheKey)

	case http.StatusMovedPermanently, http.StatusFound:
		locationURL, err := resp.Location()
		if err != nil {
			return fmt.Errorf("%w: %v", errCouldNotFetchArtifact, err)
		}
		if locationURL.Scheme != "https" {
			return fmt.Errorf("%w: redirect to non-HTTPS URL: %s", errCouldNotFetchArtifact, locationURL)
		}
		if !allowedRedirectHosts[locationURL.Hostname()] {
			app.Logger.Warn("redirect to unexpected host", "location", locationURL.String())
		}
		_ = resp.Body.Close()

		resp2, err := app.Fetch(ctx, locationURL.String(), http.MethodGet, http.Header{}, nil)
		if err != nil {
			return fmt.Errorf("%w: follow redirect to %s: %v", errCouldNotFetchArtifact, locationURL, err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: redirect target %s returned status %d", errCouldNotFetchArtifact, locationURL, resp2.StatusCode)
		}

		if resp2.ContentLength > app.maxArtifactSize {
			return fmt.Errorf("%w: %s Content-Length %d", errArtifactTooLarge, locationURL, resp2.ContentLength)
		}

		return app.writeToCache(ctx, resp2.Body, cacheKey)

	default:
		return &upstreamError{url: artifactURL, statusCode: resp.StatusCode}
	}
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

	if resp.ContentLength > app.maxArtifactSize {
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
		if _, err := io.Copy(w, io.LimitReader(resp.Body, app.maxArtifactSize)); err != nil {
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

	if !exists {
		_, ok := app.authorizeArtifact(w, r, repo, path)
		if !ok {
			return
		}

		_, sfErr, _ := app.inflight.Do(cacheKey, func() (any, error) {
			return nil, app.fetchAndCache(r.Context(), repo, path, cacheKey)
		})
		if sfErr != nil {
			if errors.Is(sfErr, errArtifactTooLarge) {
				app.Logger.Error("artifact too large", "cacheKey", cacheKey, "error", sfErr)
				http.Error(w, "Server error", http.StatusBadGateway)
				return
			}
			var ue *upstreamError
			if errors.As(sfErr, &ue) {
				app.handleUpstreamError(w, ue.statusCode, ue.url)
				return
			}
			if errors.Is(sfErr, errCouldNotFetchArtifact) {
				app.Logger.Error("could not fetch artifact", "cacheKey", cacheKey, "error", sfErr)
				http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
				return
			}
			app.Logger.Error("failed to fetch and cache artifact", "cacheKey", cacheKey, "error", sfErr)
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}
	}

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
}
