package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

// Tool names, shared by registration and the logging/error paths so the two
// can never drift.
const (
	toolListDocs   = "list_docs"
	toolSearchDocs = "search_docs"
	toolGetDoc     = "get_doc"
)

const (
	listLimitDefault   = 50
	listLimitMax       = 200
	searchLimitDefault = 10
	searchLimitMax     = 50
	suggestCount       = 3
)

// Tool inputs. The SDK infers each tool's JSON schema from these structs, so
// the `jsonschema` tags are the descriptions clients see and `omitempty` is
// what makes a field optional: a field without it is required, and the SDK
// rejects a call that omits it before the handler runs.
type (
	listDocsInput struct {
		Section string `json:"section,omitempty" jsonschema:"Optional slug prefix filter, e.g. \"en/actions\""`
		Limit   int    `json:"limit,omitempty"   jsonschema:"Maximum entries returned (default 50, max 200)"`
	}

	searchDocsInput struct {
		Query string `json:"query"           jsonschema:"Keywords to search for, e.g. \"cache action dependencies\""`
		Limit int    `json:"limit,omitempty" jsonschema:"Maximum results returned (default 10, max 50)"`
	}

	getDocInput struct {
		Slug    string `json:"slug"              jsonschema:"Doc slug from list_docs or search_docs, e.g. \"en/actions/tutorials/build-and-test-code/nodejs\". A full docs URL or an absolute path also works."`
		Heading string `json:"heading,omitempty" jsonschema:"Optional heading, matched by exact text or its #kebab-anchor; returns only that section. A miss lists the page's real headings."`
		Query   string `json:"query,omitempty"   jsonschema:"Optional keywords; returns only the sections of the page matching them, verbatim with heading breadcrumbs; the cheapest way to pull one fact from a long page. Ignored when heading is set."`
		Offset  int    `json:"offset,omitempty"  jsonschema:"Byte offset to continue a truncated page or section from; use the value given in the truncation notice"`
	}
)

// readOnly is the annotation set shared by every tool here: all three read
// public documentation and nothing else.
func readOnly(title string) *mcp.ToolAnnotations {
	no := false

	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: &no,
		IdempotentHint:  true,
		OpenWorldHint:   nil, // nil defaults to true: the docs origin is external
	}
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        toolListDocs,
		Description: "List the GitHub documentation catalogue: slug, title and one-line description per page. Returns metadata only, never page content.",
		Annotations: readOnly("List documentation pages"),
	}, s.handleListDocs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        toolSearchDocs,
		Description: "Full-text search across all GitHub docs by keyword: titles, descriptions and the complete body of every page. Returns ranked slugs with a matching-section breadcrumb; follow up with get_doc(slug, heading=...) or get_doc(slug, query=...).",
		Annotations: readOnly("Search documentation"),
	}, s.handleSearchDocs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        toolGetDoc,
		Description: "Fetch one GitHub documentation page as markdown. Accepts a slug (\"en/actions\"), a full docs URL, or a #anchor. Returns page content only. Use query= for the cheapest focused lookup, heading= for a named section.",
		Annotations: readOnly("Get documentation page"),
	}, s.handleGetDoc)
}

// textResult and errorResult build the two result shapes every handler here
// returns. A failure is a tool result rather than a protocol error: the MCP
// contract puts it in front of the model so it can correct itself.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (s *Server) handleListDocs(ctx context.Context, _ *mcp.CallToolRequest, in listDocsInput) (*mcp.CallToolResult, any, error) {
	limit := clampLimit(in.Limit, listLimitDefault, listLimitMax)

	entries, err := s.svc.List(ctx, in.Section, limit)
	if err != nil {
		return s.toolError(ctx, toolListDocs, err, "section", in.Section), nil, nil
	}

	if len(entries) == 0 {
		return textResult("no docs match that section; call list_docs without section to see everything"), nil, nil
	}

	var b strings.Builder
	for _, d := range entries {
		fmt.Fprintf(&b, "%s - %s: %s\n", d.Slug, d.Title, d.Description)
	}

	return textResult(b.String()), nil, nil
}

func (s *Server) handleSearchDocs(ctx context.Context, _ *mcp.CallToolRequest, in searchDocsInput) (*mcp.CallToolResult, any, error) {
	limit := clampLimit(in.Limit, searchLimitDefault, searchLimitMax)

	hits, err := s.svc.Search(ctx, in.Query, limit)
	if err != nil {
		return s.toolError(ctx, toolSearchDocs, err, "query", in.Query), nil, nil
	}

	if len(hits) == 0 {
		return textResult("no matches; try broader keywords or list_docs"), nil, nil
	}

	var b strings.Builder

	for _, h := range hits {
		src := ""
		if h.MatchedBody {
			src = " [matched page content]"
		}

		fmt.Fprintf(&b, "%s - %s%s\n  %s\n", h.Doc.Slug, h.Doc.Title, src, h.Snippet)
	}

	return textResult(b.String()), nil, nil
}

