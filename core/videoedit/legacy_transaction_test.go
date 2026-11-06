package videoedit

import "testing"

// This file exercises the actual persistence boundary used by chatcut_edit. A pure transaction
// may reduce equivalent RationalTime values (1/1 instead of 1000000/1000000), while the legacy
// adapter deliberately stores every float-second boundary on its microsecond clock. Persistence
// must therefore hash the document decoded from the bytes that will actually be committed.
func TestLegacyTransactionRippleDeletePersistsAndReverts(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "revision":4,
  "schema_version":1,
  "document_id":"doc-cut",
  "timeline":[
    {"id":"clip-a","start":0,"end":1,"text":"a"},
    {"id":"clip-b","start":2,"end":3,"text":"b"},
    {"id":"clip-c","start":4,"end":5,"text":"c"}
  ]
}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode fixture: diagnostics=%+v err=%v", diagnostics, err)
	}

	tx := Transaction{
		ID:               "tx-ripple-delete",
		IdempotencyKey:   "turn-1-delete",
		ExpectedRevision: document.Revision,
		Operations: []Operation{{
			ID: "delete-b", Kind: OperationDelete, ClipID: "clip-b", Ripple: true,
		}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) || applied.Commit == nil {
		t.Fatalf("apply: diagnostics=%+v commit=%+v err=%v", applied.Diagnostics, applied.Commit, err)
	}
	if applied.Document.Revision != 5 {
		t.Fatalf("revision=%d, want 5", applied.Document.Revision)
	}

	persisted := persistLegacyDocument(t, applied.Document, envelope)
	assertLegacySemanticDocumentEqual(t, applied.Document, persisted)
	if got, want := testClipIDs(persisted), []ID{"clip-a", "clip-c"}; !testEqualIDs(got, want) {
		t.Fatalf("persisted IDs=%v, want %v", got, want)
	}

	// Apply hashes its exact in-memory result. The adapter may canonicalize only the time-base
	// representation, so the store must replace that hash with the document re-read from bytes.
	persistedHash, hashErr := DocumentHash(persisted)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	record := *applied.Commit
	if record.DocumentHash != persistedHash {
		staleReplay, replayErr := Apply(persisted, tx, IdempotencyLedger{record.IdempotencyKey: record})
		if replayErr != nil || !containsDiagnostic(staleReplay.Diagnostics, CodeIdempotencyStateMismatch) {
			t.Fatalf("unadjusted persistence hash must be rejected: diagnostics=%+v err=%v", staleReplay.Diagnostics, replayErr)
		}
		record.DocumentHash = persistedHash
	}
	replay, err := Apply(persisted, tx, IdempotencyLedger{record.IdempotencyKey: record})
	if err != nil || HasErrorDiagnostics(replay.Diagnostics) || !replay.Replayed {
		t.Fatalf("persisted idempotent replay: diagnostics=%+v replayed=%v err=%v", replay.Diagnostics, replay.Replayed, err)
	}

	reverted, err := Apply(persisted, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("apply inverse to persisted document: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertLegacySemanticDocumentEqualIgnoringRevision(t, document, reverted.Document)
}

func TestLegacyTransactionSplitAndRippleTrimRoundTrip(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "timeline":[
    {"id":"clip-a","start":0,"end":4,"text":"a"},
    {"id":"clip-b","start":5,"end":9,"text":"b"},
    {"id":"clip-c","start":10,"end":14,"text":"c"}
  ]
}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode fixture: diagnostics=%+v err=%v", diagnostics, err)
	}
	splitAt := RationalTime{Value: 2, Rate: 1}
	trimRange := TimeRange{
		Start:    RationalTime{Value: 6, Rate: 1},
		Duration: RationalTime{Value: 2, Rate: 1},
	}
	tx := Transaction{
		ID:               "tx-split-trim",
		IdempotencyKey:   "turn-2-split-trim",
		ExpectedRevision: document.Revision,
		Operations: []Operation{
			{ID: "split-a", Kind: OperationSplit, ClipID: "clip-a", NewClipID: "clip-a-right", At: &splitAt},
			{ID: "trim-b", Kind: OperationTrim, ClipID: "clip-b", SourceRange: &trimRange, Ripple: true},
		},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("apply: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	persisted := persistLegacyDocument(t, applied.Document, envelope)
	assertLegacySemanticDocumentEqual(t, applied.Document, persisted)
	if got, want := testClipIDs(persisted), []ID{"clip-a", "clip-a-right", "clip-b", "clip-c"}; !testEqualIDs(got, want) {
		t.Fatalf("persisted IDs=%v, want %v", got, want)
	}

	reverted, err := Apply(persisted, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse after disk round trip: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertLegacySemanticDocumentEqualIgnoringRevision(t, document, reverted.Document)
}

func TestLegacyTransactionNonRippleDeleteCannotSilentlyCollapseGap(t *testing.T) {
	raw := []byte(`{"source":"footage.mp4","timeline":[
  {"id":"clip-a","start":0,"end":1},
  {"id":"clip-b","start":2,"end":3},
  {"id":"clip-c","start":4,"end":5}
]}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode fixture: diagnostics=%+v err=%v", diagnostics, err)
	}
	tx := Transaction{
		ID: "tx-gap", IdempotencyKey: "turn-3-gap", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete-b", Kind: OperationDelete, ClipID: "clip-b"}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("apply: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	encoded, diagnostics, err := EncodeLegacyEDL(applied.Document, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != nil || !containsDiagnostic(diagnostics, CodeLegacyShapeUnsupported) {
		t.Fatalf("non-ripple gap must be rejected, encoded=%q diagnostics=%+v", encoded, diagnostics)
	}
}

func persistLegacyDocument(t *testing.T, document Document, envelope LegacyEnvelope) Document {
	t.Helper()
	encoded, diagnostics, err := EncodeLegacyEDL(document, envelope)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("encode persisted EDL: diagnostics=%+v err=%v", diagnostics, err)
	}
	persisted, _, diagnostics, err := DecodeLegacyEDL(encoded)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode persisted EDL: diagnostics=%+v err=%v\n%s", diagnostics, err, encoded)
	}
	return persisted
}

func assertLegacySemanticDocumentEqual(t *testing.T, want, got Document) {
	t.Helper()
	wantHash := legacyClockHash(t, want)
	gotHash := legacyClockHash(t, got)
	if gotHash != wantHash {
		wantJSON, _ := CanonicalJSON(want)
		gotJSON, _ := CanonicalJSON(got)
		t.Fatalf("legacy semantic documents differ:\nwant %s\n got %s", wantJSON, gotJSON)
	}
}

func assertLegacySemanticDocumentEqualIgnoringRevision(t *testing.T, want, got Document) {
	t.Helper()
	want.Revision = 0
	got.Revision = 0
	assertLegacySemanticDocumentEqual(t, want, got)
}

func legacyClockHash(t *testing.T, document Document) string {
	t.Helper()
	canonical, err := cloneDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	for timelineIndex := range canonical.Timelines {
		for trackIndex := range canonical.Timelines[timelineIndex].Tracks {
			clips := canonical.Timelines[timelineIndex].Tracks[trackIndex].Clips
			for clipIndex := range clips {
				clips[clipIndex].TimelineRange = rescaleLegacyRange(t, clips[clipIndex].TimelineRange)
				clips[clipIndex].SourceRange = rescaleLegacyRange(t, clips[clipIndex].SourceRange)
			}
		}
	}
	hash, err := DocumentHash(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func rescaleLegacyRange(t *testing.T, value TimeRange) TimeRange {
	t.Helper()
	start, err := value.Start.Rescale(LegacyTimeRate)
	if err != nil {
		t.Fatalf("rescale legacy start %+v: %v", value.Start, err)
	}
	duration, err := value.Duration.Rescale(LegacyTimeRate)
	if err != nil {
		t.Fatalf("rescale legacy duration %+v: %v", value.Duration, err)
	}
	return TimeRange{Start: start, Duration: duration}
}

func testClipIDs(document Document) []ID {
	return clipIDs(document.Timelines[0].Tracks[0].Clips)
}

func testEqualIDs(left, right []ID) bool { return equalIDs(left, right) }
