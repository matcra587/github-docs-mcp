// Command github-docs-mcp is an MCP server exposing the GitHub
// documentation from docs.github.com via list_docs, search_docs and get_doc
// tools, over stdio or streamable HTTP.
package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/matcra587/github-docs-mcp/internal/mcpserver"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	exitOK = iota
	exitRuntime
	exitUsage
)

// errVersionRequested signals the -version flag; not a failure.
var errVersionRequested = errors.New("version requested")

type config struct {
	transport      string
	httpAddr       string
	baseURL        string
	indexTTL       time.Duration
	pageTTL        time.Duration
	fetchRPS       float64
	cacheDir       string
	cacheMaxBytes  int64
	allowedOrigins string
	logLevel       string
}

func main() {
	version = buildVersion(version, debug.ReadBuildInfo)

	os.Exit(run())
}

func run() int {
	cfg, err := parseConfig(os.Args[1:])
	if errors.Is(err, errVersionRequested) {
		fmt.Println("github-docs-mcp", version)
		return exitOK
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}

	logger := newLogger(cfg.logLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting", "version", version, "transport", cfg.transport)

	if err := serve(ctx, cfg, logger); err != nil {
		logger.Error("server exited", "error", err)
		return exitRuntime
	}

	logger.Info("shutdown complete")

	return exitOK
}

// serve wires the docs service to the selected transport.
func serve(ctx context.Context, cfg *config, logger *slog.Logger) error {
	client, err := docs.NewClient(cfg.baseURL, docs.WithRateLimit(cfg.fetchRPS))
	if err != nil {
		return fmt.Errorf("docs client: %w", err)
	}

	svcCfg := docs.ServiceConfig{
		Logger:        logger,
		IndexTTL:      cfg.indexTTL,
		PageTTL:       cfg.pageTTL,
		CacheMaxBytes: cfg.cacheMaxBytes,
	}

	if cfg.cacheDir != "" {
		disk, err := docs.NewDiskCache(cacheOriginDir(cfg.cacheDir, cfg.baseURL))
		if err != nil {
			// Degrade to memory-only: the disk cache is an optimisation,
			// never a reason to refuse to start.
			logger.Warn("disk cache unavailable, using memory only", "dir", cfg.cacheDir, "error", err)
		} else {
			svcCfg.Disk = disk
		}
	}

	// A cache cap far above typical container memory is almost certainly a
	// units typo (e.g. bytes where MiB was meant) that will OOM-kill the pod
	// under normal use; warn, don't fail.
	if cfg.cacheMaxBytes > 2<<30 {
		logger.Warn("cache-max-bytes is very large; check the units", "bytes", cfg.cacheMaxBytes)
	}

	svc := docs.NewService(client, cfg.baseURL, svcCfg)
	srv := mcpserver.New(svc, logger, version)

	switch cfg.transport {
	case "stdio":
		return srv.ServeStdio(ctx)
	default:
		return srv.ServeHTTP(ctx, cfg.httpAddr, splitOrigins(cfg.allowedOrigins))
	}
}

func splitOrigins(v string) []string {
	if v == "" {
		return nil
	}

	parts := strings.Split(v, ",")
	out := parts[:0]

	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

func parseConfig(args []string) (*config, error) {
	cfg := &config{}

	fs := flag.NewFlagSet("github-docs-mcp", flag.ContinueOnError)
	fs.StringVar(&cfg.transport, "transport", cmp.Or(os.Getenv("MCP_TRANSPORT"), "stdio"), "transport: stdio or http")
	fs.StringVar(&cfg.httpAddr, "http-addr", cmp.Or(os.Getenv("MCP_HTTP_ADDR"), "127.0.0.1:8080"), "listen address for http transport")
	fs.StringVar(&cfg.baseURL, "base-url", cmp.Or(os.Getenv("DOCS_BASE_URL"), "https://docs.github.com"), "docs origin base URL (https; http allowed for loopback)")
	fs.StringVar(&cfg.cacheDir, "cache-dir", defaultCacheDir(), "disk cache directory (empty = memory only)")
	fs.StringVar(&cfg.allowedOrigins, "allowed-origins", os.Getenv("MCP_ALLOWED_ORIGINS"), "comma-separated Origin allow-list for http transport (empty = localhost only)")
	fs.StringVar(&cfg.logLevel, "log-level", cmp.Or(os.Getenv("LOG_LEVEL"), "info"), "log level: debug, info, warn, error")

	indexTTLDefault, err := envDuration("DOCS_INDEX_TTL", time.Hour)
	if err != nil {
		return nil, err
	}

	pageTTLDefault, err := envDuration("DOCS_PAGE_TTL", 24*time.Hour)
	if err != nil {
		return nil, err
	}

	rpsDefault, err := envFloat("DOCS_FETCH_RPS", 2)
	if err != nil {
		return nil, err
	}

	cacheMaxDefault, err := envInt64("DOCS_CACHE_MAX_BYTES", 64<<20)
	if err != nil {
		return nil, err
	}

	fs.DurationVar(&cfg.indexTTL, "index-ttl", indexTTLDefault, "TTL for the docs index")
	fs.DurationVar(&cfg.pageTTL, "page-ttl", pageTTLDefault, "TTL for cached pages")
	fs.Float64Var(&cfg.fetchRPS, "fetch-rps", rpsDefault, "outbound requests per second to the docs origin")
	fs.Int64Var(&cfg.cacheMaxBytes, "cache-max-bytes", cacheMaxDefault, "memory cache byte cap (LRU)")

	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if *showVersion {
		return nil, errVersionRequested
	}

	if cfg.transport != "stdio" && cfg.transport != "http" {
		return nil, fmt.Errorf("unknown transport %q (want stdio or http)", cfg.transport)
	}

	if cfg.fetchRPS <= 0 {
		return nil, fmt.Errorf("fetch-rps must be positive, got %v", cfg.fetchRPS)
	}

	if cfg.indexTTL <= 0 || cfg.pageTTL <= 0 {
		return nil, errors.New("ttls must be positive")
	}

	if cfg.cacheMaxBytes <= 0 {
		// A zero cap would silently reject every Put, and with it the
		// stale-serving the whole design leans on.
		return nil, fmt.Errorf("cache-max-bytes must be positive, got %d", cfg.cacheMaxBytes)
	}

	return cfg, nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level

	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	// stderr always: the stdio transport owns stdout for JSON-RPC.
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}

	return d, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}

	return f, nil
}

func envInt64(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}

	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}

	return n, nil
}

// defaultCacheDir distinguishes an explicitly empty environment override from
// an unset variable. Platforms without a usable user cache remain memory-only.
func defaultCacheDir() string {
	if dir, ok := os.LookupEnv("DOCS_CACHE_DIR"); ok {
		return dir
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	return filepath.Join(dir, "github-docs-mcp")
}

// Alternate origins must never hydrate the public origin's persisted content.
func cacheOriginDir(dir, baseURL string) string {
	origin := strings.TrimSuffix(baseURL, "/")
	if origin == "https://docs.github.com" {
		return dir
	}

	return filepath.Join(dir, "origins", fmt.Sprintf("%x", sha256.Sum256([]byte(origin))))
}
