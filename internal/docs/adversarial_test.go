package docs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostileClient points a client at a fixture but lets tests drive nasty slugs.
func hostileService(t *testing.T) (*Service, *atomicHits) {
	t.Helper()

	hits := &atomicHits{seen: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.record(r.URL.Path)

		if r.URL.Path == "/llms.txt" {
			_, _ = w.Write([]byte("- [Hooks](" + hits.base + "/en/hooks.md): hook docs\n"))
			return
		}

		if r.URL.Path == "/en/hooks.md" {
			_, _ = w.Write([]byte("# Hooks\n\nbody\n"))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	hits.base = srv.URL

	client, err := NewClient(srv.URL, WithRateLimit(1000))
	require.NoError(t, err)

	svc := NewService(client, srv.URL, ServiceConfig{IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1 << 20})

	return svc, hits
}

type atomicHits struct {
	mu   sync.Mutex
	seen map[string]int
	base string
}

func (a *atomicHits) record(p string) {
	a.mu.Lock()
	a.seen[p]++
	a.mu.Unlock()
}

func (a *atomicHits) count(p string) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.seen[p]
}

// TestHostileSlugs proves no crafted slug reaches the origin as anything but a
// clean prefix-joined path, and unknown ones never fetch at all.
func TestHostileSlugs(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	svc, hits := hostileService(t)

	hostile := []string{
		"../../../etc/passwd",
		"https://evil.example.com/x",
		"//evil.example.com/x",
		"en/hooks/../../../../secret",
		"en/hooks%00.md",
		"en/hooks\x00null",
		strings.Repeat("a/", 5000) + "deep",
		"",
	}

	for _, slug := range hostile {
		_, err := svc.Get(context.Background(), slug)
		is.Error(err, "hostile slug %q unexpectedly succeeded", slug) //nolint:testifylint // loop checks every slug; require would stop at the first
	}

	// None of the crafted slugs is the one real page, so the only page fetch
	// permitted is zero.
	is.Equal(0, hits.count("/en/hooks.md"), "hostile slugs triggered page fetches")
	// The origin itself must never have seen a traversal or absolute-host path.
	hits.mu.Lock()
	for path := range hits.seen {
		is.NotContains(path, "..", "origin saw dangerous path %q", path)
		is.NotContains(path, "evil", "origin saw dangerous path %q", path)
	}
	hits.mu.Unlock()
}

// TestConcurrentCacheHammer runs Get/List/Search concurrently on shared state
// to shake out races the -race detector would catch.
func TestConcurrentCacheHammer(t *testing.T) {
	t.Parallel()

	svc, _ := hostileService(t)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			ctx := context.Background()

			switch i % 3 {
			case 0:
				_, _ = svc.Get(ctx, "en/hooks")
			case 1:
				_, _ = svc.List(ctx, "", 0)
			default:
				_, _ = svc.Search(ctx, "hook body", 0)
			}
		})
	}

	wg.Wait()
}

// TestOffsetMidRune ensures windowing never returns invalid UTF-8 by cutting a
// multibyte rune, or, if it can, that we know the exact contract.
func TestPaginateMidRune(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	// 3-byte runes packed with no newline, forcing the cut inside the buffer.
	content := []byte(strings.Repeat("あ", maxContentBytes))

	w := Paginate(content, 0)
	is.True(w.Truncated(), "expected truncation")

	is.True(utf8.Valid(w.Content), "window content is not valid UTF-8: a rune was split")

	// Every window must decode cleanly and the full set must reassemble to
	// the original bytes with no loss or corruption.
	var got []byte

	for off := 0; ; {
		win := Paginate(content, off)
		is.True(utf8.Valid(win.Content), "window at offset %d is not valid UTF-8", off)

		got = append(got, win.Content...)
		if !win.Truncated() {
			break
		}

		off = win.Next
	}

	is.Equal(string(content), string(got), "windows do not reassemble: got %d bytes, want %d", len(got), len(content))
}
