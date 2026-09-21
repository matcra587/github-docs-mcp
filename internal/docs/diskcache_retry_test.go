package docs

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// retryDiskOperation is for the stress test only: the production cache remains
// best-effort and deliberately returns when its lock deadline expires.
func retryDiskOperation(ctx context.Context, operation func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := operation()
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDiskStressRetriesContendedLock(t *testing.T) {
	t.Parallel()
	d, err := NewDiskCache(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.root.Close() })

	lock, err := d.lock()
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	attempts := 0
	err = retryDiskOperation(ctx, func() error {
		attempts++
		err := d.Store("page", []byte("complete payload"))

		if attempts == 1 {
			require.NoError(t, lock.Close())
		}

		return err
	})
	require.NoError(t, err, "stress operation must survive a transient lock timeout")
	require.GreaterOrEqual(t, attempts, 2)

	entries, err := d.Load()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "complete payload", string(entries[0].Value))
}

func TestDiskStressRetryStopsOnErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	require.ErrorIs(t, retryDiskOperation(t.Context(), func() error { return os.ErrPermission }), os.ErrPermission)
	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	err := retryDiskOperation(ctx, func() error { attempts++; cancel(); return os.ErrDeadlineExceeded })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}
