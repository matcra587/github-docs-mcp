package mcpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

func newHTTPFixture(t *testing.T, allowedOrigins []string) (*httptest.Server, *fixture) {
	t.Helper()

	f := newFixture(t)

	client, err := docs.NewClient(f.srv.URL, docs.WithRateLimit(1000))
	require.NoError(t, err)

	svc := docs.NewService(client, f.srv.URL, docs.ServiceConfig{
		IndexTTL:      time.Hour,
		PageTTL:       time.Hour,
		CacheMaxBytes: 1 << 20,
	})

	srv := New(svc, slog.New(slog.DiscardHandler), "test")

	ts := httptest.NewServer(srv.httpHandler(allowedOrigins))
	t.Cleanup(ts.Close)

	return ts, f
}

// postMCP posts body to /mcp and returns the status code and response body.
func postMCP(t *testing.T, url, origin, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", strings.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"httptest","version":"0"}}}`

func TestHTTPTransport(t *testing.T) {
	t.Parallel()

	t.Run("healthz is process only", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		ts, f := newHTTPFixture(t, nil)
		f.failing.Store(true) // origin down must not affect health

		resp, err := http.Get(ts.URL + "/healthz") //nolint:noctx // trivial test probe
		must.NoError(err)

		defer func() { _ = resp.Body.Close() }()

		is.Equal(http.StatusOK, resp.StatusCode, "healthz with origin down; must be 200")
	})

	t.Run("initialize and tool call over http", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		ts, _ := newHTTPFixture(t, nil)

		status, _ := postMCP(t, ts.URL, "", initializeBody)
		is.Equal(http.StatusOK, status, "initialize status")

		call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_doc","arguments":{"slug":"en/settings"}}}`

		_, raw := postMCP(t, ts.URL, "", call)
		is.Contains(raw, "settings body", "tool call over http failed")
	})

	t.Run("browser origin outside allow-list rejected", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		ts, _ := newHTTPFixture(t, nil)

		status, _ := postMCP(t, ts.URL, "https://evil.example.com", initializeBody)
		is.Equal(http.StatusForbidden, status, "expected 403 for foreign origin")
	})

	t.Run("allow-listed origin accepted", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		ts, _ := newHTTPFixture(t, []string{"https://app.example.com"})

		status, _ := postMCP(t, ts.URL, "https://app.example.com", initializeBody)
		is.Equal(http.StatusOK, status, "allow-listed origin rejected")
	})

	t.Run("localhost origin always accepted", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		ts, _ := newHTTPFixture(t, nil)

		status, _ := postMCP(t, ts.URL, "http://localhost:6274", initializeBody)
		is.Equal(http.StatusOK, status, "localhost origin rejected")
	})

	t.Run("oversized body rejected", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		ts, _ := newHTTPFixture(t, nil)

		big, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{
				"name":      "search_docs",
				"arguments": map[string]any{"query": strings.Repeat("x", maxRequestBytes+1024)},
			},
		})
		must.NoError(err)

		status, _ := postMCP(t, ts.URL, "", string(big))
		is.Contains([]int{http.StatusRequestEntityTooLarge, http.StatusBadRequest}, status, "oversized body not rejected")
	})

	t.Run("unknown path 404", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		ts, _ := newHTTPFixture(t, nil)

		resp, err := http.Get(ts.URL + "/nope") //nolint:noctx // trivial test probe
		must.NoError(err)

		defer func() { _ = resp.Body.Close() }()

		is.Equal(http.StatusNotFound, resp.StatusCode, "expected 404")
	})
}
