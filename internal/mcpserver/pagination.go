package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) listPage(ctx context.Context, req *mcp.CallToolRequest, in listDocsInput) (result *mcp.CallToolResult) {
	cursor := continuation{Version: cursorVersion, Tool: toolListDocs, Origin: s.svc.Origin(), Selection: selection{Section: in.Section, Language: in.Language}, Limit: clampLimit(in.Limit, listLimitDefault, listLimitMax)}

	cursor, resuming, err := resolveCursor(req, in.Cursor, cursor)
	if err != nil {
		return errorResult(err.Error())
	}

	view, err := s.listView(cursor.Selection)
	if err != nil {
		return errorResult(err.Error())
	}

	catalogue, err := view.Catalogue(ctx, cursor.Selection.Section, 0)
	defer func() { withProvenance(result, docs.Source{}, catalogue.Coverage) }()

	if err != nil {
		return s.toolError(ctx, toolListDocs, err, "section", cursor.Selection.Section)
	}

	fingerprint := catalogueFingerprint(catalogue.Docs)
	if resuming && fingerprint != cursor.Snapshot {
		return cursor.restartError()
	}

	cursor.Snapshot = fingerprint

	total := len(catalogue.Docs)
	if resuming && cursor.Position >= total {
		return errorResult("invalid cursor position; restart the original call")
	}

	end := cursor.Position + min(cursor.Limit, total-cursor.Position)

	first := cursor.Position + 1
	if total == 0 {
		first = 0
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[entries %d-%d of %d; more: %t]\n", first, end, total, end < total)

	if total == 0 {
		b.WriteString("no docs match that section; call list_docs without section to see everything\n")
	}

	for _, doc := range catalogue.Docs[cursor.Position:end] {
		writeCatalogueEntry(&b, doc)
	}

	if end < total {
		cursor.Position = end

		next, err := continuationText(cursor)
		if err != nil {
			return errorResult(err.Error())
		}

		b.WriteString(next)
	}

	return textResult(b.String())
}

func (s *Server) docPage(ctx context.Context, req *mcp.CallToolRequest, in getDocInput) (result *mcp.CallToolResult) {
	cursor := continuation{Version: cursorVersion, Tool: toolGetDoc, Origin: s.svc.Origin(), Selection: selection{Slug: in.Slug, Heading: in.Heading, Query: in.Query}, Offset: max(0, in.Offset), Limit: pageWindowBytes}

	cursor, resuming, err := resolveCursor(req, in.Cursor, cursor)
	if err != nil {
		return errorResult(err.Error())
	}

	if strings.TrimSpace(cursor.Selection.Slug) == "" {
		return errorResult("slug is required for an initial get_doc call; continuations accept only cursor")
	}

	page, err := s.svc.Get(ctx, cursor.Selection.Slug)
	defer func() { withProvenance(result, page.Source, page.Catalogue) }()

	if err != nil {
		return s.getDocError(ctx, cursor.Selection.Slug, err)
	}

	fingerprint := pageFingerprint(page)
	if resuming && cursor.Snapshot != fingerprint {
		return cursor.restartError()
	}

	cursor.Snapshot = fingerprint

	selected, err := selectContent(page.Content, &cursor)
	if err != nil {
		return s.selectionError(ctx, page, cursor, err)
	}

	if resuming && !validBytePosition(selected.content, cursor.Offset) {
		return errorResult("invalid cursor byte position; restart the original call")
	}

	window := docs.Paginate(selected.content, cursor.Offset)
	if cursor.Offset > 0 && len(window.Content) == 0 {
		return errorResult(fmt.Sprintf("offset %d is at or past the end of the content (%d bytes)", cursor.Offset, window.Total))
	}

	return renderContinuation(page.Stale, selected.summary, window, cursor, selected.nextGroup, !resuming && in.Offset > 0)
}

type selectedContent struct {
	content   []byte
	summary   string
	nextGroup int
}

func selectContent(body []byte, cursor *continuation) (selectedContent, error) {
	if cursor.Selection.Heading != "" {
		content, err := docs.ExtractHeading(body, cursor.Selection.Heading)
		return selectedContent{content: content}, err
	}

	if cursor.Selection.Query != "" {
		cursor.Limit = sectionLimit
		content, summary, next, err := sectionGroup(body, *cursor)

		return selectedContent{content: content, summary: summary, nextGroup: next}, err
	}

	return selectedContent{content: body}, nil
}

func validBytePosition(content []byte, offset int) bool {
	return offset < len(content) && utf8.RuneStart(content[offset])
}

func (s *Server) selectionError(ctx context.Context, page docs.Page, cursor continuation, err error) *mcp.CallToolResult {
	if errors.Is(err, docs.ErrHeadingNotFound) {
		return s.headingNotFoundError(ctx, cursor.Selection.Slug, cursor.Selection.Heading, page.Content)
	}

	return errorResult(err.Error())
}

func sectionGroup(body []byte, cursor continuation) ([]byte, string, int, error) {
	hits := docs.SearchSections(docs.SplitSections(body), cursor.Selection.Query, 0)
	if cursor.Position > 0 && cursor.Position >= len(hits) {
		return nil, "", 0, errors.New("invalid cursor section position; restart the original call")
	}

	end := cursor.Position + min(sectionLimit, len(hits)-cursor.Position)

	first := cursor.Position + 1
	if len(hits) == 0 {
		first = 0
	}

	summary := fmt.Sprintf("[sections %d-%d of %d]\n", first, end, len(hits))
	if len(hits) == 0 {
		summary += "no sections match; call get_doc without query to read the whole page, or search_docs to find a better page\n"
	}

	var b strings.Builder

	for i, hit := range hits[cursor.Position:end] {
		if i > 0 {
			b.WriteByte('\n')
		}

		if hit.Breadcrumb != "" {
			fmt.Fprintf(&b, "› %s\n", hit.Breadcrumb)
		}

		b.Write(hit.Body)
	}

	next := 0
	if end < len(hits) {
		next = end
	}

	return []byte(b.String()), summary, next, nil
}

func renderContinuation(stale bool, summary string, window docs.Window, cursor continuation, nextGroup int, legacy bool) *mcp.CallToolResult {
	var b strings.Builder
	b.WriteString(summary)

	more := window.Truncated() || nextGroup > 0

	if stale {
		b.WriteString("[note: served from cache; this copy may be outdated]\n\n")
	}

	if legacy {
		b.WriteString("[warning: offset-only continuations cannot validate snapshots; prefer cursor]\n\n")
	}

	fmt.Fprintf(&b, "[showing bytes %d-%d of %d; more: %t]\n\n", window.Offset, window.Next, window.Total, more)
	b.Write(window.Content)

	if more {
		if window.Truncated() {
			cursor.Offset = window.Next
		} else {
			cursor.Position, cursor.Offset = nextGroup, 0
		}

		next, err := continuationText(cursor)
		if err != nil {
			return errorResult(err.Error())
		}

		b.WriteString(next)

		if window.Truncated() && cursor.Position == 0 {
			fmt.Fprintf(&b, "[truncated; legacy offset=%d cannot validate snapshots]\n", window.Next)
		}
	}

	return textResult(b.String())
}

func (s *Server) listView(selected selection) (*docs.Service, error) {
	view, err := s.svc.ForLanguage(selected.Language)
	if err != nil {
		return nil, err
	}

	if selected.Section != "" && selected.Section != view.Language() && !strings.HasPrefix(selected.Section, view.Language()+"/") {
		return nil, fmt.Errorf("section must start with the selected language (%s); pass a matching language and section", view.Language())
	}

	return view, nil
}

func writeCatalogueEntry(b *strings.Builder, doc docs.Doc) {
	b.WriteString(doc.Slug)

	if doc.Title != "" {
		fmt.Fprintf(b, " - %s: %s", doc.Title, doc.Description)
	}

	fmt.Fprintf(b, "\n  %s", sourceText(doc.Source()))
}
