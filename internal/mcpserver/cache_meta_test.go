package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/stretchr/testify/require"
)

func TestToolCacheMetadata(t *testing.T) {
	t.Parallel()

	c, _ := newSession(t, newFixture(t))
	for _, tt := range []struct { //nolint:paralleltest // sequential calls exercise one session moving from cold to warm
		name, tool, status string
		args               map[string]any
	}{
		{"catalogue miss", "list_docs", "miss", nil},
		{"catalogue hit", "list_docs", "hit", nil},
		{"page miss", "get_doc", "miss", map[string]any{"slug": "en/hooks-guide"}},
		{"page hit", "get_doc", "hit", map[string]any{"slug": "en/hooks-guide"}},
		{"section hit", "get_doc", "hit", map[string]any{"slug": "en/hooks-guide", "query": "quickstart"}},
		{"search fallback", "search_docs", "fallback", map[string]any{"query": "quickstart"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, c, tt.tool, tt.args)
			require.False(t, res.IsError, "%s", textOf(t, res))
			raw, err := json.Marshal(res.Meta["cache"])
			require.NoError(t, err)

			var decisions []docs.CacheDecision
			require.NoError(t, json.Unmarshal(raw, &decisions))
			require.NotEmpty(t, decisions)
			require.Equal(t, tt.status, decisions[len(decisions)-1].Status)
		})
	}
}
