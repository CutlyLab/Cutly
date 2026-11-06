package videoedit

import (
	"encoding/json"
	"math"
	"testing"
)

func transactionFixture(t *testing.T) Document {
	t.Helper()
	zero := mustTime(t, 0, 1)
	ten := mustTime(t, 10, 1)
	twenty := mustTime(t, 20, 1)
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            "doc-1",
		Revision:      3,
		Assets: []Asset{{
			ID: "asset-1", Kind: AssetKindVideo, URI: "footage.mp4", Duration: &twenty,
		}},
		Timelines: []Timeline{{
			ID: "timeline-1",
			Tracks: []Track{{
				ID: "video-1", Kind: TrackKindVideo,
				Clips: []Clip{{
					ID: "clip-1", AssetID: "asset-1",
					TimelineRange: TimeRange{Start: zero, Duration: ten},
					SourceRange:   TimeRange{Start: zero, Duration: ten},
				}},
			}},
		}},
	}
}

func TestApplyRejectsStaleRevisionWithoutMutation(t *testing.T) {
	document := transactionFixture(t)
	originalHash, _ := DocumentHash(document)
	tx := Transaction{
		ID: "tx-stale", IdempotencyKey: "turn-1", ExpectedRevision: 2,
		Operations: []Operation{{ID: "op-1", Kind: OperationDelete, ClipID: "clip-1"}},
	}
	result, err := Apply(document, tx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != CodeRevisionConflict {
		t.Fatalf("diagnostics = %+v, want revision conflict", result.Diagnostics)
	}
	gotHash, _ := DocumentHash(result.Document)
	if gotHash != originalHash {
		t.Fatalf("stale transaction mutated document: %s != %s", gotHash, originalHash)
	}
}

func TestApplyTransactionIsAtomicAndIncrementsOnce(t *testing.T) {
	document := transactionFixture(t)
	five := mustTime(t, 5, 1)
	badAt := mustTime(t, 30, 1)
	tx := Transaction{
		ID: "tx-atomic", IdempotencyKey: "turn-2", ExpectedRevision: document.Revision,
		Operations: []Operation{
			{ID: "move", Kind: OperationMove, ClipID: "clip-1", At: &five},
			{ID: "missing", Kind: OperationMove, ClipID: "missing", At: &badAt},
		},
	}
	result, err := Apply(document, tx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !HasErrorDiagnostics(result.Diagnostics) || result.Diagnostics[0].Code != CodeClipMissing {
		t.Fatalf("diagnostics = %+v, want missing clip", result.Diagnostics)
	}
	if result.Document.Revision != document.Revision || result.Document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Start != document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Start {
		t.Fatal("failed multi-operation transaction partially mutated document")
	}

	valid := tx
	valid.ID = "tx-valid"
	valid.IdempotencyKey = "turn-3"
	valid.Operations = valid.Operations[:1]
	result, err = Apply(document, valid, nil)
	if err != nil || HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("valid apply: result=%+v err=%v", result, err)
	}
	if result.Document.Revision != document.Revision+1 {
		t.Fatalf("revision = %d, want %d", result.Document.Revision, document.Revision+1)
	}
}

func TestApplyInverseRestoresSemanticDocument(t *testing.T) {
	document := transactionFixture(t)
	five := mustTime(t, 5, 1)
	tx := Transaction{
		ID: "tx-split", IdempotencyKey: "turn-4", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "split", Kind: OperationSplit, ClipID: "clip-1", NewClipID: "clip-2", At: &five}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("apply: %+v %v", applied.Diagnostics, err)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: %+v %v", reverted.Diagnostics, err)
	}

	want := document
	got := reverted.Document
	// Revert is an auditable new commit, so revisions differ; editing content must match.
	want.Revision = 0
	got.Revision = 0
	wantHash, _ := DocumentHash(want)
	gotHash, _ := DocumentHash(got)
	if gotHash != wantHash {
		wantJSON, _ := CanonicalJSON(want)
		gotJSON, _ := CanonicalJSON(got)
		t.Fatalf("inverse did not restore document:\nwant %s\n got %s", wantJSON, gotJSON)
	}
}

