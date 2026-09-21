package docs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// fetchTimeout bounds a detached singleflight fetch, which outlives any
// individual caller's context by design (§2b: one caller's cancellation must
// not fail every waiter).
const fetchTimeout = 45 * time.Second

// failCooldown is how long after an origin failure the service serves stale
// content immediately instead of paying full fetch-and-retry latency on every
// request during an outage.
const failCooldown = 30 * time.Second

// Page is a fetched documentation page. Stale marks content served past its
// TTL because the origin could not be reached (stale beats error).
type Page struct {
	Content   []byte
	Stale     bool
	Source    Source
	Catalogue Coverage
}

// ServiceConfig carries the cache and freshness knobs for a Service.
type ServiceConfig struct {
	// Now supplies the clock for freshness and fetch timestamps; nil uses time.Now.
	Now           func() time.Time
	Logger        *slog.Logger
	IndexTTL      time.Duration
	PageTTL       time.Duration
	CacheMaxBytes int64
	// Disk, when set, persists entries across restarts (write-through on
	// store, loaded as stale-servable at construction).
	Disk *DiskCache
}

// Disk keys share the singleflight namespacing ("index", "page:<slug>") so no
// slug, since the parser accepts arbitrary path suffixes, can collide with a
// catalogue entry.
const (
	diskIndexKey    = "index"
	diskPageListKey = "pagelist"
	diskPagePrefix  = "page:"
)

// Service is the docs domain API consumed by the MCP layer: index management,
// page retrieval with the stale-servable cache lifecycle, and search.
type Service struct {
	now      func() time.Time
	fetcher  Fetcher
	baseURL  string
	pages    *Cache
	indexTTL time.Duration
	pageTTL  time.Duration
	sf       singleflight.Group
	disk     *DiskCache

	mu      sync.RWMutex
	curated catalogueComponent
	listed  catalogueComponent

	failMu     sync.Mutex
	lastFailAt time.Time
}

// NewService returns a Service reading from baseURL via fetcher. When a disk
// cache is configured, persisted entries are loaded immediately; entries past
// their TTL come back as stale-servable, which is the whole point of the disk
// layer: a restart during an origin outage still has content to serve.
func NewService(fetcher Fetcher, baseURL string, cfg ServiceConfig) *Service {
	s := &Service{
		now:      cfg.Now,
		fetcher:  fetcher,
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		pages:    NewCache(cfg.CacheMaxBytes),
		indexTTL: cfg.IndexTTL,
		pageTTL:  cfg.PageTTL,
		disk:     cfg.Disk,
	}
	if s.now == nil {
		s.now = time.Now
	}

	s.pages.now = s.now
	if cfg.Logger != nil {
		s.pages.logger = cfg.Logger
	}

	s.loadDisk()

	return s
}

// loadDisk hydrates memory state from the disk cache; any failure leaves the
// service in its cold-start state, which is always correct. The catalogue's two
// sources are collected before parsing so the restored index is the same merge
// a live refresh would produce.
func (s *Service) loadDisk() {
	if s.disk == nil {
		return
	}

	entries, err := s.disk.loadBounded(s.pages.maxBytes + 2*catalogueMaxBytes)
	if err != nil {
		return
	}

	for _, e := range entries {
		switch e.Key {
		case diskIndexKey, diskPageListKey:
			idx, parseErr := s.parseComponent(e.Key, e.Value)
			if parseErr != nil {
				continue
			}

			component := catalogueComponent{index: idx, at: e.StoredAt, cacheSource: "disk"}
			if e.Key == diskIndexKey {
				s.curated = component
			} else {
				s.listed = component
			}
		default:
			if slug, ok := strings.CutPrefix(e.Key, diskPagePrefix); ok {
				s.pages.putSource(slug, e.Value, s.pageTTL, e.StoredAt, "disk")
			}
		}
	}
}

// normalizeSlug forgives the slug forms an agent naturally lifts from a page:
// a full docs URL, an absolute path (which is exactly how the origin's own
// pages write their cross-links), a trailing .md, a #fragment. This turns a
// link-following miss (which otherwise dead-ends and nudges toward WebFetch)
// into a hit.
func normalizeSlug(slug string) string {
	slug = strings.TrimSpace(slug)

	// A full URL: keep only its path. Any host is accepted here because the
	// slug is resolved against the catalogue afterwards, so an off-origin URL
	// simply fails to match, which is the correct outcome either way.
	if i := strings.Index(slug, "://"); i >= 0 {
		rest := slug[i+len("://"):]

		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}

		slug = rest[j:]
	}

	return cleanSlug(strings.TrimPrefix(slug, "/"))
}

