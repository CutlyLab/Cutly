package sfntglyf

import (
	"errors"
	"math"
	"testing"
)

func TestLayoutFoundationResourceFormulasAndNNMinusOne(t *testing.T) {
	source := layoutSourceBinding{
		glyphCount: 3, coreWorkUnits: 10, outlineSetWorkUnits: 20,
		coreWorkingBytes: 1_000, outlineSetWorkingBytes: 2_000, outlineSetRetainedBytes: 10_000,
	}
	stats := layoutFoundationResourceStats{
		layoutTableBytes: 12, nodes: 2, edges: 3, aliasReferences: 1, maxStructuralDepth: 2, directBytes: 20,
		coverageTables: 1, coverageFormat1Glyphs: 2, coverageRanges: 1, coveredGlyphs: 5,
		classDefTables: 1, classFormat1Values: 3, classRanges: 1, classAssignments: 6,
		deviceTables: 1, deviceDeltaValues: 3, deviceWords: 2,
		attachLists: 1, attachPointTables: 2, attachmentGlyphReferences: 2, attachmentPoints: 4,
		ligCaretLists: 1, ligGlyphTables: 1, ligatureGlyphReferences: 1,
		caretValueTables: 2, caretValueReferences: 2, caretPointReferences: 1,
		markGlyphSetsTables: 1, markGlyphSetReferences: 1,
	}
	receipt := layoutFoundationReceipt{Profile: "p", FoundationHash: "h"}
	const canonicalBytes = 100
	resources, ok := layoutFoundationComputeResources(source, stats, canonicalBytes, receipt)
	if !ok {
		t.Fatal("resource computation overflowed")
	}
	wantWork := uint64(30 + 2*64 + 3*24 + 1*8 + 20 + 5*4 + (3+1)*8 + 2*4 + 2*8 + 4*4 + 1*8 + 2*8 + 1*8 + canonicalBytes)
	if resources.workUnits != wantWork {
		t.Fatalf("work = %d, want %d", resources.workUnits, wantWork)
	}
	wantOwnModel := uint64(32_768 + 2*128 + 3*48 + 3*8 +
		2*8 + 1*24 + 3*8 + 1*24 + 2*4 + 3*2 + 2*32 + 4*4 + 1*32 + 2*32 + 1*8)
	wantWorking := uint64(65_536 + wantOwnModel + 2*256 + 3*192 + 1*16 + 3*16)
	if resources.workingBytes != wantWorking {
		t.Fatalf("working = %d, want %d", resources.workingBytes, wantWorking)
	}
	wantRetained := uint64(10_000 + wantOwnModel)
	wantRetained += layoutFoundationReceiptStringBytes(receipt)
	if resources.retainedBytes != wantRetained {
		t.Fatalf("retained = %d, want %d", resources.retainedBytes, wantRetained)
	}

	limits := defaultLayoutFoundationLimits()
	limits.MaxWorkUnits, limits.MaxWorkingBytes, limits.MaxRetainedBytes = resources.workUnits, resources.workingBytes, resources.retainedBytes
	limits.MaxCanonicalBinaryBytes = canonicalBytes
	if _, err := checkLayoutFoundationResources(source, stats, canonicalBytes, receipt, limits); err != nil {
		t.Fatalf("exact N limits rejected: %v", err)
	}
	tests := []struct {
		name string
		cut  func(*layoutFoundationLimits)
	}{
		{"work", func(v *layoutFoundationLimits) { v.MaxWorkUnits-- }},
		{"working", func(v *layoutFoundationLimits) { v.MaxWorkingBytes-- }},
		{"retained", func(v *layoutFoundationLimits) { v.MaxRetainedBytes-- }},
		{"canonical", func(v *layoutFoundationLimits) { v.MaxCanonicalBinaryBytes-- }},
		{"nodes", func(v *layoutFoundationLimits) { v.MaxNodes = stats.nodes - 1 }},
		{"edges", func(v *layoutFoundationLimits) { v.MaxEdges = stats.edges - 1 }},
		{"aliases", func(v *layoutFoundationLimits) { v.MaxAliasReferences = stats.aliasReferences - 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			below := limits
			test.cut(&below)
			if _, err := checkLayoutFoundationResources(source, stats, canonicalBytes, receipt, below); !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("N-1 error = %v", err)
			}
		})
	}
}

func TestLayoutFoundationResourceOverflow(t *testing.T) {
	if _, ok := layoutFoundationWorkUnits(layoutSourceBinding{coreWorkUnits: math.MaxUint64, outlineSetWorkUnits: 1}, layoutFoundationResourceStats{}, 0); ok {
		t.Fatal("source work overflow accepted")
	}
	if _, ok := layoutFoundationWeightedSum(0, layoutFoundationTerm{math.MaxUint64, 2}); ok {
		t.Fatal("weighted overflow accepted")
	}
}
