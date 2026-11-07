package textraster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

type fakeSource struct {
	receiptValue   sfntglyf.LayoutSourceReceipt
	units          uint16
	ascender       int16
	glyphCountLive uint32
	glyphs         map[uint32]sourceGlyph
	glyphCalls     int
}

func (value *fakeSource) receipt() sfntglyf.LayoutSourceReceipt { return value.receiptValue }
func (value *fakeSource) unitsPerEm() uint16                    { return value.units }
func (value *fakeSource) hheaAscender() int16                   { return value.ascender }
func (value *fakeSource) glyphCount() uint32                    { return value.glyphCountLive }
func (value *fakeSource) glyph(_ context.Context, glyphID uint32, maxCommands uint64) (sourceGlyph, error) {
	value.glyphCalls++
	glyph, ok := value.glyphs[glyphID]
	if !ok || uint64(len(glyph.commands)) > maxCommands {
		return sourceGlyph{}, rasterError(CodeBindingInvalid, "test/source/glyph", ErrBindingInvalid)
	}
	glyph.commands = append([]sourceCommand(nil), glyph.commands...)
	return glyph, nil
}

func TestRasterizeUnionsOverlappingPlacementsAndBindsHashes(t *testing.T) {
	source, outline := rectangleFixture()
	one := buildTestLayout(t, outline, 1, source)
	two := buildTestLayout(t, outline, 2, source)
	request := Request{FontSize: documentv2.Rational{Numerator: 64, Denominator: 1}, Fill: color.RGBA16Input{R: 65535, G: 65535, B: 65535, A: 32768}}
	oneSurface, oneReceipt, err := rasterizeBound(context.Background(), source, requestWithLayout(request, one), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	twoSurface, twoReceipt, err := rasterizeBound(context.Background(), source, requestWithLayout(request, two), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	oneHash, err := oneSurface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	twoHash, err := twoSurface.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if oneHash != twoHash || oneHash != oneReceipt.PixelHash || twoHash != twoReceipt.PixelHash {
		t.Fatalf("overlap painted twice: one=%s/%s two=%s/%s", oneHash, oneReceipt.PixelHash, twoHash, twoReceipt.PixelHash)
	}
	if oneReceipt.PlacementCount != 1 || twoReceipt.PlacementCount != 2 ||
		oneReceipt.CoverageHash != twoReceipt.CoverageHash || twoReceipt.BindingHash == "" ||
		oneReceipt.ShapingProvenanceStatus != ShapingProvenanceStatusV1 ||
		oneReceipt.UniqueSegmentCount != 4 || twoReceipt.UniqueSegmentCount != 4 || twoReceipt.SegmentCount != 8 {
		t.Fatalf("one=%+v two=%+v", oneReceipt, twoReceipt)
	}
	receiptJSON, err := json.Marshal(oneReceipt)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/rectangle-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte(nil), receiptJSON...), '\n'); !bytes.Equal(fixture, want) {
		t.Fatalf("receipt fixture drift\ngot:  %s\nwant: %s", receiptJSON, bytes.TrimSpace(fixture))
	}
	rehashed, bytes, err := hashBinding(context.Background(), twoReceipt)
	if err != nil || rehashed != twoReceipt.BindingHash || bytes+coverageCanonicalBytes(twoReceipt)+pixelCanonicalBytes(twoReceipt) != twoReceipt.CanonicalBinaryBytes {
		t.Fatalf("binding hash=%s bytes=%d receipt=%+v err=%v", rehashed, bytes, twoReceipt, err)
	}
}

