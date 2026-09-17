package docs

import (
	"context"
	"sync"
	"time"
)

// CacheDecision describes the entry considered by a request. Missing entries
// have no age or remaining TTL; expired entries have zero remaining TTL.
type CacheDecision struct {
	Key            string `json:"key"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	AgeMS          *int64 `json:"age_ms"`
	RemainingTTLMS *int64 `json:"remaining_ttl_ms"`
}

type cacheTraceKey struct{}

// CacheTrace holds request-local decisions, including catalogue dependencies.
type CacheTrace struct {
	mu        sync.Mutex
	decisions []CacheDecision
}

// TraceCache starts a request-local trace without changing cache behaviour.
func TraceCache(ctx context.Context) (context.Context, *CacheTrace) {
	t := &CacheTrace{}
	return context.WithValue(ctx, cacheTraceKey{}, t), t
}

// Decisions returns the final decision for each entry in first-use order.
func (t *CacheTrace) Decisions() []CacheDecision {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]CacheDecision{}, t.decisions...)
}

func decision(key, status, source string, at time.Time, ttl time.Duration) CacheDecision {
	d := CacheDecision{Key: key, Status: status, Source: source}
	if d.Source == "" {
		d.Source = "memory"
	}

	if !at.IsZero() {
		age := max(time.Since(at), 0)
		ageMS, remainingMS := age.Milliseconds(), max(ttl-age, 0).Milliseconds()
		d.AgeMS, d.RemainingTTLMS = &ageMS, &remainingMS
	}

	return d
}

func (c *Cache) log(d CacheDecision) {
	c.logger.Debug("cache decision", "key", d.Key, "status", d.Status,
		"source", d.Source, "age_ms", d.AgeMS, "remaining_ttl_ms", d.RemainingTTLMS)
}

func (s *Service) record(ctx context.Context, d CacheDecision) {
	s.pages.log(d)

	t, ok := ctx.Value(cacheTraceKey{}).(*CacheTrace)
	if !ok {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := range t.decisions {
		if t.decisions[i].Key == d.Key {
			t.decisions[i] = d
			return
		}
	}

	t.decisions = append(t.decisions, d)
}
