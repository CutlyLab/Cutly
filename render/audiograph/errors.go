package audiograph

import (
	"errors"
	"fmt"
)

var (
	ErrInputInvalid        = errors.New("audiograph: invalid input")
	ErrProgramLookup       = errors.New("audiograph: program lookup failed")
	ErrSourceBinding       = errors.New("audiograph: source binding failed")
	ErrSourceSampleInvalid = errors.New("audiograph: source sample is invalid")
	ErrSourceMismatch      = errors.New("audiograph: source response does not match its bound request")
	ErrSourceProvider      = errors.New("audiograph: source provider failed")
	ErrTimeOverflow        = errors.New("audiograph: exact time overflow")
	ErrLimitExceeded       = errors.New("audiograph: configured limit exceeded")
	ErrCanceled            = errors.New("audiograph: evaluation canceled")
)

// ErrorCode is the stable machine-readable identity of a runtime failure. Compile-time document,
// capability, and media-binding failures use documentv2.Diagnostic with AUDIOGRAPH.V1.* codes.
type ErrorCode string

const (
	CodeInputInvalid        ErrorCode = "AUDIOGRAPH.V1.INPUT_INVALID"
	CodeProgramLookup       ErrorCode = "AUDIOGRAPH.V1.PROGRAM_LOOKUP"
	CodeSourceBinding       ErrorCode = "AUDIOGRAPH.V1.SOURCE_BINDING"
	CodeSourceSampleInvalid ErrorCode = "AUDIOGRAPH.V1.SOURCE_SAMPLE_INVALID"
	CodeSourceMismatch      ErrorCode = "AUDIOGRAPH.V1.SOURCE_MISMATCH"
	CodeSourceProvider      ErrorCode = "AUDIOGRAPH.V1.SOURCE_PROVIDER"
	CodeTimeOverflow        ErrorCode = "AUDIOGRAPH.V1.TIME_OVERFLOW"
	CodeLimitExceeded       ErrorCode = "AUDIOGRAPH.V1.LIMIT_EXCEEDED"
	CodeCanceled            ErrorCode = "AUDIOGRAPH.V1.CANCELED"
)

// Error attaches a stable code and semantic path while retaining errors.Is/errors.As support.
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

func runtimeError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
