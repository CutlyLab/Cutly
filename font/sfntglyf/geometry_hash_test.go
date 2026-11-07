package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
)

func TestGeometryHashCanonicalOracleAndLimit(t *testing.T) {
	source := &OutlineFont{
		font: &Font{},
		glyphs: []outlineGlyph{{
			kind: OutlineGlyphSimple, expandedPoints: 1, expandedContours: 1,
		}},
		receipt: OutlineReceipt{
			Profile: OutlineReceiptProfileV1, DescriptorHash: "source-descriptor",
			GraphHash: "source-graph", UpstreamContentProfile: "source-content-profile",
			UpstreamContentHash: "source-content",
		},
	}
	exact := exactGlyphGeometry{
		points: []exactGeometryPoint{{
			x: geometryDyadic{numerator: big.NewInt(-3), shift: 1},
			y: geometryDyadic{numerator: new(big.Int)}, onCurve: true,
		}},
		endPoints: []uint32{0},
	}
	verified := geometryGlyph{
		kind: OutlineGlyphSimple, hasContours: true,
		xMin: -2, yMin: 0, xMax: -1, yMax: 0,
		advanceWidth: 500, leftSideBearing: -2, effectiveMetricSource: 0,
	}

	writer, err := newGeometryHashWriter(context.Background(), source, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.writeGlyph(0, source.glyphs[0], exact, verified); err != nil {
		t.Fatal(err)
	}
	gotHash, gotBytes, err := writer.finish(-2, 0, -1, 0, -2, 499, -1, GeometryHeadLSBSetV1)
	if err != nil {
		t.Fatal(err)
	}

	oracle := new(bytes.Buffer)
	writeString := func(text string) {
		_ = binary.Write(oracle, binary.BigEndian, uint64(len(text)))
		_, _ = oracle.WriteString(text)
	}
	writeU8 := func(value uint8) { _ = oracle.WriteByte(value) }
	writeU32 := func(value uint32) { _ = binary.Write(oracle, binary.BigEndian, value) }
	writeI32 := func(value int32) { writeU32(uint32(value)) }
	writeU64 := func(value uint64) { _ = binary.Write(oracle, binary.BigEndian, value) }
	writeString(GeometryHashProfileV1)
	writeU32(GeometryProfileSchemaVersion)
	writeString(GeometryProfileV1)
	writeString(GeometryDescriptorHash())
	writeString(source.receipt.Profile)
	writeString(source.receipt.DescriptorHash)
	writeString(source.receipt.GraphHash)
	writeString(source.receipt.UpstreamContentProfile)
	writeString(source.receipt.UpstreamContentHash)
	writeU32(1)
	writeU32(0)
	writeU8(1)
	writeU8(1)
	writeU64(1)
	writeU32(0)
	writeU64(1)
	writeU8(2)
	writeU32(1)
	writeU64(1)
	writeU8(3)
	writeU8(0)
	writeU32(0)
	writeU64(0)
	writeU8(1)
	writeI32(-2)
	writeI32(0)
	writeI32(-1)
	writeI32(0)
	writeU32(500)
	writeI32(-2)
	writeU32(0)
	writeI32(-2)
	writeI32(0)
	writeI32(-1)
	writeI32(0)
	writeI32(-2)
	writeI32(499)
	writeI32(-1)
	writeString(GeometryHeadLSBSetV1)
	digest := sha256.Sum256(oracle.Bytes())
	wantHash := "sha256:" + hex.EncodeToString(digest[:])
	if gotHash != wantHash || gotBytes != uint64(oracle.Len()) {
		t.Fatalf("canonical geometry hash mismatch: got (%s,%d), want (%s,%d)", gotHash, gotBytes, wantHash, oracle.Len())
	}

	limited, err := newGeometryHashWriter(context.Background(), source, 1, gotBytes-1)
	if err == nil {
		err = limited.writeGlyph(0, source.glyphs[0], exact, verified)
	}
	if err == nil {
		_, _, err = limited.finish(-2, 0, -1, 0, -2, 499, -1, GeometryHeadLSBSetV1)
	}
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("N-1 canonical limit error = %v, want resource limit", err)
	}
}
