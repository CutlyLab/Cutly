package evaluator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestFrozenTextSourceBindsOwnedValuesLineageAndCanonicalFixtures(t *testing.T) {
	document := textV2SnapshotDocument(t)
	textIndex := frozenTextAssetIndex(t, document)
	want := *document.Assets[textIndex].Text
	wantLineHeight := *want.LineHeight
	wantViewport := *want.Viewport
	snapshot := compileFrozenTextSnapshot(t, document)

	// Mutating the editor-owned document after compilation cannot affect the private scalar record.
	document.Assets[textIndex].Text.Text = "mutated"
	document.Assets[textIndex].Text.LineHeight.Numerator++
	document.Assets[textIndex].Text.Viewport.WidthPixels++
	document.Assets[textIndex].Text.Fill.R++
	document.Assets[3].ContentHash = strings.Repeat("f", 71)

	view, receipt, err := snapshot.BindTextSource(
		context.Background(), wantAssetID(snapshot, textIndex), textIndex, FrozenTextSourceLimits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.Receipt() != receipt {
		t.Fatalf("view=%#v receipt=%+v", view, receipt)
	}
	if view.AssetID() != wantAssetID(snapshot, textIndex) || view.AssetIndex() != uint64(textIndex) ||
		view.Profile() != want.Profile || view.Text() != want.Text || view.FontAssetID() != want.FontAssetID ||
		view.FontAssetIndex() != uint64(snapshot.assets[textIndex].TextFontAssetIndex) ||
		view.FontContentHash() != snapshot.assets[textIndex].TextFontContentHash ||
		view.FontSize() != want.FontSize || view.LineHeight() != wantLineHeight || view.Viewport() != wantViewport ||
		view.Fill() != want.Fill || view.Language() != want.Language || view.Script() != want.Script ||
		view.Direction() != want.Direction || view.Alignment() != want.Alignment {
		t.Fatalf("view values drifted: receipt=%+v", receipt)
	}
	if unsafe.StringData(view.text) == unsafe.StringData(frozenTextRecordAt(snapshot, textIndex).text) ||
		unsafe.StringData(view.receipt.BindingHash) == unsafe.StringData(frozenTextRecordAt(snapshot, textIndex).bindingHash) {
		t.Fatal("published view retained private string backing")
	}

	asset := snapshot.assets[textIndex]
	record := *frozenTextRecordAt(snapshot, textIndex)
	if receipt.SchemaVersion != FrozenTextSourceSchemaVersion || receipt.Profile != FrozenTextSourceReceiptProfileV1 ||
		receipt.SourceProfile != FrozenTextSourceProfileV1 || receipt.DescriptorHash != FrozenTextSourceDescriptorHash() ||
		receipt.DocumentHash != snapshot.DocumentHash() || receipt.DocumentCanonicalBytes != snapshot.CanonicalBytes() ||
		receipt.AssetID != asset.ID || receipt.AssetIndex != uint64(textIndex) || receipt.AssetKind != documentv2.AssetKindText ||
		receipt.FontAssetID != asset.TextFontAssetID || receipt.FontAssetIndex != uint64(asset.TextFontAssetIndex) ||
		receipt.FontAssetKind != documentv2.AssetKindFont || receipt.FontContentHash != asset.TextFontContentHash ||
		receipt.CanonicalSourceHash != record.canonicalSourceHash || receipt.CanonicalSourceBytes != asset.CanonicalSourceBytes ||
		receipt.TextSemanticHash != asset.TextSemanticHash || receipt.LayoutInputHash != asset.TextLayoutInputHash ||
		receipt.FontSizeNumerator != want.FontSize.Numerator || receipt.FontSizeDenominator != want.FontSize.Denominator ||
		receipt.LineHeightNumerator != wantLineHeight.Numerator || receipt.LineHeightDenominator != wantLineHeight.Denominator ||
		receipt.ViewportWidthPixels != wantViewport.WidthPixels || receipt.ViewportHeightPixels != wantViewport.HeightPixels ||
		receipt.FillR != want.Fill.R || receipt.FillG != want.Fill.G || receipt.FillB != want.Fill.B || receipt.FillA != want.Fill.A ||
		receipt.Language != want.Language || receipt.Script != want.Script || receipt.Direction != want.Direction ||
		receipt.Alignment != want.Alignment || receipt.BindingStatus != FrozenTextSourceBindingStatusV1 ||
		receipt.CapabilityStatus != FrozenTextSourceCapabilityStatusV1 {
		t.Fatalf("receipt lineage=%+v asset=%+v", receipt, asset)
	}
	total, ok := frozenTextSum(
		receipt.CanonicalSourceBytes, receipt.TextSemanticCanonicalBytes,
		receipt.LayoutInputCanonicalBytes, receipt.BindingCanonicalBytes,
	)
	if !ok || total != receipt.CanonicalBinaryBytes || receipt.TextBytes != uint64(len(want.Text)) {
		t.Fatalf("canonical accounting total=%d/%t receipt=%+v", total, ok, receipt)
	}
	rehashed, bindingBytes, err := hashFrozenTextSourceRecord(context.Background(), record)
	if err != nil || rehashed != receipt.BindingHash || bindingBytes != receipt.BindingCanonicalBytes {
		t.Fatalf("binding hash=%q bytes=%d receipt=%+v err=%v", rehashed, bindingBytes, receipt, err)
	}

	assertFrozenTextDescriptorFixture(t)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	assertFrozenTextFixture(t, "testdata/frozen-text-source-receipt-v1.canonical.json", receiptJSON)
}

func TestFrozenTextSourceRejectsSelectionMutationAndSameSizeSubstitution(t *testing.T) {
	document := textV2SnapshotDocument(t)
	index := frozenTextAssetIndex(t, document)
	assetID := document.Assets[index].ID
	limits := DefaultFrozenTextSourceLimits()

	var nilSnapshot *FrozenSnapshot
	assertFrozenTextBindFailure(t, nilSnapshot, context.Background(), assetID, index, limits, ErrFrozenTextSourceInputInvalid)
	snapshot := compileFrozenTextSnapshot(t, document)
	assertFrozenTextBindFailure(t, snapshot, nil, assetID, index, limits, ErrFrozenTextSourceInputInvalid)
	assertFrozenTextBindFailure(t, snapshot, context.Background(), "wrong", index, limits, ErrFrozenTextSourceBinding)
	assertFrozenTextBindFailure(t, snapshot, context.Background(), assetID, -1, limits, ErrFrozenTextSourceInputInvalid)
	assertFrozenTextBindFailure(t, snapshot, context.Background(), assetID, 3, limits, ErrFrozenTextSourceBinding)
	invalidLimits := limits
	invalidLimits.MaxTextBytes = 0
	assertFrozenTextBindFailure(t, snapshot, context.Background(), assetID, index, invalidLimits, ErrFrozenTextSourceLimitsInvalid)

	returned := snapshot.Assets()
	returned[index].ID = "mutated"
	returned[index].TextSemanticHash = "mutated"
	if _, _, err := snapshot.BindTextSource(context.Background(), assetID, index, limits); err != nil {
		t.Fatalf("defensive Assets mutation affected bind: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*FrozenSnapshot)
	}{
		{"asset id", func(value *FrozenSnapshot) { value.assets[index].ID = "mutated" }},
		{"asset kind", func(value *FrozenSnapshot) { value.assets[index].Kind = documentv2.AssetKindImage }},
		{"asset semantic", func(value *FrozenSnapshot) { value.assets[index].TextSemanticHash = frozenTextTestDigest('a') }},
		{"asset layout", func(value *FrozenSnapshot) { value.assets[index].TextLayoutInputHash = frozenTextTestDigest('b') }},
		{"asset font link", func(value *FrozenSnapshot) { value.assets[index].TextFontAssetIndex = 0 }},
		{"font kind", func(value *FrozenSnapshot) { value.assets[3].Kind = documentv2.AssetKindImage }},
		{"font hash", func(value *FrozenSnapshot) { value.assets[3].ContentHash = frozenTextTestDigest('c') }},
		{"record text same size", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).text = "Butly" }},
		{"record font size", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).fontSize.Numerator++ }},
		{"record profile", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).textProfile = "cutly.text.other.v1" }},
		{"record source bytes", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).canonicalSourceBytes++ }},
		{"record semantic hash", func(value *FrozenSnapshot) {
			frozenTextRecordAt(value, index).textSemanticHash = frozenTextTestDigest('d')
		}},
		{"record layout upper", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).layoutInputUpperBytes++ }},
		{"record work", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).workUnits++ }},
		{"record binding", func(value *FrozenSnapshot) { frozenTextRecordAt(value, index).bindingHash = frozenTextTestDigest('e') }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := compileFrozenTextSnapshot(t, textV2SnapshotDocument(t))
			test.mutate(candidate)
			assertFrozenTextBindFailure(t, candidate, context.Background(), assetID, index, limits, ErrFrozenTextSourceBinding)
		})
	}

	t.Run("self-consistent canonical total N-1", func(t *testing.T) {
		candidate := compileFrozenTextSnapshot(t, textV2SnapshotDocument(t))
		record := frozenTextRecordAt(candidate, index)
		if record.canonicalBinaryBytes == 0 || record.workUnits == 0 {
			t.Fatal("expected non-zero resource charges")
		}
		record.canonicalBinaryBytes--
		record.workUnits--
		rehashed, bindingBytes, err := hashFrozenTextSourceRecord(context.Background(), *record)
		if err != nil || bindingBytes != record.bindingCanonicalBytes {
			t.Fatalf("rehash bytes=%d want=%d err=%v", bindingBytes, record.bindingCanonicalBytes, err)
		}
		record.bindingHash = rehashed
		assertFrozenTextBindFailure(
			t, candidate, context.Background(), assetID, index, limits, ErrFrozenTextSourceBinding,
		)
	})

	t.Run("hash-consistent work total N-1", func(t *testing.T) {
		candidate := compileFrozenTextSnapshot(t, textV2SnapshotDocument(t))
		record := frozenTextRecordAt(candidate, index)
		if record.workUnits == 0 {
			t.Fatal("expected non-zero work charge")
		}
		record.workUnits--
		rehashed, bindingBytes, err := hashFrozenTextSourceRecord(context.Background(), *record)
		if err != nil || bindingBytes != record.bindingCanonicalBytes {
			t.Fatalf("rehash bytes=%d want=%d err=%v", bindingBytes, record.bindingCanonicalBytes, err)
		}
		record.bindingHash = rehashed
		assertFrozenTextBindFailure(
			t, candidate, context.Background(), assetID, index, limits, ErrFrozenTextSourceBinding,
		)
	})

	otherDocument := textV2SnapshotDocument(t)
	otherDocument.Assets[index].Text.Text = "Butly"
	other := compileFrozenTextSnapshot(t, otherDocument)
	substituted := compileFrozenTextSnapshot(t, textV2SnapshotDocument(t))
	*frozenTextRecordAt(substituted, index) = *frozenTextRecordAt(other, index)
	assertFrozenTextBindFailure(t, substituted, context.Background(), assetID, index, limits, ErrFrozenTextSourceBinding)

	legacy := compileFrozenTextSnapshot(t, validDocument(t))
	legacyIndex := frozenTextAssetIndex(t, validDocument(t))
	assertFrozenTextBindFailure(
		t, legacy, context.Background(), legacy.assets[legacyIndex].ID, legacyIndex, limits, ErrFrozenTextSourceBinding,
	)
}

