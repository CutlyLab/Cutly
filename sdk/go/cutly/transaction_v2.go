package cutly

import (
	"context"

	"github.com/CutlyLab/Cutly/core/transactionv2"
)

// TransactionV2 is the canonical, typed Document v2 mutation envelope.
type TransactionV2 = transactionv2.Transaction
type OperationV2 = transactionv2.Operation
type OperationKindV2 = transactionv2.Kind
type OperationPreconditionV2 = transactionv2.Precondition
type AssetEditV2 = transactionv2.AssetEdit
type TimelineEditV2 = transactionv2.TimelineEdit
type TrackEditV2 = transactionv2.TrackEdit
type ClipEditV2 = transactionv2.ClipEdit
type BusEditV2 = transactionv2.BusEdit
type ClipMoveEditV2 = transactionv2.MoveEdit
type ClipTrimEditV2 = transactionv2.TrimEdit
type ClipSplitEditV2 = transactionv2.SplitEdit
type ClipRetrackEditV2 = transactionv2.RetrackEdit
type ClipDisabledEditV2 = transactionv2.DisabledEdit
type ClipAssetReferenceEditV2 = transactionv2.AssetReferenceEdit
type ClipPropertiesEditV2 = transactionv2.PropertiesEdit
type KeyframeLaneEditV2 = transactionv2.LaneEdit
type TransitionEditV2 = transactionv2.TransitionEdit
type TrackRouteEditV2 = transactionv2.RouteEdit
type ScopeV2 = transactionv2.Scope
type ScopeKindV2 = transactionv2.ScopeKind
type MetadataEditV2 = transactionv2.MetadataEdit
type AnnotationEditV2 = transactionv2.AnnotationEdit
type RenderImpactV2 = transactionv2.RenderImpact
type ApplyResultV2 = transactionv2.Result

const (
	TransactionSchemaVersionV2 = transactionv2.SchemaVersion
	TransactionProfileV2       = transactionv2.Profile

	OperationAssetAddV2         = transactionv2.AssetAdd
	OperationAssetRemoveV2      = transactionv2.AssetRemove
	OperationAssetReplaceV2     = transactionv2.AssetReplace
	OperationTimelineAddV2      = transactionv2.TimelineAdd
	OperationTimelineRemoveV2   = transactionv2.TimelineRemove
	OperationTimelineReplaceV2  = transactionv2.TimelineReplace
	OperationTrackAddV2         = transactionv2.TrackAdd
	OperationTrackRemoveV2      = transactionv2.TrackRemove
	OperationTrackReplaceV2     = transactionv2.TrackReplace
	OperationClipAddV2          = transactionv2.ClipAdd
	OperationClipRemoveV2       = transactionv2.ClipRemove
	OperationClipReplaceV2      = transactionv2.ClipReplace
	OperationBusAddV2           = transactionv2.BusAdd
	OperationBusRemoveV2        = transactionv2.BusRemove
	OperationBusReplaceV2       = transactionv2.BusReplace
	OperationClipMoveV2         = transactionv2.ClipMove
	OperationClipTrimV2         = transactionv2.ClipTrim
	OperationClipSplitV2        = transactionv2.ClipSplit
	OperationClipRetrackV2      = transactionv2.ClipRetrack
	OperationClipDeleteV2       = transactionv2.ClipDelete
	OperationClipSetDisabledV2  = transactionv2.ClipSetDisabled
	OperationClipSetAssetV2     = transactionv2.ClipSetAsset
	OperationPropertySetV2      = transactionv2.PropertySet
	OperationLaneSetV2          = transactionv2.LaneSet
	OperationLaneRemoveV2       = transactionv2.LaneRemove
	OperationTransitionSetV2    = transactionv2.TransitionSet
	OperationTransitionRemoveV2 = transactionv2.TransitionRemove
	OperationTrackRouteV2       = transactionv2.TrackRoute
	OperationMetadataSetV2      = transactionv2.MetadataSet
	OperationMetadataRemoveV2   = transactionv2.MetadataRemove
	OperationAnnotationSetV2    = transactionv2.AnnotationSet
	OperationAnnotationRemoveV2 = transactionv2.AnnotationRemove

	ScopeDocumentV2 = transactionv2.ScopeDocument
	ScopeAssetV2    = transactionv2.ScopeAsset
	ScopeTimelineV2 = transactionv2.ScopeTimeline
	ScopeTrackV2    = transactionv2.ScopeTrack
	ScopeClipV2     = transactionv2.ScopeClip
)

var ErrTransactionV2Invalid = transactionv2.ErrInvalid

// CreateTransactionV2 freezes document identity and canonical digest into a
// typed transaction proposal.
func CreateTransactionV2(document Document, transaction TransactionV2) (TransactionV2, error) {
	return transactionv2.Create(document, transaction)
}

func DecodeTransactionV2(raw []byte) (TransactionV2, error) {
	return transactionv2.Decode(raw)
}

func CanonicalTransactionV2(transaction TransactionV2) ([]byte, error) {
	return transactionv2.CanonicalJSON(transaction)
}

func TransactionDigestV2(transaction TransactionV2) (string, error) {
	return transactionv2.Digest(transaction)
}

// ApplyTransactionV2 applies atomically to an owned document candidate and
// returns a complete typed inverse. It performs no persistence or filesystem IO.
func ApplyTransactionV2(ctx context.Context, document Document, transaction TransactionV2) (ApplyResultV2, error) {
	return transactionv2.Apply(ctx, document, transaction)
}
