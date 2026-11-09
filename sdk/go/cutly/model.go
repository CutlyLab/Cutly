package cutly

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// Document is Cutly's current v2 authoring model. V1 editing transactions are
// named explicitly below because silently applying them to v2 would discard
// typed v2 semantics such as transitions, text, vectors and AudioGraph fields.
type Document = documentv2.Document
type ID = videoedit.ID
type Diagnostic = videoedit.Diagnostic
type BackendCapabilities = videoedit.BackendCapabilities
type EvaluationProgram = evaluator.Program
type FrozenSnapshot = evaluator.FrozenSnapshot
type SnapshotLimits = evaluator.SnapshotLimits
type FrameClock = videoedit.FrameClock
type SampleClock = videoedit.SampleClock
type RationalTime = videoedit.RationalTime
type TimeRange = videoedit.TimeRange
type Rational = documentv2.Rational
type Point = documentv2.Point
type Rect = documentv2.Rect
type ColorRGBA16 = documentv2.ColorRGBA16
type Asset = documentv2.Asset
type Track = documentv2.Track
type Clip = documentv2.Clip
type Transition = documentv2.Transition
type AudioBus = documentv2.AudioBus

const DocumentSchemaVersion = documentv2.SchemaVersion

const MaxDocumentBytes = 64 << 20

var ErrDocumentInvalid = documentv2.ErrDocumentInvalid

func DecodeDocument(raw []byte) (Document, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("%w: encoded length %d", ErrDocumentInvalid, len(raw))
	}
	return documentv2.Decode(raw)
}

func CanonicalDocument(document Document) ([]byte, error) {
	return documentv2.CanonicalJSON(document)
}

func DocumentHash(document Document) (string, error) {
	return documentv2.DocumentHash(document)
}

func ValidateDocument(document Document) []Diagnostic {
	return documentv2.Validate(document)
}

func MigrateDocumentV1JSON(raw []byte) (Document, []Diagnostic, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes {
		return Document{}, nil, fmt.Errorf("%w: v1 encoded length %d", ErrDocumentInvalid, len(raw))
	}
	return documentv2.MigrateV1JSON(raw)
}

// CompileDocument freezes one capability-checked evaluator snapshot. Preview
// and export consumers should share this immutable program rather than
// rebuilding timeline semantics in UI or backend code.
func CompileDocument(
	ctx context.Context,
	document Document,
	offered BackendCapabilities,
	limits SnapshotLimits,
) (*EvaluationProgram, *FrozenSnapshot, []Diagnostic) {
	return evaluator.CompileSnapshot(ctx, document, offered, limits)
}

// The v1 aliases remain public for the currently frozen transaction protocol.
// They are intentionally suffixed; a later v2 transaction protocol can be
// additive instead of changing the meaning of these symbols.
type DocumentV1 = videoedit.Document
type TransactionV1 = videoedit.Transaction
type OperationV1 = videoedit.Operation
type OperationKindV1 = videoedit.OperationKind
type ApplyResultV1 = videoedit.ApplyResult
type CommitRecordV1 = videoedit.CommitRecord
type IdempotencyLedgerV1 = videoedit.IdempotencyLedger
type RenderPlanV3 = videoedit.RenderPlan
type RenderTargetV3 = videoedit.RenderTarget
type RenderJobV1 = renderapi.RenderJob
type ExecutionManifestV1 = renderapi.ExecutionManifest
type RenderArtifactV1 = renderapi.RenderArtifact
type ExactFrameRequestV1 = renderapi.ExactFrameRequest
type ExactFrameArtifactV1 = renderapi.ExactFrameArtifact

const (
	OperationInsertV1      = videoedit.OperationInsert
	OperationMoveV1        = videoedit.OperationMove
	OperationTrimV1        = videoedit.OperationTrim
	OperationSplitV1       = videoedit.OperationSplit
	OperationDeleteV1      = videoedit.OperationDelete
	OperationRetrackV1     = videoedit.OperationRetrack
	OperationSetPropertyV1 = videoedit.OperationSetProperty
)

var ErrDocumentV1Invalid = errors.New("cutly sdk: Document v1 is invalid")

func DecodeDocumentV1(raw []byte) (DocumentV1, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes {
		return DocumentV1{}, fmt.Errorf("%w: encoded length %d", ErrDocumentV1Invalid, len(raw))
	}
	var document DocumentV1
	if err := wirejson.Decode(raw, &document); err != nil {
		return DocumentV1{}, fmt.Errorf("%w: decode: %v", ErrDocumentV1Invalid, err)
	}
	if diagnostics := videoedit.ValidateDocument(document); videoedit.HasErrorDiagnostics(diagnostics) {
		first := diagnostics[0]
		return DocumentV1{}, fmt.Errorf("%w: %s at %s: %s", ErrDocumentV1Invalid, first.Code, first.Path, first.Message)
	}
	return document, nil
}

func ApplyTransactionV1(document DocumentV1, transaction TransactionV1, ledger IdempotencyLedgerV1) (ApplyResultV1, error) {
	return videoedit.Apply(document, transaction, ledger)
}

func ResolveRenderPlanV3(document DocumentV1, renderRange TimeRange, target RenderTargetV3) (RenderPlanV3, []Diagnostic) {
	return videoedit.Resolve(document, renderRange, target)
}
