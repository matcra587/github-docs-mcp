// Package docs fetches, caches and searches the GitHub documentation
// published at docs.github.com. The catalogue is built from two origin
// sources: the curated llms.txt shortlist for titles and descriptions, and
// the Page List API for the authoritative set of paths that resolve, while
// pages come from the .md form of each path and search is delegated to the
// origin's own server-side index.
package docs

import (
	"errors"
	"fmt"
)

// Sentinel errors for expected conditions. Matched with errors.Is at the
// tool-handler boundary, where they translate to client-friendly tool errors.
var (
	ErrNotFound         = errors.New("docs: not found")
	ErrHeadingNotFound  = errors.New("docs: heading not found")
	ErrIndexUnavailable = errors.New("docs: index unavailable")
)

// FetchError carries transport-level detail about a failed origin request.
type FetchError struct {
	URL        string
	StatusCode int
	Err        error
}

func (e *FetchError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("docs: fetch %s: status %d", e.URL, e.StatusCode)
	}

	return fmt.Sprintf("docs: fetch %s: %v", e.URL, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// Doc is one entry in the documentation index.
type Doc struct {
	Slug        string
	Title       string
	Description string
	URL         string
}

// Index is the parsed llms.txt catalogue.
type Index struct {
	Docs   []Doc
	bySlug map[string]int
}

// BySlug returns the doc for slug and whether it exists.
func (idx *Index) BySlug(slug string) (Doc, bool) {
	i, ok := idx.bySlug[slug]
	if !ok {
		return Doc{}, false
	}

	return idx.Docs[i], true
}
