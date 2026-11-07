// Package sfntglyf implements Cutly's deliberately narrow, strict static-TrueType gate.
//
// The first profile validates a single-face sfnt container, its core horizontal metrics,
// loca/glyf ranges and Unicode cmap. It does not decode outlines or authorize shaping.
package sfntglyf

import (
	"errors"
	"fmt"
)

var (
	ErrContextInvalid         = errors.New("sfntglyf: context is required")
	ErrCanceled               = errors.New("sfntglyf: operation canceled")
	ErrLimitsInvalid          = errors.New("sfntglyf: limits are invalid")
	ErrProfileMismatch        = errors.New("sfntglyf: font is outside the static-glyf profile")
	ErrContainerInvalid       = errors.New("sfntglyf: sfnt container is invalid")
	ErrChecksumInvalid        = errors.New("sfntglyf: checksum is invalid")
	ErrTableInvalid           = errors.New("sfntglyf: core table is invalid")
	ErrCmapInvalid            = errors.New("sfntglyf: cmap is invalid")
	ErrOutlineInputInvalid    = errors.New("sfntglyf: outline input is invalid")
	ErrOutlineInvalid         = errors.New("sfntglyf: glyf body is invalid")
	ErrOutlineGraphInvalid    = errors.New("sfntglyf: component graph is invalid")
	ErrOutlineMaxpMismatch    = errors.New("sfntglyf: outline maxp declarations mismatch")
	ErrGeometryInputInvalid   = errors.New("sfntglyf: geometry input is invalid")
	ErrGeometryInvalid        = errors.New("sfntglyf: exact outline geometry is invalid")
	ErrGeometryMetrics        = errors.New("sfntglyf: outline metrics do not match geometry")
	ErrOutlineSetInputInvalid = errors.New("sfntglyf: outline-set input is invalid")
	ErrOutlineSetCoordinate   = errors.New("sfntglyf: outline-set coordinate is not representable")
	ErrLayoutGraphInvalid     = errors.New("sfntglyf: OpenType layout graph is invalid")
	ErrShapeProviderInvalid   = errors.New("sfntglyf: shape provider binding is invalid")
	ErrShapeInputInvalid      = errors.New("sfntglyf: shape input is invalid")
	ErrShapeMissingGlyph      = errors.New("sfntglyf: shape input has a missing glyph")
	ErrShapeInvariant         = errors.New("sfntglyf: shape provider invariant failed")
	ErrLayoutSourceInvalid    = errors.New("sfntglyf: layout source binding is invalid")
	ErrLayoutSourceInvariant  = errors.New("sfntglyf: layout source invariant failed")
	ErrResourceLimit          = errors.New("sfntglyf: resource limit exceeded")
	ErrAllocationFailed       = errors.New("sfntglyf: allocation failed")
	ErrInvariantFailed        = errors.New("sfntglyf: internal invariant failed")
)

type ErrorCode string

