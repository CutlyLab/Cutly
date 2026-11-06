package sfntglyf

import (
	"context"
	"testing"
)

func FuzzDecodeGDEFFoundation(f *testing.F) {
	for _, seed := range [][]byte{
		layoutTestHeader(0),
		layoutTestHeader(2),
		layoutTestHeader(3),
		layoutTestClassAttachFixture(),
		layoutTestMixedCaretFixture(),
		layoutTestCoverageFormat2Fixture(),
		layoutTestClassFormat2Fixture(),
	} {
		f.Add(seed, uint16(8), uint16(8))
	}
	f.Fuzz(func(t *testing.T, raw []byte, glyphCount uint16, realPointCount uint16) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		points := make([]uint32, int(glyphCount))
		for index := range points {
			points[index] = uint32(realPointCount)
		}
		foundation, stats, err := decodeGDEFFoundation(
			context.Background(), raw, uint32(glyphCount), points, layoutDecodeLimits{},
		)
		if err != nil {
			if foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("failure published state: foundation=%#v stats=%#v err=%v", foundation, stats, err)
			}
			return
		}
		if foundation == nil {
			t.Fatal("successful decode returned nil foundation")
		}
		if len(raw) == 0 {
			if stats != (layoutDecodeStats{}) {
				t.Fatalf("absent GDEF stats = %#v", stats)
			}
			return
		}
		if stats.gdefBytes != uint64(len(raw)) || stats.nodes != uint64(len(foundation.nodes)) ||
			stats.edges != uint64(len(foundation.edges)) {
			t.Fatalf("evidence mismatch: foundation=%#v stats=%#v raw=%d", foundation, stats, len(raw))
		}
	})
}
