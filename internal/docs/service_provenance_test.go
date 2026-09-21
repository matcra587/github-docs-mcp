package docs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type componentOrigin struct {
	mu                         sync.Mutex
	index, list                string
	indexErr, listErr, pageErr bool
}

func (f *componentOrigin) Fetch(_ context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.HasSuffix(url, "/llms.txt") {
		if f.indexErr {
			return nil, errors.New("index unavailable")
		}

		return []byte(f.index), nil
	}

	if strings.Contains(url, "/api/pagelist/") {
		if f.listErr {
			return nil, errors.New("pagelist unavailable")
		}

		return []byte(f.list), nil
	}

	if f.pageErr {
		return nil, errors.New("page unavailable")
	}

	return []byte("# Alpha\n\nneedle\n"), nil
}

func TestCatalogueRetainsFailedComponent(t *testing.T) {
	t.Parallel()

	f := &componentOrigin{index: "* [Alpha](https://docs.github.com/en/alpha): curated", list: "/en/alpha\n/en/gamma\n"}
	s := NewService(f, "https://docs.github.com", ServiceConfig{CacheMaxBytes: 1024, PageTTL: time.Hour})
	entries, err := s.List(t.Context(), "", 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	f.mu.Lock()
	f.listErr = true
	f.mu.Unlock()

	entries, err = s.List(t.Context(), "", 0)
	require.NoError(t, err)
	require.Len(t, entries, 2, "failed pagelist refresh must retain last-known-good entries")
}

func TestCataloguePageListOnlyColdStart(t *testing.T) {
	t.Parallel()

	f := &componentOrigin{indexErr: true, list: "/en/gamma\n"}
	s := NewService(f, "https://docs.github.com", ServiceConfig{CacheMaxBytes: 1024})
	entries, err := s.List(t.Context(), "", 0)
	require.NoError(t, err, "usable pagelist must survive llms.txt outage")
	require.Len(t, entries, 1)
}

func TestSourceTimestampsSurviveCache(t *testing.T) {
	t.Parallel()

	for _, capacity := range []int64{1, 1024} {
		t.Run(fmt.Sprintf("capacity_%d", capacity), func(t *testing.T) {
			t.Parallel()

			clock := newTestClock()
			f := &componentOrigin{index: "* [Alpha](https://docs.github.com/en/alpha): curated", list: "/en/alpha\n"}
			disk, err := NewDiskCache(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, disk.root.Close()) })

			cfg := ServiceConfig{Now: clock.Now, IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: capacity, Disk: disk}
			s := NewService(f, testBaseURL, cfg)
			page, err := s.Get(t.Context(), "en/alpha")
			require.NoError(t, err)
			require.Equal(t, clock.Now(), page.Source.FetchedAt)
			require.Equal(t, "fresh", page.Source.Freshness)
			require.Equal(t, testBaseURL+"/en/alpha", page.Source.URL)
			clock.Advance(time.Minute)

			if capacity > 1 {
				hit, err := s.Get(t.Context(), "en/alpha")
				require.NoError(t, err)
				require.Equal(t, page.Source, hit.Source)
			}
			// Hydrate with room for the body even when the first service skipped memory caching.
			cfg.CacheMaxBytes = 1024
			reloaded := NewService(f, testBaseURL, cfg)
			hit, err := reloaded.Get(t.Context(), "en/alpha")
			require.NoError(t, err)
			require.Equal(t, page.Source, hit.Source)
			clock.Advance(2 * time.Hour)
			f.mu.Lock()
			f.indexErr, f.listErr, f.pageErr = true, true, true
			f.mu.Unlock()

			stale, err := reloaded.Get(t.Context(), "en/alpha")
			require.NoError(t, err)
			require.True(t, stale.Stale)
			require.Equal(t, "stale", stale.Source.Freshness)
			require.Equal(t, page.Source.FetchedAt, stale.Source.FetchedAt)
			require.True(t, stale.Catalogue.Degraded)
		})
	}
}

