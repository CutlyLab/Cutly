//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package proberegistry

type fileLock struct{}

func lockingSupported() bool                { return false }
func tryFileLock(string) (*fileLock, error) { return nil, ErrUnsupported }
func (*fileLock) release() error            { return nil }
