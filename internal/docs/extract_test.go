package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const samplePage = `# Title

intro text

## Setup

setup body

### Setup details

nested details

## Usage

usage body
`

func TestExtractHeading(t *testing.T) {
	t.Parallel()

	t.Run("extracts section until same level heading", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		got, err := ExtractHeading([]byte(samplePage), "Setup")
		must.NoError(err)

		s := string(got)
		is.Contains(s, "setup body", "section incomplete")
		is.Contains(s, "nested details", "section incomplete")

		is.NotContains(s, "usage body", "section leaked past next heading")
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		_, err := ExtractHeading([]byte(samplePage), "setup")
		is.NoError(err, "expected case-insensitive match")
	})

	t.Run("missing heading", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		_, err := ExtractHeading([]byte(samplePage), "Nonexistent")
		is.ErrorIs(err, ErrHeadingNotFound, "expected ErrHeadingNotFound")
	})

	t.Run("nested heading extracts only its subtree", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		got, err := ExtractHeading([]byte(samplePage), "Setup details")
		must.NoError(err)

		s := string(got)
		is.Contains(s, "nested details", "wrong subtree")
		is.NotContains(s, "usage body", "wrong subtree")
		is.NotContains(s, "setup body", "wrong subtree")
	})
}

var fencedPage = strings.Join([]string{
	"## Install",
	"",
	"before the fence",
	"",
	"```bash",
	"#!/bin/bash",
	"# not a heading",
	"echo hi",
	"```",
	"",
	"after the fence",
	"",
	"## Next",
	"",
	"next section body",
}, "\n")

func TestExtractHeadingFences(t *testing.T) {
	t.Parallel()

	t.Run("hash comments inside fences are not headings", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		got, err := ExtractHeading([]byte(fencedPage), "Install")
		must.NoError(err)

		s := string(got)
		is.Contains(s, "# not a heading", "section cut inside code fence")
		is.Contains(s, "#!/bin/bash", "section cut inside code fence")

		is.Contains(s, "after the fence", "section lost content after fence")

		is.NotContains(s, "next section body", "section leaked past real next heading")
	})

	t.Run("real fixture section survives fixture shell comments", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		page, err := os.ReadFile("testdata/nodejs-guide.md")
		must.NoError(err)

		got, err := ExtractHeading(page, "Example caching dependencies")
		must.NoError(err)

		// YAML comments inside the example must not become section boundaries.
		// The whole code fence survives, but the next real heading does not.
		sec := string(got)
		is.Contains(sec, "# This workflow uses actions that are not certified by GitHub.", "section cut before in-fence comment line")
		is.Contains(sec, "pnpm install", "section lost content after in-fence comments")

		is.Zero(strings.Count(sec, "```")%2, "unbalanced fences in extracted section")

		is.NotContains(sec, "## Building and testing your code", "section leaked past next heading")
	})
}

func TestPaginate(t *testing.T) {
	t.Parallel()

	t.Run("small content untouched", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		w := Paginate([]byte("short"), 0)
		is.False(w.Truncated())
		is.Equal("short", string(w.Content))
		is.Equal(5, w.Next)
		is.Equal(5, w.Total)
	})

	t.Run("large content windows with continuation", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		big := []byte(strings.Repeat("line of text\n", (maxContentBytes/13)+100))

		w := Paginate(big, 0)
		is.True(w.Truncated(), "bad first window")
		is.LessOrEqual(len(w.Content), maxContentBytes, "bad first window")

		w2 := Paginate(big, w.Next)
		is.Equal(w.Next, w2.Offset, "continuation window broken")
		is.NotEmpty(w2.Content, "continuation window broken")
	})

	t.Run("windows tile the document exactly", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		big := []byte(strings.Repeat("0123456789\n", (maxContentBytes/11)+50))

		var got []byte

		for off := 0; ; {
			w := Paginate(big, off)

			got = append(got, w.Content...)
			if !w.Truncated() {
				break
			}

			off = w.Next
		}

		is.Contains(string(got), string(big[:100]), "windows do not reassemble document")
		is.Len(got, len(big), "windows do not reassemble document")
	})

	t.Run("offset past end is empty not error", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		w := Paginate([]byte("abc"), 99)
		is.False(w.Truncated())
		is.Empty(w.Content)
		is.Equal(3, w.Total)
	})

	t.Run("negative offset treated as zero", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		w := Paginate([]byte("abc"), -5)
		is.Equal("abc", string(w.Content))
		is.Equal(0, w.Offset)
	})
}