func TestCatalogueComponentRecovery(t *testing.T) {
	t.Parallel()

	for _, failure := range []string{"llms", "pagelist", "empty", "malformed", "truncated"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()

			clock := newTestClock()
			f := &componentOrigin{index: "* [Alpha](https://docs.github.com/en/alpha): curated", list: "/en/alpha\n/en/gamma\n"}
			s := NewService(f, testBaseURL, ServiceConfig{Now: clock.Now, IndexTTL: time.Hour})
			initial, err := s.Catalogue(t.Context(), "", 0)
			require.NoError(t, err)
			require.False(t, initial.Coverage.Degraded)
			clock.Advance(2 * time.Hour)
			f.mu.Lock()
			switch failure {
			case "llms":
				f.indexErr = true
			case "pagelist":
				f.listErr = true
			case "empty":
				f.list = ""
			case "malformed":
				f.list = "/en/alpha\n<html>wrong</html>"
			case "truncated":
				f.list = "/en/alpha\n" + strings.Repeat("x", 1024*1024+1)
			}
			f.mu.Unlock()

			partial, err := s.Catalogue(t.Context(), "", 0)
			require.NoError(t, err)
			require.Equal(t, initial.Docs, partial.Docs)
			require.True(t, partial.Coverage.Degraded)

			failedIndex := 1
			if failure == "llms" {
				failedIndex = 0
			}

			require.Equal(t, initial.Coverage.Sources[failedIndex].FetchedAt, partial.Coverage.Sources[failedIndex].FetchedAt)
			require.Equal(t, "stale", partial.Coverage.Sources[failedIndex].Freshness)
			require.Equal(t, "fresh", partial.Coverage.Sources[1-failedIndex].Freshness)
			f.mu.Lock()
			f.indexErr, f.listErr = false, false
			f.list = "/en/alpha\n/en/delta\n"
			f.mu.Unlock()
			clock.Advance(time.Minute)

			recovered, err := s.Catalogue(t.Context(), "", 0)
			require.NoError(t, err)
			require.False(t, recovered.Coverage.Degraded)
			require.Equal(t, partial.Coverage.Sources[1-failedIndex].FetchedAt, recovered.Coverage.Sources[1-failedIndex].FetchedAt, "healthy component must not refetch")
			require.Equal(t, "Alpha", recovered.Docs[0].Title, "curated metadata wins")
		})
	}
}

func TestCatalogueColdCoverageAndDisk(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"llms only", "pagelist only", "neither"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			f := &componentOrigin{index: "* [Alpha](https://docs.github.com/en/alpha): curated", list: "/en/gamma\n", indexErr: mode != "llms only", listErr: mode != "pagelist only"}
			disk, err := NewDiskCache(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, disk.root.Close()) })

			cfg := ServiceConfig{IndexTTL: time.Hour, Disk: disk}
			s := NewService(f, testBaseURL, cfg)

			catalogue, err := s.Catalogue(t.Context(), "", 0)
			if mode == "neither" {
				require.ErrorIs(t, err, ErrIndexUnavailable)
				return
			}

			require.NoError(t, err)
			require.True(t, catalogue.Coverage.Degraded)
			require.Len(t, catalogue.Docs, 1)
			f.mu.Lock()
			f.indexErr, f.listErr = true, true
			f.mu.Unlock()
			restored := NewService(f, testBaseURL, cfg)
			loaded, err := restored.Catalogue(t.Context(), "", 0)
			require.NoError(t, err)
			require.Equal(t, catalogue.Docs, loaded.Docs)
			require.Equal(t, catalogue.Coverage, loaded.Coverage)
		})
	}
}

