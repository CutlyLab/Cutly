package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestBuildLayoutFoundationAbsentGDEFAndFixedTableBindings(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GSUB": {0, 1, 0, 0},
		"GPOS": {0, 1, 0, 1, 2, 3},
	})
	foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if foundation == nil || foundation.source != set || foundation.gdef != nil || foundation.receipt != receipt {
		t.Fatalf("foundation=%#v receipt=%#v", foundation, receipt)
	}
	if receipt.GDEFPresent || receipt.GDEFMajorVersion != 0 || receipt.GDEFMinorVersion != 0 ||
		receipt.GDEFStatus != layoutFoundationGDEFAbsentStatusV1 || receipt.NodeCount != 0 || receipt.EdgeCount != 0 {
		t.Fatalf("absent GDEF receipt=%#v", receipt)
	}
	if receipt.Profile != layoutFoundationReceiptProfileV1 ||
		receipt.DescriptorHash != layoutFoundationDescriptorIdentityV1 ||
		!validLayoutFoundationDigest(receipt.FoundationHash) || receipt.CanonicalBinaryBytes == 0 {
		t.Fatalf("receipt identity=%#v", receipt)
	}
	font := set.source.source.font
	for index, tag := range []string{"GDEF", "GSUB", "GPOS"} {
		binding, tableReceipt := foundation.tables[index], receipt.Tables[index]
		if string(binding.tag[:]) != tag || tableReceipt.Tag != tag || binding.present != tableReceipt.Present {
			t.Fatalf("table %d binding=%#v receipt=%#v", index, binding, tableReceipt)
		}
		record, present := findRecord(font.tables, tag)
		if present != binding.present {
			t.Fatalf("table %s presence=%t binding=%t", tag, present, binding.present)
		}
		if present && (binding.fileOffset != record.offset || binding.length != record.length ||
			binding.checksum != record.checksum || binding.hashProfile != TableHashProfileV1 ||
			!validLayoutFoundationDigest(binding.contentHash)) {
			t.Fatalf("table %s binding=%#v record=%#v", tag, binding, record)
		}
	}
	if len(foundation.realPointCounts) != int(receipt.GlyphCount) || foundation.realPointCounts[5] != 4 ||
		receipt.RealPointInstances != 4 {
		t.Fatalf("point counts=%v receipt=%#v", foundation.realPointCounts, receipt)
	}
	if layoutFoundationStatsFromReceipt(receipt) != (layoutFoundationResourceStats{layoutTableBytes: 10}) {
		t.Fatalf("absent stats=%#v", layoutFoundationStatsFromReceipt(receipt))
	}
}

