package referencevisual

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid      = errors.New("referencevisual: context is required")
	ErrCanceled            = errors.New("referencevisual: operation canceled")
	ErrConfigInvalid       = errors.New("referencevisual: configuration is invalid")
	ErrResourceLimit       = errors.New("referencevisual: resource limit exceeded")
	ErrDocumentInvalid     = errors.New("referencevisual: document is invalid or unsupported")
	ErrVisualCompile       = errors.New("referencevisual: visual compositor compile failed")
	ErrNativeSourceCompile = errors.New("referencevisual: native PNG source compile failed")
	ErrExpressionCompile   = errors.New("referencevisual: expression source compile failed")
	ErrTextSourceCompile   = errors.New("referencevisual: text source compile failed")
	ErrProgramUnavailable  = errors.New("referencevisual: compiled program is unavailable")
	ErrRenderFailed        = errors.New("referencevisual: visual render failed")
	ErrColorFailed         = errors.New("referencevisual: output color conversion failed")
	ErrPNGFailed           = errors.New("referencevisual: PNG encoding failed")
	ErrStageMismatch       = errors.New("referencevisual: stage receipt mismatch")
)

type ErrorCode string

const (
	CodeContextInvalid      ErrorCode = "REFERENCE_VISUAL_V1.CONTEXT_INVALID"
	CodeCanceled            ErrorCode = "REFERENCE_VISUAL_V1.CANCELED"
	CodeConfigInvalid       ErrorCode = "REFERENCE_VISUAL_V1.CONFIG_INVALID"
	CodeResourceLimit       ErrorCode = "REFERENCE_VISUAL_V1.RESOURCE_LIMIT"
	CodeDocumentInvalid     ErrorCode = "REFERENCE_VISUAL_V1.DOCUMENT_INVALID"
	CodeVisualCompile       ErrorCode = "REFERENCE_VISUAL_V1.VISUAL_COMPILE_FAILED"
	CodeNativeSourceCompile ErrorCode = "REFERENCE_VISUAL_V1.NATIVE_SOURCE_COMPILE_FAILED"
	CodeExpressionCompile   ErrorCode = "REFERENCE_VISUAL_V1.EXPRESSION_COMPILE_FAILED"
	CodeTextSourceCompile   ErrorCode = "REFERENCE_VISUAL_V2.TEXT_SOURCE_COMPILE_FAILED"
	CodeProgramUnavailable  ErrorCode = "REFERENCE_VISUAL_V1.PROGRAM_UNAVAILABLE"
	CodeRenderFailed        ErrorCode = "REFERENCE_VISUAL_V1.RENDER_FAILED"
	CodeColorFailed         ErrorCode = "REFERENCE_VISUAL_V1.COLOR_FAILED"
	CodePNGFailed           ErrorCode = "REFERENCE_VISUAL_V1.PNG_FAILED"
	CodeStageMismatch       ErrorCode = "REFERENCE_VISUAL_V1.STAGE_MISMATCH"
)

// Error supplies a stable code and semantic path while retaining the dependency error chain.
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

func pipelineError(code ErrorCode, path string, err error) error {
	return &Error{Code: code, Path: path, Err: err}
}
