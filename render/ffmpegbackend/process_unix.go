//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ffmpegbackend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func platformSupportsSecureDescriptors() bool { return true }

// newCommandContext puts FFmpeg in its own process group. Killing only the direct process can
// leave helper children holding stderr open, which violates the render cancellation deadline.
func newCommandContext(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 500 * time.Millisecond
	return command
}

func attachCommandFile(command *exec.Cmd, file *os.File) commandFileEndpoint {
	descriptor := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, file)
	return commandFileEndpoint{URL: "fd:", Descriptor: fmt.Sprintf("%d", descriptor)}
}
