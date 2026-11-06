// Package pcm16le implements Cutly's deterministic final conversion from exact AudioGraph
// stereo samples to signed 16-bit little-endian interleaved PCM.
package pcm16le

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("pcm16le: context is required")
	ErrCanceled         = errors.New("pcm16le: quantization canceled")
	ErrInputInvalid     = errors.New("pcm16le: input is invalid")
	ErrResourceLimit    = errors.New("pcm16le: resource limit exceeded")
	ErrAllocationFailed = errors.New("pcm16le: allocation failed")
	ErrBounds           = errors.New("pcm16le: sample index is out of bounds")
	ErrOutputInvalid    = errors.New("pcm16le: output block is invalid")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "AUDIO.PCM_S16LE_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "AUDIO.PCM_S16LE_V1.CANCELED"
	CodeInputInvalid     ErrorCode = "AUDIO.PCM_S16LE_V1.INPUT_INVALID"
	CodeResourceLimit    ErrorCode = "AUDIO.PCM_S16LE_V1.RESOURCE_LIMIT"
	CodeAllocationFailed ErrorCode = "AUDIO.PCM_S16LE_V1.ALLOCATION_FAILED"
	CodeBounds           ErrorCode = "AUDIO.PCM_S16LE_V1.BOUNDS"
	CodeOutputInvalid    ErrorCode = "AUDIO.PCM_S16LE_V1.OUTPUT_INVALID"
)

// Error carries a stable machine code and semantic path while retaining errors.Is behavior.
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

func quantizeError(code ErrorCode, path string, err error) error {
	return &Error{Code: code, Path: path, Err: err}
}
