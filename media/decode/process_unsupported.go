//go:build !darwin && !linux && !freebsd

package videodecode

import (
	"fmt"
	"os/exec"
)

func validateProcessPlatform() error {
	return fmt.Errorf("%w: descriptor-bound FFmpeg decoding is Unix-only", ErrUnsupported)
}

func hardenCommand(command *exec.Cmd) {}
