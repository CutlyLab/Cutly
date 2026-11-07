package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestBuildGSUBFoundationLiveLineageAndOwnership(t *testing.T) {
	gsubRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": gsubRaw, "GPOS": {1, 2, 3, 4},
	})
	foundation, receipt, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if foundation == nil || foundation.parent == nil || foundation.gsub == nil || foundation.receipt != receipt ||
		foundation.parent.source != set || receipt.Profile != gsubFoundationReceiptProfileV1 ||
		receipt.ParentFoundationHash != foundation.parent.receipt.FoundationHash ||
		receipt.DescriptorHash != gsubFoundationDescriptorIdentityV1 ||
		!validLayoutFoundationDigest(receipt.FoundationHash) || receipt.GSUBStatus != gsubFoundationGSUBValidatedStatusV1 ||
		receipt.GPOSStatus != gsubFoundationGPOSStatusV1 || receipt.CapabilityStatus != gsubFoundationCapabilityStatusV1 {
		t.Fatalf("foundation=%#v receipt=%#v", foundation, receipt)
	}
	if receipt.LogicalLookupEdges != 1 || receipt.LogicalLookupDepth != 1 || receipt.LogicalExpansion != 1 ||
		receipt.ContextGlyphReferences != 2 || receipt.SequenceLookupRecords != 1 ||
		len(foundation.gsub.logical.edges) != 1 || foundation.gsub.logical.edges[0].toLookup != 1 {
		t.Fatalf("logical receipt=%#v graph=%#v", receipt, foundation.gsub.logical)
	}
	for index, tag := range []string{"GDEF", "GSUB", "GPOS"} {
		if receipt.Tables[index].Tag != tag || !receipt.Tables[index].Present ||
			receipt.Tables[index].ContentHash != foundation.parent.receipt.Tables[index].ContentHash {
			t.Fatalf("table %d=%#v parent=%#v", index, receipt.Tables[index], foundation.parent.receipt.Tables[index])
		}
	}

	wantLogical := append([]gsubLogicalLookupEdge(nil), foundation.gsub.logical.edges...)
	wantTopological := append([]uint16(nil), foundation.gsub.logical.topological...)
	wantReceipt := receipt
	record, ok := findRecord(set.source.source.font.tables, "GSUB")
	if !ok {
		t.Fatal("missing GSUB")
	}
	for index := uint32(0); index < record.length; index++ {
		set.source.source.font.encoded[record.offset+index] ^= 0xff
	}
	if foundation.receipt != wantReceipt || !slices.Equal(foundation.gsub.logical.edges, wantLogical) ||
		!slices.Equal(foundation.gsub.logical.topological, wantTopological) {
		t.Fatal("successful foundation retained a mutable raw-table view")
	}
	if rebuilt, zero, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{}); err == nil || rebuilt != nil || zero != (gsubFoundationReceipt{}) {
		t.Fatalf("mutated lineage published: foundation=%#v receipt=%#v err=%v", rebuilt, zero, err)
	}
}

func TestBuildGSUBFoundationAbsentMalformedAndFailureZero(t *testing.T) {
	absentSet := layoutFoundationBuildTestSet(t, map[string][]byte{"GPOS": {1, 2, 3}})
	absent, receipt, err := buildGSUBFoundation(context.Background(), absentSet, gsubFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if absent == nil || absent.gsub != nil || receipt.GSUBPresent || receipt.GSUBStatus != gsubFoundationGSUBAbsentStatusV1 ||
		receipt.TableBytes != 0 || receipt.NodeCount != 0 || receipt.LogicalLookupEdges != 0 {
		t.Fatalf("absent=%#v receipt=%#v", absent, receipt)
	}

	invalid := [][]byte{
		{},
		{0, 1, 0, 0},
		gsubTestAdvancedTable([]gsubAdvancedLookupSpec{{lookupType: 5, subtables: [][]byte{
			gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 0}}),
		}}}),
	}
	for index, raw := range invalid {
		set := layoutFoundationBuildTestSet(t, map[string][]byte{"GSUB": raw})
		foundation, zero, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{})
		if err == nil || foundation != nil || zero != (gsubFoundationReceipt{}) {
			t.Fatalf("invalid %d published: foundation=%#v receipt=%#v err=%v", index, foundation, zero, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if foundation, zero, err := buildGSUBFoundation(ctx, absentSet, gsubFoundationLimits{}); !errors.Is(err, ErrCanceled) || foundation != nil || zero != (gsubFoundationReceipt{}) {
		t.Fatalf("canceled published: foundation=%#v receipt=%#v err=%v", foundation, zero, err)
	}
}

func TestBuildGSUBFoundationBindsLookupFlagsToLiveGDEF(t *testing.T) {
	raw := gsubTestTable(0, 1, 0x0002, gsubTestTag("liga"), nil, gsubTestSingle1(3, 1))
	withGDEF := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": raw,
	})
	if foundation, _, err := buildGSUBFoundation(context.Background(), withGDEF, gsubFoundationLimits{}); err != nil || foundation == nil {
		t.Fatalf("live GDEF binding rejected: foundation=%#v err=%v", foundation, err)
	}
	withoutGDEF := layoutFoundationBuildTestSet(t, map[string][]byte{"GSUB": raw})
	if foundation, zero, err := buildGSUBFoundation(context.Background(), withoutGDEF, gsubFoundationLimits{}); err == nil || foundation != nil || zero != (gsubFoundationReceipt{}) {
		t.Fatalf("GDEF-dependent lookup published without GDEF: foundation=%#v receipt=%#v err=%v", foundation, zero, err)
	}
}

