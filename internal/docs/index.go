package docs

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// entryRe matches llms.txt entries: "- [Title](URL): description". The bullet
// marker varies by publisher (docs.github.com uses "*", other llms.txt origins
// use "-"), so accept any CommonMark bullet. The description is optional.
var entryRe = regexp.MustCompile(`^[-*+] \[(.+?)\]\((\S+?)\)(?::\s*(.*))?$`)

// ParseIndex parses an llms.txt document into an Index. Only entries whose URL
// sits under baseURL are kept; their slug is the URL path relative to baseURL
// without the .md suffix. An input yielding zero entries returns
// ErrIndexUnavailable: a drifted or hostile payload must never look like a
// valid empty catalogue.
func ParseIndex(baseURL string, r io.Reader) (*Index, error) {
	prefix := strings.TrimSuffix(baseURL, "/") + "/"

	var (
		docs = []Doc{}
		seen = map[string]bool{}
	)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Real index entries are short; anything huge is noise and would
		// only feed the regex pathological input.
		if len(line) > 4096 {
			continue
		}

		m := entryRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		title, rawURL, desc := m[1], m[2], m[3]

		rel, ok := strings.CutPrefix(rawURL, prefix)
		if !ok {
			continue
		}

		slug := cleanSlug(rel)
		if !isArticleSlug(slug) || seen[slug] {
			continue
		}

		seen[slug] = true
		docs = append(docs, Doc{
			Slug:        slug,
			Title:       title,
			Description: desc,
			URL:         markdownURL(prefix, slug),
		})
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan index: %w", err)
	}

	if len(docs) == 0 {
		return nil, fmt.Errorf("no entries parsed: %w", ErrIndexUnavailable)
	}

	return newIndex(docs), nil
}

// ParsePageList parses the Page List API response, one absolute docs path per
// line, into index entries. The endpoint is the authority on which paths
// exist: a path absent from it serves an HTML 404 rather than markdown. It
// carries no prose, so titles are derived from the final path segment and
// descriptions are empty; a curated llms.txt entry for the same slug always
// wins during the merge.
//
// Unlike ParseIndex an empty result is not an error: the page list is a
// best-effort widening of the catalogue, never its only source.
func ParsePageList(baseURL string, r io.Reader) []Doc {
	prefix := strings.TrimSuffix(baseURL, "/") + "/"

	var (
		docs = []Doc{}
		seen = map[string]bool{}
	)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || len(line) > 4096 {
			continue
		}

		// Absolute paths ("/en/actions") are the documented shape; tolerate a
		// full URL under the same origin in case the endpoint ever changes.
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			line = "/" + rest
		}

		if !strings.HasPrefix(line, "/") {
			continue
		}

		slug := cleanSlug(strings.TrimPrefix(line, "/"))
		if !isArticleSlug(slug) || seen[slug] {
			continue
		}

		seen[slug] = true
		docs = append(docs, Doc{
			Slug:  slug,
			Title: titleFromSlug(slug),
			URL:   markdownURL(prefix, slug),
		})
	}

	if sc.Err() != nil {
		// A truncated page list still yields a usable widening of the
		// catalogue; the entries parsed so far are all valid.
		return docs
	}

	return docs
}

// MergeIndex returns an Index holding every doc in primary plus each doc in
// extra whose slug primary does not already carry. Primary order is preserved
// and its entries win, so curated titles and descriptions are never displaced
// by path-derived ones.
func MergeIndex(primary *Index, extra []Doc) *Index {
	if primary == nil {
		return newIndex(extra)
	}

	merged := make([]Doc, len(primary.Docs), len(primary.Docs)+len(extra))
	copy(merged, primary.Docs)

	for _, d := range extra {
		if _, dup := primary.bySlug[d.Slug]; dup {
			continue
		}

		merged = append(merged, d)
	}

	return newIndex(merged)
}

func newIndex(docs []Doc) *Index {
	idx := &Index{Docs: docs, bySlug: make(map[string]int, len(docs))}
	for i, d := range docs {
		if _, dup := idx.bySlug[d.Slug]; !dup {
			idx.bySlug[d.Slug] = i
		}
	}

	return idx
}

// isArticleSlug reports whether a slug addresses a documentation article.
// Every article lives under a language segment, so this rejects the API
// endpoints the curated catalogue advertises in its "How to use" preamble
// (/api/search/v1, /api/pagelist/...): real links, but not pages, and they
// would otherwise head the catalogue and 404 the moment get_doc followed one.
func isArticleSlug(slug string) bool {
	return strings.HasPrefix(slug, docsLanguage+"/")
}

// cleanSlug normalises a path relative to the docs base into a slug: no
// trailing slash, no .md suffix, no #fragment or ?query.
func cleanSlug(rel string) string {
	if h := strings.IndexByte(rel, '#'); h >= 0 {
		rel = rel[:h]
	}

	if q := strings.IndexByte(rel, '?'); q >= 0 {
		rel = rel[:q]
	}

	rel = strings.TrimSuffix(rel, "/")

	return strings.TrimSuffix(rel, ".md")
}

// markdownURL builds the fetch URL for a slug. Both the llms.txt catalogue and
// the page list address pages without an extension, while the origin serves
// markdown only at the .md form (the bare path returns rendered HTML), so the
// suffix is applied here rather than trusted from the catalogue.
func markdownURL(prefix, slug string) string { return prefix + slug + ".md" }

// titleFromSlug renders a path-derived title for page-list entries that have
// no prose: the final segment, de-kebabed and sentence-cased.
func titleFromSlug(slug string) string {
	seg := slug
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}

	seg = strings.ReplaceAll(seg, "-", " ")
	if seg == "" {
		return slug
	}

	r := []rune(seg)
	r[0] = unicode.ToUpper(r[0])

	return string(r)
}
