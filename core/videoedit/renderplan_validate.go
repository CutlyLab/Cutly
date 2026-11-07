package videoedit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// validateSemanticRenderPlan re-derives every semantic set and projection that a backend trusts.
// A fingerprint is an integrity checksum, not an authorization boundary: callers can recompute it
// over malformed input, so decode and direct evaluation must both perform these checks.
func validateSemanticRenderPlan(plan RenderPlan) error {
	if strings.TrimSpace(string(plan.DocumentID)) == "" || strings.TrimSpace(string(plan.TimelineID)) == "" {
		return fmt.Errorf("videoedit: document_id and timeline_id are required")
	}
	if !canonicalSHA256Digest(plan.DocumentHash) {
		return fmt.Errorf("videoedit: document_hash must be a canonical sha256 digest")
	}
	if plan.Target.TimelineID != plan.TimelineID {
		return fmt.Errorf("videoedit: target timeline %q does not match plan timeline %q", plan.Target.TimelineID, plan.TimelineID)
	}
	if plan.Target.Width <= 0 || plan.Target.Height <= 0 || plan.Target.Channels <= 0 {
		return fmt.Errorf("videoedit: target dimensions and channels must be positive")
	}
	if strings.TrimSpace(plan.Target.PixelFormat) == "" || strings.TrimSpace(plan.Target.ColorSpace) == "" {
		return fmt.Errorf("videoedit: target pixel_format and color_space are required")
	}

	wantFrames, err := CompileFrameTickRange(plan.RequestedRange, plan.Target.FrameClock)
	if err != nil {
		return fmt.Errorf("videoedit: compile frame range: %w", err)
	}
	if plan.FrameRange != wantFrames {
		return fmt.Errorf("videoedit: frame range %+v does not match requested range and clock %+v", plan.FrameRange, wantFrames)
	}
	wantSamples, err := CompileSampleTickRange(plan.RequestedRange, plan.Target.SampleClock)
	if err != nil {
		return fmt.Errorf("videoedit: compile sample range: %w", err)
	}
	if plan.SampleRange != wantSamples {
		return fmt.Errorf("videoedit: sample range %+v does not match requested range and clock %+v", plan.SampleRange, wantSamples)
	}

	resources := make(map[ID]ResourceRef, len(plan.Resources))
	for index, resource := range plan.Resources {
		if strings.TrimSpace(string(resource.AssetID)) == "" || !validAssetKind(resource.Kind) {
			return fmt.Errorf("videoedit: resource %d has invalid identity or kind", index)
		}
		if resource.ContentHash != "" && !canonicalSHA256Digest(resource.ContentHash) {
			return fmt.Errorf("videoedit: resource %d content_hash must be a canonical sha256 digest", index)
		}
		if index > 0 && plan.Resources[index-1].AssetID >= resource.AssetID {
			return fmt.Errorf("videoedit: resources must be unique and sorted by asset_id")
		}
		resources[resource.AssetID] = resource
	}

	state := semanticPlanState{
		resources:        resources,
		usedResources:    make(map[ID]struct{}, len(resources)),
		requiredTracks:   make(map[TrackKind]struct{}),
		requiredAssets:   make(map[AssetKind]struct{}),
		requiredFeatures: make(map[CapabilityID]struct{}),
		tracksByID:       make(map[ID]semanticTrackIdentity),
		tracksByIndex:    make(map[int]semanticTrackIdentity),
		clipIDs:          make(map[ID]struct{}),
		lastTrackEnd:     make(map[ID]RationalTime),
	}
	if err := validatePlannedClipList(plan.Video, TrackKindVideo, plan.RequestedRange, &state); err != nil {
		return fmt.Errorf("videoedit: video plan: %w", err)
	}
	if err := validatePlannedClipList(plan.Audio, TrackKindAudio, plan.RequestedRange, &state); err != nil {
		return fmt.Errorf("videoedit: audio plan: %w", err)
	}
	if len(state.usedResources) != len(resources) {
		return fmt.Errorf("videoedit: resources must exactly equal the assets referenced by planned clips")
	}

	wantRequirements := normalizedRequirements(state.requiredTracks, state.requiredAssets, state.requiredFeatures)
	if !slices.Equal(plan.RequiredCapabilities.TrackKinds, wantRequirements.TrackKinds) ||
		!slices.Equal(plan.RequiredCapabilities.AssetKinds, wantRequirements.AssetKinds) ||
		!slices.Equal(plan.RequiredCapabilities.Features, wantRequirements.Features) {
		return fmt.Errorf("videoedit: required capabilities do not match the plan-derived requirements")
	}
	return nil
}

type semanticTrackIdentity struct {
	id    ID
	kind  TrackKind
	index int
}

type semanticPlanState struct {
	resources        map[ID]ResourceRef
	usedResources    map[ID]struct{}
	requiredTracks   map[TrackKind]struct{}
	requiredAssets   map[AssetKind]struct{}
	requiredFeatures map[CapabilityID]struct{}
	tracksByID       map[ID]semanticTrackIdentity
	tracksByIndex    map[int]semanticTrackIdentity
	clipIDs          map[ID]struct{}
	lastTrackEnd     map[ID]RationalTime
}

