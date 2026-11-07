// Package textraster implements Cutly's deterministic static-glyf text raster profile.
package textraster

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("textraster: context is required")
	ErrCanceled           = errors.New("textraster: operation canceled")
	ErrLimitsInvalid      = errors.New("textraster: limits are invalid")
	ErrInputInvalid       = errors.New("textraster: input is invalid")
	ErrBindingInvalid     = errors.New("textraster: layout source binding is invalid")
	ErrProfileUnsupported = errors.New("textraster: profile is unsupported")
	ErrResourceLimit      = errors.New("textraster: resource limit exceeded")
	ErrExactLimit         = errors.New("textraster: exact arithmetic limit exceeded")
	ErrAllocationFailed   = errors.New("textraster: allocation failed")
	ErrSurfaceFailed      = errors.New("textraster: output surface construction failed")
	ErrInvariantFailed    = errors.New("textraster: invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.CANCELED"
	CodeLimitsInvalid      ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.LIMITS_INVALID"
	CodeInputInvalid       ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.INPUT_INVALID"
	CodeBindingInvalid     ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.BINDING_INVALID"
	CodeProfileUnsupported ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.PROFILE_UNSUPPORTED"
	CodeResourceLimit      ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.RESOURCE_LIMIT"
	CodeExactLimit         ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.EXACT_LIMIT"
	CodeAllocationFailed   ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.ALLOCATION_FAILED"
	CodeSurfaceFailed      ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.SURFACE_FAILED"
	CodeInvariantFailed    ErrorCode = "RASTER.TEXT_GLYF_SS8_V1.INVARIANT_FAILED"
)

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
