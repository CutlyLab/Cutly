//go:build !unix

package ffprobe

import (
	"fmt"
	"os"
)

func validateInheritedInputPlatform() error {
	return fmt.Errorf("%w: secure inherited input descriptors are unsupported on this platform", ErrExecutable)
}

func inheritedInputBinding(*materializedInput) (string, []*os.File, error) {
	return "", nil, fmt.Errorf("%w: secure inherited input descriptors are unsupported on this platform", ErrExecutable)
}
