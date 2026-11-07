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

func TestDecodeGPOSStaticVersionsAndVariationRejection(t *testing.T) {
	points := gposTestPointCounts(16, 4)
	for _, minor := range []uint16{0, 1} {
		raw := gsubTestTable(minor, 1, 0, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
		foundation, stats, err := decodeGPOSFoundation(context.Background(), raw, 16, points, nil, layoutDecodeLimits{})
		if err != nil || foundation == nil || foundation.majorVersion != 1 || foundation.minorVersion != minor || stats.gposBytes != uint64(len(raw)) {
			t.Fatalf("minor=%d foundation=%#v stats=%#v err=%v", minor, foundation, stats, err)
		}
	}
	variable := gsubTestTable(1, 1, 0, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
	variable[13] = 1
	if result, zero, err := decodeGPOSFoundation(context.Background(), variable, 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("FeatureVariations accepted: result=%#v stats=%#v err=%v", result, zero, err)
	}
}

func TestDecodeGPOSSizeFeatureParams(t *testing.T) {
	points := gposTestPointCounts(16, 4)
	valid := gposTestSizeTable([]uint16{120, 0, 0, 0, 0}, false)
	foundation, _, err := decodeGPOSFoundation(context.Background(), valid, 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	params, ok := foundation.features[0].params.(*layoutSizeFeatureParams)
	if !ok || params.designSize != 120 || len(foundation.features[0].feature.lookupIndices) != 0 {
		t.Fatalf("params=%#v feature=%#v", foundation.features[0].params, foundation.features[0].feature)
	}
	withRange := gposTestSizeTable([]uint16{180, 3, 257, 139, 240}, false)
	if result, _, err := decodeGPOSFoundation(context.Background(), withRange, 16, points, nil, layoutDecodeLimits{}); err != nil || result == nil {
		t.Fatalf("valid range rejected: result=%#v err=%v", result, err)
	}
	for _, test := range []struct {
		name   string
		params []uint16
		lookup bool
	}{
		{name: "design_zero", params: []uint16{0, 0, 0, 0, 0}},
		{name: "tail_without_subfamily", params: []uint16{120, 0, 257, 100, 140}},
		{name: "name_low", params: []uint16{120, 1, 255, 100, 140}},
		{name: "name_high", params: []uint16{120, 1, 32768, 100, 140}},
		{name: "range_reversed", params: []uint16{120, 1, 256, 140, 100}},
		{name: "lookup_nonzero", params: []uint16{120, 0, 0, 0, 0}, lookup: true},
		{name: "params_missing", params: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, zero, err := decodeGPOSFoundation(context.Background(), gposTestSizeTable(test.params, test.lookup), 16, points, nil, layoutDecodeLimits{})
			if err == nil || result != nil || zero != (layoutDecodeStats{}) {
				t.Fatalf("invalid size feature published: result=%#v stats=%#v err=%v", result, zero, err)
			}
		})
	}

	ssRaw := gsubTestTable(0, 1, 0, gsubTestTag("ss01"), layoutTestWords(0, 300), gposTestSingle1(3, 0, nil, nil))
	ssFoundation, _, err := decodeGPOSFoundation(context.Background(), ssRaw, 16, points, nil, layoutDecodeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if params, ok := ssFoundation.features[0].params.(*layoutSSFeatureParams); !ok || params.uiLabelNameID != 300 {
		t.Fatalf("GPOS ss params=%#v", ssFoundation.features[0].params)
	}
	cvRaw := gsubTestTable(0, 1, 0, gsubTestTag("cv01"), gsubTestCVParams(), gposTestSingle1(3, 0, nil, nil))
	if result, zero, err := decodeGPOSFoundation(context.Background(), cvRaw, 16, points, nil, layoutDecodeLimits{}); err == nil || result != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("GSUB-only cv params accepted in GPOS: result=%#v stats=%#v err=%v", result, zero, err)
	}
}

func TestBuildGPOSFoundationAtomicLineageOwnershipAndFailureZero(t *testing.T) {
	gsubRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
	})
	gposRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 7, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 1, subtables: [][]byte{gposTestSingle1(6, 0x0001, []uint16{2}, nil)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": gsubRaw, "GPOS": gposRaw,
	})
	foundation, receipt, err := buildGPOSFoundation(context.Background(), set, gposFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if foundation == nil || foundation.parent == nil || foundation.parent.parent == nil || foundation.gpos == nil ||
		foundation.receipt != receipt || receipt.Profile != gposFoundationReceiptProfileV1 ||
		receipt.ParentFoundationHash != foundation.parent.receipt.FoundationHash ||
		receipt.DescriptorHash != gposFoundationDescriptorIdentityV1 ||
		!validLayoutFoundationDigest(receipt.FoundationHash) ||
		receipt.GPOSStatus != gposFoundationGPOSValidatedStatusV1 ||
		receipt.GSUBStatus != gposFoundationGSUBStatusV1 ||
		receipt.CapabilityStatus != gposFoundationCapabilityStatusV1 || receipt.Stats.LogicalEdges != 1 {
		t.Fatalf("foundation=%#v receipt=%#v", foundation, receipt)
	}
	wantReceipt := receipt
	wantEdges := append([]gsubLogicalLookupEdge(nil), foundation.gpos.logical.edges...)
	record, ok := findRecord(set.source.source.font.tables, "GPOS")
	if !ok {
		t.Fatal("missing GPOS")
	}
	for index := uint32(0); index < record.length; index++ {
		set.source.source.font.encoded[record.offset+index] ^= 0xff
	}
	if foundation.receipt != wantReceipt || !slices.Equal(foundation.gpos.logical.edges, wantEdges) {
		t.Fatal("successful GPOS foundation retained caller bytes")
	}
	if rebuilt, zero, err := buildGPOSFoundation(context.Background(), set, gposFoundationLimits{}); err == nil || rebuilt != nil || zero != (gposFoundationReceipt{}) {
		t.Fatalf("mutated lineage published: foundation=%#v receipt=%#v err=%v", rebuilt, zero, err)
	}

	invalidSet := layoutFoundationBuildTestSet(t, map[string][]byte{"GPOS": {0, 1, 0, 0}})
	if result, zero, err := buildGPOSFoundation(context.Background(), invalidSet, gposFoundationLimits{}); err == nil || result != nil || zero != (gposFoundationReceipt{}) {
		t.Fatalf("malformed GPOS published: result=%#v receipt=%#v err=%v", result, zero, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, zero, err := buildGPOSFoundation(ctx, invalidSet, gposFoundationLimits{}); !errors.Is(err, ErrCanceled) || result != nil || zero != (gposFoundationReceipt{}) {
		t.Fatalf("canceled GPOS published: result=%#v receipt=%#v err=%v", result, zero, err)
	}
}

func TestBuildGPOSFoundationAbsentAndResourceNNMinusOne(t *testing.T) {
	absentSet := layoutFoundationBuildTestSet(t, map[string][]byte{})
	absent, receipt, err := buildGPOSFoundation(context.Background(), absentSet, gposFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if absent == nil || absent.gpos != nil || receipt.GPOSPresent || receipt.GPOSStatus != gposFoundationGPOSAbsentStatusV1 || receipt.Stats != (gposFoundationStatsReceipt{}) {
		t.Fatalf("absent=%#v receipt=%#v", absent, receipt)
	}

	gposRaw := gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, gposTestPair2(3, 4))
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GPOS": gposRaw})
	_, receipt, err = buildGPOSFoundation(context.Background(), set, gposFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := gposFoundationLimits{
		MaxCanonicalBinaryBytes: receipt.CanonicalBinaryBytes,
		MaxWorkUnits:            receipt.WorkUnits, MaxWorkingBytes: receipt.WorkingBytes,
		MaxRetainedBytes: receipt.RetainedBytes,
	}
	if _, got, err := buildGPOSFoundation(context.Background(), set, exact); err != nil || got != receipt {
		t.Fatalf("exact limits receipt_equal=%t err=%v", got == receipt, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*gposFoundationLimits)
	}{
		{name: "canonical", cut: func(v *gposFoundationLimits) { v.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(v *gposFoundationLimits) { v.MaxWorkUnits-- }},
		{name: "working", cut: func(v *gposFoundationLimits) { v.MaxWorkingBytes-- }},
		{name: "retained", cut: func(v *gposFoundationLimits) { v.MaxRetainedBytes-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			below := exact
			test.cut(&below)
			if result, zero, err := buildGPOSFoundation(context.Background(), set, below); !errors.Is(err, ErrResourceLimit) || result != nil || zero != (gposFoundationReceipt{}) {
				t.Fatalf("N-1 published: result=%#v receipt=%#v err=%v", result, zero, err)
			}
		})
	}
}

func TestBuildGPOSFoundationBindsLookupFlagsToLiveGDEF(t *testing.T) {
	flagged := gsubTestTable(0, 1, 0x0202, gsubTestTag("kern"), nil, gposTestSingle1(3, 0, nil, nil))
	bound := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GPOS": flagged,
	})
	if result, receipt, err := buildGPOSFoundation(context.Background(), bound, gposFoundationLimits{}); err != nil || result == nil || !receipt.GPOSPresent {
		t.Fatalf("live GDEF binding rejected: result=%#v receipt=%#v err=%v", result, receipt, err)
	}
	unbound := layoutFoundationBuildTestSet(t, map[string][]byte{"GPOS": flagged})
	if result, zero, err := buildGPOSFoundation(context.Background(), unbound, gposFoundationLimits{}); err == nil || result != nil || zero != (gposFoundationReceipt{}) {
		t.Fatalf("lookup flags published without GDEF: result=%#v receipt=%#v err=%v", result, zero, err)
	}
}

func TestGPOSFoundationProfileReceiptAndHashGolden(t *testing.T) {
	const (
		wantDescriptorJSON = "05f830cadf2ec8ef08c55259522dd97465ad4dec6b9a3d023d2ec332cbe2d812"
		wantDescriptorID   = "sha256:1d2a703514481f4c23d6914f4ade589e405797e02e55903bc96310e9d1dba9da"
		wantReceiptJSON    = "0e592e2e9389fdc494b96c812796e55e857a653bad1ed235554f5c49fb92e8ec"
		wantFoundationHash = "sha256:32adb7f1b88772dc3f085e6b9243ffd579d996f66edeb5725d0677f7d95a2244"
		wantCanonicalBytes = uint64(2819)
	)
	descriptor := gposFoundationDescriptorCanonicalJSON()
	digest := sha256.Sum256(descriptor)
	if got := hex.EncodeToString(digest[:]); got != wantDescriptorJSON || gposFoundationDescriptorIdentityV1 != wantDescriptorID {
		t.Fatalf("descriptor=%s identity=%s", hex.EncodeToString(digest[:]), gposFoundationDescriptorIdentityV1)
	}
	gposRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 9, subtables: [][]byte{gsubTestExtension(7, gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
		{lookupType: 1, subtables: [][]byte{gposTestSingle1(5, 1, []uint16{4}, nil)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GPOS": gposRaw})
	first, receipt, err := buildGPOSFoundation(context.Background(), set, gposFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	secondFoundation, second, err := buildGPOSFoundation(context.Background(), set, gposFoundationLimits{})
	if err != nil || first == nil || secondFoundation == nil || second != receipt {
		t.Fatalf("determinism receipt_equal=%t err=%v", second == receipt, err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) == 0 || !validLayoutFoundationDigest(receipt.FoundationHash) || receipt.CanonicalBinaryBytes == 0 {
		t.Fatalf("receipt json=%s err=%v", encoded, err)
	}
	receiptDigest := sha256.Sum256(encoded)
	if got := hex.EncodeToString(receiptDigest[:]); got != wantReceiptJSON || receipt.FoundationHash != wantFoundationHash || receipt.CanonicalBinaryBytes != wantCanonicalBytes {
		t.Fatalf("receipt_json=%s foundation_hash=%s canonical_bytes=%d", got, receipt.FoundationHash, receipt.CanonicalBinaryBytes)
	}
}

func TestBuildGPOSFoundationLateCancellationFailsZero(t *testing.T) {
	gposRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 9, subtables: [][]byte{gsubTestExtension(7, gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}}))}},
		{lookupType: 4, subtables: [][]byte{gposTestMarkBase(4, 5, 2, 3)}},
	})
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GPOS": gposRaw,
	})
	probe := &geometryCountingContext{}
	if result, _, err := buildGPOSFoundation(probe, set, gposFoundationLimits{}); err != nil || result == nil {
		t.Fatalf("probe build failed: result=%#v err=%v", result, err)
	}
	checkpoints := []int{1, probe.calls / 2, probe.calls - 1, probe.calls}
	seen := make(map[int]bool)
	for _, cancelAt := range checkpoints {
		if cancelAt <= 0 || seen[cancelAt] {
			continue
		}
		seen[cancelAt] = true
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		result, zero, err := buildGPOSFoundation(ctx, set, gposFoundationLimits{})
		if !errors.Is(err, ErrCanceled) || result != nil || zero != (gposFoundationReceipt{}) {
			t.Fatalf("cancelAt=%d/%d published: result=%#v receipt=%#v err=%v", cancelAt, probe.calls, result, zero, err)
		}
	}
}

