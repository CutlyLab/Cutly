package textlayout

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestRejectsMalformedShapedInputAndOutlines(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		want   error
	}{
		{name: "unknown layout profile", mutate: func(value *Input) { value.LayoutProfile = "cutly.layout.unknown.v1" }, want: ErrProfileUnsupported},
		{name: "unknown shaped stream profile", mutate: func(value *Input) { value.ShapedStreamProfile = "cutly.text-shaped-stream.unknown.v1" }, want: ErrProfileUnsupported},
		{name: "unknown font profile", mutate: func(value *Input) { value.FontProfile = "cutly.font.unknown.v1" }, want: ErrProfileUnsupported},
		{name: "invalid utf8", mutate: func(value *Input) { value.Text = string([]byte{0xff}) }, want: ErrInputInvalid},
		{name: "wrong line range", mutate: func(value *Input) { value.Lines[0].ByteEnd = 2 }, want: ErrInputInvalid},
		{name: "missing line", mutate: func(value *Input) { value.Lines = value.Lines[:1] }, want: ErrInputInvalid},
		{name: "nonempty line without glyph", mutate: func(value *Input) { value.Lines[0].Glyphs = nil }, want: ErrInputInvalid},
		{name: "empty line with glyph", mutate: func(value *Input) {
			value.Text = "\nB"
			value.Lines = []ShapedLine{
				{ByteStart: 0, ByteEnd: 0, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 0}}},
				{ByteStart: 1, ByteEnd: 2, Glyphs: []ShapedGlyph{{GlyphID: 30, ClusterByteOffset: 1, XAdvance: 512}}},
			}
			value.Outlines = []GlyphOutline{value.Outlines[0], value.Outlines[2]}
		}, want: ErrInputInvalid},
		{name: "glyph zero", mutate: func(value *Input) { value.Lines[0].Glyphs[0].GlyphID = 0 }, want: ErrInputInvalid},
		{name: "vertical advance", mutate: func(value *Input) { value.Lines[0].Glyphs[0].YAdvance = 1 }, want: ErrInputInvalid},
		{name: "ltr negative total advance", mutate: func(value *Input) {
			value.Lines[0].Glyphs[0].XAdvance = -1000
		}, want: ErrInputInvalid},
		{name: "rtl positive total advance", mutate: func(value *Input) {
			*value = rtlInput(documentv2.TextAlignmentStart)
			value.Lines[0].Glyphs[0].XAdvance = 1000
		}, want: ErrInputInvalid},
		{name: "cluster continuation byte", mutate: func(value *Input) { value.Lines[0].Glyphs[1].ClusterByteOffset = 2 }, want: ErrInputInvalid},
		{name: "cluster at newline", mutate: func(value *Input) { value.Lines[0].Glyphs[1].ClusterByteOffset = 3 }, want: ErrInputInvalid},
		{name: "ltr cluster decreases", mutate: func(value *Input) {
			value.Lines[0].Glyphs[0].ClusterByteOffset = 1
			value.Lines[0].Glyphs[1].ClusterByteOffset = 0
		}, want: ErrInputInvalid},
		{name: "ltr cluster omits line start", mutate: func(value *Input) {
			value.Lines[0].Glyphs[0].ClusterByteOffset = 1
			value.Lines[0].Glyphs[1].ClusterByteOffset = 1
		}, want: ErrInputInvalid},
		{name: "rtl cluster omits line start", mutate: func(value *Input) {
			*value = rtlInput(documentv2.TextAlignmentStart)
			value.Lines[0].Glyphs[0].ClusterByteOffset = 1
		}, want: ErrInputInvalid},
		{name: "rtl cluster decreases", mutate: func(value *Input) {
			*value = rtlInput(documentv2.TextAlignmentStart)
			value.Lines[0].Glyphs[0].ClusterByteOffset = 1
			value.Lines[0].Glyphs[1].ClusterByteOffset = 0
		}, want: ErrInputInvalid},
		{name: "missing outline", mutate: func(value *Input) { value.Outlines = value.Outlines[:2] }, want: ErrInputInvalid},
		{name: "extra outline", mutate: func(value *Input) { value.Outlines = append(value.Outlines, GlyphOutline{GlyphID: 40}) }, want: ErrInputInvalid},
		{name: "duplicate outline", mutate: func(value *Input) { value.Outlines[1].GlyphID = 10 }, want: ErrInputInvalid},
		{name: "outline zero", mutate: func(value *Input) { value.Outlines[0].GlyphID = 0 }, want: ErrInputInvalid},
		{name: "open contour", mutate: func(value *Input) { value.Outlines[0].Commands = value.Outlines[0].Commands[:3] }, want: ErrInputInvalid},
		{name: "implicit new contour", mutate: func(value *Input) { value.Outlines[0].Commands[1].Op = MoveTo }, want: ErrInputInvalid},
		{name: "line before move", mutate: func(value *Input) { value.Outlines[0].Commands[0].Op = LineTo }, want: ErrInputInvalid},
		{name: "wrong point arity", mutate: func(value *Input) { value.Outlines[0].Commands[3].Points = []Point{{}} }, want: ErrInputInvalid},
		{name: "glyph hash uppercase", mutate: func(value *Input) {
			value.Binding.FontContentHash = "sha256:" + "A" + value.Binding.FontContentHash[8:]
		}, want: ErrInputInvalid},
		{name: "semantic hash profile drift", mutate: func(value *Input) { value.Binding.TextSemanticHashProfile = "cutly.hash.other.v1" }, want: ErrInputInvalid},
		{name: "layout input hash profile drift", mutate: func(value *Input) { value.Binding.LayoutInputHashProfile = "cutly.hash.other.v1" }, want: ErrInputInvalid},
		{name: "unicode profile drift", mutate: func(value *Input) { value.Binding.UnicodeTableProfile = "cutly.unicode.other.v1" }, want: ErrInputInvalid},
		{name: "provider profile invalid", mutate: func(value *Input) { value.Binding.ShapeProviderProfile = "Cutly.Shape.v1" }, want: ErrInputInvalid},
		{name: "language invalid", mutate: func(value *Input) { value.Language = "en--US" }, want: ErrInputInvalid},
		{name: "font size not canonical", mutate: func(value *Input) { value.FontSize = documentv2.Rational{Numerator: 2, Denominator: 2} }, want: ErrInputInvalid},
		{name: "line height zero", mutate: func(value *Input) { value.LineHeight = documentv2.Rational{Numerator: 0, Denominator: 1} }, want: ErrInputInvalid},
		{name: "bad direction", mutate: func(value *Input) { value.Direction = "auto" }, want: ErrInputInvalid},
		{name: "bad alignment", mutate: func(value *Input) { value.Alignment = "left" }, want: ErrInputInvalid},
		{name: "bad units per em", mutate: func(value *Input) { value.UnitsPerEm = 15 }, want: ErrInputInvalid},
		{name: "bad ascender", mutate: func(value *Input) { value.HheaAscender = 32768 }, want: ErrInputInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneInput(testInput())
			test.mutate(&input)
			layout, receipt, err := Build(context.Background(), input, Limits{})
			if layout != nil || receipt != (Receipt{}) || !errors.Is(err, test.want) {
				t.Fatalf("layout=%v receipt=%+v err=%v want %v", layout, receipt, err, test.want)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code == "" || typed.Path == "" {
				t.Fatalf("typed error=%#v", err)
			}
		})
	}
}