func TestBuildLayoutFoundationValidGDEFVersionsAndCompleteModels(t *testing.T) {
	tests := []struct {
		name  string
		minor uint16
		gdef  []byte
		check func(*testing.T, layoutFoundationReceipt, *gdefFoundation)
	}{
		{name: "1.0", minor: 0, gdef: layoutTestClassAttachFixture(), check: func(t *testing.T, r layoutFoundationReceipt, g *gdefFoundation) {
			if r.ClassDefTableCount != 2 || r.ClassAssignmentInstances != 5 ||
				r.AttachPointTableCount != 1 || r.AttachmentPointIndices != 2 || len(g.attachments) != 1 {
				t.Fatalf("1.0 receipt=%#v model=%#v", r, g)
			}
		}},
		{name: "1.2", minor: 2, gdef: layoutTestAliasedMarkSetsFixture(15, 12, 2), check: func(t *testing.T, r layoutFoundationReceipt, g *gdefFoundation) {
			if r.AliasReferenceCount != 1 || r.MarkGlyphSetReferences != 2 || len(g.markGlyphSets) != 2 {
				t.Fatalf("1.2 receipt=%#v model=%#v", r, g)
			}
		}},
		{name: "1.3-static-zero", minor: 3, gdef: layoutTestHeader(3), check: func(t *testing.T, r layoutFoundationReceipt, g *gdefFoundation) {
			if r.NodeCount != 1 || len(g.nodes) != 1 {
				t.Fatalf("1.3 receipt=%#v model=%#v", r, g)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := layoutFoundationBuildTestSet(t, map[string][]byte{
				"GDEF": test.gdef, "GSUB": {1, 2, 3, 4}, "GPOS": {5, 6, 7, 8},
			})
			foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
			if err != nil {
				t.Fatal(err)
			}
			gdef, ok := foundation.gdef.(*gdefFoundation)
			if !ok || gdef == nil || !receipt.GDEFPresent || receipt.GDEFMajorVersion != 1 ||
				receipt.GDEFMinorVersion != test.minor || receipt.GDEFStatus != layoutFoundationGDEFValidatedStatusV1 {
				t.Fatalf("foundation=%#v receipt=%#v", foundation, receipt)
			}
			if receipt.DirectBytes == 0 || receipt.DirectBytes > uint64(len(test.gdef)) ||
				layoutFoundationReceiptLayoutTableBytes(receipt) != uint64(len(test.gdef)+8) {
				t.Fatalf("direct/table bytes receipt=%#v", receipt)
			}
			test.check(t, receipt, gdef)
		})
	}
}

func TestBuildLayoutFoundationRejectsInvalidGDEFVariationAndSourceMutation(t *testing.T) {
	variable := layoutTestHeader(3)
	layoutTestPut32(variable, 14, 18)
	invalid := []struct {
		name string
		gdef []byte
	}{
		{name: "empty-present", gdef: []byte{}},
		{name: "truncated", gdef: layoutTestHeader(0)[:11]},
		{name: "variation-store", gdef: variable},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": test.gdef})
			foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
			if err == nil || foundation != nil || receipt != (layoutFoundationReceipt{}) {
				t.Fatalf("foundation=%#v receipt=%#v err=%v", foundation, receipt, err)
			}
		})
	}

	set := cloneOutlineSetForRebind(layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestHeader(0)}))
	font := set.source.source.font
	record, ok := findRecord(font.tables, "GDEF")
	if !ok {
		t.Fatal("missing GDEF")
	}
	font.encoded[record.offset] ^= 1
	foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
	if err == nil || foundation != nil || receipt != (layoutFoundationReceipt{}) {
		t.Fatalf("mutated foundation=%#v receipt=%#v err=%v", foundation, receipt, err)
	}
}

func TestBuildLayoutFoundationExactResourceAndCountLimits(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": {1, 2, 3}, "GPOS": {4, 5, 6, 7},
	})
	baseline, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := layoutFoundationExactLimits(receipt)
	again, exactReceipt, err := buildLayoutFoundation(context.Background(), set, exact)
	if err != nil || again == nil || exactReceipt != receipt {
		t.Fatalf("exact limits foundation=%#v receipt_equal=%t err=%v", again, exactReceipt == receipt, err)
	}
	if baseline.receipt != receipt {
		t.Fatal("baseline receipt drift")
	}

	tests := []struct {
		name string
		cut  func(*layoutFoundationLimits)
	}{
		{name: "layout-table-bytes", cut: func(v *layoutFoundationLimits) { v.MaxLayoutTableBytes-- }},
		{name: "nodes", cut: func(v *layoutFoundationLimits) { v.MaxNodes-- }},
		{name: "edges", cut: func(v *layoutFoundationLimits) { v.MaxEdges-- }},
		{name: "depth", cut: func(v *layoutFoundationLimits) { v.MaxStructuralDepth-- }},
		{name: "class-tables", cut: func(v *layoutFoundationLimits) { v.MaxClassDefTables-- }},
		{name: "class-assignments", cut: func(v *layoutFoundationLimits) { v.MaxClassAssignments-- }},
		{name: "attachment-points", cut: func(v *layoutFoundationLimits) { v.MaxAttachmentPoints-- }},
		{name: "canonical", cut: func(v *layoutFoundationLimits) { v.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(v *layoutFoundationLimits) { v.MaxWorkUnits-- }},
		{name: "working", cut: func(v *layoutFoundationLimits) { v.MaxWorkingBytes-- }},
		{name: "retained", cut: func(v *layoutFoundationLimits) { v.MaxRetainedBytes-- }},
	}
	for _, test := range tests {
		t.Run(test.name+"-N-minus-one", func(t *testing.T) {
			below := exact
			test.cut(&below)
			foundation, zero, err := buildLayoutFoundation(context.Background(), set, below)
			if !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutFoundationReceipt{}) {
				t.Fatalf("foundation=%#v receipt=%#v err=%v", foundation, zero, err)
			}
		})
	}
}

