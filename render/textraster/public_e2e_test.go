package textraster_test

import (
	"context"
	"encoding/hex"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayout"
	"github.com/CutlyLab/Cutly/render/textraster"
)

func TestRasterizePublicAPIEndToEndEmptyGlyph(t *testing.T) {
	ctx := context.Background()
	encoded := loadMinimalFont(t)

	font, fontReceipt, err := sfntglyf.Parse(ctx, encoded, sfntglyf.DefaultLimits())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	outlineFont, _, err := sfntglyf.DecodeOutlineFont(ctx, font, sfntglyf.DefaultOutlineLimits())
	if err != nil {
		t.Fatalf("DecodeOutlineFont: %v", err)
	}
	geometry, _, err := sfntglyf.ValidateOutlineGeometry(ctx, outlineFont, sfntglyf.DefaultGeometryLimits())
	if err != nil {
		t.Fatalf("ValidateOutlineGeometry: %v", err)
	}
	outlineSet, outlineSetReceipt, err := sfntglyf.MaterializeOutlineSet(ctx, geometry, sfntglyf.DefaultOutlineSetLimits())
	if err != nil {
		t.Fatalf("MaterializeOutlineSet: %v", err)
	}
	graph, graphReceipt, err := sfntglyf.BuildLayoutGraph(ctx, outlineSet, sfntglyf.DefaultLayoutGraphLimits())
	if err != nil {
		t.Fatalf("BuildLayoutGraph: %v", err)
	}
	provider, providerReceipt, err := sfntglyf.BindShapeProvider(ctx, graph, sfntglyf.DefaultShapeProviderLimits())
	if err != nil {
		t.Fatalf("BindShapeProvider: %v", err)
	}

	shapeInput := sfntglyf.ShapeInput{
		Text:      "A",
		Language:  "en",
		Script:    sfntglyf.ShapeScriptLatin,
		Direction: sfntglyf.ShapeDirectionLTR,
	}
	shaped, shapeReceipt, err := provider.Shape(ctx, shapeInput, sfntglyf.DefaultShapeLimits())
	if err != nil {
		t.Fatalf("Shape: %v", err)
	}
	source, sourceReceipt, err := provider.BindLayoutSource(ctx, sfntglyf.DefaultLayoutSourceLimits())
	if err != nil {
		t.Fatalf("BindLayoutSource: %v", err)
	}

	assertPublicSourceLineage(t, fontReceipt, outlineSetReceipt, graphReceipt, providerReceipt, shapeReceipt, sourceReceipt)
	if sourceReceipt.GlyphsWithContours != 0 || sourceReceipt.ContourCount != 0 ||
		sourceReceipt.CommandCount != 0 || sourceReceipt.MaxCommandsPerGlyph != 0 {
		t.Fatalf("minimal font unexpectedly has drawable outlines: %+v", sourceReceipt)
	}

	fontSize := documentv2.Rational{Numerator: 16, Denominator: 1}
	input := layoutInputFromPublicViews(t, shaped, shapeReceipt, source, sourceReceipt, fontSize)
	layout, layoutReceipt, err := textlayout.Build(ctx, input, textlayout.DefaultLimits())
	if err != nil {
		t.Fatalf("textlayout.Build: %v", err)
	}
	if layoutReceipt.FontContentHash != sourceReceipt.FontContentHash ||
		layoutReceipt.ShapeProviderProfile != sourceReceipt.ShapeProviderProfile ||
		layoutReceipt.ShapeProviderFingerprint != sourceReceipt.DependencyFingerprint ||
		layoutReceipt.GlyphCount != 1 || layoutReceipt.UniqueGlyphCount != 1 ||
		layoutReceipt.ContourCount != 0 || layoutReceipt.CommandCount != 0 {
		t.Fatalf("layout lineage or empty-glyph counts drifted: %+v", layoutReceipt)
	}

	surface, rasterReceipt, err := textraster.Rasterize(ctx, textraster.Request{
		Source:   source,
		Layout:   layout,
		FontSize: fontSize,
		Fill:     color.RGBA16Input{R: 65535, G: 32768, B: 16384, A: 65535},
	}, textraster.DefaultLimits())
	if err != nil {
		t.Fatalf("textraster.Rasterize: %v", err)
	}
	if surface == nil {
		t.Fatal("textraster.Rasterize returned a nil surface")
	}
	assertRasterLineage(t, rasterReceipt, sourceReceipt, layoutReceipt)
	if textraster.ShapingProvenanceStatusV1 != "cutly.text-shaping-provenance.caller-layout-unverified.v1" {
		t.Fatalf("public shaping provenance constant=%q", textraster.ShapingProvenanceStatusV1)
	}
	if rasterReceipt.ShapingProvenanceStatus != textraster.ShapingProvenanceStatusV1 {
		t.Fatalf("shaping provenance=%q", rasterReceipt.ShapingProvenanceStatus)
	}
	if rasterReceipt.PlacementCount != 1 || rasterReceipt.UniqueGlyphCount != 1 ||
		rasterReceipt.ContourCount != 0 || rasterReceipt.CommandCount != 0 ||
		rasterReceipt.UniqueSegmentCount != 0 || rasterReceipt.SegmentCount != 0 {
		t.Fatalf("empty-glyph raster counts drifted: %+v", rasterReceipt)
	}

	pixelHash, err := surface.CanonicalHash()
	if err != nil {
		t.Fatalf("Surface.CanonicalHash: %v", err)
	}
	if pixelHash != rasterReceipt.PixelHash {
		t.Fatalf("pixel hash=%q receipt=%q", pixelHash, rasterReceipt.PixelHash)
	}
	assertTransparentSurface(t, surface)
}

