//go:build darwin || linux

package worker

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func checkLaunchDescriptorV2(file *os.File) error {
	if file == nil {
		return ErrRunner
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() <= 0 || info.Size() > MaxLaunchConfigBytesV2 {
		return ErrRunner
	}
	// A child must not leak host policy to later probe/decoder execs.
	syscall.CloseOnExec(int(file.Fd()))
	return nil
}

// WatchParentV2 cancels and kills this isolated worker process group when its
// host-owned fd4 liveness writer disappears. A parent SIGKILL cannot run cleanup.
// A detached worker fails closed rather than pretending its descendants are owned.
func WatchParentV2(ctx context.Context, cancel context.CancelFunc, file *os.File) (func(), error) {
	if ctx == nil || cancel == nil || file == nil {
		return nil, ErrRunner
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, ErrRunner
	}
	group, err := syscall.Getpgid(0)
	if err != nil || group != os.Getpid() {
		return nil, ErrRunner
	}
	syscall.CloseOnExec(int(file.Fd()))
	stopped := make(chan struct{})
	var once sync.Once
	var disconnected atomic.Bool
	stop := func() {
		once.Do(func() {
			close(stopped)
			_ = file.Close()
			if disconnected.Load() {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			}
		})
	}
	go func() {
		var b [1]byte
		_, _ = file.Read(b[:])
		select {
		case <-stopped:
			return
		default:
		}
		disconnected.Store(true)
		cancel()
		timer := time.NewTimer(MaxTerminationGraceV2)
		defer timer.Stop()
		select {
		case <-stopped:
		case <-timer.C:
		}
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}()
	return stop, nil
}

func openPrivateRegularV2(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(ErrRunner, err, f.Close())
	}
	return f, nil
}
