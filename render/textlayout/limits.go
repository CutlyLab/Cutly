package textlayout

import (
	"math"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

const (
	DefaultMaxWidth                uint32 = 4096
	DefaultMaxHeight               uint32 = 4096
	DefaultMaxPixels               uint64 = 16_777_216
	DefaultMaxTextBytes            uint64 = 64 << 10
	DefaultMaxLines                uint64 = 1024
	DefaultMaxGlyphsPerLine        uint64 = 32_768
	DefaultMaxGlyphs               uint64 = 262_144
	DefaultMaxUniqueGlyphs         uint64 = 65_536
	DefaultMaxCommandsPerGlyph     uint64 = 65_536
	DefaultMaxCommands             uint64 = 2 << 20
	DefaultMaxContours             uint64 = 1 << 20
	DefaultMaxExactBits            uint32 = 512
	DefaultMaxCanonicalBinaryBytes uint64 = 256 << 20
	DefaultMaxWorkingBytes         uint64 = 512 << 20
	DefaultMaxRetainedBytes        uint64 = 512 << 20
	DefaultMaxWorkUnits            uint64 = 1 << 32
	HardMaxTextBytes               uint64 = documentv2.TextMaxUTF8Bytes
	HardMaxLines                   uint64 = documentv2.TextMaxLines
	HardMaxGlyphsPerLine           uint64 = 262_144
	HardMaxGlyphs                  uint64 = 4 << 20
	HardMaxUniqueGlyphs            uint64 = 1 << 20
	HardMaxCommandsPerGlyph        uint64 = 262_144
	HardMaxCommands                uint64 = 16 << 20
	HardMaxContours                uint64 = 8 << 20
	HardMaxExactBits               uint32 = 4096
	HardMaxCanonicalBinaryBytes    uint64 = 2 << 30
	HardMaxWorkingBytes            uint64 = 4 << 30
	HardMaxRetainedBytes           uint64 = 2 << 30
	HardMaxWorkUnits               uint64 = 1 << 60
	HardMaxUnitsPerEm              uint32 = 16_384
	HardMaxLanguageBytes                  = 128
	logicalPlacementBytes          uint64 = 48
	logicalCommandBytes            uint64 = 48
	logicalOutlineBytes            uint64 = 32
	logicalLayoutRecordBytes       uint64 = 1024
	logicalReferenceFlagBytes      uint64 = 1
	logicalWorkingFixedBytes       uint64 = 64 << 10
	maxProfileTokenBytes                  = 256
)

type Limits struct {
	MaxWidth                uint32 `json:"max_width"`
	MaxHeight               uint32 `json:"max_height"`
	MaxPixels               uint64 `json:"max_pixels"`
	MaxTextBytes            uint64 `json:"max_text_bytes"`
	MaxLines                uint64 `json:"max_lines"`
	MaxGlyphsPerLine        uint64 `json:"max_glyphs_per_line"`
	MaxGlyphs               uint64 `json:"max_glyphs"`
	MaxUniqueGlyphs         uint64 `json:"max_unique_glyphs"`
	MaxCommandsPerGlyph     uint64 `json:"max_commands_per_glyph"`
	MaxCommands             uint64 `json:"max_commands"`
	MaxContours             uint64 `json:"max_contours"`
	MaxExactBits            uint32 `json:"max_exact_bits"`
	MaxCanonicalBinaryBytes uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkingBytes         uint64 `json:"max_working_bytes"`
	MaxRetainedBytes        uint64 `json:"max_retained_bytes"`
	MaxWorkUnits            uint64 `json:"max_work_units"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: DefaultMaxWidth, MaxHeight: DefaultMaxHeight, MaxPixels: DefaultMaxPixels,
		MaxTextBytes: DefaultMaxTextBytes, MaxLines: DefaultMaxLines,
		MaxGlyphsPerLine: DefaultMaxGlyphsPerLine, MaxGlyphs: DefaultMaxGlyphs,
		MaxUniqueGlyphs:     DefaultMaxUniqueGlyphs,
		MaxCommandsPerGlyph: DefaultMaxCommandsPerGlyph, MaxCommands: DefaultMaxCommands,
		MaxContours: DefaultMaxContours, MaxExactBits: DefaultMaxExactBits,
		MaxCanonicalBinaryBytes: DefaultMaxCanonicalBinaryBytes,
		MaxWorkingBytes:         DefaultMaxWorkingBytes, MaxRetainedBytes: DefaultMaxRetainedBytes,
		MaxWorkUnits: DefaultMaxWorkUnits,
	}
}

func ValidateLimits(value Limits) error { return validateLimits(value) }

func validateLimits(value Limits) error {
	if value.MaxWidth == 0 || value.MaxWidth > documentv2.RasterViewportMaxDimension ||
		value.MaxHeight == 0 || value.MaxHeight > documentv2.RasterViewportMaxDimension ||
		value.MaxPixels == 0 || value.MaxPixels > uint64(documentv2.RasterViewportMaxDimension)*uint64(documentv2.RasterViewportMaxDimension) ||
		value.MaxTextBytes == 0 || value.MaxTextBytes > HardMaxTextBytes ||
		value.MaxLines == 0 || value.MaxLines > HardMaxLines ||
		value.MaxGlyphsPerLine == 0 || value.MaxGlyphsPerLine > HardMaxGlyphsPerLine ||
		value.MaxGlyphs == 0 || value.MaxGlyphs > HardMaxGlyphs ||
		value.MaxUniqueGlyphs == 0 || value.MaxUniqueGlyphs > HardMaxUniqueGlyphs ||
		value.MaxCommandsPerGlyph == 0 || value.MaxCommandsPerGlyph > HardMaxCommandsPerGlyph ||
		value.MaxCommands == 0 || value.MaxCommands > HardMaxCommands ||
		value.MaxContours == 0 || value.MaxContours > HardMaxContours ||
		value.MaxExactBits == 0 || value.MaxExactBits > HardMaxExactBits ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > HardMaxCanonicalBinaryBytes ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > HardMaxWorkingBytes ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > HardMaxRetainedBytes ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > HardMaxWorkUnits {
		return layoutError(CodeLimitsInvalid, "limits", ErrLimitsInvalid)
	}
	return nil
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}

func maxInt() uint64 { return uint64(^uint(0) >> 1) }
