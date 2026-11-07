// Package cpucompositor implements the deterministic pixel kernel used by Cutly's CPU
// reference compositor.
//
// This package starts after media/color normalization and stops before output color conversion,
// encoding, or muxing. Every Surface is top-down, tightly packed, linear-light, and
// premultiplied-alpha RGBA16. Keeping those boundaries explicit prevents a platform image API or
// codec default from silently becoming part of the rendering contract.
package cpucompositor

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidDimensions = errors.New("cpucompositor: invalid surface dimensions")
	ErrResourceLimit     = errors.New("cpucompositor: surface exceeds resource limits")
	ErrAllocationFailed  = errors.New("cpucompositor: surface allocation failed")
	ErrInvalidPixel      = errors.New("cpucompositor: pixel is not premultiplied RGBA16")
	ErrBounds            = errors.New("cpucompositor: pixel coordinate is out of bounds")
	ErrInvalidSurface    = errors.New("cpucompositor: invalid surface")
	ErrSurfaceMismatch   = errors.New("cpucompositor: surface dimensions do not match")
	ErrInvalidWeight     = errors.New("cpucompositor: invalid exact weight")
	ErrNoLayers          = errors.New("cpucompositor: no layers to composite")
)

// ErrorCode is a stable machine-readable identity. Pixel/color conversion stages use their own
// diagnostics; these codes are deliberately limited to the pure surface kernel.
type ErrorCode string

const (
	CodeInvalidDimensions ErrorCode = "COMPOSITOR.CPU.INVALID_DIMENSIONS"
	CodeResourceLimit     ErrorCode = "COMPOSITOR.CPU.RESOURCE_LIMIT"
	CodeAllocationFailed  ErrorCode = "COMPOSITOR.CPU.ALLOCATION_FAILED"
	CodeInvalidPixel      ErrorCode = "COMPOSITOR.CPU.INVALID_PIXEL"
	CodeBounds            ErrorCode = "COMPOSITOR.CPU.BOUNDS"
	CodeInvalidSurface    ErrorCode = "COMPOSITOR.CPU.INVALID_SURFACE"
	CodeSurfaceMismatch   ErrorCode = "COMPOSITOR.CPU.SURFACE_MISMATCH"
	CodeInvalidWeight     ErrorCode = "COMPOSITOR.CPU.INVALID_WEIGHT"
	CodeNoLayers          ErrorCode = "COMPOSITOR.CPU.NO_LAYERS"
)

// Error retains errors.Is support while adding a stable code and semantic path.
type Error struct {
	Code ErrorCode
	Path string
	Err  error
}

func (value *Error) Error() string {
	if value == nil {
		return "<nil>"
	}
	if value.Path == "" {
		return fmt.Sprintf("%s: %v", value.Code, value.Err)
	}
	return fmt.Sprintf("%s at %s: %v", value.Code, value.Path, value.Err)
}

func (value *Error) Unwrap() error {
	if value == nil {
		return nil
	}
	return value.Err
}

func kernelError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
