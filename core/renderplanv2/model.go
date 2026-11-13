// Package renderplanv2 freezes the backend-neutral semantic render plan for a
// committed Cutly Document v2.  It is additive to core/videoedit RenderPlan v3:
// the legacy RC protocol remains byte-for-byte unchanged.
package renderplanv2

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	SchemaVersion = 1
	ProfileV1     = "cutly.semantic.document-v2-render-plan.v1"
	HashProfileV1 = "cutly.hash.sha256.document-v2-render-plan-canonical-json.v1"
)

// PlannedClip is one ordinary visible clip intersection. Transition handle
// access is represented separately by PlannedTransition so a backend never has
// to overload a clip's authored half-open visible interval.
type PlannedClip struct {
	TrackID              documentv2.ID        `json:"track_id"`
	ClipID               documentv2.ID        `json:"clip_id"`
	AssetID              documentv2.ID        `json:"asset_id"`
	TrackKind            documentv2.TrackKind `json:"track_kind"`
	AssetKind            documentv2.AssetKind `json:"asset_kind"`
	TrackIndex           int                  `json:"track_index"`
	ClipIndex            int                  `json:"clip_index"`
	TimelineRange        documentv2.TimeRange `json:"timeline_range"`
	SourceRange          documentv2.TimeRange `json:"source_range"`
	VisibleTimelineRange documentv2.TimeRange `json:"visible_timeline_range"`
	VisibleSourceRange   documentv2.TimeRange `json:"visible_source_range"`
}

// TransitionSide freezes the exact source interval a transition side may
// sample over the selected part of its transition window. The interval can lie
// before or after the clip's ordinary source range: those are explicit handles,
// validated by Document v2 and later re-probed by media preflight.
type TransitionSide struct {
	TrackID             documentv2.ID        `json:"track_id"`
	ClipID              documentv2.ID        `json:"clip_id"`
	AssetID             documentv2.ID        `json:"asset_id"`
	TrackKind           documentv2.TrackKind `json:"track_kind"`
	AssetKind           documentv2.AssetKind `json:"asset_kind"`
	TrackIndex          int                  `json:"track_index"`
	ClipIndex           int                  `json:"clip_index"`
	SelectedSourceRange documentv2.TimeRange `json:"selected_source_range"`
}

// PlannedTransition is an active, non-zero transition whose half-open window
// intersects RequestedRange. Hard-cut records remain in the Document but do not
// enter the executable transition closure.
type PlannedTransition struct {
	TransitionID    documentv2.ID           `json:"transition_id"`
	Profile         string                  `json:"profile"`
	TransitionIndex int                     `json:"transition_index"`
	PreDuration     documentv2.RationalTime `json:"pre_duration"`
	PostDuration    documentv2.RationalTime `json:"post_duration"`
	Window          documentv2.TimeRange    `json:"window"`
	SelectedWindow  documentv2.TimeRange    `json:"selected_window"`
	Outgoing        TransitionSide          `json:"outgoing"`
	Incoming        TransitionSide          `json:"incoming"`
}

// ResourceRef is a logical external dependency. Locators are deliberately
// excluded; the host materializes them only in ExecutionManifest v2. Inline
// text/vector payloads remain authenticated by DocumentHash and do not appear
// as external resources. Pinned fonts do.
type ResourceRef struct {
	AssetID     documentv2.ID        `json:"asset_id"`
	Kind        documentv2.AssetKind `json:"kind"`
	ContentHash string               `json:"content_hash,omitempty"`
}

// Plan is the immutable semantic closure shared by preview and export.
type Plan struct {
	SchemaVersion        int                               `json:"schema_version"`
	Profile              string                            `json:"profile"`
	CanonicalJSONProfile string                            `json:"canonical_json_profile"`
	HashProfile          string                            `json:"hash_profile"`
	DocumentID           documentv2.ID                     `json:"document_id"`
	Revision             documentv2.Revision               `json:"revision"`
	DocumentHash         string                            `json:"document_hash"`
	TimelineID           documentv2.ID                     `json:"timeline_id"`
	RequestedRange       documentv2.TimeRange              `json:"requested_range"`
	Target               videoedit.RenderTarget            `json:"target"`
	FrameRange           videoedit.TickRange               `json:"frame_range"`
	SampleRange          videoedit.TickRange               `json:"sample_range"`
	RequiredCapabilities documentv2.CapabilityRequirements `json:"required_capabilities"`
	Visual               []PlannedClip                     `json:"visual,omitempty"`
	Audio                []PlannedClip                     `json:"audio,omitempty"`
	Transitions          []PlannedTransition               `json:"transitions,omitempty"`
	Resources            []ResourceRef                     `json:"resources,omitempty"`
	Fingerprint          string                            `json:"fingerprint"`
}

func newPlan() Plan {
	return Plan{
		SchemaVersion:        SchemaVersion,
		Profile:              ProfileV1,
		CanonicalJSONProfile: canonicaljson.Profile,
		HashProfile:          HashProfileV1,
	}
}
