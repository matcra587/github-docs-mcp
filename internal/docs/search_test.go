package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testIndex(t *testing.T) *Index {
	t.Helper()

	md := strings.Join([]string{
		"- [Hooks guide](https://docs.github.com/en/hooks-guide.md): Automate workflows with lifecycle hooks",
		"- [Hooks reference](https://docs.github.com/en/hooks-reference.md): Full hook event and schema reference",
		"- [MCP servers](https://docs.github.com/en/mcp.md): Connect Model Context Protocol servers",
		"- [Settings](https://docs.github.com/en/settings.md): Configure repository behaviour",
		"- [SDK loop](https://docs.github.com/en/agent-sdk/agent-loop.md): How the agent loop works",
	}, "\n")

	idx, err := ParseIndex("https://docs.github.com", strings.NewReader(md))
	require.NoError(t, err)

	return idx
}

func TestSearchIndex(t *testing.T) {
	t.Parallel()

	idx := testIndex(t)

	t.Run("title match outranks description match", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		hits := SearchIndex(idx, "hooks", 0)
		must.GreaterOrEqual(len(hits), 2, "expected at least 2 hits")

		is.True(strings.HasPrefix(hits[0].Doc.Slug, "en/hooks"), "expected hooks page first, got %s", hits[0].Doc.Slug)
	})

	t.Run("multi token requires all tokens", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		hits := SearchIndex(idx, "agent loop", 0)
		must.Len(hits, 1, "expected only agent-loop")
		is.Equal("en/agent-sdk/agent-loop", hits[0].Doc.Slug, "expected only agent-loop")
	})

	t.Run("multi token falls back to any-match ranking when strict yields nothing", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		// "previous" appears nowhere; strict AND would return zero. The
		// fallback must still surface the session-ish page via "resume".
		md := "- [Manage sessions](https://docs.github.com/en/sessions.md): Resume and continue conversations\n" +
			"- [Settings](https://docs.github.com/en/settings.md): Configure behaviour\n"

		idx2, err := ParseIndex("https://docs.github.com", strings.NewReader(md))
		must.NoError(err)

		hits := SearchIndex(idx2, "resume previous session", 0)
		must.NotEmpty(hits, "expected fallback hit on en/sessions")
		is.Equal("en/sessions", hits[0].Doc.Slug, "expected fallback hit on en/sessions")
	})

	t.Run("no hits", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Empty(SearchIndex(idx, "kubernetes federation", 0), "expected no hits")
	})

	t.Run("empty query no hits", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Empty(SearchIndex(idx, "  ", 0), "expected no hits for blank query")
	})

	t.Run("limit respected and default applied", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Len(SearchIndex(idx, "hooks", 1), 1, "limit 1 violated")
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.NotEmpty(SearchIndex(idx, "HOOKS", 0), "expected case-insensitive match")
	})
}

func TestFilterDocs(t *testing.T) {
	t.Parallel()

	idx := testIndex(t)

	t.Run("no section returns all up to limit", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		docs := FilterDocs(idx, "", 0)
		is.Len(docs, 5, "expected 5")
	})

	t.Run("section prefix filters", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		docs := FilterDocs(idx, "en/agent-sdk", 0)
		must.Len(docs, 1, "expected agent-sdk docs only")
		is.Equal("en/agent-sdk/agent-loop", docs[0].Slug, "expected agent-sdk docs only")
	})

	t.Run("limit caps output", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Len(FilterDocs(idx, "", 2), 2, "limit 2 violated")
	})
}

func TestSuggest(t *testing.T) {
	t.Parallel()

	idx := testIndex(t)

	t.Run("near miss suggests close slugs", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		got := Suggest(idx, "en/hooks-guid", 3)
		must.NotEmpty(got, "expected en/hooks-guide first")
		is.Equal("en/hooks-guide", got[0], "expected en/hooks-guide first")
	})

	t.Run("wildly wrong yields nothing", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Empty(Suggest(idx, "zzzzzzzzzzzzzzzz", 3), "expected no suggestions")
	})
}