func TestApplyIdempotentReplayAndConflict(t *testing.T) {
	document := transactionFixture(t)
	zero := mustTime(t, 0, 1)
	tx := Transaction{
		ID: "tx-delete", IdempotencyKey: "turn-5", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete", Kind: OperationDelete, ClipID: "clip-1"}},
	}
	first, err := Apply(document, tx, nil)
	if err != nil || first.Commit == nil {
		t.Fatalf("first apply: %+v %v", first, err)
	}
	ledger := IdempotencyLedger{first.Commit.IdempotencyKey: *first.Commit}
	replay, err := Apply(first.Document, tx, ledger)
	if err != nil || !replay.Replayed || replay.Document.Revision != first.Document.Revision {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}

	conflict := tx
	conflict.Operations = []Operation{{ID: "insert", Kind: OperationInsert, TrackID: "video-1", Clip: &Clip{
		ID: "clip-other", AssetID: "asset-1",
		TimelineRange: TimeRange{Start: zero, Duration: mustTime(t, 1, 1)},
		SourceRange:   TimeRange{Start: zero, Duration: mustTime(t, 1, 1)},
	}}}
	result, err := Apply(first.Document, conflict, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != CodeIdempotencyConflict {
		t.Fatalf("diagnostics = %+v, want idempotency conflict", result.Diagnostics)
	}
}

func TestSetPropertyCanSetAndDelete(t *testing.T) {
	document := transactionFixture(t)
	value := json.RawMessage(`{"x":24,"y":12}`)
	tx := Transaction{
		ID: "tx-property", IdempotencyKey: "turn-6", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "set", Kind: OperationSetProperty, ClipID: "clip-1", Property: "transform", Value: value}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("set property: %+v %v", applied.Diagnostics, err)
	}
	if string(applied.Document.Timelines[0].Tracks[0].Clips[0].Properties["transform"]) != string(value) {
		t.Fatalf("property was not stored: %+v", applied.Document.Timelines[0].Tracks[0].Clips[0].Properties)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("delete inverse: %+v %v", reverted.Diagnostics, err)
	}
	if _, exists := reverted.Document.Timelines[0].Tracks[0].Clips[0].Properties["transform"]; exists {
		t.Fatal("property inverse did not delete newly added value")
	}
}

func TestApplyRejectsUnexpectedFieldsDuplicateIDsAndInvalidJSON(t *testing.T) {
	document := transactionFixture(t)
	zero := mustTime(t, 0, 1)
	tests := []struct {
		name string
		tx   Transaction
		code string
	}{
		{
			name: "unexpected field",
			tx: Transaction{ID: "tx-shape", IdempotencyKey: "shape-1", ExpectedRevision: document.Revision, Operations: []Operation{{
				ID: "move", Kind: OperationMove, ClipID: "clip-1", At: &zero, TrackID: "video-1",
			}}},
			code: CodeOperationInvalid,
		},
		{
			name: "duplicate operation ID",
			tx: Transaction{ID: "tx-duplicate", IdempotencyKey: "shape-2", ExpectedRevision: document.Revision, Operations: []Operation{
				{ID: "same", Kind: OperationMove, ClipID: "clip-1", At: &zero},
				{ID: "same", Kind: OperationDelete, ClipID: "clip-1"},
			}},
			code: CodeOperationIDConflict,
		},
		{
			name: "invalid property JSON",
			tx: Transaction{ID: "tx-json", IdempotencyKey: "shape-3", ExpectedRevision: document.Revision, Operations: []Operation{{
				ID: "property", Kind: OperationSetProperty, ClipID: "clip-1", Property: "transform", Value: json.RawMessage(`{"x":`),
			}}},
			code: CodePropertyInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, _ := DocumentHash(document)
			result, err := Apply(document, test.tx, nil)
			if err != nil {
				t.Fatalf("Apply returned internal error for agent-correctable input: %v", err)
			}
			if !hasDiagnosticCode(result.Diagnostics, test.code) {
				t.Fatalf("diagnostics = %+v, want %s", result.Diagnostics, test.code)
			}
			after, _ := DocumentHash(result.Document)
			if after != before {
				t.Fatal("invalid operation mutated the document")
			}
		})
	}
}

