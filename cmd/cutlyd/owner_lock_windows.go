//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const (
	ownerErrorSharingViolation syscall.Errno = 32
	ownerErrorLockViolation    syscall.Errno = 33
)

type renderStateOwnerHandle struct {
	root *os.File
	lock *os.File
}

func acquireRenderStateOwnerHandle(stateDir string) (*renderStateOwnerHandle, error) {
	root, rootInformation, err := openRenderStatePath(
		stateDir,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
	)
	if err != nil {
		return nil, err
	}
	if rootInformation.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		rootInformation.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = root.Close()
		return nil, ErrRenderStateOwnerInvalid
	}

	lockPath := filepath.Join(stateDir, renderStateOwnerLockName)
	if information, statErr := os.Lstat(lockPath); statErr == nil {
		if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, ErrRenderStateOwnerInvalid
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = root.Close()
		if errors.Is(statErr, ownerErrorSharingViolation) || errors.Is(statErr, ownerErrorLockViolation) {
			return nil, ErrRenderStateAlreadyOwned
		}
		return nil, statErr
	}
	lock, lockInformation, err := openRenderStatePath(
		lockPath,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		syscall.OPEN_ALWAYS,
		0,
	)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, ownerErrorSharingViolation) || errors.Is(err, ownerErrorLockViolation) {
			return nil, ErrRenderStateAlreadyOwned
		}
		return nil, err
	}
	if lockInformation.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		lockInformation.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = lock.Close()
		_ = root.Close()
		return nil, ErrRenderStateOwnerInvalid
	}
	return &renderStateOwnerHandle{root: root, lock: lock}, nil
}

func openRenderStatePath(path string, access, share, creation, extraFlags uint32) (*os.File, syscall.ByHandleFileInformation, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, syscall.ByHandleFileInformation{}, err
	}
	handle, err := syscall.CreateFile(
		pointer,
		access,
		share,
		nil,
		creation,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT|extraFlags,
		0,
	)
	if err != nil {
		return nil, syscall.ByHandleFileInformation{}, err
	}
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &information); err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, syscall.ByHandleFileInformation{}, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, syscall.ByHandleFileInformation{}, errors.New("cutlyd: create render state owner handle")
	}
	return file, information, nil
}

func (handle *renderStateOwnerHandle) close() error {
	if handle == nil {
		return nil
	}
	var lockErr error
	if handle.lock != nil {
		lockErr = handle.lock.Close()
		handle.lock = nil
	}
	var rootErr error
	if handle.root != nil {
		rootErr = handle.root.Close()
		handle.root = nil
	}
	return errors.Join(lockErr, rootErr)
}
