//go:build !darwin && !linux

package releaseevidence

import (
	"fmt"
	"os"
	"os/exec"
)

func configureProcessTree(*exec.Cmd) error {
	return fmt.Errorf("release evidence fails closed: process-tree cancellation supported only on linux/darwin")
}
func cleanupProcessTree(*exec.Cmd) {}
func openToolForHash(string) (*os.File, error) {
	return nil, fmt.Errorf("release evidence fails closed: nonblocking tool observation supported only on linux/darwin")
}
