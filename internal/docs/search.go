package docs

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Hit is one search result. MatchedBody marks hits found in cached page
// content rather than the index metadata alone.
type Hit struct {
	Source      Source
	Doc         Doc
	Score       int
	Snippet     string
	MatchedBody bool
	PageBytes   *int
}

const (
	titleWeight = 3
	descWeight  = 2
	bodyWeight  = 1
)

// SearchIndex scores idx entries against a whitespace-tokenised query. Every
// token must match somewhere (title, description or slug); results are ranked
// by cumulative weight. limit <= 0 means no cap.
func SearchIndex(idx *Index, query string, limit int) []Hit {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}

	hits := []Hit{}

	for _, d := range idx.Docs {
		if score := scoreDoc(d, tokens); score > 0 {
			hits = append(hits, Hit{Doc: d, Score: score, Snippet: d.Description})
		}
	}

	// Strict all-tokens matching came up empty on a multi-word query: fall
	// back to any-token scoring so "resume previous session" still surfaces
	// the page that only says "resume". Single-token queries gain nothing.
	if len(hits) == 0 && len(tokens) > 1 {
		for _, d := range idx.Docs {
			if score := scoreDocAny(d, tokens); score > 0 {
				hits = append(hits, Hit{Doc: d, Score: score, Snippet: d.Description})
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	return hits
}

// scoreDoc returns d's cumulative match weight for tokens, or 0 when any
// token matches neither title, slug nor description.
func scoreDoc(d Doc, tokens []string) int {
	title := strings.ToLower(d.Title)
	desc := strings.ToLower(d.Description)
	slug := strings.ToLower(d.Slug)

	score := 0

	for _, tok := range tokens {
		switch {
		case strings.Contains(title, tok) || strings.Contains(slug, tok):
			score += titleWeight
		case strings.Contains(desc, tok):
			score += descWeight
		default:
			return 0
		}
	}

	return score
}

// scoreDocAny is the lenient counterpart of scoreDoc: tokens that match
// nothing simply score zero instead of excluding the doc.
func scoreDocAny(d Doc, tokens []string) int {
	title := strings.ToLower(d.Title)
	desc := strings.ToLower(d.Description)
	slug := strings.ToLower(d.Slug)

	score := 0

	for _, tok := range tokens {
		switch {
		case strings.Contains(title, tok) || strings.Contains(slug, tok):
			score += titleWeight
		case strings.Contains(desc, tok):
			score += descWeight
		}
	}

	return score
}

// FilterDocs returns index entries, optionally filtered to a slug prefix
// (section). limit <= 0 means no cap.
func FilterDocs(idx *Index, section string, limit int) []Doc {
	docs := []Doc{}

	for _, d := range idx.Docs {
		if section != "" && !strings.HasPrefix(d.Slug, section) {
			continue
		}

		docs = append(docs, d)

		if limit > 0 && len(docs) == limit {
			break
		}
	}

	return docs
}

// Suggest returns up to n slugs closest to miss by edit distance, nearest
// first. Slugs further than a third of the input length (minimum 2) away are
// dropped; beyond that the suggestion is noise.
func Suggest(idx *Index, miss string, n int) []string {
	type cand struct {
		slug string
		dist int
	}

	maxDist := max(len(miss)/3, 2)
	cands := []cand{}

	for _, d := range idx.Docs {
		dist := editDistance(miss, d.Slug)
		if dist <= maxDist {
			cands = append(cands, cand{slug: d.Slug, dist: dist})
		}
	}

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })

	out := []string{}
	for i := 0; i < len(cands) && i < n; i++ {
		out = append(out, cands[i].slug)
	}

	return out
}

// SuggestSlugs returns up to n known slugs closest to miss, for near-miss
// hints on ErrNotFound. Errors resolving the index yield no suggestions,
// the caller is already on an error path.
func (s *Service) SuggestSlugs(ctx context.Context, miss string, n int) []string {
	view, err := s.forSlug(miss)
	if err != nil {
		return nil
	}

	if view != s {
		return view.SuggestSlugs(ctx, miss, n)
	}

	idx, err := s.index(ctx)
	if err != nil {
		return nil
	}

	return Suggest(idx, miss, n)
}

// SearchPage fetches slug (via the normal cache path) and returns the sections
// of that page matching query, verbatim: retrieval within one page, so the
// caller gets the relevant slices instead of the whole document. The Page's
// Stale flag is propagated so the caller can note an origin outage.
func (s *Service) SearchPage(ctx context.Context, slug, query string, limit int) ([]SectionHit, bool, error) {
	page, err := s.Get(ctx, slug)
	if err != nil {
		return nil, false, err
	}

	return SearchSections(SplitSections(page.Content), query, limit), page.Stale, nil
}

// List returns catalogue entries, optionally filtered by slug-prefix section.
func (s *Service) List(ctx context.Context, section string, limit int) ([]Doc, error) {
	result, err := s.Catalogue(ctx, section, limit)
	return result.Docs, err
}

// Search ranks pages against query using the origin's own search endpoint,
// which indexes every page body server-side: the only way to cover the whole
// corpus, since this origin publishes no bundled full-text file. When the
// origin cannot be reached, search degrades to local scoring over catalogue
// metadata plus any page bodies already cached: narrower, but an answer where
// one was possible rather than an error.
//
// limit <= 0 means no cap, as it does for SearchIndex and FilterDocs. The two
// paths must agree on that: a caller cannot tell which one served it, so a
// limit that meant different things would make the result set depend on
// whether the origin happened to be up. The origin path is still bounded by
// the largest page the endpoint will serve.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	result, err := s.SearchWithSources(ctx, query, limit)
	return result.Hits, err
}

