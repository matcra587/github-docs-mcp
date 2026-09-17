package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureOrigin serves a two-doc index and pages, with per-path hit counting
// and a switchable failure mode.
type fixtureOrigin struct {
	srv      *httptest.Server
	failing  atomic.Bool
	pageWait time.Duration

	mu   sync.Mutex
	hits map[string]int
}

func newFixtureOrigin(t *testing.T) *fixtureOrigin {
	t.Helper()

	f := &fixtureOrigin{hits: make(map[string]int)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		f.mu.Unlock()

		if f.failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if f.pageWait > 0 {
			time.Sleep(f.pageWait)
		}

		switch r.URL.Path {
		case "/llms.txt":
			// The curated catalogue: prose entries, no .md suffix, asterisk
			// bullets, the shape the real origin publishes.
			_, _ = fmt.Fprintf(w, "* [Alpha](%s/en/alpha): first page\n* [Beta](%s/en/beta): second page\n", f.srv.URL, f.srv.URL)
		case "/api/pagelist/" + docsLanguage + "/" + docsVersion:
			// Wider than llms.txt: en/gamma exists but is not curated.
			_, _ = io.WriteString(w, "/en/alpha\n/en/beta\n/en/gamma\n")
		case "/api/search/v1":
			f.writeSearch(w, r.URL.Query().Get("query"), r.URL.Query().Get("size"))
		default:
			// Pages are served at the .md form only, as the real origin does.
			slug, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".md")

			body, known := fixturePages[slug]
			if !ok || !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// fixturePages is the origin's page corpus, keyed by slug. Only alpha and beta
// are in the curated catalogue; gamma is reachable via the page list alone.
var fixturePages = map[string]string{
	"en/alpha": "# Alpha\n\ncontent alpha\n",
	"en/beta":  "# Beta\n\ncontent beta\n",
	"en/gamma": "# Gamma\n\ncontent gamma with keyword zebra\n",
}

// writeSearch answers the origin's search endpoint by scanning the page corpus
// server-side, which is what makes search cover pages no caller has fetched.
//
// It honours size the way the real endpoint does. That matters: a fixture that
// ignored it would make every assertion about the origin path's limit handling
// vacuously true.
func (f *fixtureOrigin) writeSearch(w http.ResponseWriter, query, size string) {
	type hit struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Breadcrumbs string `json:"breadcrumbs"`
		Highlights  struct {
			Content []string `json:"content"`
		} `json:"highlights"`
	}

	resp := struct {
		Hits []hit `json:"hits"`
	}{Hits: []hit{}}

	// Deterministic order regardless of map iteration.
	for _, slug := range []string{"en/alpha", "en/beta", "en/gamma"} {
		body := fixturePages[slug]
		if query == "" || !strings.Contains(strings.ToLower(body), strings.ToLower(query)) {
			continue
		}

		h := hit{URL: "/" + slug, Title: slug, Breadcrumbs: "Fixture / " + slug}
		h.Highlights.Content = []string{"<mark>" + query + "</mark> in " + slug}
		resp.Hits = append(resp.Hits, h)
	}

	// The real endpoint always returns at most size hits.
	if n, err := strconv.Atoi(size); err == nil && n >= 0 && n < len(resp.Hits) {
		resp.Hits = resp.Hits[:n]
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fixtureOrigin) hitCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hits[path]
}

func newTestService(t *testing.T, f *fixtureOrigin) *Service {
	t.Helper()

	client, err := NewClient(f.srv.URL, WithRateLimit(1000))
	require.NoError(t, err)

	return NewService(client, f.srv.URL, ServiceConfig{
		IndexTTL:      time.Hour,
		PageTTL:       time.Hour,
		CacheMaxBytes: 1 << 20,
	})
}

func TestServiceGet(t *testing.T) {
	t.Parallel()

	t.Run("cold fetch then cached", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)

		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)
		is.False(p.Stale)

		_, err = svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		is.Equal(1, f.hitCount("/en/alpha.md"), "expected 1 origin hit")
	})

	t.Run("unknown slug no origin fetch", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)

		_, err := svc.Get(t.Context(), "en/nope")
		is.ErrorIs(err, ErrNotFound, "expected ErrNotFound") //nolint:testifylint // the zero-fetch check below is an independent verification

		is.Equal(0, f.hitCount("/en/nope.md"), "unknown slug must not hit origin")
	})

	t.Run("origin failure serves stale", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)
		svc.pageTTL = time.Nanosecond // warm entry expires immediately

		_, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		f.failing.Store(true)

		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err, "stale should be served")

		is.True(p.Stale, "expected stale alpha content")
		is.Equal("# Alpha\n\ncontent alpha", strings.TrimSpace(string(p.Content)), "expected stale alpha content")
	})

	t.Run("origin failure cold cache errors", func(t *testing.T) {
		t.Parallel()

		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)

		_, err := svc.Get(t.Context(), "en/alpha") // warm the index
		must.NoError(err)

		f.failing.Store(true)

		_, err = svc.Get(t.Context(), "en/beta")

		var fe *FetchError
		must.ErrorAs(err, &fe, "expected FetchError")
	})

	t.Run("index failure serves stale index", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)
		svc.indexTTL = time.Nanosecond

		_, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		f.failing.Store(true)

		// Index is expired and refresh fails; the stale index must still
		// resolve slugs (page comes from cache).
		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err, "stale index should serve")
		is.NotEmpty(p.Content, "stale index should serve")
	})

	t.Run("cancelled caller gets cancellation not stale", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)
		svc.pageTTL = time.Nanosecond

		_, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		f.pageWait = 300 * time.Millisecond

		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		_, err = svc.Get(ctx, "en/alpha")
		is.ErrorIs(err, context.Canceled, "cancelled caller must see cancellation")
	})

	t.Run("origin failure cool-down serves stale without refetching", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)
		svc.pageTTL = time.Nanosecond

		_, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		f.failing.Store(true)

		// First stale-path call pays the fetch failure and starts the
		// cool-down window.
		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err, "expected stale serve")
		must.True(p.Stale, "expected stale serve")

		before := f.hitCount("/en/alpha.md")

		// Within the cool-down the stale copy must come back instantly with
		// zero further origin traffic.
		for range 3 {
			p, err := svc.Get(t.Context(), "en/alpha")
			must.NoError(err, "expected stale during cool-down")
			must.True(p.Stale, "expected stale during cool-down")
		}

		is.Equal(before, f.hitCount("/en/alpha.md"), "cool-down still hit origin")
	})

	t.Run("abandoned fetch result is still cached", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		f.pageWait = 150 * time.Millisecond
		svc := newTestService(t, f)

		_, err := svc.List(t.Context(), "", 0)
		must.NoError(err)

		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		_, _ = svc.Get(ctx, "en/alpha") // abandoned by its only caller

		// Give the detached fetch time to complete and cache.
		time.Sleep(400 * time.Millisecond)

		before := f.hitCount("/en/alpha.md")

		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)
		is.False(p.Stale, "expected fresh cached page")

		is.Equal(before, f.hitCount("/en/alpha.md"), "abandoned fetch result was discarded; origin re-hit")
	})

	t.Run("cool-down expires and origin recovers", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)
		svc.pageTTL = time.Nanosecond

		_, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err)

		f.failing.Store(true)

		p, err := svc.Get(t.Context(), "en/alpha")
		must.NoError(err, "expected stale")
		must.True(p.Stale, "expected stale")

		// Origin heals; force the cool-down clock past its window.
		f.failing.Store(false)

		svc.failMu.Lock()
		svc.lastFailAt = time.Now().Add(-2 * failCooldown)
		svc.failMu.Unlock()

		p, err = svc.Get(t.Context(), "en/alpha")
		must.NoError(err, "post-cooldown fetch failed")

		is.False(p.Stale, "expected fresh content after cool-down expiry and origin recovery")
	})

	// (Cool-down clear-on-success is covered behaviourally by "cool-down
	// expires and origin recovers" above: that a later fetch resumes and
	// returns fresh content. A test poking the private cool-down flag directly
	// would assert the mechanism, not the behaviour.)

	t.Run("concurrent fetches collapse via singleflight", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		f.pageWait = 100 * time.Millisecond
		svc := newTestService(t, f)

		_, err := svc.List(t.Context(), "", 0)
		must.NoError(err) // warm index before racing

		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				_, err := svc.Get(context.Background(), "en/alpha")
				is.NoError(err)
			})
		}

		wg.Wait()

		is.Equal(1, f.hitCount("/en/alpha.md"), "singleflight should collapse to 1 hit")
	})

	t.Run("first caller cancel does not fail waiters", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		f.pageWait = 200 * time.Millisecond
		svc := newTestService(t, f)

		_, err := svc.List(t.Context(), "", 0)
		must.NoError(err)

		ctx1, cancel1 := context.WithCancel(t.Context())

		var wg sync.WaitGroup

		wg.Go(func() {
			_, _ = svc.Get(ctx1, "en/alpha")
		})

		time.Sleep(50 * time.Millisecond) // let caller 1 start the fetch

		var (
			p2   Page
			err2 error
		)

		wg.Go(func() {
			p2, err2 = svc.Get(context.Background(), "en/alpha")
		})

		time.Sleep(20 * time.Millisecond)
		cancel1()
		wg.Wait()

		must.NoError(err2, "waiter must survive first caller's cancel")
		is.Equal("# Alpha\n\ncontent alpha", strings.TrimSpace(string(p2.Content)), "waiter must survive first caller's cancel")
	})
}

