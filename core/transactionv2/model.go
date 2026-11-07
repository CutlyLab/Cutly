// Package transactionv2 implements pure, atomic Document v2 mutations. Persistence,
// idempotency replay and commit authority belong to project/localv2, not this package.
package transactionv2

import (
	"encoding/json"
	d "github.com/CutlyLab/Cutly/core/documentv2"
)

const (
	SchemaVersion       = 2
	Profile             = "cutly.transaction.document-v2.number-preserving.v2"
	MaxTransactionBytes = 16 << 20
	MaxDocumentBytes    = 128 << 20
	MaxOperations       = 4096
	MaxDepth            = 64
	MaxNodes            = 2_000_000
	MaxStringBytes      = 1 << 20
	MaxIDBytes          = 256
)

type Transaction struct {
	SchemaVersion        int         `json:"schema_version"`
	Profile              string      `json:"profile"`
	ID                   string      `json:"id"`
	IdempotencyKey       string      `json:"idempotency_key"`
	DocumentID           d.ID        `json:"document_id"`
	ExpectedRevision     d.Revision  `json:"expected_revision"`
	ExpectedDocumentHash string      `json:"expected_document_hash"`
	Operations           []Operation `json:"operations"`
	Digest               string      `json:"digest,omitempty"`
}

type Kind string

const (
	AssetAdd         Kind = "asset_add"
	AssetRemove      Kind = "asset_remove"
	AssetReplace     Kind = "asset_replace"
	TimelineAdd      Kind = "timeline_add"
	TimelineRemove   Kind = "timeline_remove"
	TimelineReplace  Kind = "timeline_replace"
	TrackAdd         Kind = "track_add"
	TrackRemove      Kind = "track_remove"
	TrackReplace     Kind = "track_replace"
	ClipAdd          Kind = "clip_add"
	ClipRemove       Kind = "clip_remove"
	ClipReplace      Kind = "clip_replace"
	BusAdd           Kind = "bus_add"
	BusRemove        Kind = "bus_remove"
	BusReplace       Kind = "bus_replace"
	ClipMove         Kind = "clip_move"
	ClipTrim         Kind = "clip_trim"
	ClipSplit        Kind = "clip_split"
	ClipRetrack      Kind = "clip_retrack"
	ClipDelete       Kind = "clip_delete"
	ClipSetDisabled  Kind = "clip_set_disabled"
	ClipSetAsset     Kind = "clip_set_asset"
	PropertySet      Kind = "property_set"
	LaneSet          Kind = "lane_set"
	LaneRemove       Kind = "lane_remove"
	TransitionSet    Kind = "transition_set"
	TransitionRemove Kind = "transition_remove"
	TrackRoute       Kind = "track_route"
	MetadataSet      Kind = "metadata_set"
	MetadataRemove   Kind = "metadata_remove"
	AnnotationSet    Kind = "annotation_set"
	AnnotationRemove Kind = "annotation_remove"
)

// Operation is a closed union: exactly the payload corresponding to Kind is
// permitted. Expect is an optional, enforced target-state predicate. Every
// generated inverse carries one; it is not a caller-owned authority token.
type Operation struct {
	ID             string              `json:"id"`
	Kind           Kind                `json:"op"`
	Expect         *Precondition       `json:"expect,omitempty"`
	Asset          *AssetEdit          `json:"asset,omitempty"`
	Timeline       *TimelineEdit       `json:"timeline,omitempty"`
	Track          *TrackEdit          `json:"track,omitempty"`
	Clip           *ClipEdit           `json:"clip,omitempty"`
	Bus            *BusEdit            `json:"bus,omitempty"`
	Move           *MoveEdit           `json:"move,omitempty"`
	Trim           *TrimEdit           `json:"trim,omitempty"`
	Split          *SplitEdit          `json:"split,omitempty"`
	Retrack        *RetrackEdit        `json:"retrack,omitempty"`
	Disabled       *DisabledEdit       `json:"disabled,omitempty"`
	AssetReference *AssetReferenceEdit `json:"asset_reference,omitempty"`
	Properties     *PropertiesEdit     `json:"properties,omitempty"`
	Lane           *LaneEdit           `json:"lane,omitempty"`
	Transition     *TransitionEdit     `json:"transition,omitempty"`
	Route          *RouteEdit          `json:"route,omitempty"`
	Metadata       *MetadataEdit       `json:"metadata,omitempty"`
	Annotation     *AnnotationEdit     `json:"annotation,omitempty"`
}

type Precondition struct {
	Hash string `json:"hash"`
}

