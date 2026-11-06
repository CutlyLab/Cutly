package sfntglyf

import (
	"context"
	"errors"
	"testing"
)

func TestOutlineSetResourceLimitsExactNAndNMinusOne(t *testing.T) {
	child := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, onCurve: false}, {x: 2, onCurve: false}}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1}, {flags: componentArgsXY, glyphID: 1},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{xMax: 2})
	geometry := outlineSetTestGeometry(t, [][]byte{nil, child, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
	}, geometryTestBBox{xMax: 2}, 0, 498, 2)
	_, baseline, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		value  uint64
		limits func(uint64) OutlineSetLimits
	}{
		{"expanded points", baseline.ExpandedPointInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExpandedPointInstances: n} }},
		{"expanded contours", baseline.ExpandedContourInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExpandedContourInstances: n} }},
		{"expanded edges", baseline.ExpandedEdgeVisits, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExpandedEdgeVisits: n} }},
		{"expanded glyphs", baseline.ExpandedGlyphInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExpandedGlyphInstances: n} }},
		{"transform visits", baseline.TransformPointVisits, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxTransformPointVisits: n} }},
		{"implied", baseline.ImpliedPointInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxImpliedPointInstances: n} }},
		{"drawing", baseline.DrawingCommandInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxDrawingCommandInstances: n} }},
		{"commands", baseline.CommandInstances, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxCommandInstances: n} }},
		{"command points", baseline.CommandPointOccurrences, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxCommandPointOccurrences: n} }},
		{"commands per glyph", baseline.MaxCommandsPerGlyph, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxCommandsPerGlyph: n} }},
		{"points per glyph", baseline.MaxCommandPointOccurrencesPerGlyph, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxCommandPointOccurrencesPerGlyph: n} }},
		{"exact ops", baseline.ExactOpCount, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExactOps: n} }},
		{"exact limbs", baseline.ExactLimbUnits, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxExactLimbUnits: n} }},
		{"canonical", baseline.CanonicalBinaryBytes, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxCanonicalBinaryBytes: n} }},
		{"work", baseline.WorkUnits, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxWorkUnits: n} }},
		{"working", baseline.WorkingBytes, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxWorkingBytes: n} }},
		{"retained", baseline.RetainedBytes, func(n uint64) OutlineSetLimits { return OutlineSetLimits{MaxRetainedBytes: n} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.value <= 1 {
				t.Fatalf("test requires nonzero N-1: %d", test.value)
			}
			if set, _, err := MaterializeOutlineSet(context.Background(), geometry, test.limits(test.value)); err != nil || set == nil {
				t.Fatalf("N=%d failed: %v", test.value, err)
			}
			set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, test.limits(test.value-1))
			if !errors.Is(err, ErrResourceLimit) || set != nil || receipt != (OutlineSetReceipt{}) {
				t.Fatalf("N-1=%d set=%v receipt=%+v err=%v", test.value-1, set, receipt, err)
			}
		})
	}
	bits := uint64(baseline.MaxObservedExactBits)
	if bits <= 1 {
		t.Fatalf("exact bits=%d", bits)
	}
	if set, _, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{MaxExactBits: uint32(bits)}); err != nil || set == nil {
		t.Fatalf("exact bits N failed: %v", err)
	}
	set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{MaxExactBits: uint32(bits - 1)})
	if !errors.Is(err, ErrResourceLimit) || set != nil || receipt != (OutlineSetReceipt{}) {
		t.Fatalf("exact bits N-1 set=%v receipt=%+v err=%v", set, receipt, err)
	}
}