func TestResourceLimitsAcceptNAndRejectNMinusOne(t *testing.T) {
	input := testInput()
	_, receipt := buildTestLayout(t, input, Limits{})
	tests := []struct {
		name  string
		value uint64
		set   func(*Limits, uint64)
	}{
		{name: "text bytes", value: uint64(len(input.Text)), set: func(value *Limits, limit uint64) { value.MaxTextBytes = limit }},
		{name: "lines", value: receipt.LineCount, set: func(value *Limits, limit uint64) { value.MaxLines = limit }},
		{name: "glyphs per line", value: 2, set: func(value *Limits, limit uint64) { value.MaxGlyphsPerLine = limit }},
		{name: "glyphs", value: receipt.GlyphCount, set: func(value *Limits, limit uint64) { value.MaxGlyphs = limit }},
		{name: "unique glyphs", value: receipt.UniqueGlyphCount, set: func(value *Limits, limit uint64) { value.MaxUniqueGlyphs = limit }},
		{name: "commands per glyph", value: 4, set: func(value *Limits, limit uint64) { value.MaxCommandsPerGlyph = limit }},
		{name: "commands", value: receipt.CommandCount, set: func(value *Limits, limit uint64) { value.MaxCommands = limit }},
		{name: "contours", value: receipt.ContourCount, set: func(value *Limits, limit uint64) { value.MaxContours = limit }},
		{name: "canonical bytes", value: receipt.CanonicalBinaryBytes, set: func(value *Limits, limit uint64) { value.MaxCanonicalBinaryBytes = limit }},
		{name: "working bytes", value: receipt.WorkingBytes, set: func(value *Limits, limit uint64) { value.MaxWorkingBytes = limit }},
		{name: "retained bytes", value: receipt.RetainedBytes, set: func(value *Limits, limit uint64) { value.MaxRetainedBytes = limit }},
		{name: "work units", value: receipt.WorkUnits, set: func(value *Limits, limit uint64) { value.MaxWorkUnits = limit }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.set(&limits, test.value)
			buildTestLayout(t, input, limits)
			limits = DefaultLimits()
			test.set(&limits, test.value-1)
			layout, gotReceipt, err := Build(context.Background(), input, limits)
			if layout != nil || gotReceipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("N-1 layout=%v receipt=%+v err=%v", layout, gotReceipt, err)
			}
		})
	}
}

