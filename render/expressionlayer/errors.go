// Package expressionlayer binds inline Document v2 expressions to immutable intrinsic raster
// surfaces. It is deliberately separate from rastercompositor v1, whose external-media contract
// is limited to content-addressed video and image assets.
package expressionlayer

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("expressionlayer: context is required")
	ErrCanceled           = errors.New("expressionlayer: operation canceled")
	ErrConfigInvalid      = errors.New("expressionlayer: configuration is invalid")
	ErrResourceLimit      = errors.New("expressionlayer: resource limit exceeded")
	ErrDocumentInvalid    = errors.New("expressionlayer: document is invalid")
	ErrProfileUnsupported = errors.New("expressionlayer: expression profile is unsupported")
	ErrProviderInvalid    = errors.New("expressionlayer: provider is unavailable")
	ErrRequestInvalid     = errors.New("expressionlayer: request is invalid")
	ErrRequestMismatch    = errors.New("expressionlayer: request does not match the frozen document")
	ErrAssetUnavailable   = errors.New("expressionlayer: rasterized expression asset is unavailable")
	ErrRasterFailed       = errors.New("expressionlayer: expression rasterization failed")
	ErrSurfaceFailed      = errors.New("expressionlayer: immutable surface validation failed")
	ErrHashFailed         = errors.New("expressionlayer: canonical hash failed")
	ErrInvariantFailed    = errors.New("expressionlayer: internal stage invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.CANCELED"
	CodeConfigInvalid      ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.CONFIG_INVALID"
	CodeResourceLimit      ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.RESOURCE_LIMIT"
	CodeDocumentInvalid    ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.DOCUMENT_INVALID"
	CodeProfileUnsupported ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.PROFILE_UNSUPPORTED"
	CodeProviderInvalid    ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.PROVIDER_INVALID"
	CodeRequestInvalid     ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.REQUEST_INVALID"
	CodeRequestMismatch    ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.REQUEST_MISMATCH"
	CodeAssetUnavailable   ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.ASSET_UNAVAILABLE"
	CodeRasterFailed       ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.RASTER_FAILED"
	CodeSurfaceFailed      ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.SURFACE_FAILED"
	CodeHashFailed         ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.HASH_FAILED"
	CodeInvariantFailed    ErrorCode = "SOURCE.EXPRESSION_LAYER_V1.INVARIANT_FAILED"
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

func providerError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