func TestDeleteInverseRestoresCanonicalClipOrder(t *testing.T) {
	document := transactionFixture(t)
	document.Assets[0].Duration = timePointer(mustTime(t, 20, 1))
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 2),
		clipAt(t, "clip-2", 4, 2),
		clipAt(t, "clip-3", 8, 2),
	}
	tx := Transaction{
		ID: "tx-delete-middle", IdempotencyKey: "order-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete-middle", Kind: OperationDelete, ClipID: "clip-2"}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("delete: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	want, got := document, reverted.Document
	want.Revision, got.Revision = 0, 0
	wantHash, _ := DocumentHash(want)
	gotHash, _ := DocumentHash(got)
	if gotHash != wantHash {
		t.Fatalf("delete inverse changed clip order: got %v", clipIDs(got.Timelines[0].Tracks[0].Clips))
	}
}

func TestRippleDeleteShiftsFollowingClipsAndInverseRestoresStarts(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 2),
		clipAt(t, "clip-2", 2, 2),
		clipAt(t, "clip-3", 4, 2),
	}
	tx := Transaction{
		ID: "tx-ripple-delete", IdempotencyKey: "ripple-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete", Kind: OperationDelete, ClipID: "clip-2", Ripple: true}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("delete: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	clips := applied.Document.Timelines[0].Tracks[0].Clips
	if got, want := clipIDs(clips), []ID{"clip-1", "clip-3"}; !equalIDs(got, want) {
		t.Fatalf("clip order = %v, want %v", got, want)
	}
	if got, want := clips[1].TimelineRange.Start, mustTime(t, 2, 1); got != want {
		t.Fatalf("ripple start = %+v, want %+v", got, want)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertSemanticDocumentEqual(t, document, reverted.Document)
}

func TestRippleTrimPreservesFollowingGapAndInverseRestoresStarts(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 4),
		clipAt(t, "clip-2", 6, 2),
	}
	trimmed := TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)}
	tx := Transaction{
		ID: "tx-ripple-trim", IdempotencyKey: "ripple-2", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "trim", Kind: OperationTrim, ClipID: "clip-1", SourceRange: &trimmed, Ripple: true}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("trim: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	clips := applied.Document.Timelines[0].Tracks[0].Clips
	if got, want := clips[1].TimelineRange.Start, mustTime(t, 4, 1); got != want {
		t.Fatalf("ripple start = %+v, want %+v", got, want)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertSemanticDocumentEqual(t, document, reverted.Document)
}

func TestInverseOperationIDsCannotCollideWithCallerIDs(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 4),
		clipAt(t, "clip-2", 4, 2),
	}
	trimmed := TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)}
	tx := Transaction{
		ID: "tx-inverse-ids", IdempotencyKey: "inverse-ids-1", ExpectedRevision: document.Revision,
		Operations: []Operation{
			{ID: "x", Kind: OperationTrim, ClipID: "clip-1", SourceRange: &trimmed, Ripple: true},
			{ID: "x:ripple:0", Kind: OperationSetProperty, ClipID: "clip-1", Property: "opacity", Value: json.RawMessage(`0.5`)},
		},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("apply: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	seen := make(map[string]struct{}, len(applied.Inverse.Operations))
	for _, operation := range applied.Inverse.Operations {
		if _, exists := seen[operation.ID]; exists {
			t.Fatalf("duplicate inverse operation ID %q", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertSemanticDocumentEqual(t, document, reverted.Document)
}

func TestMoveAndInsertKeepChronologicalClipOrder(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 2),
		clipAt(t, "clip-2", 4, 2),
		clipAt(t, "clip-3", 8, 2),
	}
	two := mustTime(t, 2, 1)
	tx := Transaction{
		ID: "tx-order", IdempotencyKey: "order-2", ExpectedRevision: document.Revision,
		Operations: []Operation{
			{ID: "move-third", Kind: OperationMove, ClipID: "clip-3", At: &two},
			{ID: "insert-fourth", Kind: OperationInsert, TrackID: "video-1", Clip: clipPointer(clipAt(t, "clip-4", 6, 2))},
		},
	}
	result, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("Apply: diagnostics=%+v err=%v", result.Diagnostics, err)
	}
	if got, want := clipIDs(result.Document.Timelines[0].Tracks[0].Clips), []ID{"clip-1", "clip-3", "clip-2", "clip-4"}; !equalIDs(got, want) {
		t.Fatalf("clip order = %v, want %v", got, want)
	}
}

func TestMoveThatCreatesOverlapIsRejectedAtomically(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		clipAt(t, "clip-1", 0, 4),
		clipAt(t, "clip-2", 6, 2),
	}
	three := mustTime(t, 3, 1)
	tx := Transaction{
		ID: "tx-overlap", IdempotencyKey: "overlap-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "move", Kind: OperationMove, ClipID: "clip-2", At: &three}},
	}
	result, err := Apply(document, tx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnosticCode(result.Diagnostics, CodeClipOverlap) {
		t.Fatalf("diagnostics = %+v, want %s", result.Diagnostics, CodeClipOverlap)
	}
	assertSemanticDocumentEqual(t, document, result.Document)
}

func TestRetrackInverseRestoresBothTrackOrders(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks = []Track{
		{
			ID: "video-1", Kind: TrackKindVideo,
			Clips: []Clip{clipAt(t, "clip-1", 0, 2), clipAt(t, "clip-2", 4, 2)},
		},
		{
			ID: "video-2", Kind: TrackKindVideo,
			Clips: []Clip{clipAt(t, "clip-3", 2, 2)},
		},
	}
	tx := Transaction{
		ID: "tx-retrack", IdempotencyKey: "retrack-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "retrack", Kind: OperationRetrack, ClipID: "clip-2", TargetTrackID: "video-2"}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("retrack: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	if got, want := clipIDs(applied.Document.Timelines[0].Tracks[1].Clips), []ID{"clip-3", "clip-2"}; !equalIDs(got, want) {
		t.Fatalf("target order = %v, want %v", got, want)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	assertSemanticDocumentEqual(t, document, reverted.Document)
}

func TestSplitInverseRestoresExactTimeRepresentations(t *testing.T) {
	document := transactionFixture(t)
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.TimelineRange.Duration = RationalTime{Value: 10, Rate: 1}
	clip.SourceRange.Duration = RationalTime{Value: 10000, Rate: 1000}
	five := mustTime(t, 5, 1)
	tx := Transaction{
		ID: "tx-split-representation", IdempotencyKey: "representation-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "split", Kind: OperationSplit, ClipID: "clip-1", NewClipID: "clip-2", At: &five}},
	}
	applied, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(applied.Diagnostics) {
		t.Fatalf("split: diagnostics=%+v err=%v", applied.Diagnostics, err)
	}
	reverted, err := Apply(applied.Document, applied.Inverse, nil)
	if err != nil || HasErrorDiagnostics(reverted.Diagnostics) {
		t.Fatalf("inverse: diagnostics=%+v err=%v", reverted.Diagnostics, err)
	}
	want, got := document, reverted.Document
	want.Revision, got.Revision = 0, 0
	wantHash, _ := DocumentHash(want)
	gotHash, _ := DocumentHash(got)
	if gotHash != wantHash {
		t.Fatalf("inverse did not restore exact time representation: want=%+v got=%+v", want.Timelines[0].Tracks[0].Clips[0], got.Timelines[0].Tracks[0].Clips[0])
	}
}

func TestTrimRetimedClipRequiresExplicitTimelineRange(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = mustTime(t, 5, 1)
	trimmed := TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 4, 1)}
	tx := Transaction{
		ID: "tx-retimed-trim", IdempotencyKey: "retime-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "trim", Kind: OperationTrim, ClipID: "clip-1", SourceRange: &trimmed}},
	}
	result, err := Apply(document, tx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnosticCode(result.Diagnostics, CodeClipTrimRetimeUnsupported) {
		t.Fatalf("diagnostics = %+v, want %s", result.Diagnostics, CodeClipTrimRetimeUnsupported)
	}
}

func TestApplyDoesNotMutateNestedInputState(t *testing.T) {
	document := transactionFixture(t)
	document.Timelines[0].Tracks[0].Clips[0].Properties = map[string]json.RawMessage{"opacity": json.RawMessage(`1`)}
	value := json.RawMessage(`0.5`)
	tx := Transaction{
		ID: "tx-copy", IdempotencyKey: "copy-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "set", Kind: OperationSetProperty, ClipID: "clip-1", Property: "opacity", Value: value}},
	}
	result, err := Apply(document, tx, nil)
	if err != nil || HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("Apply: diagnostics=%+v err=%v", result.Diagnostics, err)
	}
	if got := string(document.Timelines[0].Tracks[0].Clips[0].Properties["opacity"]); got != "1" {
		t.Fatalf("input nested map was mutated: %s", got)
	}
}

func TestIdempotentReplayChecksRecordedDocumentState(t *testing.T) {
	document := transactionFixture(t)
	tx := Transaction{
		ID: "tx-state", IdempotencyKey: "state-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete", Kind: OperationDelete, ClipID: "clip-1"}},
	}
	first, err := Apply(document, tx, nil)
	if err != nil || first.Commit == nil {
		t.Fatalf("first Apply: result=%+v err=%v", first, err)
	}
	ledger := IdempotencyLedger{tx.IdempotencyKey: *first.Commit}

	tooOld, err := Apply(document, tx, ledger)
	if err != nil || !hasDiagnosticCode(tooOld.Diagnostics, CodeIdempotencyStateMismatch) {
		t.Fatalf("old state replay = %+v err=%v", tooOld, err)
	}
	tampered, cloneErr := cloneDocument(first.Document)
	if cloneErr != nil {
		t.Fatal(cloneErr)
	}
	tampered.Timelines[0].Name = "tampered"
	mismatch, err := Apply(tampered, tx, ledger)
	if err != nil || !hasDiagnosticCode(mismatch.Diagnostics, CodeIdempotencyStateMismatch) {
		t.Fatalf("tampered state replay = %+v err=%v", mismatch, err)
	}
	replay, err := Apply(first.Document, tx, ledger)
	if err != nil || !replay.Replayed || HasErrorDiagnostics(replay.Diagnostics) {
		t.Fatalf("valid replay = %+v err=%v", replay, err)
	}
}

