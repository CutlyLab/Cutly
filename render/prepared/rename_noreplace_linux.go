//go:build linux

package prepared

import (
	"syscall"
	"unsafe"
)

const linuxRenameNoReplace = 1

func publicationSupported() bool { return true }

func renameNoReplace(oldPath, newPath string) error {
	oldPointer, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return err
	}
	atFDCWD := ^uintptr(99) // -100
	_, _, errno := syscall.Syscall6(
		linuxRenameat2Trap,
		atFDCWD, uintptr(unsafe.Pointer(oldPointer)), atFDCWD, uintptr(unsafe.Pointer(newPointer)), linuxRenameNoReplace, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
