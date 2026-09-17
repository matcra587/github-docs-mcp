package docs

import (
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// DiskCache persists cache entries as flat files under one directory, scoped
// with os.Root so no key, however hostile, can write outside it. Filenames
// are the URL-escaped key; mtime carries the entry's age across restarts.
// A process-shared lock serializes disk reads, replacement and pruning.
// Temporary files keep incomplete writes out of the cache.
type DiskCache struct {
	root     *os.Root
	maxBytes int64
}

// DiskEntry is one persisted cache entry.
type DiskEntry struct {
	Key      string
	Value    []byte
	StoredAt time.Time
}

// NewDiskCache creates dir (0700) if needed and opens it as the cache root.
func NewDiskCache(dir string) (*DiskCache, error) {
	// #nosec G703 -- The cache directory is an explicit local CLI setting.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open cache root: %w", err)
	}

	return &DiskCache{root: root, maxBytes: 256 << 20}, nil
}

// Store publishes a complete value under key with private permissions.
func (d *DiskCache) Store(key string, value []byte) error {
	if key == diskLockName {
		return fmt.Errorf("reserved cache key: %w", os.ErrInvalid)
	}

	if int64(len(value)) > d.maxBytes {
		return nil
	}

	lock, err := d.lock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	name := encodeKey(key)

	// A unique temp name per call keeps concurrent Stores of the same key
	// from clobbering each other's in-flight file; O_EXCL guarantees it.
	// The .tmp suffix is filtered out by Load.
	tmp := name + ".tmp." + rand.Text()

	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp cache file: %w", err)
	}

	if _, err := f.Write(value); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)

		return fmt.Errorf("write cache file: %w", err)
	}

	// Without a sync, a crash between rename and writeback can publish a
	// zero-length file that Load would hydrate as real content.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)

		return fmt.Errorf("sync cache file: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = d.root.Remove(tmp)
		return fmt.Errorf("close cache file: %w", err)
	}

	if err := d.root.Rename(tmp, name); err != nil {
		_ = d.root.Remove(tmp)
		return fmt.Errorf("finalise cache file: %w", err)
	}

	d.prune()

	return nil
}

// Load returns persisted entries within the disk budget; files that are not valid entries (bad
// encoding, leftover temp files) are skipped, never fatal.
func (d *DiskCache) Load() ([]DiskEntry, error) {
	return d.loadBounded(d.maxBytes)
}

func (d *DiskCache) loadBounded(budget int64) ([]DiskEntry, error) {
	lock, err := d.lock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()

	files, err := fs.ReadDir(d.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read cache dir: %w", err)
	}
	// Restore the complete catalogue before page bodies consume the budget.
	// Otherwise a fresh but incomplete index can reject cached page-list slugs.
	sort.SliceStable(files, func(i, j int) bool {
		return isCatalogueKey(files[i].Name()) && !isCatalogueKey(files[j].Name())
	})

	var (
		entries []DiskEntry
		loaded  int64
	)

	for _, file := range files {
		if file.Name() == diskLockName || !file.Type().IsRegular() {
			continue
		}

		name := file.Name()
		// Only sweep old temporary files; another process may still be writing.
		if strings.Contains(name, ".tmp.") {
			if info, err := file.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = d.root.Remove(name)
			}

			continue
		}

		entry, ok := d.loadEntry(name, budget-loaded)
		if !ok {
			continue
		}

		loaded += int64(len(entry.Value))
		entries = append(entries, entry)
	}

	return entries, nil
}

// loadEntry skips invalid, unreadable and oversized files. The read limit also
// bounds entries that grow after their directory metadata was collected.
func (d *DiskCache) loadEntry(path string, budget int64) (DiskEntry, bool) {
	key, err := decodeKey(path)
	if err != nil {
		return DiskEntry{}, false
	}

	f, err := d.root.Open(path)
	if err != nil {
		return DiskEntry{}, false
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > budget {
		return DiskEntry{}, false
	}

	value, err := io.ReadAll(io.LimitReader(f, budget+1))

	if err != nil || int64(len(value)) > budget {
		return DiskEntry{}, false
	}

	return DiskEntry{Key: key, Value: value, StoredAt: info.ModTime()}, true
}

// encodeKey maps an arbitrary key to a safe flat filename. PathEscape leaves
// no path separators, so combined with os.Root there are two independent
// layers against traversal.
func encodeKey(key string) string {
	return url.PathEscape(key)
}

func decodeKey(name string) (string, error) {
	key, err := url.PathUnescape(name)
	if err != nil {
		return "", fmt.Errorf("decode cache filename %q: %w", name, err)
	}

	if encodeKey(key) != name {
		return "", fmt.Errorf("cache filename %q is not canonical", name)
	}

	return key, nil
}

// prune bounds persisted values after writes while the disk lock is held.
// Recent temporary files belong to active writers and are never removed here.
func (d *DiskCache) prune() {
	entries, err := fs.ReadDir(d.root.FS(), ".")
	if err != nil {
		return
	}

	type candidate struct {
		name string
		info fs.FileInfo
	}

	var (
		files []candidate
		total int64
	)

	for _, entry := range entries {
		if entry.Name() == diskLockName || !entry.Type().IsRegular() || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}

		if _, err := decodeKey(entry.Name()); err != nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		total += info.Size()
		files = append(files, candidate{entry.Name(), info})
	}

	sort.Slice(files, func(i, j int) bool {
		// Retained page bodies are useless after restart without their catalogue.
		left, right := isCatalogueKey(files[i].name), isCatalogueKey(files[j].name)
		if left != right {
			return !left
		}

		return files[i].info.ModTime().Before(files[j].info.ModTime())
	})

	for _, file := range files {
		if total <= d.maxBytes {
			break
		}

		if err := d.root.Remove(file.name); err == nil {
			total -= file.info.Size()
		}
	}
}

func isCatalogueKey(key string) bool {
	return key == diskIndexKey || key == diskPageListKey
}
