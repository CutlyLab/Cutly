//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package ffmpegbackend

import (
	"context"
	"os"
)

func (b *ChunkAssemblerV2) lockV2(context.Context) (func() error, error) {
	return nil, ErrConfiguration
}
func inodeV2(os.FileInfo) (uint64, uint64, error) { return 0, 0, ErrConfiguration }