func TestFrozenTextSourceExactLimitsPreAdmissionCancellationAndConcurrency(t *testing.T) {
	document := textV2SnapshotDocument(t)
	index := frozenTextAssetIndex(t, document)
	assetID := document.Assets[index].ID
	snapshot := compileFrozenTextSnapshot(t, document)
	view, receipt, err := snapshot.BindTextSource(context.Background(), assetID, index, FrozenTextSourceLimits{})
	if err != nil || view == nil {
		t.Fatalf("baseline view=%#v receipt=%+v err=%v", view, receipt, err)
	}
	exact := frozenTextExactLimits(receipt)
	if exactView, exactReceipt, exactErr := snapshot.BindTextSource(context.Background(), assetID, index, exact); exactErr != nil ||
		exactView == nil || exactReceipt != receipt {
		t.Fatalf("exact view=%#v equal=%t err=%v", exactView, exactReceipt == receipt, exactErr)
	}

	tests := []struct {
		name   string
		mutate func(*FrozenTextSourceLimits)
	}{
		{"text", func(value *FrozenTextSourceLimits) { value.MaxTextBytes-- }},
		{"source canonical", func(value *FrozenTextSourceLimits) { value.MaxCanonicalSourceBytes-- }},
		{"semantic canonical", func(value *FrozenTextSourceLimits) { value.MaxTextSemanticCanonicalBytes-- }},
		{"layout canonical", func(value *FrozenTextSourceLimits) { value.MaxLayoutInputCanonicalBytes-- }},
		{"total canonical", func(value *FrozenTextSourceLimits) { value.MaxCanonicalBinaryBytes-- }},
		{"work", func(value *FrozenTextSourceLimits) { value.MaxWorkUnits-- }},
		{"working", func(value *FrozenTextSourceLimits) { value.MaxWorkingBytes-- }},
		{"retained", func(value *FrozenTextSourceLimits) { value.MaxRetainedBytes-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			below := exact
			test.mutate(&below)
			assertFrozenTextBindFailure(t, snapshot, context.Background(), assetID, index, below, ErrFrozenTextSourceResourceLimit)
		})
	}

	// A bad private hash would fail replay validation, but a lower scalar limit must reject first.
	preAdmitted := compileFrozenTextSnapshot(t, textV2SnapshotDocument(t))
	frozenTextRecordAt(preAdmitted, index).bindingHash = frozenTextTestDigest('f')
	tooSmall := exact
	tooSmall.MaxTextBytes--
	assertFrozenTextBindFailure(t, preAdmitted, context.Background(), assetID, index, tooSmall, ErrFrozenTextSourceResourceLimit)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assertFrozenTextBindFailure(t, snapshot, canceled, assetID, index, exact, ErrFrozenTextSourceCanceled)
	assertFrozenTextBindFailure(
		t, snapshot, frozenTextPanicContext{Context: context.Background()}, assetID, index, exact,
		ErrFrozenTextSourceInvariant,
	)
	assertFrozenTextBindFailure(
		t, snapshot, frozenTextNestedPanicContext{Context: context.Background()}, assetID, index, exact,
		ErrFrozenTextSourceInvariant,
	)

	const goroutines = 24
	const iterations = 20
	var wait sync.WaitGroup
	errorsChannel := make(chan error, goroutines)
	for worker := 0; worker < goroutines; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				concurrent, concurrentReceipt, bindErr := snapshot.BindTextSource(context.Background(), assetID, index, exact)
				if bindErr != nil {
					errorsChannel <- bindErr
					return
				}
				if concurrent.Text() != "Cutly" || concurrent.Viewport() != view.Viewport() || concurrentReceipt != receipt {
					errorsChannel <- errors.New("concurrent frozen text source drifted")
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for concurrentErr := range errorsChannel {
		t.Fatal(concurrentErr)
	}
}

func TestFrozenTextSourcePublicSurfaceHasNoMutableOrRawExit(t *testing.T) {
	typeOfSource := reflect.TypeOf(FrozenTextSource{})
	assertFrozenTextTypeHasNoReferences(t, typeOfSource)
	for index := 0; index < typeOfSource.NumField(); index++ {
		if typeOfSource.Field(index).PkgPath == "" {
			t.Fatalf("FrozenTextSource field %q is exported", typeOfSource.Field(index).Name)
		}
	}
	recordType := reflect.TypeOf(frozenTextSourceRecord{})
	assertFrozenTextTypeHasNoReferences(t, recordType)

	pointerType := reflect.TypeOf((*FrozenTextSource)(nil))
	for methodIndex := 0; methodIndex < pointerType.NumMethod(); methodIndex++ {
		method := pointerType.Method(methodIndex)
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			kind := method.Type.Out(outputIndex).Kind()
			if kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Map || kind == reflect.Interface ||
				kind == reflect.Func || kind == reflect.Chan || kind == reflect.UnsafePointer {
				t.Fatalf("method %s exposes %s", method.Name, kind)
			}
		}
		lower := strings.ToLower(method.Name)
		if strings.Contains(lower, "raw") || strings.Contains(lower, "bytes") || strings.Contains(lower, "reader") {
			t.Fatalf("method %s exposes raw storage vocabulary", method.Name)
		}
	}

	zero := (*FrozenTextSource)(nil)
	if zero.Receipt() != (FrozenTextSourceReceipt{}) || zero.AssetID() != "" || zero.AssetIndex() != 0 ||
		zero.Profile() != "" || zero.Text() != "" || zero.FontAssetID() != "" || zero.FontAssetIndex() != 0 ||
		zero.FontContentHash() != "" || zero.FontSize() != (documentv2.Rational{}) ||
		zero.LineHeight() != (documentv2.Rational{}) || zero.Viewport() != (documentv2.RasterViewport{}) ||
		zero.Fill() != (documentv2.ColorRGBA16{}) || zero.Language() != "" || zero.Script() != "" ||
		zero.Direction() != "" || zero.Alignment() != "" {
		t.Fatal("nil FrozenTextSource getters did not fail safe")
	}
}

