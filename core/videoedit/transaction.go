package videoedit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Stable transaction and operation diagnostic codes. They are API: callers branch on Code, not
// on the human-readable Message.
const (
	CodeTransactionIDRequired      = "TX.ID_REQUIRED"
	CodeTransactionEmpty           = "TX.EMPTY"
	CodeRevisionConflict           = "TX.REVISION_CONFLICT"
	CodeRevisionOverflow           = "TX.REVISION_OVERFLOW"
	CodeIdempotencyRequired        = "TX.IDEMPOTENCY_REQUIRED"
	CodeIdempotencyConflict        = "TX.IDEMPOTENCY_CONFLICT"
	CodeIdempotencyStateMismatch   = "TX.IDEMPOTENCY_STATE_MISMATCH"
	CodeOperationInvalid           = "OP.INVALID"
	CodeOperationIDConflict        = "OP.ID_CONFLICT"
	CodeOperationUnsupported       = "OP.UNSUPPORTED"
	CodeClipMissing                = "CLIP.MISSING"
	CodeClipIDConflict             = "CLIP.ID_CONFLICT"
	CodeClipSplitPointInvalid      = "CLIP.SPLIT_POINT_INVALID"
	CodeClipSplitRetimeUnsupported = "CLIP.SPLIT_RETIME_UNSUPPORTED"
	CodeClipTrimRetimeUnsupported  = "CLIP.TRIM_RETIME_UNSUPPORTED"
	CodeTrackMissing               = "TRACK.MISSING"
	CodeTrackKindMismatch          = "TRACK.KIND_MISMATCH"
	CodePropertyInvalid            = "PROPERTY.INVALID"
)

// OperationKind is deliberately flat and JSON-friendly so an agent can generate operations without
// constructing an interface-shaped union.
type OperationKind string

const (
	OperationInsert      OperationKind = "insert"
	OperationMove        OperationKind = "move"
	OperationTrim        OperationKind = "trim"
	OperationSplit       OperationKind = "split"
	OperationDelete      OperationKind = "delete"
	OperationRetrack     OperationKind = "retrack"
	OperationSetProperty OperationKind = "set_property"
)

// Operation is a tagged operation with an exact per-kind field set; unexpected fields produce an
// OP.INVALID diagnostic instead of being silently ignored. IDs are supplied by the caller; the
// core never invents random IDs, which keeps retries deterministic. Ripple is track-local and is
// currently accepted only by trim and delete.
type Operation struct {
	ID            string          `json:"id"`
	Kind          OperationKind   `json:"op"`
	ClipID        ID              `json:"clip_id,omitempty"`
	TrackID       ID              `json:"track_id,omitempty"`
	TargetTrackID ID              `json:"target_track_id,omitempty"`
	At            *RationalTime   `json:"at,omitempty"`
	SourceRange   *TimeRange      `json:"source_range,omitempty"`
	TimelineRange *TimeRange      `json:"timeline_range,omitempty"`
	Clip          *Clip           `json:"clip,omitempty"`
	NewClipID     ID              `json:"new_clip_id,omitempty"`
	Property      string          `json:"property,omitempty"`
	Value         json.RawMessage `json:"value,omitempty"`
	Ripple        bool            `json:"ripple,omitempty"`
}

// Transaction is the atomic unit agents plan, review, apply and revert.
type Transaction struct {
	ID               string      `json:"id"`
	IdempotencyKey   string      `json:"idempotency_key"`
	ExpectedRevision Revision    `json:"expected_revision"`
	Operations       []Operation `json:"operations"`
}

// CommitRecord belongs in a store/journal, not in Document. Keeping the ledger outside the editing
// state prevents an ever-growing transaction log from changing every render fingerprint.
type CommitRecord struct {
	TransactionID  string      `json:"transaction_id"`
	IdempotencyKey string      `json:"idempotency_key"`
	Digest         string      `json:"digest"`
	Revision       Revision    `json:"revision"`
	DocumentHash   string      `json:"document_hash"`
	Inverse        Transaction `json:"inverse"`
}

// IdempotencyLedger is passed into Apply by a persistence layer. Apply never mutates it.
type IdempotencyLedger map[string]CommitRecord

