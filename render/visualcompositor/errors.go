// Package visualcompositor composes native video/image surfaces and viewport-vector expression
// surfaces through one frozen Document v2 evaluator without changing the existing compositor
// profiles.
package visualcompositor

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig                 = errors.New("visualcompositor: invalid configuration")
	ErrProgramUnavailable            = errors.New("visualcompositor: compiled program is unavailable")
	ErrEvaluationFailed              = errors.New("visualcompositor: visual evaluation failed")
	ErrNativeProviderUnavailable     = errors.New("visualcompositor: native source provider is unavailable")
	ErrExpressionProviderUnavailable = errors.New("visualcompositor: expression source provider is unavailable")
	ErrSourceFailed                  = errors.New("visualcompositor: source request failed")
	ErrSourceInvalid                 = errors.New("visualcompositor: source response is invalid")
	ErrSourceMismatch                = errors.New("visualcompositor: source response does not match the frozen request")
	ErrSourceLimit                   = errors.New("visualcompositor: source response exceeds resource limits")
	ErrUnsupportedLayer              = errors.New("visualcompositor: evaluator produced an unsupported layer")
	ErrCanceled                      = errors.New("visualcompositor: operation canceled")
	ErrRasterFailed                  = errors.New("visualcompositor: CPU raster stage failed")
	ErrKernelFailed                  = errors.New("visualcompositor: CPU blend kernel failed")
	ErrResourceLimit                 = errors.New("visualcompositor: resource limit exceeded")
	ErrInvariantFailed               = errors.New("visualcompositor: internal invariant failed")
)

type ErrorCode string

const (
	CodeInvalidConfig                 ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.INVALID_CONFIG"
	CodeProgramUnavailable            ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.PROGRAM_UNAVAILABLE"
	CodeEvaluationFailed              ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.EVALUATION_FAILED"
	CodeNativeProviderUnavailable     ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.NATIVE_PROVIDER_UNAVAILABLE"
	CodeExpressionProviderUnavailable ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.EXPRESSION_PROVIDER_UNAVAILABLE"
	CodeSourceFailed                  ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.SOURCE_FAILED"
	CodeSourceInvalid                 ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.SOURCE_INVALID"
	CodeSourceMismatch                ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.SOURCE_MISMATCH"
	CodeSourceLimit                   ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.SOURCE_LIMIT"
	CodeUnsupportedLayer              ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.UNSUPPORTED_LAYER"
	CodeCanceled                      ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.CANCELED"
	CodeRasterFailed                  ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.RASTER_FAILED"
	CodeKernelFailed                  ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.KERNEL_FAILED"
	CodeResourceLimit                 ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.RESOURCE_LIMIT"
	CodeInvariantFailed               ErrorCode = "COMPOSITOR.VISUAL_REFERENCE_V1.INVARIANT_FAILED"
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

func compositorError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}

func contextError(ctx context.Context, path string) error {
	if ctx == nil {
		return compositorError(CodeInvalidConfig, path, ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return compositorError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}
