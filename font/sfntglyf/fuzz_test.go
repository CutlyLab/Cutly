package sfntglyf

import (
	"context"
	"testing"
)

func FuzzParseNoPanic(f *testing.F) {
	seed := buildTestSFNT(&fuzzTestingT{f: f}, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	f.Add(seed)
	f.Add([]byte{})
	f.Add([]byte{0, 1, 0, 0})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		limits := DefaultLimits()
		limits.MaxEncodedBytes = 1 << 20
		font, receipt, err := Parse(context.Background(), encoded, limits)
		if err != nil {
			if font != nil || !isZeroReceipt(receipt) {
				t.Fatalf("failure published partial result: %v, %#v, %v", font, receipt, err)
			}
			return
		}
		if font == nil || receipt.Profile != ReceiptProfileV1 {
			t.Fatalf("success missing result: %v, %#v", font, receipt)
		}
	})
}

// FuzzParseRechecksummedCmap keeps the sfnt envelope and every checksum valid so mutations reach
// cmap bounds and semantic validation instead of stopping at the container checksum gate.
func FuzzParseRechecksummedCmap(f *testing.F) {
	f.Add(makeTestCmap([][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}}))
	f.Add(makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x10000, 0x10000, 1}})}, []testCmapRecord{{3, 10, 0}}))
	f.Fuzz(func(t *testing.T, cmap []byte) {
		if len(cmap) > 1<<16 {
			t.Skip()
		}
		encoded := buildTestSFNT(t, minimalTestTables(4, cmap))
		limits := DefaultLimits()
		limits.MaxEncodedBytes = 1 << 20
		font, receipt, err := Parse(context.Background(), encoded, limits)
		if err != nil {
			if font != nil || !isZeroReceipt(receipt) {
				t.Fatalf("failure published partial result: %v, %#v, %v", font, receipt, err)
			}
			return
		}
		if font == nil || receipt.CmapProfile != CmapProfileV1 {
			t.Fatalf("success missing result: %v, %#v", font, receipt)
		}
	})
}

func FuzzDecodeOutlineFontNoPanic(f *testing.F) {
	seed := buildTestSFNT(&fuzzTestingT{f: f}, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 1<<20 {
			t.Skip()
		}
		font, _, err := Parse(context.Background(), encoded, Limits{MaxEncodedBytes: 1 << 20})
		if err != nil {
			return
		}
		limits := DefaultOutlineLimits()
		limits.MaxRawPoints = 1 << 16
		limits.MaxRawContours = 1 << 16
		limits.MaxComponentRecords = 1 << 16
		limits.MaxExpandedPointInstances = 1 << 18
		limits.MaxExpandedContourInstances = 1 << 18
		limits.MaxExpandedEdgeVisits = 1 << 18
		outline, receipt, err := DecodeOutlineFont(context.Background(), font, limits)
		if err != nil {
			if outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("failure published partial outline: %v, %#v, %v", outline, receipt, err)
			}
			return
		}
		if outline == nil || receipt.Profile != OutlineReceiptProfileV1 {
			t.Fatalf("success missing result: %v, %#v", outline, receipt)
		}
	})
}

// FuzzDecodeOutlineRechecksummedGlyph rebuilds loca, every table checksum and the whole-font
// checksum around arbitrary glyf bytes so mutations reach the body scanner instead of stopping at
// the T1b1a container gate.
func FuzzDecodeOutlineRechecksummedGlyph(f *testing.F) {
	f.Add([]byte{})
	f.Add(makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{0, 0, true}}, nil, false, 0))
	f.Add(makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 0}}, nil, 0))
	f.Fuzz(func(t *testing.T, glyphBody []byte) {
		if len(glyphBody) > 1<<16 {
			t.Skip()
		}
		// Deliberately overdeclare every phase-one maxp field so fuzz mutations reach flags,
		// coordinates, components and graph validation before the final exact-maxp gate.
		tables := outlineTestTables(t, [][]byte{nil, glyphBody}, outlineTestMaxp{
			points: 0xffff, contours: 0xffff, compositePoints: 0xffff, compositeContours: 0xffff,
			instructions: 0xffff, componentElements: 0xffff, depth: 0xffff,
		})
		font, _, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{MaxEncodedBytes: 1 << 20})
		if err != nil {
			t.Fatalf("checksum-preserving envelope failed: %v", err)
		}
		limits := DefaultOutlineLimits()
		limits.MaxRawPoints = 1 << 16
		limits.MaxRawContours = 1 << 16
		limits.MaxComponentRecords = 1 << 16
		outline, receipt, err := DecodeOutlineFont(context.Background(), font, limits)
		if err != nil {
			if outline != nil || !isZeroOutlineReceipt(receipt) {
				t.Fatalf("failure published partial outline: %v, %#v, %v", outline, receipt, err)
			}
			return
		}
		if outline == nil || receipt.Profile != OutlineReceiptProfileV1 {
			t.Fatalf("success missing result: %v, %#v", outline, receipt)
		}
	})
}

// fuzzTestingT adapts the deterministic test builder without duplicating it. Fatalf is unreachable
// for the package-owned seed definition.
type fuzzTestingT struct{ f *testing.F }

func (value *fuzzTestingT) Helper() {}
func (value *fuzzTestingT) Fatalf(format string, arguments ...any) {
	value.f.Fatalf(format, arguments...)
}
