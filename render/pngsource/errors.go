// Package pngsource binds content-addressed Document v2 image assets to strict, normalized PNG
// surfaces and implements referencecompositor.SourceProvider.
//
// It is deliberately a static-image adapter. It does not fetch URIs, decode video, resize,
// transform, crop, or infer color metadata.
package pngsource

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid    = errors.New("pngsource: context is invalid")
	ErrCanceled          = errors.New("pngsource: operation canceled")
	ErrConfigInvalid     = errors.New("pngsource: configuration is invalid")
	ErrDocumentInvalid   = errors.New("pngsource: document is invalid or unsupported")
	ErrBindingInvalid    = errors.New("pngsource: asset binding is invalid")
	ErrBindingMissing    = errors.New("pngsource: referenced image binding is missing")
	ErrBindingExtra      = errors.New("pngsource: binding is not referenced by the document")
	ErrContentMismatch   = errors.New("pngsource: encoded content hash does not match the document")
	ErrDecodeFailed      = errors.New("pngsource: strict PNG decode failed")
	ErrDimensionMismatch = errors.New("pngsource: image dimensions do not match the frozen target")
	ErrResourceLimit     = errors.New("pngsource: resource limit exceeded")
	ErrProviderInvalid   = errors.New("pngsource: provider is unavailable")
	ErrRequestInvalid    = errors.New("pngsource: source request is invalid")
	ErrDocumentMismatch  = errors.New("pngsource: source request belongs to another document")
	ErrAssetUnavailable  = errors.New("pngsource: requested image asset is unavailable")
)

type ErrorCode string

const (
	CodeContextInvalid    ErrorCode = "SOURCE.PNG_V1.CONTEXT_INVALID"
	CodeCanceled          ErrorCode = "SOURCE.PNG_V1.CANCELED"
	CodeConfigInvalid     ErrorCode = "SOURCE.PNG_V1.CONFIG_INVALID"
	CodeDocumentInvalid   ErrorCode = "SOURCE.PNG_V1.DOCUMENT_INVALID"
	CodeBindingInvalid    ErrorCode = "SOURCE.PNG_V1.BINDING_INVALID"
	CodeBindingMissing    ErrorCode = "SOURCE.PNG_V1.BINDING_MISSING"
	CodeBindingExtra      ErrorCode = "SOURCE.PNG_V1.BINDING_EXTRA"
	CodeContentMismatch   ErrorCode = "SOURCE.PNG_V1.CONTENT_MISMATCH"
	CodeDecodeFailed      ErrorCode = "SOURCE.PNG_V1.DECODE_FAILED"
	CodeDimensionMismatch ErrorCode = "SOURCE.PNG_V1.DIMENSION_MISMATCH"
	CodeResourceLimit     ErrorCode = "SOURCE.PNG_V1.RESOURCE_LIMIT"
	CodeProviderInvalid   ErrorCode = "SOURCE.PNG_V1.PROVIDER_INVALID"
	CodeRequestInvalid    ErrorCode = "SOURCE.PNG_V1.REQUEST_INVALID"
	CodeDocumentMismatch  ErrorCode = "SOURCE.PNG_V1.DOCUMENT_MISMATCH"
	CodeAssetUnavailable  ErrorCode = "SOURCE.PNG_V1.ASSET_UNAVAILABLE"
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

func sourceError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
