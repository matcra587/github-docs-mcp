package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupHeadingFidelity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, body, anchor, want string }{
		{"emphasis", "## **Strong** and _emphasis_\nbody\n", "#strong-and-emphasis", "## **Strong** and _emphasis_\nbody\n"},
		{"nested link", "## [**Guide**](https://example.invalid/a(b))\nbody", "#guide", "## [**Guide**](https://example.invalid/a(b))\nbody"},
		{"code literal", "## `[label](url)`\nbody", "#labelurl", "## `[label](url)`\nbody"},
		{"inline html", "## <em>Guide</em> &amp; more\nbody", "#guide--more", "## <em>Guide</em> &amp; more\nbody"},
		{"url autolink", "## See <https://example.com>\nbody", "#see-httpsexamplecom", "## See <https://example.com>\nbody"},
		{"email autolink", "## Contact <help@example.com>\nbody", "#contact-helpexamplecom", "## Contact <help@example.com>\nbody"},
		{"custom scheme autolink", "## <git+ssh:repo>\nbody", "#gitsshrepo", "## <git+ssh:repo>\nbody"},
		{"indented atx", "   ## Guide ###\r\nbody", "#guide", "   ## Guide ###\r\nbody"},
		{"setext", "Guide\r\n=====\r\nbody\r\nNext\r\n====\r\nother", "#guide", "Guide\r\n=====\r\nbody\r\n"},
		{"combining", "## Cafe\u0301\nbody", "#cafe\u0301", "## Cafe\u0301\nbody"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ExtractHeading([]byte(test.body), test.anchor)
			require.NoError(t, err)
			require.Equal(t, test.want, string(got))
		})
	}
}

func TestLookupListFence(t *testing.T) {
	t.Parallel()

	for _, marker := range []string{"- ", "* ", "+ ", "1. ", "12) ", "  - ", "123456789. "} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()

			indent := strings.Repeat(" ", len(marker))
			body := []byte("## Before\n" + marker + "```sh\n" + indent + "## Fake\n" + indent + "```\n## After\nreal\n")
			_, err := ExtractHeading(body, "#fake")
			require.ErrorIs(t, err, ErrHeadingNotFound, "list-contained code must not create headings")
			section, err := ExtractHeading(body, "#after")
			require.NoError(t, err)
			require.Equal(t, "## After\nreal\n", string(section))
		})
	}
}

func TestLookupListFenceBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, body string }{
		{"tab indentation", "-\t```sh\n\t## Fake\n\t```\n## After\nreal\n"},
		{"container ends", "- ```sh\n  ## Fake\n\n## After\nreal\n"},
		{"marker inside code", "```sh\n- ```\n## Fake\n```\n## After\nreal\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(test.body)
			_, err := ExtractHeading(body, "#fake")
			require.ErrorIs(t, err, ErrHeadingNotFound)
			section, err := ExtractHeading(body, "#after")
			require.NoError(t, err)
			require.Equal(t, "## After\nreal\n", string(section))
		})
	}
}
