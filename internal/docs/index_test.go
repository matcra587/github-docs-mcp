package docs

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBaseURL = "https://docs.github.com"

func TestParseIndex(t *testing.T) {
	t.Parallel()

	t.Run("real fixture", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		f, err := os.Open("testdata/llms.txt")
		must.NoError(err)

		defer func() { _ = f.Close() }()

		idx, err := ParseIndex(testBaseURL, f)
		must.NoError(err)

		is.Len(idx.Docs, 110, "expected 110 article entries (API-endpoint links filtered out)")

		doc, ok := idx.BySlug("en/copilot")
		must.True(ok, "en/copilot not found in index")

		is.NotEmpty(doc.Title, "copilot entry incomplete")
		is.NotEmpty(doc.Description, "copilot entry incomplete")

		// The catalogue addresses pages without an extension, but only the .md
		// form serves markdown; the parser must supply the suffix.
		is.Equal("https://docs.github.com/en/copilot.md", doc.URL)
	})

	t.Run("any bullet marker is accepted", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		md := "* [Star](https://docs.github.com/en/star): asterisk bullet\n" +
			"- [Dash](https://docs.github.com/en/dash): dash bullet\n" +
			"+ [Plus](https://docs.github.com/en/plus): plus bullet\n"

		idx, err := ParseIndex(testBaseURL, strings.NewReader(md))
		must.NoError(err)

		must.Len(idx.Docs, 3, "every CommonMark bullet must parse")

		for _, slug := range []string{"en/star", "en/dash", "en/plus"} {
			_, ok := idx.BySlug(slug)
			is.True(ok, "missing %s", slug)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		_, err := ParseIndex(testBaseURL, strings.NewReader(""))
		is.ErrorIs(err, ErrIndexUnavailable, "expected ErrIndexUnavailable")
	})

	t.Run("html error page never empty-success", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		html := `<!DOCTYPE html><html><head><title>Attention Required</title></head><body>Checking your browser</body></html>`

		_, err := ParseIndex(testBaseURL, strings.NewReader(html))
		is.ErrorIs(err, ErrIndexUnavailable, "expected ErrIndexUnavailable")
	})

	t.Run("prose without entries is an error", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		md := "# GitHub Docs\n\n> Some description.\n\n## Docs\n\nno list entries here\n"

		_, err := ParseIndex(testBaseURL, strings.NewReader(md))
		is.ErrorIs(err, ErrIndexUnavailable, "expected ErrIndexUnavailable")
	})

	t.Run("duplicate slugs keep first", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		md := "- [First](https://docs.github.com/en/dup.md): first entry\n" +
			"- [Second](https://docs.github.com/en/dup.md): second entry\n"

		idx, err := ParseIndex(testBaseURL, strings.NewReader(md))
		must.NoError(err)

		is.Len(idx.Docs, 1, "expected 1 doc")

		doc, _ := idx.BySlug("en/dup")
		is.Equal("First", doc.Title, "expected first entry kept")
	})

	t.Run("entries outside base url are skipped", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		md := "- [Inside](https://docs.github.com/en/ok.md): kept\n" +
			"- [Outside](https://example.com/evil.md): dropped\n"

		idx, err := ParseIndex(testBaseURL, strings.NewReader(md))
		must.NoError(err)

		must.Len(idx.Docs, 1, "expected only en/ok")
		is.Equal("en/ok", idx.Docs[0].Slug, "expected only en/ok")
	})
}