func TestFrozenTextSourceUsesSparseDenseSlotsAndRejectsSlotDrift(t *testing.T) {
	legacy := compileFrozenTextSnapshot(t, validDocument(t))
	if legacy.textSourceSlots != nil || legacy.textSources != nil {
		t.Fatalf("legacy snapshot allocated text slots=%v records=%d", legacy.textSourceSlots, len(legacy.textSources))
	}

	document := textV2SnapshotDocument(t)
	firstIndex := frozenTextAssetIndex(t, document)
	secondSource := *document.Assets[firstIndex].Text
	secondSource.Text = "Second"
	secondAsset := document.Assets[firstIndex]
	secondAsset.ID = "asset-text-second"
	secondAsset.Text = &secondSource
	document.Assets = append(document.Assets, secondAsset)
	secondIndex := len(document.Assets) - 1

	snapshot := compileFrozenTextSnapshot(t, document)
	if len(snapshot.textSourceSlots) != len(snapshot.assets) || len(snapshot.textSources) != 2 || cap(snapshot.textSources) != 2 ||
		snapshot.textSourceSlots[firstIndex] != 1 || snapshot.textSourceSlots[secondIndex] != 2 {
		t.Fatalf("slots=%v records=%d assets=%d", snapshot.textSourceSlots, len(snapshot.textSources), len(snapshot.assets))
	}
	for index, slot := range snapshot.textSourceSlots {
		if index != firstIndex && index != secondIndex && slot != 0 {
			t.Fatalf("non-text asset %d has slot %d", index, slot)
		}
	}
	if frozenTextRecordAt(snapshot, firstIndex).assetIndex != uint64(firstIndex) ||
		frozenTextRecordAt(snapshot, secondIndex).assetIndex != uint64(secondIndex) {
		t.Fatalf("dense records lost asset alignment: %+v", snapshot.textSources)
	}
	view, _, err := snapshot.BindTextSource(
		context.Background(), document.Assets[secondIndex].ID, secondIndex, FrozenTextSourceLimits{},
	)
	if err != nil || view == nil || view.Text() != "Second" {
		t.Fatalf("second sparse bind view=%#v err=%v", view, err)
	}

	mutations := []struct {
		name   string
		mutate func(*FrozenSnapshot)
	}{
		{"nil table", func(value *FrozenSnapshot) { value.textSourceSlots = nil }},
		{"short table", func(value *FrozenSnapshot) {
			value.textSourceSlots = value.textSourceSlots[:len(value.textSourceSlots)-1]
		}},
		{"zero slot", func(value *FrozenSnapshot) { value.textSourceSlots[firstIndex] = 0 }},
		{"negative slot", func(value *FrozenSnapshot) { value.textSourceSlots[firstIndex] = -1 }},
		{"past dense", func(value *FrozenSnapshot) { value.textSourceSlots[firstIndex] = len(value.textSources) + 1 }},
		{"other record", func(value *FrozenSnapshot) { value.textSourceSlots[firstIndex] = value.textSourceSlots[secondIndex] }},
		{"missing dense", func(value *FrozenSnapshot) { value.textSources = nil }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := compileFrozenTextSnapshot(t, document)
			mutation.mutate(candidate)
			assertFrozenTextBindFailure(
				t, candidate, context.Background(), document.Assets[firstIndex].ID, firstIndex,
				FrozenTextSourceLimits{}, ErrFrozenTextSourceBinding,
			)
		})
	}
}

