// Package wavencode deterministically wraps Cutly stereo PCM16LE blocks in the same strict
// RIFF/WAVE structural subset as media/wavdecode. Decoder resource limits remain independent.
package wavencode

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("wavencode: context is required")
	ErrCanceled         = errors.New("wavencode: encoding canceled")
	ErrInputInvalid     = errors.New("wavencode: PCM input is invalid")
	ErrInputMismatch    = errors.New("wavencode: PCM input fields do not match")
	ErrResourceLimit    = errors.New("wavencode: resource limit exceeded")
	ErrAllocationFailed = errors.New("wavencode: allocation failed")
	ErrArtifactInvalid  = errors.New("wavencode: artifact is invalid")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.CANCELED"
	CodeInputInvalid     ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.INPUT_INVALID"
	CodeInputMismatch    ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.INPUT_MISMATCH"
	CodeResourceLimit    ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.RESOURCE_LIMIT"
	CodeAllocationFailed ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.ALLOCATION_FAILED"
	CodeArtifactInvalid  ErrorCode = "AUDIO.WAV_PCM_S16LE_V1.ARTIFACT_INVALID"
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

func encodeError(code ErrorCode, path string, err error) error {
	return &Error{Code: code, Path: path, Err: err}
}
