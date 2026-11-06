// Package wavsource binds strict immutable WAV resources to Cutly's exact AudioGraph source
// contract and performs deterministic linear varispeed sampling.
package wavsource

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid    = errors.New("wavsource: context is required")
	ErrCanceled          = errors.New("wavsource: operation canceled")
	ErrConfigInvalid     = errors.New("wavsource: configuration is invalid")
	ErrDocumentInvalid   = errors.New("wavsource: document is invalid")
	ErrDocumentMismatch  = errors.New("wavsource: binding document hash does not match")
	ErrBindingInvalid    = errors.New("wavsource: binding is invalid")
	ErrBindingMissing    = errors.New("wavsource: required binding is missing")
	ErrBindingExtra      = errors.New("wavsource: binding is not referenced")
	ErrContentMismatch   = errors.New("wavsource: encoded content hash does not match the document")
	ErrDecodeFailed      = errors.New("wavsource: WAV decode failed")
	ErrDurationMismatch  = errors.New("wavsource: decoded duration does not match the document")
	ErrResourceLimit     = errors.New("wavsource: resource limit exceeded")
	ErrRequestInvalid    = errors.New("wavsource: source request is invalid")
	ErrRequestMismatch   = errors.New("wavsource: source request does not match the frozen scope")
	ErrSourceUnavailable = errors.New("wavsource: requested source is unavailable")
	ErrExactLimit        = errors.New("wavsource: exact sample exceeds the configured bit limit")
)

type ErrorCode string

const (
	CodeContextInvalid    ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.CONTEXT_INVALID"
	CodeCanceled          ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.CANCELED"
	CodeConfigInvalid     ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.CONFIG_INVALID"
	CodeDocumentInvalid   ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.DOCUMENT_INVALID"
	CodeDocumentMismatch  ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.DOCUMENT_MISMATCH"
	CodeBindingInvalid    ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.BINDING_INVALID"
	CodeBindingMissing    ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.BINDING_MISSING"
	CodeBindingExtra      ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.BINDING_EXTRA"
	CodeContentMismatch   ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.CONTENT_MISMATCH"
	CodeDecodeFailed      ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.DECODE_FAILED"
	CodeDurationMismatch  ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.DURATION_MISMATCH"
	CodeResourceLimit     ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.RESOURCE_LIMIT"
	CodeRequestInvalid    ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.REQUEST_INVALID"
	CodeRequestMismatch   ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.REQUEST_MISMATCH"
	CodeSourceUnavailable ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.SOURCE_UNAVAILABLE"
	CodeExactLimit        ErrorCode = "SOURCE.WAV_PCM16_LINEAR_V1.EXACT_LIMIT"
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

func sourceError(code ErrorCode, path string, err error) error {
	return &Error{Code: code, Path: path, Err: err}
}
