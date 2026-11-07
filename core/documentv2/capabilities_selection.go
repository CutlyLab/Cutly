package documentv2

import (
	"context"
	"fmt"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	CodeCapabilitySelectionInvalid  = "CAPABILITY.V2.SELECTION_INVALID"
	CodeCapabilitySelectionCanceled = "CAPABILITY.V2.SELECTION_CANCELED"
)

// CapabilitySelection identifies the enabled clips and active transitions of one timeline.
// ClipIDs must include both ordinary intersecting clips and every selected transition's
// participants, including participants outside their ordinary visible intervals. TransitionIDs
// are explicit: selecting both participants does not implicitly select their transition.
// Ordering is immaterial, but IDs must be unique within each list.
// Bus IDs are deliberately absent; routing dependencies are derived from the selected clips.
type CapabilitySelection struct {
	TimelineID    ID
	ClipIDs       []ID
	TransitionIDs []ID
}

type capabilitySelectionClip struct {
	clip  Clip
	track TrackKind
	busID ID
}

// RequiredCapabilitiesForSelection validates the entire document, then derives only the
// selection's rendering requirements, including indirect fonts and routed bus ancestors/master.
// It does not infer time intersection; callers select clips and active transitions themselves.
// Invalid documents/selections and cancellation return zero requirements. Outputs are sorted
// and unique, and neither input is mutated.
//
// Selection lengths are bounded by the authored timeline before selection-sized allocations.
// Callers handling untrusted documents should also admit/bound the document before calling:
// whole-document Validate is synchronous and cannot be interrupted by ctx. Cancellation is
// checked before/after validation and throughout the scoped traversal.
func RequiredCapabilitiesForSelection(ctx context.Context, document Document, selection CapabilitySelection) (CapabilityRequirements, []Diagnostic) {
	invalid := func(path, message string) (CapabilityRequirements, []Diagnostic) {
		return CapabilityRequirements{}, []Diagnostic{capabilityDiagnostic(CodeCapabilitySelectionInvalid, path, message)}
	}
	canceled := func() (CapabilityRequirements, []Diagnostic) {
		return CapabilityRequirements{}, []Diagnostic{capabilityDiagnostic(CodeCapabilitySelectionCanceled, "/selection", "capability selection canceled")}
	}
	if ctx == nil {
		return invalid("/selection", "context must not be nil")
	}
	if ctx.Err() != nil {
		return canceled()
	}

	// Count before allocating maps from caller-controlled selection lengths. Validation below
	// rejects duplicate document IDs; retaining the first timeline here is not an ambiguity grant.
	var timeline *Timeline
	for index := range document.Timelines {
		if ctx.Err() != nil {
			return canceled()
		}
		if document.Timelines[index].ID == selection.TimelineID && timeline == nil {
			timeline = &document.Timelines[index]
		}
	}
	if timeline == nil || selection.TimelineID == "" {
		return invalid("/selection/timeline_id", "selected timeline must exist")
	}
	// Saturate at the requested count to avoid integer overflow for very large documents.
	clipCount := 0
	for _, track := range timeline.Tracks {
		if ctx.Err() != nil {
			return canceled()
		}
		if len(track.Clips) >= len(selection.ClipIDs)-clipCount {
			clipCount = len(selection.ClipIDs)
			break
		}
		clipCount += len(track.Clips)
	}
	if len(selection.ClipIDs) > clipCount {
		return invalid("/selection/clip_ids", "selected clip count exceeds timeline clip count")
	}
	if len(selection.TransitionIDs) > len(timeline.Transitions) {
		return invalid("/selection/transition_ids", "selected transition count exceeds timeline transition count")
	}
	if ctx.Err() != nil {
		return canceled()
	}
	diagnostics := Validate(document)
	if ctx.Err() != nil {
		return canceled()
	}
	if HasErrorDiagnostics(diagnostics) {
		return CapabilityRequirements{}, diagnostics
	}

	clips := make(map[ID]capabilitySelectionClip, len(selection.ClipIDs))
	for index, id := range selection.ClipIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		path := fmt.Sprintf("/selection/clip_ids/%d", index)
		if id == "" {
			return invalid(path, "selected clip ID must not be empty")
		}
		if _, exists := clips[id]; exists {
			return invalid(path, "selected clip IDs must be unique")
		}
		clips[id] = capabilitySelectionClip{}
	}
	for _, track := range timeline.Tracks {
		if ctx.Err() != nil {
			return canceled()
		}
		for _, clip := range track.Clips {
			if ctx.Err() != nil {
				return canceled()
			}
			if _, selected := clips[clip.ID]; selected {
				clips[clip.ID] = capabilitySelectionClip{clip: clip, track: track.Kind, busID: track.AudioBusID}
			}
		}
	}
	for index, id := range selection.ClipIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		entry := clips[id]
		path := fmt.Sprintf("/selection/clip_ids/%d", index)
		if entry.clip.ID == "" {
			return invalid(path, "selected clip must belong to the selected timeline")
		}
		if entry.clip.Disabled {
			return invalid(path, "selected clip must be enabled")
		}
	}
	transitions := make(map[ID]Transition, len(selection.TransitionIDs))
	for index, id := range selection.TransitionIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		path := fmt.Sprintf("/selection/transition_ids/%d", index)
		if id == "" {
			return invalid(path, "selected transition ID must not be empty")
		}
		if _, exists := transitions[id]; exists {
			return invalid(path, "selected transition IDs must be unique")
		}
		transitions[id] = Transition{}
	}
	for _, transition := range timeline.Transitions {
		if ctx.Err() != nil {
			return canceled()
		}
		if _, selected := transitions[transition.ID]; selected {
			transitions[transition.ID] = transition
		}
	}
	for index, id := range selection.TransitionIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		transition := transitions[id]
		path := fmt.Sprintf("/selection/transition_ids/%d", index)
		if transition.ID == "" {
			return invalid(path, "selected transition must belong to the selected timeline")
		}
		_, outgoing := clips[transition.OutgoingClipID]
		_, incoming := clips[transition.IncomingClipID]
		if !outgoing || !incoming {
			return invalid(path, "selected transition participants must both be selected enabled clips")
		}
	}

	trackKinds := make(map[TrackKind]struct{})
	assetKinds := make(map[AssetKind]struct{})
	features := make(map[videoedit.CapabilityID]struct{})
	addFeature := func(value videoedit.CapabilityID) {
		if value != "" {
			features[value] = struct{}{}
		}
	}
	assets := make(map[ID]Asset, len(clips))
	for _, id := range selection.ClipIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		assets[clips[id].clip.AssetID] = Asset{}
	}
	for _, asset := range document.Assets {
		if ctx.Err() != nil {
			return canceled()
		}
		if _, selected := assets[asset.ID]; selected {
			assets[asset.ID] = asset
		}
	}
	busRoots := make(map[ID]struct{})
	for _, id := range selection.ClipIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		entry := clips[id]
		clip, asset := entry.clip, assets[entry.clip.AssetID]
		if entry.track != TrackKindAnnotation {
			trackKinds[entry.track] = struct{}{}
			assetKinds[asset.Kind] = struct{}{}
			addAssetCapabilities(asset, assetKinds, addFeature)
		}
		if entry.track == TrackKindAudio && len(document.AudioBuses) != 0 {
			busRoots[entry.busID] = struct{}{}
		}
		if (entry.track == TrackKindVideo && asset.Kind == AssetKindVideo) ||
			(entry.track == TrackKindAudio && (asset.Kind == AssetKindAudio || asset.Kind == AssetKindVideo)) {
			comparison, err := clip.SourceRange.Duration.Compare(clip.TimelineRange.Duration)
			if err == nil && comparison != 0 {
				if entry.track == TrackKindAudio {
					addFeature(videoedit.CapabilityAudioVarispeed)
				} else {
					addFeature(videoedit.CapabilityVideoLinearRetime)
				}
			}
		}
		addPropertyCapabilities(clip.PropertySet, addFeature)
		for _, lane := range clip.KeyframeLanes {
			if ctx.Err() != nil {
				return canceled()
			}
			addFeature(CapabilityKeyframesV1)
			addFeature(capabilityForKeyframeTargetAndAsset(lane.Target, asset))
			for _, keyframe := range lane.Keyframes {
				if ctx.Err() != nil {
					return canceled()
				}
				addFeature(capabilityForInterpolation(keyframe.Interpolation))
			}
		}
	}
	for _, id := range selection.TransitionIDs {
		if ctx.Err() != nil {
			return canceled()
		}
		transition := transitions[id]
		if transitionIsHardCut(transition) {
			continue
		}
		addFeature(videoedit.CapabilityID(transition.Profile))
		outgoing, incoming := clips[transition.OutgoingClipID], clips[transition.IncomingClipID]
		if outgoing.track == TrackKindVector && incoming.track == TrackKindVector &&
			vectorTransitionAsset(assets[outgoing.clip.AssetID]) && vectorTransitionAsset(assets[incoming.clip.AssetID]) {
			addFeature(CapabilityVectorTransitionScopeV1)
		} else if outgoing.track == TrackKindText && incoming.track == TrackKindText &&
			textTransitionAsset(assets[outgoing.clip.AssetID]) && textTransitionAsset(assets[incoming.clip.AssetID]) {
			addFeature(CapabilityTextTransitionScopeV1)
		}
	}
	if len(busRoots) != 0 {
		buses := make(map[ID]AudioBus, len(document.AudioBuses))
		for _, bus := range document.AudioBuses {
			if ctx.Err() != nil {
				return canceled()
			}
			buses[bus.ID] = bus
		}
		visited := make(map[ID]struct{})
		for root := range busRoots {
			for id := root; id != ""; {
				if ctx.Err() != nil {
					return canceled()
				}
				if _, exists := visited[id]; exists {
					break
				}
				visited[id] = struct{}{}
				bus := buses[id] // Whole-document validation proved every chain reaches the master.
				addFeature(CapabilityAudioRouteV1)
				addFeature(CapabilityAudioGainV1)
				addFeature(CapabilityAudioPanV1)
				if bus.Properties.SamplePeakLimiter != nil {
					addFeature(CapabilityAudioSamplePeakLimiterV1)
				}
				id = bus.ParentBusID
			}
		}
	}
	result := canonicalRequirements(trackKinds, assetKinds, features)
	if ctx.Err() != nil {
		return canceled()
	}
	return result, diagnostics
}
