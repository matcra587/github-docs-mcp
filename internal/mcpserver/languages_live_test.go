//go:build integration

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func liveProcess(t *testing.T, binary, cache string) *mcp.ClientSession {
	t.Helper()
	command := exec.CommandContext(context.WithoutCancel(t.Context()), binary, "-transport", "stdio", "-base-url", "https://docs.github.com", "-cache-dir", cache, "-index-ttl", "1h", "-page-ttl", "24h", "-log-level", "error") //nolint:gosec // Executes the binary built by this test in t.TempDir.
	client := mcp.NewClient(&mcp.Implementation{Name: "live-language-canary", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, session.Close()) })
	return session
}

func liveSource(t *testing.T, origin *http.Client, path string) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://docs.github.com"+path, nil)
	require.NoError(t, err)
	response, err := origin.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	require.NoError(t, err)
	return body
}

func requireDiskSource(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	raw, err := json.Marshal(result.Meta["cache"])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"source":"disk"`, "restart replay must use the persisted cache")
}

func TestLiveMultilingualMCP(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "github-docs-mcp")
	output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/github-docs-mcp").CombinedOutput()
	require.NoError(t, err, "%s", output)
	cache := t.TempDir()
	session := liveProcess(t, binary, cache)
	origin := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{}}
	t.Cleanup(origin.CloseIdleConnections)
	var languages struct {
		Languages []string `json:"languages"`
	}
	require.NoError(t, json.Unmarshal(liveSource(t, origin, "/api/pagelist/languages"), &languages))
	require.ElementsMatch(t, docs.SupportedLanguages(), languages.Languages, "review new upstream languages before claiming complete language coverage")
	english := liveSource(t, origin, "/en/actions/reference/workflows-and-actions/workflow-syntax.md")
	queries := map[string]string{"en": "repository", "es": "repositorio", "ja": "リポジトリ", "pt": "repositório", "zh": "存储库", "ru": "репозиторий", "fr": "dépôt", "ko": "리포지토리", "de": "Repository"}
	for _, language := range docs.SupportedLanguages() {
		t.Run(language, func(t *testing.T) {
			t.Parallel()
			search := callTool(t, session, toolSearchDocs, map[string]any{"language": language, "query": queries[language], "limit": 2})
			require.False(t, search.IsError, textOf(t, search))
			require.Contains(t, textOf(t, search), "Search: upstream")
			hits := regexp.MustCompile(`(?m)^(`+language+`/\S+) - `).FindAllStringSubmatch(textOf(t, search), -1)
			require.Len(t, hits, 2)
			for _, hit := range hits {
				page := callTool(t, session, toolGetDoc, map[string]any{"slug": hit[1]})
				require.False(t, page.IsError, textOf(t, page))
				require.Contains(t, textOf(t, page), "language: "+language)
			}
			slug := language + "/actions/reference/workflows-and-actions/workflow-syntax"
			windows := traverse(t, session, toolGetDoc, map[string]any{"slug": slug})
			require.Greater(t, len(windows), 1)
			var whole strings.Builder
			for _, text := range windows {
				require.True(t, utf8.ValidString(text))
				whole.WriteString(pageBody(t, text))
			}
			reference := liveSource(t, origin, "/"+slug+".md")
			require.Equal(t, string(reference), whole.String(), "MCP pagination must reproduce the upstream bytes exactly")
			if language != "en" {
				require.NotEqual(t, string(english), whole.String(), "a localized page must not silently return the English body")
			}
			headings := regexp.MustCompile(`(?m)^## (.+)$`).FindAllStringSubmatch(whole.String(), -1)
			require.NotEmpty(t, headings)
			selected := callTool(t, session, toolGetDoc, map[string]any{"slug": slug, "heading": headings[0][1]})
			require.False(t, selected.IsError, textOf(t, selected))
			selectedBody := pageBody(t, textOf(t, selected))
			require.NotEmpty(t, selectedBody)
			require.True(t, strings.HasPrefix(selectedBody, "## "+headings[0][1]+"\n"))
			require.Contains(t, whole.String(), selectedBody)
			groups := traverse(t, session, toolGetDoc, map[string]any{"slug": slug, "query": "jobs"})
			require.Greater(t, len(groups), 1)
			listing := callTool(t, session, toolListDocs, map[string]any{"language": language, "limit": 1})
			require.False(t, listing.IsError)
			listCursor := nextArguments(t, textOf(t, listing))
			require.NotNil(t, listCursor)
			listed := callTool(t, session, toolListDocs, listCursor)
			pageCursor := nextArguments(t, windows[0])
			restarted := liveProcess(t, binary, cache)
			listReplay := callTool(t, restarted, toolListDocs, listCursor)
			pageReplay := callTool(t, restarted, toolGetDoc, pageCursor)
			require.Equal(t, textOf(t, listed), textOf(t, listReplay))
			require.Equal(t, windows[1], textOf(t, pageReplay))
			requireDiskSource(t, listReplay)
			requireDiskSource(t, pageReplay)
			missing := callTool(t, session, toolGetDoc, map[string]any{"slug": language + "/__missing_multilingual_canary__"})
			require.True(t, missing.IsError)
			if language != "en" {
				require.Contains(t, textOf(t, missing), "unavailable in the requested language")
			}
			t.Logf("LIVE PASS language=%s search_hits=%d page_bytes=%d page_windows=%d section_windows=%d heading=true cursor_replay=true disk_restart=true", language, len(hits), whole.Len(), len(windows), len(groups))
		})
	}
}
