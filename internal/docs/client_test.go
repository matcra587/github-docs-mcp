package docs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientSchemeGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{"https allowed", "https://docs.github.com", false},
		{"http non-loopback rejected", "http://example.com/docs", true},
		{"http localhost allowed", "http://localhost:9999", false},
		{"http 127.0.0.1 allowed", "http://127.0.0.1:9999", false},
		{"garbage rejected", "://not-a-url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			is := assert.New(t)

			_, err := NewClient(tt.baseURL)
			if tt.wantErr {
				is.Error(err, "NewClient(%q)", tt.baseURL)
			} else {
				is.NoError(err, "NewClient(%q)", tt.baseURL)
			}
		})
	}
}

func TestClientFetch(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("# Hello\n"))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL)
		must.NoError(err)

		got, err := c.Fetch(t.Context(), srv.URL+"/en/page.md")
		must.NoError(err, "Fetch")

		is.Equal("# Hello\n", string(got))
	})

	t.Run("non-200 returns FetchError with status", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL)
		_, err := c.Fetch(t.Context(), srv.URL+"/x.md")

		var fe *FetchError
		must.ErrorAs(err, &fe, "expected FetchError 500")
		is.Equal(http.StatusInternalServerError, fe.StatusCode)
	})

	t.Run("body over cap rejected", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", pageMaxBytes+100)))
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL)

		// A page just over the page cap is rejected; the same body under the
		// bundle endpoint would be accepted (different cap by URL).
		_, err := c.Fetch(t.Context(), srv.URL+"/big.md")
		is.ErrorContains(err, "size cap", "expected size cap error")
	})

	t.Run("context timeout aborts", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL)

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		_, err := c.Fetch(ctx, srv.URL+"/slow.md")
		is.ErrorIs(err, context.DeadlineExceeded, "expected deadline exceeded")
	})

	t.Run("fetch outside base host rejected", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("x"))
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL)

		_, err := c.Fetch(t.Context(), "https://example.com/evil.md")
		is.ErrorContains(err, "outside base", "expected outside-base rejection")
	})
}

func TestBodyCap(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	cases := map[string]int64{
		"https://docs.github.com/llms.txt":                             catalogueMaxBytes,
		"https://docs.github.com/api/pagelist/en/free-pro-team@latest": catalogueMaxBytes,
		"https://docs.github.com/en/hooks.md":                          pageMaxBytes,
		"https://docs.github.com/en/x":                                 pageMaxBytes,
	}

	for url, want := range cases {
		is.Equal(want, bodyCap(url), "bodyCap(%q)", url)
	}
}

func TestFetchRejectsHTML(t *testing.T) {
	t.Parallel()

	t.Run("html on a 200 is refused and never retried", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		var attempts atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Attention Required</body></html>"))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, WithRateLimit(1000))
		require.NoError(t, err)

		_, err = c.Fetch(t.Context(), srv.URL+"/en/x.md")
		must.ErrorContains(err, "unexpected html response", "an interstitial must never be cached as documentation")

		// Deterministic origin behaviour: retrying would only triple the load.
		is.Equal(int32(1), attempts.Load(), "html response must not be retried")
	})

	t.Run("markdown, plain text and json are accepted", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		for _, ct := range []string{"text/markdown; charset=utf-8", "text/plain; charset=utf-8", "application/json"} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write([]byte("body"))
			}))

			c, err := NewClient(srv.URL, WithRateLimit(1000))
			require.NoError(t, err)

			got, err := c.Fetch(t.Context(), srv.URL+"/en/x.md")
			must.NoError(err, "content-type %q must be accepted", ct)
			is.Equal("body", string(got))

			srv.Close()
		}
	})
}
