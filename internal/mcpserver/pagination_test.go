package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stretchr/testify/require"
)

type paginationOrigin struct {
	fixture *fixture
	body    atomic.Value
	entries atomic.Int64
}

func newPaginationOrigin(t *testing.T, count int, body string) *paginationOrigin {
	t.Helper()

	origin := &paginationOrigin{fixture: &fixture{}}
	origin.body.Store(body)
	origin.entries.Store(int64(count))
	origin.fixture.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin.fixture.failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		switch {
		case r.URL.Path == "/llms.txt":
			for i := range int(origin.entries.Load()) {
				_, _ = fmt.Fprintf(w, "* [Page %03d](%s/en/page-%03d): test page\n", i, origin.fixture.srv.URL, i)
			}
		case strings.HasPrefix(r.URL.Path, "/api/pagelist/"):
			for i := range int(origin.entries.Load()) {
				_, _ = fmt.Fprintf(w, "/en/page-%03d\n", i)
			}
		default:
			_, _ = fmt.Fprint(w, origin.body.Load())
		}
	}))
	t.Cleanup(origin.fixture.srv.Close)

	return origin
}

func TestSectionPaginationOffersAllMatches(t *testing.T) {
	t.Parallel()

	var body strings.Builder
	for i := range 6 {
		fmt.Fprintf(&body, "## Needle %d\n\nbody %d\n", i, i)
	}

	origin := newPaginationOrigin(t, 1, body.String())
	cs, _ := newSession(t, origin.fixture)
	result := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000", "query": "needle"})
	require.False(t, result.IsError)
	require.Contains(t, textOf(t, result), "sections 1-5 of 6", "all matches must be counted before applying the group limit")
	require.Contains(t, textOf(t, result), `"cursor":`, "sixth section must be reachable")
}

func TestCatalogueOffersContinuation(t *testing.T) {
	t.Parallel()
	origin := newPaginationOrigin(t, 51, "# Test\n")
	cs, _ := newSession(t, origin.fixture)
	result := callTool(t, cs, toolListDocs, nil)
	require.False(t, result.IsError)
	require.Contains(t, textOf(t, result), "entries 1-50 of 51")
	require.Contains(t, textOf(t, result), `"cursor":`)
}

var (
	cursorPattern = regexp.MustCompile(`Continue: (get_doc|list_docs)\((\{"cursor":"[^"]+"\})\)`)
	windowPattern = regexp.MustCompile(`\[showing bytes ([0-9]+)-([0-9]+) of ([0-9]+); more: (true|false)\]\n\n`)
)

func nextArguments(t *testing.T, text string) map[string]any {
	t.Helper()

	matches := cursorPattern.FindAllStringSubmatch(text, -1)
	require.LessOrEqual(t, len(matches), 1, "one executable continuation per response")

	if len(matches) == 0 {
		return nil
	}

	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(matches[0][2]), &args))
	require.Len(t, args, 1)

	return args
}

func pageBody(t *testing.T, text string) string {
	t.Helper()

	match := windowPattern.FindStringSubmatchIndex(text)
	require.NotNil(t, match, "missing byte range")
	start, err := strconv.Atoi(text[match[2]:match[3]])
	require.NoError(t, err)
	end, err := strconv.Atoi(text[match[4]:match[5]])
	require.NoError(t, err)
	require.LessOrEqual(t, end-start, 50*1024)

	return text[match[1] : match[1]+end-start]
}

func traverse(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) []string {
	t.Helper()

	var texts []string

	for range 100 {
		result := callTool(t, cs, name, args)
		require.False(t, result.IsError, "%s", textOf(t, result))
		text := textOf(t, result)
		texts = append(texts, text)

		args = nextArguments(t, text)
		if args == nil {
			require.Contains(t, text, "more: false")
			return texts
		}

		require.Contains(t, text, "more: true")
	}

	t.Fatal("continuation traversal did not terminate")

	return nil
}

