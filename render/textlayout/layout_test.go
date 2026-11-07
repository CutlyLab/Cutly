package textlayout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"unsafe"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestBuildLTRPlacementScalingAndYUpMapping(t *testing.T) {
	input := testInput()
	layout, receipt := buildTestLayout(t, input, Limits{})
	if got := layout.FontSize(); got != input.FontSize {
		t.Fatalf("font size=%+v want %+v", got, input.FontSize)
	}
	placements := placementsForTest(t, layout)
	want := []GlyphPlacement{
		{LineIndex: 0, GlyphIndex: 0, GlyphID: 10, ClusterByteOffset: 0, OriginX: -64, BaselineY: 3008},
		{LineIndex: 0, GlyphIndex: 1, GlyphID: 20, ClusterByteOffset: 1, OriginX: 672, BaselineY: 3104},
		{LineIndex: 1, GlyphIndex: 0, GlyphID: 30, ClusterByteOffset: 4, OriginX: 0, BaselineY: 4352},
	}
	if len(placements) != len(want) {
		t.Fatalf("placements=%+v", placements)
	}
	for index := range want {
		if placements[index] != want[index] {
			t.Fatalf("placement[%d]=%+v want %+v", index, placements[index], want[index])
		}
	}
	if receipt.LineCount != 2 || receipt.GlyphCount != 3 || receipt.UniqueGlyphCount != 3 ||
		receipt.ContourCount != 2 || receipt.CommandCount != 7 {
		t.Fatalf("receipt counts=%+v", receipt)
	}
	view, err := layout.View()
	if err != nil || view.Viewport() != input.Viewport || view.OutlineCount() != 3 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	outline, err := view.Outline(10)
	if err != nil || outline.CommandCount() != 4 {
		t.Fatalf("outline=%+v err=%v", outline, err)
	}
	point, err := outline.Point(1, 0)
	if err != nil || point != (Point{X: 640, Y: 0}) {
		t.Fatalf("scaled point=%+v err=%v", point, err)
	}
}

func TestLogicalAlignmentLTRAndRTL(t *testing.T) {
	tests := []struct {
		name       string
		input      Input
		wantFirst  Q26_6
		wantSecond Q26_6
	}{
		{name: "ltr start", input: testInput(), wantFirst: -64, wantSecond: 672},
		{name: "ltr center", input: func() Input { value := testInput(); value.Alignment = documentv2.TextAlignmentCenter; return value }(), wantFirst: 2656, wantSecond: 3392},
		{name: "ltr end", input: func() Input { value := testInput(); value.Alignment = documentv2.TextAlignmentEnd; return value }(), wantFirst: 5376, wantSecond: 6112},
		{name: "rtl start", input: rtlInput(documentv2.TextAlignmentStart), wantFirst: 5760, wantSecond: 5440},
		{name: "rtl center", input: rtlInput(documentv2.TextAlignmentCenter), wantFirst: 3040, wantSecond: 2720},
		{name: "rtl end", input: rtlInput(documentv2.TextAlignmentEnd), wantFirst: 320, wantSecond: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout, _ := buildTestLayout(t, test.input, Limits{})
			placements := placementsForTest(t, layout)
			if placements[0].OriginX != test.wantFirst || placements[1].OriginX != test.wantSecond {
				t.Fatalf("origins=(%d,%d) want=(%d,%d)", placements[0].OriginX, placements[1].OriginX, test.wantFirst, test.wantSecond)
			}
		})
	}
}

func TestRTLProviderNormalizationPreservesRawHarfBuzzOrigins(t *testing.T) {
	// A representative raw provider stream for logical "AB" is [B,A] with positive advances.
	// Raw start is W-total=5440. B is at 5440+40=5480; A is at 5760-20=5740.
	// Reversal plus advance/offset normalization must preserve those two physical origins.
	input := rtlInput(documentv2.TextAlignmentStart)
	input.Lines[0].Glyphs[0].XOffset = -20 - 640 // raw A offset - raw A advance
	input.Lines[0].Glyphs[1].XOffset = 40 - 320  // raw B offset - raw B advance
	layout, _ := buildTestLayout(t, input, Limits{})
	placements := placementsForTest(t, layout)
	if placements[0].GlyphID != 10 || placements[0].ClusterByteOffset != 0 || placements[0].OriginX != 5740 ||
		placements[1].GlyphID != 20 || placements[1].ClusterByteOffset != 1 || placements[1].OriginX != 5480 {
		t.Fatalf("normalized RTL placements=%+v", placements)
	}
}

