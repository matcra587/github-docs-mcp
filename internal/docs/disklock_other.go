//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package docs

import "os"

func tryDiskLock(_ *os.File) (bool, error) {
	return false, os.ErrInvalid
}
