//go:build darwin || linux

package worker

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func cleanupDescendants(process *os.Process, grace time.Duration) error {
	if process == nil {
		return nil
	}
	alive, err := processGroupAlive(process.Pid)
	if err != nil || !alive {
		return err
	}
	cleanupErr := errors.Join(ErrDescendants, terminateProcess(process))
	deadline := time.NewTimer(grace)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			alive, probeErr := processGroupAlive(process.Pid)
			if probeErr != nil {
				return errors.Join(cleanupErr, probeErr)
			}
			if !alive {
				return cleanupErr
			}
		case <-deadline.C:
			return errors.Join(cleanupErr, killProcess(process))
		}
	}
}

func processGroupAlive(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func exitSignal(err error) string {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return ""
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}

func descendantIsolation() bool                   { return true }
func processIsolationSupported() bool             { return true }
func executableModeAllowed(info os.FileInfo) bool { return info.Mode()&0o111 != 0 }
