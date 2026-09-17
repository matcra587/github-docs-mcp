package docs

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// curBytesLocked reads the tracked byte total under the cache lock so tests can
// assert the accounting invariant.
func (c *Cache) trackedBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.curBytes
}

// actualBytes sums the live entries independently of curBytes.
func (c *Cache) actualBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	var total int64

	for el := c.lru.Front(); el != nil; el = el.Next() {
		e := el.Value.(*cacheEntry) //nolint:forcetypeassert,errcheck // list only holds *cacheEntry
		total += int64(len(e.value))
	}

	return total
}

// TestCacheAccountingUnderChurn hammers Put/Get with overlapping keys and
// varying sizes, then asserts curBytes matches reality and never exceeds the
// cap. A bookkeeping bug (double-count on replace, missed eviction decrement)
// would surface as drift.
func TestCacheAccountingUnderChurn(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	const capBytes = 4096

	c := NewCache(capBytes)

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			key := "k" + strconv.Itoa(i%20) // 20 keys, heavy replacement
			size := 50 + (i%7)*40
			val := make([]byte, size)
			c.Put(key, val, time.Hour)
			_, _, _ = c.Get(key)
		})
	}

	wg.Wait()

	tracked := c.trackedBytes()
	actual := c.actualBytes()

	is.Equal(actual, tracked, "byte accounting drift")
	is.LessOrEqual(tracked, int64(capBytes), "cache exceeded cap")
	is.GreaterOrEqual(tracked, int64(0), "negative byte count")
}

// TestCacheReplaceSameKeyAccounting pins the replace path: overwriting a key
// with a different-sized value must adjust curBytes by exactly the delta.
func TestCacheReplaceSameKeyAccounting(t *testing.T) {
	t.Parallel()

	is := assert.New(t)

	c := NewCache(1 << 20)

	c.Put("k", make([]byte, 100), time.Hour)
	is.Equal(int64(100), c.trackedBytes(), "after first put")

	c.Put("k", make([]byte, 30), time.Hour)
	is.Equal(int64(30), c.trackedBytes(), "after shrink: double-count or stale delta")

	c.Put("k", make([]byte, 500), time.Hour)
	is.Equal(int64(500), c.trackedBytes(), "after grow")

	is.Equal(int64(500), c.actualBytes(), "actual disagrees")
}
