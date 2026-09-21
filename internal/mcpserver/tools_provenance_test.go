package mcpserver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvenanceText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cs, _ := newSession(t, f)
	res := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/hooks-guide"})
	require.False(t, res.IsError)
	require.Contains(t, textOf(t, res), "Source: "+f.srv.URL+"/en/hooks-guide")
	require.Contains(t, textOf(t, res), "Fetched:")
	require.Contains(t, textOf(t, res), "fresh")
}

func TestEmptyAndErrorProvenance(t *testing.T) {
	t.Parallel()

	cs, _ := newSession(t, newFixture(t))
	for _, test := range []struct {
		name      string
		args      map[string]any
		wantError bool
	}{
		{toolListDocs, map[string]any{"section": "en/missing"}, false},
		{toolSearchDocs, map[string]any{"query": "missing-words"}, false},
		{toolGetDoc, map[string]any{"slug": "en/missing"}, true},
		{toolGetDoc, map[string]any{"slug": "en/hooks-guide", "query": "missing-words"}, false},
		{toolGetDoc, map[string]any{"slug": "en/hooks-guide", "heading": "missing"}, true},
	} {
		result := callTool(t, cs, test.name, test.args)
		require.Equal(t, test.wantError, result.IsError)
		require.Contains(t, textOf(t, result), "Coverage: degraded")
		require.Contains(t, textOf(t, result), "freshness: unavailable")

		if test.name == toolSearchDocs {
			require.Contains(t, textOf(t, result), "fallback")
		}

		require.Contains(t, result.Meta, "cache")
	}
}
