package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxRequestBytes caps inbound JSON-RPC bodies on /mcp. Tool arguments are
// tiny; anything near this size is abuse.
const maxRequestBytes = 1 << 20

const shutdownGrace = 10 * time.Second

// ServeHTTP runs the streamable HTTP transport on addr until ctx is
// cancelled, then shuts down gracefully. allowedOrigins extends the
// browser-Origin allow-list beyond localhost. Note that originGate is the only
// origin check in the stack: the SDK's CrossOriginProtection is left nil, so
// nothing below this rejects a foreign Origin.
func (s *Server) ServeHTTP(ctx context.Context, addr string, allowedOrigins []string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.httpHandler(allowedOrigins),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	errCh := make(chan error, 1)

	go func() {
		s.logger.Info("http transport listening", "addr", addr)

		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http transport: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}

	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http transport: %w", err)
	}

	return nil
}

// httpHandler assembles the transport mux: /mcp (origin-gated, body-capped
// streamable HTTP) and /healthz (process health only, never origin
// reachability, so an origin outage cannot evict pods whose whole value is
// serving stale content).
func (s *Server) httpHandler(allowedOrigins []string) http.Handler {
	// Stateless: every request carries its own short-lived session, so no
	// server-side session table can grow unbounded on a public listener.
	// Origin checking stays with originGate below rather than the SDK's
	// CrossOriginProtection, because the allow-list is a documented knob
	// (MCP_ALLOWED_ORIGINS) with loopback always permitted.
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: s.logger},
	)

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle("/mcp", s.originGate(allowedOrigins, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		streamable.ServeHTTP(w, r)
	})))

	return mux
}

// originGate rejects browser-originated requests whose Origin is neither
// localhost nor allow-listed. Requests without an Origin header (CLI clients,
// servers) pass through; the header only exists in browser contexts, which
// is exactly where DNS-rebinding and drive-by requests come from.
func (s *Server) originGate(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || originAllowed(origin, allowed) {
			next.ServeHTTP(w, r)
			return
		}

		s.logger.WarnContext(r.Context(), "rejected foreign origin", "origin", origin)
		http.Error(w, "forbidden origin", http.StatusForbidden)
	})
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSuffix(a, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}

	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
