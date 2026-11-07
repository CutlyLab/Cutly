package vectorraster

import (
	"math"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	DefaultMaxWidth                   uint32 = 4096
	DefaultMaxHeight                  uint32 = 4096
	DefaultMaxPixels                  uint64 = 16_777_216
	DefaultMaxPaths                   uint64 = 1024
	DefaultMaxCommands                uint64 = 65_536
	DefaultMaxEdges                   uint64 = 65_536
	DefaultMaxExactBits               uint32 = 512
	DefaultMaxCanonicalSourceBytes    uint64 = 32 << 20
	DefaultMaxWorkingBytes            uint64 = 512 << 20
	DefaultMaxRetainedBytes           uint64 = 256 << 20
	DefaultMaxWorkUnits               uint64 = 1 << 32
	HardMaxPaths                      uint64 = 1 << 20
	HardMaxCommands                   uint64 = 4 << 20
	HardMaxEdges                      uint64 = 4 << 20
	HardMaxExactBits                  uint32 = 4096
	HardMaxCanonicalSourceBytes       uint64 = 512 << 20
	HardMaxWorkingBytes               uint64 = 4 << 30
	HardMaxWorkUnits                  uint64 = 1 << 60
	logicalBytesPerStagingPixel       uint64 = cpucompositor.BytesPerPixel
	logicalBytesPerCoveragePixel      uint64 = 1
	logicalCanonicalizationMultiplier uint64 = 3
)

// Limits is an explicit per-call policy. Zero fields do not inherit defaults: callers that want
// the reference policy must pass DefaultLimits(). Limits affect admission only, never pixels.
type Limits struct {
	MaxWidth                uint32 `json:"max_width"`
	MaxHeight               uint32 `json:"max_height"`
	MaxPixels               uint64 `json:"max_pixels"`
	MaxPaths                uint64 `json:"max_paths"`
	MaxCommands             uint64 `json:"max_commands"`
	MaxEdges                uint64 `json:"max_edges"`
	MaxExactBits            uint32 `json:"max_exact_bits"`
	MaxCanonicalSourceBytes uint64 `json:"max_canonical_source_bytes"`
	MaxWorkingBytes         uint64 `json:"max_working_bytes"`
	MaxRetainedBytes        uint64 `json:"max_retained_bytes"`
	MaxWorkUnits            uint64 `json:"max_work_units"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: DefaultMaxWidth, MaxHeight: DefaultMaxHeight, MaxPixels: DefaultMaxPixels,
		MaxPaths: DefaultMaxPaths, MaxCommands: DefaultMaxCommands, MaxEdges: DefaultMaxEdges,
		MaxExactBits: DefaultMaxExactBits, MaxCanonicalSourceBytes: DefaultMaxCanonicalSourceBytes,
		MaxWorkingBytes: DefaultMaxWorkingBytes, MaxRetainedBytes: DefaultMaxRetainedBytes,
		MaxWorkUnits: DefaultMaxWorkUnits,
	}
}

// ValidateLimits performs the complete pure policy validation used by Rasterize. Providers should
// call it while validating their own configuration even when a document has no vector assets.
func ValidateLimits(value Limits) error { return validateLimits(value) }

func validateLimits(value Limits) error {
	maxSurfacePixels := cpucompositor.HardMaxSurfaceBytes / cpucompositor.BytesPerPixel
	if value.MaxWidth == 0 || value.MaxWidth > documentv2.RasterViewportMaxDimension ||
		value.MaxHeight == 0 || value.MaxHeight > documentv2.RasterViewportMaxDimension ||
		value.MaxPixels == 0 || value.MaxPixels > maxSurfacePixels ||
		value.MaxPaths == 0 || value.MaxPaths > HardMaxPaths ||
		value.MaxCommands == 0 || value.MaxCommands > HardMaxCommands ||
		value.MaxEdges == 0 || value.MaxEdges > HardMaxEdges ||
		value.MaxExactBits == 0 || value.MaxExactBits > HardMaxExactBits ||
		value.MaxCanonicalSourceBytes == 0 || value.MaxCanonicalSourceBytes > HardMaxCanonicalSourceBytes ||
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

func maxInt() uint64 { return uint64(^uint(0) >> 1) }
