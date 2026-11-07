//go:build darwin || linux || freebsd

package videodecode

import (
	"fmt"
	"os"
	"syscall"
)

// openRegularNoFollow opens a leaf without following a symlink and without
// ever blocking on a raced FIFO/device. The caller still verifies the complete
// path policy and content identity around this descriptor-bound operation.
func openRegularNoFollow(path string) (_ *os.File, _ os.FileInfo, returnErr error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, nil, fmt.Errorf("videodecode: wrap opened descriptor")
	}
	defer func() {
		if returnErr != nil {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("videodecode: opened descriptor is not a regular file")
	}
	// O_NONBLOCK is required for the open itself. Once fstat proves a regular
	// file, restore ordinary blocking semantics before handing it to FFmpeg.
	if err := syscall.SetNonblock(fd, false); err != nil {
		return nil, nil, err
	}
	return file, info, nil
}

func trustedPathOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())
}
