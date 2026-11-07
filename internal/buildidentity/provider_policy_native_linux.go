//go:build linux

package buildidentity

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

const providerMeasurementSupported = true
const providerExecutablePermissionBits = true

// O_PATH selects an inode without opening a device/FIFO for reading. Linux's
// /proc/self/fd reopening of this held regular-file descriptor binds the readable
// descriptor to that inode, not to a second resolution of the caller's path.
// Missing/unavailable procfs fails closed. Each component uses openat with
// O_NOFOLLOW, so even transient ancestor symlink substitutions are rejected.
func openProviderExecutable(path string) (*os.File, error) {
	const pathOnly = 0x200000 // Linux O_PATH, identical on supported Linux arches.
	fd, err := syscall.Open("/", pathOnly|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		flags := pathOnly | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		if index != len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		closeErr := syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		if closeErr != nil {
			_ = syscall.Close(next)
			return nil, closeErr
		}
		fd = next
	}
	metadata := os.NewFile(uintptr(fd), path)
	info, err := metadata.Stat()
	if err == nil {
		err = validateProviderExecutableInfo(info)
	}
	if err != nil {
		_ = metadata.Close()
		return nil, err
	}
	file, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", metadata.Fd()), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	closeErr := metadata.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		_ = file.Close()
		return nil, closeErr
	}
	return file, nil
}
