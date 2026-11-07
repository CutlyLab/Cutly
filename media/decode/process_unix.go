//go:build darwin || linux || freebsd

package videodecode

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func validateProcessPlatform() error { return nil }

func hardenCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
}
