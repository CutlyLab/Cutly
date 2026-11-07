//go:build darwin || linux || freebsd

package videodecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSecureOpenRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(realTempDir(t), "input.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		file, _, err := openRegularNoFollow(path)
		if file != nil {
			_ = file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("FIFO was accepted as a regular input")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a FIFO blocked despite O_NONBLOCK")
	}
}

func TestAdapterRejectsGroupWritableExecutable(t *testing.T) {
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{})
	if err := os.Chmod(executable, 0o720); err != nil {
		t.Fatal(err)
	}
	_, err := New(context.Background(), executable, DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), Options{})
	if !errors.Is(err, ErrExecutable) {
		t.Fatalf("group-writable executable error = %v", err)
	}
}
