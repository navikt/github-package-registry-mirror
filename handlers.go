package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var allowedRedirectHosts = map[string]bool{
	"maven.pkg.github.com":                 true,
	"objects.githubusercontent.com":        true,
	"pkg-containers.githubusercontent.com": true,
}

type App struct {
	Fetch    func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error)
	GetToken func(ctx context.Context, name string) (string, error)
	Storage  Storage
	Logger   *slog.Logger
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
		GetToken: realGetToken(storage),
		Storage:  storage,
		Logger:   logger,
	}
}

func realGetToken(storage Storage) func(ctx context.Context, name string) (string, error) {
	return func(ctx context.Context, tokenName string) (string, error) {
		if _, err := os.Stat(tokenName); err == nil {
			data, err := os.ReadFile(tokenName)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(data)), nil
		}

		reader, err := storage.File("credentials/" + tokenName).NewReader(ctx)
		if err != nil {
			return "", err
		}
		defer reader.Close()

		data, err := io.ReadAll(reader)
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(string(data)), nil
	}
}

func (app *App) isPackagePublic(ctx context.Context, token string, parsed Artifact) (isPublic bool, found bool, err error) {
	packageName := parsed.GroupID + "." + parsed.ArtifactID
	query := `query($name: [String!]!) { organization(login:"navikt"){ packages(first:1,names:$name){ nodes{ repository{ isPrivate } } } } }`
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

	type graphQLResponse struct {
		Data *struct {
			Organization *struct {
				Packages struct {
					Nodes []struct {
						Repository *struct {
							IsPrivate bool `json:"isPrivate"`
						} `json:"repository"`
					} `json:"nodes"`
				} `json:"packages"`
			} `json:"organization"`
		} `json:"data"`
	}

	var result graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, false, err
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
	if result.Data.Organization.Packages.Nodes[0].Repository == nil {
		return false, false, nil
	}
	if result.Data.Organization.Packages.Nodes[0].Repository.IsPrivate {
		return false, true, nil
	}

	return true, true, nil
}

func containsPathTraversal(path string) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return true
		}
	}
	return false
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
	defer func() {
		if recovered := recover(); recovered != nil {
			http.Error(w, "Server error", http.StatusInternalServerError)
		}
	}()

	if containsPathTraversal(path) || containsPathTraversal(repo) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return
	}

	token, err := app.GetToken(r.Context(), "github-token")
	if err != nil {
		app.Logger.Error("failed to get github token", "error", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	parsed, err := ParsePathAsArtifact(path)
	if err != nil {
		app.Logger.Error("failed to parse artifact path", "path", path, "error", err)
	}

	if !IsValidMavenCoordinate(parsed.GroupID) || !IsValidMavenCoordinate(parsed.ArtifactID) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return
	}

	if !IsNavPackage(parsed.GroupID) {
		http.Error(w, "GroupId does not start with an accepted prefix. Assuming a non NAV package", http.StatusNotFound)
		return
	}

	isPublic, found, err := app.isPackagePublic(r.Context(), token, parsed)
	if err != nil {
		app.Logger.Error("failed to get package visibility", "repo", repo, "path", path, "error", err)
	}
	if err != nil || !found || !isPublic {
		http.Error(w, fmt.Sprintf("Could not get metadata for the Github repo %q in the navikt organization - it may not exist, or perhaps it's a private repository?", repo), http.StatusNotFound)
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
		if _, err := io.Copy(w, resp.Body); err != nil {
			app.Logger.Error("failed streaming simple response", "url", artifactURL, "error", err)
		}
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

func (app *App) handleCached(w http.ResponseWriter, r *http.Request, repo, path string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			http.Error(w, "Server error", http.StatusInternalServerError)
		}
	}()

	if containsPathTraversal(path) || containsPathTraversal(repo) {
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

	token, err := app.GetToken(r.Context(), "github-token")
	if err != nil {
		app.Logger.Error("failed to get github token", "error", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	parsed, err := ParsePathAsArtifact(path)
	if err != nil {
		app.Logger.Error("failed to parse artifact path", "path", path, "error", err)
	}

	if !IsValidMavenCoordinate(parsed.GroupID) || !IsValidMavenCoordinate(parsed.ArtifactID) {
		http.Error(w, "422: The file path you provided was probably invalid (not a valid Maven repository path)", http.StatusUnprocessableEntity)
		return
	}

	if !IsNavPackage(parsed.GroupID) {
		http.Error(w, "GroupId does not start with an accepted prefix. Assuming a non NAV package", http.StatusNotFound)
		return
	}

	isPublic, _, err := app.isPackagePublic(r.Context(), token, parsed)
	if err != nil {
		app.Logger.Error("failed to get package visibility", "repo", repo, "path", path, "error", err)
	}
	if err != nil || !isPublic {
		http.Error(w, fmt.Sprintf("Could not get metadata for the Github repo %q in the navikt organization - it may not exist, or perhaps it's a private repository?", repo), http.StatusNotFound)
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

	switch resp.StatusCode {
	case http.StatusOK:
		cacheFile := app.Storage.File(cacheKey)
		writer := cacheFile.NewWriter(r.Context())
		tee := io.TeeReader(resp.Body, writer)
		_, copyErr := io.Copy(w, tee)
		closeErr := writer.Close()
		if copyErr != nil {
			app.Logger.Error("failed streaming upstream artifact", "url", artifactURL, "error", copyErr)
			_ = cacheFile.Delete(r.Context())
		} else if closeErr != nil {
			app.Logger.Error("failed writing cached artifact", "cacheKey", cacheKey, "error", closeErr)
			_ = cacheFile.Delete(r.Context())
		}
	case http.StatusMovedPermanently, http.StatusFound:
		location := resp.Header.Get("Location")
		locationURL, err := url.Parse(location)
		if err != nil || !allowedRedirectHosts[locationURL.Hostname()] {
			app.Logger.Error("redirect to disallowed host", "location", location)
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}
		_ = resp.Body.Close()

		resp2, err := app.Fetch(r.Context(), location, http.MethodGet, http.Header{}, nil)
		if err != nil {
			app.Logger.Error("failed following redirect", "location", location, "error", err)
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			http.Error(w, "Could not fetch the artifact from Github Package Registry.", http.StatusInternalServerError)
			return
		}

		cacheFile := app.Storage.File(cacheKey)
		writer := cacheFile.NewWriter(r.Context())
		tee := io.TeeReader(resp2.Body, writer)
		_, copyErr := io.Copy(w, tee)
		closeErr := writer.Close()
		if copyErr != nil {
			app.Logger.Error("failed streaming redirected artifact", "location", location, "error", copyErr)
			_ = cacheFile.Delete(r.Context())
		} else if closeErr != nil {
			app.Logger.Error("failed writing redirected cached artifact", "cacheKey", cacheKey, "error", closeErr)
			_ = cacheFile.Delete(r.Context())
		}
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