func gposTestSizeTable(params []uint16, includeLookup bool) []byte {
	headerSize := 10
	scriptList := layoutTestWords(1)
	scriptList = append(scriptList, byte(layoutDFLTTag>>24), byte((layoutDFLTTag>>16)&0xff), byte((layoutDFLTTag>>8)&0xff), byte(layoutDFLTTag&0xff), 0, 8)
	scriptList = append(scriptList, layoutTestWords(4, 0, 0, 0xffff, 1, 0)...)

	featureList := layoutTestWords(1)
	tag := layoutSizeTag
	featureList = append(featureList, byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag), 0, 8)
	paramsOffset := uint16(0)
	if params != nil {
		paramsOffset = 4
	}
	lookupCount := uint16(0)
	if includeLookup {
		lookupCount = 1
	}
	featureList = append(featureList, layoutTestWords(paramsOffset, lookupCount)...)
	if includeLookup {
		featureList = append(featureList, layoutTestWords(0)...)
		if paramsOffset != 0 {
			layoutTestPut16(featureList, 8, 6)
		}
	}
	featureList = append(featureList, layoutTestWords(params...)...)

	lookupList := layoutTestWords(1, 4, 1, 0, 1, 8)
	lookupList = append(lookupList, gposTestSingle1(3, 0, nil, nil)...)
	result := make([]byte, headerSize)
	layoutTestPut16(result, 0, 1)
	scriptStart := len(result)
	result = append(result, scriptList...)
	featureStart := len(result)
	result = append(result, featureList...)
	lookupStart := len(result)
	result = append(result, lookupList...)
	layoutTestPut16(result, 4, uint16(scriptStart))
	layoutTestPut16(result, 6, uint16(featureStart))
	layoutTestPut16(result, 8, uint16(lookupStart))
	return result
}