// SearchWithSources returns coverage even when no pages match.
func (s *Service) SearchWithSources(ctx context.Context, query string, limit int) (SearchResult, error) {
	idx, err := s.index(ctx)
	result := SearchResult{Coverage: idx.Coverage}

	result.Coverage.Mode = "unavailable"
	if err != nil {
		return result, err
	}

	tokens := tokenize(query)
	if len(tokens) == 0 {
		result.Coverage.Mode = "none (no searchable keywords)"
		return result, nil
	}

	if !s.originCoolingDown() {
		upstream, serr := s.searchOrigin(ctx, idx, query, limit)
		if serr == nil {
			s.record(ctx, decision("search", "bypass", "origin", time.Time{}, 0))
			result.Hits = s.withSizes(upstream.Hits)
			result.Coverage.Mode = "upstream (bounded ranked retrieval)"
			result.Coverage.Sources = append(result.Coverage.Sources, upstream.Coverage.Sources...)

			return result, nil
		}

		if ctx.Err() != nil {
			return result, serr
		}

		s.noteOriginFailure()
	}

	result.Hits, result.Coverage.StaleBodies = s.searchLocal(ctx, idx, query, tokens, limit)
	if err := ctx.Err(); err != nil {
		return result, err
	}

	result.Hits = s.withSizes(result.Hits)
	result.Coverage.Mode = "fallback (catalogue metadata and cached bodies only; reduced coverage)"
	result.Coverage.Degraded = true

	s.record(ctx, decision("search", "fallback", "memory", time.Time{}, 0))

	return result, nil
}

// searchLocal ranks catalogue metadata and augments it with cached page
// bodies: a doc whose cached content contains every token is included even
// when its metadata does not match. This is the origin-outage path.
func (s *Service) searchLocal(ctx context.Context, idx *Index, query string, tokens []string, limit int) ([]Hit, bool) {
	staleBodies := false

	hits := SearchIndex(idx, query, 0)

	seen := make(map[string]bool, len(hits))

	for _, h := range hits {
		seen[h.Doc.Slug] = true
	}

	// Body matches: record the match cheaply, defer snippet building until
	// after sort+truncate so only the retained hits pay for section splitting.
	bodyMatched := make(map[string][]byte)
	decisions := make(map[string]CacheDecision)

	for _, d := range idx.Docs {
		if ctx.Err() != nil {
			break
		}

		body, state, ok, cacheDecision := s.pages.lookup(d.Slug)
		s.pages.log(cacheDecision)

		if state == StateStale {
			staleBodies = true
		}

		if !ok || seen[d.Slug] || !containsAll(strings.ToLower(string(body)), tokens) {
			continue
		}

		bodyMatched[d.Slug] = body

		if state == StateStale {
			cacheDecision.Status = cacheStaleServe
		}

		decisions[d.Slug] = cacheDecision

		hits = append(hits, Hit{Doc: d, Source: pageSource(d, cacheDecision.fetchedAt, state == StateStale), Score: bodyWeight * len(tokens), MatchedBody: true})
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	for i := range hits {
		if body, ok := bodyMatched[hits[i].Doc.Slug]; ok {
			hits[i].Snippet = bodySnippet(body, query, tokens)
			s.record(ctx, decisions[hits[i].Doc.Slug])
		}
	}

	return hits, staleBodies
}

func tokenize(q string) []string {
	fields := strings.Fields(strings.ToLower(q))
	out := fields[:0]

	for _, f := range fields {
		if len(f) > 1 {
			out = append(out, f)
		}
	}

	return out
}

// containsAll reports whether s contains every token.
func containsAll(s string, tokens []string) bool {
	for _, tok := range tokens {
		if !strings.Contains(s, tok) {
			return false
		}
	}

	return true
}

// bodySnippet returns the breadcrumb of the best-matching section plus a short
// window, actionable, so the agent can follow up with get_doc(slug,
// heading=...) instead of re-fetching the whole page to relocate the match.
func bodySnippet(body []byte, query string, tokens []string) string {
	if hits := SearchSections(SplitSections(body), query, 1); len(hits) > 0 && hits[0].Breadcrumb != "" {
		return hits[0].Breadcrumb + " - " + snippetAround(strings.ToLower(string(hits[0].Body)), string(hits[0].Body), tokens[0])
	}

	return snippetAround(strings.ToLower(string(body)), string(body), tokens[0])
}

// snippetAround extracts a short window of original text around the first
// occurrence of tok in lower (the lowercased original).
func snippetAround(lower, original, tok string) string {
	before, _, ok := strings.Cut(lower, tok)
	if !ok {
		return ""
	}

	// Case folding can change UTF-8 widths (for example İ becomes i).
	// Map the folded byte position back to rune positions in the original.
	startRune := utf8.RuneCountInString(before)
	runes := []rune(original)
	start := max(startRune-60, 0)
	end := min(startRune+utf8.RuneCountInString(tok)+60, len(runes))

	return strings.TrimSpace(string(runes[start:end]))
}

// editDistance is plain Levenshtein distance over bytes.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}

	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		cur[0] = i

		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}

		prev, cur = cur, prev
	}

	return prev[len(b)]
}

// withSizes uses only already-cached bodies; measuring a result never fetches it.
func (s *Service) withSizes(hits []Hit) []Hit {
	for i := range hits {
		if hits[i].Source.URL == "" {
			hits[i].Source = hits[i].Doc.Source()
		}

		if size, ok := s.pages.Size(hits[i].Doc.Slug); ok {
			hits[i].PageBytes = &size
		}
	}

	return hits
}
