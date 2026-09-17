// Package mcpserver wires the docs service to MCP transports and owns the
// error-translation and logging boundary: errors are logged here and nowhere
// below, and clients only ever see friendly tool errors.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

// Server exposes the docs service over MCP transports.
type Server struct {
	mcp    *mcp.Server
	svc    *docs.Service
	logger *slog.Logger
}

// New builds the MCP server with all tools registered.
func New(svc *docs.Service, logger *slog.Logger, version string) *Server {
	s := &Server{
		svc:    svc,
		logger: logger,
	}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{
			Name:        "github-docs",
			Title:       "GitHub documentation",
			Description: "Read-only access to the GitHub documentation at docs.github.com.",
			Version:     version,
		},
		&mcp.ServerOptions{
			Logger: logger,
			// A non-nil value replaces the SDK's historical default of
			// {"logging":{}}. The MCP logging feature is deprecated as of
			// 2026-07-28 (SEP-2577) and this server does not implement it,
			// diagnostics go to stderr, which is where the spec now points.
			// ListChanged stays false because the three tools are registered
			// once at construction and never change, so a client that caches
			// tools/list is never wrong.
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: false},
			},
		},
	)
	s.mcp.AddReceivingMiddleware(recoverMiddleware(logger), cacheHintMiddleware)
	s.registerTools()

	return s
}

// toolListTTL is how long a client may cache tools/list. The catalogue served
// by the tools changes constantly; the tool list itself never does within a
// process lifetime, so this is bounded only by how long a deploy might run.
const toolListTTL = time.Hour

// cacheHintMiddleware stamps the 2026-07-28 cache hints (SEP-2549) onto list
// results. The SDK models the fields but leaves the policy to the server, and
// without them a client must re-fetch tools/list on every reconnect.
func cacheHintMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}

		if lt, ok := res.(*mcp.ListToolsResult); ok {
			lt.TTLMs = int(toolListTTL.Milliseconds())
			// The tool list is identical for every caller: no credentials,
			// no per-user filtering, so intermediaries may share it.
			lt.CacheScope = "public"
		}

		return res, nil
	}
}

// recoverMiddleware turns a panic in any handler into a JSON-RPC error rather
// than a killed process. The SDK has no built-in equivalent, and a docs server
// losing its transport because one malformed page tripped a slice bound would
// take down every tool, not just the failing call.
func recoverMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.ErrorContext(ctx, "panic in handler",
						"method", method, "panic", r, "stack", string(debug.Stack()))

					// The panic value may carry internals; it is logged above
					// and never crosses to the client.
					res, err = nil, errors.New("internal error")
				}
			}()

			return next(ctx, method, req)
		}
	}
}

// ServeStdio runs the stdio transport until ctx is cancelled or stdin closes.
func (s *Server) ServeStdio(ctx context.Context) error {
	// Run reports the cancellation that stopped it; a requested shutdown is
	// not a failure.
	if err := s.mcp.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return fmt.Errorf("stdio transport: %w", err)
	}

	return nil
}
