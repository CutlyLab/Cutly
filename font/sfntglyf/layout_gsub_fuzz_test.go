package sfntglyf

import (
	"context"
	"testing"
)

func FuzzDecodeGSUBFoundation(f *testing.F) {
	for _, seed := range []struct {
		raw      []byte
		gdefMode uint8
	}{
		{raw: nil},
		{raw: gsubTestTable(0, 1, 0, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))},
		{raw: gsubTestTable(1, 1, 0, gsubTestTag("cv01"), gsubTestCVParams(), gsubTestSingle2([]uint16{2}, []uint16{4}))},
		{raw: gsubTestTable(0, 2, 0, gsubTestTag("liga"), nil, gsubTestMultiple(3, []uint16{4, 5}))},
		{raw: gsubTestTable(0, 3, 0, gsubTestTag("salt"), nil, gsubTestAlternate(3, []uint16{4, 5}))},
		{raw: gsubTestTable(0, 4, 0, gsubTestTag("liga"), nil, gsubTestLigature(3, 7, []uint16{4, 5}))},
		{raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
			{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
			{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
		})},
		{raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
			{lookupType: 6, subtables: [][]byte{gsubTestChain3([]uint16{2}, []uint16{3, 4}, []uint16{5}, []gsubSequenceLookupRecord{{sequenceIndex: 1, lookupIndex: 1}})}},
			{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
		})},
		{raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
			{lookupType: 7, subtables: [][]byte{gsubTestExtension(5, gsubTestContext1(3, []uint16{4}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
			{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
		})},
		{raw: gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 8, subtables: [][]byte{gsubTestReverse(3, []uint16{2}, []uint16{4}, 5)}}})},
		{raw: gsubTestTable(0, 1, 0x0112, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1)), gdefMode: 1},
	} {
		f.Add(seed.raw, seed.gdefMode)
	}
	f.Fuzz(func(t *testing.T, raw []byte, gdefMode uint8) {
		var gdef *gdefFoundation
		if gdefMode&1 != 0 {
			gdef = &gdefFoundation{
				glyphClasses:      &gdefClassDef{},
				markAttachClasses: &gdefClassDef{},
				markGlyphSets:     []*gdefCoverage{{}},
			}
		}
		foundation, stats, err := decodeGSUBFoundation(context.Background(), raw, 16, gdef, layoutDecodeLimits{})
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
		if stats.gsubBytes != uint64(len(raw)) || uint64(len(foundation.nodes)) != stats.nodes || uint64(len(foundation.edges)) != stats.edges {
			t.Fatalf("receipt drift: raw=%d nodes=%d/%d edges=%d/%d", len(raw), len(foundation.nodes), stats.nodes, len(foundation.edges), stats.edges)
		}
		for _, node := range foundation.nodes {
			if node.tableTag != layoutGSUBTag {
				t.Fatalf("node tag=%#x", node.tableTag)
			}
		}
	})
}
