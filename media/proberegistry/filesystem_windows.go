//go:build windows

package proberegistry

import (
	"errors"
	"os"
	"syscall"
)

func filesystemSupported() bool { return true }

func requireDirectory(path string) error {
	handle, information, err := openPathNoFollow(path, syscall.GENERIC_READ, syscall.FILE_FLAG_BACKUP_SEMANTICS)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("path is not a non-reparse directory")
	}
	return nil
}

func openRegularNoFollow(path string) (*os.File, error) {
	handle, information, err := openPathNoFollow(path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0)
	if err != nil {
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
		return nil, errors.New("probe registry: create record handle")
	}
	return file, nil
}

func openPathNoFollow(path string, access uint32, extraFlags uint32) (syscall.Handle, syscall.ByHandleFileInformation, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, syscall.ByHandleFileInformation{}, err
	}
	handle, err := syscall.CreateFile(
		pointer, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT|extraFlags, 0,
	)
	if err != nil {
		return 0, syscall.ByHandleFileInformation{}, err
	}
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &information); err != nil {
		_ = syscall.CloseHandle(handle)
		return 0, syscall.ByHandleFileInformation{}, err
	}
	return handle, information, nil
}

// Go exposes no portable directory fsync on Windows. The manifest file is
// flushed before publication; power-loss directory durability is therefore
// weaker than the POSIX profile and documented by the package.
func syncDirectory(string) error { return nil }
