package docs

import (
	"bytes"
	"os"
	"testing"
	"unicode/utf8"
)

func FuzzParseIndex(f *testing.F) {
	if seed, err := os.ReadFile("testdata/llms.txt"); err == nil {
		f.Add(seed)
	}

	f.Add([]byte(""))
	f.Add([]byte("<!DOCTYPE html><html><body>Attention Required</body></html>"))
	f.Add([]byte("* [T](https://docs.github.com/en/x): d\n"))
	f.Add([]byte("- [T](https://docs.github.com/en/x.md): d\n"))
	f.Add([]byte("+ [](\x00)(:"))

	// Beyond the two invariants below, no panic is the property under test.
	f.Fuzz(func(t *testing.T, data []byte) {
		idx, err := ParseIndex("https://docs.github.com", bytes.NewReader(data))
		if err == nil && len(idx.Docs) == 0 {
			t.Fatal("empty index must never be a success")
		}

		if err != nil && idx != nil {
			t.Fatal("error must not return a partial index")
		}
	})
}

func FuzzExtractHeading(f *testing.F) {
	if seed, err := os.ReadFile("testdata/nodejs-guide.md"); err == nil {
		f.Add(seed, "Quickstart")
	}

	f.Add([]byte("# A\n\nbody\n"), "A")
	f.Add([]byte("### deep\n#\n######## overlong\n"), "deep")
	f.Add([]byte(""), "")

	f.Fuzz(func(t *testing.T, data []byte, heading string) {
		out, err := ExtractHeading(data, heading)
		if err == nil && len(out) == 0 {
			t.Fatal("successful extraction must return content")
		}

		_ = Paginate(out, 0)
		_ = Paginate(data, len(data)/2)
	})
}

func FuzzParsePageList(f *testing.F) {
	if seed, err := os.ReadFile("testdata/pagelist.txt"); err == nil {
		f.Add(seed)
	}

	f.Add([]byte("/en/actions\n/en/rest\n"))
	f.Add([]byte("/en/x\n/en/x\n/en/x/\n/en/x.md\n"))
	f.Add([]byte("https://docs.github.com/en/x\nnot-a-path\n\x00\n"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		docs := ParsePageList("https://docs.github.com", bytes.NewReader(data))

		seen := make(map[string]bool, len(docs))

		for _, d := range docs {
			if d.Slug == "" {
				t.Fatal("parsed a doc with an empty slug")
			}

			// Duplicate slugs would give MergeIndex two entries competing for
			// one BySlug position, so dedup is a parser invariant.
			if seen[d.Slug] {
				t.Fatalf("duplicate slug %q", d.Slug)
			}

			seen[d.Slug] = true

			if utf8.ValidString(d.Slug) && !utf8.ValidString(d.Title) {
				t.Fatalf("valid slug %q produced invalid UTF-8 title", d.Slug)
			}
		}
	})
}