func TestRTLNormalizationUsesIndependentRoundingAtNonUnitScale(t *testing.T) {
	// Raw font-space records [B,A] use advances [3,5] and offsets [1,2]. Normalization
	// produces logical [A,B] records with advances [-5,-3] and offsets [-3,-2]. At a 1/6
	// viewport scale, each complete normalized value is rounded independently: [-1,0] advances
	// and [0,0] offsets. No post-scale equality with a separately rounded raw path is implied.
	input := rtlInput(documentv2.TextAlignmentStart)
	input.Viewport = documentv2.RasterViewport{WidthPixels: 10, HeightPixels: 10}
	input.FontSize = documentv2.Rational{Numerator: 8, Denominator: 3}
	input.UnitsPerEm = 16
	input.HheaAscender = 0
	input.Lines[0].Glyphs[0].XAdvance = -5
	input.Lines[0].Glyphs[0].XOffset = -3
	input.Lines[0].Glyphs[1].XAdvance = -3
	input.Lines[0].Glyphs[1].XOffset = -2
	layout, _ := buildTestLayout(t, input, Limits{})
	placements := placementsForTest(t, layout)
	if placements[0].OriginX != 640 || placements[1].OriginX != 639 {
		t.Fatalf("non-unit RTL placements=%+v", placements)
	}
}

func TestCenterAlignmentRoundsSignedHalfToEvenDirectly(t *testing.T) {
	ltr := oneGlyphInput("A")
	ltr.Alignment = documentv2.TextAlignmentCenter
	ltr.Lines[0].Glyphs[0].XAdvance = 1
	layout, _ := buildTestLayout(t, ltr, Limits{})
	if got := placementsForTest(t, layout)[0].OriginX; got != 3200 {
		t.Fatalf("LTR center tie=%d want 3200", got)
	}

	rtl := rtlInput(documentv2.TextAlignmentCenter)
	rtl.Text = "A"
	rtl.Lines = []ShapedLine{{ByteStart: 0, ByteEnd: 1, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 0, XAdvance: -1}}}}
	rtl.Outlines = rtl.Outlines[:1]
	layout, _ = buildTestLayout(t, rtl, Limits{})
	if got := placementsForTest(t, layout)[0].OriginX; got != 3200 {
		t.Fatalf("RTL center tie=%d want 3200", got)
	}
}

func TestCenterAlignmentExactBitBoundaryChecksPreRoundNumerator(t *testing.T) {
	if got, err := (exactContext{maxBits: 6}).halfEven(63, "center"); err != nil || got != 32 {
		t.Fatalf("halfEven maxBits=6 got=%d err=%v want 32", got, err)
	}
	if _, err := (exactContext{maxBits: 5}).halfEven(63, "center"); !errors.Is(err, ErrExactLimit) {
		t.Fatalf("halfEven maxBits=5 error=%v want ErrExactLimit", err)
	}

	input := oneGlyphInput("A")
	input.Viewport = documentv2.RasterViewport{WidthPixels: 1, HeightPixels: 1}
	input.FontSize = documentv2.Rational{Numerator: 1, Denominator: 1}
	input.LineHeight = documentv2.Rational{Numerator: 1, Denominator: 1}
	input.UnitsPerEm = 16
	input.HheaAscender = 0
	input.Alignment = documentv2.TextAlignmentCenter
	input.Lines[0].Glyphs[0].XAdvance = 16
	input.Outlines[0].Commands = nil

	limits := DefaultLimits()
	limits.MaxExactBits = 6
	layout, _ := buildTestLayout(t, input, limits)
	if got := placementsForTest(t, layout)[0].OriginX; got != 32 {
		t.Fatalf("center origin=%d want 32", got)
	}

	limits.MaxExactBits = 5
	layout, receipt, err := Build(context.Background(), input, limits)
	if layout != nil || receipt != (Receipt{}) || !errors.Is(err, ErrExactLimit) {
		t.Fatalf("below exact boundary layout=%v receipt=%+v err=%v", layout, receipt, err)
	}
}

