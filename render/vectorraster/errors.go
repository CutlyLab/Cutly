// Package vectorraster implements Cutly's deterministic reference rasterizer for
// viewport-bound v2 vector paths.
package vectorraster

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("vectorraster: context is required")
	ErrCanceled           = errors.New("vectorraster: operation canceled")
	ErrLimitsInvalid      = errors.New("vectorraster: limits are invalid")
	ErrSourceInvalid      = errors.New("vectorraster: vector source is invalid")
	ErrProfileUnsupported = errors.New("vectorraster: vector source profile is unsupported")
	ErrResourceLimit      = errors.New("vectorraster: resource limit exceeded")
	ErrExactLimit         = errors.New("vectorraster: exact arithmetic limit exceeded")
	ErrAllocationFailed   = errors.New("vectorraster: allocation failed")
	ErrSurfaceFailed      = errors.New("vectorraster: output surface construction failed")
)

// ErrorCode is the stable machine-readable error identity for this raster stage.
type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "RASTER.VECTOR_SS8_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "RASTER.VECTOR_SS8_V1.CANCELED"
	CodeLimitsInvalid      ErrorCode = "RASTER.VECTOR_SS8_V1.LIMITS_INVALID"
	CodeSourceInvalid      ErrorCode = "RASTER.VECTOR_SS8_V1.SOURCE_INVALID"
	CodeProfileUnsupported ErrorCode = "RASTER.VECTOR_SS8_V1.PROFILE_UNSUPPORTED"
	CodeResourceLimit      ErrorCode = "RASTER.VECTOR_SS8_V1.RESOURCE_LIMIT"
	CodeExactLimit         ErrorCode = "RASTER.VECTOR_SS8_V1.EXACT_LIMIT"
	CodeAllocationFailed   ErrorCode = "RASTER.VECTOR_SS8_V1.ALLOCATION_FAILED"
	CodeSurfaceFailed      ErrorCode = "RASTER.VECTOR_SS8_V1.SURFACE_FAILED"
)

// Error preserves errors.Is while attaching a stable code and semantic path.
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

func rasterError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
