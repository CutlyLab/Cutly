//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package localv2

import (
	"errors"
	"os"
	"syscall"
)

func supported() bool   { return true }
func noFollowFlag() int { return syscall.O_NOFOLLOW | syscall.O_NONBLOCK }

// Open relative to a confined descriptor and never follow the lock leaf.
// This is deliberately the v1 .cutly/project.lock domain as both profiles use
// project.cutly.json. The inode is never removed or replaced by this package.
func lockFile(root *os.Root) (*os.File, error) {
	file, err := root.OpenFile(".cutly/project.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrInvalid
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	linked, err := root.Lstat(".cutly/project.lock")
	if err != nil || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
		file.Close()
		return nil, ErrInvalid
	}
	return file, nil
}
