package docs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskCache(t *testing.T) {
	t.Parallel()

	t.Run("roundtrip with age", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		dc, err := NewDiskCache(t.TempDir())
		must.NoError(err)

		must.NoError(dc.Store("en/alpha", []byte("hello")))

		entries, err := dc.Load()
		must.NoError(err)

		must.Len(entries, 1, "expected 1 entry")

		e := entries[0]
		is.Equal("en/alpha", e.Key, "bad entry")
		is.Equal("hello", string(e.Value), "bad entry")

		is.LessOrEqual(time.Since(e.StoredAt), time.Minute, "age unreasonable")
	})

	t.Run("file permissions are private", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		if runtime.GOOS == "windows" {
			t.Skip("unix permissions")
		}

		dir := t.TempDir()

		dc, err := NewDiskCache(dir)
		must.NoError(err)

		must.NoError(dc.Store("k", []byte("v")))

		files, err := os.ReadDir(dir)
		must.NoError(err)

		for _, f := range files {
			info, err := f.Info()
			must.NoError(err)

			is.Equal(os.FileMode(0o600), info.Mode().Perm(), "file %s perm, expected 600", f.Name())
		}
	})

	t.Run("traversal keys stay under root", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		dir := t.TempDir()
		outside := filepath.Join(dir, "..", "escape-marker")

		dc, err := NewDiskCache(dir)
		must.NoError(err)

		for _, key := range []string{"../escape-marker", "..%2Fescape", "/etc/passwd", "a/../../escape-marker"} {
			_ = dc.Store(key, []byte("x")) // may error; must never escape
		}

		_, err = os.Stat(outside)
		must.Error(err, "cache write escaped its root")

		files, _ := os.ReadDir(dir)
		for _, f := range files {
			is.False(strings.Contains(f.Name(), "..") && strings.Contains(f.Name(), "/"), "suspicious cache filename: %q", f.Name())
		}
	})

	t.Run("unwritable dir fails construction", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		if runtime.GOOS == "windows" || os.Getuid() == 0 {
			t.Skip("permission semantics")
		}

		dir := t.TempDir()
		must.NoError(os.Chmod(dir, 0o500)) //nolint:gosec // directory perms need the execute bit; this removes write access for the test

		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restore private directory perms so TempDir cleanup works

		_, err := NewDiskCache(filepath.Join(dir, "sub"))
		is.Error(err, "expected error for unwritable parent")
	})

	t.Run("corrupt entries are skipped on load", func(t *testing.T) {
		t.Parallel()

		is := assert.New(t)
		must := require.New(t)

		dir := t.TempDir()

		dc, err := NewDiskCache(dir)
		must.NoError(err)

		must.NoError(dc.Store("good", []byte("ok")))

		// A stray file whose name is not valid key encoding.
		must.NoError(os.WriteFile(filepath.Join(dir, "%zz-not-valid"), []byte("junk"), 0o600))

		entries, err := dc.Load()
		must.NoError(err)

		must.Len(entries, 1, "expected only the good entry")
		is.Equal("good", entries[0].Key, "expected only the good entry")
	})
}

func TestServiceDiskPersistence(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	f := newFixtureOrigin(t)
	dir := t.TempDir()

	dc, err := NewDiskCache(dir)
	must.NoError(err)

	client, err := NewClient(f.srv.URL, WithRateLimit(1000))
	must.NoError(err)

	cfg := ServiceConfig{IndexTTL: time.Hour, PageTTL: time.Hour, CacheMaxBytes: 1 << 20, Disk: dc}

	svc1 := NewService(client, f.srv.URL, cfg)
	_, err = svc1.Get(t.Context(), "en/alpha")
	must.NoError(err)

	// "Restart": fresh service, same disk dir, origin dead.
	f.failing.Store(true)

	dc2, err := NewDiskCache(dir)
	must.NoError(err)

	cfg.Disk = dc2

	svc2 := NewService(client, f.srv.URL, cfg)

	p, err := svc2.Get(t.Context(), "en/alpha")
	must.NoError(err, "disk-warmed restart should serve")

	is.Equal("# Alpha\n\ncontent alpha", strings.TrimSpace(string(p.Content)), "wrong content after restart")
}
