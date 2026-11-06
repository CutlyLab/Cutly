//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package prepared

import (
	"errors"
	"os"
	"syscall"
)

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrCorrupt
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("prepared: create record handle")
	}
	return file, nil
}
