package docs

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupRedirectScope(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/outside/en/page.md", "/docs/en/enterprise-server@3.18/page.md", "/docs/en/../en/page.md", "/docs/en/%2e%2e/en/page.md"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			var escaped atomic.Int64

			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/docs/en/start.md" {
					w.Header().Set("Location", target)
					w.WriteHeader(http.StatusFound)

					return
				}

				escaped.Add(1)

				_, _ = w.Write([]byte("# Wrong scope\n"))
			}))
			t.Cleanup(origin.Close)
			client, err := NewClient(origin.URL+"/docs", WithRateLimit(1000))
			require.NoError(t, err)
			_, err = client.Fetch(t.Context(), origin.URL+"/docs/en/start.md")
			require.Error(t, err, "redirect must not escape documentation scope")
			require.Zero(t, escaped.Load(), "rejected redirect must not reach destination")
		})
	}
}