const (
	CodeContextInvalid          ErrorCode = "FONT.SFNT_GLYF_CORE_V1.CONTEXT_INVALID"
	CodeCanceled                ErrorCode = "FONT.SFNT_GLYF_CORE_V1.CANCELED"
	CodeLimitsInvalid           ErrorCode = "FONT.SFNT_GLYF_CORE_V1.LIMITS_INVALID"
	CodeProfileMismatch         ErrorCode = "FONT.SFNT_GLYF_CORE_V1.PROFILE_MISMATCH"
	CodeContainerInvalid        ErrorCode = "FONT.SFNT_GLYF_CORE_V1.CONTAINER_INVALID"
	CodeChecksumInvalid         ErrorCode = "FONT.SFNT_GLYF_CORE_V1.CHECKSUM_INVALID"
	CodeTableInvalid            ErrorCode = "FONT.SFNT_GLYF_CORE_V1.TABLE_INVALID"
	CodeCmapInvalid             ErrorCode = "FONT.SFNT_GLYF_CORE_V1.CMAP_INVALID"
	CodeOutlineInputInvalid     ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.INPUT_INVALID"
	CodeOutlineInvalid          ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.GLYPH_INVALID"
	CodeOutlineGraphInvalid     ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.GRAPH_INVALID"
	CodeOutlineMaxpMismatch     ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.MAXP_MISMATCH"
	CodeOutlineLimitsInvalid    ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.LIMITS_INVALID"
	CodeOutlineResourceLimit    ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.RESOURCE_LIMIT"
	CodeOutlineInvariantFailed  ErrorCode = "FONT.STATIC_GLYF_SCAN_V1.INVARIANT_FAILED"
	CodeGeometryInputInvalid    ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.INPUT_INVALID"
	CodeGeometryInvalid         ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.GEOMETRY_INVALID"
	CodeGeometryMetrics         ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.METRICS_MISMATCH"
	CodeGeometryLimitsInvalid   ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.LIMITS_INVALID"
	CodeGeometryResourceLimit   ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.RESOURCE_LIMIT"
	CodeGeometryInvariant       ErrorCode = "FONT.STATIC_GLYF_GEOMETRY_V1.INVARIANT_FAILED"
	CodeOutlineSetInputInvalid  ErrorCode = "FONT.STATIC_GLYF_OUTLINE_SET_V1.INPUT_INVALID"
	CodeOutlineSetLimitsInvalid ErrorCode = "FONT.STATIC_GLYF_OUTLINE_SET_V1.LIMITS_INVALID"
	CodeOutlineSetResourceLimit ErrorCode = "FONT.STATIC_GLYF_OUTLINE_SET_V1.RESOURCE_LIMIT"
	CodeOutlineSetCoordinate    ErrorCode = "FONT.STATIC_GLYF_OUTLINE_SET_V1.COORDINATE_RANGE"
	CodeOutlineSetInvariant     ErrorCode = "FONT.STATIC_GLYF_OUTLINE_SET_V1.INVARIANT_FAILED"
	CodeLayoutGraphInvalid      ErrorCode = "FONT.OPENTYPE_LAYOUT_GRAPH_V1.INVALID"
	CodeLayoutGraphLimits       ErrorCode = "FONT.OPENTYPE_LAYOUT_GRAPH_V1.LIMITS_INVALID"
	CodeLayoutGraphResource     ErrorCode = "FONT.OPENTYPE_LAYOUT_GRAPH_V1.RESOURCE_LIMIT"
	CodeLayoutGraphInvariant    ErrorCode = "FONT.OPENTYPE_LAYOUT_GRAPH_V1.INVARIANT_FAILED"
	CodeShapeProviderInvalid    ErrorCode = "FONT.GO_TEXT_SHAPE_V1.PROVIDER_INVALID"
	CodeShapeInputInvalid       ErrorCode = "FONT.GO_TEXT_SHAPE_V1.INPUT_INVALID"
	CodeShapeMissingGlyph       ErrorCode = "FONT.GO_TEXT_SHAPE_V1.MISSING_GLYPH"
	CodeShapeLimitsInvalid      ErrorCode = "FONT.GO_TEXT_SHAPE_V1.LIMITS_INVALID"
	CodeShapeResourceLimit      ErrorCode = "FONT.GO_TEXT_SHAPE_V1.RESOURCE_LIMIT"
	CodeShapeInvariant          ErrorCode = "FONT.GO_TEXT_SHAPE_V1.INVARIANT_FAILED"
	CodeLayoutSourceInvalid     ErrorCode = "FONT.STATIC_GLYF_LAYOUT_SOURCE_V1.INVALID"
	CodeLayoutSourceLimits      ErrorCode = "FONT.STATIC_GLYF_LAYOUT_SOURCE_V1.LIMITS_INVALID"
	CodeLayoutSourceResource    ErrorCode = "FONT.STATIC_GLYF_LAYOUT_SOURCE_V1.RESOURCE_LIMIT"
	CodeLayoutSourceInvariant   ErrorCode = "FONT.STATIC_GLYF_LAYOUT_SOURCE_V1.INVARIANT_FAILED"
	CodeResourceLimit           ErrorCode = "FONT.SFNT_GLYF_CORE_V1.RESOURCE_LIMIT"
	CodeAllocationFailed        ErrorCode = "FONT.SFNT_GLYF_CORE_V1.ALLOCATION_FAILED"
	CodeInvariantFailed         ErrorCode = "FONT.SFNT_GLYF_CORE_V1.INVARIANT_FAILED"
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

func fontError(code ErrorCode, path string, cause error) error {
	return &Error{Code: code, Path: path, Err: cause}
}
