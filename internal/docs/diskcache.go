package docs

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DiskCache persists cache entries as flat files under one directory, scoped
// with os.Root so no key, however hostile, can write outside it. Filenames
// are the URL-escaped key; mtime carries the entry's age across restarts.
// Writes are atomic (temp file + rename), so a torn write can never surface.
type DiskCache struct {
	root   *os.Root
	tmpSeq atomic.Uint64
}

// DiskEntry is one persisted cache entry.
type DiskEntry struct {
	Key      string
	Value    []byte
	StoredAt time.Time
}

// NewDiskCache creates dir (0700) if needed and opens it as the cache root.
func NewDiskCache(dir string) (*DiskCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open cache root: %w", err)
	}

	return &DiskCache{root: root}, nil
}

// Store writes value under key atomically with private permissions.
func (d *DiskCache) Store(key string, value []byte) error {
	name := encodeKey(key)

	// A unique temp name per call keeps concurrent Stores of the same key
	// from clobbering each other's in-flight file; O_EXCL guarantees it.
	// The .tmp suffix is filtered out by Load.
	tmp := name + ".tmp." + strconv.FormatUint(d.tmpSeq.Add(1), 10)

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

	return nil
}

// Load returns all persisted entries; files that are not valid entries (bad
// encoding, leftover temp files) are skipped, never fatal.
func (d *DiskCache) Load() ([]DiskEntry, error) {
	var entries []DiskEntry

	err := fs.WalkDir(d.root.FS(), ".", func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || path == "." {
			return nil //nolint:nilerr // skip unreadable entries, never fatal
		}

		// Temp files are leftovers from a Store interrupted before rename
		// (unique-named, so they would otherwise accumulate). Sweep them.
		if strings.Contains(path, ".tmp.") {
			_ = d.root.Remove(path)
			return nil
		}

		key, kerr := decodeKey(path)
		if kerr != nil {
			return nil //nolint:nilerr // stray file, not ours
		}

		info, ierr := de.Info()
		if ierr != nil {
			return nil //nolint:nilerr // vanished between list and stat
		}

		value, rerr := fs.ReadFile(d.root.FS(), path)
		if rerr != nil {
			return nil //nolint:nilerr // unreadable entry is skipped
		}

		entries = append(entries, DiskEntry{Key: key, Value: value, StoredAt: info.ModTime()})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk cache dir: %w", err)
	}

	return entries, nil
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
