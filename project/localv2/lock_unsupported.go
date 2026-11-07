//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package localv2

import "os"

// Fail closed rather than substitute a process-local or nonexclusive lock.
func supported() bool                          { return false }
func noFollowFlag() int                        { return 0 }
func lockFile(root *os.Root) (*os.File, error) { return nil, ErrUnsupported }
