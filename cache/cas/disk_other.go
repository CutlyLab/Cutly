//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package cas

import (
	"fmt"
	"syscall"
)

func availableBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("statfs %q: %w", path, syscall.ENOTSUP)
}
