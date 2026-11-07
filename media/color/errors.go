// Package color implements narrow, versioned media color-normalization profiles.
//
// The input profile accepts only explicitly described sRGB straight-alpha RGBA8 images and
// converts them into Cutly's linear-light BT.709 premultiplied RGBA16 CPU surface. The independent
// output profile converts that working surface back to a strictly defined straight-alpha RGBA8
// sRGB byte frame. A separate scalar paint profile converts straight sRGB RGBA16 values without
// importing document types. These profiles do not decode media, convert YUV, resize, rotate,
// interpret ICC profiles, or tone-map HDR.
package color

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid    = errors.New("color: context is invalid")
	ErrCanceled          = errors.New("color: normalization canceled")
	ErrMetadataInvalid   = errors.New("color: input metadata does not match the selected profile")
	ErrDimensionsInvalid = errors.New("color: input dimensions are invalid")
	ErrLayoutInvalid     = errors.New("color: input byte layout is invalid")
	ErrResourceLimit     = errors.New("color: resource limit exceeded")
	ErrAllocationFailed  = errors.New("color: allocation failed")
	ErrSurfaceFailed     = errors.New("color: output surface construction failed")
)

type ErrorCode string

const (
	CodeContextInvalid    ErrorCode = "COLOR.RGBA8_SRGB_V1.CONTEXT_INVALID"
	CodeCanceled          ErrorCode = "COLOR.RGBA8_SRGB_V1.CANCELED"
	CodeMetadataInvalid   ErrorCode = "COLOR.RGBA8_SRGB_V1.METADATA_INVALID"
	CodeDimensionsInvalid ErrorCode = "COLOR.RGBA8_SRGB_V1.DIMENSIONS_INVALID"
	CodeLayoutInvalid     ErrorCode = "COLOR.RGBA8_SRGB_V1.LAYOUT_INVALID"
	CodeResourceLimit     ErrorCode = "COLOR.RGBA8_SRGB_V1.RESOURCE_LIMIT"
	CodeAllocationFailed  ErrorCode = "COLOR.RGBA8_SRGB_V1.ALLOCATION_FAILED"
	CodeSurfaceFailed     ErrorCode = "COLOR.RGBA8_SRGB_V1.SURFACE_FAILED"
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

func profileError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