func TestApplyRejectsRevisionOverflow(t *testing.T) {
	document := transactionFixture(t)
	document.Revision = Revision(math.MaxUint64)
	tx := Transaction{
		ID: "tx-overflow", IdempotencyKey: "overflow-1", ExpectedRevision: document.Revision,
		Operations: []Operation{{ID: "delete", Kind: OperationDelete, ClipID: "clip-1"}},
	}
	result, err := Apply(document, tx, nil)
	if err != nil || !hasDiagnosticCode(result.Diagnostics, CodeRevisionOverflow) {
		t.Fatalf("overflow result = %+v err=%v", result, err)
	}
}

func TestTransactionDigestIncludesExpectedRevision(t *testing.T) {
	tx := Transaction{
		ID: "tx-digest", IdempotencyKey: "digest-1", ExpectedRevision: 1,
		Operations: []Operation{{ID: "delete", Kind: OperationDelete, ClipID: "clip-1"}},
	}
	first, err := TransactionDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.ExpectedRevision++
	second, err := TransactionDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("transactions planned against different revisions must not share a digest")
	}
}

func clipAt(t *testing.T, id ID, start, duration int64) Clip {
	t.Helper()
	return Clip{
		ID: id, AssetID: "asset-1",
		TimelineRange: TimeRange{Start: mustTime(t, start, 1), Duration: mustTime(t, duration, 1)},
		SourceRange:   TimeRange{Start: mustTime(t, start, 1), Duration: mustTime(t, duration, 1)},
	}
}

func clipPointer(value Clip) *Clip { return &value }

func timePointer(value RationalTime) *RationalTime { return &value }

func clipIDs(clips []Clip) []ID {
	ids := make([]ID, len(clips))
	for index := range clips {
		ids[index] = clips[index].ID
	}
	return ids
}

func equalIDs(left, right []ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hasDiagnosticCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func assertSemanticDocumentEqual(t *testing.T, want, got Document) {
	t.Helper()
	want.Revision, got.Revision = 0, 0
	wantHash, wantErr := DocumentHash(want)
	gotHash, gotErr := DocumentHash(got)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("DocumentHash: wantErr=%v gotErr=%v", wantErr, gotErr)
	}
	if gotHash != wantHash {
		wantJSON, _ := CanonicalJSON(want)
		gotJSON, _ := CanonicalJSON(got)
		t.Fatalf("documents differ:\nwant %s\n got %s", wantJSON, gotJSON)
	}
}
