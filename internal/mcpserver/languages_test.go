package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/stretchr/testify/require"
)

type languageOrigin struct {
	fixture       *fixture
	failed        atomic.Value
	searchDown    atomic.Bool
	englishBodies atomic.Int32
	body          string
}

func newLanguageOrigin(t *testing.T) *languageOrigin {
	t.Helper()

	origin := &languageOrigin{fixture: &fixture{}}
	origin.failed.Store("")

	var body strings.Builder
	for i := range 7 {
		fmt.Fprintf(&body, "## 設定 %d\n\n%s\n", i, strings.Repeat("日本語 café 한국어 😀 ", 1000))
	}

	origin.body = body.String()
	origin.fixture.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		path := r.URL.Path
		if path == "/llms.txt" {
			_, _ = fmt.Fprintf(w, "* [English guide](%s/en/guide): English description\n* [English only](%s/en/english-only): Untranslated\n", origin.fixture.srv.URL, origin.fixture.srv.URL)
			return
		}

		language, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if strings.HasPrefix(path, "/api/pagelist/") {
			language = strings.Split(path, "/")[3]
			if origin.failed.Load() == language {
				http.NotFound(w, r)
				return
			}

			_, _ = fmt.Fprintf(w, "/%s/guide\n/%s/other\n/%s/redirect\n/%s/vanished\n", language, language, language, language) //nolint:gosec // Test-only plain-text origin; paths are exercised through the real client.

			return
		}

		if path == "/api/search/v1" {
			if origin.searchDown.Load() {
				http.NotFound(w, r)
				return
			}

			language = r.URL.Query().Get("language")

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": []any{map[string]any{
				"url": "/" + language + "/guide", "title": language + " ガイド", "highlights": map[string]any{"content": []string{language + " 日本語 café 한국어 😀"}},
			}}})

			return
		}

		if strings.HasSuffix(path, "/redirect.md") {
			http.Redirect(w, r, "/en/guide.md", http.StatusFound)
			return
		}

		if strings.HasSuffix(path, "/vanished.md") {
			http.NotFound(w, r)
			return
		}

		if language == "en" {
			origin.englishBodies.Add(1)
		}

		if origin.failed.Load() == language {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		_, _ = fmt.Fprintf(w, "# %s guide\n\n%s", language, origin.body) //nolint:gosec // Test-only Markdown origin with no HTML consumer.
	}))
	t.Cleanup(origin.fixture.srv.Close)

	return origin
}

func TestMultilingualMCP(t *testing.T) {
	t.Parallel()
	origin := newLanguageOrigin(t)

	cs, _ := newSession(t, origin.fixture)
	for _, language := range docs.SupportedLanguages() {
		t.Run(language, func(t *testing.T) {
			t.Parallel()
			listed := traverse(t, cs, toolListDocs, map[string]any{"language": language, "limit": 1})
			require.GreaterOrEqual(t, len(listed), 4)

			for _, text := range listed {
				require.Contains(t, text, "language: "+language)

				if language != "en" {
					require.NotContains(t, text, "English description")
					require.NotContains(t, text, "Coverage: degraded")
					require.NotContains(t, text, "/en/")
				}
			}

			search := callTool(t, cs, toolSearchDocs, map[string]any{"language": language, "query": "日本語"})
			require.False(t, search.IsError, textOf(t, search))
			require.Contains(t, textOf(t, search), language+"/guide")
			require.Contains(t, textOf(t, search), "Search: upstream")
			full := traverse(t, cs, toolGetDoc, map[string]any{"slug": language + "/guide"})

			var joined strings.Builder

			for _, text := range full {
				require.True(t, utf8.ValidString(text))
				joined.WriteString(pageBody(t, text))
			}

			require.Equal(t, "# "+language+" guide\n\n"+origin.body, joined.String())
			sections := traverse(t, cs, toolGetDoc, map[string]any{"slug": language + "/guide", "query": "設定"})
			require.Contains(t, sections[len(sections)-1], "sections 6-7 of 7")
			joined.Reset()

			for _, text := range sections {
				joined.WriteString(pageBody(t, text))
			}

			for i := range 7 {
				require.Equal(t, 1, strings.Count(joined.String(), fmt.Sprintf("## 設定 %d\n", i)))
			}

			heading := callTool(t, cs, toolGetDoc, map[string]any{"slug": language + "/guide", "heading": "#設定-6"})
			require.False(t, heading.IsError, textOf(t, heading))
			require.Contains(t, pageBody(t, textOf(t, heading)), "## 設定 6")
		})
	}
}

func TestMissingTranslationOffersEnglishWithoutFetchingIt(t *testing.T) {
	t.Parallel()
	origin := newLanguageOrigin(t)
	cs, _ := newSession(t, origin.fixture)
	result := callTool(t, cs, toolGetDoc, map[string]any{"slug": "ja/english-only"})
	require.True(t, result.IsError)
	require.Contains(t, textOf(t, result), "unavailable in the requested language")
	require.Contains(t, textOf(t, result), `get_doc({"slug":"en/english-only"})`)
	require.Zero(t, origin.englishBodies.Load(), "offering English must not fetch English content")
	result = callTool(t, cs, toolGetDoc, map[string]any{"slug": "ja/redirect"})
	require.True(t, result.IsError, "an English redirect must never become Japanese content")
	require.Contains(t, textOf(t, result), `get_doc({"slug":"en/redirect"})`)
	require.Zero(t, origin.englishBodies.Load())
	result = callTool(t, cs, toolGetDoc, map[string]any{"slug": "ja/unknown-everywhere"})
	require.True(t, result.IsError)
	require.Contains(t, textOf(t, result), "unavailable in the requested language")
	require.NotContains(t, textOf(t, result), "English alternative:")
}

