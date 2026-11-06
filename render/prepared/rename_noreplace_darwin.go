//go:build darwin

package prepared

import (
	"syscall"
	"unsafe"
)

const (
	darwinRenameatxNP = 488
	darwinRenameExcl  = 0x00000004
)

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
	atFDCWD := ^uintptr(1) // -2
	_, _, errno := syscall.Syscall6(
		darwinRenameatxNP,
		atFDCWD, uintptr(unsafe.Pointer(oldPointer)), atFDCWD, uintptr(unsafe.Pointer(newPointer)), darwinRenameExcl, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
