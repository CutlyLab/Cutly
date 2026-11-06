//go:build unix

package ffprobe

import (
	"fmt"
	"os"
)

func validateInheritedInputPlatform() error {
	return nil
}

func inheritedInputBinding(input *materializedInput) (string, []*os.File, error) {
	if input == nil || input.file == nil || input.closed {
		return "", nil, fmt.Errorf("%w: private snapshot is not open", ErrInput)
	}
	if _, err := input.file.Seek(0, 0); err != nil {
		return "", nil, fmt.Errorf("%w: rewind private snapshot: %v", ErrInput, err)
	}
	// The first ExtraFiles entry is descriptor 3 in the child. FFprobe's seekable fd protocol consumes
	// that exact descriptor, so it never reopens a pathname.
	return "fd:", []*os.File{input.file}, nil
}
