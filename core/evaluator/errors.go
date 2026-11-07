// Package evaluator implements Cutly Document v2's pure, backend-neutral evaluation rules.
//
// It deliberately stops before decoding, rasterization, audio DSP, and compositing. Callers first
// compile a capability-checked immutable Document v2 snapshot, then use the exact values produced
// here for preview and export alike.
package evaluator

import (
	"errors"
	"fmt"
)

var (
	ErrInputInvalid     = errors.New("evaluator: invalid input")
	ErrTimeOverflow     = errors.New("evaluator: exact time does not fit RationalTime")
	ErrKeyframeInvalid  = errors.New("evaluator: invalid keyframe lane")
	ErrValueNotWireSafe = errors.New("evaluator: exact value does not fit Document v2 Rational")
	ErrProgramLookup    = errors.New("evaluator: compiled program lookup failed")
)

// ErrorCode is stable machine-readable failure identity for runtime evaluation. Schema and
// capability failures remain documentv2.Diagnostics and are returned by Compile.
type ErrorCode string

const (
	CodeInputInvalid     ErrorCode = "EVALUATOR.V2.INPUT_INVALID"
	CodeTimeOverflow     ErrorCode = "EVALUATOR.V2.TIME_OVERFLOW"
	CodeKeyframeInvalid  ErrorCode = "EVALUATOR.V2.KEYFRAME_INVALID"
	CodeValueNotWireSafe ErrorCode = "EVALUATOR.V2.VALUE_NOT_WIRE_SAFE"
	CodeProgramLookup    ErrorCode = "EVALUATOR.V2.PROGRAM_LOOKUP"
	CodeCompileInvalid   ErrorCode = "EVALUATOR.V2.COMPILE_INVALID"
)

// Error adds a stable code and semantic path without discarding errors.Is support.
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

func evaluationError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
