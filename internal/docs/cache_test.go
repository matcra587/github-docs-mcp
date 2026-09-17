package docs

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCacheLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("fresh within ttl", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			is := assert.New(t)

			c := NewCache(1 << 20)
			c.Put("k", []byte("v1"), time.Hour)

			val, state, ok := c.Get("k")
			is.True(ok)
			is.Equal(StateFresh, state)
			is.Equal("v1", string(val))
		})
	})

	t.Run("stale after ttl is retained and servable", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			is := assert.New(t)

			c := NewCache(1 << 20)
			c.Put("k", []byte("v1"), time.Hour)

			time.Sleep(2 * time.Hour)

			val, state, ok := c.Get("k")
			is.True(ok, "expired entry must be servable stale")
			is.Equal(StateStale, state, "expired entry must be servable stale")
			is.Equal("v1", string(val), "expired entry must be servable stale")
		})
	})

	t.Run("replacement resets freshness", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			is := assert.New(t)

			c := NewCache(1 << 20)
			c.Put("k", []byte("v1"), time.Hour)
			time.Sleep(2 * time.Hour)
			c.Put("k", []byte("v2"), time.Hour)

			val, state, ok := c.Get("k")
			is.True(ok)
			is.Equal(StateFresh, state)
			is.Equal("v2", string(val))
		})
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c := NewCache(1 << 20)
		_, _, ok := c.Get("nope")
		is.False(ok, "expected miss")
	})

	t.Run("byte cap evicts least recently used", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c := NewCache(10)
		c.Put("a", []byte("aaaa"), time.Hour) // 4 bytes
		c.Put("b", []byte("bbbb"), time.Hour) // 8 total
		_, _, _ = c.Get("a")                  // touch a; b becomes LRU
		c.Put("c", []byte("cccc"), time.Hour) // 12 > 10: evict b

		_, _, ok := c.Get("b")
		is.False(ok, "b should be evicted")

		_, _, ok = c.Get("a")
		is.True(ok, "a should survive (recently used)")

		_, _, ok = c.Get("c")
		is.True(ok, "c should be present")
	})

	t.Run("expiry never evicts", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			is := assert.New(t)

			c := NewCache(1 << 20)
			c.Put("k", []byte("v"), time.Nanosecond)
			time.Sleep(time.Hour)

			_, state, ok := c.Get("k")
			is.True(ok, "expired entry must remain")
			is.Equal(StateStale, state, "expired entry must remain")
		})
	})

	t.Run("oversized value not cached", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c := NewCache(4)
		c.Put("big", []byte("toolarge"), time.Hour)

		_, _, ok := c.Get("big")
		is.False(ok, "value larger than cap must not be cached")
	})

	t.Run("stored value immutable against caller mutation", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)

		c := NewCache(1 << 20)
		src := []byte("orig")
		c.Put("k", src, time.Hour)
		src[0] = 'X'

		val, _, _ := c.Get("k")
		is.Equal("orig", string(val), "cache shares caller's backing array")
	})
}
