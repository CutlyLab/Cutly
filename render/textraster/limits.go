package textraster

import (
	"math"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	DefaultMaxWidth                uint32 = 4096
	DefaultMaxHeight               uint32 = 4096
	DefaultMaxPixels               uint64 = 16_777_216
	DefaultMaxPlacements           uint64 = 262_144
	DefaultMaxUniqueGlyphs         uint64 = 65_536
	DefaultMaxCommands             uint64 = 2 << 20
	DefaultMaxSegments             uint64 = 4 << 20
	DefaultMaxExactBits            uint32 = 512
	DefaultMaxCanonicalBinaryBytes uint64 = 256 << 20
	DefaultMaxWorkingBytes         uint64 = 512 << 20
	DefaultMaxRetainedBytes        uint64 = 512 << 20
	DefaultMaxWorkUnits            uint64 = 1 << 32

	HardMaxPlacements           uint64 = 4 << 20
	HardMaxUniqueGlyphs         uint64 = 1 << 20
	HardMaxCommands             uint64 = 16 << 20
	HardMaxSegments             uint64 = 32 << 20
	HardMaxExactBits            uint32 = 4096
	HardMaxCanonicalBinaryBytes uint64 = 2 << 30
	HardMaxWorkingBytes         uint64 = 4 << 30
	HardMaxWorkUnits            uint64 = 1 << 60
)

// Limits is an explicit per-call admission policy. Zero fields do not inherit defaults; callers
// that want the reference policy must pass DefaultLimits(). Limits never select a lower-quality
// curve or sampling path.
type Limits struct {
	MaxWidth                uint32 `json:"max_width"`
	MaxHeight               uint32 `json:"max_height"`
	MaxPixels               uint64 `json:"max_pixels"`
	MaxPlacements           uint64 `json:"max_placements"`
	MaxUniqueGlyphs         uint64 `json:"max_unique_glyphs"`
	MaxCommands             uint64 `json:"max_commands"`
	MaxSegments             uint64 `json:"max_segments"`
	MaxExactBits            uint32 `json:"max_exact_bits"`
	MaxCanonicalBinaryBytes uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkingBytes         uint64 `json:"max_working_bytes"`
	MaxRetainedBytes        uint64 `json:"max_retained_bytes"`
	MaxWorkUnits            uint64 `json:"max_work_units"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: DefaultMaxWidth, MaxHeight: DefaultMaxHeight, MaxPixels: DefaultMaxPixels,
		MaxPlacements: DefaultMaxPlacements, MaxUniqueGlyphs: DefaultMaxUniqueGlyphs,
		MaxCommands: DefaultMaxCommands, MaxSegments: DefaultMaxSegments,
		MaxExactBits: DefaultMaxExactBits, MaxCanonicalBinaryBytes: DefaultMaxCanonicalBinaryBytes,
		MaxWorkingBytes: DefaultMaxWorkingBytes, MaxRetainedBytes: DefaultMaxRetainedBytes,
		MaxWorkUnits: DefaultMaxWorkUnits,
	}
}

func ValidateLimits(value Limits) error { return validateLimits(value) }

func validateLimits(value Limits) error {
	maxPixels := cpucompositor.HardMaxSurfaceBytes / cpucompositor.BytesPerPixel
	if value.MaxWidth == 0 || value.MaxWidth > documentv2.RasterViewportMaxDimension ||
		value.MaxHeight == 0 || value.MaxHeight > documentv2.RasterViewportMaxDimension ||
		value.MaxPixels == 0 || value.MaxPixels > maxPixels ||
		value.MaxPlacements == 0 || value.MaxPlacements > HardMaxPlacements ||
		value.MaxUniqueGlyphs == 0 || value.MaxUniqueGlyphs > HardMaxUniqueGlyphs ||
		value.MaxCommands == 0 || value.MaxCommands > HardMaxCommands ||
		value.MaxSegments == 0 || value.MaxSegments > HardMaxSegments ||
		value.MaxExactBits == 0 || value.MaxExactBits > HardMaxExactBits ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > HardMaxCanonicalBinaryBytes ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > HardMaxWorkingBytes ||
		value.MaxRetainedBytes < cpucompositor.BytesPerPixel || value.MaxRetainedBytes > cpucompositor.HardMaxSurfaceBytes ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > HardMaxWorkUnits {
		return rasterError(CodeLimitsInvalid, "limits", ErrLimitsInvalid)
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
