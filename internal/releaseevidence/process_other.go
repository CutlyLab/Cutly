//go:build !darwin && !linux

package releaseevidence

import (
	"fmt"
	"os/exec"
)

func configureProcessTree(*exec.Cmd) error {
	return fmt.Errorf("release evidence fails closed: process-tree cancellation supported only on linux/darwin")
}
func cleanupProcessTree(*exec.Cmd) {}
