package sfntglyf

import (
	"context"
	"errors"
	"testing"
)

func TestOutlineSetRejectsMutatedSourceBindings(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, onCurve: false}, {x: 2, onCurve: false}}, nil, false, 0)
	base := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1}, geometryTestBBox{xMax: 2}, 0, 498, 2)
	tests := []struct {
		name   string
		mutate func(*GeometryFont)
	}{
		{name: "receipt profile", mutate: func(value *GeometryFont) { value.receipt.Profile += ".mutated" }},
		{name: "geometry hash", mutate: func(value *GeometryFont) { value.receipt.GeometryHash = "sha256:" + string(make([]byte, 64)) }},
		{name: "work", mutate: func(value *GeometryFont) { value.receipt.WorkUnits++ }},
		{name: "working", mutate: func(value *GeometryFont) { value.receipt.WorkingBytes++ }},
		{name: "retained", mutate: func(value *GeometryFont) { value.receipt.RetainedBytes++ }},
		{name: "glyph bbox", mutate: func(value *GeometryFont) { value.glyphs[1].xMax++ }},
		{name: "raw point", mutate: func(value *GeometryFont) { value.source.glyphs[1].points[0].x++ }},
		{name: "raw flag", mutate: func(value *GeometryFont) { value.source.glyphs[1].points[0].onCurve = true }},
		{name: "graph hash", mutate: func(value *GeometryFont) { value.source.receipt.GraphHash += "0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneOutlineSetGeometry(base)
			test.mutate(value)
			set, receipt, err := MaterializeOutlineSet(context.Background(), value, OutlineSetLimits{})
			if err == nil || set != nil || receipt != (OutlineSetReceipt{}) {
				t.Fatalf("set=%v receipt=%+v err=%v", set, receipt, err)
			}
			if !errors.Is(err, ErrOutlineSetInputInvalid) && !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("unexpected error classification: %v", err)
			}
		})
	}
}

func TestOutlineSetCancellationIsAtomic(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{3}, []outlineTestPoint{
		{x: 0, onCurve: false}, {x: 2, onCurve: false}, {x: 3, onCurve: true}, {x: 4, onCurve: true},
	}, nil, false, 0)
	geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 4, contours: 1}, geometryTestBBox{xMax: 4}, 0, 496, 4)
	probe := &geometryCountingContext{}
	if _, _, err := MaterializeOutlineSet(probe, geometry, OutlineSetLimits{}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 20 {
		t.Fatalf("too few cancellation checkpoints: %d", probe.calls)
	}
	for cancelAt := 1; cancelAt <= probe.calls; cancelAt++ {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		set, receipt, err := MaterializeOutlineSet(ctx, geometry, OutlineSetLimits{})
		if !errors.Is(err, ErrCanceled) || set != nil || receipt != (OutlineSetReceipt{}) {
			t.Fatalf("cancel=%d/%d set=%v receipt=%+v err=%v", cancelAt, probe.calls, set, receipt, err)
		}
	}
}

func TestOutlineSetGeometryReplayRejectsReboundMutatedGraph(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, onCurve: false}, {x: 2, onCurve: false}}, nil, false, 0)
	base := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1}, geometryTestBBox{xMax: 2}, 0, 498, 2)
	mutated := cloneOutlineSetGeometry(base)
	mutated.source.glyphs[1].points[0].onCurve = true
	graphHash, canonicalBytes := oracleOutlineGraphHash(mutated.source.font, mutated.source)
	if canonicalBytes != mutated.source.receipt.CanonicalBinaryBytes {
		t.Fatal("fixed-width graph canonical size drift")
	}
	mutated.source.receipt.GraphHash = graphHash
	mutated.receipt.SourceGraphHash = graphHash
	mutated.receipt.SourceCanonicalBinaryBytes = canonicalBytes
	set, receipt, err := MaterializeOutlineSet(context.Background(), mutated, OutlineSetLimits{})
	if !errors.Is(err, ErrOutlineSetInputInvalid) || set != nil || receipt != (OutlineSetReceipt{}) {
		t.Fatalf("set=%v receipt=%+v err=%v", set, receipt, err)
	}
}

func cloneOutlineSetGeometry(source *GeometryFont) *GeometryFont {
	copyGeometry := *source
	copyGeometry.glyphs = append([]geometryGlyph(nil), source.glyphs...)
	copyOutline := *source.source
	copyOutline.glyphs = append([]outlineGlyph(nil), source.source.glyphs...)
	for index := range copyOutline.glyphs {
		original := source.source.glyphs[index]
		copyOutline.glyphs[index].points = append([]outlineRawPoint(nil), original.points...)
		copyOutline.glyphs[index].endPoints = append([]uint32(nil), original.endPoints...)
		copyOutline.glyphs[index].components = append([]outlineComponent(nil), original.components...)
		copyOutline.glyphs[index].instructions = append([]byte(nil), original.instructions...)
	}
	copyGeometry.source = &copyOutline
	return &copyGeometry
}
