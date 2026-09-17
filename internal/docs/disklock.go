package docs

import (
	"fmt"
	"os"
	"time"
)

const diskLockName = ".cache-lock"

// lock opens a separate handle per operation so goroutines and processes contend
// on the same lock. Never unlink this file: waiters must share the same inode.
// Closing the handle (including on process exit) releases the operating-system lock.
func (d *DiskCache) lock() (*os.File, error) {
	f, err := d.root.OpenFile(diskLockName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open cache lock: %w", err)
	}

	deadline := time.Now().Add(time.Second)

	for {
		locked, err := tryDiskLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock cache: %w", err)
		}

		if locked {
			return f, nil
		}

		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock cache: %w", os.ErrDeadlineExceeded)
		}

		time.Sleep(5 * time.Millisecond)
	}
}