func TestLanguageValidationAndEmptyResults(t *testing.T) {
	t.Parallel()

	cs, _ := newSession(t, newLanguageOrigin(t).fixture)
	for _, test := range []struct {
		name    string
		tool    string
		args    map[string]any
		message string
	}{
		{"unsupported list", toolListDocs, map[string]any{"language": "xx"}, "supported:"},
		{"unsupported search", toolSearchDocs, map[string]any{"language": "xx", "query": "guide"}, "supported:"},
		{"unsupported slug", toolGetDoc, map[string]any{"slug": "xx/guide"}, "supported:"},
		{"mixed section", toolListDocs, map[string]any{"language": "ja", "section": "en/"}, "section must start"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := callTool(t, cs, test.tool, test.args)
			require.True(t, result.IsError)
			require.Contains(t, textOf(t, result), test.message)
		})
	}

	result := callTool(t, cs, toolListDocs, map[string]any{"language": "ja", "section": "ja/missing"})
	require.False(t, result.IsError)
	require.Contains(t, textOf(t, result), "entries 0-0 of 0")
	require.Contains(t, textOf(t, result), "language: ja")
	result = callTool(t, cs, toolListDocs, nil)
	require.Contains(t, textOf(t, result), "English description")
}

func TestLanguageCursorsAndDiskRestart(t *testing.T) {
	t.Parallel()

	for _, name := range []string{toolListDocs, toolGetDoc} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			origin := newLanguageOrigin(t)
			disk, err := docs.NewDiskCache(t.TempDir())
			require.NoError(t, err)

			cfg := docs.ServiceConfig{Disk: disk, IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 4 << 20}

			cs, _ := newSessionCfg(t, origin.fixture, cfg)

			args := map[string]any{"language": "ja", "limit": 1}
			if name == toolGetDoc {
				args = map[string]any{"slug": "ja/guide"}
			}

			first := callTool(t, cs, name, args)
			cursor := nextArguments(t, textOf(t, first))
			require.NotNil(t, cursor)
			second := callTool(t, cs, name, cursor)

			origin.failed.Store("ja")
			restarted, _ := newSessionCfg(t, origin.fixture, cfg)
			replay := callTool(t, restarted, name, cursor)
			require.False(t, replay.IsError, textOf(t, replay))
			require.Equal(t, textOf(t, second), textOf(t, replay))

			mixed := map[string]any{"cursor": cursor["cursor"], "language": "en"}
			require.True(t, callTool(t, cs, name, mixed).IsError)

			encoded, ok := cursor["cursor"].(string)
			require.True(t, ok)

			decoded, err := decodeCursor(encoded, name, origin.fixture.srv.URL)
			require.NoError(t, err)

			if name == toolListDocs {
				decoded.Selection.Language = "fr"
			} else {
				decoded.Selection.Slug = "fr/guide"
			}

			forged, err := encodeCursor(decoded)
			require.NoError(t, err)
			foreign := callTool(t, cs, name, map[string]any{"cursor": forged})
			require.True(t, foreign.IsError, "changing a cursor's language must invalidate its snapshot")
			require.Contains(t, textOf(t, foreign), "snapshot changed")
			origin.failed.Store("")
		})
	}
}

func TestPreviousCursorFormatRequiresRestart(t *testing.T) {
	t.Parallel()
	origin := newLanguageOrigin(t)
	cs, _ := newSession(t, origin.fixture)
	first := callTool(t, cs, toolGetDoc, map[string]any{"slug": "en/guide", "query": "設定"})
	arguments := nextArguments(t, textOf(t, first))
	require.NotNil(t, arguments)
	encoded, ok := arguments["cursor"].(string)
	require.True(t, ok)

	cursor, err := decodeCursor(encoded, toolGetDoc, origin.fixture.srv.URL)
	require.NoError(t, err)
	// Version 1 selected content normalised CRLF and appended a final newline.
	// Its offsets cannot safely address the verbatim selection format.
	cursor.Version = 1
	old, err := encodeCursor(cursor)
	require.NoError(t, err)
	result := callTool(t, cs, toolGetDoc, map[string]any{"cursor": old})
	require.True(t, result.IsError, "historical offsets must not be applied to the new selection format")
	require.Contains(t, textOf(t, result), "unsupported cursor version; restart the original call")
}

func TestLocalizedFallbackCannotSearchOtherLanguages(t *testing.T) {
	t.Parallel()
	origin := newLanguageOrigin(t)

	cs, _ := newSession(t, origin.fixture)
	for _, language := range []string{"en", "ja", "fr"} {
		result := callTool(t, cs, toolGetDoc, map[string]any{"slug": language + "/guide"})
		require.False(t, result.IsError)
	}

	origin.searchDown.Store(true)

	for _, language := range []string{"en", "ja", "fr"} {
		result := callTool(t, cs, toolSearchDocs, map[string]any{"language": language, "query": "日本語"})
		require.False(t, result.IsError)
		text := textOf(t, result)
		require.Contains(t, text, "fallback")
		require.Contains(t, text, "reduced coverage")
		require.Contains(t, text, language+"/guide")

		for _, other := range []string{"en", "ja", "fr"} {
			if other != language {
				require.NotContains(t, text, other+"/guide")
			}
		}

		empty := callTool(t, cs, toolSearchDocs, map[string]any{"language": language, "query": "absent-canary-term"})
		require.False(t, empty.IsError)
		require.Contains(t, textOf(t, empty), "reduced coverage")
		require.Contains(t, textOf(t, empty), "language: "+language)
	}
}