func TestSectionCursorTraversal(t *testing.T) {
	t.Parallel()

	for _, count := range []int{0, 1, 5, 6, 17} {
		t.Run(fmt.Sprintf("matches_%d", count), func(t *testing.T) {
			t.Parallel()

			var body strings.Builder
			body.WriteString("# Document\n\nintro\n")

			for i := range count {
				fmt.Fprintf(&body, "## Needle %02d\n\nunique body %02d\n", i, i)
			}

			origin := newPaginationOrigin(t, 1, body.String())
			cs, _ := newSession(t, origin.fixture)
			texts := traverse(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000", "query": "needle"})
			require.Len(t, texts, max(1, (count+4)/5))

			var actual strings.Builder
			for _, text := range texts {
				actual.WriteString(pageBody(t, text))
			}

			last := -1

			for i := range count {
				marker := fmt.Sprintf("unique body %02d", i)
				require.Equal(t, 1, strings.Count(actual.String(), marker))
				position := strings.Index(actual.String(), marker)
				require.Greater(t, position, last, "equal scores preserve original section order")
				last = position
			}

			if count == 0 {
				require.Contains(t, texts[0], "sections 0-0 of 0")
			}
		})
	}
}

func TestOversizedSectionGroupsFinishBytesFirst(t *testing.T) {
	t.Parallel()

	var body, expected strings.Builder

	for i := range 6 {
		section := fmt.Sprintf("## Needle %d\n%smarker-%d\n", i, strings.Repeat("long line 世界\n", 6000), i)
		body.WriteString(section)

		if i != 0 && i != 5 {
			expected.WriteByte('\n')
		}

		fmt.Fprintf(&expected, "› Needle %d\n%s", i, section)
	}

	origin := newPaginationOrigin(t, 1, body.String())
	cs, _ := newSession(t, origin.fixture)
	texts := traverse(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000", "query": "needle"})

	var actual strings.Builder

	groupTwo := false

	for _, text := range texts {
		if strings.Contains(text, "sections 6-6 of 6") {
			groupTwo = true
		} else {
			require.False(t, groupTwo, "must not return to an earlier group")
		}

		actual.WriteString(pageBody(t, text))
	}

	require.True(t, groupTwo)
	require.Equal(t, expected.String(), actual.String())
}

func TestFullAndHeadingCursorTraversal(t *testing.T) {
	t.Parallel()

	body := "# Document\nintro\n## Large\n" + strings.Repeat("content 世界\n", 12000) + "## Small\nsmall body\n"

	for _, heading := range []string{"", "Large"} {
		t.Run("heading_"+heading, func(t *testing.T) {
			t.Parallel()
			origin := newPaginationOrigin(t, 1, body)
			cs, _ := newSession(t, origin.fixture)
			args := map[string]any{"slug": "en/page-000"}
			want := body

			if heading != "" {
				args["heading"] = heading
				want = "## Large\n" + strings.Repeat("content 世界\n", 12000)
			}

			texts := traverse(t, cs, toolGetDoc, args)

			var actual strings.Builder
			for _, text := range texts {
				actual.WriteString(pageBody(t, text))
			}

			require.Equal(t, want, actual.String())
			replay := callTool(t, cs, toolGetDoc, nextArguments(t, texts[0]))
			require.Equal(t, texts[1], textOf(t, replay))
			legacy := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000", "offset": 5})
			require.False(t, legacy.IsError)
			require.Contains(t, textOf(t, legacy), "offset-only continuations cannot validate snapshots")
		})
	}
}

func TestCatalogueCursorTraversal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, section   string
		limit, expected int
	}{
		{"default", "", 0, 205},
		{"maximum", "", 1000, 205},
		{"single", "en/page-001", 1, 1},
		{"filtered", "en/page-00", 3, 10},
		{"empty", "en/missing", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origin := newPaginationOrigin(t, 205, "# Page\n")
			cs, _ := newSession(t, origin.fixture)
			texts := traverse(t, cs, toolListDocs, map[string]any{"section": tc.section, "limit": tc.limit})

			var slugs []string

			for _, text := range texts {
				for line := range strings.SplitSeq(text, "\n") {
					if strings.HasPrefix(line, "en/page-") {
						slug, _, _ := strings.Cut(line, " - ")
						slugs = append(slugs, slug)
					}
				}
			}

			require.Len(t, slugs, tc.expected)
			require.True(t, slices.IsSorted(slugs))
			require.Len(t, slices.Compact(slugs), tc.expected, "no duplicates")

			if tc.expected > 50 {
				replay := callTool(t, cs, toolListDocs, nextArguments(t, texts[0]))
				require.Equal(t, texts[1], textOf(t, replay))
			}
		})
	}
}

func TestCursorSnapshotsRefreshAndRestart(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{toolGetDoc, toolListDocs} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()

			var seconds atomic.Int64
			seconds.Store(1800000000)

			origin := newPaginationOrigin(t, 6, "# Large\n"+strings.Repeat("content\n", 15000))
			disk, err := docs.NewDiskCache(t.TempDir())
			require.NoError(t, err)

			cfg := docs.ServiceConfig{Now: func() time.Time { return time.Unix(seconds.Load(), 0) }, IndexTTL: time.Second, PageTTL: time.Second, CacheMaxBytes: 1 << 20, Disk: disk}
			cs, _ := newSessionCfg(t, origin.fixture, cfg)

			args := map[string]any{"slug": "en/page-000"}
			if tool == toolListDocs {
				args = map[string]any{"section": "en/page-", "limit": 2}
			}

			initial := callTool(t, cs, tool, args)
			require.False(t, initial.IsError)
			continuationArgs := nextArguments(t, textOf(t, initial))
			require.NotNil(t, continuationArgs)
			first := callTool(t, cs, tool, continuationArgs)
			require.False(t, first.IsError)
			seconds.Add(2)

			refreshed := callTool(t, cs, tool, continuationArgs)
			require.False(t, refreshed.IsError, "identical refreshed content must retain snapshot")

			if tool == toolGetDoc {
				require.Equal(t, pageBody(t, textOf(t, first)), pageBody(t, textOf(t, refreshed)))
			}
			// A new service has no cursor sessions. The persisted snapshot is sufficient.
			restarted, _ := newSessionCfg(t, origin.fixture, cfg)
			origin.fixture.failing.Store(true)
			seconds.Add(2)

			replayed := callTool(t, restarted, tool, continuationArgs)
			require.False(t, replayed.IsError)
			require.Contains(t, textOf(t, replayed), "freshness: stale")
			origin.fixture.failing.Store(false)
			seconds.Add(60)

			if tool == toolGetDoc {
				origin.body.Store("# Changed\n" + strings.Repeat("different\n", 15000))
			} else {
				origin.entries.Store(7)
			}

			changed := callTool(t, restarted, tool, continuationArgs)
			require.True(t, changed.IsError)
			require.Contains(t, textOf(t, changed), "snapshot changed; restart with "+tool)

			for key, value := range args {
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				require.Contains(t, textOf(t, changed), fmt.Sprintf("%q:%s", key, raw))
			}
		})
	}
}

