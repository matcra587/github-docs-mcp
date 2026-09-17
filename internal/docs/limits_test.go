package docs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLongSlugDiskKey drives a slug whose URL-escaped filename would exceed the
// common 255-byte filesystem limit. Store must fail cleanly, never panic, and
// the service must still serve the page from memory.
func TestLongSlugDiskKey(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	longSeg := strings.Repeat("a", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/llms.txt" {
			_, _ = w.Write([]byte("- [Long](" + rBase(r) + "/en/" + longSeg + ".md): long slug\n")) //nolint:gosec // test fixture bytes
			return
		}

		_, _ = w.Write([]byte("# Long\n\nbody\n"))
	}))
	t.Cleanup(srv.Close)

	dc, err := NewDiskCache(t.TempDir())
	must.NoError(err)

	client, err := NewClient(srv.URL, WithRateLimit(1000))
	must.NoError(err)

	svc := NewService(client, srv.URL, ServiceConfig{
		IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1 << 20, Disk: dc,
	})

	p, err := svc.Get(context.Background(), "en/"+longSeg)
	must.NoError(err, "long slug should still serve from memory")

	is.Equal("# Long\n\nbody", strings.TrimSpace(string(p.Content)), "wrong content")
}

func rBase(r *http.Request) string {
	return "http://" + r.Host
}

// TestHugeIndex checks the parser stays linear on a large catalogue and the
// regex does not blow up.
func TestHugeIndex(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	var b strings.Builder
	for i := range 10000 {
		b.WriteString("- [Doc ")
		b.WriteString(strings.Repeat("x", 3))
		b.WriteString("](https://docs.github.com/en/p")
		b.WriteString(itoa(i))
		b.WriteString(".md): description text here\n")
	}

	start := time.Now()

	idx, err := ParseIndex("https://docs.github.com", strings.NewReader(b.String()))
	must.NoError(err)

	is.Len(idx.Docs, 10000)

	// A hang guard, not a complexity guard. `entryRe` is stdlib regexp, which
	// is RE2 and linear by construction, so the catastrophic backtracking an
	// earlier version of this comment claimed to catch cannot happen. What is
	// left worth catching is a parser that stops terminating at all.
	//
	// Hence deliberately loose: a 2s budget failed CI at 2.1s under the race
	// detector alongside other parallel tests: noise, not the thing being
	// tested. The honest parse here is well under a second.
	is.Less(time.Since(start), 30*time.Second, "parse did not terminate in reasonable time")

	// Search and suggest over 10k entries must also stay quick.
	is.Len(SearchIndex(idx, "description", 5), 5, "limit not applied on large index")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var buf [12]byte

	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}

	return string(buf[i:])
}

// TestPathologicalEntryLine feeds a single 5MB entry line: the scanner cap must
// reject it without hanging the regex.
func TestPathologicalEntryLine(t *testing.T) {
	t.Parallel()

	line := "- [" + strings.Repeat("x", 5<<20) + "](https://docs.github.com/en/x.md): d"

	done := make(chan struct{})

	go func() {
		_, _ = ParseIndex("https://docs.github.com", strings.NewReader(line+"\n"))

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("parser hung on pathological line")
	}
}
