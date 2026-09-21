package docs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheDecisions(t *testing.T) {
	t.Parallel()
	f := newFixtureOrigin(t)
	s := newTestService(t, f)

	var logs bytes.Buffer

	s.pages.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	get := func(want string) CacheDecision {
		t.Helper()
		ctx, trace := TraceCache(t.Context())
		page, err := s.Get(ctx, "en/alpha")
		require.NoError(t, err)
		require.Equal(t, want == "stale-serve", page.Stale)

		decisions := trace.Decisions()
		require.Len(t, decisions, 2)
		d := decisions[1]
		require.Equal(t, "page:en/alpha", d.Key)
		require.Equal(t, want, d.Status)

		return d
	}
	d := get("miss")
	require.Nil(t, d.AgeMS)
	require.Nil(t, d.RemainingTTLMS)
	d = get("hit")
	require.NotNil(t, d.AgeMS)
	require.Positive(t, *d.RemainingTTLMS)
	s.pages.putAged("en/alpha", []byte(fixturePages["en/alpha"]), time.Hour, time.Now().Add(-2*time.Hour))
	f.failing.Store(true)

	d = get("stale-serve")
	require.GreaterOrEqual(t, *d.AgeMS, (2 * time.Hour).Milliseconds())
	require.Zero(t, *d.RemainingTTLMS)

	before := f.hitCount("/en/alpha.md")

	get("stale-serve")
	require.Equal(t, before, f.hitCount("/en/alpha.md"), "cooldown must not retry")

	statuses := map[string]bool{}

	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var event struct {
			Key, Status string
			AgeMS       *int64 `json:"age_ms"`
		}
		require.NoError(t, decoder.Decode(&event))

		if event.Key == "page:en/alpha" {
			statuses[event.Status] = true
		}
	}

	for _, status := range []string{"miss", "hit", "write", "stale-serve"} {
		require.True(t, statuses[status], "missing debug decision %s", status)
	}
}

func TestCacheEvictionLog(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	c := NewCache(3)
	c.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.Put("first", []byte("abc"), time.Hour)
	c.Put("second", []byte("xyz"), time.Hour)
	require.Contains(t, logs.String(), `"status":"eviction"`)
	require.Contains(t, logs.String(), `"key":"page:first"`)
}

func TestDiskAndIndexCacheDecisions(t *testing.T) {
	t.Parallel()
	f := newFixtureOrigin(t)
	client, err := NewClient(f.srv.URL, WithRateLimit(1000))
	require.NoError(t, err)
	disk, err := NewDiskCache(t.TempDir())
	require.NoError(t, err)

	config := ServiceConfig{IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1 << 20, Disk: disk}
	first := NewService(client, f.srv.URL, config)
	_, err = first.Get(t.Context(), "en/alpha")
	require.NoError(t, err)

	second := NewService(client, f.srv.URL, config)
	f.failing.Store(true)

	ctx, trace := TraceCache(t.Context())
	_, err = second.Get(ctx, "en/alpha")
	require.NoError(t, err)

	decisions := trace.Decisions()
	require.Len(t, decisions, 2)

	for _, d := range decisions {
		require.Equal(t, "hit", d.Status)
		require.Equal(t, "disk", d.Source)
		require.NotNil(t, d.AgeMS)
		require.Positive(t, *d.RemainingTTLMS)
	}

	second.mu.Lock()
	second.curated.at = time.Now().Add(-2 * time.Hour)
	second.listed.at = second.curated.at
	second.curated.failedAt = time.Now()
	second.listed.failedAt = time.Now()
	second.mu.Unlock()
	second.noteOriginFailure()

	ctx, trace = TraceCache(t.Context())
	_, err = second.List(ctx, "", 1)
	require.NoError(t, err)
	require.Equal(t, "stale-serve", trace.Decisions()[0].Status)
	require.Zero(t, *trace.Decisions()[0].RemainingTTLMS)
}

func TestOfflineSearchCacheDecisions(t *testing.T) {
	t.Parallel()
	f := newFixtureOrigin(t)
	s := newTestService(t, f)
	_, err := s.Get(t.Context(), "en/alpha")
	require.NoError(t, err)
	s.pages.putAged("en/alpha", []byte("# Alpha\n\nneedle"), time.Hour, time.Now().Add(-2*time.Hour))
	f.failing.Store(true)

	ctx, trace := TraceCache(t.Context())
	hits, err := s.Search(ctx, "needle", 1)
	require.NoError(t, err)
	require.Len(t, hits, 1)

	decisions := trace.Decisions()
	require.Len(t, decisions, 3)
	require.Equal(t, "index", decisions[0].Key)
	require.Equal(t, "page:en/alpha", decisions[1].Key)
	require.Equal(t, "stale-serve", decisions[1].Status)
	require.Zero(t, *decisions[1].RemainingTTLMS)
	require.Equal(t, "fallback", decisions[2].Status)
}
