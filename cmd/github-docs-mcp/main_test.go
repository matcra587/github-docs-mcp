package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envKeys are all env vars parseConfig reads; tests blank them so each case
// sees pure defaults. t.Setenv also keeps these tests serial, as env-mutating
// tests must be.
var envKeys = []string{
	"MCP_TRANSPORT", "MCP_HTTP_ADDR", "DOCS_BASE_URL", "DOCS_CACHE_DIR",
	"MCP_ALLOWED_ORIGINS", "LOG_LEVEL", "DOCS_INDEX_TTL", "DOCS_PAGE_TTL",
	"DOCS_FETCH_RPS", "DOCS_CACHE_MAX_BYTES",
}

func TestParseConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		cfg, err := parseConfig(nil)
		must.NoError(err)

		is.Equal("stdio", cfg.transport, "unexpected defaults")
		is.Equal("127.0.0.1:8080", cfg.httpAddr, "unexpected defaults")
		is.Equal("https://docs.github.com", cfg.baseURL, "unexpected defaults")

		is.Equal(time.Hour, cfg.indexTTL, "unexpected default knobs")
		is.Equal(24*time.Hour, cfg.pageTTL, "unexpected default knobs")
		is.InDelta(2.0, cfg.fetchRPS, 1e-9, "unexpected default knobs")
		is.Equal(int64(64<<20), cfg.cacheMaxBytes, "unexpected default knobs")
	})

	t.Run("flags override env and defaults", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		t.Setenv("DOCS_INDEX_TTL", "5m")

		cfg, err := parseConfig([]string{
			"-transport", "http",
			"-index-ttl", "2h",
			"-page-ttl", "30m",
			"-fetch-rps", "5",
			"-cache-max-bytes", "1024",
		})
		must.NoError(err)

		is.Equal("http", cfg.transport, "flags not applied")
		is.Equal(2*time.Hour, cfg.indexTTL, "flags not applied")
		is.Equal(30*time.Minute, cfg.pageTTL, "flags not applied")

		is.InDelta(5.0, cfg.fetchRPS, 1e-9, "flags not applied")
		is.Equal(int64(1024), cfg.cacheMaxBytes, "flags not applied")
	})

	t.Run("env supplies defaults", func(t *testing.T) {
		is := assert.New(t)
		must := require.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		t.Setenv("DOCS_PAGE_TTL", "45m")

		cfg, err := parseConfig(nil)
		must.NoError(err)

		is.Equal(45*time.Minute, cfg.pageTTL, "env default not applied")
	})

	t.Run("malformed env rejected", func(t *testing.T) {
		is := assert.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		t.Setenv("DOCS_PAGE_TTL", "1day")

		_, err := parseConfig(nil)
		is.Error(err, "expected error for malformed DOCS_PAGE_TTL")
	})

	t.Run("zero cache cap rejected", func(t *testing.T) {
		is := assert.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		_, err := parseConfig([]string{"-cache-max-bytes", "0"})
		is.Error(err, "expected error for zero cache-max-bytes")
	})

	t.Run("version flag", func(t *testing.T) {
		is := assert.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		_, err := parseConfig([]string{"-version"})
		is.ErrorIs(err, errVersionRequested, "expected errVersionRequested")
	})

	t.Run("invalid transport rejected", func(t *testing.T) {
		is := assert.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		_, err := parseConfig([]string{"-transport", "carrier-pigeon"})
		is.Error(err, "expected error for unknown transport")
	})

	t.Run("non-positive rps rejected", func(t *testing.T) {
		is := assert.New(t)

		for _, key := range envKeys {
			t.Setenv(key, "")
		}

		_, err := parseConfig([]string{"-fetch-rps", "0"})
		is.Error(err, "expected error for zero rps")
	})
}
