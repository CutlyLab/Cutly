package textlayer

import (
	"math"

	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

const (
	DefaultMaxBindings               = 256
	HardMaxBindings                  = 4096
	DefaultMaxClipScopes             = 65_536
	HardMaxClipScopes                = 1 << 20
	DefaultMaxTextAssets             = 256
	HardMaxTextAssets                = 4096
	DefaultMaxFontAssets             = 256
	HardMaxFontAssets                = 4096
	DefaultMaxDocumentNodes          = 262_144
	HardMaxDocumentNodes             = 4 << 20
	DefaultMaxTracksPerTimeline      = 4096
	HardMaxTracksPerTimeline         = 65_536
	DefaultMaxCanonicalDocumentBytes = 128 << 20
	HardMaxCanonicalDocumentBytes    = 1 << 30
	DefaultMaxTotalSurfaceBytes      = 512 << 20
	HardMaxTotalSurfaceBytes         = 4 << 30
	DefaultMaxCompileWorkingBytes    = 8 << 30
	HardMaxCompileWorkingBytes       = 16 << 30
)

type Limits struct {
	MaxBindings                int                              `json:"max_bindings"`
	MaxClipScopes              int                              `json:"max_clip_scopes"`
	MaxTextAssets              int                              `json:"max_text_assets"`
	MaxFontAssets              int                              `json:"max_font_assets"`
	MaxDocumentNodes           int                              `json:"max_document_nodes"`
	MaxTracksPerTimeline       int                              `json:"max_tracks_per_timeline"`
	MaxCanonicalDocumentBytes  uint64                           `json:"max_canonical_document_bytes"`
	MaxTotalStaticSurfaceBytes uint64                           `json:"max_total_static_surface_bytes"`
	MaxCompileWorkingBytes     uint64                           `json:"max_compile_working_bytes"`
	FrozenText                 evaluator.FrozenTextSourceLimits `json:"frozen_text"`
	Assembly                   textassembly.Limits              `json:"assembly"`
	Raster                     textassembly.RasterLimits        `json:"raster"`
}

type Config struct {
	Limits Limits `json:"limits"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxBindings: DefaultMaxBindings, MaxClipScopes: DefaultMaxClipScopes,
		MaxTextAssets: DefaultMaxTextAssets, MaxFontAssets: DefaultMaxFontAssets,
		MaxDocumentNodes: DefaultMaxDocumentNodes, MaxTracksPerTimeline: DefaultMaxTracksPerTimeline,
		MaxCanonicalDocumentBytes:  DefaultMaxCanonicalDocumentBytes,
		MaxTotalStaticSurfaceBytes: DefaultMaxTotalSurfaceBytes,
		MaxCompileWorkingBytes:     DefaultMaxCompileWorkingBytes,
		FrozenText:                 evaluator.DefaultFrozenTextSourceLimits(), Assembly: textassembly.DefaultLimits(),
		Raster: textassembly.DefaultRasterLimits(),
	}
}

func DefaultConfig() Config { return Config{Limits: DefaultLimits()} }

func normalizeConfig(value Config) (Config, error) {
	result := value
	defaults := DefaultLimits()
	if result.Limits == (Limits{}) {
		result.Limits = defaults
	} else {
		limits := &result.Limits
		if limits.MaxBindings == 0 {
			limits.MaxBindings = defaults.MaxBindings
		}
		if limits.MaxClipScopes == 0 {
			limits.MaxClipScopes = defaults.MaxClipScopes
		}
		if limits.MaxTextAssets == 0 {
			limits.MaxTextAssets = defaults.MaxTextAssets
		}
		if limits.MaxFontAssets == 0 {
			limits.MaxFontAssets = defaults.MaxFontAssets
		}
		if limits.MaxDocumentNodes == 0 {
			limits.MaxDocumentNodes = defaults.MaxDocumentNodes
		}
		if limits.MaxTracksPerTimeline == 0 {
			limits.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
		}
		if limits.MaxCanonicalDocumentBytes == 0 {
			limits.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
		}
		if limits.MaxTotalStaticSurfaceBytes == 0 {
			limits.MaxTotalStaticSurfaceBytes = defaults.MaxTotalStaticSurfaceBytes
		}
		if limits.MaxCompileWorkingBytes == 0 {
			limits.MaxCompileWorkingBytes = defaults.MaxCompileWorkingBytes
		}
		if limits.FrozenText == (evaluator.FrozenTextSourceLimits{}) {
			limits.FrozenText = defaults.FrozenText
		}
		if limits.Assembly == (textassembly.Limits{}) {
			limits.Assembly = defaults.Assembly
		}
		if limits.Raster == (textassembly.RasterLimits{}) {
			limits.Raster = defaults.Raster
		}
	}
	l := result.Limits
	if l.MaxBindings <= 0 || l.MaxBindings > HardMaxBindings ||
		l.MaxClipScopes <= 0 || l.MaxClipScopes > HardMaxClipScopes ||
		l.MaxTextAssets <= 0 || l.MaxTextAssets > HardMaxTextAssets ||
		l.MaxFontAssets <= 0 || l.MaxFontAssets > HardMaxFontAssets ||
		l.MaxDocumentNodes <= 0 || l.MaxDocumentNodes > HardMaxDocumentNodes ||
		l.MaxTracksPerTimeline <= 0 || l.MaxTracksPerTimeline > HardMaxTracksPerTimeline ||
		l.MaxCanonicalDocumentBytes == 0 || l.MaxCanonicalDocumentBytes > HardMaxCanonicalDocumentBytes ||
		l.MaxTotalStaticSurfaceBytes < cpucompositor.BytesPerPixel || l.MaxTotalStaticSurfaceBytes > HardMaxTotalSurfaceBytes ||
		l.MaxCompileWorkingBytes == 0 || l.MaxCompileWorkingBytes > HardMaxCompileWorkingBytes {
		return Config{}, providerError(CodeConfigInvalid, "config/limits", ErrConfigInvalid)
	}
	if err := validateFrozenLimits(l.FrozenText); err != nil {
		return Config{}, providerError(CodeConfigInvalid, "config/limits/frozen_text", err)
	}
	if err := validateAssemblyLimits(l.Assembly); err != nil {
		return Config{}, providerError(CodeConfigInvalid, "config/limits/assembly", err)
	}
	if err := validateRasterLimits(l.Raster); err != nil {
		return Config{}, providerError(CodeConfigInvalid, "config/limits/raster", err)
	}
	return result, nil
}

func validateFrozenLimits(value evaluator.FrozenTextSourceLimits) error {
	if value.MaxTextBytes == 0 || value.MaxTextBytes > 1<<20 ||
		value.MaxCanonicalSourceBytes == 0 || value.MaxCanonicalSourceBytes > 64<<20 ||
		value.MaxTextSemanticCanonicalBytes == 0 || value.MaxTextSemanticCanonicalBytes > 128<<20 ||
		value.MaxLayoutInputCanonicalBytes == 0 || value.MaxLayoutInputCanonicalBytes > 128<<20 ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > 512<<20 ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > 1<<40 ||
		value.MaxWorkingBytes < 64<<10 || value.MaxWorkingBytes > 1<<30 ||
		value.MaxRetainedBytes < 2<<10 || value.MaxRetainedBytes > 256<<20 {
		return ErrConfigInvalid
	}
	return nil
}

func validateAssemblyLimits(value textassembly.Limits) error {
	s := value.Shape
	if s.MaxTextBytes == 0 || s.MaxTextBytes > sfntglyf.HardMaxShapeTextBytes ||
		s.MaxLines == 0 || s.MaxLines > sfntglyf.HardMaxShapeLines ||
		s.MaxScalarsPerLine == 0 || s.MaxScalarsPerLine > sfntglyf.HardMaxShapeScalarsPerLine ||
		s.MaxScalars == 0 || s.MaxScalars > sfntglyf.HardMaxShapeScalars ||
		s.MaxGlyphsPerLine == 0 || s.MaxGlyphsPerLine > sfntglyf.HardMaxShapeGlyphsPerLine ||
		s.MaxGlyphs == 0 || s.MaxGlyphs > sfntglyf.HardMaxShapeGlyphs ||
		s.MaxCanonicalBytes == 0 || s.MaxCanonicalBytes > sfntglyf.HardMaxShapeCanonicalBytes ||
		s.MaxWorkUnits == 0 || s.MaxWorkUnits > sfntglyf.HardMaxShapeWorkUnits ||
		s.MaxWorkingBytes == 0 || s.MaxWorkingBytes > sfntglyf.HardMaxShapeWorkingBytes ||
		s.MaxRetainedBytes == 0 || s.MaxRetainedBytes > sfntglyf.HardMaxShapeRetainedBytes {
		return ErrConfigInvalid
	}
	ls := value.LayoutSource
	if ls.MaxGlyphs == 0 || ls.MaxGlyphs > sfntglyf.HardMaxLayoutSourceGlyphs ||
		ls.MaxContours == 0 || ls.MaxContours > sfntglyf.HardMaxLayoutSourceContours ||
		ls.MaxCommands == 0 || ls.MaxCommands > sfntglyf.HardMaxLayoutSourceCommands ||
		ls.MaxCommandPointOccurrences == 0 || ls.MaxCommandPointOccurrences > sfntglyf.HardMaxLayoutSourceCommandPointOccurrences ||
		ls.MaxCommandsPerGlyph == 0 || ls.MaxCommandsPerGlyph > sfntglyf.HardMaxLayoutSourceCommandsPerGlyph ||
		ls.MaxCanonicalBinaryBytes == 0 || ls.MaxCanonicalBinaryBytes > sfntglyf.HardMaxLayoutSourceCanonicalBytes ||
		ls.MaxWorkUnits == 0 || ls.MaxWorkUnits > sfntglyf.HardMaxLayoutSourceWorkUnits ||
		ls.MaxWorkingBytes == 0 || ls.MaxWorkingBytes > sfntglyf.HardMaxLayoutSourceWorkingBytes ||
		ls.MaxRetainedBytes == 0 || ls.MaxRetainedBytes > sfntglyf.HardMaxLayoutSourceRetainedBytes {
		return ErrConfigInvalid
	}
	if err := textlayout.ValidateLimits(value.Layout); err != nil {
		return err
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
		return ErrConfigInvalid
	}
	return nil
}

func validateRasterLimits(value textassembly.RasterLimits) error {
	if err := textraster.ValidateLimits(value.Raster); err != nil {
		return err
	}
	if value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > 4<<30 ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > 1<<62 ||
		value.MaxWorkingBytes == 0 || value.MaxWorkingBytes > 16<<30 ||
		value.MaxRetainedBytes == 0 || value.MaxRetainedBytes > 8<<30 {
		return ErrConfigInvalid
	}
	return nil
}