func TestViewportLimitsAcceptNAndRejectNMinusOne(t *testing.T) {
	input := testInput()
	tests := []struct {
		name  string
		value uint64
		set   func(*Limits, uint64)
	}{
		{name: "width", value: uint64(input.Viewport.WidthPixels), set: func(value *Limits, limit uint64) { value.MaxWidth = uint32(limit) }},
		{name: "height", value: uint64(input.Viewport.HeightPixels), set: func(value *Limits, limit uint64) { value.MaxHeight = uint32(limit) }},
		{name: "pixels", value: uint64(input.Viewport.WidthPixels) * uint64(input.Viewport.HeightPixels), set: func(value *Limits, limit uint64) { value.MaxPixels = limit }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.set(&limits, test.value)
			buildTestLayout(t, input, limits)
			limits = DefaultLimits()
			test.set(&limits, test.value-1)
			layout, receipt, err := Build(context.Background(), input, limits)
			if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("N-1 layout=%v receipt=%+v err=%v", layout, receipt, err)
			}
		})
	}
}

func TestRejectsLimitsBeyondHardCaps(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{name: "width", mutate: func(value *Limits) { value.MaxWidth = documentv2.RasterViewportMaxDimension + 1 }},
		{name: "height", mutate: func(value *Limits) { value.MaxHeight = documentv2.RasterViewportMaxDimension + 1 }},
		{name: "pixels", mutate: func(value *Limits) {
			value.MaxPixels = uint64(documentv2.RasterViewportMaxDimension)*uint64(documentv2.RasterViewportMaxDimension) + 1
		}},
		{name: "text", mutate: func(value *Limits) { value.MaxTextBytes = HardMaxTextBytes + 1 }},
		{name: "lines", mutate: func(value *Limits) { value.MaxLines = HardMaxLines + 1 }},
		{name: "glyphs per line", mutate: func(value *Limits) { value.MaxGlyphsPerLine = HardMaxGlyphsPerLine + 1 }},
		{name: "glyphs", mutate: func(value *Limits) { value.MaxGlyphs = HardMaxGlyphs + 1 }},
		{name: "unique glyphs", mutate: func(value *Limits) { value.MaxUniqueGlyphs = HardMaxUniqueGlyphs + 1 }},
		{name: "commands per glyph", mutate: func(value *Limits) { value.MaxCommandsPerGlyph = HardMaxCommandsPerGlyph + 1 }},
		{name: "commands", mutate: func(value *Limits) { value.MaxCommands = HardMaxCommands + 1 }},
		{name: "contours", mutate: func(value *Limits) { value.MaxContours = HardMaxContours + 1 }},
		{name: "exact bits", mutate: func(value *Limits) { value.MaxExactBits = HardMaxExactBits + 1 }},
		{name: "canonical bytes", mutate: func(value *Limits) { value.MaxCanonicalBinaryBytes = HardMaxCanonicalBinaryBytes + 1 }},
		{name: "working bytes", mutate: func(value *Limits) { value.MaxWorkingBytes = HardMaxWorkingBytes + 1 }},
		{name: "retained bytes", mutate: func(value *Limits) { value.MaxRetainedBytes = HardMaxRetainedBytes + 1 }},
		{name: "work units", mutate: func(value *Limits) { value.MaxWorkUnits = HardMaxWorkUnits + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.mutate(&limits)
			if err := ValidateLimits(limits); !errors.Is(err, ErrLimitsInvalid) {
				t.Fatalf("ValidateLimits error=%v", err)
			}
			layout, receipt, err := Build(context.Background(), testInput(), limits)
			if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrLimitsInvalid) {
				t.Fatalf("layout=%v receipt=%+v err=%v", layout, receipt, err)
			}
		})
	}
}