// ApplyResult is returned for both a new commit and a safe idempotent replay.
type ApplyResult struct {
	Document    Document      `json:"document"`
	Inverse     Transaction   `json:"inverse,omitempty"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
	Commit      *CommitRecord `json:"commit,omitempty"`
	Replayed    bool          `json:"replayed,omitempty"`
}

// Apply validates and atomically applies tx to a deep copy of document. Agent-correctable failures
// are diagnostics; error is reserved for canonicalization or an internal invariant failure.
func Apply(document Document, tx Transaction, ledger IdempotencyLedger) (ApplyResult, error) {
	result := ApplyResult{Document: document}
	result.Diagnostics = validateTransactionShape(tx)
	if HasErrorDiagnostics(result.Diagnostics) {
		return result, nil
	}

	digest, err := TransactionDigest(tx)
	if err != nil {
		return result, err
	}

	if previous, exists := ledger[tx.IdempotencyKey]; exists {
		if previous.Digest != digest {
			result.Diagnostics = append(result.Diagnostics, errorDiagnostic(
				CodeIdempotencyConflict,
				"/idempotency_key",
				fmt.Sprintf("idempotency key %q was already used for a different transaction", tx.IdempotencyKey),
			))
			return result, nil
		}
		if diagnostics := ValidateDocument(document); HasErrorDiagnostics(diagnostics) {
			result.Diagnostics = diagnostics
			return result, nil
		}
		if document.Revision < previous.Revision {
			result.Diagnostics = append(result.Diagnostics, errorDiagnostic(
				CodeIdempotencyStateMismatch,
				"/idempotency_key",
				fmt.Sprintf("idempotency record was committed at revision %d, but current revision is %d", previous.Revision, document.Revision),
			))
			return result, nil
		}
		if document.Revision == previous.Revision && previous.DocumentHash != "" {
			currentHash, hashErr := DocumentHash(document)
			if hashErr != nil {
				return result, hashErr
			}
			if currentHash != previous.DocumentHash {
				result.Diagnostics = append(result.Diagnostics, errorDiagnostic(
					CodeIdempotencyStateMismatch,
					"/idempotency_key",
					"current document does not match the document recorded for this idempotent commit",
				))
				return result, nil
			}
		}
		result.Replayed = true
		result.Inverse = previous.Inverse
		copy := previous
		result.Commit = &copy
		return result, nil
	}

	if document.Revision != tx.ExpectedRevision {
		result.Diagnostics = append(result.Diagnostics, errorDiagnostic(
			CodeRevisionConflict,
			"/expected_revision",
			fmt.Sprintf("expected revision %d, current revision is %d", tx.ExpectedRevision, document.Revision),
		))
		return result, nil
	}
	if diagnostics := ValidateDocument(document); HasErrorDiagnostics(diagnostics) {
		result.Diagnostics = diagnostics
		return result, nil
	}
	if document.Revision == ^Revision(0) {
		result.Diagnostics = append(result.Diagnostics, errorDiagnostic(CodeRevisionOverflow, "/revision", "document revision cannot be incremented"))
		return result, nil
	}

	working, err := cloneDocument(document)
	if err != nil {
		return result, err
	}
	inverseOperations := make([]Operation, 0, len(tx.Operations)*2)
	for index, operation := range tx.Operations {
		inverses, diagnostics := applyOperation(&working, operation, index)
		if HasErrorDiagnostics(diagnostics) {
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
			return result, nil // result.Document remains the original: the transaction is atomic.
		}
		// A transaction is undone in reverse operation order. Some operations (split) produce more
		// than one inverse; prepend the group while preserving the group's internal order.
		combined := make([]Operation, 0, len(inverses)+len(inverseOperations))
		combined = append(combined, inverses...)
		combined = append(combined, inverseOperations...)
		inverseOperations = combined
	}
	if diagnostics := ValidateDocument(working); HasErrorDiagnostics(diagnostics) {
		result.Diagnostics = diagnostics
		return result, nil
	}

	working.Revision++
	// Operation IDs in the inverse are generated centrally instead of being derived only from
	// caller-controlled IDs. This prevents a valid transaction such as "x" plus "x:ripple:0"
	// from producing duplicate inverse operation IDs that could not be applied.
	for index := range inverseOperations {
		inverseOperations[index].ID = fmt.Sprintf("inverse:%s:%d", tx.ID, index)
	}
	committedHash, err := DocumentHash(working)
	if err != nil {
		return result, err
	}
	inverse := Transaction{
		ID:               "inverse:" + tx.ID,
		IdempotencyKey:   fmt.Sprintf("inverse:%s:%d", tx.IdempotencyKey, working.Revision),
		ExpectedRevision: working.Revision,
		Operations:       inverseOperations,
	}
	commit := CommitRecord{
		TransactionID:  tx.ID,
		IdempotencyKey: tx.IdempotencyKey,
		Digest:         digest,
		Revision:       working.Revision,
		DocumentHash:   committedHash,
		Inverse:        inverse,
	}
	result.Document = working
	result.Inverse = inverse
	result.Commit = &commit
	return result, nil
}

// TransactionDigest identifies the exact proposal protected by an idempotency key.
func TransactionDigest(tx Transaction) (string, error) {
	return canonicalHash(tx)
}

func validateTransactionShape(tx Transaction) []Diagnostic {
	var diagnostics []Diagnostic
	if strings.TrimSpace(tx.ID) == "" {
		diagnostics = append(diagnostics, errorDiagnostic(CodeTransactionIDRequired, "/id", "transaction ID is required"))
	}
	if strings.TrimSpace(tx.IdempotencyKey) == "" {
		diagnostics = append(diagnostics, errorDiagnostic(CodeIdempotencyRequired, "/idempotency_key", "idempotency key is required"))
	}
	if len(tx.Operations) == 0 {
		diagnostics = append(diagnostics, errorDiagnostic(CodeTransactionEmpty, "/operations", "transaction must contain at least one operation"))
	}

	operationIDs := make(map[string]int, len(tx.Operations))
	for index, operation := range tx.Operations {
		path := fmt.Sprintf("/operations/%d", index)
		operationID := strings.TrimSpace(operation.ID)
		if operationID == "" {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/id", "operation ID is required"))
		} else if previous, exists := operationIDs[operationID]; exists {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationIDConflict, path+"/id", fmt.Sprintf("operation ID %q is already used at /operations/%d/id", operation.ID, previous)))
		} else {
			operationIDs[operationID] = index
		}
		diagnostics = append(diagnostics, validateOperationShape(operation, path)...)
	}
	return diagnostics
}

func validateOperationShape(operation Operation, path string) []Diagnostic {
	var diagnostics []Diagnostic
	requireID := func(value ID, field string) {
		if strings.TrimSpace(string(value)) == "" {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/"+field, field+" is required"))
		}
	}
	requirePointer := func(present bool, field string) {
		if !present {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/"+field, field+" is required"))
		}
	}
	allowed := map[string]bool{}
	switch operation.Kind {
	case OperationInsert:
		allowed["track_id"], allowed["clip"] = true, true
		requireID(operation.TrackID, "track_id")
		requirePointer(operation.Clip != nil, "clip")
		if operation.Clip != nil {
			requireID(operation.Clip.ID, "clip/id")
			if _, err := CanonicalJSON(operation.Clip); err != nil {
				diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/clip", err.Error()))
			}
		}
	case OperationMove:
		allowed["clip_id"], allowed["at"] = true, true
		requireID(operation.ClipID, "clip_id")
		requirePointer(operation.At != nil, "at")
	case OperationTrim:
		allowed["clip_id"], allowed["source_range"], allowed["timeline_range"], allowed["ripple"] = true, true, true, true
		requireID(operation.ClipID, "clip_id")
		requirePointer(operation.SourceRange != nil, "source_range")
	case OperationSplit:
		allowed["clip_id"], allowed["new_clip_id"], allowed["at"] = true, true, true
		requireID(operation.ClipID, "clip_id")
		requireID(operation.NewClipID, "new_clip_id")
		requirePointer(operation.At != nil, "at")
	case OperationDelete:
		allowed["clip_id"], allowed["ripple"] = true, true
		requireID(operation.ClipID, "clip_id")
	case OperationRetrack:
		allowed["clip_id"], allowed["target_track_id"] = true, true
		requireID(operation.ClipID, "clip_id")
		requireID(operation.TargetTrackID, "target_track_id")
	case OperationSetProperty:
		allowed["clip_id"], allowed["property"], allowed["value"] = true, true, true
		requireID(operation.ClipID, "clip_id")
		if strings.TrimSpace(operation.Property) == "" {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/property", "property is required"))
		} else if operation.Property != strings.TrimSpace(operation.Property) {
			diagnostics = append(diagnostics, errorDiagnostic(CodePropertyInvalid, path+"/property", "property must not have leading or trailing whitespace"))
		}
		if len(operation.Value) > 0 && !json.Valid(operation.Value) {
			diagnostics = append(diagnostics, errorDiagnostic(CodePropertyInvalid, path+"/value", "property value must be valid JSON or omitted to delete"))
		}
	default:
		diagnostics = append(diagnostics, errorDiagnostic(CodeOperationUnsupported, path+"/op", fmt.Sprintf("operation %q is not supported", operation.Kind)))
		return diagnostics
	}

	fields := []struct {
		name    string
		present bool
	}{
		{"clip_id", operation.ClipID != ""},
		{"track_id", operation.TrackID != ""},
		{"target_track_id", operation.TargetTrackID != ""},
		{"at", operation.At != nil},
		{"source_range", operation.SourceRange != nil},
		{"timeline_range", operation.TimelineRange != nil},
		{"clip", operation.Clip != nil},
		{"new_clip_id", operation.NewClipID != ""},
		{"property", operation.Property != ""},
		{"value", len(operation.Value) > 0},
		{"ripple", operation.Ripple},
	}
	for _, field := range fields {
		if field.present && !allowed[field.name] {
			diagnostics = append(diagnostics, errorDiagnostic(CodeOperationInvalid, path+"/"+field.name, fmt.Sprintf("%s is not valid for %s", field.name, operation.Kind)))
		}
	}
	return diagnostics
}

func applyOperation(document *Document, operation Operation, operationIndex int) ([]Operation, []Diagnostic) {
	path := fmt.Sprintf("/operations/%d", operationIndex)
	if strings.TrimSpace(operation.ID) == "" {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/id", "operation ID is required")}
	}

	switch operation.Kind {
	case OperationInsert:
		return applyInsert(document, operation, path)
	case OperationMove:
		return applyMove(document, operation, path)
	case OperationTrim:
		return applyTrim(document, operation, path)
	case OperationSplit:
		return applySplit(document, operation, path)
	case OperationDelete:
		return applyDelete(document, operation, path)
	case OperationRetrack:
		return applyRetrack(document, operation, path)
	case OperationSetProperty:
		return applySetProperty(document, operation, path)
	default:
		return nil, []Diagnostic{errorDiagnostic(CodeOperationUnsupported, path+"/op", fmt.Sprintf("operation %q is not supported", operation.Kind))}
	}
}

func applyInsert(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.Clip == nil || operation.TrackID == "" {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "insert requires track_id and clip")}
	}
	if _, _, _, found := findClip(*document, operation.Clip.ID); found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipIDConflict, path+"/clip/id", fmt.Sprintf("clip %q already exists", operation.Clip.ID))}
	}
	timelineIndex, trackIndex, track, found := findTrack(*document, operation.TrackID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeTrackMissing, path+"/track_id", fmt.Sprintf("track %q does not exist", operation.TrackID))}
	}
	clip, err := cloneClip(*operation.Clip)
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/clip", err.Error())}
	}
	document.Timelines[timelineIndex].Tracks[trackIndex].Clips = append(track.Clips, clip)
	sortTrackClips(&document.Timelines[timelineIndex].Tracks[trackIndex])
	return []Operation{{
		ID:     "undo:" + operation.ID,
		Kind:   OperationDelete,
		ClipID: clip.ID,
	}}, nil
}

func applyMove(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" || operation.At == nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "move requires clip_id and at")}
	}
	if err := operation.At.Validate(); err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}
	timelineIndex, trackIndex, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	old := document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex].TimelineRange.Start
	document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex].TimelineRange.Start = *operation.At
	sortTrackClips(&document.Timelines[timelineIndex].Tracks[trackIndex])
	return []Operation{{ID: "undo:" + operation.ID, Kind: OperationMove, ClipID: operation.ClipID, At: &old}}, nil
}

func applyTrim(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" || operation.SourceRange == nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "trim requires clip_id and source_range")}
	}
	if err := operation.SourceRange.Validate(); err != nil || operation.SourceRange.Duration.Value <= 0 {
		message := "trim source_range must have a positive duration"
		if err != nil {
			message += ": " + err.Error()
		}
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/source_range", message)}
	}
	if operation.TimelineRange != nil {
		if err := operation.TimelineRange.Validate(); err != nil || operation.TimelineRange.Duration.Value <= 0 {
			message := "trim timeline_range must have a positive duration"
			if err != nil {
				message += ": " + err.Error()
			}
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/timeline_range", message)}
		}
	}
	timelineIndex, trackIndex, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	clip := &document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex]
	if operation.TimelineRange == nil {
		comparison, err := clip.TimelineRange.Duration.Compare(clip.SourceRange.Duration)
		if err != nil || comparison != 0 {
			return nil, []Diagnostic{errorDiagnostic(CodeClipTrimRetimeUnsupported, path, "trim on a retimed clip requires an explicit timeline_range")}
		}
	}
	oldSourceRange := clip.SourceRange
	oldTimelineRange := clip.TimelineRange
	newTimelineRange := clip.TimelineRange
	if operation.TimelineRange == nil {
		newTimelineRange.Duration = operation.SourceRange.Duration
	} else {
		newTimelineRange = *operation.TimelineRange
	}
	var rippleMoves []rippleMove
	if operation.Ripple {
		oldEnd, endErr := oldTimelineRange.End()
		if endErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/timeline_range", endErr.Error())}
		}
		newEnd, endErr := newTimelineRange.End()
		if endErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/timeline_range", endErr.Error())}
		}
		delta, deltaErr := newEnd.Sub(oldEnd)
		if deltaErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/timeline_range", deltaErr.Error())}
		}
		rippleMoves, deltaErr = planRippleMoves(document.Timelines[timelineIndex].Tracks[trackIndex], clip.ID, oldEnd, delta)
		if deltaErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/ripple", deltaErr.Error())}
		}
	}
	clip.SourceRange = *operation.SourceRange
	clip.TimelineRange = newTimelineRange
	track := &document.Timelines[timelineIndex].Tracks[trackIndex]
	applyRippleMoves(track, rippleMoves)
	sortTrackClips(track)
	inverses := []Operation{{
		ID:            "undo:" + operation.ID,
		Kind:          OperationTrim,
		ClipID:        operation.ClipID,
		SourceRange:   &oldSourceRange,
		TimelineRange: &oldTimelineRange,
	}}
	for index := range rippleMoves {
		oldStart := rippleMoves[index].oldStart
		inverses = append(inverses, Operation{
			ID:     fmt.Sprintf("undo:%s:ripple:%d", operation.ID, index),
			Kind:   OperationMove,
			ClipID: rippleMoves[index].clipID,
			At:     &oldStart,
		})
	}
	return inverses, nil
}

func applySplit(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" || operation.NewClipID == "" || operation.At == nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "split requires clip_id, new_clip_id and at")}
	}
	if _, _, _, found := findClip(*document, operation.NewClipID); found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipIDConflict, path+"/new_clip_id", fmt.Sprintf("clip %q already exists", operation.NewClipID))}
	}
	timelineIndex, trackIndex, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	if err := operation.At.Validate(); err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}
	clip := &document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex]
	if comparison, err := clip.TimelineRange.Duration.Compare(clip.SourceRange.Duration); err != nil || comparison != 0 {
		return nil, []Diagnostic{errorDiagnostic(CodeClipSplitRetimeUnsupported, path, "split currently requires timeline and source durations to match")}
	}
	end, err := clip.TimelineRange.End()
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}
	startComparison, startErr := operation.At.Compare(clip.TimelineRange.Start)
	endComparison, endErr := operation.At.Compare(end)
	if startErr != nil || endErr != nil || startComparison <= 0 || endComparison >= 0 {
		return nil, []Diagnostic{errorDiagnostic(CodeClipSplitPointInvalid, path+"/at", "split point must be strictly inside the clip timeline range")}
	}

	oldSourceRange := clip.SourceRange
	oldTimelineRange := clip.TimelineRange
	offset, err := operation.At.Sub(clip.TimelineRange.Start)
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}
	rightDuration, err := clip.TimelineRange.Duration.Sub(offset)
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}
	rightSourceStart, err := clip.SourceRange.Start.Add(offset)
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/at", err.Error())}
	}

	right, err := cloneClip(*clip)
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, err.Error())}
	}
	right.ID = operation.NewClipID
	right.TimelineRange = TimeRange{Start: *operation.At, Duration: rightDuration}
	right.SourceRange = TimeRange{Start: rightSourceStart, Duration: rightDuration}
	clip.TimelineRange.Duration = offset
	clip.SourceRange.Duration = offset

	track := &document.Timelines[timelineIndex].Tracks[trackIndex]
	track.Clips = append(track.Clips, Clip{})
	copy(track.Clips[clipIndex+2:], track.Clips[clipIndex+1:])
	track.Clips[clipIndex+1] = right
	sortTrackClips(track)

	return []Operation{
		{ID: "undo:" + operation.ID + ":delete", Kind: OperationDelete, ClipID: operation.NewClipID},
		{ID: "undo:" + operation.ID + ":trim", Kind: OperationTrim, ClipID: operation.ClipID, SourceRange: &oldSourceRange, TimelineRange: &oldTimelineRange},
	}, nil
}

func applyDelete(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/clip_id", "delete requires clip_id")}
	}
	timelineIndex, trackIndex, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	track := &document.Timelines[timelineIndex].Tracks[trackIndex]
	deleted, err := cloneClip(track.Clips[clipIndex])
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, err.Error())}
	}
	var rippleMoves []rippleMove
	if operation.Ripple {
		deletedEnd, endErr := deleted.TimelineRange.End()
		if endErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/ripple", endErr.Error())}
		}
		zero := RationalTime{Value: 0, Rate: 1}
		delta, deltaErr := zero.Sub(deleted.TimelineRange.Duration)
		if deltaErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/ripple", deltaErr.Error())}
		}
		rippleMoves, deltaErr = planRippleMoves(*track, deleted.ID, deletedEnd, delta)
		if deltaErr != nil {
			return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/ripple", deltaErr.Error())}
		}
	}
	track.Clips = append(track.Clips[:clipIndex], track.Clips[clipIndex+1:]...)
	applyRippleMoves(track, rippleMoves)
	sortTrackClips(track)
	inverses := []Operation{{ID: "undo:" + operation.ID, Kind: OperationInsert, TrackID: track.ID, Clip: &deleted}}
	for index := range rippleMoves {
		oldStart := rippleMoves[index].oldStart
		inverses = append(inverses, Operation{
			ID:     fmt.Sprintf("undo:%s:ripple:%d", operation.ID, index),
			Kind:   OperationMove,
			ClipID: rippleMoves[index].clipID,
			At:     &oldStart,
		})
	}
	return inverses, nil
}

func applyRetrack(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" || operation.TargetTrackID == "" {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "retrack requires clip_id and target_track_id")}
	}
	sourceTimeline, sourceTrack, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	targetTimeline, targetTrack, _, found := findTrack(*document, operation.TargetTrackID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeTrackMissing, path+"/target_track_id", fmt.Sprintf("track %q does not exist", operation.TargetTrackID))}
	}
	if sourceTimeline != targetTimeline {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path+"/target_track_id", "retrack cannot move a clip between timelines")}
	}
	source := &document.Timelines[sourceTimeline].Tracks[sourceTrack]
	target := &document.Timelines[targetTimeline].Tracks[targetTrack]
	if source.Kind != target.Kind {
		return nil, []Diagnostic{errorDiagnostic(CodeTrackKindMismatch, path+"/target_track_id", fmt.Sprintf("cannot move %s clip to %s track", source.Kind, target.Kind))}
	}
	oldTrackID := source.ID
	clip, err := cloneClip(source.Clips[clipIndex])
	if err != nil {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, err.Error())}
	}
	source.Clips = append(source.Clips[:clipIndex], source.Clips[clipIndex+1:]...)
	target.Clips = append(target.Clips, clip)
	sortTrackClips(source)
	if source != target {
		sortTrackClips(target)
	}
	return []Operation{{ID: "undo:" + operation.ID, Kind: OperationRetrack, ClipID: operation.ClipID, TargetTrackID: oldTrackID}}, nil
}

func applySetProperty(document *Document, operation Operation, path string) ([]Operation, []Diagnostic) {
	if operation.ClipID == "" || strings.TrimSpace(operation.Property) == "" {
		return nil, []Diagnostic{errorDiagnostic(CodeOperationInvalid, path, "set_property requires clip_id and property")}
	}
	if len(operation.Value) > 0 && !json.Valid(operation.Value) {
		return nil, []Diagnostic{errorDiagnostic(CodePropertyInvalid, path+"/value", "property value must be valid JSON or omitted to delete")}
	}
	timelineIndex, trackIndex, clipIndex, found := findClip(*document, operation.ClipID)
	if !found {
		return nil, []Diagnostic{errorDiagnostic(CodeClipMissing, path+"/clip_id", fmt.Sprintf("clip %q does not exist", operation.ClipID))}
	}
	clip := &document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex]
	if clip.Properties == nil {
		clip.Properties = make(map[string]json.RawMessage)
	}
	old, existed := clip.Properties[operation.Property]
	if len(operation.Value) == 0 {
		delete(clip.Properties, operation.Property)
	} else {
		clip.Properties[operation.Property] = append(json.RawMessage(nil), operation.Value...)
	}
	inverse := Operation{ID: "undo:" + operation.ID, Kind: OperationSetProperty, ClipID: operation.ClipID, Property: operation.Property}
	if existed {
		inverse.Value = append(json.RawMessage(nil), old...)
	}
	return []Operation{inverse}, nil
}

func findTrack(document Document, trackID ID) (timelineIndex, trackIndex int, track Track, found bool) {
	for ti, timeline := range document.Timelines {
		for tri, candidate := range timeline.Tracks {
			if candidate.ID == trackID {
				return ti, tri, candidate, true
			}
		}
	}
	return 0, 0, Track{}, false
}

func findClip(document Document, clipID ID) (timelineIndex, trackIndex, clipIndex int, found bool) {
	for ti, timeline := range document.Timelines {
		for tri, track := range timeline.Tracks {
			for ci, clip := range track.Clips {
				if clip.ID == clipID {
					return ti, tri, ci, true
				}
			}
		}
	}
	return 0, 0, 0, false
}

// sortTrackClips gives every mutation a single canonical sequence order. Clips on one track may
// not overlap, so chronological order cannot alter compositing semantics; track order remains the
// explicit layering mechanism. The ID tie-breaker is deterministic for invalid intermediate state
// and final validation still rejects equal-start overlaps.
func sortTrackClips(track *Track) {
	sort.SliceStable(track.Clips, func(i, j int) bool {
		comparison, err := track.Clips[i].TimelineRange.Start.Compare(track.Clips[j].TimelineRange.Start)
		if err != nil || comparison == 0 {
			return track.Clips[i].ID < track.Clips[j].ID
		}
		return comparison < 0
	})
}

type rippleMove struct {
	clipID   ID
	oldStart RationalTime
	newStart RationalTime
}

func planRippleMoves(track Track, excludedID ID, threshold, delta RationalTime) ([]rippleMove, error) {
	if delta.Value == 0 {
		return nil, nil
	}
	moves := make([]rippleMove, 0, len(track.Clips))
	for _, clip := range track.Clips {
		if clip.ID == excludedID {
			continue
		}
		comparison, err := clip.TimelineRange.Start.Compare(threshold)
		if err != nil {
			return nil, err
		}
		if comparison < 0 {
			continue
		}
		newStart, err := clip.TimelineRange.Start.Add(delta)
		if err != nil {
			return nil, err
		}
		moves = append(moves, rippleMove{clipID: clip.ID, oldStart: clip.TimelineRange.Start, newStart: newStart})
	}
	return moves, nil
}

func applyRippleMoves(track *Track, moves []rippleMove) {
	byID := make(map[ID]RationalTime, len(moves))
	for _, move := range moves {
		byID[move.clipID] = move.newStart
	}
	for index := range track.Clips {
		if start, exists := byID[track.Clips[index].ID]; exists {
			track.Clips[index].TimelineRange.Start = start
		}
	}
}

func cloneDocument(document Document) (Document, error) {
	data, err := CanonicalJSON(document)
	if err != nil {
		return Document{}, err
	}
	var clone Document
	if err := json.Unmarshal(data, &clone); err != nil {
		return Document{}, fmt.Errorf("videoedit: clone document: %w", err)
	}
	return clone, nil
}

func cloneClip(clip Clip) (Clip, error) {
	data, err := CanonicalJSON(clip)
	if err != nil {
		return Clip{}, err
	}
	var clone Clip
	if err := json.Unmarshal(data, &clone); err != nil {
		return Clip{}, fmt.Errorf("videoedit: clone clip: %w", err)
	}
	return clone, nil
}

func errorDiagnostic(code, path, message string) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message}
}
