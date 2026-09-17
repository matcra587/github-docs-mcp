package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Body caps: ordinary pages are small, so cap them tightly; only the catalogue
// endpoints (llms.txt and the page list, which enumerates thousands of paths)
// need the larger ceiling. Both still bound memory against a misbehaving
// origin.
const (
	pageMaxBytes      = 2 << 20
	catalogueMaxBytes = 8 << 20
)

const (
	maxAttempts = 3
	baseBackoff = 500 * time.Millisecond
	// maxRetryAfter caps how long a Retry-After header can make us wait. Kept
	// well under the service fetch timeout (service.fetchTimeout) so two
	// clamped waits plus request time cannot exhaust the budget before the
	// attempts run, so a docs fetch fails fast to the stale-serve path rather
	// than pinning the caller.
	maxRetryAfter = 8 * time.Second
)

// Fetcher retrieves a raw document body by absolute URL. Implementations must
// be safe for concurrent use.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) ([]byte, error)
}

// waiter is the slice of rate.Limiter the client needs; an interface so tests
// can observe or bypass throttling.
type waiter interface {
	Wait(ctx context.Context) error
}

// Client is the HTTP Fetcher for the docs origin. It refuses URLs outside its
// base host, never follows redirects off it, self-throttles with a token
// bucket, and retries 429/5xx/transient failures with jittered backoff.
type Client struct {
	base    *url.URL
	http    *http.Client
	limiter waiter
	sleep   func(ctx context.Context, d time.Duration) error
}

var _ Fetcher = (*Client)(nil)

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithRateLimit sets the outbound token bucket to rps requests per second
// with a burst of 2×rps (minimum 1).
func WithRateLimit(rps float64) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(rate.Limit(rps), max(int(2*rps), 1))
	}
}

// NewClient validates baseURL (https required; plain http permitted only for
// loopback hosts, so local fixture servers work) and returns a hardened client.
func NewClient(baseURL string, opts ...ClientOption) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}

	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("plain http base url %q allowed only for loopback hosts", baseURL)
		}
	default:
		return nil, fmt.Errorf("unsupported base url scheme %q", u.Scheme)
	}

	c := &Client{
		base:    u,
		limiter: rate.NewLimiter(2, 4),
		sleep:   sleepCtx,
	}
	c.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
		},
		// Redirects must stay on the base host and scheme: a compromised
		// or misbehaving origin must not steer fetches elsewhere or downgrade
		// https to http. A custom policy also drops the stdlib's 10-hop cap,
		// so reinstate it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}

			if req.URL.Host != u.Host {
				return fmt.Errorf("redirect to %q outside base host", req.URL.Host)
			}

			if req.URL.Scheme != u.Scheme {
				return fmt.Errorf("redirect changes scheme to %q", req.URL.Scheme)
			}

			return nil
		},
		Jar: nil, // never store or replay origin cookies
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// Fetch GETs rawURL, which must sit under the client's base host. Each attempt
// waits on the rate limiter; retryable failures (429, 5xx, network errors)
// back off exponentially with jitter, honouring Retry-After as a floor. A 404
// maps to ErrNotFound and is never retried.
func (c *Client) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	if u.Host != c.base.Host {
		return nil, fmt.Errorf("url %q outside base host %q", rawURL, c.base.Host)
	}

	var (
		lastErr    error
		retryAfter time.Duration
	)

	for attempt := range maxAttempts {
		if attempt > 0 {
			backoff := baseBackoff << (attempt - 1)
			backoff += rand.N(backoff / 2) //nolint:gosec // jitter needs no cryptographic strength
			wait := max(backoff, retryAfter)

			// If the wait won't fit the remaining deadline, stop now and
			// return the real terminal error rather than sleeping into a
			// generic context cancellation; the "up to maxAttempts" contract
			// must degrade to the last FetchError, not DeadlineExceeded.
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= wait {
				return nil, lastErr
			}

			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
		}

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}

		body, ra, err := c.doOnce(ctx, rawURL)
		if err == nil {
			return body, nil
		}

		if !isRetryable(err) {
			return nil, err
		}

		lastErr, retryAfter = err, ra
	}

	return nil, lastErr
}

// doOnce performs a single GET, returning any Retry-After hint alongside the
// error so the retry loop can respect it.
func (c *Client) doOnce(ctx context.Context, rawURL string) ([]byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("User-Agent", "github-docs-mcp")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, &FetchError{URL: rawURL, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Page listed in a stale index but gone upstream.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, 0, &FetchError{URL: rawURL, StatusCode: resp.StatusCode, Err: ErrNotFound}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")), &FetchError{URL: rawURL, StatusCode: resp.StatusCode}
	}

	// No endpoint this server reads serves HTML: pages come back as markdown,
	// the catalogue as plain text, search as JSON. HTML on a 200 means the
	// origin rendered a page (or an error) instead of returning source, and
	// caching that as documentation would poison the cache for a full TTL.
	if ct := resp.Header.Get("Content-Type"); isHTML(ct) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, 0, &FetchError{URL: rawURL, Err: fmt.Errorf("%w (content-type %q)", errUnexpectedHTML, ct)}
	}

	limit := bodyCap(rawURL)

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, &FetchError{URL: rawURL, Err: err}
	}

	if int64(len(body)) > limit {
		return nil, 0, fmt.Errorf("body for %q exceeds %d byte size cap", rawURL, limit)
	}

	return body, 0, nil
}

// errUnexpectedHTML marks a 200 response carrying HTML where markdown, plain
// text or JSON was expected.
var errUnexpectedHTML = errors.New("unexpected html response")

// isRetryable reports whether err is worth another attempt: 429, any 5xx, or
// a transport-level failure that is not a caller cancellation. 404 and other
// 4xx are terminal.
func isRetryable(err error) bool {
	var fe *FetchError
	if !errors.As(err, &fe) {
		return false
	}

	// An HTML body on a 200 is a deterministic origin behaviour, not a blip;
	// retrying only triples the load on an origin that will answer the same
	// way each time.
	if errors.Is(fe.Err, errUnexpectedHTML) {
		return false
	}

	switch {
	case fe.StatusCode == http.StatusTooManyRequests:
		return true
	case fe.StatusCode >= 500:
		return true
	case fe.StatusCode == 0:
		return !errors.Is(fe.Err, context.Canceled) && !errors.Is(fe.Err, context.DeadlineExceeded)
	default:
		return false
	}
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}

	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return min(time.Duration(secs)*time.Second, maxRetryAfter)
	}

	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, maxRetryAfter)
		}
	}

	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// bodyCap returns the read cap for a URL: the larger catalogue cap for the
// llms.txt and page-list endpoints that together build the index, the tight
// page cap for everything else.
func bodyCap(rawURL string) int64 {
	if strings.HasSuffix(rawURL, "/llms.txt") || strings.Contains(rawURL, pageListPath+"/") {
		return catalogueMaxBytes
	}

	return pageMaxBytes
}

// isHTML reports whether a Content-Type names an HTML document, ignoring any
// parameters (charset and friends).
func isHTML(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	return false
}
