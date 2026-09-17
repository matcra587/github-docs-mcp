package docs

import (
	"bytes"
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
	Content []byte
	Stale   bool
}

// ServiceConfig carries the cache and freshness knobs for a Service.
type ServiceConfig struct {
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
	fetcher  Fetcher
	baseURL  string
	pages    *Cache
	indexTTL time.Duration
	pageTTL  time.Duration
	sf       singleflight.Group
	disk     *DiskCache

	mu        sync.RWMutex
	idx       *Index
	idxAt     time.Time
	idxSource string

	failMu     sync.Mutex
	lastFailAt time.Time
}

// NewService returns a Service reading from baseURL via fetcher. When a disk
// cache is configured, persisted entries are loaded immediately; entries past
// their TTL come back as stale-servable, which is the whole point of the disk
// layer: a restart during an origin outage still has content to serve.
func NewService(fetcher Fetcher, baseURL string, cfg ServiceConfig) *Service {
	s := &Service{
		fetcher:  fetcher,
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		pages:    NewCache(cfg.CacheMaxBytes),
		indexTTL: cfg.IndexTTL,
		pageTTL:  cfg.PageTTL,
		disk:     cfg.Disk,
	}
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

	entries, err := s.disk.Load()
	if err != nil {
		return
	}

	var (
		indexRaw    []byte
		pageListRaw []byte
		indexAt     time.Time
	)

	for _, e := range entries {
		switch e.Key {
		case diskIndexKey:
			indexRaw, indexAt = e.Value, e.StoredAt
		case diskPageListKey:
			pageListRaw = e.Value
		default:
			if slug, ok := strings.CutPrefix(e.Key, diskPagePrefix); ok {
				s.pages.putSource(slug, e.Value, s.pageTTL, e.StoredAt, "disk")
			}
			// Unprefixed keys (including any legacy layout) are ignored.
		}
	}

	if indexRaw == nil {
		return
	}

	idx, perr := ParseIndex(s.baseURL, bytes.NewReader(indexRaw))
	if perr != nil {
		return
	}

	if pageListRaw != nil {
		idx = MergeIndex(idx, ParsePageList(s.baseURL, bytes.NewReader(pageListRaw)))
	}

	s.mu.Lock()
	s.idx, s.idxAt, s.idxSource = idx, indexAt, "disk"
	s.mu.Unlock()
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
		return Page{}, err
	}

	doc, ok := idx.BySlug(slug)
	if !ok {
		return Page{}, fmt.Errorf("slug %q: %w", slug, ErrNotFound)
	}

	content, state, cached, d := s.pages.lookup(slug)
	if cached && state == StateFresh {
		s.record(ctx, d)
		return Page{Content: content}, nil
	}

	if cached && s.originCoolingDown() {
		d.Status = "stale-serve"
		s.record(ctx, d)

		return Page{Content: content, Stale: true}, nil
	}

	d.Status = "miss"
	s.record(ctx, d)

	fetched, err := s.fetchShared(ctx, "page:"+slug, func(dctx context.Context) ([]byte, error) {
		body, ferr := s.fetcher.Fetch(dctx, doc.URL)
		if ferr != nil {
			return nil, ferr
		}

		// Caching inside the singleflight closure means exactly one writer
		// per fetch and a result that survives even when every waiter has
		// abandoned it.
		s.pages.Put(slug, body, s.pageTTL)
		s.storeDisk(diskPagePrefix+slug, body)
		s.noteOriginHealthy()

		return body, nil
	})
	if err == nil {
		return Page{Content: fetched}, nil
	}

	// A cancelled caller gets its cancellation, never a stale copy dressed up
	// as an origin outage.
	if errors.Is(err, context.Canceled) {
		return Page{}, fmt.Errorf("fetch page %q: %w", slug, err)
	}

	if errors.Is(err, ErrNotFound) {
		// The index is stale: the page vanished upstream. Refresh it in the
		// background so the catalogue heals; the singleflight key dedups.
		s.refreshIndexAsync()

		return Page{}, fmt.Errorf("fetch page %q: %w", slug, err)
	}

	s.noteOriginFailure()

	if stale, _, ok, staleDecision := s.pages.lookup(slug); ok {
		staleDecision.Status = "stale-serve"
		s.record(ctx, staleDecision)

		return Page{Content: stale, Stale: true}, nil
	}

	return Page{}, fmt.Errorf("fetch page %q: %w", slug, err)
}