func (s *Server) handleGetDoc(ctx context.Context, _ *mcp.CallToolRequest, in getDocInput) (*mcp.CallToolResult, any, error) {
	// query is section-search within the page: the token-thrift path. heading
	// (exact named section) takes precedence when both are given.
	if in.Query != "" && in.Heading == "" {
		return s.getDocSections(ctx, in.Slug, in.Query, in.Offset), nil, nil
	}

	page, err := s.svc.Get(ctx, in.Slug)
	if err != nil {
		return s.getDocError(ctx, in.Slug, err), nil, nil
	}

	content := page.Content

	if in.Heading != "" {
		content, err = docs.ExtractHeading(content, in.Heading)
		if err != nil {
			if errors.Is(err, docs.ErrHeadingNotFound) {
				return s.headingNotFoundError(ctx, in.Slug, in.Heading, page.Content), nil, nil
			}

			return s.toolError(ctx, toolGetDoc, err, "slug", in.Slug, "heading", in.Heading), nil, nil
		}
	}

	w := docs.Paginate(content, in.Offset)
	if in.Offset > 0 && len(w.Content) == 0 {
		return errorResult(fmt.Sprintf("offset %d is at or past the end of the content (%d bytes)", in.Offset, w.Total)), nil, nil
	}

	if w.Truncated() {
		s.logger.DebugContext(ctx, "content windowed", "tool", toolGetDoc, "slug", in.Slug, "offset", w.Offset, "next", w.Next, "total", w.Total)
	}

	return textResult(renderPage(page.Stale, in.Heading != "", w)), nil, nil
}

// getDocError translates a Service.Get failure into the right client copy.
func (s *Server) getDocError(ctx context.Context, slug string, err error) *mcp.CallToolResult {
	var fe *docs.FetchError
	if errors.As(err, &fe) && fe.StatusCode == http.StatusNotFound {
		// In the index but gone upstream: a different situation from a bad
		// slug, and "unreachable" would be a lie.
		s.logger.InfoContext(ctx, "doc removed upstream", "tool", toolGetDoc, "slug", slug, "error", err)

		return errorResult(fmt.Sprintf("doc %q was removed or moved upstream; the index is refreshing; retry shortly or use list_docs", slug))
	}

	if errors.Is(err, docs.ErrNotFound) {
		return s.notFoundError(ctx, slug, err)
	}

	return s.toolError(ctx, toolGetDoc, err, "slug", slug)
}

// renderPage assembles the client-facing text: staleness note, window notice
// at the TOP (so it survives preview clipping when clients persist large
// outputs), content, then the continuation marker.
func renderPage(stale, headingUsed bool, w docs.Window) string {
	var b strings.Builder

	if stale {
		b.WriteString("[note: served from cache; the docs origin is currently unreachable and this copy may be outdated]\n\n")
	}

	if w.Truncated() || w.Offset > 0 {
		fmt.Fprintf(&b, "[showing bytes %d-%d of %d]\n\n", w.Offset, w.Next, w.Total)
	}

	b.Write(w.Content)

	if w.Truncated() {
		advice := fmt.Sprintf("continue with offset=%d", w.Next)
		if !headingUsed && w.Offset == 0 {
			advice = fmt.Sprintf("use the heading argument for a single section, or continue with offset=%d", w.Next)
		}

		fmt.Fprintf(&b, "\n\n---\n[truncated: %s]\n", advice)
	}

	return b.String()
}

const sectionLimit = 5

// getDocSections handles get_doc's query path: return only the page sections
// matching query, verbatim, paginated as one document.
func (s *Server) getDocSections(ctx context.Context, slug, query string, offset int) *mcp.CallToolResult {
	hits, stale, err := s.svc.SearchPage(ctx, slug, query, sectionLimit)
	if err != nil {
		return s.getDocError(ctx, slug, err)
	}

	if len(hits) == 0 {
		return textResult(fmt.Sprintf("no sections of %q match %q; call get_doc without query to read the whole page, or search_docs to find a better page", slug, query))
	}

	var doc strings.Builder

	for i, h := range hits {
		if i > 0 {
			doc.WriteString("\n")
		}

		if h.Breadcrumb != "" {
			fmt.Fprintf(&doc, "› %s\n", h.Breadcrumb)
		}

		doc.Write(h.Body)
	}

	w := docs.Paginate([]byte(doc.String()), offset)
	if offset > 0 && len(w.Content) == 0 {
		return errorResult(fmt.Sprintf("offset %d is at or past the end of the matched sections (%d bytes)", offset, w.Total))
	}

	return textResult(renderPage(stale, false, w))
}

// headingNotFoundError lists the page's actual headings (or the closest ones)
// so a heading miss is recoverable in one hop instead of forcing a full-page
// re-fetch.
func (s *Server) headingNotFoundError(ctx context.Context, slug, heading string, content []byte) *mcp.CallToolResult {
	s.logger.InfoContext(ctx, "heading not found", "tool", toolGetDoc, "slug", slug, "heading", heading)

	headings := docs.PageHeadings(content)

	msg := fmt.Sprintf("heading %q not found in %q. ", heading, slug)

	switch {
	case len(headings) == 0:
		msg += "The page has no headings; call get_doc without heading."
	case len(headings) <= 12:
		msg += "Available headings: " + strings.Join(headings, "; ")
	default:
		msg += "Available headings include: " + strings.Join(headings[:12], "; ") + " … (call get_doc without heading, or use query= to search sections)"
	}

	return errorResult(msg)
}

// notFoundError builds the near-miss ErrNotFound tool error. It lives apart
// from toolError because it needs the service for suggestions.
func (s *Server) notFoundError(ctx context.Context, slug string, err error) *mcp.CallToolResult {
	s.logger.InfoContext(ctx, "doc not found", "tool", toolGetDoc, "slug", slug, "error", err)

	msg := fmt.Sprintf("doc %q not found", slug)
	if suggestions := s.svc.SuggestSlugs(ctx, slug, suggestCount); len(suggestions) > 0 {
		msg += "; closest matches: " + strings.Join(suggestions, ", ")
	}

	msg += ". Use list_docs or search_docs to find valid slugs."

	return errorResult(msg)
}

func clampLimit(v, def, maxAllowed int) int {
	if v <= 0 {
		return def
	}

	return min(v, maxAllowed)
}
