package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestLayoutSourceBindsExactMetricsAndOutlineValueViews(t *testing.T) {
	provider := shapeTestProvider(t)
	source, receipt, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	core := provider.source.source.source.font
	if source == nil || source.Receipt() != receipt || source.UnitsPerEm() != core.unitsPerEm ||
		source.HheaAscender() != core.hheaAscender || source.GlyphCount() != core.numGlyphs ||
		receipt.ShapeProviderHash != provider.receipt.ProviderHash ||
		receipt.LayoutGraphHash != provider.graph.receipt.GraphHash ||
		receipt.OutlineSetHash != provider.source.receipt.OutlineSetHash ||
		receipt.FontContentHash != core.receipt.ContentHash ||
		receipt.CapabilityStatus != LayoutSourceCapabilityStatusV1 ||
		receipt.RasterStatus != LayoutSourceRasterStatusV1 {
		t.Fatalf("source=%#v receipt=%+v", source, receipt)
	}
	rehashed, canonical, err := hashLayoutSourceBinding(context.Background(), receipt)
	if err != nil || rehashed != receipt.LayoutSourceHash || canonical != receipt.CanonicalBinaryBytes ||
		receipt.WorkUnits != provider.receipt.WorkUnits+receipt.CanonicalBinaryBytes ||
		receipt.RetainedBytes <= provider.receipt.RetainedBytes {
		t.Fatalf("hash=%q bytes=%d receipt=%+v err=%v", rehashed, canonical, receipt, err)
	}
	mutatedSchema := receipt
	mutatedSchema.SchemaVersion++
	mutatedHash, mutatedCanonical, err := hashLayoutSourceBinding(context.Background(), mutatedSchema)
	if err != nil || mutatedHash == receipt.LayoutSourceHash || mutatedCanonical != receipt.CanonicalBinaryBytes {
		t.Fatalf("schema mutation hash=%q bytes=%d err=%v", mutatedHash, mutatedCanonical, err)
	}
	for glyphID := uint32(0); glyphID < source.GlyphCount(); glyphID++ {
		want, wantOK := provider.source.Glyph(glyphID)
		got, gotOK := source.GlyphOutline(glyphID)
		if !wantOK || !gotOK || got.GlyphID() != glyphID || got.ContourCount() != want.ContourCount() ||
			got.CommandCount() != want.CommandCount() {
			t.Fatalf("glyph %d got=%+v/%t want=%+v/%t", glyphID, got, gotOK, want, wantOK)
		}
		for commandIndex := uint64(0); commandIndex < got.CommandCount(); commandIndex++ {
			wantCommand, wantOK := want.Command(commandIndex)
			gotCommand, gotOK := got.Command(commandIndex)
			if !wantOK || !gotOK || gotCommand.Op() != wantCommand.Op() ||
				gotCommand.PointCount() != wantCommand.PointCount() {
				t.Fatalf("glyph %d command %d mismatch", glyphID, commandIndex)
			}
			for pointIndex := uint8(0); pointIndex < gotCommand.PointCount(); pointIndex++ {
				wantPoint, _ := wantCommand.Point(pointIndex)
				gotPoint, ok := gotCommand.Point(pointIndex)
				if !ok || gotPoint != wantPoint {
					t.Fatalf("glyph %d command %d point %d=%+v/%t want=%+v", glyphID, commandIndex, pointIndex, gotPoint, ok, wantPoint)
				}
				gotPoint.X++
				again, _ := gotCommand.Point(pointIndex)
				if again != wantPoint {
					t.Fatalf("point view leaked mutable storage: got=%+v want=%+v", again, wantPoint)
				}
			}
			if _, ok := gotCommand.Point(gotCommand.PointCount()); ok {
				t.Fatalf("command %d accepted out-of-range point", commandIndex)
			}
		}
		if _, ok := got.Command(got.CommandCount()); ok {
			t.Fatalf("glyph %d accepted out-of-range command", glyphID)
		}
	}
	if _, ok := source.GlyphOutline(source.GlyphCount()); ok {
		t.Fatal("accepted out-of-range glyph")
	}
	invalidGlyph := LayoutGlyphOutlineView{}
	invalidCommand := LayoutOutlineCommandView{}
	if invalidGlyph.GlyphID() != 0 || invalidGlyph.ContourCount() != 0 || invalidGlyph.CommandCount() != 0 ||
		invalidCommand.Op() != 0 || invalidCommand.PointCount() != 0 {
		t.Fatal("invalid views did not fail safe")
	}
	if _, ok := invalidGlyph.Command(0); ok {
		t.Fatal("invalid glyph returned command")
	}
	if _, ok := invalidCommand.Point(0); ok {
		t.Fatal("invalid command returned point")
	}
	if len(provider.source.commands) == 0 {
		t.Fatal("fixture has no command for fixed-array defense")
	}
	corrupt := LayoutOutlineCommandView{source: source, index: 0}
	originalPointCount := provider.source.commands[0].pointCount
	provider.source.commands[0].pointCount = uint8(len(provider.source.commands[0].points) + 1)
	if corrupt.PointCount() != 0 {
		t.Fatal("corrupt point count crossed fixed-array boundary")
	}
	if _, ok := corrupt.Point(0); ok {
		t.Fatal("corrupt command exposed an in-array point")
	}
	if _, ok := corrupt.Point(uint8(len(provider.source.commands[0].points))); ok {
		t.Fatal("corrupt command returned point outside fixed array")
	}
	provider.source.commands[0].pointCount = originalPointCount
	if (LayoutOutlineCommandView{source: source, index: ^uint64(0)}).PointCount() != 0 {
		t.Fatal("overflow command index did not fail safe")
	}
	assertLayoutSourcePublicAPIHasNoMutableOrRawExit(t)
}

