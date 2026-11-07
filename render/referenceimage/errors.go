// Package referenceimage compiles and renders the narrow, deterministic static-PNG reference
// export pipeline. It is not an authorization, signing, publication, or general media backend.
package referenceimage

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("referenceimage: context is required")
	ErrCanceled           = errors.New("referenceimage: operation canceled")
	ErrConfigInvalid      = errors.New("referenceimage: configuration is invalid")
	ErrResourceLimit      = errors.New("referenceimage: resource limit exceeded")
	ErrDocumentInvalid    = errors.New("referenceimage: document is invalid or unsupported")
	ErrCompositorCompile  = errors.New("referenceimage: raster compositor compile failed")
	ErrSourceCompile      = errors.New("referenceimage: native PNG source compile failed")
	ErrProgramUnavailable = errors.New("referenceimage: compiled program is unavailable")
	ErrRenderFailed       = errors.New("referenceimage: raster render failed")
	ErrColorFailed        = errors.New("referenceimage: output color conversion failed")
	ErrPNGFailed          = errors.New("referenceimage: PNG encoding failed")
	ErrStageMismatch      = errors.New("referenceimage: stage receipt mismatch")
)

type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "REFERENCE_IMAGE_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "REFERENCE_IMAGE_V1.CANCELED"
	CodeConfigInvalid      ErrorCode = "REFERENCE_IMAGE_V1.CONFIG_INVALID"
	CodeResourceLimit      ErrorCode = "REFERENCE_IMAGE_V1.RESOURCE_LIMIT"
	CodeDocumentInvalid    ErrorCode = "REFERENCE_IMAGE_V1.DOCUMENT_INVALID"
	CodeCompositorCompile  ErrorCode = "REFERENCE_IMAGE_V1.COMPOSITOR_COMPILE_FAILED"
	CodeSourceCompile      ErrorCode = "REFERENCE_IMAGE_V1.SOURCE_COMPILE_FAILED"
	CodeProgramUnavailable ErrorCode = "REFERENCE_IMAGE_V1.PROGRAM_UNAVAILABLE"
	CodeRenderFailed       ErrorCode = "REFERENCE_IMAGE_V1.RENDER_FAILED"
	CodeColorFailed        ErrorCode = "REFERENCE_IMAGE_V1.COLOR_FAILED"
	CodePNGFailed          ErrorCode = "REFERENCE_IMAGE_V1.PNG_FAILED"
	CodeStageMismatch      ErrorCode = "REFERENCE_IMAGE_V1.STAGE_MISMATCH"
)

// Error adds stable machine-readable identity and a semantic path while retaining the complete
// dependency error chain for errors.Is/errors.As. Every operation returns a zero result on Error.
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