func compileFrozenTextSnapshot(t *testing.T, document documentv2.Document) *FrozenSnapshot {
	t.Helper()
	program, snapshot, diagnostics := CompileSnapshot(
		context.Background(), document, snapshotOffered(t, document),
		SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20},
	)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
	return snapshot
}

func frozenTextRecordAt(snapshot *FrozenSnapshot, assetIndex int) *frozenTextSourceRecord {
	slot := snapshot.textSourceSlots[assetIndex]
	return &snapshot.textSources[slot-1]
}

func frozenTextAssetIndex(t *testing.T, document documentv2.Document) int {
	t.Helper()
	for index, asset := range document.Assets {
		if asset.Kind == documentv2.AssetKindText {
			return index
		}
	}
	t.Fatal("text asset missing")
	return -1
}

func wantAssetID(snapshot *FrozenSnapshot, index int) documentv2.ID {
	return snapshot.assets[index].ID
}

func frozenTextExactLimits(receipt FrozenTextSourceReceipt) FrozenTextSourceLimits {
	return FrozenTextSourceLimits{
		MaxTextBytes:                  receipt.TextBytes,
		MaxCanonicalSourceBytes:       receipt.CanonicalSourceBytes,
		MaxTextSemanticCanonicalBytes: receipt.TextSemanticCanonicalBytes,
		MaxLayoutInputCanonicalBytes:  receipt.LayoutInputCanonicalBytes,
		MaxCanonicalBinaryBytes:       receipt.CanonicalBinaryBytes,
		MaxWorkUnits:                  receipt.WorkUnits,
		MaxWorkingBytes:               receipt.WorkingBytes,
		MaxRetainedBytes:              receipt.RetainedBytes,
	}
}

