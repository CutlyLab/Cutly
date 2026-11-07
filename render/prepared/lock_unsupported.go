//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package prepared

type fileLock struct{}

func lockingSupported() bool                { return false }
func tryFileLock(string) (*fileLock, error) { return nil, ErrUnsupported }
func (*fileLock) release() error            { return nil }