func TestBuildLayoutFoundationAbsentGDEFExactResourceLimits(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GSUB": {0, 1, 0, 0}, "GPOS": {0, 1, 0, 0},
	})
	_, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GDEFPresent {
		t.Fatalf("unexpected GDEF receipt=%#v", receipt)
	}
	exact := layoutFoundationExactLimits(receipt)
	foundation, rebuilt, err := buildLayoutFoundation(context.Background(), set, exact)
	if err != nil || foundation == nil || rebuilt != receipt {
		t.Fatalf("exact limits foundation=%#v receipt_equal=%t err=%v", foundation, rebuilt == receipt, err)
	}
	tests := []struct {
		name string
		cut  func(*layoutFoundationLimits)
	}{
		{name: "canonical", cut: func(value *layoutFoundationLimits) { value.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(value *layoutFoundationLimits) { value.MaxWorkUnits-- }},
		{name: "working", cut: func(value *layoutFoundationLimits) { value.MaxWorkingBytes-- }},
		{name: "retained", cut: func(value *layoutFoundationLimits) { value.MaxRetainedBytes-- }},
	}
	for _, test := range tests {
		t.Run(test.name+"-N-minus-one", func(t *testing.T) {
			below := exact
			test.cut(&below)
			got, zero, err := buildLayoutFoundation(context.Background(), set, below)
			if !errors.Is(err, ErrResourceLimit) || got != nil || zero != (layoutFoundationReceipt{}) {
				t.Fatalf("foundation=%#v receipt=%#v err=%v", got, zero, err)
			}
		})
	}
}

func TestBuildLayoutFoundationLargeGapAndDeviceHeavyExactRebuild(t *testing.T) {
	largeGap := make([]byte, 512<<10)
	copy(largeGap, layoutTestHeader(0))
	tests := []struct {
		name  string
		gdef  []byte
		check func(*testing.T, layoutFoundationReceipt)
	}{
		{name: "large-gap-header-only", gdef: largeGap, check: func(t *testing.T, receipt layoutFoundationReceipt) {
			if receipt.NodeCount != 1 || receipt.EdgeCount != 0 || receipt.DirectBytes != uint64(len(layoutTestHeader(0))) ||
				layoutFoundationReceiptLayoutTableBytes(receipt) != uint64(len(largeGap)) {
				t.Fatalf("large-gap receipt=%#v", receipt)
			}
		}},
		{name: "device-heavy", gdef: layoutFoundationDeviceHeavyFixture(), check: func(t *testing.T, receipt layoutFoundationReceipt) {
			if receipt.DeviceTableCount != 1 || receipt.DeviceDeltaValues != 4_096 || receipt.DeviceWordCount != 2_048 ||
				receipt.CaretValueReferences != 1 {
				t.Fatalf("device-heavy receipt=%#v", receipt)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": test.gdef})
			_, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, receipt)
			foundation, rebuilt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationExactLimits(receipt))
			if err != nil || foundation == nil || rebuilt != receipt {
				t.Fatalf("exact rebuild foundation=%#v receipt_equal=%t err=%v", foundation, rebuilt == receipt, err)
			}
		})
	}
}

func TestBuildLayoutFoundationPrivateMutationFailureIsAtomic(t *testing.T) {
	base := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture()})
	tests := []struct {
		name   string
		mutate func(*OutlineSet)
	}{
		{name: "outline-set-receipt", mutate: func(value *OutlineSet) {
			value.receipt.WorkUnits++
		}},
		{name: "outline-graph", mutate: func(value *OutlineSet) {
			value.source.source.glyphs[5].points[0].x++
		}},
		{name: "font-table-record", mutate: func(value *OutlineSet) {
			record, ok := findRecord(value.source.source.font.tables, "GDEF")
			if !ok {
				t.Fatal("missing GDEF record")
			}
			for index := range value.source.source.font.tables {
				if value.source.source.font.tables[index].tag == record.tag {
					value.source.source.font.tables[index].checksum++
					return
				}
			}
			t.Fatal("missing mutable GDEF record")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := cloneOutlineSetForRebind(base)
			test.mutate(set)
			foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
			if err == nil || foundation != nil || receipt != (layoutFoundationReceipt{}) {
				t.Fatalf("foundation=%#v receipt=%#v err=%v", foundation, receipt, err)
			}
		})
	}
}

