package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestOutlineSetHashIndependentCanonicalOracle(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: -1, y: 1, onCurve: false}, {x: 2, y: -2, onCurve: false}}, nil, false, 0)
	geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1},
		geometryTestBBox{-1, -2, 2, 1}, 0, 497, 3)
	set, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	writeString := func(text string) {
		_ = binary.Write(&stream, binary.BigEndian, uint64(len(text)))
		_, _ = stream.WriteString(text)
	}
	writeString(OutlineSetHashProfileV1)
	_ = binary.Write(&stream, binary.BigEndian, uint32(OutlineSetProfileSchemaVersion))
	writeString(OutlineSetProfileV1)
	writeString(OutlineSetOutlineProfileV1)
	writeString(OutlineSetDescriptorHash())
	writeString(geometry.receipt.Profile)
	writeString(geometry.receipt.DescriptorHashProfile)
	writeString(geometry.receipt.DescriptorHash)
	writeString(geometry.receipt.GeometryHashProfile)
	writeString(geometry.receipt.GeometryHash)
	_ = binary.Write(&stream, binary.BigEndian, geometry.receipt.CanonicalBinaryBytes)
	writeString(geometry.receipt.SourceGraphHashProfile)
	writeString(geometry.receipt.SourceGraphHash)
	_ = binary.Write(&stream, binary.BigEndian, geometry.receipt.SourceCanonicalBinaryBytes)
	writeString(geometry.receipt.UpstreamContentHashProfile)
	writeString(geometry.receipt.UpstreamContentHash)
	_ = binary.Write(&stream, binary.BigEndian, set.GlyphCount())
	for glyphID := uint32(0); glyphID < set.GlyphCount(); glyphID++ {
		view, ok := set.Glyph(glyphID)
		if !ok {
			t.Fatal("missing glyph")
		}
		_ = binary.Write(&stream, binary.BigEndian, glyphID)
		_ = binary.Write(&stream, binary.BigEndian, view.ContourCount())
		_ = binary.Write(&stream, binary.BigEndian, view.CommandCount())
		for commandIndex := uint64(0); commandIndex < view.CommandCount(); commandIndex++ {
			command, ok := view.Command(commandIndex)
			if !ok {
				t.Fatal("missing command")
			}
			_ = stream.WriteByte(byte(command.Op()))
			_ = stream.WriteByte(command.PointCount())
			for pointIndex := uint8(0); pointIndex < command.PointCount(); pointIndex++ {
				point, ok := command.Point(pointIndex)
				if !ok {
					t.Fatal("missing point")
				}
				_ = binary.Write(&stream, binary.BigEndian, int64(point.X))
				_ = binary.Write(&stream, binary.BigEndian, int64(point.Y))
			}
		}
	}
	for _, number := range []uint64{
		receipt.ExpandedPointInstances, receipt.ExpandedContourInstances, receipt.ImpliedPointInstances,
		receipt.LineCommandInstances, receipt.QuadCommandInstances, receipt.CommandInstances,
		receipt.CommandPointOccurrences,
	} {
		_ = binary.Write(&stream, binary.BigEndian, number)
	}
	digest := sha256.Sum256(stream.Bytes())
	oracle := "sha256:" + hex.EncodeToString(digest[:])
	if oracle != receipt.OutlineSetHash || uint64(stream.Len()) != receipt.CanonicalBinaryBytes {
		t.Fatalf("oracle=%s production=%s bytes=%d receipt=%d", oracle, receipt.OutlineSetHash, stream.Len(), receipt.CanonicalBinaryBytes)
	}
}

func TestOutlineSetWorkFormula(t *testing.T) {
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, onCurve: false}, {x: 2, onCurve: false}}, nil, false, 0)
	geometry := outlineSetTestGeometry(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1}, geometryTestBBox{xMax: 2}, 0, 498, 2)
	_, r, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	want := r.SourceGraphCanonicalBinaryBytes + r.SourceGeometryCanonicalBinaryBytes +
		64*uint64(r.GlyphCount) + 16*r.ExpandedPointInstances + 8*r.ExpandedContourInstances +
		32*r.ExpandedEdgeVisits + 16*r.ExpandedGlyphInstances + 12*r.TransformPointVisits +
		16*r.ImpliedPointInstances + 12*r.DrawingCommandInstances + 8*r.CommandInstances +
		2*r.CommandPointOccurrences + 8*r.ExactOpCount + r.ExactLimbUnits + r.CanonicalBinaryBytes
	if want != r.WorkUnits {
		t.Fatalf("work=%d want=%d receipt=%+v", r.WorkUnits, want, r)
	}
}
