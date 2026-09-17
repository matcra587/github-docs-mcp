package docs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Concurrent Store of the SAME key must never leave a torn or missing final
// file (CodeRabbit finding 4). Unique temp names + rename make each publish
// atomic; last writer wins with intact content.
func TestDiskConcurrentSameKey(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	dc, err := NewDiskCache(t.TempDir())
	must.NoError(err)

	// Each writer stores a distinct 64-byte value; a torn write would leave a
	// final file matching none of them.
	valid := map[string]bool{}

	var wg sync.WaitGroup

	for i := range 40 {
		v := strings.Repeat(string(rune('A'+i%26)), 64) + strconv.Itoa(i)
		valid[v] = true

		wg.Go(func() {
			_ = dc.Store("en/hooks", []byte(v))
		})
	}

	wg.Wait()

	entries, err := dc.Load()
	must.NoError(err)

	must.Len(entries, 1, "expected exactly 1 entry (temp leak or torn write)")

	is.True(valid[string(entries[0].Value)], "final value is torn: matches no single writer: %q", entries[0].Value)
}

// A temp file left by an interrupted Store must be swept by Load, not
// accumulate (unique temp names would otherwise leak one file per crash).
func TestDiskCacheSweepsLeftoverTemps(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)

	dir := t.TempDir()

	dc, err := NewDiskCache(dir)
	must.NoError(err)

	must.NoError(dc.Store("en/hooks", []byte("real")))

	// Simulate two crashed stores.
	for _, name := range []string{"en%2Fhooks.tmp.99", "en%2Fother.tmp.1"} {
		must.NoError(os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0o600))
	}

	entries, err := dc.Load()
	must.NoError(err)

	must.Len(entries, 1, "expected only the real entry")
	is.Equal("en/hooks", entries[0].Key, "expected only the real entry")

	left, _ := os.ReadDir(dir)
	for _, f := range left {
		is.NotContains(f.Name(), ".tmp.", "temp file not swept: %s", f.Name())
	}
}
