// Package referencecompositor assembles Cutly's exact visual evaluator and deterministic CPU
// blend kernel around a caller-supplied normalized-surface source. It deliberately does not
// decode media, normalize color, resize, transform, crop, or rasterize text/vector content.
package referencecompositor

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig       = errors.New("referencecompositor: invalid configuration")
	ErrProgramUnavailable  = errors.New("referencecompositor: compiled program is unavailable")
	ErrEvaluationFailed    = errors.New("referencecompositor: visual evaluation failed")
	ErrProviderUnavailable = errors.New("referencecompositor: source provider is unavailable")
	ErrSourceFailed        = errors.New("referencecompositor: normalized source surface failed")
	ErrSurfaceMismatch     = errors.New("referencecompositor: normalized source surface does not match target")
	ErrUnsupportedLayer    = errors.New("referencecompositor: evaluator produced an unsupported layer")
	ErrCanceled            = errors.New("referencecompositor: render canceled")
	ErrKernelFailed        = errors.New("referencecompositor: CPU kernel failed")
)

type ErrorCode string

const (
	CodeInvalidConfig       ErrorCode = "COMPOSITOR.REFERENCE.INVALID_CONFIG"
	CodeProgramUnavailable  ErrorCode = "COMPOSITOR.REFERENCE.PROGRAM_UNAVAILABLE"
	CodeEvaluationFailed    ErrorCode = "COMPOSITOR.REFERENCE.EVALUATION_FAILED"
	CodeProviderUnavailable ErrorCode = "COMPOSITOR.REFERENCE.PROVIDER_UNAVAILABLE"
	CodeSourceFailed        ErrorCode = "COMPOSITOR.REFERENCE.SOURCE_FAILED"
	CodeSurfaceMismatch     ErrorCode = "COMPOSITOR.REFERENCE.SURFACE_MISMATCH"
	CodeUnsupportedLayer    ErrorCode = "COMPOSITOR.REFERENCE.UNSUPPORTED_LAYER"
	CodeCanceled            ErrorCode = "COMPOSITOR.REFERENCE.CANCELED"
	CodeKernelFailed        ErrorCode = "COMPOSITOR.REFERENCE.KERNEL_FAILED"
)

// Error preserves errors.Is/error unwrapping while adding stable machine-readable identity and
// a semantic path. No render method publishes output before returning, so every error is a
// fail-before-publish result.
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

func compositorError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
