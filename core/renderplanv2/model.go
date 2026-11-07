// Package renderplanv2 defines the additive Document v2 semantic render-plan boundary.
// It does not authorize resources or promise that a backend can execute a plan.
// Verify proves standalone consistency; only VerifyAgainstDocument proves that the
// resource, capability, bus and transition closure equals the authored document.
package renderplanv2

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const SchemaVersion uint32 = 1
const ProfileV1 = "cutly.semantic.document-v2-render-plan.v1"

// All collections are required, non-null arrays, including empty arrays. Authored
// ranges retain their time bases; computed intersections/projections are reduced.
// Wire indices are uint32, independent of the host's int width.
type ClipBinding struct {
	TrackID       documentv2.ID        `json:"track_id"`
	ClipID        documentv2.ID        `json:"clip_id"`
	AssetID       documentv2.ID        `json:"asset_id"`
	TrackKind     documentv2.TrackKind `json:"track_kind"`
	AssetKind     documentv2.AssetKind `json:"asset_kind"`
	TrackIndex    uint32               `json:"track_index"`
	ClipIndex     uint32               `json:"clip_index"`
	TimelineRange documentv2.TimeRange `json:"timeline_range"`
	SourceRange   documentv2.TimeRange `json:"source_range"`
	FontAssetID   documentv2.ID        `json:"font_asset_id"`
	AudioBusID    documentv2.ID        `json:"audio_bus_id"`
}

type PlannedClip struct {
	Binding              ClipBinding          `json:"binding"`
	VisibleTimelineRange documentv2.TimeRange `json:"visible_timeline_range"`
	VisibleSourceRange   documentv2.TimeRange `json:"visible_source_range"`
}

type HandleKind string

const (
	HandleHead HandleKind = "head"
	HandleTail HandleKind = "tail"
)

// HandleSpan denotes only temporal source positions outside the authored visible
// interval. Static sources have no temporal handles. Sampled ranges on a
// participant include its whole transition contribution, not an extra draw layer.
type HandleSpan struct {
	Kind          HandleKind           `json:"kind"`
	TimelineRange documentv2.TimeRange `json:"timeline_range"`
	SourceRange   documentv2.TimeRange `json:"source_range"`
}

type TransitionParticipant struct {
	Binding              ClipBinding          `json:"binding"`
	SampledTimelineRange documentv2.TimeRange `json:"sampled_timeline_range"`
	SampledSourceRange   documentv2.TimeRange `json:"sampled_source_range"`
	Handles              []HandleSpan         `json:"handles"`
}

type PlannedTransition struct {
	TransitionID        documentv2.ID           `json:"transition_id"`
	TransitionIndex     uint32                  `json:"transition_index"`
	Profile             string                  `json:"profile"`
	Cut                 documentv2.RationalTime `json:"cut"`
	PreDuration         documentv2.RationalTime `json:"pre_duration"`
	PostDuration        documentv2.RationalTime `json:"post_duration"`
	TimelineRange       documentv2.TimeRange    `json:"timeline_range"`
	ActiveTimelineRange documentv2.TimeRange    `json:"active_timeline_range"`
	Outgoing            TransitionParticipant   `json:"outgoing"`
	Incoming            TransitionParticipant   `json:"incoming"`
}

// Resources contain logical identities only, never URIs or machine locators.
// Empty ContentHash is explicit and means unmaterialized/URI-only, NOT verified
// content. Execution authorization and content binding belong to a later stage.
type ResourceRef struct {
	AssetID     documentv2.ID        `json:"asset_id"`
	Kind        documentv2.AssetKind `json:"kind"`
	ContentHash string               `json:"content_hash"`
}

type CapabilityRequirements struct {
	TrackKinds []documentv2.TrackKind   `json:"track_kinds"`
	AssetKinds []documentv2.AssetKind   `json:"asset_kinds"`
	Features   []videoedit.CapabilityID `json:"features"`
}

type Plan struct {
	SchemaVersion        uint32                 `json:"schema_version"`
	Profile              string                 `json:"profile"`
	CanonicalJSONProfile string                 `json:"canonical_json_profile"`
	DocumentID           documentv2.ID          `json:"document_id"`
	Revision             documentv2.Revision    `json:"revision"`
	DocumentHash         string                 `json:"document_hash"`
	TimelineID           documentv2.ID          `json:"timeline_id"`
	RequestedRange       documentv2.TimeRange   `json:"requested_range"`
	Target               videoedit.RenderTarget `json:"target"`
	FrameRange           videoedit.TickRange    `json:"frame_range"`
	SampleRange          videoedit.TickRange    `json:"sample_range"`
	RequiredCapabilities CapabilityRequirements `json:"required_capabilities"`
	Visual               []PlannedClip          `json:"visual"`
	Audio                []PlannedClip          `json:"audio"`
	Transitions          []PlannedTransition    `json:"transitions"`
	AudioBusIDs          []documentv2.ID        `json:"audio_bus_ids"`
	Resources            []ResourceRef          `json:"resources"`
	Fingerprint          string                 `json:"fingerprint"`
}
