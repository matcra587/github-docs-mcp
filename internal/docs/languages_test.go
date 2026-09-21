package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestLocalizedSourceWithBasePath(t *testing.T) {
	t.Parallel()

	source := (Doc{Slug: "ja/actions/guide", URL: "https://example.com/docs/ja/actions/guide.md"}).Source()
	require.Equal(t, "ja", source.Language)
	require.Equal(t, "actions", source.Product)
}

func TestLocaleRedirectIsTerminal(t *testing.T) {
	t.Parallel()

	for _, route := range []string{"/ja/guide.md", "/docs/ja/guide.md", "/api/pagelist/ja/free-pro-team@latest", "/api/search/v1?language=ja&query=guide"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			testLocaleRedirect(t, route)
		})
	}
}

func testLocaleRedirect(t *testing.T, route string) {
	t.Helper()

	var attempts, english atomic.Int32

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() == route {
			attempts.Add(1)
			http.Redirect(w, r, strings.Replace(route, "ja", "en", 1), http.StatusFound)

			return
		}

		english.Add(1)

		_, _ = fmt.Fprint(w, "# English body")
	}))
	t.Cleanup(origin.Close)

	base := origin.URL
	if strings.HasPrefix(route, "/docs/") {
		base += "/docs"
	}

	client, err := NewClient(base, WithRateLimit(1000))
	require.NoError(t, err)

	client.sleep = func(context.Context, time.Duration) error { return nil }
	_, err = client.Fetch(t.Context(), origin.URL+route)
	require.ErrorIs(t, err, ErrNotFound)
	require.EqualValues(t, 1, attempts.Load(), "language rejection must not be retried")
	require.Zero(t, english.Load())
}

func TestRejectsDeclaredEnglishFallback(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Header().Set("Content-Language", "en")
		_, _ = fmt.Fprint(w, "# English fallback")
	}))
	t.Cleanup(origin.Close)
	client, err := NewClient(origin.URL, WithRateLimit(1000))
	require.NoError(t, err)
	_, err = client.Fetch(t.Context(), origin.URL+"/ja/guide.md")
	require.ErrorIs(t, err, ErrNotFound, "explicit English fallback must not be accepted as Japanese")
}

func TestContentLanguageCompatibility(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		header, language string
		matches          bool
	}{
		{"", "ja", true},
		{"ja", "ja", true},
		{"en", "ja", false},
		{"pt-BR", "pt", true},
		{"zh-Hans", "zh", true},
		{"FR-ca", "fr", true},
		{"en, ja", "ja", true},
		{"japanese", "ja", false},
	} {
		t.Run(test.header+"/"+test.language, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.matches, contentLanguageMatches(test.header, test.language))
		})
	}
}

func TestLocalizedDiskRejectsForeignCatalogue(t *testing.T) {
	t.Parallel()
	disk, err := NewDiskCache(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, disk.root.Close()) })
	require.NoError(t, disk.Store("pagelist:ja:"+docsVersion, []byte("/en/guide\n")))
	require.NoError(t, disk.Store("page:ja/guide", []byte("poisoned body")))

	fetcher := provenanceFetcher(func(context.Context, string) ([]byte, error) {
		return nil, errors.New("offline")
	})
	svc := NewService(fetcher, testBaseURL, ServiceConfig{Disk: disk, IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1024})
	page, err := svc.Get(t.Context(), "ja/guide")
	require.ErrorIs(t, err, ErrIndexUnavailable)
	require.Empty(t, page.Content, "a foreign catalogue must not authorize a cached localized page")
}

