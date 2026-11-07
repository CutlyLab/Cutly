package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func TestShapeProviderRealGoTextLigatureKerningMarkRTLCJK(t *testing.T) {
	provider := shapeTestProvider(t)
	tests := []struct {
		name      string
		input     ShapeInput
		wantIDs   []uint32
		wantFirst uint64
		check     func(*testing.T, []ShapedGlyphRecord)
	}{
		{
			name: "latin_ligature", input: ShapeInput{Text: "fi", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR},
			wantIDs: []uint32{9}, wantFirst: 0,
		},
		{
			name: "latin_kerning", input: ShapeInput{Text: "AV", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR},
			wantIDs: []uint32{3, 4}, wantFirst: 0,
			check: func(t *testing.T, glyphs []ShapedGlyphRecord) {
				if glyphs[0].XAdvance != 480*64 || glyphs[1].XOffset != 5*64 {
					t.Fatalf("kerning records=%+v", glyphs)
				}
			},
		},
		{
			name: "combining_mark", input: ShapeInput{Text: "A\u0301", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR},
			wantIDs: []uint32{3, 5}, wantFirst: 0,
			check: func(t *testing.T, glyphs []ShapedGlyphRecord) {
				if glyphs[1].XAdvance != 0 {
					t.Fatalf("mark retained advance: %+v", glyphs)
				}
			},
		},
		{
			name: "rtl_logical_normalization", input: ShapeInput{Text: "\u0627\u0628", Language: "ar", Script: ShapeScriptArabic, Direction: ShapeDirectionRTL},
			wantIDs: []uint32{6, 7}, wantFirst: 0,
			check: func(t *testing.T, glyphs []ShapedGlyphRecord) {
				if glyphs[0].XAdvance >= 0 || glyphs[1].XAdvance >= 0 || glyphs[0].ClusterByteOffset != 0 || glyphs[1].ClusterByteOffset != 2 {
					t.Fatalf("RTL records=%+v", glyphs)
				}
			},
		},
		{
			name: "cjk", input: ShapeInput{Text: "\u4e2d", Language: "zh-Hans", Script: ShapeScriptHan, Direction: ShapeDirectionLTR},
			wantIDs: []uint32{8}, wantFirst: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, receipt, err := provider.Shape(context.Background(), test.input, ShapeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if output == nil || output.Receipt() != receipt || receipt.OutputProfile != ShapeOutputProfileV1 ||
				receipt.ProviderHash != provider.receipt.ProviderHash || receipt.OutputHash == "" {
				t.Fatalf("output=%#v receipt=%+v", output, receipt)
			}
			view, err := output.View()
			if err != nil || view.LineCount() != 1 {
				t.Fatalf("view=%#v err=%v", view, err)
			}
			line, err := view.Line(0)
			if err != nil || line.ByteStart() != 0 || line.ByteEnd() != uint64(len(test.input.Text)) || line.GlyphCount() != len(test.wantIDs) {
				t.Fatalf("line=%#v err=%v", line, err)
			}
			glyphs := make([]ShapedGlyphRecord, line.GlyphCount())
			for index := range glyphs {
				glyphs[index], err = line.Glyph(index)
				if err != nil || glyphs[index].GlyphID != test.wantIDs[index] {
					t.Fatalf("glyph[%d]=%+v err=%v want=%d", index, glyphs[index], err, test.wantIDs[index])
				}
			}
			if len(glyphs) != 0 && glyphs[0].ClusterByteOffset != test.wantFirst {
				t.Fatalf("first cluster=%d", glyphs[0].ClusterByteOffset)
			}
			if test.check != nil {
				test.check(t, glyphs)
			}
		})
	}
}

