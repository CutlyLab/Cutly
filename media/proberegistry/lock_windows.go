//go:build windows

package proberegistry

import (
	"errors"
	"os"
	"syscall"
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

type fileLock struct{ file *os.File }

func lockingSupported() bool { return true }

func tryFileLock(path string) (*fileLock, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		if errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation) {
			return nil, ErrBusy
		}
		return nil, err
	}
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &information); err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = syscall.CloseHandle(handle)
		return nil, ErrCorrupt
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("probe registry: create lock handle")
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