func TestRasterizeRejectsTamperedOutlineLineageAndFontSize(t *testing.T) {
	source, outline := rectangleFixture()
	layout := buildTestLayout(t, outline, 1, source)
	request := Request{Layout: layout, FontSize: documentv2.Rational{Numerator: 64, Denominator: 1}, Fill: color.RGBA16Input{A: 65535}}
	tampered := *source
	tampered.glyphs = cloneGlyphMap(source.glyphs)
	glyph := tampered.glyphs[1]
	glyph.commands[1].points[0].X++
	tampered.glyphs[1] = glyph
	if surface, receipt, err := rasterizeBound(context.Background(), &tampered, request, DefaultLimits()); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrBindingInvalid) {
		t.Fatalf("tampered outline surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	wrongDependency := *source
	wrongDependency.receiptValue.DependencyFingerprint = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if surface, receipt, err := rasterizeBound(context.Background(), &wrongDependency, request, DefaultLimits()); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrBindingInvalid) {
		t.Fatalf("tampered lineage surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	request.FontSize = documentv2.Rational{Numerator: 32, Denominator: 1}
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, DefaultLimits()); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrBindingInvalid) {
		t.Fatalf("tampered font size surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
}

func TestRasterCoverageUnionsOppositeWindingPlacements(t *testing.T) {
	clockwise := []rasterSegment{
		{kind: segmentLine, p0: rasterPoint{x: 2 * 64, y: 2 * 64}, p1: rasterPoint{x: 6 * 64, y: 2 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 6 * 64, y: 2 * 64}, p1: rasterPoint{x: 6 * 64, y: 6 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 6 * 64, y: 6 * 64}, p1: rasterPoint{x: 2 * 64, y: 6 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 2 * 64, y: 6 * 64}, p1: rasterPoint{x: 2 * 64, y: 2 * 64}},
	}
	counterclockwise := []rasterSegment{
		{kind: segmentLine, p0: rasterPoint{x: 2 * 64, y: 2 * 64}, p1: rasterPoint{x: 2 * 64, y: 6 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 2 * 64, y: 6 * 64}, p1: rasterPoint{x: 6 * 64, y: 6 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 6 * 64, y: 6 * 64}, p1: rasterPoint{x: 6 * 64, y: 2 * 64}},
		{kind: segmentLine, p0: rasterPoint{x: 6 * 64, y: 2 * 64}, p1: rasterPoint{x: 2 * 64, y: 2 * 64}},
	}
	measure := rasterMeasure{width: 8, height: 8, pixels: 64}
	one, err := rasterCoverage(context.Background(), exactContext{maxBits: DefaultMaxExactBits}, boundInput{
		measure: measure, placements: []placedGeometry{{glyphID: 1, segments: clockwise}},
	})
	if err != nil {
		t.Fatal(err)
	}
	union, err := rasterCoverage(context.Background(), exactContext{maxBits: DefaultMaxExactBits}, boundInput{
		measure: measure,
		placements: []placedGeometry{
			{glyphID: 1, segments: clockwise},
			{glyphID: 2, segments: counterclockwise},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, union) {
		t.Fatalf("opposite winding placements canceled or darkened: one=%v union=%v", one, union)
	}
}

func TestRasterizeRejectsSourceReceiptStatusAndLiveMetricDrift(t *testing.T) {
	source, outline := rectangleFixture()
	layout := buildTestLayout(t, outline, 1, source)
	request := Request{Layout: layout, FontSize: documentv2.Rational{Numerator: 64, Denominator: 1}, Fill: color.RGBA16Input{A: 65535}}
	tests := []struct {
		name   string
		mutate func(*fakeSource)
	}{
		{"binding_status", func(value *fakeSource) { value.receiptValue.BindingStatus = "" }},
		{"outline_profile", func(value *fakeSource) { value.receiptValue.OutlineProfile = "" }},
		{"command_status", func(value *fakeSource) { value.receiptValue.CommandStatus = "" }},
		{"rounding_status", func(value *fakeSource) { value.receiptValue.RoundingStatus = "" }},
		{"hinting_status", func(value *fakeSource) { value.receiptValue.HintingStatus = "" }},
		{"layout_status", func(value *fakeSource) { value.receiptValue.LayoutStatus = "" }},
		{"variable_layout_status", func(value *fakeSource) { value.receiptValue.VariableLayoutStatus = "" }},
		{"raster_status", func(value *fakeSource) { value.receiptValue.RasterStatus = "" }},
		{"capability_status", func(value *fakeSource) { value.receiptValue.CapabilityStatus = "" }},
		{"units_per_em", func(value *fakeSource) { value.receiptValue.UnitsPerEm++ }},
		{"hhea_ascender", func(value *fakeSource) { value.receiptValue.HheaAscender++ }},
		{"glyph_count", func(value *fakeSource) { value.receiptValue.GlyphCount++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := *source
			test.mutate(&mutated)
			surface, receipt, err := rasterizeBound(context.Background(), &mutated, request, DefaultLimits())
			if surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrBindingInvalid) {
				t.Fatalf("surface=%#v receipt=%+v err=%v", surface, receipt, err)
			}
		})
	}
}

func TestRasterizeExactWorkLimitCancellationAndFailureZero(t *testing.T) {
	source, outline := rectangleFixture()
	layout := buildTestLayout(t, outline, 1, source)
	request := Request{Layout: layout, FontSize: documentv2.Rational{Numerator: 64, Denominator: 1}, Fill: color.RGBA16Input{A: 65535}}
	_, baseline, err := rasterizeBound(context.Background(), source, request, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	exact := DefaultLimits()
	exact.MaxWorkUnits = baseline.WorkUnits
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, exact); err != nil || surface == nil || receipt.WorkUnits != baseline.WorkUnits {
		t.Fatalf("exact limit surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	exact.MaxWorkUnits--
	workLimited := *source
	workLimited.glyphCalls = 0
	if surface, receipt, err := rasterizeBound(context.Background(), &workLimited, request, exact); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) || workLimited.glyphCalls != 0 {
		t.Fatalf("N-1 surface=%#v receipt=%+v calls=%d err=%v", surface, receipt, workLimited.glyphCalls, err)
	}
	working := DefaultLimits()
	working.MaxWorkingBytes = baseline.WorkingBytes
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, working); err != nil || surface == nil || receipt.WorkingBytes != baseline.WorkingBytes {
		t.Fatalf("exact working limit surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	working.MaxWorkingBytes--
	workingLimited := *source
	workingLimited.glyphCalls = 0
	if surface, receipt, err := rasterizeBound(context.Background(), &workingLimited, request, working); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) || workingLimited.glyphCalls != 0 {
		t.Fatalf("working N-1 surface=%#v receipt=%+v calls=%d err=%v", surface, receipt, workingLimited.glyphCalls, err)
	}
	retained := DefaultLimits()
	retained.MaxRetainedBytes = baseline.RetainedBytes
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, retained); err != nil || surface == nil || receipt.RetainedBytes != baseline.RetainedBytes {
		t.Fatalf("exact retained limit surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	retained.MaxRetainedBytes--
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, retained); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("retained N-1 surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	canonicalAdmission := uint64(9*16*16 + 16<<10)
	canonical := DefaultLimits()
	canonical.MaxCanonicalBinaryBytes = canonicalAdmission
	if surface, _, err := rasterizeBound(context.Background(), source, request, canonical); err != nil || surface == nil {
		t.Fatalf("exact canonical admission surface=%#v err=%v", surface, err)
	}
	canonical.MaxCanonicalBinaryBytes--
	if surface, receipt, err := rasterizeBound(context.Background(), source, request, canonical); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("canonical admission N-1 surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	segmentLimited := *source
	segmentLimited.glyphCalls = 0
	segmentLimits := DefaultLimits()
	segmentLimits.MaxSegments = 3
	if surface, receipt, err := rasterizeBound(context.Background(), &segmentLimited, request, segmentLimits); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrResourceLimit) || segmentLimited.glyphCalls != 0 {
		t.Fatalf("segment pre-admission surface=%#v receipt=%+v calls=%d err=%v", surface, receipt, segmentLimited.glyphCalls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if surface, receipt, err := rasterizeBound(ctx, source, request, DefaultLimits()); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
	if surface, receipt, err := Rasterize(context.Background(), Request{}, DefaultLimits()); surface != nil || receipt != (Receipt{}) || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("nil public input surface=%#v receipt=%+v err=%v", surface, receipt, err)
	}
}

type cancelAfterChecks struct {
	context.Context
	allowed int
	checks  int
}

func (value *cancelAfterChecks) Err() error {
	value.checks++
	if value.checks > value.allowed {
		return context.Canceled
	}
	return nil
}

func TestBindingAndPlacementCancelInsideLargeSingleGlyph(t *testing.T) {
	source, _ := rectangleFixture()
	trustedCommands := make([]sourceCommand, 0, 1026)
	layoutCommands := make([]textlayout.OutlineCommand, 0, 1026)
	start := sfntglyf.OutlinePoint{X: 2 * 64, Y: 2 * 64}
	trustedCommands = append(trustedCommands, sourceCommand{op: sfntglyf.OutlineMoveTo, count: 1, points: [2]sfntglyf.OutlinePoint{start}})
	layoutCommands = append(layoutCommands, textlayout.OutlineCommand{Op: textlayout.MoveTo, Points: []textlayout.Point{{X: 2 * 64, Y: 2 * 64}}})
	for index := 0; index < 1024; index++ {
		point := sfntglyf.OutlinePoint{X: sfntglyf.Q26_6(2*64 + index%4), Y: 2 * 64}
		trustedCommands = append(trustedCommands, sourceCommand{op: sfntglyf.OutlineLineTo, count: 1, points: [2]sfntglyf.OutlinePoint{point}})
		layoutCommands = append(layoutCommands, textlayout.OutlineCommand{Op: textlayout.LineTo, Points: []textlayout.Point{{X: textlayout.Q26_6(point.X), Y: textlayout.Q26_6(point.Y)}}})
	}
	trustedCommands = append(trustedCommands, sourceCommand{op: sfntglyf.OutlineClose})
	layoutCommands = append(layoutCommands, textlayout.OutlineCommand{Op: textlayout.Close})
	layout := buildTestLayout(t, textlayout.GlyphOutline{GlyphID: 1, Commands: layoutCommands}, 1, source)
	view, err := layout.View()
	if err != nil {
		t.Fatal(err)
	}
	outline, err := view.Outline(1)
	if err != nil {
		t.Fatal(err)
	}
	bindCtx := &cancelAfterChecks{Context: context.Background(), allowed: 1}
	if geometry, err := bindGlyph(bindCtx, exactContext{maxBits: DefaultMaxExactBits}, sourceGlyph{
		id: 1, contours: 1, commands: trustedCommands,
	}, outline, documentv2.Rational{Numerator: 64, Denominator: 1}, 64, 0); geometry.id != 0 || geometry.contours != 0 || geometry.commands != 0 || geometry.segments != nil || !errors.Is(err, ErrCanceled) || bindCtx.checks < 2 {
		t.Fatalf("bind geometry=%+v checks=%d err=%v", geometry, bindCtx.checks, err)
	}
	segments := make([]rasterSegment, 2049)
	for index := range segments {
		segments[index] = rasterSegment{kind: segmentLine, p0: rasterPoint{x: int64(index)}, p1: rasterPoint{x: int64(index + 1)}}
	}
	placeCtx := &cancelAfterChecks{Context: context.Background(), allowed: 1}
	if placed, err := placeSegments(placeCtx, segments, textlayout.GlyphPlacement{}, 0); placed != nil || !errors.Is(err, ErrCanceled) || placeCtx.checks < 2 {
		t.Fatalf("placed=%v checks=%d err=%v", placed, placeCtx.checks, err)
	}
}

func rectangleFixture() (*fakeSource, textlayout.GlyphOutline) {
	commands := []sourceCommand{
		{op: sfntglyf.OutlineMoveTo, count: 1, points: [2]sfntglyf.OutlinePoint{{X: 2 * 64, Y: 0}}},
		{op: sfntglyf.OutlineLineTo, count: 1, points: [2]sfntglyf.OutlinePoint{{X: 6 * 64, Y: 0}}},
		{op: sfntglyf.OutlineLineTo, count: 1, points: [2]sfntglyf.OutlinePoint{{X: 6 * 64, Y: 4 * 64}}},
		{op: sfntglyf.OutlineLineTo, count: 1, points: [2]sfntglyf.OutlinePoint{{X: 2 * 64, Y: 4 * 64}}},
		{op: sfntglyf.OutlineClose},
	}
	provider := sfntglyf.ShapeProviderDescriptor()
	source := &fakeSource{
		units: 64, ascender: 8, glyphCountLive: 2,
		glyphs: map[uint32]sourceGlyph{1: {id: 1, contours: 1, commands: commands}},
		receiptValue: sfntglyf.LayoutSourceReceipt{
			SchemaVersion: sfntglyf.LayoutSourceSchemaVersion, Profile: sfntglyf.LayoutSourceReceiptProfileV1,
			LayoutSourceProfile:   sfntglyf.LayoutSourceProfileV1,
			DescriptorHashProfile: sfntglyf.LayoutSourceDescriptorHashProfileV1,
			DescriptorHash:        sfntglyf.LayoutSourceDescriptorHash(), LayoutSourceHashProfile: sfntglyf.LayoutSourceHashProfileV1,
			LayoutSourceHash: testDigest, ShapeProviderProfile: sfntglyf.ShapeProviderProfileV1,
			ShapeProviderHashProfile: sfntglyf.ShapeProviderHashProfileV1, ShapeProviderHash: testDigest,
			DependencyFingerprintProfile: provider.DependencyFingerprintProfile,
			DependencyFingerprint:        provider.DependencyFingerprint,
			LayoutGraphProfile:           sfntglyf.LayoutGraphReceiptProfileV1,
			LayoutGraphHashProfile:       sfntglyf.LayoutGraphCanonicalHashProfileV1, LayoutGraphHash: testDigest,
			OutlineSetProfile:     sfntglyf.OutlineSetReceiptProfileV1,
			OutlineSetHashProfile: sfntglyf.OutlineSetHashProfileV1, OutlineSetHash: testDigest,
			FontReceiptProfile:     sfntglyf.ReceiptProfileV1,
			FontContentHashProfile: sfntglyf.ContentHashProfileV1, FontContentHash: testDigest,
			BindingStatus:        sfntglyf.LayoutSourceBindingStatusV1,
			OutlineProfile:       sfntglyf.OutlineSetOutlineProfileV1,
			CommandStatus:        sfntglyf.OutlineSetCommandStatusV1,
			RoundingStatus:       sfntglyf.OutlineSetRoundingStatusV1,
			HintingStatus:        sfntglyf.OutlineSetHintingStatusV1,
			LayoutStatus:         sfntglyf.LayoutSourceLayoutStatusV1,
			VariableLayoutStatus: sfntglyf.LayoutGraphVariableLayoutStatusV1,
			RasterStatus:         sfntglyf.LayoutSourceRasterStatusV1,
			CapabilityStatus:     sfntglyf.LayoutSourceCapabilityStatusV1,
			UnitsPerEm:           64, HheaAscender: 8, GlyphCount: 2,
		},
	}
	outline := textlayout.GlyphOutline{GlyphID: 1, Commands: []textlayout.OutlineCommand{
		{Op: textlayout.MoveTo, Points: []textlayout.Point{{X: 2 * 64, Y: 0}}},
		{Op: textlayout.LineTo, Points: []textlayout.Point{{X: 6 * 64, Y: 0}}},
		{Op: textlayout.LineTo, Points: []textlayout.Point{{X: 6 * 64, Y: 4 * 64}}},
		{Op: textlayout.LineTo, Points: []textlayout.Point{{X: 2 * 64, Y: 4 * 64}}},
		{Op: textlayout.Close},
	}}
	return source, outline
}

func buildTestLayout(t *testing.T, outline textlayout.GlyphOutline, glyphCount int, source *fakeSource) *textlayout.Layout {
	t.Helper()
	glyphs := make([]textlayout.ShapedGlyph, glyphCount)
	for index := range glyphs {
		glyphs[index] = textlayout.ShapedGlyph{GlyphID: 1, ClusterByteOffset: 0}
	}
	input := textlayout.Input{
		TextProfile:         documentv2.TextProfilePinnedFontViewportPXV2,
		RunInputProfile:     documentv2.TextRunInputProfileV1,
		ShapedStreamProfile: textlayout.NormalizedShapedStreamProfileV1,
		LayoutProfile:       textlayout.ProfileV1, FontProfile: documentv2.TextFontProfileStaticGlyfV1,
		OutlineProfile: documentv2.TextOutlineProfileQ26_6V1, Text: "A",
		Viewport:   documentv2.RasterViewport{WidthPixels: 16, HeightPixels: 16},
		FontSize:   documentv2.Rational{Numerator: 64, Denominator: 1},
		LineHeight: documentv2.Rational{Numerator: 10, Denominator: 1},
		Language:   "en", Script: documentv2.TextScriptLatin, Direction: documentv2.TextDirectionLTR,
		Alignment: documentv2.TextAlignmentStart, UnitsPerEm: 64, HheaAscender: 8,
		Lines:    []textlayout.ShapedLine{{ByteStart: 0, ByteEnd: 1, Glyphs: glyphs}},
		Outlines: []textlayout.GlyphOutline{outline},
		Binding: textlayout.Binding{
			TextSemanticHashProfile: documentv2.TextSemanticHashProfileV1, TextSemanticHash: testDigest,
			LayoutInputHashProfile: documentv2.TextLayoutInputHashProfileV1, LayoutInputHash: testDigest,
			FontContentHash: testDigest, UnicodeTableProfile: documentv2.TextUnicodeValidationTableProfileV1,
			UnicodeTableHashProfile:  documentv2.TextUnicodeValidationTableHashProfileV1,
			UnicodeTableHash:         documentv2.TextUnicodeValidationTableHashV1,
			ShapeProviderProfile:     sfntglyf.ShapeProviderProfileV1,
			ShapeProviderFingerprint: source.receiptValue.DependencyFingerprint,
		},
	}
	layout, _, err := textlayout.Build(context.Background(), input, textlayout.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

func requestWithLayout(request Request, layout *textlayout.Layout) Request {
	request.Layout = layout
	return request
}

func cloneGlyphMap(source map[uint32]sourceGlyph) map[uint32]sourceGlyph {
	result := make(map[uint32]sourceGlyph, len(source))
	for glyphID, glyph := range source {
		glyph.commands = append([]sourceCommand(nil), glyph.commands...)
		result[glyphID] = glyph
	}
	return result
}

func coverageCanonicalBytes(receipt Receipt) uint64 {
	return 8 + uint64(len(CoverageHashProfileV1)) + 4 + 4 + 8 + uint64(receipt.WidthPixels)*uint64(receipt.HeightPixels)
}

func pixelCanonicalBytes(receipt Receipt) uint64 {
	return uint64(receipt.WidthPixels) * uint64(receipt.HeightPixels) * 8
}
