package docs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiskCacheBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := NewDiskCache(dir)
	require.NoError(t, err)

	d.maxBytes = 6
	require.NoError(t, d.Store("first", []byte("abc")))

	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "first"), old, old))
	require.NoError(t, d.Store("second", []byte("def")))
	require.NoError(t, d.Store("third", []byte("ghi")))

	_, err = os.Stat(filepath.Join(dir, "first"))
	require.True(t, os.IsNotExist(err), "oldest write should be evicted")
	entries, err := d.Load()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	entries, err = d.loadBounded(3)
	require.NoError(t, err)
	require.Len(t, entries, 1, "hydration must honour its smaller budget")
	require.NoError(t, d.Store("oversized", []byte("1234567")))

	_, err = os.Stat(filepath.Join(dir, "oversized"))
	require.True(t, os.IsNotExist(err))
}

func TestDiskCacheIndependentWriters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewDiskCache(dir)
	require.NoError(t, err)
	b, err := NewDiskCache(dir)
	require.NoError(t, err)
	// A fresh temporary file may still belong to another process.
	active := filepath.Join(dir, "key.tmp.active")
	require.NoError(t, os.WriteFile(active, []byte("partial"), 0o600))

	var wg sync.WaitGroup

	errors := make(chan error, 40)

	for range 20 {
		wg.Go(func() { errors <- a.Store("key", []byte("writer A")) })
		wg.Go(func() {
			_, err := b.Load()
			if err != nil {
				errors <- err
				return
			}

			errors <- b.Store("key", []byte("writer B"))
		})
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}

	_, err = os.Stat(active)
	require.NoError(t, err, "load removed an active writer's temporary file")
	entries, err := b.Load()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, []string{"writer A", "writer B"}, string(entries[0].Value))
}

func TestDiskCacheLoadsCatalogueBeforePages(t *testing.T) {
	t.Parallel()
	d, err := NewDiskCache(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, d.Store(diskIndexKey, []byte("index")))
	require.NoError(t, d.Store(diskPagePrefix+"en/alpha", []byte("large page body")))
	require.NoError(t, d.Store(diskPageListKey, []byte("pagelist")))
	entries, err := d.loadBounded(int64(len("index") + len("pagelist") + len("large page body") - 1))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, diskIndexKey, entries[0].Key)
	require.Equal(t, diskPageListKey, entries[1].Key)
}

func TestDiskCachePrunesPagesBeforeCatalogue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := NewDiskCache(dir)
	require.NoError(t, err)

	d.maxBytes = 15
	require.NoError(t, d.Store(diskIndexKey, []byte("index")))
	require.NoError(t, d.Store(diskPageListKey, []byte("list")))

	old := time.Now().Add(-time.Hour)
	for _, key := range []string{diskIndexKey, diskPageListKey} {
		require.NoError(t, os.Chtimes(filepath.Join(dir, key), old, old))
	}

	for _, key := range []string{"page:first", "page:second", "page:third"} {
		require.NoError(t, d.Store(key, []byte("abc")))
	}

	entries, err := d.Load()
	require.NoError(t, err)

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}

	require.Len(t, entries, 4)
	require.Contains(t, keys, diskIndexKey)
	require.Contains(t, keys, diskPageListKey)
}
