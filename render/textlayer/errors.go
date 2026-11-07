// Package textlayer binds frozen Text v2 sources and explicitly authorized font providers to
// immutable intrinsic raster surfaces.
package textlayer

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid     = errors.New("textlayer: context is required")
	ErrCanceled           = errors.New("textlayer: operation canceled")
	ErrConfigInvalid      = errors.New("textlayer: configuration is invalid")
	ErrResourceLimit      = errors.New("textlayer: resource limit exceeded")
	ErrDocumentInvalid    = errors.New("textlayer: document is invalid")
	ErrProfileUnsupported = errors.New("textlayer: text profile is unsupported")
	ErrBindingInvalid     = errors.New("textlayer: font binding is invalid")
	ErrProviderInvalid    = errors.New("textlayer: provider is unavailable")
	ErrRequestInvalid     = errors.New("textlayer: request is invalid")
	ErrRequestMismatch    = errors.New("textlayer: request does not match the frozen document")
	ErrAssetUnavailable   = errors.New("textlayer: rasterized text asset is unavailable")
	ErrAssemblyFailed     = errors.New("textlayer: trusted text assembly failed")
	ErrRasterFailed       = errors.New("textlayer: trusted text rasterization failed")
	ErrSurfaceFailed      = errors.New("textlayer: immutable surface validation failed")
	ErrInvariantFailed    = errors.New("textlayer: internal stage invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid     ErrorCode = "SOURCE.TEXT_LAYER_V1.CONTEXT_INVALID"
	CodeCanceled           ErrorCode = "SOURCE.TEXT_LAYER_V1.CANCELED"
	CodeConfigInvalid      ErrorCode = "SOURCE.TEXT_LAYER_V1.CONFIG_INVALID"
	CodeResourceLimit      ErrorCode = "SOURCE.TEXT_LAYER_V1.RESOURCE_LIMIT"
	CodeDocumentInvalid    ErrorCode = "SOURCE.TEXT_LAYER_V1.DOCUMENT_INVALID"
	CodeProfileUnsupported ErrorCode = "SOURCE.TEXT_LAYER_V1.PROFILE_UNSUPPORTED"
	CodeBindingInvalid     ErrorCode = "SOURCE.TEXT_LAYER_V1.BINDING_INVALID"
	CodeProviderInvalid    ErrorCode = "SOURCE.TEXT_LAYER_V1.PROVIDER_INVALID"
	CodeRequestInvalid     ErrorCode = "SOURCE.TEXT_LAYER_V1.REQUEST_INVALID"
	CodeRequestMismatch    ErrorCode = "SOURCE.TEXT_LAYER_V1.REQUEST_MISMATCH"
	CodeAssetUnavailable   ErrorCode = "SOURCE.TEXT_LAYER_V1.ASSET_UNAVAILABLE"
	CodeAssemblyFailed     ErrorCode = "SOURCE.TEXT_LAYER_V1.ASSEMBLY_FAILED"
	CodeRasterFailed       ErrorCode = "SOURCE.TEXT_LAYER_V1.RASTER_FAILED"
	CodeSurfaceFailed      ErrorCode = "SOURCE.TEXT_LAYER_V1.SURFACE_FAILED"
	CodeInvariantFailed    ErrorCode = "SOURCE.TEXT_LAYER_V1.INVARIANT_FAILED"
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
