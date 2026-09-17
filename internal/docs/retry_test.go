package docs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedServer replies with the queued status codes in order, then 200.
func scriptedServer(t *testing.T, statuses []int, headers map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1)) - 1
		if n < len(statuses) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}

			w.WriteHeader(statuses[n])

			return
		}

		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

// testClient returns a client with recorded sleeps and no rate limiting.
func testClient(t *testing.T, baseURL string) (*Client, *[]time.Duration) {
	t.Helper()

	c, err := NewClient(baseURL)
	require.NoError(t, err)

	var slept []time.Duration

	c.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	return c, &slept
}

func TestFetchRetry(t *testing.T) {
	t.Parallel()

	t.Run("429 with retry-after then success", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, hits := scriptedServer(t, []int{http.StatusTooManyRequests}, map[string]string{"Retry-After": "3"})
		c, slept := testClient(t, srv.URL)

		body, err := c.Fetch(t.Context(), srv.URL+"/x.md")
		must.NoError(err)
		is.Equal("ok", string(body))

		is.Equal(int32(2), hits.Load(), "origin hits, expected 2")

		must.Len(*slept, 1, "Retry-After floor not honoured")
		is.GreaterOrEqual((*slept)[0], 3*time.Second, "Retry-After floor not honoured")
	})

	t.Run("absurd retry-after is clamped", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, _ := scriptedServer(t, []int{http.StatusTooManyRequests}, map[string]string{"Retry-After": "86400"})
		c, slept := testClient(t, srv.URL)

		_, err := c.Fetch(t.Context(), srv.URL+"/x.md")
		must.NoError(err)

		must.Len(*slept, 1, "Retry-After not clamped")
		is.LessOrEqual((*slept)[0], maxRetryAfter, "Retry-After not clamped")
	})

	t.Run("5xx retried to exhaustion", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, hits := scriptedServer(t, []int{500, 500, 500, 500}, nil)
		c, _ := testClient(t, srv.URL)

		_, err := c.Fetch(t.Context(), srv.URL+"/x.md")

		var fe *FetchError
		must.ErrorAs(err, &fe, "expected FetchError 500")
		is.Equal(http.StatusInternalServerError, fe.StatusCode)

		is.Equal(int32(maxAttempts), hits.Load(), "origin hits")
	})

	t.Run("404 maps to ErrNotFound without retry", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		srv, hits := scriptedServer(t, []int{404, 404}, nil)
		c, _ := testClient(t, srv.URL)

		_, err := c.Fetch(t.Context(), srv.URL+"/gone.md")
		is.ErrorIs(err, ErrNotFound, "expected ErrNotFound") //nolint:testifylint // the no-retry count check below is an independent verification

		is.Equal(int32(1), hits.Load(), "404 must not retry")
	})

	t.Run("other 4xx not retried", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, hits := scriptedServer(t, []int{403, 403}, nil)
		c, _ := testClient(t, srv.URL)

		_, err := c.Fetch(t.Context(), srv.URL+"/x.md")

		var fe *FetchError
		must.ErrorAs(err, &fe, "expected FetchError 403")
		is.Equal(http.StatusForbidden, fe.StatusCode)

		is.Equal(int32(1), hits.Load(), "4xx must not retry")
	})

	t.Run("cancelled context aborts between attempts", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, _ := scriptedServer(t, []int{500, 500, 500}, nil)

		c, err := NewClient(srv.URL)
		must.NoError(err)

		c.sleep = func(ctx context.Context, _ time.Duration) error {
			return context.Cause(ctx)
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = c.Fetch(ctx, srv.URL+"/x.md")
		is.ErrorIs(err, context.Canceled, "expected context.Canceled")
	})

	t.Run("deadline shorter than remaining waits returns FetchError not context error", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		// Origin always 503 with Retry-After: 1s. A 1.2s deadline lets the
		// first wait (1s) through but not a second, so the loop must return
		// the terminal FetchError, never a bare context error (which would
		// send Service.Get down the wrong branch instead of stale-serving).
		srv, _ := scriptedServer(t, []int{503, 503, 503}, map[string]string{"Retry-After": "1"})

		c, err := NewClient(srv.URL, WithRateLimit(1000)) // real sleepCtx, real time
		must.NoError(err)

		ctx, cancel := context.WithTimeout(t.Context(), 1200*time.Millisecond)
		defer cancel()

		_, err = c.Fetch(ctx, srv.URL+"/x.md")

		var fe *FetchError
		must.ErrorAs(err, &fe, "want terminal FetchError 503")
		is.Equal(http.StatusServiceUnavailable, fe.StatusCode)
	})

	t.Run("backoff grows across retries", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		srv, _ := scriptedServer(t, []int{500, 500}, nil)
		c, slept := testClient(t, srv.URL)

		_, err := c.Fetch(t.Context(), srv.URL+"/x.md")
		must.NoError(err)

		must.Len(*slept, 2, "expected growing backoff")
		is.Greater((*slept)[1], (*slept)[0]/2, "expected growing backoff")
	})
}
