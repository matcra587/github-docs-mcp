package docs

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchURL(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	svc := &Service{baseURL: testBaseURL}

	u, err := url.Parse(svc.searchURL("cache deps", 5))
	must.NoError(err)

	is.Equal("/api/search/v1", u.Path)

	q := u.Query()
	is.Equal("cache deps", q.Get("query"))
	is.Equal(docsLanguage, q.Get("language"))
	is.Equal(docsVersion, q.Get("version"))
	// The endpoint rejects external requests that omit this.
	is.Equal(clientName, q.Get("client_name"))
	is.Equal("5", q.Get("size"))

	t.Run("size is clamped to the endpoint's range", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		// limit <= 0 is "no cap", which for a paginated endpoint means the
		// largest page it serves, not one result.
		for limit, want := range map[int]string{0: "50", -3: "50", 500: "50", 7: "7"} {
			u, err := url.Parse(svc.searchURL("q", limit))
			require.NoError(t, err)

			is.Equal(want, u.Query().Get("size"), "limit %d", limit)
		}
	})
}

func TestPageListURL(t *testing.T) {
	t.Parallel()

	svc := &Service{baseURL: testBaseURL}

	assert.New(t).Equal(
		"https://docs.github.com/api/pagelist/en/free-pro-team@latest",
		svc.pageListURL(),
	)
}

func TestAPISnippet(t *testing.T) {
	t.Parallel()

	t.Run("strips marks and flattens to one line", func(t *testing.T) {
		t.Parallel()

		got := apiSnippet(
			"Actions / Tutorials",
			[]string{"Caching <mark>deps</mark>\nSpeed up your workflow\nwith a cache"},
			nil,
		)

		assert.New(t).Equal("Actions / Tutorials - Caching deps Speed up your workflow with a cache", got)
	})

	t.Run("falls back to the title highlight", func(t *testing.T) {
		t.Parallel()

		got := apiSnippet("Apps", nil, []string{"Using <mark>webhooks</mark>"})
		assert.New(t).Equal("Apps - Using webhooks", got)
	})

	t.Run("each part is optional", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Equal("Apps", apiSnippet("Apps", nil, nil))
		is.Equal("body", apiSnippet("", []string{"body"}, nil))
		is.Empty(apiSnippet("", nil, nil))
	})

	t.Run("long highlights are truncated", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		got := apiSnippet("", []string{strings.Repeat("word ", 200)}, nil)

		is.LessOrEqual(len(got), snippetMaxLen+len("…"), "snippet exceeded its cap")
		is.True(strings.HasSuffix(got, "…"), "truncation must be visible")
	})
}
