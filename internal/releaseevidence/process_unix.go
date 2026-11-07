//go:build darwin || linux

package releaseevidence

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
func cleanupProcessTree(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

func openToolForHash(path string) (*os.File, error) {
	// O_NONBLOCK prevents an executable FIFO on PATH (including a path swapped
	// after LookPath) from waiting for a writer before descriptor validation.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
}
