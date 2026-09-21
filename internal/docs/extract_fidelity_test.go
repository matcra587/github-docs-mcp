package docs

import (
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
