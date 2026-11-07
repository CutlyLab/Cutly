// Package wavdecode implements a deliberately narrow, deterministic RIFF/WAVE PCM ingest
// profile. It owns the container parser and sample decoding instead of delegating either step to
// a platform media stack.
package wavdecode

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("wavdecode: context is invalid")
	ErrCanceled         = errors.New("wavdecode: decode canceled")
	ErrInputInvalid     = errors.New("wavdecode: input is not a supported strict WAVE file")
	ErrChunkInvalid     = errors.New("wavdecode: RIFF chunk envelope is invalid")
	ErrProfileMismatch  = errors.New("wavdecode: WAVE file does not match the selected profile")
	ErrResourceLimit    = errors.New("wavdecode: resource limit exceeded")
	ErrAllocationFailed = errors.New("wavdecode: sample buffer allocation failed")
	ErrBounds           = errors.New("wavdecode: sample coordinates are out of bounds")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.CANCELED"
	CodeInputInvalid     ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.INPUT_INVALID"
	CodeChunkInvalid     ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.CHUNK_INVALID"
	CodeProfileMismatch  ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.PROFILE_MISMATCH"
	CodeResourceLimit    ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.RESOURCE_LIMIT"
	CodeAllocationFailed ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.ALLOCATION_FAILED"
	CodeBounds           ErrorCode = "MEDIA.WAV_PCM_S16LE_V1.BOUNDS"
)

// Error provides a stable machine-readable code and semantic path while preserving
// errors.Is/errors.As behavior.
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
