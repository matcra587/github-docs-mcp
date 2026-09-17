package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultCacheDir(t *testing.T) {
	// Environment mutation requires serial execution.
	t.Setenv("DOCS_CACHE_DIR", "")
	require.Empty(t, defaultCacheDir(), "explicit empty environment opts out")
	t.Setenv("DOCS_CACHE_DIR", t.TempDir())
	require.Equal(t, os.Getenv("DOCS_CACHE_DIR"), defaultCacheDir())

	cfg, err := parseConfig([]string{"-cache-dir", ""})
	require.NoError(t, err)
	require.Empty(t, cfg.cacheDir, "explicit empty flag overrides environment")
	require.NoError(t, os.Unsetenv("DOCS_CACHE_DIR"))

	dir, err := os.UserCacheDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "github-docs-mcp"), defaultCacheDir())

	cfg, err = parseConfig(nil)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "github-docs-mcp"), cfg.cacheDir)

	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CACHE_HOME", "relative-path")
		require.Empty(t, defaultCacheDir(), "unusable platform cache directory stays memory-only")
	}
}

func TestCacheOriginDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.Equal(t, dir, cacheOriginDir(dir, "https://docs.github.com/"))
	first := cacheOriginDir(dir, "http://localhost:9999")
	require.NotEqual(t, dir, first)
	require.Equal(t, first, cacheOriginDir(dir, "http://localhost:9999/"))
	require.NotEqual(t, first, cacheOriginDir(dir, "http://localhost:9998"))
}
