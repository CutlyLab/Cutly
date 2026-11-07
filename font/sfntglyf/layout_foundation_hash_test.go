package sfntglyf

import (
	"context"
	"errors"
	"testing"
)

const layoutFoundationTestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func testLayoutFoundationCanonicalEvidence() layoutFoundationCanonicalEvidence {
	source := layoutSourceBinding{
		outlineSetProfile:               OutlineSetReceiptProfileV1,
		outlineSetDescriptorHashProfile: OutlineSetDescriptorHashProfileV1,
		outlineSetDescriptorHash:        layoutFoundationTestDigest,
		outlineSetHashProfile:           OutlineSetHashProfileV1, outlineSetHash: layoutFoundationTestDigest,
		fontContentHashProfile: ContentHashProfileV1, fontContentHash: layoutFoundationTestDigest,
		glyphCount: 2, realPointInstances: 3,
	}
	tables := [3]layoutTableBinding{
		{tag: layoutFoundationTableTags[0], present: true, fileOffset: 128, length: 12, checksum: 7, hashProfile: TableHashProfileV1, contentHash: layoutFoundationTestDigest},
		{tag: layoutFoundationTableTags[1]},
		{tag: layoutFoundationTableTags[2]},
	}
	keys := []layoutFoundationCanonicalNodeKey{
		{tag: layoutFoundationTableTags[0], offset: 0, kind: 1, param0: 1},
		{tag: layoutFoundationTableTags[0], offset: 4, kind: 2, param0: 2},
		{tag: layoutFoundationTableTags[0], offset: 8, kind: 3, param0: 3},
	}
	nodes := []layoutFoundationCanonicalNode{
		{key: keys[2], spanStart: 8, spanLength: 4, payload: []byte{8, 9, 10, 11}},
		{key: keys[0], spanStart: 0, spanLength: 4, payload: []byte{0, 1, 2, 3}},
		{key: keys[1], spanStart: 4, spanLength: 4, payload: []byte{4, 5, 6, 7}},
	}
	edges := []layoutFoundationCanonicalEdge{
		{from: keys[1], role: 2, rawOffset: 4, to: keys[2]},
		{from: keys[0], role: 1, ordinal: 1, rawOffset: 8, to: keys[2]},
		{from: keys[0], role: 1, rawOffset: 4, to: keys[1]},
	}
	return layoutFoundationCanonicalEvidence{
		source: source, tables: tables, gdefPresent: true, gdefMajorVersion: 1, gdefMinorVersion: 0,
		nodes: nodes, edges: edges,
		resources: layoutFoundationResourceStats{layoutTableBytes: 12, nodes: 3, edges: 3, aliasReferences: 1, directBytes: 12},
	}
}

