//go:build linux

package prepared

import (
	"syscall"
	"unsafe"
)

func storageSupportedV2() bool { return true }
func openatV2(dir int, name string, flags int, mode uint32) (int, error) {
	return syscall.Openat(dir, name, flags, mode)
}
func mkdiratV2(dir int, name string) error { return syscall.Mkdirat(dir, name, 0700) }
func unlinkatV2(dir int, name string, directory bool) error {
	p, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	flags := uintptr(0)
	if directory {
		flags = 0x200
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dir), uintptr(unsafe.Pointer(p)), flags)
	if errno != 0 {
		return errno
	}
	return nil
}
func renameatV2(from int, old string, to int, name string) error {
	a, e := syscall.BytePtrFromString(old)
	if e != nil {
		return e
	}
	b, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall6(linuxRenameat2Trap, uintptr(from), uintptr(unsafe.Pointer(a)), uintptr(to), uintptr(unsafe.Pointer(b)), linuxRenameNoReplace, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