func TestLayoutSourcePreservesEmptyAndCompoundGlyphs(t *testing.T) {
	provider := layoutSourceCompoundProvider(t)
	source, receipt, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	empty, ok := source.GlyphOutline(0)
	if !ok || empty.ContourCount() != 0 || empty.CommandCount() != 0 {
		t.Fatalf("empty=%+v ok=%t", empty, ok)
	}
	compound, ok := source.GlyphOutline(2)
	if !ok || compound.ContourCount() != 2 || compound.CommandCount() != 6 ||
		receipt.GlyphsWithContours != 2 || receipt.ContourCount != 3 || receipt.CommandCount != 9 {
		t.Fatalf("compound=%+v ok=%t receipt=%+v", compound, ok, receipt)
	}
	for commandIndex := uint64(0); commandIndex < compound.CommandCount(); commandIndex++ {
		command, valid := compound.Command(commandIndex)
		if !valid || command.Op() == 0 || command.PointCount() > 2 {
			t.Fatalf("compound command %d=%+v/%t", commandIndex, command, valid)
		}
	}
}

func TestLayoutSourceExactLimitsAndReplayPreAdmission(t *testing.T) {
	provider := layoutSourceCompoundProvider(t)
	_, receipt, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := layoutSourceExactLimits(receipt)
	if got, exactReceipt, exactErr := provider.BindLayoutSource(context.Background(), exact); exactErr != nil ||
		got == nil || exactReceipt != receipt {
		t.Fatalf("exact source=%#v equal=%t err=%v", got, exactReceipt == receipt, exactErr)
	}
	for _, test := range []struct {
		name string
		cut  func(*LayoutSourceLimits)
	}{
		{"glyphs", func(value *LayoutSourceLimits) { value.MaxGlyphs-- }},
		{"contours", func(value *LayoutSourceLimits) { value.MaxContours-- }},
		{"commands", func(value *LayoutSourceLimits) { value.MaxCommands-- }},
		{"command_points", func(value *LayoutSourceLimits) { value.MaxCommandPointOccurrences-- }},
		{"commands_per_glyph", func(value *LayoutSourceLimits) { value.MaxCommandsPerGlyph-- }},
		{"canonical", func(value *LayoutSourceLimits) { value.MaxCanonicalBinaryBytes-- }},
		{"work", func(value *LayoutSourceLimits) { value.MaxWorkUnits-- }},
		{"working", func(value *LayoutSourceLimits) { value.MaxWorkingBytes-- }},
		{"retained", func(value *LayoutSourceLimits) { value.MaxRetainedBytes-- }},
	} {
		t.Run("N_minus_1_"+test.name, func(t *testing.T) {
			below := exact
			test.cut(&below)
			if got, zero, bindErr := provider.BindLayoutSource(context.Background(), below); got != nil ||
				zero != (LayoutSourceReceipt{}) || !errors.Is(bindErr, ErrResourceLimit) {
				t.Fatalf("source=%#v receipt=%+v err=%v", got, zero, bindErr)
			}
		})
	}

	core := provider.source.source.source.font
	core.encoded[len(core.encoded)-1] ^= 1
	below := exact
	below.MaxWorkUnits--
	if got, zero, bindErr := provider.BindLayoutSource(context.Background(), below); got != nil ||
		zero != (LayoutSourceReceipt{}) || !errors.Is(bindErr, ErrResourceLimit) {
		t.Fatalf("low limit did not reject before replay: source=%#v receipt=%+v err=%v", got, zero, bindErr)
	}
	if got, zero, bindErr := provider.BindLayoutSource(context.Background(), exact); got != nil ||
		zero != (LayoutSourceReceipt{}) || !errors.Is(bindErr, ErrLayoutSourceInvalid) {
		t.Fatalf("mutated bytes passed replay: source=%#v receipt=%+v err=%v", got, zero, bindErr)
	}

	over := DefaultLayoutSourceLimits()
	over.MaxWorkUnits = HardMaxLayoutSourceWorkUnits + 1
	if got, zero, bindErr := provider.BindLayoutSource(context.Background(), over); got != nil ||
		zero != (LayoutSourceReceipt{}) || !errors.Is(bindErr, ErrLimitsInvalid) {
		t.Fatalf("over-hard source=%#v receipt=%+v err=%v", got, zero, bindErr)
	}
}