func TestParsePageList(t *testing.T) {
	t.Parallel()

	t.Run("real fixture", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		raw, err := os.ReadFile("testdata/pagelist.txt")
		must.NoError(err)

		docs := ParsePageList(testBaseURL, bytes.NewReader(raw))
		must.NotEmpty(docs, "page list produced no entries")

		bySlug := map[string]Doc{}
		for _, d := range docs {
			bySlug[d.Slug] = d
		}

		d, ok := bySlug["en/actions/tutorials/build-and-test-code/nodejs"]
		must.True(ok, "expected the deep tutorial path")

		is.Equal("Nodejs", d.Title, "title comes from the final path segment")
		is.Empty(d.Description, "the page list carries no prose")
		is.Equal("https://docs.github.com/en/actions/tutorials/build-and-test-code/nodejs.md", d.URL)
	})

	t.Run("normalises and dedups paths", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		raw := "/en/x\n" +
			"/en/x/\n" + // trailing slash
			"/en/x.md\n" + // explicit suffix
			"https://docs.github.com/en/x\n" + // full URL under the origin
			"# a comment line\n" +
			"relative/path\n" + // not absolute
			"\n" +
			"/en/y\n"

		docs := ParsePageList(testBaseURL, strings.NewReader(raw))

		slugs := make([]string, 0, len(docs))
		for _, d := range docs {
			slugs = append(slugs, d.Slug)
		}

		is.Equal([]string{"en/x", "en/y"}, slugs)
	})

	t.Run("empty input is not an error", func(t *testing.T) {
		t.Parallel()

		// Unlike the curated catalogue, the page list is a best-effort
		// widening: nothing parsed simply means nothing added.
		assert.New(t).Empty(ParsePageList(testBaseURL, strings.NewReader("")))
	})
}

func TestMergeIndex(t *testing.T) {
	t.Parallel()

	t.Run("curated entries win and order is preserved", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		primary, err := ParseIndex(testBaseURL, strings.NewReader(
			"* [Curated Actions](https://docs.github.com/en/actions): the good description\n",
		))
		must.NoError(err)

		merged := MergeIndex(primary, ParsePageList(testBaseURL, strings.NewReader(
			"/en/actions\n/en/rest\n",
		)))

		must.Len(merged.Docs, 2, "one overlap, one addition")

		is.Equal("en/actions", merged.Docs[0].Slug, "curated entries come first")

		actions, _ := merged.BySlug("en/actions")
		is.Equal("Curated Actions", actions.Title, "path-derived title must not displace the curated one")
		is.Equal("the good description", actions.Description)

		rest, ok := merged.BySlug("en/rest")
		must.True(ok, "page-list-only slug must be reachable")
		is.Equal("Rest", rest.Title)
	})

	t.Run("nil primary yields the extras alone", func(t *testing.T) {
		t.Parallel()

		merged := MergeIndex(nil, ParsePageList(testBaseURL, strings.NewReader("/en/rest\n")))

		_, ok := merged.BySlug("en/rest")
		assert.New(t).True(ok)
	})
}

func TestArticleSlugFilter(t *testing.T) {
	t.Parallel()

	t.Run("catalogue drops the API endpoints it advertises", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		// The real llms.txt opens with a "How to use" section linking the docs
		// APIs. They are valid links but not articles: get_doc would 404 on
		// them, and they would otherwise head the catalogue.
		md := "* [Search API](https://docs.github.com/api/search/v1): Search across all docs content.\n" +
			"* [Page List API](https://docs.github.com/api/pagelist/en/free-pro-team@latest): Returns every docs page path.\n" +
			"* [GitHub Actions](https://docs.github.com/en/actions): Automate your workflows.\n"

		idx, err := ParseIndex(testBaseURL, strings.NewReader(md))
		must.NoError(err)

		must.Len(idx.Docs, 1, "only the article should survive")
		is.Equal("en/actions", idx.Docs[0].Slug)
	})

	t.Run("page list drops non-article paths too", func(t *testing.T) {
		t.Parallel()

		docs := ParsePageList(testBaseURL, strings.NewReader("/en\n/en/actions\n/api/search/v1\n"))

		slugs := make([]string, 0, len(docs))
		for _, d := range docs {
			slugs = append(slugs, d.Slug)
		}

		// "/en" is the language root, not an article, so it goes too.
		assert.New(t).Equal([]string{"en/actions"}, slugs)
	})
}
