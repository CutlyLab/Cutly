//go:build darwin

package chunkcachev2

import (
	"syscall"
	"unsafe"
)

func openat(dir int, name string, flags int, mode uint32) (int, error) {
	p, e := syscall.BytePtrFromString(name)
	if e != nil {
		return -1, e
	}
	r, _, errno := syscall.Syscall6(463, uintptr(dir), uintptr(unsafe.Pointer(p)), uintptr(flags), uintptr(mode), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}
func mkdirat(dir int, name string) error {
	p, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall(475, uintptr(dir), uintptr(unsafe.Pointer(p)), 0700)
	if errno != 0 {
		return errno
	}
	return nil
}
func unlinkat(dir int, name string) error {
	p, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall(472, uintptr(dir), uintptr(unsafe.Pointer(p)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
func renameExclusive(from int, old string, to int, name string) error {
	a, e := syscall.BytePtrFromString(old)
	if e != nil {
		return e
	}
	b, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall6(488, uintptr(from), uintptr(unsafe.Pointer(a)), uintptr(to), uintptr(unsafe.Pointer(b)), 4, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
