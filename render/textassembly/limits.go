package textassembly

import (
	"math"

	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

const (
	DefaultMaxLines                   uint64 = 1_024
	DefaultMaxGlyphs                  uint64 = 262_144
	DefaultMaxUniqueGlyphs            uint64 = 65_536
	DefaultMaxCommands                uint64 = 2 << 20
	DefaultMaxCommandPointOccurrences uint64 = 4 << 20
	DefaultMaxCanonicalBinaryBytes    uint64 = 1 << 30
	DefaultMaxWorkUnits               uint64 = 1 << 60
	DefaultMaxWorkingBytes            uint64 = 4 << 30
	DefaultMaxRetainedBytes           uint64 = 2 << 30

	assemblyFixedWorkingBytes  uint64 = 64 << 10
	assemblyFixedRetainedBytes uint64 = 4 << 10
	assemblyLineBytes          uint64 = 24
	assemblyGlyphBytes         uint64 = 48
	assemblyClosureBytes       uint64 = 36
	assemblyOutlineBytes       uint64 = 32
	assemblyCommandBytes       uint64 = 48
	assemblyPointBytes         uint64 = 16
	assemblyDigestStringBytes  uint64 = 71
)

type Limits struct {
	Shape        sfntglyf.ShapeLimits        `json:"shape"`
	LayoutSource sfntglyf.LayoutSourceLimits `json:"layout_source"`
	Layout       textlayout.Limits           `json:"layout"`

	MaxLines                   uint64 `json:"max_lines"`
	MaxGlyphs                  uint64 `json:"max_glyphs"`
	MaxUniqueGlyphs            uint64 `json:"max_unique_glyphs"`
	MaxCommands                uint64 `json:"max_commands"`
	MaxCommandPointOccurrences uint64 `json:"max_command_point_occurrences"`
	MaxCanonicalBinaryBytes    uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits               uint64 `json:"max_work_units"`
	MaxWorkingBytes            uint64 `json:"max_working_bytes"`
	MaxRetainedBytes           uint64 `json:"max_retained_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		Shape: sfntglyf.DefaultShapeLimits(), LayoutSource: sfntglyf.DefaultLayoutSourceLimits(),
		Layout: textlayout.DefaultLimits(), MaxLines: DefaultMaxLines, MaxGlyphs: DefaultMaxGlyphs,
		MaxUniqueGlyphs: DefaultMaxUniqueGlyphs, MaxCommands: DefaultMaxCommands,
		MaxCommandPointOccurrences: DefaultMaxCommandPointOccurrences,
		MaxCanonicalBinaryBytes:    DefaultMaxCanonicalBinaryBytes, MaxWorkUnits: DefaultMaxWorkUnits,
		MaxWorkingBytes: DefaultMaxWorkingBytes, MaxRetainedBytes: DefaultMaxRetainedBytes,
	}
}

type RasterLimits struct {
	Raster                  textraster.Limits `json:"raster"`
	MaxCanonicalBinaryBytes uint64            `json:"max_canonical_binary_bytes"`
	MaxWorkUnits            uint64            `json:"max_work_units"`
	MaxWorkingBytes         uint64            `json:"max_working_bytes"`
	MaxRetainedBytes        uint64            `json:"max_retained_bytes"`
}

func DefaultRasterLimits() RasterLimits {
	return RasterLimits{
		Raster: textraster.DefaultLimits(), MaxCanonicalBinaryBytes: DefaultMaxCanonicalBinaryBytes,
		MaxWorkUnits: DefaultMaxWorkUnits, MaxWorkingBytes: DefaultMaxWorkingBytes,
		MaxRetainedBytes: DefaultMaxRetainedBytes,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if value.Shape == (sfntglyf.ShapeLimits{}) {
		value.Shape = sfntglyf.DefaultShapeLimits()
	} else {
		value.Shape = fillShapeDefaults(value.Shape)
	}
	if value.LayoutSource == (sfntglyf.LayoutSourceLimits{}) {
		value.LayoutSource = sfntglyf.DefaultLayoutSourceLimits()
	} else {
		value.LayoutSource = fillLayoutSourceDefaults(value.LayoutSource)
	}
	if value.Layout == (textlayout.Limits{}) {
		value.Layout = textlayout.DefaultLimits()
	}
	if err := validateShapeLimits(value.Shape); err != nil {
		return Limits{}, err
	}
	if err := validateLayoutSourceLimits(value.LayoutSource); err != nil {
		return Limits{}, err
	}
	if value.MaxLines == 0 || value.MaxLines > textlayout.HardMaxLines ||
		value.MaxGlyphs == 0 || value.MaxGlyphs > textlayout.HardMaxGlyphs ||
		value.MaxUniqueGlyphs == 0 || value.MaxUniqueGlyphs > textlayout.HardMaxUniqueGlyphs ||
		value.MaxCommands == 0 || value.MaxCommands > textlayout.HardMaxCommands ||
		value.MaxCommandPointOccurrences == 0 || value.MaxCommandPointOccurrences > math.MaxUint32 ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > 4<<30 ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > 1<<62 ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > 16<<30 ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > 8<<30 {
		return Limits{}, assemblyError(CodeLimitsInvalid, "limits", ErrLimitsInvalid)
	}
	if err := textlayout.ValidateLimits(value.Layout); err != nil {
		return Limits{}, assemblyError(CodeLimitsInvalid, "limits/layout", err)
	}
	return value, nil
}

func validateShapeLimits(value sfntglyf.ShapeLimits) error {
	if value.MaxTextBytes == 0 || value.MaxTextBytes > sfntglyf.HardMaxShapeTextBytes ||
		value.MaxLines == 0 || value.MaxLines > sfntglyf.HardMaxShapeLines ||
		value.MaxScalarsPerLine == 0 || value.MaxScalarsPerLine > sfntglyf.HardMaxShapeScalarsPerLine ||
		value.MaxScalars == 0 || value.MaxScalars > sfntglyf.HardMaxShapeScalars ||
		value.MaxGlyphsPerLine == 0 || value.MaxGlyphsPerLine > sfntglyf.HardMaxShapeGlyphsPerLine ||
		value.MaxGlyphs == 0 || value.MaxGlyphs > sfntglyf.HardMaxShapeGlyphs ||
		value.MaxCanonicalBytes == 0 || value.MaxCanonicalBytes > sfntglyf.HardMaxShapeCanonicalBytes ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > sfntglyf.HardMaxShapeWorkUnits ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > sfntglyf.HardMaxShapeWorkingBytes ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > sfntglyf.HardMaxShapeRetainedBytes {
		return assemblyError(CodeLimitsInvalid, "limits/shape", ErrLimitsInvalid)
	}
	return nil
}

func validateLayoutSourceLimits(value sfntglyf.LayoutSourceLimits) error {
	if value.MaxGlyphs == 0 || value.MaxGlyphs > sfntglyf.HardMaxLayoutSourceGlyphs ||
		value.MaxContours == 0 || value.MaxContours > sfntglyf.HardMaxLayoutSourceContours ||
		value.MaxCommands == 0 || value.MaxCommands > sfntglyf.HardMaxLayoutSourceCommands ||
		value.MaxCommandPointOccurrences == 0 || value.MaxCommandPointOccurrences > sfntglyf.HardMaxLayoutSourceCommandPointOccurrences ||
		value.MaxCommandsPerGlyph == 0 || value.MaxCommandsPerGlyph > sfntglyf.HardMaxLayoutSourceCommandsPerGlyph ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > sfntglyf.HardMaxLayoutSourceCanonicalBytes ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > sfntglyf.HardMaxLayoutSourceWorkUnits ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > sfntglyf.HardMaxLayoutSourceWorkingBytes ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > sfntglyf.HardMaxLayoutSourceRetainedBytes {
		return assemblyError(CodeLimitsInvalid, "limits/layout_source", ErrLimitsInvalid)
	}
	return nil
}

func fillShapeDefaults(value sfntglyf.ShapeLimits) sfntglyf.ShapeLimits {
	defaults := sfntglyf.DefaultShapeLimits()
	fields := [][2]*uint64{
		{&value.MaxTextBytes, &defaults.MaxTextBytes}, {&value.MaxLines, &defaults.MaxLines},
		{&value.MaxScalarsPerLine, &defaults.MaxScalarsPerLine}, {&value.MaxScalars, &defaults.MaxScalars},
		{&value.MaxGlyphsPerLine, &defaults.MaxGlyphsPerLine}, {&value.MaxGlyphs, &defaults.MaxGlyphs},
		{&value.MaxCanonicalBytes, &defaults.MaxCanonicalBytes}, {&value.MaxWorkUnits, &defaults.MaxWorkUnits},
		{&value.MaxWorkingBytes, &defaults.MaxWorkingBytes}, {&value.MaxRetainedBytes, &defaults.MaxRetainedBytes},
	}
	for _, field := range fields {
		if *field[0] == 0 {
			*field[0] = *field[1]
		}
	}
	return value
}

func fillLayoutSourceDefaults(value sfntglyf.LayoutSourceLimits) sfntglyf.LayoutSourceLimits {
	defaults := sfntglyf.DefaultLayoutSourceLimits()
	fields := [][2]*uint64{
		{&value.MaxGlyphs, &defaults.MaxGlyphs}, {&value.MaxContours, &defaults.MaxContours},
		{&value.MaxCommands, &defaults.MaxCommands}, {&value.MaxCommandPointOccurrences, &defaults.MaxCommandPointOccurrences},
		{&value.MaxCommandsPerGlyph, &defaults.MaxCommandsPerGlyph},
		{&value.MaxCanonicalBinaryBytes, &defaults.MaxCanonicalBinaryBytes},
		{&value.MaxWorkUnits, &defaults.MaxWorkUnits}, {&value.MaxWorkingBytes, &defaults.MaxWorkingBytes},
		{&value.MaxRetainedBytes, &defaults.MaxRetainedBytes},
	}
	for _, field := range fields {
		if *field[0] == 0 {
			*field[0] = *field[1]
		}
	}
	return value
}

func normalizeRasterLimits(value RasterLimits) (RasterLimits, error) {
	if value == (RasterLimits{}) {
		value = DefaultRasterLimits()
	}
	if value.Raster == (textraster.Limits{}) {
		value.Raster = textraster.DefaultLimits()
	}
	if err := textraster.ValidateLimits(value.Raster); err != nil {
		return RasterLimits{}, assemblyError(CodeLimitsInvalid, "limits/raster", err)
	}
	if value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > 4<<30 ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > 1<<62 ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > 16<<30 ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > 8<<30 {
		return RasterLimits{}, assemblyError(CodeLimitsInvalid, "limits", ErrLimitsInvalid)
	}
	return value, nil
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
