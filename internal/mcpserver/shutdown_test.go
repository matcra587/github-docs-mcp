package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matcra587/github-docs-mcp/internal/docs"
)

// slowFetcher blocks each fetch on release so a request can be caught in flight.
type slowFetcher struct {
	release chan struct{}
	started chan struct{}
	body    []byte
}

func (f *slowFetcher) Fetch(ctx context.Context, _ string) ([]byte, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}

	select {
	case <-f.release:
		return f.body, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func freePort(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// TestGracefulShutdownDrainsInflight proves ServeHTTP does not truncate a
// request that is mid-flight when the context is cancelled: the in-flight tool
// call completes, and only then does ServeHTTP return.
// Deliberately NOT parallel: this drives a real HTTP server with
// timing-sensitive assertions, and competing with the whole parallel suite
// under -race makes those windows flaky.
func TestGracefulShutdownDrainsInflight(t *testing.T) { //nolint:paralleltest // timing-sensitive real-server test, must run without contention
	is := assert.New(t)

	fetcher := &slowFetcher{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
		body:    []byte("- [Alpha](https://docs.github.com/en/alpha.md): a\n"),
	}
	release := sync.OnceFunc(func() { close(fetcher.release) })
	t.Cleanup(release)
	// A pooled transport can leave a speculative connection in StateNew,
	// making Shutdown wait for the header timeout after the request drains.
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	svc := docs.NewService(fetcher, "https://docs.github.com", docs.ServiceConfig{
		IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1 << 20,
	})
	srv := New(svc, slog.New(slog.DiscardHandler), "test")

	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)

	go func() { serveErr <- srv.ServeHTTP(ctx, addr, nil) }()

	waitListening(t, client, addr)

	// Fire a request that will block inside the fetcher (index load).
	respCh := make(chan int, 1)

	go func() {
		status, _ := postMCPAddr(client, addr, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_docs","arguments":{}}}`)
		respCh <- status
	}()

	// Wait until the fetch is genuinely in flight, then ask the server to stop.
	select {
	case <-fetcher.started:
	case <-time.After(10 * time.Second):
		t.Fatal("request never reached the fetcher")
	}

	cancel()

	// The server must not have returned yet; the request is still draining.
	select {
	case err := <-serveErr:
		t.Fatalf("server returned before in-flight request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Let the fetch complete; the request and shutdown should both finish.
	release()

	select {
	case status := <-respCh:
		is.Equal(http.StatusOK, status, "in-flight request was truncated")
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-serveErr:
		is.True(err == nil || errors.Is(err, http.ErrServerClosed), "shutdown returned error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down after draining")
	}
}

func waitListening(t *testing.T, client *http.Client, addr string) {
	t.Helper()

	// Poll the real handler (not just the TCP port): /healthz answering 200
	// proves the mux is serving, avoiding a race where the socket is open but
	// the handler is not yet wired.
	for range 150 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/healthz", nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("server never became ready on %s", addr)
}

// postMCPAddr is safe to call from a spawned goroutine: it never uses
// require/FailNow (which must run on the test goroutine), returning any error
// as the string result instead.
func postMCPAddr(client *http.Client, addr, body string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+"/mcp", strings.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}

	defer func() { _ = resp.Body.Close() }()

	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err.Error()
	}

	return resp.StatusCode, string(response)
}