func assertFrozenTextBindFailure(
	t *testing.T,
	snapshot *FrozenSnapshot,
	ctx context.Context,
	assetID documentv2.ID,
	index int,
	limits FrozenTextSourceLimits,
	want error,
) {
	t.Helper()
	view, receipt, err := snapshot.BindTextSource(ctx, assetID, index, limits)
	if view != nil || receipt != (FrozenTextSourceReceipt{}) || !errors.Is(err, want) {
		t.Fatalf("view=%#v receipt=%+v err=%v want=%v", view, receipt, err, want)
	}
}

func assertFrozenTextTypeHasNoReferences(t *testing.T, value reflect.Type) {
	t.Helper()
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		switch field.Type.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
			t.Fatalf("%s field %s has reference kind %s", value, field.Name, field.Type.Kind())
		}
	}
}

func assertFrozenTextDescriptorFixture(t *testing.T) {
	t.Helper()
	canonical := FrozenTextSourceDescriptorCanonicalJSON()
	assertFrozenTextFixture(t, "testdata/frozen-text-source-profile-v1.canonical.json", canonical)
	second := FrozenTextSourceDescriptorCanonicalJSON()
	exactEncoding, err := json.Marshal(FrozenTextSourceDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, exactEncoding) ||
		!bytes.HasPrefix(second, []byte(`{"schema_version":1,"profile":`)) {
		t.Fatalf("descriptor is not exact ordered encoding/json: %s", second)
	}
	canonical[0] ^= 0xff
	if bytes.Equal(canonical, second) {
		t.Fatal("descriptor canonical JSON aliases published storage")
	}
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(FrozenTextSourceDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(FrozenTextSourceDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(second)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(second)
	want := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if FrozenTextSourceDescriptorHash() != want {
		t.Fatalf("descriptor hash=%q want=%q", FrozenTextSourceDescriptorHash(), want)
	}
}

func assertFrozenTextFixture(t *testing.T, path string, canonical []byte) {
	t.Helper()
	fixture, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\ngot=%s", path, err, canonical)
	}
	want := append(append([]byte(nil), canonical...), '\n')
	if !bytes.Equal(fixture, want) {
		t.Fatalf("fixture %s drifted\ngot=%s\nwant=%s", path, canonical, bytes.TrimSpace(fixture))
	}
}

func frozenTextTestDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

type frozenTextPanicContext struct{ context.Context }

func (frozenTextPanicContext) Err() error { panic("frozen text source panic oracle") }

type frozenTextNestedPanicContext struct{ context.Context }

func (frozenTextNestedPanicContext) Err() error { panic(frozenTextNestedPanicPayload{}) }

type frozenTextNestedPanicPayload struct{}

func (frozenTextNestedPanicPayload) String() string { panic("nested panic oracle") }
