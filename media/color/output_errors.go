package color

import "errors"

var (
	ErrOutputSurfaceInvalid = errors.New("color: output conversion surface is invalid")
	ErrOutputFrameInvalid   = errors.New("color: RGBA8 output frame is invalid")
	ErrOutputBounds         = errors.New("color: RGBA8 output access is out of bounds")
	ErrOutputWriteFailed    = errors.New("color: RGBA8 output write failed")
)

const (
	CodeOutputContextInvalid   ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.CONTEXT_INVALID"
	CodeOutputCanceled         ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.CANCELED"
	CodeOutputResourceLimit    ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.RESOURCE_LIMIT"
	CodeOutputAllocationFailed ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.ALLOCATION_FAILED"
	CodeOutputSurfaceInvalid   ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.SURFACE_INVALID"
	CodeOutputFrameInvalid     ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.FRAME_INVALID"
	CodeOutputBounds           ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.BOUNDS"
	CodeOutputWriteFailed      ErrorCode = "COLOR.LINEAR_RGBA16_TO_SRGB_RGBA8_V1.WRITE_FAILED"
)