func TestLayoutSourceRejectsProviderLineageAndModelMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *ShapeProvider)
	}{
		{"provider_receipt", func(_ *testing.T, value *ShapeProvider) {
			value.receipt.ProviderHash = "sha256:" + strings.Repeat("0", 64)
		}},
		{"provider_hash_profile", func(_ *testing.T, value *ShapeProvider) {
			// ShapeProvider's legacy preimage uses a constant here, so the
			// provider hash itself does not change. LayoutSource must still reject it.
			value.receipt.ProviderHashProfile = "cutly.hash.sha256.tampered.v1"
		}},
		{"provider_schema", func(_ *testing.T, value *ShapeProvider) { value.receipt.SchemaVersion++ }},
		{"provider_lookup_expansion", func(_ *testing.T, value *ShapeProvider) { value.gsubLookupExpansion++ }},
		{"provider_top_level_applications", func(_ *testing.T, value *ShapeProvider) {
			value.gsubTopLevelApplications++
		}},
		{"provider_base", func(t *testing.T, value *ShapeProvider) {
			value.base = layoutSourceCompoundProvider(t).base
		}},
		{"graph_receipt", func(_ *testing.T, value *ShapeProvider) {
			value.graph.receipt.GraphHash = "sha256:" + strings.Repeat("1", 64)
		}},
		{"outline_receipt", func(_ *testing.T, value *ShapeProvider) {
			value.source.receipt.OutlineSetHash = "sha256:" + strings.Repeat("2", 64)
		}},
		{"font_metric", func(_ *testing.T, value *ShapeProvider) { value.source.source.source.font.hheaAscender++ }},
		{"font_bytes", func(_ *testing.T, value *ShapeProvider) { value.source.source.source.font.encoded[0] ^= 1 }},
		{"outline_command", func(t *testing.T, value *ShapeProvider) {
			if len(value.source.commands) == 0 {
				t.Fatal("fixture has no command")
			}
			value.source.commands[0].op = OutlineClose
		}},
		{"same_size_other_source", func(t *testing.T, value *ShapeProvider) { value.source = shapeTestProvider(t).source }},
		{"same_size_other_graph", func(t *testing.T, value *ShapeProvider) { value.graph = shapeTestProvider(t).graph }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := shapeTestProvider(t)
			test.mutate(t, provider)
			got, zero, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
			if got != nil || zero != (LayoutSourceReceipt{}) || !errors.Is(err, ErrLayoutSourceInvalid) {
				t.Fatalf("source=%#v receipt=%+v err=%v", got, zero, err)
			}
		})
	}
}