func TestLayoutFoundationHashGoldenAndDiscoveryIndependence(t *testing.T) {
	evidence := testLayoutFoundationCanonicalEvidence()
	hash, count, err := hashLayoutFoundation(context.Background(), evidence, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "sha256:1e2846ea14e63d8d6e2f8c1aa057f183523b14c607f084e0b4394020b667806f"
	const wantCount uint64 = 1495
	if hash != wantHash || count != wantCount {
		t.Fatalf("hash/count = %q/%d", hash, count)
	}

	reversed := evidence
	reversed.nodes = append([]layoutFoundationCanonicalNode(nil), evidence.nodes...)
	reversed.edges = append([]layoutFoundationCanonicalEdge(nil), evidence.edges...)
	for left, right := 0, len(reversed.nodes)-1; left < right; left, right = left+1, right-1 {
		reversed.nodes[left], reversed.nodes[right] = reversed.nodes[right], reversed.nodes[left]
	}
	for left, right := 0, len(reversed.edges)-1; left < right; left, right = left+1, right-1 {
		reversed.edges[left], reversed.edges[right] = reversed.edges[right], reversed.edges[left]
	}
	reorderedHash, reorderedCount, err := hashLayoutFoundation(context.Background(), reversed, count)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedHash != hash || reorderedCount != count {
		t.Fatalf("discovery order changed hash/count: %q/%d", reorderedHash, reorderedCount)
	}

	mutated := evidence
	mutated.nodes = append([]layoutFoundationCanonicalNode(nil), evidence.nodes...)
	mutated.nodes[0].payload = append([]byte(nil), evidence.nodes[0].payload...)
	mutated.nodes[0].payload[0] ^= 0xff
	mutatedHash, _, err := hashLayoutFoundation(context.Background(), mutated, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if mutatedHash == hash {
		t.Fatal("direct-span payload mutation did not change hash")
	}
}

func TestLayoutFoundationHashCanonicalLimitAndCancellation(t *testing.T) {
	evidence := testLayoutFoundationCanonicalEvidence()
	wantHash, count, err := hashLayoutFoundation(context.Background(), evidence, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, gotCount, err := hashLayoutFoundation(context.Background(), evidence, count)
	if err != nil || gotHash != wantHash || gotCount != count {
		t.Fatalf("exact limit = %q/%d/%v", gotHash, gotCount, err)
	}
	if _, _, err := hashLayoutFoundation(context.Background(), evidence, count-1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("N-1 error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashLayoutFoundation(canceled, evidence, 1<<20); !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestLayoutFoundationCanonicalAllocationCheckpoints(t *testing.T) {
	evidence := testLayoutFoundationCanonicalEvidence()
	gdef := &gdefFoundation{
		majorVersion: 1,
		nodes: []layoutNodeEvidence{
			{tableTag: layoutGDEFTag, start: 0, end: 4, kind: 1, paramFirst: 1},
			{tableTag: layoutGDEFTag, start: 4, end: 8, kind: 2, paramFirst: 2},
			{tableTag: layoutGDEFTag, start: 8, end: 12, kind: 3, paramFirst: 3},
		},
		edges: []layoutEdgeEvidence{
			{tableTag: layoutGDEFTag, parentStart: 0, childStart: 4, role: 1, rawOffset: 4},
			{tableTag: layoutGDEFTag, parentStart: 0, childStart: 8, role: 1, ordinal: 1, rawOffset: 8},
			{tableTag: layoutGDEFTag, parentStart: 4, childStart: 8, role: 2, rawOffset: 4},
		},
	}
	for _, cancelAt := range []int{1, 3} {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		got, err := layoutFoundationCanonicalEvidenceFromGDEF(ctx, evidence.source, evidence.tables,
			gdef, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, evidence.resources)
		if !errors.Is(err, ErrCanceled) || got.gdef != nil || len(got.nodes) != 0 || len(got.edges) != 0 {
			t.Fatalf("projection cancel=%d evidence=%#v err=%v", cancelAt, got, err)
		}
	}

	for _, cancelAt := range []int{2, 3} {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		nodes, edges, err := normalizeLayoutFoundationCanonicalEvidence(ctx, evidence)
		if !errors.Is(err, ErrCanceled) || nodes != nil || edges != nil {
			t.Fatalf("normalize cancel=%d nodes=%v edges=%v err=%v", cancelAt, nodes, edges, err)
		}
	}
}

func TestLayoutFoundationDigestRequiresLowercase(t *testing.T) {
	if !validLayoutFoundationDigest(layoutFoundationTestDigest) {
		t.Fatal("valid lowercase digest rejected")
	}
	upper := "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if validLayoutFoundationDigest(upper) {
		t.Fatal("uppercase digest accepted")
	}
}

func TestLayoutFoundationHashRejectsOverlappingCanonicalSpans(t *testing.T) {
	evidence := testLayoutFoundationCanonicalEvidence()
	evidence.nodes[1].spanLength = 5
	if _, _, err := hashLayoutFoundation(context.Background(), evidence, 1<<20); !errors.Is(err, ErrInvariantFailed) {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestLayoutCancelableSortAndLargeCanonicalCancellation(t *testing.T) {
	values := make([]int, 4_096)
	for index := range values {
		values[index] = len(values) - index
	}
	probe := &geometryCountingContext{}
	if err := layoutCancelableSort(probe, "test/sort", values, func(left, right int) bool {
		return values[left] < values[right]
	}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 4 {
		t.Fatalf("sort cancellation checkpoints=%d", probe.calls)
	}
	for _, cancelAt := range []int{1, 2, probe.calls / 2, probe.calls} {
		input := make([]int, 4_096)
		for index := range input {
			input[index] = len(input) - index
		}
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		if err := layoutCancelableSort(ctx, "test/sort", input, func(left, right int) bool {
			return input[left] < input[right]
		}); !errors.Is(err, ErrCanceled) {
			t.Fatalf("sort cancel=%d/%d err=%v", cancelAt, probe.calls, err)
		}
	}

	evidence := testLargeLayoutFoundationCanonicalEvidence(1_024)
	hashProbe := &geometryCountingContext{}
	if _, _, err := hashLayoutFoundation(hashProbe, evidence, 1<<24); err != nil {
		t.Fatal(err)
	}
	if hashProbe.calls < 20 {
		t.Fatalf("hash cancellation checkpoints=%d", hashProbe.calls)
	}
	for _, cancelAt := range []int{1, 2, 3, hashProbe.calls / 2, hashProbe.calls - 1, hashProbe.calls} {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		hash, count, err := hashLayoutFoundation(ctx, evidence, 1<<24)
		if !errors.Is(err, ErrCanceled) || hash != "" || count != 0 {
			t.Fatalf("hash cancel=%d/%d hash=%q count=%d err=%v", cancelAt, hashProbe.calls, hash, count, err)
		}
	}
}

func testLargeLayoutFoundationCanonicalEvidence(count int) layoutFoundationCanonicalEvidence {
	evidence := testLayoutFoundationCanonicalEvidence()
	payload := make([]byte, count)
	keys := make([]layoutFoundationCanonicalNodeKey, count)
	nodes := make([]layoutFoundationCanonicalNode, count)
	for index := 0; index < count; index++ {
		keys[index] = layoutFoundationCanonicalNodeKey{
			tag: layoutFoundationTableTags[0], offset: uint32(index), kind: 1, param0: uint64(index),
		}
		nodes[count-index-1] = layoutFoundationCanonicalNode{
			key: keys[index], spanStart: uint32(index), spanLength: 1, payload: payload[index : index+1],
		}
	}
	edges := make([]layoutFoundationCanonicalEdge, count-1)
	for index := range edges {
		edges[len(edges)-index-1] = layoutFoundationCanonicalEdge{
			from: keys[index+1], role: 1, ordinal: uint32(index), rawOffset: 1, to: keys[0],
		}
	}
	evidence.tables[0].length = uint32(count)
	evidence.nodes, evidence.edges = nodes, edges
	evidence.resources = layoutFoundationResourceStats{
		layoutTableBytes: uint64(count), nodes: uint64(count), edges: uint64(len(edges)),
		aliasReferences: uint64(len(edges) - 1), directBytes: uint64(count),
	}
	return evidence
}
