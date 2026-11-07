//go:build windows

package prepared

import "syscall"

func publicationSupported() bool { return true }

func renameNoReplace(oldPath, newPath string) error {
	oldPointer, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return syscall.MoveFile(oldPointer, newPointer)
}