func TestLanguageCancellationKeepsOtherCallersAlive(t *testing.T) {
	t.Parallel()

	started, release := make(chan struct{}), make(chan struct{})

	var calls atomic.Int32

	fetcher := provenanceFetcher(func(ctx context.Context, raw string) ([]byte, error) {
		if strings.Contains(raw, "/ja/") {
			if calls.Add(1) == 1 {
				close(started)
			}

			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			return []byte("/ja/guide\n"), nil
		}

		return []byte("/fr/guide\n"), nil
	})
	svc := NewService(fetcher, testBaseURL, ServiceConfig{IndexTTL: time.Hour})
	ja, err := svc.ForLanguage("ja")
	require.NoError(t, err)
	fr, err := svc.ForLanguage("fr")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() { _, err := ja.Catalogue(ctx, "", 0); done <- err }()

	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	french, err := fr.Catalogue(t.Context(), "", 0)
	require.NoError(t, err)
	require.Equal(t, "fr/guide", french.Docs[0].Slug)
	close(release)

	japanese, err := ja.Catalogue(t.Context(), "", 0)
	require.NoError(t, err)
	require.Equal(t, "ja/guide", japanese.Docs[0].Slug)
	require.EqualValues(t, 1, calls.Load(), "cancelled caller must leave the detached language refresh reusable")
}

func TestLanguageRefreshIsolation(t *testing.T) {
	t.Parallel()

	clock := newTestClock()

	var failing atomic.Bool

	fetcher := provenanceFetcher(func(_ context.Context, raw string) ([]byte, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}

		if failing.Load() && strings.Contains(u.Path, "/ja/") {
			return nil, errors.New("Japanese origin unavailable")
		}

		if strings.Contains(u.Path, pageListPath) {
			language := strings.Split(u.Path, "/")[3]
			return []byte("/" + language + "/guide\n"), nil
		}

		return []byte("# 本文 café\n"), nil
	})
	svc := NewService(fetcher, testBaseURL, ServiceConfig{Now: clock.Now, IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1024})
	before, err := svc.Get(t.Context(), "ja/guide")
	require.NoError(t, err)
	clock.Advance(2 * time.Hour)
	failing.Store(true)

	stale, err := svc.Get(t.Context(), "ja/guide")
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.True(t, stale.Catalogue.Degraded)
	require.Equal(t, before.Source.FetchedAt, stale.Source.FetchedAt)
	fresh, err := svc.Get(t.Context(), "fr/guide")
	require.NoError(t, err)
	require.False(t, fresh.Stale)
	require.False(t, fresh.Catalogue.Degraded)
	require.Equal(t, "fr", fresh.Source.Language)
	failing.Store(false)
	clock.Advance(failCooldown + time.Second)

	recovered, err := svc.Get(t.Context(), "ja/guide")
	require.NoError(t, err)
	require.False(t, recovered.Stale)
	require.False(t, recovered.Catalogue.Degraded)
	require.Equal(t, clock.Now(), recovered.Source.FetchedAt)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = svc.Get(ctx, "ja/guide")
	require.ErrorIs(t, err, context.Canceled)
}

func TestLanguageViewsKeepOneCacheBudget(t *testing.T) {
	t.Parallel()

	fetcher := provenanceFetcher(func(_ context.Context, raw string) ([]byte, error) {
		if strings.Contains(raw, pageListPath) {
			language, _, _ := strings.Cut(strings.Split(raw, pageListPath+"/")[1], "/")
			return []byte("/" + language + "/guide\n"), nil
		}

		if strings.HasSuffix(raw, "/llms.txt") {
			return nil, errors.New("curated catalogue unavailable")
		}

		return []byte(strings.Repeat("x", 128)), nil
	})

	svc := NewService(fetcher, testBaseURL, ServiceConfig{IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 256})
	for _, language := range SupportedLanguages() {
		_, err := svc.Get(t.Context(), language+"/guide")
		require.NoError(t, err)
	}

	require.LessOrEqual(t, svc.pages.curBytes, int64(256))
	_, _, retained := svc.pages.Get("en/guide")
	require.False(t, retained, "later languages must evict older pages within the shared budget")
}

