package textlayout

import (
	"context"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func testDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func testInput() Input {
	return Input{
		TextProfile:         documentv2.TextProfilePinnedFontViewportPXV2,
		RunInputProfile:     documentv2.TextRunInputProfileV1,
		ShapedStreamProfile: NormalizedShapedStreamProfileV1,
		LayoutProfile:       ProfileV1,
		FontProfile:         documentv2.TextFontProfileStaticGlyfV1,
		OutlineProfile:      documentv2.TextOutlineProfileQ26_6V1,
		Text:                "Aé\nB",
		Viewport:            documentv2.RasterViewport{WidthPixels: 100, HeightPixels: 128},
		FontSize:            documentv2.Rational{Numerator: 64, Denominator: 1},
		LineHeight:          documentv2.Rational{Numerator: 20, Denominator: 1},
		Language:            "en",
		Script:              documentv2.TextScriptLatin,
		Direction:           documentv2.TextDirectionLTR,
		Alignment:           documentv2.TextAlignmentStart,
		UnitsPerEm:          64,
		HheaAscender:        48,
		Lines: []ShapedLine{
			{ByteStart: 0, ByteEnd: 3, Glyphs: []ShapedGlyph{
				{GlyphID: 10, ClusterByteOffset: 0, XAdvance: 640, XOffset: -64, YOffset: 64},
				{GlyphID: 20, ClusterByteOffset: 1, XAdvance: 320, XOffset: 32, YOffset: -32},
			}},
			{ByteStart: 4, ByteEnd: 5, Glyphs: []ShapedGlyph{
				{GlyphID: 30, ClusterByteOffset: 4, XAdvance: 512},
			}},
		},
		Outlines: []GlyphOutline{
			{GlyphID: 10, Commands: []OutlineCommand{
				{Op: MoveTo, Points: []Point{{X: 0, Y: 0}}},
				{Op: LineTo, Points: []Point{{X: 640, Y: 0}}},
				{Op: QuadTo, Points: []Point{{X: 640, Y: 640}, {X: 0, Y: 640}}},
				{Op: Close},
			}},
			{GlyphID: 20},
			{GlyphID: 30, Commands: []OutlineCommand{
				{Op: MoveTo, Points: []Point{{X: -64, Y: -64}}},
				{Op: LineTo, Points: []Point{{X: 320, Y: 448}}},
				{Op: Close},
			}},
		},
		Binding: Binding{
			TextSemanticHashProfile: documentv2.TextSemanticHashProfileV1,
			TextSemanticHash:        testDigest('1'), LayoutInputHash: testDigest('2'),
			LayoutInputHashProfile:   documentv2.TextLayoutInputHashProfileV1,
			FontContentHash:          testDigest('3'),
			UnicodeTableProfile:      documentv2.TextUnicodeValidationTableProfileV1,
			UnicodeTableHashProfile:  documentv2.TextUnicodeValidationTableHashProfileV1,
			UnicodeTableHash:         documentv2.TextUnicodeValidationTableHashV1,
			ShapeProviderProfile:     "cutly.shape.test-static-glyf.v1",
			ShapeProviderFingerprint: testDigest('4'),
		},
	}
}

func cloneInput(input Input) Input {
	result := input
	result.Lines = make([]ShapedLine, len(input.Lines))
	for lineIndex, line := range input.Lines {
		result.Lines[lineIndex] = line
		result.Lines[lineIndex].Glyphs = append([]ShapedGlyph(nil), line.Glyphs...)
	}
	result.Outlines = make([]GlyphOutline, len(input.Outlines))
	for outlineIndex, outline := range input.Outlines {
		result.Outlines[outlineIndex] = outline
		result.Outlines[outlineIndex].Commands = make([]OutlineCommand, len(outline.Commands))
		for commandIndex, command := range outline.Commands {
			result.Outlines[outlineIndex].Commands[commandIndex] = command
			result.Outlines[outlineIndex].Commands[commandIndex].Points = append([]Point(nil), command.Points...)
		}
	}
	return result
}

func oneGlyphInput(text string) Input {
	result := testInput()
	result.Text = text
	result.Lines = []ShapedLine{{ByteStart: 0, ByteEnd: uint64(len(text)), Glyphs: []ShapedGlyph{
		{GlyphID: 10, ClusterByteOffset: 0, XAdvance: 640},
	}}}
	result.Outlines = result.Outlines[:1]
	return result
}

func rtlInput(alignment documentv2.TextAlignment) Input {
	result := testInput()
	result.Text = "AB"
	result.Direction = documentv2.TextDirectionRTL
	result.Alignment = alignment
	result.Lines = []ShapedLine{{ByteStart: 0, ByteEnd: 2, Glyphs: []ShapedGlyph{
		{GlyphID: 10, ClusterByteOffset: 0, XAdvance: -640, XOffset: -640},
		{GlyphID: 20, ClusterByteOffset: 1, XAdvance: -320, XOffset: -320},
	}}}
	result.Outlines = result.Outlines[:2]
	return result
}

func buildTestLayout(t *testing.T, input Input, limits Limits) (*Layout, Receipt) {
	t.Helper()
	layout, receipt, err := Build(context.Background(), input, limits)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if layout == nil || receipt == (Receipt{}) {
		t.Fatalf("Build returned empty success: layout=%v receipt=%+v", layout, receipt)
	}
	return layout, receipt
}

func placementsForTest(t *testing.T, layout *Layout) []GlyphPlacement {
	t.Helper()
	view, err := layout.View()
	if err != nil {
		t.Fatal(err)
	}
	result := make([]GlyphPlacement, view.PlacementCount())
	for index := range result {
		result[index], err = view.Placement(index)
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}
