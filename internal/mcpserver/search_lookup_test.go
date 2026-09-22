package mcpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupSearchFragmentRoundTrip(t *testing.T) {
	t.Parallel()

	origin := &fixture{}
	origin.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/llms.txt":
			_, _ = fmt.Fprintf(w, "* [Page](%s/en/page): test\n", origin.srv.URL)
		case strings.HasPrefix(r.URL.Path, "/api/pagelist/"):
			_, _ = fmt.Fprint(w, "/en/page\n")
		case r.URL.Path == "/api/search/v1":
			_, _ = fmt.Fprint(w, `{"hits":[{"url":"/en/page#second","title":"Page","breadcrumbs":"Not a real heading"}]}`)
		default:
			_, _ = fmt.Fprint(w, "## First\nfirst\n## Second\nsecond\n")
		}
	}))
	t.Cleanup(origin.srv.Close)
	session, _ := newSession(t, origin)
	search := callTool(t, session, toolSearchDocs, map[string]any{"query": "second"})
	require.False(t, search.IsError)
	require.Contains(t, textOf(t, search), `get_doc({"slug":"en/page#second"})`)
	page := callTool(t, session, toolGetDoc, map[string]any{"slug": "en/page#second"})
	require.False(t, page.IsError)
	require.Equal(t, "## Second\nsecond\n", pageBody(t, textOf(t, page)))
}
