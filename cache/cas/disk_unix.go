//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cas

import "syscall"

func availableBytes(path string) (uint64, error) {
	var value syscall.Statfs_t
	if err := syscall.Statfs(path, &value); err != nil {
		return 0, err
	}
	blockSize := uint64(value.Bsize)
	available := uint64(value.Bavail)
	if blockSize != 0 && available > ^uint64(0)/blockSize {
		return ^uint64(0), nil
	}
	return available * blockSize, nil
}