func TestBuildLayoutFoundationAliasDeviceCaretAndMarkCountLimits(t *testing.T) {
	tests := []struct {
		name string
		gdef []byte
		cut  func(*layoutFoundationLimits, layoutFoundationReceipt)
	}{
		{name: "aliases", gdef: layoutTestAliasedMarkSetsFixture(15, 16, 3), cut: func(v *layoutFoundationLimits, r layoutFoundationReceipt) {
			v.MaxAliasReferences = r.AliasReferenceCount - 1
		}},
		{name: "mark-set-references", gdef: layoutTestAliasedMarkSetsFixture(15, 16, 3), cut: func(v *layoutFoundationLimits, r layoutFoundationReceipt) {
			v.MaxMarkGlyphSets = r.MarkGlyphSetReferences - 1
		}},
		{name: "device-tables", gdef: func() []byte { raw, _ := layoutTestAllDeviceFormatsFixture(); return raw }(), cut: func(v *layoutFoundationLimits, r layoutFoundationReceipt) {
			v.MaxDeviceTables = r.DeviceTableCount - 1
		}},
		{name: "device-deltas", gdef: func() []byte { raw, _ := layoutTestAllDeviceFormatsFixture(); return raw }(), cut: func(v *layoutFoundationLimits, r layoutFoundationReceipt) {
			v.MaxDeviceDeltaValues = r.DeviceDeltaValues - 1
		}},
		{name: "carets", gdef: layoutTestMixedCaretFixture(), cut: func(v *layoutFoundationLimits, r layoutFoundationReceipt) {
			v.MaxCaretValues = r.CaretValueReferences - 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": test.gdef})
			_, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
			if err != nil {
				t.Fatal(err)
			}
			limits := layoutFoundationExactLimits(receipt)
			test.cut(&limits, receipt)
			foundation, zero, err := buildLayoutFoundation(context.Background(), set, limits)
			if !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutFoundationReceipt{}) {
				t.Fatalf("receipt=%#v foundation=%#v zero=%#v err=%v", receipt, foundation, zero, err)
			}
		})
	}
}