func loadMinimalFont(t *testing.T) []byte {
	t.Helper()
	fixture, err := os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if err != nil {
		t.Fatalf("read minimal font fixture: %v", err)
	}
	encoded, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		t.Fatalf("decode minimal font fixture: %v", err)
	}
	return encoded
}

func layoutInputFromPublicViews(
	t *testing.T,
	shaped *sfntglyf.ShapedText,
	shapeReceipt sfntglyf.ShapeReceipt,
	source *sfntglyf.LayoutSource,
	sourceReceipt sfntglyf.LayoutSourceReceipt,
	fontSize documentv2.Rational,
) textlayout.Input {
	t.Helper()
	view, err := shaped.View()
	if err != nil {
		t.Fatalf("ShapedText.View: %v", err)
	}
	lines := make([]textlayout.ShapedLine, view.LineCount())
	glyphClosure := make(map[uint32]struct{})
	for lineIndex := range lines {
		line, err := view.Line(lineIndex)
		if err != nil {
			t.Fatalf("ShapedTextView.Line(%d): %v", lineIndex, err)
		}
		lines[lineIndex] = textlayout.ShapedLine{
			ByteStart: line.ByteStart(),
			ByteEnd:   line.ByteEnd(),
			Glyphs:    make([]textlayout.ShapedGlyph, line.GlyphCount()),
		}
		for glyphIndex := range lines[lineIndex].Glyphs {
			glyph, err := line.Glyph(glyphIndex)
			if err != nil {
				t.Fatalf("ShapedLineView.Glyph(%d, %d): %v", lineIndex, glyphIndex, err)
			}
			lines[lineIndex].Glyphs[glyphIndex] = textlayout.ShapedGlyph{
				GlyphID:           glyph.GlyphID,
				ClusterByteOffset: glyph.ClusterByteOffset,
				XAdvance:          textlayout.Q26_6(glyph.XAdvance),
				YAdvance:          textlayout.Q26_6(glyph.YAdvance),
				XOffset:           textlayout.Q26_6(glyph.XOffset),
				YOffset:           textlayout.Q26_6(glyph.YOffset),
			}
			glyphClosure[glyph.GlyphID] = struct{}{}
		}
	}
	if len(lines) != 1 || len(lines[0].Glyphs) != 1 || lines[0].Glyphs[0].GlyphID != 1 ||
		lines[0].Glyphs[0].ClusterByteOffset != 0 {
		t.Fatalf("minimal font shaped stream drifted: %+v", lines)
	}

	glyphIDs := make([]uint32, 0, len(glyphClosure))
	for glyphID := range glyphClosure {
		glyphIDs = append(glyphIDs, glyphID)
	}
	sort.Slice(glyphIDs, func(left, right int) bool { return glyphIDs[left] < glyphIDs[right] })
	outlines := make([]textlayout.GlyphOutline, len(glyphIDs))
	for outlineIndex, glyphID := range glyphIDs {
		outline, ok := source.GlyphOutline(glyphID)
		if !ok || outline.GlyphID() != glyphID {
			t.Fatalf("LayoutSource.GlyphOutline(%d)=%+v/%t", glyphID, outline, ok)
		}
		outlines[outlineIndex] = textlayout.GlyphOutline{
			GlyphID:  glyphID,
			Commands: make([]textlayout.OutlineCommand, int(outline.CommandCount())),
		}
		for commandIndex := range outlines[outlineIndex].Commands {
			command, ok := outline.Command(uint64(commandIndex))
			if !ok {
				t.Fatalf("LayoutGlyphOutlineView.Command(%d, %d) not found", glyphID, commandIndex)
			}
			points := make([]textlayout.Point, int(command.PointCount()))
			for pointIndex := range points {
				point, ok := command.Point(uint8(pointIndex))
				if !ok {
					t.Fatalf("LayoutOutlineCommandView.Point(%d, %d, %d) not found", glyphID, commandIndex, pointIndex)
				}
				points[pointIndex] = textlayout.Point{X: textlayout.Q26_6(point.X), Y: textlayout.Q26_6(point.Y)}
			}
			outlines[outlineIndex].Commands[commandIndex] = textlayout.OutlineCommand{
				Op:     publicOutlineOp(t, command.Op()),
				Points: points,
			}
		}
	}

	direction := documentv2.TextDirectionLTR
	if shapeReceipt.Direction == sfntglyf.ShapeDirectionRTL {
		direction = documentv2.TextDirectionRTL
	} else if shapeReceipt.Direction != sfntglyf.ShapeDirectionLTR {
		t.Fatalf("unsupported shaped direction %q", shapeReceipt.Direction)
	}

	// These two hashes remain caller claims by contract. The live font/provider lineage below is
	// sourced exclusively from the receipt-bound LayoutSource and is independently checked by Rasterize.
	callerClaim := "sha256:" + strings.Repeat("0", 64)
	return textlayout.Input{
		TextProfile:         documentv2.TextProfilePinnedFontViewportPXV2,
		RunInputProfile:     documentv2.TextRunInputProfileV1,
		ShapedStreamProfile: textlayout.NormalizedShapedStreamProfileV1,
		LayoutProfile:       textlayout.ProfileV1,
		FontProfile:         documentv2.TextFontProfileStaticGlyfV1,
		OutlineProfile:      documentv2.TextOutlineProfileQ26_6V1,
		Text:                "A",
		Viewport:            documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16},
		FontSize:            fontSize,
		LineHeight:          documentv2.Rational{Numerator: 16, Denominator: 1},
		Language:            shapeReceipt.Language,
		Script:              shapeReceipt.Script,
		Direction:           direction,
		Alignment:           documentv2.TextAlignmentStart,
		UnitsPerEm:          uint32(source.UnitsPerEm()),
		HheaAscender:        int32(source.HheaAscender()),
		Lines:               lines,
		Outlines:            outlines,
		Binding: textlayout.Binding{
			TextSemanticHashProfile:  documentv2.TextSemanticHashProfileV1,
			TextSemanticHash:         callerClaim,
			LayoutInputHashProfile:   documentv2.TextLayoutInputHashProfileV1,
			LayoutInputHash:          callerClaim,
			FontContentHash:          sourceReceipt.FontContentHash,
			UnicodeTableProfile:      documentv2.TextUnicodeValidationTableProfileV1,
			UnicodeTableHashProfile:  documentv2.TextUnicodeValidationTableHashProfileV1,
			UnicodeTableHash:         documentv2.TextUnicodeValidationTableHashV1,
			ShapeProviderProfile:     sourceReceipt.ShapeProviderProfile,
			ShapeProviderFingerprint: sourceReceipt.DependencyFingerprint,
		},
	}
}

