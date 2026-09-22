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
		Language string `json:"language,omitempty" jsonschema:"Documentation language: en (default), es, ja, pt, zh, ru, fr, ko, de. Must match any section prefix."`
		Cursor   string `json:"cursor,omitempty" jsonschema:"Opaque continuation from a previous list_docs result; pass cursor only"`
		Section  string `json:"section,omitempty" jsonschema:"Optional slug prefix filter, e.g. \"en/actions\""`
		Limit    int    `json:"limit,omitempty"   jsonschema:"Maximum entries returned (default 50, max 200)"`
	}

	searchDocsInput struct {
		Language string `json:"language,omitempty" jsonschema:"Documentation language: en (default), es, ja, pt, zh, ru, fr, ko, de. Queries are passed through without translation."`
		Query    string `json:"query"           jsonschema:"Keywords to search for, e.g. \"cache action dependencies\""`
		Limit    int    `json:"limit,omitempty" jsonschema:"Maximum results returned (default 10, max 50)"`
	}

	getDocInput struct {
		Cursor  string `json:"cursor,omitempty" jsonschema:"Opaque continuation from a previous get_doc result; pass cursor only"`
		Slug    string `json:"slug,omitempty"              jsonschema:"Doc slug from list_docs or search_docs, e.g. \"en/actions/tutorials/build-and-test-code/nodejs\". A full docs URL or an absolute path also works."`
		Heading string `json:"heading,omitempty" jsonschema:"Optional heading, matched by exact text or its #kebab-anchor; returns only that section. A miss lists the page's real headings."`
		Query   string `json:"query,omitempty"   jsonschema:"Optional keywords; returns only the sections of the page matching them, verbatim with heading breadcrumbs; the cheapest way to pull one fact from a long page. Ignored when heading is set."`
		Offset  int    `json:"offset,omitempty"  jsonschema:"Legacy byte offset; cannot validate snapshots. Prefer cursor continuations."`
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
		Description: "List the GitHub documentation catalogue in the selected language (default en): slugs and available metadata. Non-English listings contain slugs without English title substitutions. Follow cursor-only continuations for all entries.",
		Annotations: readOnly("List documentation pages"),
	}, s.handleListDocs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        toolSearchDocs,
		Description: "Bounded ranked search using GitHub’s upstream index, with reduced coverage over catalogue metadata and cached bodies during outages. Returns ranked slugs with contextual breadcrumbs, not executable heading names. Follow up with get_doc(slug) or get_doc(slug, query=...). Hits missing from the catalogue carry an availability notice.",
		Annotations: readOnly("Search documentation"),
	}, s.handleSearchDocs)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        toolGetDoc,
		Description: "Fetch one GitHub documentation page as markdown. Accepts a page slug or a URL on the configured docs origin, optionally with #anchor. Selection precedence is heading, query, fragment, then full page. A fragment requires a page path; missing heading anchors return an error. Query strings, foreign origins and enterprise/version paths are rejected. Returns page content and source freshness. Use query= for focused lookup, heading= for a named section. Initial calls require slug; follow cursor-only continuations for every matching section and byte window.",
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

func (s *Server) handleListDocs(ctx context.Context, req *mcp.CallToolRequest, in listDocsInput) (result *mcp.CallToolResult, output any, err error) {
	ctx, trace := docs.TraceCache(ctx)

	defer func() {
		if result != nil {
			result.Meta = mcp.Meta{"cache": trace.Decisions()}
		}
	}()

	return s.listPage(ctx, req, in), nil, nil
}

func (s *Server) handleSearchDocs(ctx context.Context, _ *mcp.CallToolRequest, in searchDocsInput) (result *mcp.CallToolResult, output any, err error) {
	ctx, trace := docs.TraceCache(ctx)

	defer func() {
		if result != nil {
			result.Meta = mcp.Meta{"cache": trace.Decisions()}
		}
	}()

	limit := clampLimit(in.Limit, searchLimitDefault, searchLimitMax)

	view, err := s.svc.ForLanguage(in.Language)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}

	search, err := view.SearchWithSources(ctx, in.Query, limit)

	defer func() {
		if result != nil {
			withProvenance(result, docs.Source{}, search.Coverage)
		}
	}()

	hits := search.Hits

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

		size := " [page bytes: unknown]"
		if h.PageBytes != nil {
			size = fmt.Sprintf(" [cached page bytes: %d]", *h.PageBytes)
		}

		fmt.Fprintf(&b, "%s - %s%s%s\n  %s\n  %s", h.Doc.Slug, h.Doc.Title, src, size, h.Snippet, sourceText(h.Source))
	}

	return textResult(b.String()), nil, nil
}

func (s *Server) handleGetDoc(ctx context.Context, req *mcp.CallToolRequest, in getDocInput) (result *mcp.CallToolResult, output any, err error) {
	ctx, trace := docs.TraceCache(ctx)

	defer func() {
		if result != nil {
			result.Meta = mcp.Meta{"cache": trace.Decisions()}
		}
	}()

	return s.docPage(ctx, req, in), nil, nil
}

// getDocError translates a Service.Get failure into the right client copy.
func (s *Server) getDocError(ctx context.Context, slug string, err error) *mcp.CallToolResult {
	if errors.Is(err, docs.ErrUnsupportedLanguage) {
		return errorResult(err.Error())
	}

	if errors.Is(err, docs.ErrNotFound) {
		if language := docs.SlugLanguage(slug); language != "en" {
			message := fmt.Sprintf("doc %q is unavailable in the requested language (%s).", slug, language)
			if alternative := s.svc.EnglishAlternative(ctx, slug); alternative != "" {
				message += fmt.Sprintf(" English alternative: get_doc({\"slug\":%q}).", alternative)
			}

			return errorResult(message)
		}
	}

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

const sectionLimit = 5

// headingNotFoundError lists the page's actual headings (or the closest ones)
// so a heading miss is recoverable in one hop instead of forcing a full-page
// re-fetch.
func (s *Server) headingNotFoundError(ctx context.Context, slug, heading string, content []byte) *mcp.CallToolResult {
	s.logger.InfoContext(ctx, "heading not found", "tool", toolGetDoc, "slug", slug, "heading", heading)

	headings := docs.PageHeadings(content)

	msg := fmt.Sprintf("heading %q not found or unsupported in %q. Only heading anchors select sections, not arbitrary HTML IDs. Read the page with get_doc({\"slug\":%q}), or select an actual heading below. ", heading, slug, slug)

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