func TestSearchUsesOrigin(t *testing.T) {
	t.Parallel()

	t.Run("surfaces a page no caller has fetched", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)

		// "zebra" appears only in the body of en/gamma, which is not in the
		// curated catalogue and has never been fetched. Only a server-side
		// index can find it.
		hits, err := svc.Search(t.Context(), "zebra", 0)
		must.NoError(err)

		must.Len(hits, 1, "origin search did not surface the cold-page body match")
		is.Equal("en/gamma", hits[0].Doc.Slug)

		is.True(hits[0].MatchedBody, "expected a body match")
		is.Equal("Fixture / en/gamma - zebra in en/gamma", hits[0].Snippet, "breadcrumbs and de-marked highlight expected")

		is.Positive(f.hitCount("/api/search/v1"), "search endpoint was never called")
	})

	t.Run("catalogue metadata wins over a synthesised doc", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		svc := newTestService(t, newFixtureOrigin(t))

		hits, err := svc.Search(t.Context(), "content alpha", 0)
		must.NoError(err)

		must.Len(hits, 1)
		is.Equal("en/alpha", hits[0].Doc.Slug)
		// The hit's own title is the bare slug; the curated entry must win.
		is.Equal("Alpha", hits[0].Doc.Title)
		is.Equal("first page", hits[0].Doc.Description)
	})

	t.Run("falls back to local scoring when the origin fails", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f := newFixtureOrigin(t)
		svc := newTestService(t, f)

		// Warm the catalogue while the origin is healthy, then break it.
		_, err := svc.List(t.Context(), "", 0)
		must.NoError(err)

		f.failing.Store(true)

		hits, err := svc.Search(t.Context(), "Alpha", 0)
		must.NoError(err, "an origin outage must not fail search outright")

		must.NotEmpty(hits, "local fallback found nothing")
		is.Equal("en/alpha", hits[0].Doc.Slug, "metadata match should still rank")

		// The corpus-wide body match is exactly what is lost in this mode.
		degraded, err := svc.Search(t.Context(), "zebra", 0)
		must.NoError(err)
		is.Empty(degraded, "body match is unavailable without the origin")
	})
}

