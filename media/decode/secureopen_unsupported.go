//go:build !darwin && !linux && !freebsd

package videodecode

import (
	"fmt"
	"os"
)

func openRegularNoFollow(string) (*os.File, os.FileInfo, error) {
	return nil, nil, fmt.Errorf("%w: race-safe descriptor opening is unavailable", ErrUnsupported)
}

func trustedPathOwner(os.FileInfo) bool { return false }