func TestBaselineExactLimitRejectsLargeTermsThatCancel(t *testing.T) {
	_, err := (exactContext{maxBits: 5}).baseline(
		-32,
		documentv2.Rational{Numerator: 1, Denominator: 1},
		documentv2.Rational{Numerator: 2, Denominator: 1},
		16,
		1,
		"baseline",
	)
	if !errors.Is(err, ErrExactLimit) {
		t.Fatalf("error=%v want ErrExactLimit", err)
	}
}

func TestMixedSignedGlyphAdvancesUseOnlyScaledLineTotalSign(t *testing.T) {
	ltr := testInput()
	ltr.Text = "AB"
	ltr.Lines = []ShapedLine{{ByteStart: 0, ByteEnd: 2, Glyphs: []ShapedGlyph{
		{GlyphID: 10, ClusterByteOffset: 0, XAdvance: -64},
		{GlyphID: 20, ClusterByteOffset: 1, XAdvance: 128},
	}}}
	ltr.Outlines = ltr.Outlines[:2]
	layout, _ := buildTestLayout(t, ltr, Limits{})
	placements := placementsForTest(t, layout)
	if placements[0].OriginX != 0 || placements[1].OriginX != -64 {
		t.Fatalf("LTR mixed advances placements=%+v", placements)
	}

	rtl := rtlInput(documentv2.TextAlignmentStart)
	rtl.Lines[0].Glyphs[0].XAdvance = 64
	rtl.Lines[0].Glyphs[1].XAdvance = -128
	rtl.Lines[0].Glyphs[0].XOffset = 0
	rtl.Lines[0].Glyphs[1].XOffset = 0
	layout, _ = buildTestLayout(t, rtl, Limits{})
	placements = placementsForTest(t, layout)
	if placements[0].OriginX != 6400 || placements[1].OriginX != 6464 {
		t.Fatalf("RTL mixed advances placements=%+v", placements)
	}
}

func TestSignedAscenderAllowsZeroAndNegativeBaselines(t *testing.T) {
	for _, ascender := range []int32{0, -16, -32768} {
		input := oneGlyphInput("A")
		input.HheaAscender = ascender
		layout, _ := buildTestLayout(t, input, Limits{})
		got := placementsForTest(t, layout)[0].BaselineY
		want := Q26_6(int64(ascender) * 64)
		if got != want {
			t.Fatalf("ascender=%d baseline=%d want %d", ascender, got, want)
		}
	}
}

func TestBaselinesAreComputedDirectlyNotAccumulated(t *testing.T) {
	input := oneGlyphInput("A")
	input.Text = "A\nA\nA"
	input.Viewport = documentv2.RasterViewport{WidthPixels: 8, HeightPixels: 8}
	input.FontSize = documentv2.Rational{Numerator: 16, Denominator: 1}
	input.LineHeight = documentv2.Rational{Numerator: 1, Denominator: 128}
	input.UnitsPerEm = 16
	input.HheaAscender = 1
	input.Lines = []ShapedLine{
		{ByteStart: 0, ByteEnd: 1, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 0, XAdvance: 16}}},
		{ByteStart: 2, ByteEnd: 3, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 2, XAdvance: 16}}},
		{ByteStart: 4, ByteEnd: 5, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 4, XAdvance: 16}}},
	}
	layout, _ := buildTestLayout(t, input, Limits{})
	placements := placementsForTest(t, layout)
	want := []Q26_6{64, 64, 65}
	for index := range want {
		if placements[index].BaselineY != want[index] {
			t.Fatalf("baseline[%d]=%d want %d", index, placements[index].BaselineY, want[index])
		}
	}
}

