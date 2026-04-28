package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockResponse is a fake HTTP response for mock fetcher.
type mockResponse struct {
	status  int
	body    string
	headers http.Header
}

// mockFetch creates a fetch function that returns responses in sequence.
// Panics if called more times than responses provided.
func mockFetch(responses ...*mockResponse) func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
	calls := 0
	return func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
		if calls >= len(responses) {
			panic("unexpected fetch call #" + fmt.Sprint(calls+1) + " for url: " + url)
		}
		r := responses[calls]
		calls++
		resp := &http.Response{
			StatusCode: r.status,
			Header:     r.headers,
			Body:       io.NopCloser(strings.NewReader(r.body)),
		}
		if resp.Header == nil {
			resp.Header = http.Header{}
		}
		return resp, nil
	}
}

// publicGraphQLResponse creates a fake GraphQL response indicating a public package.
func publicGraphQLResponse() *mockResponse {
	return &mockResponse{
		status: 200,
		body:   `{"data":{"organization":{"packages":{"nodes":[{"repository":{"name":"tjenestespesifikasjoner","isPrivate":false}}]}}}}`,
	}
}

func privateGraphQLResponse() *mockResponse {
	return &mockResponse{
		status: 200,
		body:   `{"data":{"organization":{"packages":{"nodes":[{"repository":{"name":"tjenestespesifikasjoner","isPrivate":true}}]}}}}`,
	}
}

// packageNotFoundGraphQLResponse creates a fake GraphQL response with empty nodes.
func packageNotFoundGraphQLResponse() *mockResponse {
	return &mockResponse{
		status: 200,
		body:   `{"data":{"organization":{"packages":{"nodes":[]}}}}`,
	}
}

// artifactResponse creates a fake artifact HTTP response.
func artifactResponse(status int, body string, headers ...http.Header) *mockResponse {
	h := http.Header{"Content-Type": {"application/octet-stream"}}
	for _, extra := range headers {
		maps.Copy(h, extra)
	}
	return &mockResponse{status: status, body: body, headers: h}
}

// mockStorage creates a fake Storage implementation.
type mockStorageState struct {
	exists      bool
	timeCreated time.Time
	content     string
	writtenData string
	deleted     bool
}

type mockStorageImpl struct {
	state *mockStorageState
}

func (m *mockStorageImpl) File(name string) FileHandle {
	return &mockFileHandle{state: m.state}
}

func (m *mockStorageImpl) Close() error { return nil }

type mockFileHandle struct {
	state *mockStorageState
}

func (f *mockFileHandle) Exists(ctx context.Context) (bool, error) {
	return f.state.exists, nil
}

func (f *mockFileHandle) GetMetadata(ctx context.Context) (FileMetadata, error) {
	tc := f.state.timeCreated
	if tc.IsZero() {
		tc = time.Now()
	}
	return FileMetadata{TimeCreated: tc}, nil
}

func (f *mockFileHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.state.content)), nil
}

func (f *mockFileHandle) NewWriter(ctx context.Context) (io.WriteCloser, error) {
	return &mockWriter{state: f.state}, nil
}

func (f *mockFileHandle) Delete(ctx context.Context) error {
	f.state.deleted = true
	return nil
}

type mockWriter struct {
	state *mockStorageState
	buf   bytes.Buffer
}

func (w *mockWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

func (w *mockWriter) Close() error {
	w.state.writtenData += w.buf.String()
	return nil
}

// newMockStorage creates a mockStorage with given options.
func newMockStorage(exists bool, timeCreated time.Time, content string) (*mockStorageImpl, *mockStorageState) {
	state := &mockStorageState{exists: exists, timeCreated: timeCreated, content: content}
	return &mockStorageImpl{state: state}, state
}

// newTestServer creates an httptest.Server with an App using mock dependencies.
func newTestServer(fetch func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error), getToken func(ctx context.Context, name string) (string, error), storage Storage) *httptest.Server {
	logger := slog.New(slog.DiscardHandler)
	app := &App{
		Fetch:           fetch,
		GetToken:        getToken,
		Storage:         storage,
		Logger:          logger,
		visibilityCache: make(map[string]*visibilityCacheEntry),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simple/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		app.handleSimple(w, r, r.PathValue("repo"), r.PathValue("path"))
	})
	mux.HandleFunc("GET /cached/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		app.handleCached(w, r, r.PathValue("repo"), r.PathValue("path"))
	})
	return httptest.NewServer(mux)
}