// Position is mandatory for add and replace, absent for remove. Replace never
// silently moves an entity: Position must equal its current position.
type AssetEdit struct {
	AssetID  d.ID     `json:"asset_id"`
	Position *int     `json:"position,omitempty"`
	Value    *d.Asset `json:"value,omitempty"`
}
type TimelineEdit struct {
	TimelineID d.ID        `json:"timeline_id"`
	Position   *int        `json:"position,omitempty"`
	Value      *d.Timeline `json:"value,omitempty"`
}
type TrackEdit struct {
	TimelineID d.ID     `json:"timeline_id"`
	TrackID    d.ID     `json:"track_id"`
	Position   *int     `json:"position,omitempty"`
	Value      *d.Track `json:"value,omitempty"`
}
type ClipEdit struct {
	TrackID  d.ID    `json:"track_id"`
	ClipID   d.ID    `json:"clip_id"`
	Position *int    `json:"position,omitempty"`
	Value    *d.Clip `json:"value,omitempty"`
}
type BusEdit struct {
	BusID    d.ID        `json:"bus_id"`
	Position *int        `json:"position,omitempty"`
	Value    *d.AudioBus `json:"value,omitempty"`
}
type MoveEdit struct {
	ClipID d.ID           `json:"clip_id"`
	At     d.RationalTime `json:"at"`
}
type TrimEdit struct {
	ClipID        d.ID        `json:"clip_id"`
	TimelineRange d.TimeRange `json:"timeline_range"`
	SourceRange   d.TimeRange `json:"source_range"`
}

// Split requires complete caller-authored replacements, including all animation
// and nested IDs. Core verifies exact source/timeline partitioning, never copies
// nested IDs or approximates cubic keyframes. Left.ID must retain ClipID.
type SplitEdit struct {
	ClipID d.ID           `json:"clip_id"`
	At     d.RationalTime `json:"at"`
	Left   d.Clip         `json:"left"`
	Right  d.Clip         `json:"right"`
}
type RetrackEdit struct {
	ClipID   d.ID `json:"clip_id"`
	TrackID  d.ID `json:"track_id"`
	Position int  `json:"position"`
}
type DisabledEdit struct {
	ClipID   d.ID `json:"clip_id"`
	Disabled bool `json:"disabled"`
}
type AssetReferenceEdit struct {
	ClipID  d.ID `json:"clip_id"`
	AssetID d.ID `json:"asset_id"`
}

// Omitted Value explicitly clears the complete PropertySet, not an individual
// untyped JSON path. All supported transform/crop/opacity/gain/pan/matrix fields
// are inherited from Document v2's closed typed property model.
type PropertiesEdit struct {
	ClipID d.ID           `json:"clip_id"`
	Value  *d.PropertySet `json:"value,omitempty"`
}
type LaneEdit struct {
	ClipID   d.ID            `json:"clip_id"`
	LaneID   d.ID            `json:"lane_id"`
	Position *int            `json:"position,omitempty"`
	Value    *d.KeyframeLane `json:"value,omitempty"`
}
type TransitionEdit struct {
	TimelineID   d.ID          `json:"timeline_id"`
	TransitionID d.ID          `json:"transition_id"`
	Position     *int          `json:"position,omitempty"`
	Value        *d.Transition `json:"value,omitempty"`
}
type RouteEdit struct {
	TrackID d.ID `json:"track_id"`
	BusID   d.ID `json:"bus_id"`
}

type ScopeKind string

const (
	ScopeDocument ScopeKind = "document"
	ScopeAsset    ScopeKind = "asset"
	ScopeTimeline ScopeKind = "timeline"
	ScopeTrack    ScopeKind = "track"
	ScopeClip     ScopeKind = "clip"
)

// Scope explicitly identifies one document-owned entity. Metadata is supported
// at document/asset/timeline/track scopes; annotations at document/timeline/track/clip.
type Scope struct {
	Kind ScopeKind `json:"kind"`
	ID   d.ID      `json:"id"`
}
type MetadataEdit struct {
	Scope Scope           `json:"scope"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value,omitempty"`
}
type AnnotationEdit struct {
	Scope        Scope         `json:"scope"`
	AnnotationID d.ID          `json:"annotation_id"`
	Position     *int          `json:"position,omitempty"`
	Value        *d.Annotation `json:"value,omitempty"`
}

type RenderImpact struct {
	IdentityChanged bool   `json:"identity_changed"`
	Visual          bool   `json:"visual"`
	Audio           bool   `json:"audio"`
	RenderRequired  bool   `json:"render_required"`
	TimelineIDs     []d.ID `json:"timeline_ids"`
}
type Result struct {
	Document          d.Document     `json:"document"`
	Inverse           Transaction    `json:"inverse"`
	Diagnostics       []d.Diagnostic `json:"diagnostics,omitempty"`
	Impact            RenderImpact   `json:"impact"`
	DocumentHash      string         `json:"document_hash,omitempty"`
	TransactionDigest string         `json:"transaction_digest,omitempty"`
}

const (
	CodeInvalid          = "TX.V2.INVALID"
	CodeLimit            = "TX.V2.LIMIT"
	CodeDocumentInvalid  = "TX.V2.DOCUMENT_INVALID"
	CodeDocumentConflict = "TX.V2.DOCUMENT_CONFLICT"
	CodeRevisionConflict = "TX.V2.REVISION_CONFLICT"
	CodeHashConflict     = "TX.V2.HASH_CONFLICT"
	CodeRevisionOverflow = "TX.V2.REVISION_OVERFLOW"
	CodeMissing          = "OP.V2.MISSING"
	CodeConflict         = "OP.V2.CONFLICT"
	CodePrecondition     = "OP.V2.POSTIMAGE_CONFLICT"
	CodeOperationInvalid = "OP.V2.INVALID"
	CodeSplit            = "OP.V2.SPLIT_INVALID"
)