func TestLeadingTrailingAndConsecutiveEmptyLines(t *testing.T) {
	input := oneGlyphInput("A")
	input.Text = "\nA\n\n"
	input.Lines = []ShapedLine{
		{ByteStart: 0, ByteEnd: 0},
		{ByteStart: 1, ByteEnd: 2, Glyphs: []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: 1, XAdvance: 640}}},
		{ByteStart: 3, ByteEnd: 3},
		{ByteStart: 4, ByteEnd: 4},
	}
	layout, receipt := buildTestLayout(t, input, Limits{})
	if receipt.LineCount != 4 || len(placementsForTest(t, layout)) != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestBuildOwnsPublishedDataAndReadViewsAreDefensive(t *testing.T) {
	input := testInput()
	layout, receipt := buildTestLayout(t, input, Limits{})
	for _, pair := range [][2]string{
		{input.Binding.TextSemanticHash, receipt.TextSemanticHash},
		{input.Binding.LayoutInputHash, receipt.LayoutInputHash},
		{input.Binding.FontContentHash, receipt.FontContentHash},
		{input.Binding.ShapeProviderProfile, receipt.ShapeProviderProfile},
		{input.Binding.ShapeProviderFingerprint, receipt.ShapeProviderFingerprint},
	} {
		if unsafe.StringData(pair[0]) == unsafe.StringData(pair[1]) {
			t.Fatalf("receipt retained caller string backing for %q", pair[0])
		}
	}
	if receipt.LayoutProfile != ProfileV1 || receipt.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		receipt.ShapedStreamProfile != NormalizedShapedStreamProfileV1 {
		t.Fatalf("receipt did not rebuild fixed profiles: %+v", receipt)
	}
	input.Lines[0].Glyphs[0].GlyphID = 999
	input.Outlines[0].GlyphID = 999
	input.Outlines[0].Commands[1].Points[0] = Point{X: 999, Y: 999}

	placements := placementsForTest(t, layout)
	if placements[0].GlyphID != 10 || layout.Receipt() != receipt {
		t.Fatalf("published data changed: placements=%+v receipt=%+v", placements, layout.Receipt())
	}
	view, _ := layout.View()
	outline, err := view.Outline(10)
	if err != nil {
		t.Fatal(err)
	}
	point, err := outline.Point(1, 0)
	if err != nil || point != (Point{X: 640}) {
		t.Fatalf("point=%+v err=%v", point, err)
	}
	point.X = -1
	again, _ := outline.Point(1, 0)
	if again.X != 640 {
		t.Fatalf("point view aliased: %+v", again)
	}
}

func TestLayoutSupportsConcurrentReadViews(t *testing.T) {
	layout, receipt := buildTestLayout(t, testInput(), Limits{})
	const readers = 16
	var wait sync.WaitGroup
	wait.Add(readers)
	for reader := 0; reader < readers; reader++ {
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1000; iteration++ {
				view, err := layout.View()
				if err != nil || view.PlacementCount() != 3 || layout.Receipt().LayoutHash != receipt.LayoutHash {
					t.Errorf("read failed: view=%+v err=%v", view, err)
					return
				}
				if _, err := view.Placement(iteration % 3); err != nil {
					t.Errorf("placement: %v", err)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestFailureAlwaysReturnsNilLayoutAndZeroReceipt(t *testing.T) {
	tests := []struct {
		name   string
		ctx    context.Context
		input  Input
		limits Limits
		want   error
	}{
		{name: "nil context", input: testInput(), want: ErrContextInvalid},
		{name: "bad limits", ctx: context.Background(), input: testInput(), limits: Limits{MaxWidth: 1}, want: ErrLimitsInvalid},
		{name: "bad input", ctx: context.Background(), input: func() Input { value := testInput(); value.Lines[0].Glyphs[0].GlyphID = 0; return value }(), want: ErrInputInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout, receipt, err := Build(test.ctx, test.input, test.limits)
			if layout != nil || receipt != (Receipt{}) || !errors.Is(err, test.want) {
				t.Fatalf("layout=%v receipt=%+v err=%v", layout, receipt, err)
			}
		})
	}
}
