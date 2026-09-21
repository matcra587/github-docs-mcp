package docs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupLocalizedPublishedAnchor(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h2 id="overview"><a class="heading-link">概要<span></span></a></h2><div id="not-heading">other</div>`))
	}))
	t.Cleanup(origin.Close)
	client, err := NewClient(origin.URL, WithRateLimit(1000))
	require.NoError(t, err)

	service := NewService(client, origin.URL, ServiceConfig{CacheMaxBytes: 1 << 20})
	page := Page{Content: []byte("## 概要\r\n説明\r\n"), Source: Source{URL: origin.URL + "/ja/guide.md", Language: "ja"}}
	resolved, err := service.ResolveHeading(t.Context(), page, "#overview")
	require.NoError(t, err, "published English anchor must resolve to localized heading")
	require.Equal(t, "#概要", resolved)
	selected, err := ExtractHeading(page.Content, resolved)
	require.NoError(t, err)
	require.Equal(t, page.Content, selected)
}
