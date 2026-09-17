package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The origin's search endpoint indexes the whole corpus server-side, which is
// what makes search_docs cover every page rather than only the curated
// llms.txt catalogue. Language and version must match the page list the
// catalogue is built from, or search would return slugs the catalogue denies.
const (
	searchPath    = "/api/search/v1"
	pageListPath  = "/api/pagelist"
	docsLanguage  = "en"
	docsVersion   = "free-pro-team@latest"
	clientName    = "github-docs-mcp"
	searchAPIMax  = 50
	snippetMaxLen = 240
)

// searchResponse is the subset of the endpoint's JSON this server consumes.
// Unknown fields are ignored, so an origin adding fields cannot break parsing.
type searchResponse struct {
	Hits []struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Breadcrumbs string `json:"breadcrumbs"`
		Highlights  struct {
			Content []string `json:"content"`
			Title   []string `json:"title"`
		} `json:"highlights"`
	} `json:"hits"`
}

// searchURL builds the origin search request for query. A limit of zero or
// less means "no cap", matching SearchIndex and FilterDocs; the endpoint always
// needs a concrete size, so no cap becomes the largest page it will serve.
func (s *Service) searchURL(query string, limit int) string {
	size := searchAPIMax
	if limit > 0 {
		size = min(limit, searchAPIMax)
	}

	q := url.Values{
		"query":       {query},
		"language":    {docsLanguage},
		"version":     {docsVersion},
		"client_name": {clientName},
		"size":        {strconv.Itoa(size)},
	}

	return s.baseURL + searchPath + "?" + q.Encode()
}

// pageListURL builds the request for the catalogue-widening page list.
func (s *Service) pageListURL() string {
	return s.baseURL + pageListPath + "/" + docsLanguage + "/" + url.PathEscape(docsVersion)
}

// searchOrigin runs query against the origin's search endpoint and maps the
// results onto index entries. A hit for a slug the catalogue does not carry is
// kept with a synthesised Doc rather than dropped: the search index and the
// page list are refreshed independently, and a real page must never be
// invisible just because the catalogue has not caught up.
func (s *Service) searchOrigin(ctx context.Context, idx *Index, query string, limit int) ([]Hit, error) {
	raw, err := s.fetchShared(ctx, "search:"+query, func(dctx context.Context) ([]byte, error) {
		body, ferr := s.fetcher.Fetch(dctx, s.searchURL(query, limit))
		if ferr != nil {
			return nil, ferr
		}

		s.noteOriginHealthy()

		return body, nil
	})
	if err != nil {
		return nil, fmt.Errorf("search origin: %w", err)
	}

	var resp searchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	prefix := s.baseURL + "/"
	hits := make([]Hit, 0, len(resp.Hits))
	seen := make(map[string]bool, len(resp.Hits))

	for _, h := range resp.Hits {
		slug := cleanSlug(strings.TrimPrefix(h.URL, "/"))
		if !isArticleSlug(slug) || seen[slug] {
			continue
		}

		seen[slug] = true

		doc, ok := idx.BySlug(slug)
		if !ok {
			doc = Doc{Slug: slug, Title: h.Title, URL: markdownURL(prefix, slug)}
		}

		// The endpoint returns hits already ranked; preserve that order by
		// scoring them in descending sequence rather than re-ranking locally.
		hits = append(hits, Hit{
			Doc:         doc,
			Score:       len(resp.Hits) - len(hits),
			Snippet:     apiSnippet(h.Breadcrumbs, h.Highlights.Content, h.Highlights.Title),
			MatchedBody: true,
		})
	}

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	return hits, nil
}

// apiSnippet renders a one-line snippet from a hit's breadcrumbs and
// highlights, matching the shape local search produces: "breadcrumb - text".
func apiSnippet(breadcrumbs string, content, title []string) string {
	body := ""

	if len(content) > 0 {
		body = content[0]
	} else if len(title) > 0 {
		body = title[0]
	}

	body = stripMarks(body)
	// Highlights embed newlines where the origin joined title, description and
	// body; a single line keeps one result to one row.
	body = strings.Join(strings.Fields(body), " ")

	if len(body) > snippetMaxLen {
		body = strings.TrimSpace(body[:snippetMaxLen]) + "…"
	}

	switch {
	case breadcrumbs != "" && body != "":
		return breadcrumbs + " - " + body
	case breadcrumbs != "":
		return breadcrumbs
	default:
		return body
	}
}

// stripMarks removes the <mark> wrappers the endpoint puts around matched
// terms. They are display markup for the docs site's own UI and would read as
// literal noise in a tool result.
func stripMarks(s string) string {
	return strings.NewReplacer("<mark>", "", "</mark>", "").Replace(s)
}