func publicOutlineOp(t *testing.T, op sfntglyf.OutlineCommandOp) textlayout.OutlineCommandOp {
	t.Helper()
	switch op {
	case sfntglyf.OutlineMoveTo:
		return textlayout.MoveTo
	case sfntglyf.OutlineLineTo:
		return textlayout.LineTo
	case sfntglyf.OutlineQuadTo:
		return textlayout.QuadTo
	case sfntglyf.OutlineClose:
		return textlayout.Close
	default:
		t.Fatalf("unsupported outline command op %d", op)
		return ""
	}
}

func assertPublicSourceLineage(
	t *testing.T,
	font sfntglyf.Receipt,
	outlines sfntglyf.OutlineSetReceipt,
	graph sfntglyf.LayoutGraphReceipt,
	provider sfntglyf.ShapeProviderReceipt,
	shape sfntglyf.ShapeReceipt,
	source sfntglyf.LayoutSourceReceipt,
) {
	t.Helper()
	if provider.LayoutGraphHash != graph.GraphHash || provider.OutlineSetHash != outlines.OutlineSetHash ||
		provider.FontContentHash != font.ContentHash {
		t.Fatalf("provider lineage mismatch: font=%+v outlines=%+v graph=%+v provider=%+v", font, outlines, graph, provider)
	}
	if shape.ProviderHash != provider.ProviderHash || shape.DependencyFingerprint != provider.DependencyFingerprint ||
		shape.LayoutGraphHash != graph.GraphHash || shape.OutlineSetHash != outlines.OutlineSetHash ||
		shape.FontContentHash != font.ContentHash {
		t.Fatalf("shape lineage mismatch: shape=%+v provider=%+v", shape, provider)
	}
	if source.ShapeProviderHash != provider.ProviderHash || source.DependencyFingerprint != provider.DependencyFingerprint ||
		source.LayoutGraphHash != graph.GraphHash || source.OutlineSetHash != outlines.OutlineSetHash ||
		source.FontContentHash != font.ContentHash {
		t.Fatalf("layout source lineage mismatch: source=%+v provider=%+v", source, provider)
	}
}