func TestBuildGSUBFoundationResourceNNMinusOneAndLateCancel(t *testing.T) {
	gsubRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{lookupIndex: 2}, {lookupIndex: 2}})}},
		{lookupType: 6, subtables: [][]byte{gsubTestChain3(nil, []uint16{5}, nil, []gsubSequenceLookupRecord{{lookupIndex: 2}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture(), "GSUB": gsubRaw})
	_, receipt, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := gsubFoundationLimits{
		MaxOrderedRecords: receipt.OrderedRecords, MaxSubstituteGlyphs: receipt.SubstituteGlyphReferences,
		MaxLigatureComponents: defaultLayoutMaxLigComponents, MaxFeatureCharacters: defaultLayoutMaxFeatureChars,
		MaxContextGlyphs: receipt.ContextGlyphReferences, MaxSequenceLookups: receipt.SequenceLookupRecords,
		MaxLogicalLookupEdges: receipt.LogicalLookupEdges, MaxLogicalLookupDepth: receipt.LogicalLookupDepth,
		MaxLogicalExpansion: receipt.LogicalExpansion, MaxCanonicalBinaryBytes: receipt.CanonicalBinaryBytes,
		MaxWorkUnits: receipt.WorkUnits, MaxWorkingBytes: receipt.WorkingBytes, MaxRetainedBytes: receipt.RetainedBytes,
	}
	if _, exactReceipt, err := buildGSUBFoundation(context.Background(), set, exact); err != nil || exactReceipt != receipt {
		t.Fatalf("exact limits receipt_equal=%t err=%v", exactReceipt == receipt, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*gsubFoundationLimits)
	}{
		{name: "context_glyphs", cut: func(v *gsubFoundationLimits) { v.MaxContextGlyphs-- }},
		{name: "sequence_lookups", cut: func(v *gsubFoundationLimits) { v.MaxSequenceLookups-- }},
		{name: "logical_edges", cut: func(v *gsubFoundationLimits) { v.MaxLogicalLookupEdges-- }},
		{name: "logical_expansion", cut: func(v *gsubFoundationLimits) { v.MaxLogicalExpansion-- }},
		{name: "canonical", cut: func(v *gsubFoundationLimits) { v.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(v *gsubFoundationLimits) { v.MaxWorkUnits-- }},
		{name: "working", cut: func(v *gsubFoundationLimits) { v.MaxWorkingBytes-- }},
		{name: "retained", cut: func(v *gsubFoundationLimits) { v.MaxRetainedBytes-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			below := exact
			test.cut(&below)
			if foundation, zero, err := buildGSUBFoundation(context.Background(), set, below); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (gsubFoundationReceipt{}) {
				t.Fatalf("N-1 published: foundation=%#v receipt=%#v err=%v", foundation, zero, err)
			}
		})
	}

	probe := &geometryCountingContext{}
	if _, _, err := buildGSUBFoundation(probe, set, gsubFoundationLimits{}); err != nil {
		t.Fatal(err)
	}
	ctx := &geometryCountingContext{cancelAt: probe.calls - 1}
	if foundation, zero, err := buildGSUBFoundation(ctx, set, gsubFoundationLimits{}); !errors.Is(err, ErrCanceled) || foundation != nil || zero != (gsubFoundationReceipt{}) {
		t.Fatalf("late cancel published: foundation=%#v receipt=%#v err=%v", foundation, zero, err)
	}
}

func TestGSUBFoundationProfileReceiptAndHashGolden(t *testing.T) {
	descriptor := gsubFoundationDescriptorCanonicalJSON()
	descriptorDigest := sha256.Sum256(descriptor)
	if got := hex.EncodeToString(descriptorDigest[:]); got != "6d517c65d6762a42cf688340930b1545b3972d081867cc85679f2064e93374f9" {
		t.Fatalf("descriptor golden drift: %s", got)
	}
	if gsubFoundationDescriptorIdentityV1 != "sha256:1cafe54a8e0da87aa313524a5b7e21c4c773f5a4547ae237b32fe4bd8f618b14" {
		t.Fatalf("descriptor identity drift: %s", gsubFoundationDescriptorIdentityV1)
	}

	gsubRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 7, subtables: [][]byte{gsubTestExtension(5, gsubTestContext3([]uint16{3, 4}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
		{lookupType: 8, subtables: [][]byte{gsubTestReverse(5, []uint16{2}, []uint16{6}, 7)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture(), "GSUB": gsubRaw, "GPOS": {9, 8, 7}})
	_, receipt, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	if got := hex.EncodeToString(receiptDigest[:]); got != "5dcdf056e4633fee8eb2ff7f317bdfe4ddf937bbee08ff433ebb2f6e79a73268" {
		t.Fatalf("receipt golden drift: %s", got)
	}
	if receipt.FoundationHash != "sha256:7ffea5c1bdf0949b1cb1e1002ab3447b588c51159b96f4da7e51bd8492d5b0ae" || receipt.CanonicalBinaryBytes != 3253 {
		t.Fatalf("canonical hash golden drift: hash=%s bytes=%d", receipt.FoundationHash, receipt.CanonicalBinaryBytes)
	}

	again, second, err := buildGSUBFoundation(context.Background(), set, gsubFoundationLimits{})
	if err != nil || again == nil || second != receipt {
		t.Fatalf("determinism receipt_equal=%t err=%v", second == receipt, err)
	}
}