func TestLayoutFoundationDecodeRemainingBudgetNNMinusOne(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture()})
	rebound, err := rebindOutlineSetSource(context.Background(), set, DefaultOutlineSetLimits())
	if err != nil {
		t.Fatal(err)
	}
	source, tables, err := bindLayoutFoundationSource(set, rebound)
	if err != nil {
		t.Fatal(err)
	}
	_, points, err := copyLayoutFoundationPointCounts(context.Background(), rebound, source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := layoutFoundationDirectTableView(rebound.font, rebound.gdef)
	if err != nil {
		t.Fatal(err)
	}
	wide := defaultLayoutFoundationLimits()
	wideDecode, err := layoutFoundationDecodeLimits(wide, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, wideDecode)
	if err != nil {
		t.Fatal(err)
	}
	sourceWork, _ := checkedAdd(source.coreWorkUnits, source.outlineSetWorkUnits)
	minimumCanonical, _ := layoutFoundationCanonicalMinimumBytes(source, tables)
	foundationWorkBase, _ := checkedAdd(sourceWork, minimumCanonical)
	exactFoundationWork, _ := layoutFoundationDecodeWork(foundationWorkBase, stats)

	workLimits := wide
	workLimits.MaxWorkUnits = exactFoundationWork
	exactWorkDecode, err := layoutFoundationDecodeLimits(workLimits, source, tables)
	if err != nil || exactWorkDecode.foundationWorkBase != foundationWorkBase ||
		exactWorkDecode.foundationWorkLimit != exactFoundationWork {
		t.Fatalf("exact work decode=%#v stats=%#v err=%v", exactWorkDecode, stats, err)
	}
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, exactWorkDecode); err != nil {
		t.Fatalf("parser work N rejected: %v", err)
	}
	workLimits.MaxWorkUnits--
	belowWorkDecode, err := layoutFoundationDecodeLimits(workLimits, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	if foundation, zero, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, belowWorkDecode); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("parser work N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}

	skeleton := makeLayoutFoundationReceipt(source, tables, &gdefFoundation{}, layoutFoundationResourceStats{},
		layoutFoundationDigestPlaceholder, 0)
	ownedPoints := uint64(source.glyphCount) * 8
	retainedBase := source.outlineSetRetainedBytes + layoutFoundationFixedRetainedBytes + ownedPoints +
		layoutFoundationReceiptStringBytes(skeleton)
	exactFoundationRetained, _ := layoutFoundationDecodeRetained(retainedBase, stats)
	retainedLimits := wide
	retainedLimits.MaxRetainedBytes = exactFoundationRetained
	exactRetainedDecode, err := layoutFoundationDecodeLimits(retainedLimits, source, tables)
	if err != nil || exactRetainedDecode.foundationRetainedBase != retainedBase ||
		exactRetainedDecode.foundationRetainedLimit != exactFoundationRetained {
		t.Fatalf("exact retained decode=%#v stats=%#v err=%v", exactRetainedDecode, stats, err)
	}
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, exactRetainedDecode); err != nil {
		t.Fatalf("parser retained N rejected: %v", err)
	}
	retainedLimits.MaxRetainedBytes--
	belowRetainedDecode, err := layoutFoundationDecodeLimits(retainedLimits, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	if foundation, zero, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, belowRetainedDecode); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("parser retained N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}

	sourceWorking := layoutFoundationMaximum(source.coreWorkingBytes, source.outlineSetWorkingBytes)
	exactFoundationWorking, _ := layoutFoundationDecodeWorking(sourceWorking, uint64(source.glyphCount), stats)
	workingLimits := wide
	workingLimits.MaxWorkingBytes = exactFoundationWorking
	exactWorkingDecode, err := layoutFoundationDecodeLimits(workingLimits, source, tables)
	if err != nil || exactWorkingDecode.foundationWorkingLimit != exactFoundationWorking {
		t.Fatalf("exact working decode=%#v stats=%#v err=%v", exactWorkingDecode, stats, err)
	}
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, exactWorkingDecode); err != nil {
		t.Fatalf("parser working N rejected: %v", err)
	}
	workingLimits.MaxWorkingBytes--
	belowWorkingDecode, err := layoutFoundationDecodeLimits(workingLimits, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	if foundation, zero, err := decodeGDEFFoundation(context.Background(), raw, source.glyphCount, points, belowWorkingDecode); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("parser working N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestCopyLayoutFoundationPointCountsAllocationCheckpoints(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestHeader(0)})
	rebound, err := rebindOutlineSetSource(context.Background(), set, DefaultOutlineSetLimits())
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := bindLayoutFoundationSource(set, rebound)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelAt := range []int{1, 2} {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		counts, decodeCounts, err := copyLayoutFoundationPointCounts(ctx, rebound, source)
		if !errors.Is(err, ErrCanceled) || counts != nil || decodeCounts != nil {
			t.Fatalf("cancel=%d counts=%v decode=%v err=%v", cancelAt, counts, decodeCounts, err)
		}
	}
}

func TestLayoutFoundationLargeCompactWorkingNNMinusOne(t *testing.T) {
	const glyphCount = uint32(65_535)
	raw := layoutTestLargeClassRangeFixture()
	points := make([]uint32, glyphCount)
	source := layoutSourceBinding{
		outlineSetProfile:               OutlineSetReceiptProfileV1,
		outlineSetDescriptorHashProfile: OutlineSetDescriptorHashProfileV1,
		outlineSetDescriptorHash:        layoutFoundationTestDigest,
		outlineSetHashProfile:           OutlineSetHashProfileV1, outlineSetHash: layoutFoundationTestDigest,
		fontContentHashProfile: ContentHashProfileV1, fontContentHash: layoutFoundationTestDigest,
		glyphCount: glyphCount, coreWorkUnits: 10, outlineSetWorkUnits: 20,
		coreWorkingBytes: 1_000, outlineSetWorkingBytes: 2_000, outlineSetRetainedBytes: 4_096,
	}
	tables := [3]layoutTableBinding{
		{tag: layoutFoundationTableTags[0], present: true, fileOffset: 128, length: uint32(len(raw)),
			checksum: 1, hashProfile: TableHashProfileV1, contentHash: layoutFoundationTestDigest},
		{tag: layoutFoundationTableTags[1]}, {tag: layoutFoundationTableTags[2]},
	}
	wide := defaultLayoutFoundationLimits()
	decodeLimits, err := layoutFoundationDecodeLimits(wide, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err := decodeGDEFFoundation(context.Background(), raw, glyphCount, points, decodeLimits)
	if err != nil {
		t.Fatal(err)
	}
	wantWorking, ok := layoutFoundationDecodeWorking(2_000, uint64(glyphCount), stats)
	if !ok {
		t.Fatal("working overflow")
	}
	exact := wide
	exact.MaxWorkingBytes = wantWorking
	exactDecode, err := layoutFoundationDecodeLimits(exact, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeGDEFFoundation(context.Background(), raw, glyphCount, points, exactDecode); err != nil {
		t.Fatalf("large working N rejected: %v", err)
	}
	exact.MaxWorkingBytes--
	belowDecode, err := layoutFoundationDecodeLimits(exact, source, tables)
	if err != nil {
		t.Fatal(err)
	}
	if foundation, zero, err := decodeGDEFFoundation(context.Background(), raw, glyphCount, points, belowDecode); !errors.Is(err, ErrResourceLimit) || foundation != nil || zero != (layoutDecodeStats{}) {
		t.Fatalf("large working N-1 foundation=%#v stats=%#v err=%v", foundation, zero, err)
	}
}

func TestBuildLayoutFoundationCancellationIsAtomic(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture()})
	probe := &geometryCountingContext{}
	if _, _, err := buildLayoutFoundation(probe, set, layoutFoundationLimits{}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 20 {
		t.Fatalf("too few cancellation checkpoints: %d", probe.calls)
	}
	for cancelAt := 1; cancelAt <= probe.calls; cancelAt++ {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		foundation, receipt, err := buildLayoutFoundation(ctx, set, layoutFoundationLimits{})
		if !errors.Is(err, ErrCanceled) || foundation != nil || receipt != (layoutFoundationReceipt{}) {
			t.Fatalf("cancel=%d/%d foundation=%#v receipt=%#v err=%v", cancelAt, probe.calls, foundation, receipt, err)
		}
	}
}

func TestBuildLayoutFoundationOwnsNormalizedValuesAndSupportsConcurrentReads(t *testing.T) {
	set := layoutFoundationBuildTestSet(t, map[string][]byte{"GDEF": layoutTestClassAttachFixture()})
	foundation, receipt, err := buildLayoutFoundation(context.Background(), set, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	gdef := foundation.gdef.(*gdefFoundation)
	wantClasses := layoutTestClassValues(gdef.glyphClasses, receipt.GlyphCount)
	wantPoints := append([]uint16(nil), gdef.attachments[0].pointIndices...)
	wantCounts := append([]uint64(nil), foundation.realPointCounts...)
	record, _ := findRecord(set.source.source.font.tables, "GDEF")
	for index := uint32(0); index < record.length; index++ {
		set.source.source.font.encoded[record.offset+index] ^= 0xff
	}
	if !slices.Equal(layoutTestClassValues(gdef.glyphClasses, receipt.GlyphCount), wantClasses) || !slices.Equal(gdef.attachments[0].pointIndices, wantPoints) ||
		!slices.Equal(foundation.realPointCounts, wantCounts) || foundation.receipt != receipt {
		t.Fatal("foundation retained an operation-local raw table view")
	}

	var wait sync.WaitGroup
	errorsSeen := make(chan string, 32)
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if foundation.receipt != receipt || !slices.Equal(foundation.realPointCounts, wantCounts) ||
					!slices.Equal(layoutTestClassValues(gdef.glyphClasses, receipt.GlyphCount), wantClasses) || !slices.Equal(gdef.attachments[0].pointIndices, wantPoints) {
					errorsSeen <- "concurrent read drift"
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for message := range errorsSeen {
		t.Fatal(message)
	}
}

func TestLayoutFoundationCanonicalDescriptorAndReceiptFixtures(t *testing.T) {
	descriptor := layoutFoundationDescriptorCanonicalJSON()
	layoutFoundationRequireFixture(t, "testdata/layout-foundation-profile-v1.canonical.json", descriptor,
		"5940ba79dcadaee12a9a30e610128bcb2326e8ca4e04ec0830ec3095e4c1265f")
	minimalSet := layoutFoundationBuildTestSet(t, map[string][]byte{"GSUB": {0, 1, 0, 0}, "GPOS": {0, 1, 0, 0}})
	_, minimal, err := buildLayoutFoundation(context.Background(), minimalSet, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	minimalJSON, _ := json.Marshal(minimal)
	layoutFoundationRequireFixture(t, "testdata/minimal-layout-foundation-receipt-v1.canonical.json", minimalJSON,
		"148b045372bdc2beb2854c3ec928ff814a30c0d3f13ebf1c9affe53d3e15b426")
	fullSet := layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": {1, 2, 3, 4}, "GPOS": {5, 6, 7, 8},
	})
	_, full, err := buildLayoutFoundation(context.Background(), fullSet, layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	fullJSON, _ := json.Marshal(full)
	layoutFoundationRequireFixture(t, "testdata/full-layout-foundation-receipt-v1.canonical.json", fullJSON,
		"b40ab670561621dc9474a5d2ffe06f3dab0001766e8b024a45f4034a273b28e7")
}

func layoutFoundationBuildTestSet(t testing.TB, layout map[string][]byte) *OutlineSet {
	t.Helper()
	bodies := make([][]byte, 8)
	bodies[5] = makeOutlineSimpleGlyph([]uint16{3}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 1, y: 1, onCurve: true},
		{x: 2, y: 1, onCurve: true}, {x: 3, y: 0, onCurve: true},
	}, nil, false, 0)
	tables := outlineTestTables(t, bodies, outlineTestMaxp{points: 4, contours: 1})
	setGeometryDeclarations(tables, geometryTestBBox{xMax: 3, yMax: 1}, 0, 497, 3)
	for tag, raw := range layout {
		tables[tag] = append([]byte(nil), raw...)
	}
	outline := decodeGeometryTestOutline(t, tables)
	geometry, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func layoutFoundationDeviceHeavyFixture() []byte {
	result := layoutTestHeader(0)
	ligatureOffset := len(result)
	result = append(result, layoutTestWords(
		6, 1, 12, // LigCaretList header.
		1, 1, 5, // Coverage format 1 for glyph 5.
		1, 4, // One LigGlyph caret at relative offset 4.
		3, 100, 6, // CaretValue format 3; Device begins immediately after it.
		1, 4_096, 3, // 4,096 signed eight-bit deltas.
	)...)
	result = append(result, make([]byte, 4_096)...)
	layoutTestPut16(result, 8, uint16(ligatureOffset))
	return result
}

func layoutFoundationExactLimits(receipt layoutFoundationReceipt) layoutFoundationLimits {
	return layoutFoundationLimits{
		MaxLayoutTableBytes: layoutFoundationReceiptLayoutTableBytes(receipt),
		MaxNodes:            receipt.NodeCount, MaxEdges: receipt.EdgeCount,
		MaxAliasReferences: receipt.AliasReferenceCount, MaxStructuralDepth: receipt.MaxStructuralDepth,
		MaxCoverageTables: receipt.CoverageTableCount, MaxCoveredGlyphs: receipt.CoveredGlyphInstances,
		MaxClassDefTables: receipt.ClassDefTableCount, MaxClassAssignments: receipt.ClassAssignmentInstances,
		MaxDeviceTables: receipt.DeviceTableCount, MaxDeviceDeltaValues: receipt.DeviceDeltaValues,
		MaxAttachPointTables: receipt.AttachPointTableCount, MaxAttachmentPoints: receipt.AttachmentPointIndices,
		MaxLigGlyphTables: receipt.LigGlyphTableCount, MaxCaretValues: receipt.CaretValueReferences,
		MaxMarkGlyphSets:        receipt.MarkGlyphSetReferences,
		MaxCanonicalBinaryBytes: receipt.CanonicalBinaryBytes,
		MaxWorkUnits:            receipt.WorkUnits, MaxWorkingBytes: receipt.WorkingBytes,
		MaxRetainedBytes: receipt.RetainedBytes,
	}
}

func layoutFoundationRequireFixture(t testing.TB, path string, value []byte, wantDigest string) {
	t.Helper()
	fixture, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\ngot=%s", path, err, value)
	}
	want := append(append([]byte(nil), value...), '\n')
	if !bytes.Equal(fixture, want) {
		t.Fatalf("fixture drift %s\ngot=%s", path, value)
	}
	digest := sha256.Sum256(value)
	gotDigest := hex.EncodeToString(digest[:])
	if wantDigest != "" && gotDigest != wantDigest {
		t.Fatalf("fixture digest %s=%s want=%s", path, gotDigest, wantDigest)
	}
	if strings.Contains(string(value), "\\u003c") {
		t.Fatalf("fixture %s unexpectedly contains escaped ordering syntax", path)
	}
}