func TestExactBitLimitBoundary(t *testing.T) {
	input := testInput()
	minimum := uint32(0)
	for bits := uint32(1); bits <= DefaultMaxExactBits; bits++ {
		limits := DefaultLimits()
		limits.MaxExactBits = bits
		if layout, _, err := Build(context.Background(), input, limits); err == nil && layout != nil {
			minimum = bits
			break
		}
	}
	if minimum <= 1 {
		t.Fatalf("unexpected exact boundary %d", minimum)
	}
	limits := DefaultLimits()
	limits.MaxExactBits = minimum
	buildTestLayout(t, input, limits)
	limits.MaxExactBits--
	layout, receipt, err := Build(context.Background(), input, limits)
	if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrExactLimit) {
		t.Fatalf("below exact boundary layout=%v receipt=%+v err=%v", layout, receipt, err)
	}
}

func TestCancellationBeforeAndDuringBuildIsAtomic(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{canceled, &cancelAfterContext{remaining: 5}} {
		layout, receipt, err := Build(ctx, testInput(), Limits{})
		if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("layout=%v receipt=%+v err=%v", layout, receipt, err)
		}
	}
}

func TestShapeProviderProfileUsesCanonicalVersionedGrammar(t *testing.T) {
	if got := Descriptor().MaxShapeProviderProfileBytes; got != maxProfileTokenBytes {
		t.Fatalf("descriptor max_shape_provider_profile_bytes=%d want %d", got, maxProfileTokenBytes)
	}
	invalid := []string{
		"", "a.v", "a.v0", ".v1", "a..b.v1", "a.-b.v1", "a_b_.v1",
		"a.vx", "a.v1.extra", "a.v01", "A.b.v1", "a/b.v1", "a.v1.v2",
		strings.Repeat("a", maxProfileTokenBytes+1) + ".v1",
	}
	for _, token := range invalid {
		t.Run(token, func(t *testing.T) {
			input := testInput()
			input.Binding.ShapeProviderProfile = token
			layout, receipt, err := Build(context.Background(), input, Limits{})
			if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrInputInvalid) {
				t.Fatalf("token %q layout=%v receipt=%+v err=%v", token, layout, receipt, err)
			}
		})
	}
	for _, token := range []string{"a.v1", "cutly.shape.harfbuzz_11-2.v12"} {
		input := testInput()
		input.Binding.ShapeProviderProfile = token
		buildTestLayout(t, input, Limits{})
	}
}

func TestLanguageHardByteBoundary(t *testing.T) {
	valid := "abcdefgh" + strings.Repeat("-12345678", 13) + "-ab"
	if len(valid) != HardMaxLanguageBytes {
		t.Fatalf("test language length=%d", len(valid))
	}
	input := testInput()
	input.Language = valid
	buildTestLayout(t, input, Limits{})
	input.Language += "-a"
	layout, receipt, err := Build(context.Background(), input, Limits{})
	if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("layout=%v receipt=%+v err=%v", layout, receipt, err)
	}
}

type cancelAfterContext struct{ remaining int64 }

func (value *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *cancelAfterContext) Done() <-chan struct{}       { return nil }
func (value *cancelAfterContext) Value(any) any               { return nil }
func (value *cancelAfterContext) Err() error {
	if atomic.AddInt64(&value.remaining, -1) < 0 {
		return context.Canceled
	}
	return nil
}

func TestValidateLimitsAndDefaultNormalization(t *testing.T) {
	if err := ValidateLimits(DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimits(Limits{}); !errors.Is(err, ErrLimitsInvalid) {
		t.Fatalf("zero explicit limits error=%v", err)
	}
	buildTestLayout(t, testInput(), Limits{})
}
