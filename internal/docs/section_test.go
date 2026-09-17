package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sectionPage = strings.Join([]string{
	"# Page title",
	"",
	"intro paragraph about nothing",
	"",
	"## Exit codes",
	"",
	"how exit codes work",
	"",
	"### Exit code 2",
	"",
	"exit 2 blocks the step and shows stderr in the job log",
	"",
	"```bash",
	"# this hash is not a heading",
	"exit 2",
	"```",
	"",
	"### Exit code 0",
	"",
	"exit 0 proceeds",
	"",
	"## Configuration",
	"",
	"config lives in settings.json",
}, "\n")

func TestSplitSections(t *testing.T) {
	t.Parallel()

	secs := SplitSections([]byte(sectionPage))

	t.Run("one section per heading", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		var headings []string
		for _, s := range secs {
			headings = append(headings, s.Heading)
		}

		want := []string{"Page title", "Exit codes", "Exit code 2", "Exit code 0", "Configuration"}
		is.Equal(strings.Join(want, "|"), strings.Join(headings, "|"), "headings")
	})

	t.Run("breadcrumb tracks ancestry", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		for _, s := range secs {
			if s.Heading == "Exit code 2" {
				is.Equal("Page title > Exit codes > Exit code 2", s.Breadcrumb)

				return
			}
		}

		t.Fatal("Exit code 2 section not found")
	})

	t.Run("hash inside a fence does not start a section", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		for _, s := range secs {
			is.NotEqual("this hash is not a heading", s.Heading, "fence comment parsed as a heading")
		}

		// The fence line must live inside the Exit code 2 body.
		for _, s := range secs {
			if s.Heading == "Exit code 2" {
				is.Contains(string(s.Body), "# this hash is not a heading", "fence content missing from section body")
			}
		}
	})

	t.Run("nested section body stops before the next same-or-higher heading", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		for _, s := range secs {
			if s.Heading == "Exit code 2" {
				is.NotContains(string(s.Body), "exit 0 proceeds", "section leaked into sibling")
			}
		}
	})
}

func TestSearchSections(t *testing.T) {
	t.Parallel()

	secs := SplitSections([]byte(sectionPage))

	t.Run("matches heading and body, ranks heading higher", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		hits := SearchSections(secs, "exit code 2", 0)
		must.NotEmpty(hits, "expected Exit code 2 first")
		is.Equal("Exit code 2", hits[0].Heading, "expected Exit code 2 first")
	})

	t.Run("body-only term still matches", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		hits := SearchSections(secs, "stderr", 0)
		must.Len(hits, 1, "expected the stderr section")
		is.Equal("Exit code 2", hits[0].Heading, "expected the stderr section")
	})

	t.Run("no match", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Empty(SearchSections(secs, "kubernetes", 0), "expected no hits")
	})

	t.Run("limit caps results", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Len(SearchSections(secs, "exit", 1), 1, "limit not applied")
	})

	t.Run("empty query returns nothing", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		is.Empty(SearchSections(secs, "   ", 0), "blank query returned results")
	})
}

func TestNormalizeSlug(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	cases := map[string]string{
		"en/hooks":                  "en/hooks",
		"en/hooks.md":               "en/hooks",
		"en/hooks#matcher-patterns": "en/hooks",
		// Absolute paths are exactly how the origin's own pages write their
		// cross-links, so following one must resolve rather than dead-end.
		"/en/hooks":                              "en/hooks",
		"https://docs.github.com/en/hooks.md":    "en/hooks",
		"https://docs.github.com/en/hooks#setup": "en/hooks",
		"https://docs.github.com/en/hooks?x=1":   "en/hooks",
		"  en/hooks/  ":                          "en/hooks",
		// A bare origin carries no page.
		"https://docs.github.com": "",
	}

	for in, want := range cases {
		is.Equal(want, normalizeSlug(in), "normalizeSlug(%q)", in)
	}
}

func TestExtractHeadingKebabAnchor(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	page := "# Title\n\n## Hook Events\n\nbody about events\n\n## Other\n\nx\n"

	for _, form := range []string{"Hook Events", "hook events", "#hook-events", "hook-events"} {
		got, err := ExtractHeading([]byte(page), form)
		must.NoError(err, "ExtractHeading(%q)", form)

		is.Contains(string(got), "body about events", "form %q extracted wrong section", form)
	}
}
