package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(
		m,
		// A detached fetch outlives the caller that started it by design, so at
		// the end of a run it can outlive the test binary too. Both are bounded
		// by fetchTimeout: outliving, not leaked.
		//
		// Two distinct goroutines, and each has flaked on its own:
		//
		//   fetchShared.func1       the singleflight worker, running the fetch
		//                           on a context.WithoutCancel so one caller's
		//                           cancellation cannot fail the others
		//   refreshIndexAsync.func1 the fire-and-forget index refresh, blocked
		//                           in fetchShared's select waiting on that
		//                           worker
		//
		// Ignoring either alone leaves the other able to fail the package.
		goleak.IgnoreAnyFunction("github.com/matcra587/github-docs-mcp/internal/docs.fetchShared[...].func1"),
		goleak.IgnoreAnyFunction("github.com/matcra587/github-docs-mcp/internal/docs.(*Service).refreshIndexAsync.func1"),
	)
}

type fixture struct {
	srv          *httptest.Server
	hits         atomic.Int32
	failing      atomic.Bool
	garbageIndex atomic.Bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)

		if f.failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		switch r.URL.Path {
		case "/llms.txt":
			if f.garbageIndex.Load() {
				// Plain-text prose with no entries: drifted catalogue rather
				// than an interstitial. An HTML body is rejected earlier, by
				// the client's content-type guard.
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write([]byte("# GitHub Docs\n\nno list entries here\n"))

				return
			}

			_, _ = fmt.Fprintf(w, "- [Hooks guide](%s/en/hooks-guide.md): Automate with hooks\n- [Settings](%s/en/settings.md): Configure behaviour\n- [Ghost](%s/en/ghost.md): Removed upstream\n", f.srv.URL, f.srv.URL, f.srv.URL)
		case "/en/hooks-guide.md":
			_, _ = w.Write([]byte("# Hooks\n\n## Quickstart\n\nquickstart body\n\n## Reference\n\nreference body\n"))
		case "/en/settings.md":
			_, _ = w.Write([]byte("# Settings\n\nsettings body\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// newSession builds a server and a connected client over in-memory transports.
func newSession(t *testing.T, f *fixture) (*mcp.ClientSession, *Server) {
	t.Helper()

	return newSessionCfg(t, f, docs.ServiceConfig{
		IndexTTL:      time.Hour,
		PageTTL:       time.Hour,
		CacheMaxBytes: 1 << 20,
	})
}

func newSessionCfg(t *testing.T, f *fixture, cfg docs.ServiceConfig) (*mcp.ClientSession, *Server) {
	t.Helper()

	client, err := docs.NewClient(f.srv.URL, docs.WithRateLimit(1000))
	require.NoError(t, err)

	svc := docs.NewService(client, f.srv.URL, cfg)

	srv := New(svc, slog.New(slog.DiscardHandler), "test")

	// A linked pair of in-memory transports runs the real client and server
	// session machinery, schema validation and all, without a socket.
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.mcp.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)

	t.Cleanup(func() { _ = serverSession.Close() })

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "itest", Version: "0"}, nil)

	cs, err := mcpClient.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)

	t.Cleanup(func() { _ = cs.Close() })

	return cs, srv
}

func callTool(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	res, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "CallTool %s", name)

	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()

	require.NotEmpty(t, res.Content, "empty content")

	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected text content, got %T", res.Content[0])

	return tc.Text
}

