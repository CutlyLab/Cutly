//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package prepared

import "os"

func openRegularNoFollow(string) (*os.File, error) { return nil, ErrUnsupported }
