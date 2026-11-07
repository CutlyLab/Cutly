//go:build !darwin && !linux

package worker

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func configureProcess(*exec.Cmd) {}

func terminateProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killProcess(process *os.Process) error               { return terminateProcess(process) }
func cleanupDescendants(*os.Process, time.Duration) error { return nil }
func exitSignal(error) string                             { return "" }
func descendantIsolation() bool                           { return false }
func processIsolationSupported() bool {
	switch runtime.GOOS {
	case "aix", "dragonfly", "freebsd", "netbsd", "openbsd", "solaris", "windows":
		return true
	default:
		return false
	}
}
func executableModeAllowed(info os.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}
