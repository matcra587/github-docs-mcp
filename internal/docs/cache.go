package docs

import (
	"container/list"
	"sync"
	"time"
)

// EntryState describes cache entry freshness.
type EntryState int

// Entry freshness states; StateUnknown sits at zero so an uninitialised
// value never reads as a real state.
const (
	StateUnknown EntryState = iota
	StateFresh
	StateStale
)

// Cache is a byte-capped LRU cache whose entries expire into a stale-but-
// servable state rather than being deleted: expiry never evicts, only
// replacement or byte-cap pressure does. Stored values are copied on the way
// in and out, so entries are immutable to callers. Safe for concurrent use.
type Cache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	entries  map[string]*list.Element
	lru      *list.List // front = most recently used
}

type cacheEntry struct {
	key      string
	value    []byte
	storedAt time.Time
	ttl      time.Duration
}

// NewCache returns a Cache holding at most maxBytes of values.
func NewCache(maxBytes int64) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}
}

// Get returns the value for key and its freshness state.
func (c *Cache) Get(key string) ([]byte, EntryState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return nil, StateUnknown, false
	}

	c.lru.MoveToFront(el)
	e := el.Value.(*cacheEntry) //nolint:forcetypeassert,errcheck // list only ever holds *cacheEntry

	state := StateFresh
	if time.Since(e.storedAt) >= e.ttl {
		state = StateStale
	}

	out := make([]byte, len(e.value))
	copy(out, e.value)

	return out, state, true
}

// Put stores value under key with ttl, replacing any existing entry. Values
// larger than the byte cap are silently not cached; the caller still has the
// value; caching it is impossible without evicting everything else.
func (c *Cache) Put(key string, value []byte, ttl time.Duration) {
	c.putAged(key, value, ttl, time.Now())
}

// putAged is Put with an explicit storage time, used when rehydrating from
// disk so persisted entries keep their real age (and staleness).
func (c *Cache) putAged(key string, value []byte, ttl time.Duration, storedAt time.Time) {
	if int64(len(value)) > c.maxBytes {
		return
	}

	stored := make([]byte, len(value))
	copy(stored, value)

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		e := el.Value.(*cacheEntry) //nolint:forcetypeassert,errcheck // list only ever holds *cacheEntry
		c.curBytes -= int64(len(e.value))
		e.value = stored
		e.storedAt = storedAt
		e.ttl = ttl
		c.curBytes += int64(len(stored))
		c.lru.MoveToFront(el)
	} else {
		el := c.lru.PushFront(&cacheEntry{key: key, value: stored, storedAt: storedAt, ttl: ttl})
		c.entries[key] = el
		c.curBytes += int64(len(stored))
	}

	for c.curBytes > c.maxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}

		e := oldest.Value.(*cacheEntry) //nolint:forcetypeassert,errcheck // list only ever holds *cacheEntry
		c.lru.Remove(oldest)
		delete(c.entries, e.key)
		c.curBytes -= int64(len(e.value))
	}
}
