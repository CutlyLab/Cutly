package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
)

func TestBuildLayoutGraphPublishesAtomicImmutableViews(t *testing.T) {
	set := layoutGraphFullTestSet(t)
	graph, receipt, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if graph == nil || graph.Receipt() != receipt || graph.GlyphCount() != set.GlyphCount() ||
		receipt.Profile != LayoutGraphReceiptProfileV1 || receipt.DescriptorHash != LayoutGraphDescriptorHash() ||
		receipt.CapabilityStatus != LayoutGraphCapabilityStatusV1 ||
		receipt.LookupExecutionStatus != LayoutGraphLookupExecutionStatusV1 ||
		!validLayoutFoundationDigest(receipt.GraphHash) || !validLayoutFoundationDigest(receipt.GDEFFoundationHash) ||
		!validLayoutFoundationDigest(receipt.GSUBFoundationHash) || !validLayoutFoundationDigest(receipt.GPOSFoundationHash) {
		t.Fatalf("graph=%#v receipt=%#v", graph, receipt)
	}

	gdef, ok := graph.Table(LayoutTableGDEF)
	if !ok || !gdef.Present() || gdef.Tag() != LayoutTableGDEF || gdef.NodeCount() == 0 {
		t.Fatalf("GDEF view=%#v/%t receipt=%#v", gdef, ok, gdef.Receipt())
	}
	if class, ok := gdef.GlyphClass(5); !ok || class != 4 {
		t.Fatalf("glyph class=%d/%t", class, ok)
	}
	if _, ok := gdef.GlyphClass(graph.GlyphCount()); ok {
		t.Fatal("GDEF view accepted out-of-domain glyph")
	}

	for _, tag := range []LayoutTableTag{LayoutTableGSUB, LayoutTableGPOS} {
		table, ok := graph.Table(tag)
		if !ok || !table.Present() || table.MajorVersion() != 1 || table.ScriptCount() == 0 ||
			table.FeatureCount() == 0 || table.LookupCount() != 2 || table.NodeCount() == 0 ||
			table.EdgeCount() == 0 || table.LogicalEdgeCount() != 1 || table.LogicalDepth() != 1 ||
			table.LogicalExpansion() != 1 {
			t.Fatalf("%s view=%#v receipt=%#v", tag, table, table.Receipt())
		}
		script, ok := table.Script(0)
		if !ok || script.Tag().String() != "DFLT" {
			t.Fatalf("%s script=%#v/%t", tag, script, ok)
		}
		language, ok := script.DefaultLanguage()
		if !ok || !language.IsDefault() || language.FeatureCount() != 1 {
			t.Fatalf("%s language=%#v/%t", tag, language, ok)
		}
		feature, ok := table.Feature(0)
		if !ok || feature.LookupCount() != 2 || feature.ParameterKind() != LayoutFeatureParametersNone {
			t.Fatalf("%s feature=%#v/%t", tag, feature, ok)
		}
		lookup, ok := table.Lookup(0)
		if !ok || lookup.Index() != 0 || lookup.SubtableCount() != 1 {
			t.Fatalf("%s lookup=%#v/%t", tag, lookup, ok)
		}
		edge, ok := table.LogicalEdge(0)
		if !ok || edge.FromLookup() != 0 || edge.ToLookup() != 1 || edge.Subtable() != 0 || edge.RecordOrdinal() != 0 {
			t.Fatalf("%s logical edge=%#v/%t", tag, edge, ok)
		}
		if first, ok := table.TopologicalLookup(0); !ok || first != 0 {
			t.Fatalf("%s topology first=%d/%t", tag, first, ok)
		}
		node, ok := table.Node(0)
		if !ok || node.TableTag().String() != string(tag) || node.End() <= node.Start() || node.Kind().String() == "unknown" {
			t.Fatalf("%s node=%#v/%t", tag, node, ok)
		}
		physical, ok := table.Edge(0)
		if !ok || physical.TableTag().String() != string(tag) || physical.ChildStart() == physical.ParentStart() ||
			physical.Role().String() == "unknown" {
			t.Fatalf("%s physical edge=%#v/%t", tag, physical, ok)
		}
	}
	if _, ok := graph.Table(LayoutTableTag("BASE")); ok {
		t.Fatal("unknown table tag returned a view")
	}

	want := receipt
	wantLogical := make([]LayoutLogicalEdgeView, 0, 2)
	for _, tag := range []LayoutTableTag{LayoutTableGSUB, LayoutTableGPOS} {
		table, _ := graph.Table(tag)
		edge, _ := table.LogicalEdge(0)
		wantLogical = append(wantLogical, edge)
	}
	record, ok := findRecord(set.source.source.font.tables, "GPOS")
	if !ok {
		t.Fatal("missing GPOS")
	}
	for index := uint32(0); index < record.length; index++ {
		set.source.source.font.encoded[record.offset+index] ^= 0xff
	}
	if graph.Receipt() != want {
		t.Fatal("published graph receipt changed after source mutation")
	}
	for index, tag := range []LayoutTableTag{LayoutTableGSUB, LayoutTableGPOS} {
		table, _ := graph.Table(tag)
		edge, ok := table.LogicalEdge(0)
		if !ok || edge != wantLogical[index] {
			t.Fatalf("%s view changed after source mutation", tag)
		}
	}
	if rebuilt, zero, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{}); err == nil || rebuilt != nil || zero != (LayoutGraphReceipt{}) {
		t.Fatalf("mutated source published: graph=%#v receipt=%#v err=%v", rebuilt, zero, err)
	}
}