// index returns a fresh index when possible, refreshing through singleflight;
// when refresh fails, or the origin is cooling down after a failure, a
// previously parsed index is served instead (§2b lifecycle: a working
// catalogue is never discarded).
func (s *Service) index(ctx context.Context) (*Index, error) {
	s.mu.RLock()
	idx, at, source := s.idx, s.idxAt, s.idxSource
	s.mu.RUnlock()

	if idx != nil && time.Since(at) < s.indexTTL {
		s.record(ctx, decision("index", "hit", source, at, s.indexTTL))
		return idx, nil
	}

	if idx != nil && s.originCoolingDown() {
		s.record(ctx, decision("index", "stale-serve", source, at, s.indexTTL))
		return idx, nil
	}

	s.record(ctx, decision("index", "miss", source, at, s.indexTTL))

	fresh, err := s.refreshIndex(ctx)
	if err == nil {
		return fresh, nil
	}

	if idx != nil {
		s.record(ctx, decision("index", "stale-serve", source, at, s.indexTTL))
		return idx, nil
	}

	return nil, err
}

func (s *Service) refreshIndex(ctx context.Context) (*Index, error) {
	_, err := s.fetchShared(ctx, "index", func(dctx context.Context) ([]byte, error) {
		raw, ferr := s.fetcher.Fetch(dctx, s.baseURL+"/llms.txt")
		if ferr != nil {
			return nil, fmt.Errorf("fetch index: %w", ferr)
		}

		idx, perr := ParseIndex(s.baseURL, bytes.NewReader(raw))
		if perr != nil {
			return nil, fmt.Errorf("parse index: %w", perr)
		}

		// llms.txt is a curated shortlist; the page list is the origin's
		// authoritative set of paths that resolve. Widening with it is what
		// lets get_doc accept the thousands of slugs the origin's own pages
		// link to. Best-effort: a page-list failure leaves the curated
		// catalogue intact rather than failing the whole refresh.
		if list, lerr := s.fetcher.Fetch(dctx, s.pageListURL()); lerr == nil {
			idx = MergeIndex(idx, ParsePageList(s.baseURL, bytes.NewReader(list)))
			s.storeDisk(diskPageListKey, list)
		}

		s.mu.Lock()
		s.idx, s.idxAt, s.idxSource = idx, time.Now(), "memory"
		s.pages.log(decision("index", "write", "memory", s.idxAt, s.indexTTL))
		s.mu.Unlock()

		s.storeDisk(diskIndexKey, raw)
		s.noteOriginHealthy()

		return raw, nil
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.noteOriginFailure()
		}

		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.idx, nil
}

func (s *Service) refreshIndexAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()

		// Failure is deliberately dropped: the next index() call surfaces it.
		_, _ = s.refreshIndex(ctx)
	}()
}

// storeDisk best-effort persists an entry; a failing disk (full, read-only)
// silently degrades the service to memory-only, exactly its cold behaviour.
func (s *Service) storeDisk(key string, value []byte) {
	if s.disk == nil {
		return
	}

	if err := s.disk.Store(key, value); err != nil {
		s.pages.logger.Debug("cache decision", "key", key, "status", "write-failed", "source", "disk", "age_ms", 0, "error", err)
	} else {
		s.pages.log(decision(key, "write", "disk", time.Now(), 0))
	}
}

func (s *Service) originCoolingDown() bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()

	return !s.lastFailAt.IsZero() && time.Since(s.lastFailAt) < failCooldown
}

func (s *Service) noteOriginFailure() {
	s.failMu.Lock()
	s.lastFailAt = time.Now()
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
func (s *Service) fetchShared(ctx context.Context, key string, work func(context.Context) ([]byte, error)) ([]byte, error) {
	ch := s.sf.DoChan(key, func() (any, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()

		return work(dctx)
	})

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}

		return res.Val.([]byte), nil //nolint:forcetypeassert,errcheck // singleflight fn only returns []byte
	}
}