// tokenFn returns a GetToken function that always returns the given token.
func tokenFn(token string) func(ctx context.Context, name string) (string, error) {
	return func(ctx context.Context, name string) (string, error) {
		return token, nil
	}
}

// errorTokenFn returns a GetToken function that always returns an error.
func errorTokenFn() func(ctx context.Context, name string) (string, error) {
	return func(ctx context.Context, name string) (string, error) {
		return "", fmt.Errorf("token error: boom")
	}
}

const (
	simplePath       = "/simple/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar"
	nonNavSimplePath = "/simple/commons-lang/org/apache/commons/lang3/3.0/lang3-3.0.jar"
)

func TestHandleSimple(t *testing.T) {
	storage, _ := newMockStorage(false, time.Time{}, "")

	t.Run("returns 200 and streams artifact body for public packages", func(t *testing.T) {
		token := "secret-token"
		var fetchCalls []struct {
			url     string
			headers http.Header
			body    string
		}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(200, "artifact-data"),
		)
		fetchWrapper := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			var bodyBytes []byte
			if body != nil {
				bodyBytes, _ = io.ReadAll(body)
				body = bytes.NewReader(bodyBytes)
			}
			fetchCalls = append(fetchCalls, struct {
				url     string
				headers http.Header
				body    string
			}{url, headers.Clone(), string(bodyBytes)})
			return fetch(ctx, url, method, headers, body)
		}
		srv := newTestServer(fetchWrapper, tokenFn(token), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "artifact-data" {
			t.Errorf("body = %q, want %q", body, "artifact-data")
		}
		if len(fetchCalls) != 2 {
			t.Errorf("fetch calls = %d, want 2", len(fetchCalls))
		}
		gqlAuth := fetchCalls[0].headers.Get("Authorization")
		if gqlAuth != "bearer "+token {
			t.Errorf("graphql auth = %q, want %q", gqlAuth, "bearer "+token)
		}
		var gqlPayload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal([]byte(fetchCalls[0].body), &gqlPayload); err != nil {
			t.Fatalf("failed to decode graphql payload: %v", err)
		}
		if gqlPayload.Query == "" {
			t.Error("graphql query should not be empty")
		}
		nameValues, ok := gqlPayload.Variables["name"].([]any)
		if !ok || len(nameValues) != 1 || nameValues[0] != "no.nav.foo.bar" {
			t.Errorf("graphql variables[name] = %#v, want [\"no.nav.foo.bar\"]", gqlPayload.Variables["name"])
		}
		artAuth := fetchCalls[1].headers.Get("Authorization")
		if artAuth != TokenAuthHeader(token) {
			t.Errorf("artifact auth = %q, want %q", artAuth, TokenAuthHeader(token))
		}
		if len(fetchCalls[1].headers) != 1 {
			t.Errorf("artifact headers = %#v, want only authorization header", fetchCalls[1].headers)
		}
	})

	t.Run("forwards 301 redirect responses from the artifact endpoint", func(t *testing.T) {
		locationHeaders := http.Header{"Location": {"https://example.test/artifact.jar"}}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(301, "", locationHeaders),
		)
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := client.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 301 {
			t.Errorf("status = %d, want 301", resp.StatusCode)
		}
		if resp.Header.Get("Location") != "https://example.test/artifact.jar" {
			t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), "https://example.test/artifact.jar")
		}
	})

	t.Run("returns 404 for non-NAV packages", func(t *testing.T) {
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called for non-NAV packages, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + nonNavSimplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "accepted prefix") {
			t.Errorf("body %q should contain 'accepted prefix'", body)
		}
	})

	t.Run("returns 404 for private packages", func(t *testing.T) {
		var artifactCalled bool
		fetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			if strings.Contains(url, "maven.pkg.github.com") {
				artifactCalled = true
			}
			return mockFetch(privateGraphQLResponse())(ctx, url, method, headers, body)
		}
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "private repository") {
			t.Errorf("body %q should contain 'private repository'", body)
		}
		if artifactCalled {
			t.Error("artifact endpoint should not be called for private packages")
		}
	})

	t.Run("returns 404 when package metadata is not found", func(t *testing.T) {
		fetch := mockFetch(packageNotFoundGraphQLResponse())
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "may not exist") {
			t.Errorf("body %q should contain 'may not exist'", body)
		}
	})

	t.Run("maps upstream 400 to client 500", func(t *testing.T) {
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(400, "bad credentials"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Could not authenticate") {
			t.Errorf("body %q should contain 'Could not authenticate'", body)
		}
	})

	t.Run("maps upstream 404 to client 404 (bug fix)", func(t *testing.T) {
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(404, "missing artifact"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404 (BUG FIX)", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Looks like this package") {
			t.Errorf("body %q should contain 'Looks like this package'", body)
		}
	})

	t.Run("preserves upstream 422 responses", func(t *testing.T) {
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(422, "invalid path"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 422 {
			t.Errorf("status = %d, want 422", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "not a valid Maven repository path") {
			t.Errorf("body %q should contain 'not a valid Maven repository path'", body)
		}
	})

	t.Run("maps unexpected upstream statuses to client 500", func(t *testing.T) {
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(429, "rate limited"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Got an unexpected response") {
			t.Errorf("body %q should contain 'Got an unexpected response'", body)
		}
	})

	t.Run("returns 500 when token lookup throws", func(t *testing.T) {
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called when token lookup fails, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, errorTokenFn(), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + simplePath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if strings.TrimSpace(string(body)) != "Server error" {
			t.Errorf("body = %q, want 'Server error'", body)
		}
	})

	t.Run("returns 422 for invalid maven coordinates in simple path", func(t *testing.T) {
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called for invalid coordinates, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/simple/tjenestespesifikasjoner/no/nav/foo/bar!/1.0/bar-1.0.jar")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 422 {
			t.Errorf("status = %d, want 422", resp.StatusCode)
		}
	})
}