func TestBuildLayoutGraphAbsentTablesAndFailureZero(t *testing.T) {
	absentSet := layoutFoundationBuildTestSet(t, map[string][]byte{})
	graph, receipt, err := BuildLayoutGraph(context.Background(), absentSet, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if graph == nil || receipt.Tables[0].Present || receipt.Tables[1].Present || receipt.Tables[2].Present {
		t.Fatalf("absent graph=%#v receipt=%#v", graph, receipt)
	}
	for _, tag := range []LayoutTableTag{LayoutTableGDEF, LayoutTableGSUB, LayoutTableGPOS} {
		table, ok := graph.Table(tag)
		if !ok || table.Present() || table.NodeCount() != 0 || table.LookupCount() != 0 {
			t.Fatalf("absent %s=%#v/%t", tag, table, ok)
		}
	}

	for _, test := range []struct {
		name   string
		layout map[string][]byte
	}{
		{name: "gdef", layout: map[string][]byte{"GDEF": {0, 1, 0, 0}}},
		{name: "gsub", layout: map[string][]byte{"GSUB": {0, 1, 0, 0}}},
		{name: "gpos", layout: map[string][]byte{"GPOS": {0, 1, 0, 0}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := layoutFoundationBuildTestSet(t, test.layout)
			result, zero, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
			if err == nil || result != nil || zero != (LayoutGraphReceipt{}) || !errors.Is(err, ErrLayoutGraphInvalid) {
				t.Fatalf("malformed table published: graph=%#v receipt=%#v err=%v", result, zero, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, zero, err := BuildLayoutGraph(ctx, absentSet, LayoutGraphLimits{}); !errors.Is(err, ErrCanceled) || result != nil || zero != (LayoutGraphReceipt{}) {
		t.Fatalf("canceled graph published: graph=%#v receipt=%#v err=%v", result, zero, err)
	}
	if result, zero, err := BuildLayoutGraph(nil, absentSet, LayoutGraphLimits{}); !errors.Is(err, ErrContextInvalid) || result != nil || zero != (LayoutGraphReceipt{}) {
		t.Fatalf("nil context graph published: graph=%#v receipt=%#v err=%v", result, zero, err)
	}
}

func TestBuildLayoutGraphExactResourceBoundaries(t *testing.T) {
	set := layoutGraphFullTestSet(t)
	_, receipt, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := LayoutGraphLimits{
		MaxValidationCanonicalBinaryBytes: receipt.ValidationCanonicalBinaryBytes,
		MaxCanonicalBinaryBytes:           receipt.CanonicalBinaryBytes,
		MaxWorkUnits:                      receipt.WorkUnits, MaxWorkingBytes: receipt.WorkingBytes,
		MaxRetainedBytes: receipt.RetainedBytes,
	}
	if graph, got, err := BuildLayoutGraph(context.Background(), set, exact); err != nil || graph == nil || got != receipt {
		t.Fatalf("exact limits receipt_equal=%t graph=%#v err=%v", got == receipt, graph, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*LayoutGraphLimits)
	}{
		{name: "validation_canonical", cut: func(v *LayoutGraphLimits) { v.MaxValidationCanonicalBinaryBytes-- }},
		{name: "canonical", cut: func(v *LayoutGraphLimits) { v.MaxCanonicalBinaryBytes-- }},
		{name: "work", cut: func(v *LayoutGraphLimits) { v.MaxWorkUnits-- }},
		{name: "working", cut: func(v *LayoutGraphLimits) { v.MaxWorkingBytes-- }},
		{name: "retained", cut: func(v *LayoutGraphLimits) { v.MaxRetainedBytes-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			below := exact
			test.cut(&below)
			if result, zero, err := BuildLayoutGraph(context.Background(), set, below); !errors.Is(err, ErrResourceLimit) || result != nil || zero != (LayoutGraphReceipt{}) {
				t.Fatalf("N-1 published: graph=%#v receipt=%#v err=%v", result, zero, err)
			}
		})
	}

	over := DefaultLayoutGraphLimits()
	over.MaxCanonicalBinaryBytes = hardLayoutGraphCanonicalBytes + 1
	if result, zero, err := BuildLayoutGraph(context.Background(), set, over); !errors.Is(err, ErrLimitsInvalid) || result != nil || zero != (LayoutGraphReceipt{}) {
		t.Fatalf("over-hard limit published: graph=%#v receipt=%#v err=%v", result, zero, err)
	}
}

func TestLayoutGraphConcurrentReadViews(t *testing.T) {
	graph, receipt, err := BuildLayoutGraph(context.Background(), layoutGraphFullTestSet(t), LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	failures := make(chan string, 32)
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if graph.Receipt() != receipt {
					failures <- "receipt drift"
					return
				}
				for _, tag := range []LayoutTableTag{LayoutTableGDEF, LayoutTableGSUB, LayoutTableGPOS} {
					table, ok := graph.Table(tag)
					if !ok || table.Receipt() != receipt.Tables[map[LayoutTableTag]int{LayoutTableGDEF: 0, LayoutTableGSUB: 1, LayoutTableGPOS: 2}[tag]] {
						failures <- "table drift"
						return
					}
				}
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

func TestBuildLayoutGraphCancellationIsAtomicAcrossPublicHash(t *testing.T) {
	set := layoutGraphFullTestSet(t)
	probe := &geometryCountingContext{}
	if graph, _, err := BuildLayoutGraph(probe, set, LayoutGraphLimits{}); err != nil || graph == nil {
		t.Fatalf("probe build: graph=%#v err=%v", graph, err)
	}
	checkpoints := []int{1, probe.calls / 2, probe.calls - 1, probe.calls}
	seen := make(map[int]bool)
	for _, cancelAt := range checkpoints {
		if cancelAt <= 0 || seen[cancelAt] {
			continue
		}
		seen[cancelAt] = true
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		graph, zero, err := BuildLayoutGraph(ctx, set, LayoutGraphLimits{})
		if !errors.Is(err, ErrCanceled) || graph != nil || zero != (LayoutGraphReceipt{}) {
			t.Fatalf("cancelAt=%d/%d published: graph=%#v receipt=%#v err=%v", cancelAt, probe.calls, graph, zero, err)
		}
	}
}

func TestLayoutGraphCanonicalFixtures(t *testing.T) {
	descriptor := LayoutGraphDescriptorCanonicalJSON()
	layoutGraphRequireFixture(t, "testdata/layout-graph-profile-v1.canonical.json", descriptor,
		"676997cdf4e3ffb8bad512f39294ee6eb58623d25213f19abab7b6f951cbad23")

	_, minimal, err := BuildLayoutGraph(context.Background(), layoutFoundationBuildTestSet(t, map[string][]byte{}), LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	minimalJSON, _ := json.Marshal(minimal)
	layoutGraphRequireFixture(t, "testdata/minimal-layout-graph-receipt-v1.canonical.json", minimalJSON,
		"32b1b0ab8e0d781b08a77775c8205b8970202d09e03b81c8cef0171ff4d88eb6")

	_, full, err := BuildLayoutGraph(context.Background(), layoutGraphFullTestSet(t), LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	fullJSON, _ := json.Marshal(full)
	layoutGraphRequireFixture(t, "testdata/full-layout-graph-receipt-v1.canonical.json", fullJSON,
		"e9245660c4775a5d309f37e0697996414a619d47614825eaf09885a187295780")
}

func layoutGraphFullTestSet(t testing.TB) *OutlineSet {
	t.Helper()
	gsubRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 5, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 1, subtables: [][]byte{gsubTestSingle1(6, 1)}},
	})
	gposRaw := gsubTestAdvancedTable([]gsubAdvancedLookupSpec{
		{lookupType: 7, subtables: [][]byte{gsubTestContext3([]uint16{3}, []gsubSequenceLookupRecord{{lookupIndex: 1}})}},
		{lookupType: 1, subtables: [][]byte{gposTestSingle1(6, 0x0001, []uint16{2}, nil)}},
	})
	return layoutFoundationBuildTestSet(t, map[string][]byte{
		"GDEF": layoutTestClassAttachFixture(), "GSUB": gsubRaw, "GPOS": gposRaw,
	})
}

func layoutGraphRequireFixture(t testing.TB, path string, value []byte, wantDigest string) {
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
}
