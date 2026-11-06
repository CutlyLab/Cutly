package sfntglyf

import (
	"bytes"
	"errors"
	"testing"
)

func TestLayoutFoundationLimitsNormalizeAndHardBoundaries(t *testing.T) {
	got, err := normalizeLayoutFoundationLimits(layoutFoundationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultLayoutFoundationLimits() {
		t.Fatalf("zero limits did not normalize to defaults: %#v", got)
	}

	tests := []struct {
		name string
		hard uint64
		set  func(*layoutFoundationLimits, uint64)
	}{
		{"table_bytes", hardMaxLayoutFoundationTableBytes, func(v *layoutFoundationLimits, n uint64) { v.MaxLayoutTableBytes = n }},
		{"nodes", hardMaxLayoutFoundationNodes, func(v *layoutFoundationLimits, n uint64) { v.MaxNodes = n }},
		{"edges", hardMaxLayoutFoundationEdges, func(v *layoutFoundationLimits, n uint64) { v.MaxEdges = n }},
		{"aliases", hardMaxLayoutFoundationAliases, func(v *layoutFoundationLimits, n uint64) { v.MaxAliasReferences = n }},
		{"coverage_tables", hardMaxLayoutFoundationCoverageTables, func(v *layoutFoundationLimits, n uint64) { v.MaxCoverageTables = n }},
		{"coverage_glyphs", hardMaxLayoutFoundationCoverageGlyphs, func(v *layoutFoundationLimits, n uint64) { v.MaxCoveredGlyphs = n }},
		{"class_tables", hardMaxLayoutFoundationClassDefTables, func(v *layoutFoundationLimits, n uint64) { v.MaxClassDefTables = n }},
		{"class_assignments", hardMaxLayoutFoundationClassAssignments, func(v *layoutFoundationLimits, n uint64) { v.MaxClassAssignments = n }},
		{"device_tables", hardMaxLayoutFoundationDeviceTables, func(v *layoutFoundationLimits, n uint64) { v.MaxDeviceTables = n }},
		{"device_deltas", hardMaxLayoutFoundationDeviceDeltas, func(v *layoutFoundationLimits, n uint64) { v.MaxDeviceDeltaValues = n }},
		{"attach_tables", hardMaxLayoutFoundationAttachPointTables, func(v *layoutFoundationLimits, n uint64) { v.MaxAttachPointTables = n }},
		{"attachment_points", hardMaxLayoutFoundationAttachmentPoints, func(v *layoutFoundationLimits, n uint64) { v.MaxAttachmentPoints = n }},
		{"lig_tables", hardMaxLayoutFoundationLigGlyphTables, func(v *layoutFoundationLimits, n uint64) { v.MaxLigGlyphTables = n }},
		{"carets", hardMaxLayoutFoundationCaretValues, func(v *layoutFoundationLimits, n uint64) { v.MaxCaretValues = n }},
		{"mark_sets", hardMaxLayoutFoundationMarkGlyphSets, func(v *layoutFoundationLimits, n uint64) { v.MaxMarkGlyphSets = n }},
		{"canonical", hardMaxLayoutFoundationCanonicalBytes, func(v *layoutFoundationLimits, n uint64) { v.MaxCanonicalBinaryBytes = n }},
		{"work", hardMaxLayoutFoundationWorkUnits, func(v *layoutFoundationLimits, n uint64) { v.MaxWorkUnits = n }},
		{"working", hardMaxLayoutFoundationWorkingBytes, func(v *layoutFoundationLimits, n uint64) { v.MaxWorkingBytes = n }},
		{"retained", hardMaxLayoutFoundationRetainedBytes, func(v *layoutFoundationLimits, n uint64) { v.MaxRetainedBytes = n }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			at := defaultLayoutFoundationLimits()
			test.set(&at, test.hard)
			if _, err := normalizeLayoutFoundationLimits(at); err != nil {
				t.Fatalf("hard limit rejected: %v", err)
			}
			over := defaultLayoutFoundationLimits()
			test.set(&over, test.hard+1)
			if _, err := normalizeLayoutFoundationLimits(over); !errors.Is(err, ErrLimitsInvalid) {
				t.Fatalf("hard+1 error = %v", err)
			}
		})
	}
	depth := defaultLayoutFoundationLimits()
	depth.MaxStructuralDepth = hardMaxLayoutFoundationDepth
	if _, err := normalizeLayoutFoundationLimits(depth); err != nil {
		t.Fatalf("hard depth rejected: %v", err)
	}
	depth.MaxStructuralDepth++
	if _, err := normalizeLayoutFoundationLimits(depth); !errors.Is(err, ErrLimitsInvalid) {
		t.Fatalf("hard depth+1 error = %v", err)
	}
}

func TestLayoutFoundationPrivateDescriptorGolden(t *testing.T) {
	first := layoutFoundationDescriptorCanonicalJSON()
	second := layoutFoundationDescriptorCanonicalJSON()
	if len(first) == 0 || !bytes.Equal(first, second) || &first[0] == &second[0] {
		t.Fatal("canonical descriptor must be deterministic and defensively copied")
	}
	const wantIdentity = "sha256:1dfb1ee3f49e8c24868a965946dde4913f9906d9fe16afda7175ba33ef660ec0"
	if layoutFoundationDescriptorIdentityV1 != wantIdentity {
		t.Fatalf("descriptor identity = %q", layoutFoundationDescriptorIdentityV1)
	}
	if !bytes.Contains(first, []byte(`"capability_semantics":"This is an internal GDEF foundation only.`)) ||
		!bytes.Contains(first, []byte(`"max_structural_depth":64`)) {
		t.Fatalf("descriptor omitted the private/capability or limits contract: %s", first)
	}
}

func TestLayoutFoundationReceiptIsComparable(t *testing.T) {
	left, right := layoutFoundationReceipt{}, layoutFoundationReceipt{}
	if left != right {
		t.Fatal("zero receipts differ")
	}
}
