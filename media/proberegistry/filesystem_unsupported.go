//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package proberegistry

import (
	"os"
)

func filesystemSupported() bool                    { return false }
func requireDirectory(string) error                { return ErrUnsupported }
func openRegularNoFollow(string) (*os.File, error) { return nil, ErrUnsupported }
func syncDirectory(string) error                   { return ErrUnsupported }
