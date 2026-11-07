// Package pngdecode implements a deliberately narrow, deterministic PNG ingest profile.
//
// The first profile accepts only non-interlaced, 8-bit truecolor-with-alpha PNG files that
// explicitly carry one sRGB chunk and no other ancillary chunks. Successful pixels are passed to
// media/color's versioned RGBA8 normalizer; this package does not invoke host color management.
package pngdecode

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid    = errors.New("pngdecode: context is invalid")
	ErrCanceled          = errors.New("pngdecode: decode canceled")
	ErrInputInvalid      = errors.New("pngdecode: input is not a supported strict PNG")
	ErrChunkInvalid      = errors.New("pngdecode: PNG chunk sequence is invalid")
	ErrProfileMismatch   = errors.New("pngdecode: PNG does not match the selected profile")
	ErrResourceLimit     = errors.New("pngdecode: resource limit exceeded")
	ErrDecodeFailed      = errors.New("pngdecode: lossless pixel decode failed")
	ErrAllocationFailed  = errors.New("pngdecode: decode buffer allocation failed")
	ErrNormalizeFailed   = errors.New("pngdecode: color normalization failed")
	ErrSurfaceHashFailed = errors.New("pngdecode: surface hash failed")
)

type ErrorCode string

const (
	CodeContextInvalid    ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.CONTEXT_INVALID"
	CodeCanceled          ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.CANCELED"
	CodeInputInvalid      ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.INPUT_INVALID"
	CodeChunkInvalid      ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.CHUNK_INVALID"
	CodeProfileMismatch   ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.PROFILE_MISMATCH"
	CodeResourceLimit     ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.RESOURCE_LIMIT"
	CodeDecodeFailed      ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.DECODE_FAILED"
	CodeAllocationFailed  ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.ALLOCATION_FAILED"
	CodeNormalizeFailed   ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.NORMALIZE_FAILED"
	CodeSurfaceHashFailed ErrorCode = "MEDIA.PNG_RGBA8_SRGB_V1.SURFACE_HASH_FAILED"
)

// Error provides a stable code and semantic path while retaining errors.Is/errors.As behavior.
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

func decodeError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