func TestInvalidCursorsAndMixedArguments(t *testing.T) {
	t.Parallel()
	origin := newPaginationOrigin(t, 6, "# Large\n"+strings.Repeat("body\n", 20000))
	cs, _ := newSession(t, origin.fixture)
	initial := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000"})
	args := nextArguments(t, textOf(t, initial))
	encoded, ok := args["cursor"].(string)
	require.True(t, ok)

	base, err := decodeCursor(encoded, toolGetDoc, origin.fixture.srv.URL)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		change func(*continuation)
	}{
		{"version", func(c *continuation) { c.Version++ }},
		{"tool", func(c *continuation) { c.Tool = toolListDocs }},
		{"origin", func(c *continuation) { c.Origin = "https://other.example" }},
		{"negative offset", func(c *continuation) { c.Offset = -1 }},
		{"overshoot", func(c *continuation) { c.Offset = math.MaxInt }},
		{"negative position", func(c *continuation) { c.Position = -1 }},
		{"page position", func(c *continuation) { c.Position = 5 }},
		{"limit", func(c *continuation) { c.Limit = 1 }},
		{"snapshot", func(c *continuation) { c.Snapshot = "bad" }},
		{"selection", func(c *continuation) { c.Selection.Slug = "" }},
		{"start position", func(c *continuation) { c.Offset = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			invalid := base
			tc.change(&invalid)
			encoded, err := encodeCursor(invalid)
			require.NoError(t, err)
			result := callTool(t, cs, toolGetDoc, map[string]any{"cursor": encoded})
			require.True(t, result.IsError)
			require.Contains(t, textOf(t, result), "cursor")
		})
	}

	for _, input := range []string{"", "%%%", "e30", strings.Repeat("A", maxCursorBytes+1), base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"extra":true}`)), base64.RawURLEncoding.EncodeToString([]byte(`{} {}`))} {
		result := callTool(t, cs, toolGetDoc, map[string]any{"cursor": input})
		require.True(t, result.IsError)
	}

	for _, extra := range []string{"slug", "heading", "query", "offset"} {
		mixed := map[string]any{"cursor": encoded, extra: ""}
		if extra == "offset" {
			mixed[extra] = 0
		}

		result := callTool(t, cs, toolGetDoc, mixed)
		require.True(t, result.IsError)
		require.Contains(t, textOf(t, result), "only argument")
	}

	list := callTool(t, cs, toolListDocs, map[string]any{"limit": 1})

	listArgs := nextArguments(t, textOf(t, list))
	for _, extra := range []string{"section", "limit"} {
		mixed := maps.Clone(listArgs)

		mixed[extra] = ""
		if extra == "limit" {
			mixed[extra] = 0
		}

		require.True(t, callTool(t, cs, toolListDocs, mixed).IsError)
	}

	listEncoded, ok := listArgs["cursor"].(string)
	require.True(t, ok)

	listCursor, err := decodeCursor(listEncoded, toolListDocs, origin.fixture.srv.URL)
	require.NoError(t, err)

	listCursor.Position = math.MaxInt
	invalid, err := encodeCursor(listCursor)
	require.NoError(t, err)
	require.True(t, callTool(t, cs, toolListDocs, map[string]any{"cursor": invalid}).IsError)
}

func TestCursorCancellationAllowsReplay(t *testing.T) {
	t.Parallel()
	origin := newPaginationOrigin(t, 3, "# Large\n"+strings.Repeat("body\n", 20000))
	cs, _ := newSession(t, origin.fixture)
	initial := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/page-000"})
	args := nextArguments(t, textOf(t, initial))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: toolGetDoc, Arguments: args})
	require.Error(t, err)
	require.False(t, callTool(t, cs, toolGetDoc, args).IsError)
}

func TestListDocsWithoutArgumentsOverHTTP(t *testing.T) {
	t.Parallel()
	server, _ := newHTTPFixture(t, nil)
	status, raw := postMCP(t, server.URL, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_docs"}}`)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, raw, "en/hooks-guide", "omitted arguments must keep the valid legacy list_docs call")
	require.NotContains(t, raw, `"isError":true`)
}
