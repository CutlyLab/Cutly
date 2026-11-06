//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package ffmpegbackend

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// Hard-cut v1 fails closed on platforms where Go cannot pass already-authorized regular-file
// handles to FFmpeg. Path fallback would silently reintroduce TOCTOU and root-escape behavior.
func platformSupportsSecureDescriptors() bool { return false }

func newCommandContext(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.WaitDelay = 500 * time.Millisecond
	return command
}

func attachCommandFile(_ *exec.Cmd, _ *os.File) commandFileEndpoint {
	// New rejects this platform before a Backend can be constructed. Keep the unreachable adapter
	// fail-closed as defense in depth instead of returning the mutable file name.
	return commandFileEndpoint{}
}
