// Package textlayout implements Cutly's deterministic shaped-text layout contract. It consumes
// explicit shaped glyphs and glyf outlines; it never opens a font or performs shaping.
package textlayout

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("textlayout: context is required")
	ErrCanceled           = errors.New("textlayout: operation canceled")
	ErrLimitsInvalid      = errors.New("textlayout: limits are invalid")
	ErrProfileUnsupported = errors.New("textlayout: profile is unsupported")
	ErrInputInvalid       = errors.New("textlayout: input is invalid")
	ErrResourceLimit      = errors.New("textlayout: resource limit exceeded")
	ErrExactLimit         = errors.New("textlayout: exact arithmetic limit exceeded")
	ErrInvariantFailed    = errors.New("textlayout: invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "LAYOUT.TEXT_Q26_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "LAYOUT.TEXT_Q26_V1.CANCELED"
	CodeLimitsInvalid      ErrorCode = "LAYOUT.TEXT_Q26_V1.LIMITS_INVALID"
	CodeProfileUnsupported ErrorCode = "LAYOUT.TEXT_Q26_V1.PROFILE_UNSUPPORTED"
	CodeInputInvalid       ErrorCode = "LAYOUT.TEXT_Q26_V1.INPUT_INVALID"
	CodeResourceLimit      ErrorCode = "LAYOUT.TEXT_Q26_V1.RESOURCE_LIMIT"
	CodeExactLimit         ErrorCode = "LAYOUT.TEXT_Q26_V1.EXACT_LIMIT"
	CodeInvariantFailed    ErrorCode = "LAYOUT.TEXT_Q26_V1.INVARIANT_FAILED"
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

func layoutError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
