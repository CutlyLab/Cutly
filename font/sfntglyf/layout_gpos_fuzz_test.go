package sfntglyf

import (
	"context"
	"testing"
)

func FuzzDecodeGPOSFoundation(f *testing.F) {
	for _, seed := range []struct {
		raw      []byte
		gdefMode uint8
	}{
		{raw: nil},
		{raw: gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, gposTestSingle1(3, 1, []uint16{2}, nil))},
		{raw: gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, gposTestPair1(3, 4))},
		{raw: gsubTestTable(0, 3, 0, gsubTestTag("curs"), nil, gposTestCursive(5, 2, 3))},
		{raw: gsubTestTable(0, 4, 0, gsubTestTag("mark"), nil, gposTestMarkBase(3, 5, 2, 3))},
		{raw: gsubTestTable(0, 5, 0, gsubTestTag("mark"), nil, gposTestMarkLigature(3, 5))},
		{raw: gsubTestTable(0, 6, 0, gsubTestTag("mkmk"), nil, gposTestMarkMark(3, 5))},
		{raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
			{lookupType: 9, subtables: [][]byte{gsubTestExtension(7, gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
			{lookupType: 1, subtables: [][]byte{gposTestSingle1(5, 0, nil, nil)}},
		})},
		{raw: gsubTestTable(0, 1, 0x0112, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil)), gdefMode: 1},
	} {
		f.Add(seed.raw, seed.gdefMode)
	}
	f.Fuzz(func(t *testing.T, raw []byte, gdefMode uint8) {
		points := gposTestPointCounts(16, 8)
		var gdef *gdefFoundation
		if gdefMode&1 != 0 {
			gdef = &gdefFoundation{
				glyphClasses:      &gdefClassDef{format: 1, values: []uint16{1}},
				markAttachClasses: &gdefClassDef{format: 1, values: []uint16{1}},
				markGlyphSets:     []*gdefCoverage{{}},
			}
		}
		foundation, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, gdef, layoutDecodeLimits{})
		if err != nil {
			if foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("failure published state: foundation=%#v stats=%#v", foundation, stats)
			}
			return
		}
		if foundation == nil {
			t.Fatal("success returned nil foundation")
		}
		if len(raw) == 0 {
			if stats != (layoutDecodeStats{}) {
				t.Fatalf("absent stats=%#v", stats)
			}
			return
		}
		if stats.gposBytes != uint64(len(raw)) || uint64(len(foundation.nodes)) != stats.nodes || uint64(len(foundation.edges)) != stats.edges {
			t.Fatalf("receipt drift: raw=%d nodes=%d/%d edges=%d/%d", len(raw), len(foundation.nodes), stats.nodes, len(foundation.edges), stats.edges)
		}
		for _, node := range foundation.nodes {
			if node.tableTag != layoutGPOSTag {
				t.Fatalf("node tag=%#x", node.tableTag)
			}
		}
	})
}

func FuzzDecodeGPOSWithDecodedGDEF(f *testing.F) {
	validGDEF := layoutTestClassAttachFixture()
	validGPOS := gsubTestTable(0, 1, 0x0202, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
	f.Add(validGDEF, validGPOS)
	f.Add([]byte(nil), gsubTestTable(0, 1, 0, gsubTestTag("kern"), nil, gposTestSingle1(3, 1, []uint16{2}, nil)))
	f.Add([]byte{0, 1, 0}, []byte{0, 1, 0})
	f.Fuzz(func(t *testing.T, gdefRaw, gposRaw []byte) {
		points := make([]uint32, 8)
		points[5] = 4
		gdef, gdefStats, gdefErr := decodeGDEFFoundation(context.Background(), gdefRaw, 8, points, layoutDecodeLimits{})
		if gdefErr != nil {
			if gdef != nil || gdefStats != (layoutDecodeStats{}) {
				t.Fatalf("GDEF failure published state: foundation=%#v stats=%#v", gdef, gdefStats)
			}
			return
		}
		foundation, stats, err := decodeGPOSFoundation(context.Background(), gposRaw, 8, points, gdef, layoutDecodeLimits{})
		if err != nil {
			if foundation != nil || stats != (layoutDecodeStats{}) {
				t.Fatalf("GPOS failure published state: foundation=%#v stats=%#v", foundation, stats)
			}
			return
		}
		if foundation == nil {
			t.Fatal("success returned nil GPOS foundation")
		}
		if len(gposRaw) != 0 && (stats.gposBytes != uint64(len(gposRaw)) || uint64(len(foundation.nodes)) != stats.nodes) {
			t.Fatalf("GPOS binding drift: bytes=%d/%d nodes=%d/%d", stats.gposBytes, len(gposRaw), len(foundation.nodes), stats.nodes)
		}
	})
}
