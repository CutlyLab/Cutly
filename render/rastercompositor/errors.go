// Package rastercompositor assembles Cutly's exact visual evaluator, normalized native-size
// source frames, deterministic CPU affine rasterizer and premultiplied CPU blend kernel.
package rastercompositor

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig       = errors.New("rastercompositor: invalid configuration")
	ErrProgramUnavailable  = errors.New("rastercompositor: compiled program is unavailable")
	ErrEvaluationFailed    = errors.New("rastercompositor: visual evaluation failed")
	ErrProviderUnavailable = errors.New("rastercompositor: native source provider is unavailable")
	ErrSourceFailed        = errors.New("rastercompositor: native source request failed")
	ErrSourceInvalid       = errors.New("rastercompositor: native source response is invalid")
	ErrSourceMismatch      = errors.New("rastercompositor: native source response does not match the request")
	ErrSourceLimit         = errors.New("rastercompositor: native source exceeds resource limits")
	ErrUnsupportedLayer    = errors.New("rastercompositor: evaluator produced an unsupported layer")
	ErrCanceled            = errors.New("rastercompositor: render canceled")
	ErrRasterFailed        = errors.New("rastercompositor: CPU raster stage failed")
	ErrKernelFailed        = errors.New("rastercompositor: CPU blend kernel failed")
	ErrResourceLimit       = errors.New("rastercompositor: resource limit exceeded")
)

type ErrorCode string

const (
	CodeInvalidConfig       ErrorCode = "COMPOSITOR.RASTER_REFERENCE.INVALID_CONFIG"
	CodeProgramUnavailable  ErrorCode = "COMPOSITOR.RASTER_REFERENCE.PROGRAM_UNAVAILABLE"
	CodeEvaluationFailed    ErrorCode = "COMPOSITOR.RASTER_REFERENCE.EVALUATION_FAILED"
	CodeProviderUnavailable ErrorCode = "COMPOSITOR.RASTER_REFERENCE.PROVIDER_UNAVAILABLE"
	CodeSourceFailed        ErrorCode = "COMPOSITOR.RASTER_REFERENCE.SOURCE_FAILED"
	CodeSourceInvalid       ErrorCode = "COMPOSITOR.RASTER_REFERENCE.SOURCE_INVALID"
	CodeSourceMismatch      ErrorCode = "COMPOSITOR.RASTER_REFERENCE.SOURCE_MISMATCH"
	CodeSourceLimit         ErrorCode = "COMPOSITOR.RASTER_REFERENCE.SOURCE_LIMIT"
	CodeUnsupportedLayer    ErrorCode = "COMPOSITOR.RASTER_REFERENCE.UNSUPPORTED_LAYER"
	CodeCanceled            ErrorCode = "COMPOSITOR.RASTER_REFERENCE.CANCELED"
	CodeRasterFailed        ErrorCode = "COMPOSITOR.RASTER_REFERENCE.RASTER_FAILED"
	CodeKernelFailed        ErrorCode = "COMPOSITOR.RASTER_REFERENCE.KERNEL_FAILED"
	CodeResourceLimit       ErrorCode = "COMPOSITOR.RASTER_REFERENCE.RESOURCE_LIMIT"
)

// Error adds a stable code and semantic path while preserving errors.Is/errors.As behavior.
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

func compositorError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