func validatePlannedClipList(clips []PlannedClip, kind TrackKind, requested TimeRange, state *semanticPlanState) error {
	previousTrackIndex, previousClipIndex := -1, -1
	for index, clip := range clips {
		if strings.TrimSpace(string(clip.TrackID)) == "" || strings.TrimSpace(string(clip.ClipID)) == "" || strings.TrimSpace(string(clip.AssetID)) == "" {
			return fmt.Errorf("clip %d requires track_id, clip_id and asset_id", index)
		}
		if clip.TrackIndex < 0 || clip.ClipIndex < 0 {
			return fmt.Errorf("clip %q has a negative track or clip index", clip.ClipID)
		}
		if clip.TrackIndex < previousTrackIndex || (clip.TrackIndex == previousTrackIndex && clip.ClipIndex <= previousClipIndex) {
			return fmt.Errorf("clip %q is not in canonical track/clip order", clip.ClipID)
		}
		previousTrackIndex, previousClipIndex = clip.TrackIndex, clip.ClipIndex
		if _, duplicate := state.clipIDs[clip.ClipID]; duplicate {
			return fmt.Errorf("clip ID %q is duplicated", clip.ClipID)
		}
		state.clipIDs[clip.ClipID] = struct{}{}

		identity := semanticTrackIdentity{id: clip.TrackID, kind: kind, index: clip.TrackIndex}
		if existing, ok := state.tracksByID[clip.TrackID]; ok && existing != identity {
			return fmt.Errorf("track ID %q has inconsistent kind or index", clip.TrackID)
		}
		if existing, ok := state.tracksByIndex[clip.TrackIndex]; ok && existing != identity {
			return fmt.Errorf("track index %d has inconsistent ID or kind", clip.TrackIndex)
		}
		state.tracksByID[clip.TrackID] = identity
		state.tracksByIndex[clip.TrackIndex] = identity

		resource, ok := state.resources[clip.AssetID]
		if !ok {
			return fmt.Errorf("clip %q references resource %q that is not declared", clip.ClipID, clip.AssetID)
		}
		state.usedResources[clip.AssetID] = struct{}{}
		state.requiredTracks[kind] = struct{}{}
		state.requiredAssets[resource.Kind] = struct{}{}

		if err := validatePositiveRange(clip.TimelineRange); err != nil {
			return fmt.Errorf("clip %q timeline_range: %w", clip.ClipID, err)
		}
		if err := validatePositiveRange(clip.SourceRange); err != nil {
			return fmt.Errorf("clip %q source_range: %w", clip.ClipID, err)
		}
		if err := validatePositiveRange(clip.VisibleTimelineRange); err != nil {
			return fmt.Errorf("clip %q visible_timeline_range: %w", clip.ClipID, err)
		}
		if err := validatePositiveRange(clip.VisibleSourceRange); err != nil {
			return fmt.Errorf("clip %q visible_source_range: %w", clip.ClipID, err)
		}
		if previousEnd, exists := state.lastTrackEnd[clip.TrackID]; exists {
			comparison, err := clip.TimelineRange.Start.Compare(previousEnd)
			if err != nil || comparison < 0 {
				return fmt.Errorf("clip %q overlaps or precedes the previous clip on track %q", clip.ClipID, clip.TrackID)
			}
		}
		end, err := clip.TimelineRange.End()
		if err != nil {
			return fmt.Errorf("clip %q timeline end: %w", clip.ClipID, err)
		}
		state.lastTrackEnd[clip.TrackID] = end

		visibleTimeline, intersects, err := intersectRanges(clip.TimelineRange, requested)
		if err != nil || !intersects || visibleTimeline != clip.VisibleTimelineRange {
			return fmt.Errorf("clip %q visible timeline range is not the exact requested-range intersection", clip.ClipID)
		}
		visibleOffset, err := visibleTimeline.Start.Sub(clip.TimelineRange.Start)
		if err != nil {
			return fmt.Errorf("clip %q visible offset: %w", clip.ClipID, err)
		}
		sourceOffset, err := scaleTimeByRatio(visibleOffset, clip.SourceRange.Duration, clip.TimelineRange.Duration)
		if err != nil {
			return fmt.Errorf("clip %q source offset: %w", clip.ClipID, err)
		}
		sourceStart, err := clip.SourceRange.Start.Add(sourceOffset)
		if err != nil {
			return fmt.Errorf("clip %q visible source start: %w", clip.ClipID, err)
		}
		sourceDuration, err := scaleTimeByRatio(visibleTimeline.Duration, clip.SourceRange.Duration, clip.TimelineRange.Duration)
		if err != nil {
			return fmt.Errorf("clip %q visible source duration: %w", clip.ClipID, err)
		}
		if clip.VisibleSourceRange != (TimeRange{Start: sourceStart, Duration: sourceDuration}) {
			return fmt.Errorf("clip %q visible source range is not the exact linear projection", clip.ClipID)
		}
		for key, raw := range clip.Properties {
			if len(raw) == 0 || !json.Valid(raw) {
				return fmt.Errorf("clip %q property %q is not valid JSON", clip.ClipID, key)
			}
		}
		comparison, err := clip.SourceRange.Duration.Compare(clip.TimelineRange.Duration)
		if err != nil {
			return fmt.Errorf("clip %q retime: %w", clip.ClipID, err)
		}
		if comparison != 0 {
			state.requiredFeatures[retimeCapabilityForTrack(kind)] = struct{}{}
		}
	}
	return nil
}

func validatePositiveRange(value TimeRange) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Duration.Value <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	return nil
}

func canonicalSHA256Digest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	hexValue := strings.TrimPrefix(value, prefix)
	if hexValue != strings.ToLower(hexValue) {
		return false
	}
	_, err := hex.DecodeString(hexValue)
	return err == nil
}
