package mcpserver

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchSizeHints(t *testing.T) {
	t.Parallel()
	c, _ := newSession(t, newFixture(t))
	args := map[string]any{"query": "hooks"}
	cold := callTool(t, c, toolSearchDocs, args)
	require.False(t, cold.IsError)
	require.Contains(t, textOf(t, cold), "[page bytes: unknown]")
	page := callTool(t, c, toolGetDoc, map[string]any{"slug": "en/hooks-guide"})
	require.False(t, page.IsError)
	warm := callTool(t, c, toolSearchDocs, args)
	require.False(t, warm.IsError)
	require.Contains(t, textOf(t, warm), fmt.Sprintf("[cached page bytes: %d]", len("# Hooks\n\n## Quickstart\n\nquickstart body\n\n## Reference\n\nreference body\n")))
	require.NotContains(t, textOf(t, warm), "[page bytes: unknown]")
}