const (
	cachedPath         = "/cached/tjenestespesifikasjoner/no/nav/foo/bar/1.0/bar-1.0.jar"
	cachedMetadataPath = "/cached/tjenestespesifikasjoner/no/nav/foo/bar/maven-metadata.xml"
	nonNavCachedPath   = "/cached/commons-lang/org/apache/commons/lang3/3.0/lang3-3.0.jar"
)

func TestHandleCached(t *testing.T) {
	t.Run("cache miss upstream 200 returns 200 and stores artifact", func(t *testing.T) {
		storage, state := newMockStorage(false, time.Time{}, "")
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(200, "artifact-bytes"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "artifact-bytes" {
			t.Errorf("body = %q, want 'artifact-bytes'", body)
		}
		if state.writtenData != "artifact-bytes" {
			t.Errorf("stored = %q, want 'artifact-bytes'", state.writtenData)
		}
	})

	t.Run("cache miss upstream 301 redirect follows target and returns 200", func(t *testing.T) {
		storage, state := newMockStorage(false, time.Time{}, "")
		redirectHeaders := http.Header{"Location": {"https://objects.githubusercontent.com/artifact"}}
		var fetchCalls []struct {
			url     string
			headers http.Header
		}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(301, "", redirectHeaders),
			artifactResponse(200, "redirected-301-body"),
		)
		fetchWrapper := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			fetchCalls = append(fetchCalls, struct {
				url     string
				headers http.Header
			}{url, headers.Clone()})
			return fetch(ctx, url, method, headers, body)
		}
		srv := newTestServer(fetchWrapper, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "redirected-301-body" {
			t.Errorf("body = %q, want 'redirected-301-body'", body)
		}
		if state.writtenData != "redirected-301-body" {
			t.Errorf("stored = %q, want 'redirected-301-body'", state.writtenData)
		}
		if len(fetchCalls) != 3 {
			t.Errorf("fetch calls = %d, want 3", len(fetchCalls))
		}
		if got := fetchCalls[2].headers.Get("Authorization"); got != "" {
			t.Errorf("redirect auth header = %q, want empty", got)
		}
		if len(fetchCalls[2].headers) != 0 {
			t.Errorf("redirect headers = %#v, want empty headers", fetchCalls[2].headers)
		}
	})

	t.Run("cache miss redirect to unknown host logs warning but follows redirect", func(t *testing.T) {
		storage, state := newMockStorage(false, time.Time{}, "")
		redirectHeaders := http.Header{"Location": {"https://evil.example/artifact"}}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(302, "", redirectHeaders),
			artifactResponse(200, "redirected-body"),
		)
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "redirected-body" {
			t.Errorf("body = %q, want %q", body, "redirected-body")
		}
		if state.writtenData != "redirected-body" {
			t.Errorf("stored = %q, want %q", state.writtenData, "redirected-body")
		}
	})

	t.Run("cache miss upstream 302 redirect follows target and returns 200", func(t *testing.T) {
		storage, state := newMockStorage(false, time.Time{}, "")
		redirectHeaders := http.Header{"Location": {"https://objects.githubusercontent.com/artifact"}}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(302, "", redirectHeaders),
			artifactResponse(200, "redirected-302-body"),
		)
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "redirected-302-body" {
			t.Errorf("body = %q, want 'redirected-302-body'", body)
		}
		if state.writtenData != "redirected-302-body" {
			t.Errorf("stored = %q, want 'redirected-302-body'", state.writtenData)
		}
	})

	t.Run("cache miss redirect target failure returns 500", func(t *testing.T) {
		storage, _ := newMockStorage(false, time.Time{}, "")
		redirectHeaders := http.Header{"Location": {"https://objects.githubusercontent.com/artifact"}}
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(302, "", redirectHeaders),
			artifactResponse(500, "server failure"),
		)
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Could not fetch the artifact") {
			t.Errorf("body %q should contain 'Could not fetch the artifact'", body)
		}
	})

	t.Run("cache hit serves from storage without GitHub fetches", func(t *testing.T) {
		storage, _ := newMockStorage(true, time.Time{}, "cached-hit-body")
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called on cache hit, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "cached-hit-body" {
			t.Errorf("body = %q, want 'cached-hit-body'", body)
		}
	})

	t.Run("cache hit maven-metadata.xml fresh TTL under 15 minutes serves cached", func(t *testing.T) {
		fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
		storage, state := newMockStorage(true, fiveMinutesAgo, "fresh-metadata-body")
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called for fresh cache, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedMetadataPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "fresh-metadata-body" {
			t.Errorf("body = %q, want 'fresh-metadata-body'", body)
		}
		if state.deleted {
			t.Error("fresh cache should not be deleted")
		}
	})

	t.Run("cache hit maven-metadata.xml stale TTL over 15 minutes deletes and refetches", func(t *testing.T) {
		twentyMinutesAgo := time.Now().Add(-20 * time.Minute)
		storage, state := newMockStorage(true, twentyMinutesAgo, "stale-metadata")
		fetch := mockFetch(
			publicGraphQLResponse(),
			artifactResponse(200, "refetched-metadata-body"),
		)
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedMetadataPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if string(body) != "refetched-metadata-body" {
			t.Errorf("body = %q, want 'refetched-metadata-body'", body)
		}
		if !state.deleted {
			t.Error("stale cache should have been deleted")
		}
		if state.writtenData != "refetched-metadata-body" {
			t.Errorf("stored = %q, want 'refetched-metadata-body'", state.writtenData)
		}
	})

	t.Run("non-NAV package returns 404", func(t *testing.T) {
		storage, _ := newMockStorage(false, time.Time{}, "")
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called for non-NAV packages, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + nonNavCachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "non NAV package") {
			t.Errorf("body %q should contain 'non NAV package'", body)
		}
	})

	t.Run("upstream 404 maps to 404", func(t *testing.T) {
		storage, _ := newMockStorage(false, time.Time{}, "")
		fetch := mockFetch(publicGraphQLResponse(), artifactResponse(404, "missing"))
		srv := newTestServer(fetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 404 {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "404 Not Found") {
			t.Errorf("body %q should contain '404 Not Found'", body)
		}
	})

	t.Run("returns 500 when token lookup throws", func(t *testing.T) {
		storage, _ := newMockStorage(false, time.Time{}, "")
		srv := newTestServer(mockFetch(), errorTokenFn(), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + cachedPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 500 {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if strings.TrimSpace(string(body)) != "Server error" {
			t.Errorf("body = %q, want 'Server error'", body)
		}
	})

	t.Run("returns 422 for invalid maven coordinates in cached path", func(t *testing.T) {
		storage, _ := newMockStorage(false, time.Time{}, "")
		panicFetch := func(ctx context.Context, url string, method string, headers http.Header, body io.Reader) (*http.Response, error) {
			t.Errorf("fetch should not be called for invalid coordinates, called with %q", url)
			return nil, fmt.Errorf("unexpected call")
		}
		srv := newTestServer(panicFetch, tokenFn("token"), storage)
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/cached/tjenestespesifikasjoner/no/nav/foo/bar!/1.0/bar-1.0.jar")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 422 {
			t.Errorf("status = %d, want 422", resp.StatusCode)
		}
	})
}