func TestLayoutSourceFailureZeroCancellationAndConcurrentReads(t *testing.T) {
	provider := shapeTestProvider(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var nilProvider *ShapeProvider
	for _, test := range []struct {
		name     string
		provider *ShapeProvider
		ctx      context.Context
		want     error
		code     ErrorCode
	}{
		{"nil_context", provider, nil, ErrContextInvalid, CodeContextInvalid},
		{"canceled", provider, canceled, ErrCanceled, CodeCanceled},
		{"nil_provider", nilProvider, context.Background(), ErrLayoutSourceInvalid, CodeLayoutSourceInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, zero, err := test.provider.BindLayoutSource(test.ctx, LayoutSourceLimits{})
			if got != nil || zero != (LayoutSourceReceipt{}) || !errors.Is(err, test.want) {
				t.Fatalf("source=%#v receipt=%+v err=%v", got, zero, err)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("code=%v err=%v", typed, err)
			}
		})
	}

	counting := &geometryCountingContext{cancelAt: 16}
	if got, zero, err := provider.BindLayoutSource(counting, LayoutSourceLimits{}); got != nil ||
		zero != (LayoutSourceReceipt{}) || !errors.Is(err, ErrCanceled) || counting.calls < counting.cancelAt {
		t.Fatalf("mid-bind source=%#v receipt=%+v err=%v calls=%d", got, zero, err, counting.calls)
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != CodeCanceled {
			t.Fatalf("mid-bind code=%v err=%v", typed, err)
		}
	}

	baseline, receipt, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wait sync.WaitGroup
	failures := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			bound, got, bindErr := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
			if bindErr != nil || got != receipt || bound == nil || baseline.Receipt() != receipt {
				failures <- fmt.Errorf("bind=%v receipt_equal=%t source_nil=%t", bindErr, got == receipt, bound == nil)
				return
			}
			for glyphID := uint32(0); glyphID < bound.GlyphCount(); glyphID++ {
				glyph, ok := bound.GlyphOutline(glyphID)
				if !ok || glyph.GlyphID() != glyphID {
					failures <- fmt.Errorf("glyph %d drift", glyphID)
					return
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

func TestLayoutSourceDescriptorAndReceiptCanonicalFixtures(t *testing.T) {
	descriptor := LayoutSourceDescriptorCanonicalJSON()
	var decoded LayoutSourceProfileDescriptor
	if err := json.Unmarshal(descriptor, &decoded); err != nil || decoded != LayoutSourceDescriptor() ||
		decoded.Profile != LayoutSourceProfileV1 || LayoutSourceDescriptorHash() == "" {
		t.Fatalf("descriptor=%+v err=%v", decoded, err)
	}
	descriptorFixture, err := os.ReadFile("testdata/layout-source-profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(descriptorFixture, append(append([]byte(nil), descriptor...), '\n')) {
		t.Fatalf("descriptor fixture drift\ngot=%s", descriptor)
	}
	descriptorDigest := sha256.Sum256(descriptor)
	if got := fmt.Sprintf("%x", descriptorDigest); got != "41cf49c06d19595fce47e64147874a5529a210db528c8d536fa89b2a59f4a766" {
		t.Fatalf("descriptor digest=%s", got)
	}

	provider := shapeTestProvider(t)
	_, receipt, err := provider.BindLayoutSource(context.Background(), LayoutSourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFixture, err := os.ReadFile("testdata/layout-source-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptFixture, append(append([]byte(nil), receiptJSON...), '\n')) {
		t.Fatalf("receipt fixture drift\ngot=%s", receiptJSON)
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	if got := fmt.Sprintf("%x", receiptDigest); got != "0e443d4ddf4552de095622ec6f6bbd05d4e4607f490c75b5ea3e1ab561a3ad34" {
		t.Fatalf("receipt digest=%s", got)
	}
}

func layoutSourceCompoundProvider(t testing.TB) *ShapeProvider {
	t.Helper()
	child := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 0, onCurve: true}, {x: 3, onCurve: false},
	}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1},
		{flags: componentScale, glyphID: 1, arg1: 1, arg2: 1, matrix: []int16{0x2000}},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{xMax: 3})
	geometry := outlineSetTestGeometry(t, [][]byte{nil, child, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
	}, geometryTestBBox{xMax: 3}, 0, 497, 3)
	set, _, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	graph, _, err := BuildLayoutGraph(context.Background(), set, LayoutGraphLimits{})
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := BindShapeProvider(context.Background(), graph, ShapeProviderLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func layoutSourceExactLimits(receipt LayoutSourceReceipt) LayoutSourceLimits {
	return LayoutSourceLimits{
		MaxGlyphs: uint64(receipt.GlyphCount), MaxContours: receipt.ContourCount,
		MaxCommands: receipt.CommandCount, MaxCommandPointOccurrences: receipt.CommandPointOccurrences,
		MaxCommandsPerGlyph:     receipt.MaxCommandsPerGlyph,
		MaxCanonicalBinaryBytes: receipt.CanonicalBinaryBytes, MaxWorkUnits: receipt.WorkUnits,
		MaxWorkingBytes: receipt.WorkingBytes, MaxRetainedBytes: receipt.RetainedBytes,
	}
}

func assertLayoutSourcePublicAPIHasNoMutableOrRawExit(t *testing.T) {
	t.Helper()
	for _, typ := range []reflect.Type{
		reflect.TypeOf((*LayoutSource)(nil)), reflect.TypeOf(LayoutGlyphOutlineView{}),
		reflect.TypeOf(LayoutOutlineCommandView{}),
	} {
		for index := 0; index < typ.NumMethod(); index++ {
			method := typ.Method(index)
			for output := 0; output < method.Type.NumOut(); output++ {
				result := method.Type.Out(output)
				if result.Kind() == reflect.Slice || result.Kind() == reflect.Map || result.Kind() == reflect.Func ||
					result.Kind() == reflect.Chan || result == reflect.TypeOf((*Font)(nil)) ||
					result == reflect.TypeOf((*OutlineSet)(nil)) || strings.Contains(result.PkgPath(), "go-text") {
					t.Fatalf("method %s exposes forbidden result %v", method.Name, result)
				}
			}
		}
	}
}