func assertRasterLineage(
	t *testing.T,
	raster textraster.Receipt,
	source sfntglyf.LayoutSourceReceipt,
	layout textlayout.Receipt,
) {
	t.Helper()
	if raster.LayoutSourceProfile != source.LayoutSourceProfile ||
		raster.LayoutSourceHashProfile != source.LayoutSourceHashProfile || raster.LayoutSourceHash != source.LayoutSourceHash ||
		raster.ShapeProviderProfile != source.ShapeProviderProfile ||
		raster.ShapeProviderHashProfile != source.ShapeProviderHashProfile || raster.ShapeProviderHash != source.ShapeProviderHash ||
		raster.DependencyFingerprintProfile != source.DependencyFingerprintProfile || raster.DependencyFingerprint != source.DependencyFingerprint ||
		raster.FontContentHashProfile != source.FontContentHashProfile || raster.FontContentHash != source.FontContentHash {
		t.Fatalf("raster/source lineage mismatch: raster=%+v source=%+v", raster, source)
	}
	if raster.LayoutReceiptProfile != layout.Profile ||
		raster.LayoutHashProfile != layout.LayoutHashProfile || raster.LayoutHash != layout.LayoutHash ||
		raster.GlyphStreamHashProfile != layout.GlyphStreamHashProfile || raster.GlyphStreamHash != layout.GlyphStreamHash ||
		raster.OutlineSetHashProfile != layout.OutlineSetHashProfile || raster.OutlineSetHash != layout.OutlineSetHash ||
		raster.PlacementHashProfile != layout.PlacementHashProfile || raster.PlacementHash != layout.PlacementHash {
		t.Fatalf("raster/layout lineage mismatch: raster=%+v layout=%+v", raster, layout)
	}
}

func assertTransparentSurface(t *testing.T, surface *cpucompositor.Surface) {
	t.Helper()
	for y := 0; y < surface.Height(); y++ {
		for x := 0; x < surface.Width(); x++ {
			pixel, err := surface.Pixel(x, y)
			if err != nil {
				t.Fatalf("Surface.Pixel(%d, %d): %v", x, y, err)
			}
			if pixel.R != 0 || pixel.G != 0 || pixel.B != 0 || pixel.A != 0 {
				t.Fatalf("Surface.Pixel(%d, %d)=%+v, want transparent black", x, y, pixel)
			}
		}
	}
}