func TestGetLocalizedPage(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pagelist/en/free-pro-team@latest":
			_, _ = fmt.Fprintln(w, "/en/guide")
		case "/api/pagelist/ja/free-pro-team@latest":
			_, _ = fmt.Fprintln(w, "/ja/guide")
		case "/ja/guide.md":
			_, _ = fmt.Fprintln(w, "# ガイド\n\n日本語の本文")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)
	client, err := NewClient(origin.URL, WithRateLimit(1000))
	require.NoError(t, err)

	svc := NewService(client, origin.URL, ServiceConfig{IndexTTL: time.Hour, PageTTL: time.Hour})
	page, err := svc.Get(t.Context(), "ja/guide")
	require.NoError(t, err, "a localized slug must use its own catalogue")
	require.Contains(t, string(page.Content), "日本語の本文")
	require.Equal(t, "ja", page.Source.Language)
	require.False(t, page.Catalogue.Degraded, "English llms.txt is not a dependency of Japanese pages")
}

func TestLocalizedHeadingAnchor(t *testing.T) {
	t.Parallel()

	body, err := ExtractHeading([]byte("# ガイド\n\n## 環境の設定\n\n本文\n\n## その他\n\n別の本文\n"), "#環境の設定")
	require.NoError(t, err, "non-Latin heading anchors must remain addressable")
	require.Contains(t, string(body), "本文")
	require.NotContains(t, string(body), "その他")
}

func TestLocalizedHeadingPreservesSourceBytes(t *testing.T) {
	t.Parallel()

	for _, ending := range []string{"\n", "\r\n", ""} {
		t.Run(fmt.Sprintf("ending_%q", ending), func(t *testing.T) {
			t.Parallel()

			body := "## Guía rápida\r\n\r\nTexto original." + ending
			got, err := ExtractHeading([]byte("# Documento\r\n\r\n"+body), "Guía rápida")
			require.NoError(t, err)
			require.Equal(t, body, string(got), "heading retrieval must preserve original line endings")
			sections := SplitSections([]byte(body))
			require.Len(t, sections, 1)
			require.Equal(t, body, string(sections[0].Body), "section retrieval must preserve original line endings")
		})
	}
}

func TestSearchRejectsForeignLanguagePayload(t *testing.T) {
	t.Parallel()

	for _, slugs := range [][]string{{"/en/guide"}, {"/ja/guide", "/en/guide"}} {
		t.Run(strings.Join(slugs, ","), func(t *testing.T) {
			t.Parallel()

			fetcher := provenanceFetcher(func(_ context.Context, raw string) ([]byte, error) {
				if strings.Contains(raw, searchPath) {
					hits := make([]map[string]string, 0, len(slugs))
					for _, slug := range slugs {
						hits = append(hits, map[string]string{"url": slug, "title": "origin title"})
					}

					return json.Marshal(map[string]any{"hits": hits})
				}

				return []byte("/ja/guide\n"), nil
			})
			service := NewService(fetcher, testBaseURL, ServiceConfig{IndexTTL: time.Hour})
			view, err := service.ForLanguage("ja")
			require.NoError(t, err)
			result, err := view.SearchWithSources(t.Context(), "guide", 5)
			require.NoError(t, err)
			require.True(t, result.Coverage.Degraded, "foreign-language hits must report a failed upstream language contract")
			require.Contains(t, result.Coverage.Mode, "fallback")
			require.Len(t, result.Hits, 1)
			require.Equal(t, "ja/guide", result.Hits[0].Doc.Slug)
		})
	}
}

func TestUnicodeSnippets(t *testing.T) {
	t.Parallel()
	t.Run("upstream snippet boundary", func(t *testing.T) {
		t.Parallel()

		snippet := apiSnippet("", []string{"a" + strings.Repeat("漢", 100)}, nil)
		require.True(t, utf8.ValidString(snippet), "snippet truncation must preserve complete characters")
	})
	t.Run("case mapping changes byte width", func(t *testing.T) {
		t.Parallel()

		body := strings.Repeat("İ", 100) + "needle"
		snippet := snippetAround(strings.ToLower(body), body, "needle")
		require.Contains(t, snippet, "needle", "folded search offsets must address the original text")
		require.True(t, utf8.ValidString(snippet))
	})
}
