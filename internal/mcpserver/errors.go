package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

// toolError is the single error-handling boundary: the full chain is logged
// here with structured context, and the client receives only a friendly
// message with a recovery hint. Internal error text never crosses over.
func (s *Server) toolError(ctx context.Context, tool string, err error, attrs ...any) *mcp.CallToolResult {
	logAttrs := append([]any{"tool", tool, "error", err}, attrs...)

	switch {
	case errors.Is(err, context.Canceled):
		s.logger.DebugContext(ctx, "tool call cancelled", logAttrs...)
		return errorResult("request cancelled")
	case errors.Is(err, docs.ErrNotFound):
		s.logger.InfoContext(ctx, "doc not found", logAttrs...)
		return errorResult("doc not found; use list_docs or search_docs to find valid slugs")
	case errors.Is(err, docs.ErrHeadingNotFound):
		s.logger.InfoContext(ctx, "heading not found", logAttrs...)
		return errorResult("heading not found in that page; call get_doc without heading to see the full page")
	case errors.Is(err, docs.ErrIndexUnavailable):
		s.logger.ErrorContext(ctx, "docs index unavailable", logAttrs...)
		return errorResult("the docs index is currently unavailable; try again shortly")
	default:
		s.logger.ErrorContext(ctx, "tool call failed", logAttrs...)
		return errorResult("the docs site could not be reached; try again shortly")
	}
}
