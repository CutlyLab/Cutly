// Package textassembly closes the trust boundary between a frozen Text v2 source,
// the pinned static-glyf shape provider, deterministic layout, and text rasterization.
package textassembly

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid  = errors.New("textassembly: context is required")
	ErrCanceled        = errors.New("textassembly: operation canceled")
	ErrLimitsInvalid   = errors.New("textassembly: limits are invalid")
	ErrInputInvalid    = errors.New("textassembly: input is invalid")
	ErrBinding         = errors.New("textassembly: trusted lineage binding failed")
	ErrResourceLimit   = errors.New("textassembly: resource limit exceeded")
	ErrInvariantFailed = errors.New("textassembly: invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid  ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.CONTEXT_INVALID"
	CodeCanceled        ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.CANCELED"
	CodeLimitsInvalid   ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.LIMITS_INVALID"
	CodeInputInvalid    ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.INPUT_INVALID"
	CodeBinding         ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.BINDING_FAILED"
	CodeResourceLimit   ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.RESOURCE_LIMIT"
	CodeInvariantFailed ErrorCode = "RENDER.TEXT_ASSEMBLY_V1.INVARIANT_FAILED"
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

func assemblyError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
