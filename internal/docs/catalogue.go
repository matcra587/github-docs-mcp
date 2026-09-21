package docs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"
)

type catalogueComponent struct {
	index       *Index
	at          time.Time
	failedAt    time.Time
	cacheSource string
}

func (s *Service) parseComponent(key string, raw []byte) (*Index, error) {
	if key == diskIndexKey {
		return ParseIndex(s.baseURL, bytes.NewReader(raw))
	}

	entries, err := parsePageList(s.baseURL, bytes.NewReader(raw), true)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("empty pagelist: %w", ErrIndexUnavailable)
	}

	return newIndex(entries), nil
}

func (s *Service) componentSource(c catalogueComponent, url string) Source {
	source := Source{URL: url, Language: docsLanguage, Version: docsVersion, FetchedAt: c.at.UTC(), Freshness: "unavailable"}
	if c.index != nil {
		source.Freshness = freshnessFresh
		if s.now().Sub(c.at) >= s.indexTTL || !c.failedAt.IsZero() {
			source.Freshness = "stale"
		}
	}

	return source
}

func (s *Service) catalogueSnapshot() *Index {
	s.mu.RLock()
	curated, listed := s.curated, s.listed
	s.mu.RUnlock()

	var extra []Doc
	if listed.index != nil {
		extra = listed.index.Docs
	}

	idx := MergeIndex(curated.index, extra)

	idx.Coverage.Sources = []Source{s.componentSource(curated, s.baseURL+"/llms.txt"), s.componentSource(listed, s.pageListURL())}
	for _, source := range idx.Coverage.Sources {
		if source.Freshness != freshnessFresh {
			idx.Coverage.Degraded = true
		}
	}

	return idx
}

func (s *Service) index(ctx context.Context) (*Index, error) {
	if err := ctx.Err(); err != nil {
		return s.catalogueSnapshot(), err
	}

	s.mu.RLock()
	curated, listed := s.curated, s.listed
	s.mu.RUnlock()

	status := "hit"

	var err error

	if s.componentDue(curated) || s.componentDue(listed) {
		status = "miss"
		_, err = s.refreshIndex(ctx, false)
	}

	idx := s.catalogueSnapshot()
	for _, dependency := range idx.Coverage.Sources {
		if dependency.Freshness == "stale" {
			status = cacheStaleServe
		}
	}

	at, source := curated.at, curated.cacheSource
	if at.IsZero() || (!listed.at.IsZero() && listed.at.Before(at)) {
		at, source = listed.at, listed.cacheSource
	}

	s.record(ctx, decisionAt("index", status, source, at, s.indexTTL, s.now()))

	if err != nil {
		return idx, err
	}

	if len(idx.Docs) == 0 {
		return idx, ErrIndexUnavailable
	}

	return idx, nil
}

func (s *Service) componentDue(c catalogueComponent) bool {
	if !c.failedAt.IsZero() && s.now().Sub(c.failedAt) < failCooldown {
		return false
	}

	return c.index == nil || !c.failedAt.IsZero() || s.now().Sub(c.at) >= s.indexTTL
}

func (s *Service) refreshIndex(ctx context.Context, force bool) (*Index, error) {
	type refresh struct {
		index  *Index
		forced bool
	}

	for {
		result, err := fetchShared(ctx, s, "index", func(dctx context.Context) (refresh, error) {
			var workers sync.WaitGroup
			workers.Go(func() { s.refreshComponent(dctx, diskIndexKey, s.baseURL+"/llms.txt", force) })
			workers.Go(func() { s.refreshComponent(dctx, diskPageListKey, s.pageListURL(), force) })
			workers.Wait()

			idx := s.catalogueSnapshot()
			if len(idx.Docs) == 0 {
				return refresh{index: idx, forced: force}, ErrIndexUnavailable
			}

			return refresh{index: idx, forced: force}, nil
		})
		if err != nil {
			return s.catalogueSnapshot(), err
		}
		// A 404 refresh that joined an ordinary flight must still refresh fresh components.
		if !force || result.forced {
			return result.index, nil
		}
	}
}

func (s *Service) refreshComponent(ctx context.Context, key, url string, force bool) {
	s.mu.RLock()

	component := s.curated
	if key == diskPageListKey {
		component = s.listed
	}

	s.mu.RUnlock()

	if !force && !s.componentDue(component) {
		return
	}

	raw, err := s.fetcher.Fetch(ctx, url)

	var idx *Index
	if err == nil {
		idx, err = s.parseComponent(key, raw)
	}

	at := s.now()
	if err != nil {
		component.failedAt = at

		s.pages.logger.Debug("catalogue refresh failed", "key", key, "error", err)
	} else {
		component = catalogueComponent{index: idx, at: at, cacheSource: "memory"}
		s.storeDisk(key, raw, at)
		s.pages.log(decisionAt(key, "write", "memory", at, s.indexTTL, at))
	}

	s.mu.Lock()
	if key == diskIndexKey {
		s.curated = component
	} else {
		s.listed = component
	}
	s.mu.Unlock()
}

// Catalogue returns metadata alongside entries, including for empty selections.
func (s *Service) Catalogue(ctx context.Context, section string, limit int) (Catalogue, error) {
	idx, err := s.index(ctx)
	return Catalogue{Docs: FilterDocs(idx, section, limit), Coverage: idx.Coverage}, err
}