// Get returns the markdown for slug. Unknown slugs return ErrNotFound without
// touching the origin. When the origin fails and an expired copy exists, that
// copy is served with Stale set; during the post-failure cool-down the stale
// copy is served without re-attempting the fetch at all.
func (s *Service) Get(ctx context.Context, slug string) (Page, error) {
	slug = normalizeSlug(slug)

	idx, err := s.index(ctx)
	if err != nil {
		return Page{Catalogue: idx.Coverage}, err
	}

	doc, ok := idx.BySlug(slug)
	if !ok {
		return Page{Catalogue: idx.Coverage}, fmt.Errorf("slug %q: %w", slug, ErrNotFound)
	}

	content, state, cached, d := s.pages.lookup(slug)
	if cached && state == StateFresh {
		s.record(ctx, d)
		return s.page(doc, content, d.fetchedAt, false, idx.Coverage), nil
	}

	if cached && s.originCoolingDown() {
		d.Status = cacheStaleServe
		s.record(ctx, d)

		return s.page(doc, content, d.fetchedAt, true, idx.Coverage), nil
	}

	d.Status = "miss"
	s.record(ctx, d)

	fetched, err := fetchShared(ctx, s, "page:"+slug, func(dctx context.Context) (Page, error) {
		body, ferr := s.fetcher.Fetch(dctx, doc.URL)
		if ferr != nil {
			return Page{}, ferr
		}

		// Caching inside the singleflight closure means exactly one writer
		// per fetch and a result that survives even when every waiter has
		// abandoned it.
		at := s.now()
		s.pages.putAged(slug, body, s.pageTTL, at)
		s.storeDisk(diskPagePrefix+slug, body, at)
		s.noteOriginHealthy()

		return s.page(doc, body, at, false, Coverage{}), nil
	})
	if err == nil {
		fetched.Catalogue = idx.Coverage
		return fetched, nil
	}

	// A cancelled caller gets its cancellation, never a stale copy dressed up
	// as an origin outage.
	if ctx.Err() != nil {
		return s.page(doc, nil, time.Time{}, false, idx.Coverage), fmt.Errorf("fetch page %q: %w", slug, err)
	}

	if errors.Is(err, ErrNotFound) {
		// The index is stale: the page vanished upstream. Refresh it in the
		// background so the catalogue heals; the singleflight key dedups.
		s.refreshIndexAsync()

		return s.page(doc, nil, time.Time{}, false, idx.Coverage), fmt.Errorf("fetch page %q: %w", slug, err)
	}

	s.noteOriginFailure()

	if stale, _, ok, staleDecision := s.pages.lookup(slug); ok {
		staleDecision.Status = cacheStaleServe
		s.record(ctx, staleDecision)

		return s.page(doc, stale, staleDecision.fetchedAt, true, idx.Coverage), nil
	}

	return s.page(doc, nil, time.Time{}, false, idx.Coverage), fmt.Errorf("fetch page %q: %w", slug, err)
}

func (s *Service) refreshIndexAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()

		// Failure is deliberately dropped: the next index() call surfaces it.
		_, _ = s.refreshIndex(ctx, true)
	}()
}

// storeDisk best-effort persists an entry; a failing disk (full, read-only)
// silently degrades the service to memory-only, exactly its cold behaviour.
func (s *Service) storeDisk(key string, value []byte, at time.Time) {
	if s.disk == nil {
		return
	}

	if err := s.disk.storeAt(key, value, at); err != nil {
		s.pages.logger.Debug("cache decision", "key", key, "status", "write-failed", "source", "disk", "age_ms", 0, "error", err)
	} else {
		s.pages.log(decisionAt(key, "write", "disk", at, 0, s.now()))
	}
}

func (s *Service) originCoolingDown() bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()

	return !s.lastFailAt.IsZero() && s.now().Sub(s.lastFailAt) < failCooldown
}

func (s *Service) noteOriginFailure() {
	s.failMu.Lock()
	s.lastFailAt = s.now()
	s.failMu.Unlock()
}

func (s *Service) noteOriginHealthy() {
	s.failMu.Lock()
	s.lastFailAt = time.Time{}
	s.failMu.Unlock()
}

// fetchShared runs work once per key through singleflight on a detached
// context, so concurrent callers share a single request and one caller's
// cancellation cannot fail the rest. The calling context still controls how
// long this caller waits.
func fetchShared[T any](ctx context.Context, s *Service, key string, work func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	ch := s.sf.DoChan(key, func() (any, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()

		return work(dctx)
	})

	select {
	case <-ctx.Done():
		return zero, context.Cause(ctx)
	case res := <-ch:
		if res.Err != nil {
			return zero, res.Err
		}

		return res.Val.(T), nil //nolint:forcetypeassert,errcheck // Each key has one concrete result type.
	}
}
