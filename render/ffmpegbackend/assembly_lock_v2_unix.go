//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ffmpegbackend

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// Lock the retained directory inode rather than a removable lock leaf. Separate
// opens supply separate open-file descriptions, including within one process.
func (b *ChunkAssemblerV2) lockV2(ctx context.Context) (func() error, error) {
	f, err := b.io.outputHandle.Open(".")
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	unlock := func() error { return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) }
	if err = b.io.verifyOutputPathIdentity(); err != nil {
		_ = unlock()
		return nil, err
	}
	return unlock, nil
}
func inodeV2(info os.FileInfo) (uint64, uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, ErrPublication
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}
