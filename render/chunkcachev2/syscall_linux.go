//go:build linux

package chunkcachev2

import (
	"runtime"
	"syscall"
	"unsafe"
)

func openat(dir int, name string, flags int, mode uint32) (int, error) {
	return syscall.Openat(dir, name, flags, mode)
}
func mkdirat(dir int, name string) error  { return syscall.Mkdirat(dir, name, 0700) }
func unlinkat(dir int, name string) error { return syscall.Unlinkat(dir, name) }
func renameExclusive(from int, old string, to int, name string) error {
	var trap uintptr
	switch runtime.GOARCH {
	case "amd64":
		trap = 316
	case "arm64", "riscv64", "loong64":
		trap = 276
	case "386":
		trap = 353
	case "arm":
		trap = 382
	case "ppc64", "ppc64le":
		trap = 357
	case "s390x":
		trap = 347
	case "mips", "mipsle":
		trap = 4351
	case "mips64", "mips64le":
		trap = 5311
	default:
		return ErrUnsupported
	}
	a, e := syscall.BytePtrFromString(old)
	if e != nil {
		return e
	}
	b, e := syscall.BytePtrFromString(name)
	if e != nil {
		return e
	}
	_, _, errno := syscall.Syscall6(trap, uintptr(from), uintptr(unsafe.Pointer(a)), uintptr(to), uintptr(unsafe.Pointer(b)), 1, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
