package docs

import (
	"strings"
	"time"
)

const (
	freshnessFresh  = "fresh"
	cacheStaleServe = "stale-serve"
)

// Source identifies upstream content and when this server fetched it. Freshness
// describes the local TTL, never the upstream document's revision date.
type Source struct {
	URL       string
	Language  string
	Product   string
	Version   string
	FetchedAt time.Time
	Freshness string
}

// Coverage records dependencies even when a result is empty or a lookup fails.
type Coverage struct {
	Sources     []Source
	Degraded    bool
	Mode        string
	StaleBodies bool
}

// Catalogue is an ordered selection and its independently refreshed sources.
type Catalogue struct {
	Docs     []Doc
	Coverage Coverage
}

// SearchResult distinguishes bounded upstream retrieval from local fallback.
type SearchResult struct {
	Hits     []Hit
	Coverage Coverage
}

func pageSource(url string, at time.Time, stale bool) Source {
	source := Source{
		URL: strings.TrimSuffix(url, ".md"), Language: docsLanguage,
		Version: docsVersion, FetchedAt: at.UTC(), Freshness: "unknown",
	}
	if !at.IsZero() {
		source.Freshness = freshnessFresh
		if stale {
			source.Freshness = "stale"
		}
	}

	if _, path, ok := strings.Cut(source.URL, "/en/"); ok {
		parts := strings.Split(path, "/")
		if strings.Contains(parts[0], "@") {
			source.Version = parts[0]
			parts = parts[1:]
		}

		if len(parts) > 0 {
			source.Product = parts[0]
		}
	}

	return source
}

func (s *Service) page(doc Doc, body []byte, at time.Time, stale bool, coverage Coverage) Page {
	return Page{Content: body, Stale: stale, Source: pageSource(doc.URL, at, stale), Catalogue: coverage}
}

// Source returns the page identity with unknown freshness until fetched.
func (d Doc) Source() Source { return pageSource(d.URL, time.Time{}, false) }