func TestShapeProviderRealGoTextGPOSMarkGolden(t *testing.T) {
	set := shapeTestSetWithGPOS(t, gsubTestTable(
		0, 4, 0, gsubTestTag("mark"), nil, gposTestMarkBase(5, 3, 1, 0),
	))
	graph, _, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	output, _, err := provider.Shape(context.Background(), ShapeInput{
		Text: "A\u0301", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR,
	}, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := output.View()
	line, _ := view.Line(0)
	base, _ := line.Glyph(0)
	mark, _ := line.Glyph(1)
	if base != (ShapedGlyphRecord{GlyphID: 3, ClusterByteOffset: 0, XAdvance: 500 * 64}) ||
		mark != (ShapedGlyphRecord{GlyphID: 5, ClusterByteOffset: 1, XAdvance: 0, XOffset: -500 * 64}) {
		t.Fatalf("GPOS mark golden base=%+v mark=%+v", base, mark)
	}
}

func TestShapeProviderNormalizationDottedCircleMultipleAdmissionGolden(t *testing.T) {
	set := shapeExpansionTestSet(t, gsubTestTag("liga"))
	graph, _, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.gsubLookupExpansion != 2 || provider.gsubTopLevelApplications != 1 ||
		graph.receipt.Tables[1].Expansion != 0 {
		t.Fatalf("admission model lookupExpansion=%d applications=%d logicalExpansion=%d",
			provider.gsubLookupExpansion, provider.gsubTopLevelApplications, graph.receipt.Tables[1].Expansion)
	}
	input := ShapeInput{Text: "\u0344", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	output, receipt, err := provider.Shape(context.Background(), input, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := output.View()
	line, _ := view.Line(0)
	want := []uint32{4, 2, 2, 3} // dotted circle, U+0308 doubled by GSUB, U+0301
	if line.GlyphCount() != len(want) || receipt.OutputGlyphCount != uint64(len(want)) ||
		receipt.AdmissionGlyphBound != 10 {
		t.Fatalf("line glyphs=%d receipt=%+v", line.GlyphCount(), receipt)
	}
	for index, wantID := range want {
		glyph, glyphErr := line.Glyph(index)
		if glyphErr != nil || glyph.GlyphID != wantID || glyph.ClusterByteOffset != 0 {
			t.Fatalf("glyph[%d]=%+v err=%v wantID=%d", index, glyph, glyphErr, wantID)
		}
	}
	exact := ShapeLimits{
		MaxTextBytes: receipt.TextBytes, MaxLines: receipt.LineCount,
		MaxScalarsPerLine: receipt.InputScalarCount, MaxScalars: receipt.InputScalarCount,
		MaxGlyphsPerLine: receipt.AdmissionGlyphBound, MaxGlyphs: receipt.AdmissionGlyphBound,
		MaxCanonicalBytes: receipt.AdmissionCanonicalBytes, MaxWorkUnits: receipt.WorkUnits,
		MaxWorkingBytes: receipt.WorkingBytes, MaxRetainedBytes: receipt.RetainedBytes,
	}
	if got, exactReceipt, exactErr := provider.Shape(context.Background(), input, exact); exactErr != nil || got == nil || exactReceipt != receipt {
		t.Fatalf("exact hostile admission output=%#v equal=%t err=%v", got, exactReceipt == receipt, exactErr)
	}
	for _, field := range []struct {
		name string
		cut  func(*ShapeLimits)
	}{
		{"per_line", func(value *ShapeLimits) { value.MaxGlyphsPerLine-- }},
		{"total", func(value *ShapeLimits) { value.MaxGlyphs-- }},
	} {
		t.Run("N_minus_1_"+field.name, func(t *testing.T) {
			below := exact
			field.cut(&below)
			if got, zero, shapeErr := provider.Shape(context.Background(), input, below); got != nil ||
				zero != (ShapeReceipt{}) || !errors.Is(shapeErr, ErrResourceLimit) {
				t.Fatalf("output=%#v receipt=%+v err=%v", got, zero, shapeErr)
			}
		})
	}
	record, ok := findRecord(set.source.source.font.tables, "GSUB")
	if !ok {
		t.Fatal("missing GSUB")
	}
	set.source.source.font.encoded[record.offset] ^= 1
	below := exact
	below.MaxGlyphs--
	if got, zero, shapeErr := provider.Shape(context.Background(), input, below); got != nil ||
		zero != (ShapeReceipt{}) || !errors.Is(shapeErr, ErrResourceLimit) {
		t.Fatalf("N-1 did not reject before live replay: output=%#v receipt=%+v err=%v", got, zero, shapeErr)
	}
}

func TestShapeProviderRejectsUnboundedArabicStretchFeature(t *testing.T) {
	tests := []struct {
		name     string
		set      *OutlineSet
		wantPath string
	}{
		{"gsub", shapeExpansionTestSet(t, gsubTestTag("stch")), "shape_provider/admission/gsub/stch"},
		{"gpos_only_with_arabic_gsub_multiple", shapeArabicGPOSStretchTestSet(t), "shape_provider/admission/gpos/stch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph, _, err := BuildLayoutGraph(context.Background(), test.set, LayoutGraphLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "gpos_only_with_arabic_gsub_multiple" {
				if graph.terminal.parent.gsub.features[0].tag == gsubTestTag("stch") ||
					graph.terminal.gpos.features[0].tag != gsubTestTag("stch") {
					t.Fatalf("hostile feature placement GSUB=%08x GPOS=%08x",
						graph.terminal.parent.gsub.features[0].tag, graph.terminal.gpos.features[0].tag)
				}
				if _, ok := graph.terminal.parent.gsub.lookups[0].subtables[0].(*gsubMultiple); !ok {
					t.Fatalf("hostile Arabic GSUB lookup=%T", graph.terminal.parent.gsub.lookups[0].subtables[0])
				}
			}
			got, zero, bindErr := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
			var typed *Error
			if got != nil || zero != (ShapeProviderReceipt{}) || !errors.Is(bindErr, ErrShapeProviderInvalid) ||
				!errors.As(bindErr, &typed) || typed.Path != test.wantPath {
				t.Fatalf("stch provider=%#v receipt=%+v err=%v", got, zero, bindErr)
			}
		})
	}
}

func TestShapeProviderAliasedDefaultFeatureRecordsApplyAndAdmitIndependently(t *testing.T) {
	set := shapeAliasedFeatureExpansionTestSet(t)
	graph, _, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if graph.terminal.parent.receipt.FeatureLookupReferences != 1 {
		t.Fatalf("fixture must physically deduplicate shared Feature table: references=%d",
			graph.terminal.parent.receipt.FeatureLookupReferences)
	}
	provider, _, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.gsubLookupExpansion != 8 || provider.gsubTopLevelApplications != 2 {
		t.Fatalf("admission F=%d R=%d", provider.gsubLookupExpansion, provider.gsubTopLevelApplications)
	}
	output, receipt, err := provider.Shape(context.Background(), ShapeInput{
		Text: "\u0627", Language: "ar", Script: ShapeScriptArabic, Direction: ShapeDirectionRTL,
	}, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := output.View()
	line, _ := view.Line(0)
	// ccmp and liga are separate paused stages in the Arabic shaper. Although both FeatureRecords alias one
	// physical Feature table and lookup, go-text applies the 8-way MultipleSubstitution in
	// each stage: 1 -> 8 -> 64. R must therefore be 2, never the deduplicated receipt value 1.
	if line.GlyphCount() != 64 || receipt.OutputGlyphCount != 64 || receipt.AdmissionGlyphBound != 320 {
		t.Fatalf("aliased default stages glyphs=%d receipt=%+v", line.GlyphCount(), receipt)
	}
	for index := 0; index < line.GlyphCount(); index++ {
		glyph, glyphErr := line.Glyph(index)
		if glyphErr != nil || glyph.GlyphID != 2 || glyph.ClusterByteOffset != 0 {
			t.Fatalf("glyph[%d]=%+v err=%v", index, glyph, glyphErr)
		}
	}
}

func TestShapeProviderTextByteAdmissionPrecedesUTF8Scan(t *testing.T) {
	provider := shapeTestProvider(t)
	huge := strings.Repeat("A", int(HardMaxShapeTextBytes)*4)
	input := ShapeInput{Text: huge, Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	limits := DefaultShapeLimits()
	limits.MaxTextBytes = 1
	utf8Calls := 0
	_, err := scanShapeInputWithUTF8Validator(
		context.Background(), input, limits,
		provider.gsubLookupExpansion, provider.gsubTopLevelApplications,
		func(string) bool {
			utf8Calls++
			return true
		},
	)
	if !errors.Is(err, ErrResourceLimit) || utf8Calls != 0 {
		t.Fatalf("pre-admission err=%v utf8_calls=%d", err, utf8Calls)
	}
	if got, zero, shapeErr := provider.Shape(context.Background(), input, limits); got != nil ||
		zero != (ShapeReceipt{}) || !errors.Is(shapeErr, ErrResourceLimit) {
		t.Fatalf("over-limit output=%#v receipt=%+v err=%v", got, zero, shapeErr)
	}
}

func TestShapeProviderNewlineDenseTemporaryLineWorkingExactLimit(t *testing.T) {
	provider := shapeTestProvider(t)
	text := strings.Repeat("\n", int(HardMaxShapeLines-1))
	input := ShapeInput{Text: text, Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	initial := DefaultShapeLimits()
	initial.MaxTextBytes = uint64(len(text))
	initial.MaxLines = HardMaxShapeLines
	output, receipt, err := provider.Shape(context.Background(), input, initial)
	if err != nil {
		t.Fatal(err)
	}
	if output == nil || receipt.LineCount != HardMaxShapeLines || receipt.OutputGlyphCount != 0 {
		t.Fatalf("newline-dense output=%#v receipt=%+v", output, receipt)
	}
	lineBytes := receipt.LineCount * shapeLogicalLineBytes
	wantWorking := shapeLogicalFixedWorkingBytes + lineBytes
	if wantWorking < provider.receipt.WorkingBytes {
		wantWorking = provider.receipt.WorkingBytes
	}
	if receipt.WorkingBytes != wantWorking {
		t.Fatalf("working=%d want=%d line_bytes=%d", receipt.WorkingBytes, wantWorking, lineBytes)
	}

	exact := initial
	exact.MaxCanonicalBytes = receipt.AdmissionCanonicalBytes
	exact.MaxWorkUnits = receipt.WorkUnits
	exact.MaxWorkingBytes = receipt.WorkingBytes
	exact.MaxRetainedBytes = receipt.RetainedBytes
	if got, exactReceipt, exactErr := provider.Shape(context.Background(), input, exact); exactErr != nil ||
		got == nil || exactReceipt != receipt {
		t.Fatalf("exact output=%#v equal=%t err=%v", got, exactReceipt == receipt, exactErr)
	}
	below := exact
	below.MaxWorkingBytes--
	if got, zero, belowErr := provider.Shape(context.Background(), input, below); got != nil ||
		zero != (ShapeReceipt{}) || !errors.Is(belowErr, ErrResourceLimit) {
		t.Fatalf("N-1 output=%#v receipt=%+v err=%v", got, zero, belowErr)
	}
}

func TestShapeProviderAdmissionBeforeHashAndCanonicalPropertyOwnership(t *testing.T) {
	provider := shapeTestProvider(t)
	largeText := strings.Repeat("A\n", 1<<14)
	largeInput := ShapeInput{Text: largeText, Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	common := ShapeLimits{
		MaxTextBytes: uint64(len(largeText)), MaxLines: (1 << 14) + 1,
		MaxGlyphs: HardMaxShapeGlyphs,
	}
	lowWork, lowWorking := common, common
	lowWork.MaxWorkUnits = 1
	lowWorking.MaxWorkingBytes = 1
	for _, limits := range []ShapeLimits{lowWork, lowWorking} {
		if got, zero, err := provider.Shape(context.Background(), largeInput, limits); got != nil ||
			zero != (ShapeReceipt{}) || !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("low-resource output=%#v receipt=%+v err=%v", got, zero, err)
		}
	}

	// hashShapeInput must allocate a fixed amount, not one []byte proportional to Text.
	hashAllocs := func(text string) int64 {
		input := ShapeInput{Text: text, Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
		result := testing.Benchmark(func(benchmark *testing.B) {
			for index := 0; index < benchmark.N; index++ {
				if _, _, err := hashShapeInput(context.Background(), provider.receipt, input); err != nil {
					benchmark.Fatal(err)
				}
			}
		})
		return result.AllocedBytesPerOp()
	}
	smallBytes := hashAllocs("A")
	largeBytes := hashAllocs(strings.Repeat("A", 64<<10))
	if largeBytes > smallBytes+(16<<10) {
		t.Fatalf("hash allocation scales with Text: small=%d large=%d", smallBytes, largeBytes)
	}

	languageBacking := strings.Repeat("l", 1<<20) + "en"
	scriptBacking := strings.Repeat("s", 1<<20) + ShapeScriptLatin
	directionBacking := strings.Repeat("d", 1<<20) + string(ShapeDirectionLTR)
	input := ShapeInput{
		Text: "A", Language: languageBacking[len(languageBacking)-2:],
		Script:    scriptBacking[len(scriptBacking)-len(ShapeScriptLatin):],
		Direction: ShapeDirection(directionBacking[len(directionBacking)-len(ShapeDirectionLTR):]),
	}
	_, receipt, err := provider.Shape(context.Background(), input, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Language != "en" || receipt.Script != ShapeScriptLatin || receipt.Direction != ShapeDirectionLTR {
		t.Fatalf("canonical properties=%+v", receipt)
	}
	if unsafe.StringData(receipt.Language) == unsafe.StringData(input.Language) ||
		unsafe.StringData(receipt.Script) == unsafe.StringData(input.Script) ||
		unsafe.StringData(string(receipt.Direction)) == unsafe.StringData(string(input.Direction)) {
		t.Fatal("receipt retained caller property backing storage")
	}
}

func TestShapeProviderMissingGlyphLinesAndFailureZero(t *testing.T) {
	provider := shapeTestProvider(t)
	input := ShapeInput{Text: "A\n\n\u4e2d\n", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	output, receipt, err := provider.Shape(context.Background(), input, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := output.View()
	if view.LineCount() != 4 || receipt.LineCount != 4 || receipt.OutputGlyphCount != 2 {
		t.Fatalf("view lines=%d receipt=%+v", view.LineCount(), receipt)
	}
	wantRanges := [][2]uint64{{0, 1}, {2, 2}, {3, 6}, {7, 7}}
	for index, want := range wantRanges {
		line, _ := view.Line(index)
		if line.ByteStart() != want[0] || line.ByteEnd() != want[1] {
			t.Fatalf("line %d range=[%d,%d) want=%v", index, line.ByteStart(), line.ByteEnd(), want)
		}
	}
	missing := input
	missing.Text = "\u6587"
	if got, zero, err := provider.Shape(context.Background(), missing, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrShapeMissingGlyph) {
		t.Fatalf("missing output=%#v receipt=%+v err=%v", got, zero, err)
	}
	invalid := input
	invalid.Language = "en--US"
	if got, zero, err := provider.Shape(context.Background(), invalid, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrShapeInputInvalid) {
		t.Fatalf("invalid output=%#v receipt=%+v err=%v", got, zero, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, zero, err := provider.Shape(ctx, input, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled output=%#v receipt=%+v err=%v", got, zero, err)
	}
	if got, zero, err := provider.Shape(nil, input, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context output=%#v receipt=%+v err=%v", got, zero, err)
	}
	probe := &geometryCountingContext{}
	if got, _, err := provider.Shape(probe, input, ShapeLimits{}); err != nil || got == nil || probe.calls < 16 {
		t.Fatalf("cancellation probe output=%#v calls=%d err=%v", got, probe.calls, err)
	}
	late := &geometryCountingContext{cancelAt: probe.calls - 1}
	if got, zero, err := provider.Shape(late, input, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("late cancel output=%#v receipt=%+v calls=%d err=%v", got, zero, late.calls, err)
	}
}

func TestShapeProviderExactLimitsMutationAndConcurrency(t *testing.T) {
	set := shapeTestSet(t)
	graph, _, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider, providerReceipt, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	bindProbe := &geometryCountingContext{}
	if got, _, err := BindShapeProvider(bindProbe, graph, ShapeProviderLimits{}); err != nil || got == nil || bindProbe.calls < 16 {
		t.Fatalf("bind cancel probe provider=%#v calls=%d err=%v", got, bindProbe.calls, err)
	}
	bindLate := &geometryCountingContext{cancelAt: bindProbe.calls / 2}
	if got, zero, err := BindShapeProvider(bindLate, graph, ShapeProviderLimits{}); got != nil || zero != (ShapeProviderReceipt{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("partial bind cancel provider=%#v receipt=%+v calls=%d err=%v", got, zero, bindLate.calls, err)
	}
	exactProvider := ShapeProviderLimits{
		MaxWorkUnits: providerReceipt.WorkUnits, MaxWorkingBytes: providerReceipt.WorkingBytes,
		MaxRetainedBytes: providerReceipt.RetainedBytes,
	}
	if rebound, got, err := BindShapeProvider(context.Background(), graph, exactProvider); err != nil || rebound == nil || got != providerReceipt {
		t.Fatalf("exact provider=%#v equal=%t err=%v", rebound, got == providerReceipt, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*ShapeProviderLimits)
	}{
		{"work", func(value *ShapeProviderLimits) { value.MaxWorkUnits-- }},
		{"working", func(value *ShapeProviderLimits) { value.MaxWorkingBytes-- }},
		{"retained", func(value *ShapeProviderLimits) { value.MaxRetainedBytes-- }},
	} {
		t.Run("provider_"+test.name+"_N_minus_1", func(t *testing.T) {
			below := exactProvider
			test.cut(&below)
			if got, zero, err := BindShapeProvider(context.Background(), graph, below); got != nil || zero != (ShapeProviderReceipt{}) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("provider=%#v receipt=%+v err=%v", got, zero, err)
			}
		})
	}

	input := ShapeInput{Text: "AV", Language: "en", Script: ShapeScriptLatin, Direction: ShapeDirectionLTR}
	_, shapedReceipt, err := provider.Shape(context.Background(), input, ShapeLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exactShape := ShapeLimits{
		MaxTextBytes: shapedReceipt.TextBytes, MaxLines: shapedReceipt.LineCount,
		MaxScalarsPerLine: shapedReceipt.InputScalarCount, MaxScalars: shapedReceipt.InputScalarCount,
		MaxGlyphsPerLine: shapedReceipt.AdmissionGlyphBound, MaxGlyphs: shapedReceipt.AdmissionGlyphBound,
		MaxCanonicalBytes: shapedReceipt.AdmissionCanonicalBytes, MaxWorkUnits: shapedReceipt.WorkUnits,
		MaxWorkingBytes: shapedReceipt.WorkingBytes, MaxRetainedBytes: shapedReceipt.RetainedBytes,
	}
	if got, receipt, err := provider.Shape(context.Background(), input, exactShape); err != nil || got == nil || receipt != shapedReceipt {
		t.Fatalf("exact shape=%#v equal=%t err=%v", got, receipt == shapedReceipt, err)
	}
	for _, test := range []struct {
		name string
		cut  func(*ShapeLimits)
	}{
		{"glyphs", func(value *ShapeLimits) { value.MaxGlyphs-- }},
		{"canonical", func(value *ShapeLimits) { value.MaxCanonicalBytes-- }},
		{"work", func(value *ShapeLimits) { value.MaxWorkUnits-- }},
		{"working", func(value *ShapeLimits) { value.MaxWorkingBytes-- }},
		{"retained", func(value *ShapeLimits) { value.MaxRetainedBytes-- }},
	} {
		t.Run("shape_"+test.name+"_N_minus_1", func(t *testing.T) {
			below := exactShape
			test.cut(&below)
			if got, zero, err := provider.Shape(context.Background(), input, below); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("shape=%#v receipt=%+v err=%v", got, zero, err)
			}
		})
	}

	const workers = 24
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, got, err := provider.Shape(context.Background(), input, ShapeLimits{})
			if err != nil {
				errorsSeen <- err
				return
			}
			if got != shapedReceipt {
				errorsSeen <- errors.New("concurrent receipt drift")
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}

	record, ok := findRecord(set.source.source.font.tables, "GPOS")
	if !ok {
		t.Fatal("missing GPOS")
	}
	set.source.source.font.encoded[record.offset] ^= 1
	earlyBind := exactProvider
	earlyBind.MaxWorkUnits--
	if got, zero, err := BindShapeProvider(context.Background(), graph, earlyBind); got != nil || zero != (ShapeProviderReceipt{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("pre-admission bind did proportional replay: provider=%#v receipt=%+v err=%v", got, zero, err)
	}
	if got, zero, err := provider.Shape(context.Background(), input, ShapeLimits{MaxWorkUnits: 1}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("pre-admission shape did proportional replay: shape=%#v receipt=%+v err=%v", got, zero, err)
	}
	if got, zero, err := provider.Shape(context.Background(), input, ShapeLimits{}); got != nil || zero != (ShapeReceipt{}) || !errors.Is(err, ErrShapeProviderInvalid) {
		t.Fatalf("mutated shape=%#v receipt=%+v err=%v", got, zero, err)
	}
}

func TestShapeProviderDescriptorCanonicalJSON(t *testing.T) {
	encoded := ShapeProviderDescriptorCanonicalJSON()
	var decoded ShapeProviderProfileDescriptor
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != ShapeProviderDescriptor() || decoded.Profile != ShapeProviderProfileV1 ||
		decoded.OutputProfile != ShapeOutputProfileV1 || decoded.DependencyFingerprint == "" ||
		ShapeProviderDescriptorHash() == "" {
		t.Fatalf("descriptor=%+v", decoded)
	}
	fixture, err := os.ReadFile("testdata/shape-provider-profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture, append(append([]byte(nil), encoded...), '\n')) {
		t.Fatalf("canonical descriptor fixture drift\ngot=%s", encoded)
	}
	digest := sha256.Sum256(encoded)
	if got := fmt.Sprintf("%x", digest); got != "d887a4434975d95501c90fcdc9b8a8055f71d04c99bc8a77862cf9036b13b2f9" {
		t.Fatalf("canonical descriptor digest=%s", got)
	}
}

func shapeTestProvider(t testing.TB) *ShapeProvider {
	t.Helper()
	graph, _, err := BuildLayoutGraph(context.Background(), shapeTestSet(t), LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider, receipt, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if provider == nil || provider.Receipt() != receipt || receipt.CapabilityStatus != ShapeProviderCapabilityStatusV1 {
		t.Fatalf("provider=%#v receipt=%+v", provider, receipt)
	}
	return provider
}

func shapeTestSet(t testing.TB) *OutlineSet {
	return shapeTestSetWithGPOS(t, gsubTestTable(0, 2, 0, gsubTestTag("kern"), nil, gposTestPair1(3, 4)))
}

func shapeTestSetWithGPOS(t testing.TB, gpos []byte) *OutlineSet {
	t.Helper()
	bodies := make([][]byte, 10)
	bodies[5] = makeOutlineSimpleGlyph([]uint16{3}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 1, y: 1, onCurve: true},
		{x: 2, y: 1, onCurve: true}, {x: 3, y: 0, onCurve: true},
	}, nil, false, 0)
	tables := outlineTestTables(t, bodies, outlineTestMaxp{points: 4, contours: 1})
	tables["cmap"] = makeTestCmap([][]byte{makeFormat12([]testFormat12Group{
		{0x0041, 0x0041, 3}, {0x0056, 0x0056, 4}, {0x0066, 0x0066, 1}, {0x0069, 0x0069, 2},
		{0x0301, 0x0301, 5}, {0x0627, 0x0627, 6}, {0x0628, 0x0628, 7}, {0x4e2d, 0x4e2d, 8},
	})}, []testCmapRecord{{3, 10, 0}})
	setGeometryDeclarations(tables, geometryTestBBox{xMax: 3, yMax: 1}, 0, 497, 3)
	tables["GSUB"] = gsubTestTable(0, 4, 0, gsubTestTag("liga"), nil, gsubTestLigature(1, 9, []uint16{2}))
	tables["GPOS"] = append([]byte(nil), gpos...)
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

func shapeExpansionTestSet(t testing.TB, featureTag uint32) *OutlineSet {
	t.Helper()
	bodies := make([][]byte, 5)
	bodies[4] = makeOutlineSimpleGlyph([]uint16{3}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 1, y: 1, onCurve: true},
		{x: 2, y: 1, onCurve: true}, {x: 3, y: 0, onCurve: true},
	}, nil, false, 0)
	tables := outlineTestTables(t, bodies, outlineTestMaxp{points: 4, contours: 1})
	tables["cmap"] = makeTestCmap([][]byte{makeFormat12([]testFormat12Group{
		{0x0301, 0x0301, 3}, {0x0308, 0x0308, 2}, {0x0344, 0x0344, 1}, {0x25cc, 0x25cc, 4},
	})}, []testCmapRecord{{3, 10, 0}})
	setGeometryDeclarations(tables, geometryTestBBox{xMax: 3, yMax: 1}, 0, 497, 3)
	tables["GSUB"] = gsubTestTable(0, 2, 0, featureTag, nil, gsubTestMultiple(2, []uint16{2, 2}))
	delete(tables, "GPOS")
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

func shapeArabicGPOSStretchTestSet(t testing.TB) *OutlineSet {
	t.Helper()
	gsub := shapeTestLayoutScriptTag(
		gsubTestTable(0, 2, 0, gsubTestTag("rlig"), nil, gsubTestMultiple(2, []uint16{2, 2})),
		gsubTestTag("arab"),
	)
	gpos := shapeTestLayoutScriptTag(
		gsubTestTable(0, 1, 0, gsubTestTag("stch"), nil, gposTestSingle1(2, 0, nil, nil)),
		gsubTestTag("arab"),
	)
	return shapeAdmissionTestSet(t, []testFormat12Group{{0x0627, 0x0627, 2}}, 3, gsub, gpos)
}

func shapeAliasedFeatureExpansionTestSet(t testing.TB) *OutlineSet {
	t.Helper()
	return shapeAdmissionTestSet(
		t, []testFormat12Group{{0x0627, 0x0627, 2}}, 3,
		shapeTestAliasedDefaultExpansionTable(), nil,
	)
}

func shapeAdmissionTestSet(
	t testing.TB, mappings []testFormat12Group, glyphCount int, gsub, gpos []byte,
) *OutlineSet {
	t.Helper()
	bodies := make([][]byte, glyphCount)
	bodies[2] = makeOutlineSimpleGlyph([]uint16{3}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 1, y: 1, onCurve: true},
		{x: 2, y: 1, onCurve: true}, {x: 3, y: 0, onCurve: true},
	}, nil, false, 0)
	tables := outlineTestTables(t, bodies, outlineTestMaxp{points: 4, contours: 1})
	tables["cmap"] = makeTestCmap(
		[][]byte{makeFormat12(mappings)}, []testCmapRecord{{3, 10, 0}},
	)
	setGeometryDeclarations(tables, geometryTestBBox{xMax: 3, yMax: 1}, 0, 497, 3)
	tables["GSUB"] = append([]byte(nil), gsub...)
	if gpos == nil {
		delete(tables, "GPOS")
	} else {
		tables["GPOS"] = append([]byte(nil), gpos...)
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

func shapeTestLayoutScriptTag(raw []byte, tag uint32) []byte {
	result := append([]byte(nil), raw...)
	scriptList := int(uint16(result[4])<<8 | uint16(result[5]))
	record := scriptList + 2
	result[record+0] = byte(tag >> 24)
	result[record+1] = byte(tag >> 16)
	result[record+2] = byte(tag >> 8)
	result[record+3] = byte(tag)
	return result
}

func shapeTestAliasedDefaultExpansionTable() []byte {
	const headerSize = 10
	scriptList := layoutTestWords(1)
	arabTag := gsubTestTag("arab")
	scriptList = append(scriptList,
		byte(arabTag>>24), byte(arabTag>>16), byte(arabTag>>8), byte(arabTag), 0, 8,
	)
	scriptList = append(scriptList, layoutTestWords(4, 0, 0, 0xffff, 2, 0, 1)...)

	// Both records deliberately point at the same Feature table at offset 14.
	featureList := layoutTestWords(2)
	for _, tag := range []uint32{gsubTestTag("ccmp"), gsubTestTag("liga")} {
		featureList = append(featureList,
			byte(tag>>24), byte(tag>>16), byte(tag>>8), byte(tag), 0, 14,
		)
	}
	featureList = append(featureList, layoutTestWords(0, 1, 0)...)

	outputs := []uint16{2, 2, 2, 2, 2, 2, 2, 2}
	lookupList := layoutTestWords(1, 4, 2, 0, 1, 8)
	lookupList = append(lookupList, gsubTestMultiple(2, outputs)...)
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
