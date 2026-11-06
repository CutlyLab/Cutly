//go:build !darwin && !linux && !freebsd

package videodecode

import (
	"context"
	"fmt"
	"os"
)

type sourceSnapshot struct{}

func materializeLocalInput(context.Context, string, string, int64, int64) (*sourceSnapshot, error) {
	return nil, fmt.Errorf("%w: descriptor-bound input is Unix-only", ErrUnsupported)
}

func (value *sourceSnapshot) inheritedBinding() (string, []*os.File, error) {
	return "", nil, ErrUnsupported
}

func (value *sourceSnapshot) close() error { return nil }
