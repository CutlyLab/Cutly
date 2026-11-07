package sfntglyf

import (
	"context"
	"errors"
	"testing"
)

func TestOutlineSetSourceRebindReplaysCompleteLineage(t *testing.T) {
	set := outlineSetRebindFixture(t, 1)
	evidence, err := rebindOutlineSetSource(context.Background(), set, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.font != set.source.source.font || evidence.outline != set.source.source ||
		evidence.contentHash != set.source.source.font.receipt.ContentHash ||
		evidence.coreWorkUnits != set.source.source.font.receipt.WorkUnits || evidence.coreWorkingBytes == 0 ||
		!evidence.gdef.present || !evidence.gsub.present || !evidence.gpos.present ||
		evidence.gdef.record.length != 4 || evidence.gsub.record.length != 4 || evidence.gpos.record.length != 4 {
		t.Fatalf("evidence=%+v", evidence)
	}
	for glyphID, glyph := range set.source.source.glyphs {
		points, ok := evidence.expandedRealPointCount(uint32(glyphID))
		if !ok || points != glyph.expandedPoints {
			t.Fatalf("glyph=%d points=%d ok=%t want=%d", glyphID, points, ok, glyph.expandedPoints)
		}
	}
}

func TestOutlineSetSourceRebindRejectsPrivateMutations(t *testing.T) {
	base := outlineSetRebindFixture(t, 1)
	tests := []struct {
		name   string
		mutate func(*OutlineSet)
	}{
		{name: "core encoded", mutate: func(value *OutlineSet) {
			font := value.source.source.font
			font.encoded[font.tables[0].offset] ^= 1
		}},
		{name: "core private record", mutate: func(value *OutlineSet) {
			value.source.source.font.tables[0].checksum++
		}},
		{name: "core table receipt", mutate: func(value *OutlineSet) {
			value.source.source.font.receipt.Tables[0].ContentHash = value.source.source.font.receipt.ContentHash
		}},
		{name: "core cmap mapping", mutate: func(value *OutlineSet) {
			value.source.source.font.mappings[0].codepoint++
		}},
		{name: "outline receipt resource", mutate: func(value *OutlineSet) {
			value.source.source.receipt.WorkUnits++
		}},
		{name: "outline graph", mutate: func(value *OutlineSet) {
			value.source.source.glyphs[1].points[0].x++
		}},
		{name: "geometry receipt", mutate: func(value *OutlineSet) {
			value.source.receipt.GeometryHash = value.source.receipt.SourceGraphHash
		}},
		{name: "geometry glyph", mutate: func(value *OutlineSet) {
			value.source.glyphs[1].xMax++
		}},
		{name: "set receipt resource", mutate: func(value *OutlineSet) {
			value.receipt.WorkUnits++
		}},
		{name: "set range", mutate: func(value *OutlineSet) {
			value.glyphs[1].commandStart++
		}},
		{name: "set command", mutate: func(value *OutlineSet) {
			value.commands[0].points[0].X++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneOutlineSetForRebind(base)
			test.mutate(value)
			evidence, err := rebindOutlineSetSource(context.Background(), value, OutlineSetLimits{})
			if err == nil || evidence != (outlineSetRebindEvidence{}) {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestOutlineSetSourceRebindRejectsCommandWithReboundHash(t *testing.T) {
	set := cloneOutlineSetForRebind(outlineSetRebindFixture(t, 1))
	set.commands[0].points[0].X++
	set.receipt.OutlineSetHash = rehashMutatedOutlineSetForTest(t, set)
	evidence, err := rebindOutlineSetSource(context.Background(), set, OutlineSetLimits{})
	if !errors.Is(err, ErrOutlineSetInputInvalid) || evidence != (outlineSetRebindEvidence{}) {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestOutlineSetSourceRebindRejectsReplacedValidCore(t *testing.T) {
	base := outlineSetRebindFixture(t, 1)
	alternate := outlineSetRebindFixture(t, 2)
	mutated := cloneOutlineSetForRebind(base)
	mutated.source.source.font = alternate.source.source.font
	evidence, err := rebindOutlineSetSource(context.Background(), mutated, OutlineSetLimits{})
	if !errors.Is(err, ErrOutlineSetInputInvalid) || evidence != (outlineSetRebindEvidence{}) {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestOutlineSetSourceRebindResourceAdmission(t *testing.T) {
	set := outlineSetRebindFixture(t, 1)
	coreWork, coreWorking, err := validateOutlineSetCoreReceipt(set.source.source.font)
	if err != nil {
		t.Fatal(err)
	}
	totalWork, ok := checkedAdd(coreWork, set.receipt.WorkUnits)
	if !ok || totalWork == 0 || coreWorking == 0 {
		t.Fatalf("coreWork=%d setWork=%d coreWorking=%d", coreWork, set.receipt.WorkUnits, coreWorking)
	}
	fontReceipt := set.source.source.font.receipt
	containerWorking, ok := outlineWeightedSum(logicalFixedWorkingBytes,
		[2]uint64{uint64(fontReceipt.TableCount), 2 * logicalTableBytes})
	cmapReplayWorking, cmapOK := outlineWeightedSum(fontReceipt.WorkingBytes,
		[2]uint64{uint64(fontReceipt.TableCount), logicalTableBytes},
		[2]uint64{fontReceipt.CmapMappingCount, logicalMappingBytes})
	if !ok || !cmapOK {
		t.Fatal("working formula overflow")
	}
	wantCoreWorking := containerWorking
	if cmapReplayWorking > wantCoreWorking {
		wantCoreWorking = cmapReplayWorking
	}
	if coreWorking != wantCoreWorking {
		t.Fatalf("core working=%d want=%d (cmap replay=%d)", coreWorking, wantCoreWorking, cmapReplayWorking)
	}
	limits := DefaultOutlineSetLimits()
	limits.MaxWorkUnits = totalWork
	if _, err := rebindOutlineSetSource(context.Background(), set, limits); err != nil {
		t.Fatalf("exact work rejected: %v", err)
	}
	limits.MaxWorkUnits = totalWork - 1
	if evidence, err := rebindOutlineSetSource(context.Background(), set, limits); !errors.Is(err, ErrResourceLimit) || evidence != (outlineSetRebindEvidence{}) {
		t.Fatalf("work N-1 evidence=%+v err=%v", evidence, err)
	}
	working := coreWorking
	if set.receipt.WorkingBytes > working {
		working = set.receipt.WorkingBytes
	}
	limits = DefaultOutlineSetLimits()
	limits.MaxWorkingBytes = working
	if _, err := rebindOutlineSetSource(context.Background(), set, limits); err != nil {
		t.Fatalf("exact working rejected: %v", err)
	}
	limits.MaxWorkingBytes = working - 1
	if evidence, err := rebindOutlineSetSource(context.Background(), set, limits); !errors.Is(err, ErrResourceLimit) || evidence != (outlineSetRebindEvidence{}) {
		t.Fatalf("working N-1 evidence=%+v err=%v", evidence, err)
	}
}

func TestOutlineSetSourceRebindCancellationIsAtomic(t *testing.T) {
	set := outlineSetRebindFixture(t, 1)
	probe := &geometryCountingContext{}
	if _, err := rebindOutlineSetSource(probe, set, OutlineSetLimits{}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 20 {
		t.Fatalf("too few cancellation checkpoints: %d", probe.calls)
	}
	for cancelAt := 1; cancelAt <= probe.calls; cancelAt++ {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		evidence, err := rebindOutlineSetSource(ctx, set, OutlineSetLimits{})
		if !errors.Is(err, ErrCanceled) || evidence != (outlineSetRebindEvidence{}) {
			t.Fatalf("cancel=%d/%d evidence=%+v err=%v", cancelAt, probe.calls, evidence, err)
		}
	}
}

func outlineSetRebindFixture(t testing.TB, layoutByte byte) *OutlineSet {
	t.Helper()
	body := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 0, onCurve: false}, {x: 2, onCurve: false},
	}, nil, false, 0)
	tables := outlineTestTables(t, [][]byte{nil, body}, outlineTestMaxp{points: 2, contours: 1})
	setGeometryDeclarations(tables, geometryTestBBox{xMax: 2}, 0, 498, 2)
	tables["GDEF"] = []byte{0, 1, 0, layoutByte}
	tables["GSUB"] = []byte{0, 1, 0, layoutByte}
	tables["GPOS"] = []byte{0, 1, 0, layoutByte}
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

func cloneOutlineSetForRebind(source *OutlineSet) *OutlineSet {
	copySet := *source
	copySet.glyphs = append([]outlineSetGlyphRange(nil), source.glyphs...)
	copySet.commands = append([]outlineSetCommand(nil), source.commands...)
	copyGeometry := *source.source
	copyGeometry.glyphs = append([]geometryGlyph(nil), source.source.glyphs...)
	copyOutline := *source.source.source
	copyOutline.glyphs = append([]outlineGlyph(nil), source.source.source.glyphs...)
	for index := range copyOutline.glyphs {
		original := source.source.source.glyphs[index]
		copyOutline.glyphs[index].points = append([]outlineRawPoint(nil), original.points...)
		copyOutline.glyphs[index].endPoints = append([]uint32(nil), original.endPoints...)
		copyOutline.glyphs[index].components = append([]outlineComponent(nil), original.components...)
		copyOutline.glyphs[index].instructions = append([]byte(nil), original.instructions...)
	}
	copyFont := *source.source.source.font
	copyFont.encoded = append([]byte(nil), source.source.source.font.encoded...)
	copyFont.tables = append([]tableRecord(nil), source.source.source.font.tables...)
	copyFont.mappings = append([]cmapMapping(nil), source.source.source.font.mappings...)
	copyFont.receipt.OpaqueLayoutTables = append([]string(nil), source.source.source.font.receipt.OpaqueLayoutTables...)
	copyFont.receipt.Tables = append([]TableReceipt(nil), source.source.source.font.receipt.Tables...)
	copyOutline.font = &copyFont
	copyGeometry.source = &copyOutline
	copySet.source = &copyGeometry
	return &copySet
}

func rehashMutatedOutlineSetForTest(t testing.TB, set *OutlineSet) string {
	t.Helper()
	meter := &outlineSetWorkMeter{
		limit: HardMaxOutlineSetWorkUnits, maxExactOps: HardMaxOutlineSetExactOps,
		maxExactLimbs: HardMaxOutlineSetExactLimbUnits,
	}
	writer, err := newOutlineSetHashWriter(context.Background(), set.source, HardMaxOutlineSetCanonicalBytes, meter)
	if err != nil {
		t.Fatal(err)
	}
	for glyphID, glyph := range set.glyphs {
		if err := writer.writeGlyph(uint32(glyphID), glyph, set.commands); err != nil {
			t.Fatal(err)
		}
	}
	hash, bytes, err := writer.finish(set.source, outlineSetCommandTopologyFromReceipt(set.receipt))
	if err != nil {
		t.Fatal(err)
	}
	if bytes != set.receipt.CanonicalBinaryBytes {
		t.Fatalf("canonical bytes=%d want=%d", bytes, set.receipt.CanonicalBinaryBytes)
	}
	return hash
}