func TestSearchLimitSemantics(t *testing.T) {
	t.Parallel()

	// "content" is in the body of all three fixture pages.
	const query = "content"

	t.Run("origin path treats limit<=0 as no cap", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		svc := newTestService(t, newFixtureOrigin(t))

		hits, err := svc.Search(t.Context(), query, 0)
		must.NoError(err)

		// Regression: size was previously clamped with max(limit, 1), so a
		// limit of zero asked the endpoint for a single result and silently
		// meant the opposite of what it means everywhere else in the package.
		is.Len(hits, len(fixturePages), "limit<=0 must not cap the origin path")
	})

	t.Run("origin path honours a positive limit", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		svc := newTestService(t, newFixtureOrigin(t))

		hits, err := svc.Search(t.Context(), query, 2)
		must.NoError(err)

		is.Len(hits, 2)
	})

	t.Run("both paths agree on what limit means", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		svc := newTestService(t, newFixtureOrigin(t))

		// Warm the catalogue and two page bodies so the local path has
		// something to match, then push the service into its outage mode.
		_, err := svc.List(t.Context(), "", 0)
		must.NoError(err)

		for _, slug := range []string{"en/alpha", "en/beta"} {
			_, gerr := svc.Get(t.Context(), slug)
			must.NoError(gerr)
		}

		svc.noteOriginFailure()

		uncapped, err := svc.Search(t.Context(), query, 0)
		must.NoError(err)

		is.Len(uncapped, 2, "limit<=0 must not cap the local path either")

		capped, err := svc.Search(t.Context(), query, 1)
		must.NoError(err)

		is.Len(capped, 1, "a positive limit caps the local path")
	})
}
