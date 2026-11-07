// Package cpuraster implements Cutly's deterministic CPU crop/affine sampling profile for
// already-normalized linear-light premultiplied RGBA16 surfaces.
package cpuraster

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("cpuraster: context is invalid")
	ErrCanceled         = errors.New("cpuraster: rasterization canceled")
	ErrInputInvalid     = errors.New("cpuraster: raster input is invalid")
	ErrGeometryInvalid  = errors.New("cpuraster: transform or crop geometry is invalid")
	ErrResourceLimit    = errors.New("cpuraster: resource limit exceeded")
	ErrAllocationFailed = errors.New("cpuraster: allocation failed")
	ErrSampleFailed     = errors.New("cpuraster: source sampling failed")
	ErrSurfaceFailed    = errors.New("cpuraster: output surface construction failed")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "RASTER.CPU_AFFINE_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "RASTER.CPU_AFFINE_V1.CANCELED"
	CodeInputInvalid     ErrorCode = "RASTER.CPU_AFFINE_V1.INPUT_INVALID"
	CodeGeometryInvalid  ErrorCode = "RASTER.CPU_AFFINE_V1.GEOMETRY_INVALID"
	CodeResourceLimit    ErrorCode = "RASTER.CPU_AFFINE_V1.RESOURCE_LIMIT"
	CodeAllocationFailed ErrorCode = "RASTER.CPU_AFFINE_V1.ALLOCATION_FAILED"
	CodeSampleFailed     ErrorCode = "RASTER.CPU_AFFINE_V1.SAMPLE_FAILED"
	CodeSurfaceFailed    ErrorCode = "RASTER.CPU_AFFINE_V1.SURFACE_FAILED"
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