func TestIntegration(t *testing.T) {
	t.Parallel()

	t.Run("tools list has three annotated tools", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		c, _ := newSession(t, newFixture(t))

		res, err := c.ListTools(t.Context(), nil)
		must.NoError(err)

		must.Len(res.Tools, 3, "expected 3 tools")

		for _, tool := range res.Tools {
			must.NotNil(tool.Annotations, "tool %s missing annotations", tool.Name)

			is.True(tool.Annotations.ReadOnlyHint, "tool %s missing readOnlyHint", tool.Name)
			is.NotEmpty(tool.Annotations.Title, "tool %s missing title", tool.Name)
		}
	})

	t.Run("negotiates the current protocol version", func(t *testing.T) {
		t.Parallel()

		c, _ := newSession(t, newFixture(t))

		// The reason for being on the official SDK: it tracks the spec. If a
		// dependency bump silently drops the negotiated version, that is a
		// regression worth failing on.
		assert.New(t).Equal("2026-07-28", c.InitializeResult().ProtocolVersion)
	})

	t.Run("advertises no deprecated capabilities", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		c, _ := newSession(t, newFixture(t))

		caps := c.InitializeResult().Capabilities
		must.NotNil(caps)

		// Logging, roots and sampling are deprecated as of 2026-07-28
		// (SEP-2577). This server implements none of them, and the SDK would
		// otherwise advertise logging by default.
		is.Nil(caps.Logging, "logging is deprecated and not implemented here") //nolint:staticcheck // reading the deprecated field is how its absence is asserted
		is.Nil(caps.Prompts, "no prompts are served")
		is.Nil(caps.Resources, "no resources are served")

		must.NotNil(caps.Tools, "tools capability missing")
		is.False(caps.Tools.ListChanged, "the tool list is fixed at construction; claiming otherwise invites needless re-fetches")
	})

	t.Run("tools list carries cache hints", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		c, _ := newSession(t, newFixture(t))

		res, err := c.ListTools(t.Context(), nil)
		must.NoError(err)

		// SEP-2549: without a TTL a client must re-fetch on every reconnect.
		is.Equal(int(toolListTTL.Milliseconds()), res.TTLMs, "tools/list should carry a TTL hint")
		is.Equal("public", res.CacheScope, "the tool list is identical for every caller")

		// A cache hint is only useful if the payload is stable. The spec asks
		// for a deterministic order for exactly this reason; the SDK sorts by
		// name, and caching above depends on that staying true.
		names := make([]string, 0, len(res.Tools))
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}

		is.Equal([]string{"get_doc", "list_docs", "search_docs"}, names, "tools/list must be deterministically ordered")
	})

	t.Run("required arguments are enforced by the input schema", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		c, _ := newSession(t, newFixture(t))

		// query/slug carry no omitempty, so the SDK rejects the call before
		// the handler runs, so no hand-written argument checks needed.
		for _, tc := range []struct{ tool, missing string }{
			{"search_docs", "query"},
			{"get_doc", "slug"},
		} {
			res, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: map[string]any{}})
			must.NoError(err, "%s: argument problems are tool results, not protocol errors", tc.tool)

			if is.NotNil(res, "%s returned no result", tc.tool) {
				is.True(res.IsError, "%s accepted a call missing %q", tc.tool, tc.missing)
				is.Contains(textOf(t, res), tc.missing, "%s error should name the missing argument", tc.tool)
			}
		}
	})

	t.Run("tool schemas mark the right arguments required", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		c, _ := newSession(t, newFixture(t))

		res, err := c.ListTools(t.Context(), nil)
		must.NoError(err)

		want := map[string][]any{
			"list_docs":   nil,
			"search_docs": {"query"},
			"get_doc":     {"slug"},
		}

		for _, tool := range res.Tools {
			schema, ok := tool.InputSchema.(map[string]any)
			must.True(ok, "tool %s: unexpected schema type %T", tool.Name, tool.InputSchema)

			var required []any
			if r, present := schema["required"]; present {
				required, _ = r.([]any)
			}

			is.Equal(want[tool.Name], required, "tool %s required fields", tool.Name)
		}
	})

	t.Run("list search get end to end", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t))

		list := textOf(t, callTool(t, c, "list_docs", nil))
		is.Contains(list, "en/hooks-guide", "list missing entries")
		is.Contains(list, "en/settings", "list missing entries")

		search := textOf(t, callTool(t, c, "search_docs", map[string]any{"query": "hooks"}))
		is.Contains(search, "en/hooks-guide", "search missing hooks-guide")

		page := textOf(t, callTool(t, c, "get_doc", map[string]any{"slug": "en/hooks-guide"}))
		is.Contains(page, "quickstart body", "page content wrong")
	})

	t.Run("query returns matching sections verbatim", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t))

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/hooks-guide", "query": "quickstart"})
		msg := textOf(t, res)

		is.Contains(msg, "quickstart body", "matching section missing")

		is.NotContains(msg, "reference body", "non-matching section leaked")

		is.Contains(msg, "›", "expected a heading breadcrumb")
	})

	t.Run("query with no section match guides the caller", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t))

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/hooks-guide", "query": "kubernetes federation"})
		is.False(res.IsError, "no-match should be guidance, not error: %q", textOf(t, res))

		is.Contains(textOf(t, res), "no sections", "expected guidance text")
	})

	t.Run("heading extraction", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t))

		section := textOf(t, callTool(t, c, "get_doc", map[string]any{"slug": "en/hooks-guide", "heading": "Quickstart"}))
		is.Contains(section, "quickstart body", "heading extraction wrong")
		is.NotContains(section, "reference body", "heading extraction wrong")
	})

	t.Run("unknown slug suggests near misses without origin fetch", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		c, _ := newSession(t, f)

		_ = textOf(t, callTool(t, c, "list_docs", nil)) // warm index
		before := f.hits.Load()

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/hooks-guid"})
		is.True(res.IsError, "expected tool error")

		msg := textOf(t, res)
		is.Contains(msg, "en/hooks-guide", "expected near-miss suggestion")

		is.Equal(before, f.hits.Load(), "unknown slug must not hit origin")
	})

	t.Run("origin down with warm cache serves stale with note", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		c, _ := newSessionCfg(t, f, docs.ServiceConfig{
			IndexTTL:      time.Hour,
			PageTTL:       time.Nanosecond, // entries are stale immediately
			CacheMaxBytes: 1 << 20,
		})

		_ = textOf(t, callTool(t, c, "get_doc", map[string]any{"slug": "en/settings"}))

		f.failing.Store(true)

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/settings"})
		is.False(res.IsError, "stale should serve, got error: %q", textOf(t, res))

		msg := textOf(t, res)
		is.Contains(msg, "served from cache", "expected stale note + content")
		is.Contains(msg, "settings body", "expected stale note + content")
	})

	t.Run("origin down cold cache returns friendly error without internals", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		c, _ := newSession(t, f)

		_ = textOf(t, callTool(t, c, "list_docs", nil)) // warm index only

		f.failing.Store(true)

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/settings"})
		is.True(res.IsError, "expected tool error")

		msg := textOf(t, res)
		is.NotContains(msg, "500", "internal detail leaked to client")
		is.Contains(msg, "Source: "+f.srv.URL+"/llms.txt", "source provenance remains available on errors")
	})

	t.Run("listed page that 404s reports removed or moved", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t)) // en/ghost is listed but unserved (404)

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/ghost"})
		is.True(res.IsError, "expected tool error")

		msg := textOf(t, res)
		is.Contains(msg, "removed or moved", "expected removed-or-moved copy")

		is.NotContains(msg, "unreachable", "must not claim the site is unreachable")
	})

	t.Run("missing heading translates to friendly error", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c, _ := newSession(t, newFixture(t))

		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/settings", "heading": "No Such Section"})
		is.True(res.IsError, "expected tool error")

		msg := textOf(t, res)
		is.Contains(msg, "not found", "expected heading-not-found copy with available headings")
		is.Contains(msg, "Available headings", "expected heading-not-found copy with available headings")
	})

	t.Run("unparseable index translates to index unavailable", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		f.garbageIndex.Store(true)
		c, _ := newSession(t, f)

		res := callTool(t, c, "list_docs", nil)
		is.True(res.IsError, "expected tool error")

		is.Contains(textOf(t, res), "index is currently unavailable", "expected index-unavailable copy")
	})

	t.Run("offset continues where truncation stopped", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		c, _ := newSession(t, f)

		// settings page is small; ask past its end to exercise the overshoot
		// path, then offset 3 for a mid-document window.
		res := callTool(t, c, "get_doc", map[string]any{"slug": "en/settings", "offset": 100000})
		is.True(res.IsError, "expected past-the-end error")
		is.Contains(textOf(t, res), "past the end", "expected past-the-end error")

		res2 := callTool(t, c, "get_doc", map[string]any{"slug": "en/settings", "offset": 2})
		msg := textOf(t, res2)

		is.Contains(msg, "[showing bytes 2-", "expected window notice at top")

		is.NotContains(msg, "# Settings", "offset ignored: content starts at beginning")
	})

	t.Run("handler panic becomes tool error not crash", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixture(t)
		c, srv := newSession(t, f)

		mcp.AddTool(
			srv.mcp,
			&mcp.Tool{Name: "boom", Description: "test-only panicking tool"},
			func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, any, error) {
				panic("kaboom")
			},
		)

		res, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: "boom"})
		is.False(err == nil && (res == nil || !res.IsError), "expected error surface from panic")

		// Session must still work after the panic.
		list := textOf(t, callTool(t, c, "list_docs", nil))
		is.Contains(list, "en/settings", "session broken after handler panic")
	})
}
