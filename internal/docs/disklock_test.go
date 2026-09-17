package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiskCacheRejectsLockKey(t *testing.T) {
	t.Parallel()
	d, err := NewDiskCache(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.root.Close() })
	require.ErrorIs(t, d.Store(diskLockName, []byte("replacement")), os.ErrInvalid)
	require.NoError(t, d.Store("page", []byte("value")))
	entries, err := d.Load()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "page", entries[0].Key)
}
