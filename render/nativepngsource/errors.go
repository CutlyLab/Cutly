// Package nativepngsource binds strict, content-addressed PNG resources to rastercompositor's
// native-size immutable source-frame contract.
package nativepngsource

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid   = errors.New("nativepngsource: context is required")
	ErrCanceled         = errors.New("nativepngsource: operation canceled")
	ErrConfigInvalid    = errors.New("nativepngsource: configuration is invalid")
	ErrResourceLimit    = errors.New("nativepngsource: resource limit exceeded")
	ErrDocumentInvalid  = errors.New("nativepngsource: document is invalid")
	ErrDocumentMismatch = errors.New("nativepngsource: document hash mismatch")
	ErrBindingInvalid   = errors.New("nativepngsource: binding is invalid")
	ErrBindingMissing   = errors.New("nativepngsource: required binding is missing")
	ErrBindingExtra     = errors.New("nativepngsource: binding is not an enabled image source")
	ErrContentMismatch  = errors.New("nativepngsource: encoded content hash mismatch")
	ErrDecodeFailed     = errors.New("nativepngsource: strict PNG decode failed")
	ErrProviderInvalid  = errors.New("nativepngsource: provider is unavailable")
	ErrRequestInvalid   = errors.New("nativepngsource: request is invalid")
	ErrRequestMismatch  = errors.New("nativepngsource: request does not match the frozen document")
	ErrAssetUnavailable = errors.New("nativepngsource: decoded image asset is unavailable")
)

type ErrorCode string

const (
	CodeContextInvalid   ErrorCode = "SOURCE.NATIVE_PNG_V1.CONTEXT_INVALID"
	CodeCanceled         ErrorCode = "SOURCE.NATIVE_PNG_V1.CANCELED"
	CodeConfigInvalid    ErrorCode = "SOURCE.NATIVE_PNG_V1.CONFIG_INVALID"
	CodeResourceLimit    ErrorCode = "SOURCE.NATIVE_PNG_V1.RESOURCE_LIMIT"
	CodeDocumentInvalid  ErrorCode = "SOURCE.NATIVE_PNG_V1.DOCUMENT_INVALID"
	CodeDocumentMismatch ErrorCode = "SOURCE.NATIVE_PNG_V1.DOCUMENT_MISMATCH"
	CodeBindingInvalid   ErrorCode = "SOURCE.NATIVE_PNG_V1.BINDING_INVALID"
	CodeBindingMissing   ErrorCode = "SOURCE.NATIVE_PNG_V1.BINDING_MISSING"
	CodeBindingExtra     ErrorCode = "SOURCE.NATIVE_PNG_V1.BINDING_EXTRA"
	CodeContentMismatch  ErrorCode = "SOURCE.NATIVE_PNG_V1.CONTENT_MISMATCH"
	CodeDecodeFailed     ErrorCode = "SOURCE.NATIVE_PNG_V1.DECODE_FAILED"
	CodeProviderInvalid  ErrorCode = "SOURCE.NATIVE_PNG_V1.PROVIDER_INVALID"
	CodeRequestInvalid   ErrorCode = "SOURCE.NATIVE_PNG_V1.REQUEST_INVALID"
	CodeRequestMismatch  ErrorCode = "SOURCE.NATIVE_PNG_V1.REQUEST_MISMATCH"
	CodeAssetUnavailable ErrorCode = "SOURCE.NATIVE_PNG_V1.ASSET_UNAVAILABLE"
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

func sourceError(code ErrorCode, path string, err error) error {
	return &Error{Code: code, Path: path, Err: err}
}
