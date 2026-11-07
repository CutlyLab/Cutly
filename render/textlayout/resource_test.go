package textlayout

import (
	"context"
	"errors"
	"testing"
	"unsafe"
)

func TestLogicalRecordBudgetsCoverRetainedGoRecords(t *testing.T) {
	tests := []struct {
		name   string
		actual uintptr
		budget uint64
	}{
		{name: "placement", actual: unsafe.Sizeof(GlyphPlacement{}), budget: logicalPlacementBytes},
		{name: "command", actual: unsafe.Sizeof(layoutCommand{}), budget: logicalCommandBytes},
		{name: "outline", actual: unsafe.Sizeof(layoutOutline{}), budget: logicalOutlineBytes},
		{name: "layout", actual: unsafe.Sizeof(Layout{}), budget: logicalLayoutRecordBytes},
		{name: "reference flag", actual: unsafe.Sizeof(false), budget: logicalReferenceFlagBytes},
	}
	for _, test := range tests {
		if uint64(test.actual) > test.budget {
			t.Fatalf("%s record size=%d exceeds logical budget=%d", test.name, test.actual, test.budget)
		}
	}
}

func TestLineGeometryChecksCancellationInsideLongGlyphLoop(t *testing.T) {
	input := oneGlyphInput("A")
	input.Lines[0].Glyphs = make([]ShapedGlyph, 4097)
	for index := range input.Lines[0].Glyphs {
		input.Lines[0].Glyphs[index] = ShapedGlyph{GlyphID: 10, ClusterByteOffset: 0}
	}
	_, err := computeLineGeometry(
		&cancelAfterContext{remaining: 1},
		exactContext{maxBits: DefaultMaxExactBits},
		input,
		input.Lines[0],
		0,
	)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestMaterializationChecksCancellationInsideLongCommandLoop(t *testing.T) {
	input := oneGlyphInput("A")
	commands := make([]OutlineCommand, 2050)
	commands[0] = OutlineCommand{Op: MoveTo, Points: []Point{{}}}
	for index := 1; index < len(commands)-1; index++ {
		commands[index] = OutlineCommand{Op: LineTo, Points: []Point{{X: Q26_6(index)}}}
	}
	commands[len(commands)-1] = OutlineCommand{Op: Close}
	input.Outlines[0].Commands = commands
	limits := DefaultLimits()
	measure, err := validateAndMeasure(context.Background(), input, limits)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := materializeLayout(
		&cancelAfterContext{remaining: 2},
		exactContext{maxBits: limits.MaxExactBits},
		input,
		measure,
		Receipt{},
	)
	if layout != nil || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("layout=%v err=%v", layout, err)
	}
}

func TestOutlineReferenceTailScanChecksCancellation(t *testing.T) {
	const count = 2049
	input := Input{Lines: []ShapedLine{{Glyphs: make([]ShapedGlyph, count)}}, Outlines: make([]GlyphOutline, count)}
	for index := 0; index < count; index++ {
		glyphID := uint32(index + 1)
		input.Lines[0].Glyphs[index].GlyphID = glyphID
		input.Outlines[index].GlyphID = glyphID
	}
	err := validateOutlineClosure(&cancelAfterContext{remaining: 4}, input)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