func TestSearchCoverageEmptyAndStale(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	f := &componentOrigin{index: "* [Alpha](https://docs.github.com/en/alpha): curated", list: "/en/alpha\n"}
	s := NewService(f, testBaseURL, ServiceConfig{Now: clock.Now, IndexTTL: time.Hour, PageTTL: time.Minute, CacheMaxBytes: 1024})
	_, err := s.Get(t.Context(), "en/alpha")
	require.NoError(t, err)
	clock.Advance(2 * time.Minute)

	for _, query := range []string{"needle", "no-match"} {
		result, err := s.SearchWithSources(t.Context(), query, 10)
		require.NoError(t, err)
		require.Contains(t, result.Coverage.Mode, "fallback")
		require.True(t, result.Coverage.Degraded)
		require.True(t, result.Coverage.StaleBodies)

		if query == "needle" {
			require.Len(t, result.Hits, 1)
			require.Equal(t, "stale", result.Hits[0].Source.Freshness)
		} else {
			require.Empty(t, result.Hits)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = s.Get(ctx, "en/alpha")
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Catalogue(ctx, "", 0)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.SearchWithSources(ctx, "needle", 1)
	require.ErrorIs(t, err, context.Canceled)
}

type testClock struct{ nanos atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.nanos.Store(time.Date(2026, 9, 20, 0, 0, 0, 123, time.UTC).UnixNano())

	return c
}
func (c *testClock) Now() time.Time          { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *testClock) Advance(d time.Duration) { c.nanos.Add(int64(d)) }

type provenanceFetcher func(context.Context, string) ([]byte, error)

func (f provenanceFetcher) Fetch(ctx context.Context, url string) ([]byte, error) { return f(ctx, url) }

func TestCatalogueIndependentRefreshAndCancellation(t *testing.T) {
	t.Parallel()

	started, release := make(chan struct{}), make(chan struct{})
	f := provenanceFetcher(func(ctx context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "/llms.txt") {
			select {
			case <-release:
				return nil, errors.New("llms timeout")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		close(started)

		return []byte("/en/alpha\n"), nil
	})
	s := NewService(f, testBaseURL, ServiceConfig{IndexTTL: time.Hour})
	ctx, cancel := context.WithCancel(t.Context())
	completed := make(chan error, 1)

	go func() { _, err := s.Catalogue(ctx, "", 0); completed <- err }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pagelist must start independently of stalled llms.txt")
	}

	cancel()
	require.ErrorIs(t, <-completed, context.Canceled)
	close(release)

	catalogue, err := s.Catalogue(t.Context(), "", 0)
	require.NoError(t, err)
	require.Len(t, catalogue.Docs, 1)
	require.True(t, catalogue.Coverage.Degraded)
}

func TestForcedCatalogueRefreshAfterOrdinaryFlight(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	started, release := make(chan struct{}), make(chan struct{})

	var indexCalls, listCalls atomic.Int32

	f := provenanceFetcher(func(ctx context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "/llms.txt") {
			indexCalls.Add(1)
			return []byte("* [Alpha](https://docs.github.com/en/alpha): curated"), nil
		}

		if listCalls.Add(1) == 2 {
			close(started)

			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return []byte("/en/alpha\n"), nil
	})
	s := NewService(f, testBaseURL, ServiceConfig{Now: clock.Now, IndexTTL: time.Hour})
	_, err := s.Catalogue(t.Context(), "", 0)
	require.NoError(t, err)
	s.mu.Lock()
	s.listed.at = clock.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	ordinary := make(chan error, 1)

	go func() { _, err := s.Catalogue(t.Context(), "", 0); ordinary <- err }()

	<-started

	forced := make(chan error, 1)

	go func() { _, err := s.refreshIndex(t.Context(), true); forced <- err }()

	close(release)
	require.NoError(t, <-ordinary)
	require.NoError(t, <-forced)
	require.EqualValues(t, 2, indexCalls.Load(), "forced refresh must not be swallowed by a flight that skipped fresh llms.txt")
}

func TestUpstreamSearchProvenance(t *testing.T) {
	t.Parallel()
	f := newFixtureOrigin(t)

	s := newTestService(t, f)
	for _, query := range []string{"alpha", "does-not-exist"} {
		result, err := s.SearchWithSources(t.Context(), query, 3)
		require.NoError(t, err)
		require.Contains(t, result.Coverage.Mode, "upstream")
		require.False(t, result.Coverage.Degraded)
		require.Len(t, result.Coverage.Sources, 3)
		require.False(t, result.Coverage.Sources[2].FetchedAt.IsZero())

		if query == "alpha" {
			require.Len(t, result.Hits, 1)
			require.Equal(t, "unknown", result.Hits[0].Source.Freshness)
		} else {
			require.Empty(t, result.Hits)
		}
	}
}
