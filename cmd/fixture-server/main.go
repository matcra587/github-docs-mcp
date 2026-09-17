// Command fixture-server serves the committed docs fixtures on :9999 so the
// real binary can run fully offline:
//
//	DOCS_BASE_URL=http://127.0.0.1:9999 github-docs-mcp
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const addr = "127.0.0.1:9999"

// Mirrors the origin's endpoint layout so the fixture exercises the same code
// paths as production. Kept in sync with internal/docs/apisearch.go.
const (
	pageListPath = "/api/pagelist/en/free-pro-team@latest"
	searchPath   = "/api/search/v1"
)

func main() {
	dir := "internal/docs/testdata"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	// Fixtures are read once at startup; request handling never touches the
	// filesystem, so no request data can influence a path.
	index := mustRead(dir, "llms.txt")

	// The committed fixture is a byte-exact copy of the production catalogue,
	// so its entry URLs point at the real origin. Rewrite them to this
	// server's base so the parser (which rightly drops foreign-host entries)
	// keeps them when DOCS_BASE_URL points here.
	index = bytes.ReplaceAll(index,
		[]byte("https://docs.github.com"),
		[]byte("http://"+addr))

	pageList := mustRead(dir, "pagelist.txt")
	page := mustRead(dir, "nodejs-guide.md")
	search := mustRead(dir, "search.json")

	mux := http.NewServeMux()

	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(index)
	})

	mux.HandleFunc(pageListPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(pageList)
	})

	// The committed search response is captured from the real endpoint, so its
	// hit URLs are already origin-relative paths and need no rewriting.
	mux.HandleFunc(searchPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		if q := r.URL.Query().Get("query"); q == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": []any{}})
			return
		}

		_, _ = w.Write(search)
	})

	// Every .md path serves the one committed page fixture, enough to
	// exercise the full flow offline. Anything else 404s, as the origin does
	// for a path that carries no article.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".md") {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write(page)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("fixture server on http://%s serving %s\n", addr, dir)
	log.Fatal(srv.ListenAndServe())
}

func mustRead(dir, name string) []byte {
	b, err := os.ReadFile(filepath.Join(filepath.Clean(dir), name)) //nolint:gosec // dev-only fixture server, path comes from argv not a request
	if err != nil {
		log.Fatalf("read %s fixture: %v", name, err)
	}

	return b
}
