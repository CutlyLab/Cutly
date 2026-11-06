//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type renderStateOwnerHandle struct {
	root *os.File
	lock *os.File
}

func acquireRenderStateOwnerHandle(stateDir string) (*renderStateOwnerHandle, error) {
	rootDescriptor, err := syscall.Open(
		stateDir,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_DIRECTORY,
		0,
	)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			return nil, ErrRenderStateOwnerInvalid
		}
		return nil, err
	}
	root := os.NewFile(uintptr(rootDescriptor), stateDir)
	if root == nil {
		_ = syscall.Close(rootDescriptor)
		return nil, errors.New("cutlyd: create render state directory handle")
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	rootInformation, err := root.Stat()
	if err != nil || !rootInformation.IsDir() {
		return nil, errors.Join(ErrRenderStateOwnerInvalid, err)
	}
	// Lock the directory inode as the lifecycle identity. The companion lock
	// file remains for compatibility and diagnostics, but its pathname may be
	// unlinked on Unix while an older owner still holds that inode. Directory
	// locking keeps a second cooperative owner out even in that case.
	if err := syscall.Flock(rootDescriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrRenderStateAlreadyOwned
		}
		return nil, fmt.Errorf("cutlyd: lock render state directory: %w", err)
	}

	lockPath := filepath.Join(stateDir, renderStateOwnerLockName)
	if information, statErr := os.Lstat(lockPath); statErr == nil {
		if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
			return nil, ErrRenderStateOwnerInvalid
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	lockDescriptor, err := syscall.Open(
		lockPath,
		syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.ENOTDIR) {
			return nil, ErrRenderStateOwnerInvalid
		}
		return nil, err
	}
	lock := os.NewFile(uintptr(lockDescriptor), lockPath)
	if lock == nil {
		_ = syscall.Close(lockDescriptor)
		return nil, errors.New("cutlyd: create render state lock handle")
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	lockInformation, err := lock.Stat()
	if err != nil || !lockInformation.Mode().IsRegular() || lockInformation.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(ErrRenderStateOwnerInvalid, err)
	}
	// The path lookup used to open the lock must still name the directory whose
	// descriptor we retained. This closes the pre-open replacement window; the
	// state directory remains a host-owned lifecycle boundary thereafter.
	currentRoot, err := os.Lstat(stateDir)
	if err != nil || currentRoot.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInformation, currentRoot) {
		return nil, errors.Join(ErrRenderStateOwnerInvalid, err)
	}
	if err := syscall.Flock(lockDescriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrRenderStateAlreadyOwned
		}
		return nil, fmt.Errorf("cutlyd: lock render state: %w", err)
	}
	currentLock, err := os.Lstat(lockPath)
	if err != nil || currentLock.Mode()&os.ModeSymlink != 0 || !currentLock.Mode().IsRegular() ||
		!os.SameFile(lockInformation, currentLock) {
		_ = syscall.Flock(lockDescriptor, syscall.LOCK_UN)
		return nil, errors.Join(ErrRenderStateOwnerInvalid, err)
	}
	closeRoot = false
	closeLock = false
	return &renderStateOwnerHandle{root: root, lock: lock}, nil
}

func (handle *renderStateOwnerHandle) close() error {
	if handle == nil {
		return nil
	}
	var unlockErr error
	if handle.lock != nil {
		unlockErr = syscall.Flock(int(handle.lock.Fd()), syscall.LOCK_UN)
	}
	var lockErr error
	if handle.lock != nil {
		lockErr = handle.lock.Close()
		handle.lock = nil
	}
	var rootErr error
	if handle.root != nil {
		rootErr = errors.Join(syscall.Flock(int(handle.root.Fd()), syscall.LOCK_UN), handle.root.Close())
		handle.root = nil
	}
	return errors.Join(unlockErr, lockErr, rootErr)
}
