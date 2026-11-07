// Package pngencode serializes Cutly's strict RGBA8 output frames into one deterministic PNG
// structural profile. It owns the zlib/stored-DEFLATE byte contract instead of delegating
// pixel-affecting or byte-affecting choices to a platform image encoder.
package pngencode

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("pngencode: context is required")
	ErrCanceled         = errors.New("pngencode: encoding canceled")
	ErrInputInvalid     = errors.New("pngencode: RGBA8 input is invalid")
	ErrInputMismatch    = errors.New("pngencode: RGBA8 input fields do not match")
	ErrResourceLimit    = errors.New("pngencode: resource limit exceeded")
	ErrAllocationFailed = errors.New("pngencode: allocation failed")
	ErrArtifactInvalid  = errors.New("pngencode: artifact is invalid")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.CANCELED"
	CodeInputInvalid     ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.INPUT_INVALID"
	CodeInputMismatch    ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.INPUT_MISMATCH"
	CodeResourceLimit    ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.RESOURCE_LIMIT"
	CodeAllocationFailed ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.ALLOCATION_FAILED"
	CodeArtifactInvalid  ErrorCode = "VIDEO.PNG_RGBA8_STORED_V1.ARTIFACT_INVALID"
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
