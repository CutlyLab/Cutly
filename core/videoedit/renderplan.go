package videoedit

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// RenderPlanSchemaVersion evolves independently from the canonical Document schema. Version 3
// fixes composition-wide frame/sample clocks and separates semantic requirements from the backend
// selected later by preflight. Version 1 and 2 plans must be rebuilt from their source Document.
const RenderPlanSchemaVersion = 3

// RenderSemanticProfile versions algorithms that affect the meaning of a semantic plan. Concrete
// compiler/backend builds belong to an execution manifest and cache key, not this profile.
const RenderSemanticProfile = "cutly.semantic.v1"

// RenderTarget selects one timeline and describes the output clocks and surface.
type RenderTarget struct {
	TimelineID  ID          `json:"timeline_id,omitempty"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	FrameClock  FrameClock  `json:"frame_clock"`
	SampleClock SampleClock `json:"sample_clock"`
	Channels    int         `json:"channels"`
	PixelFormat string      `json:"pixel_format,omitempty"`
	ColorSpace  string      `json:"color_space,omitempty"`
}

// CapabilityID identifies a versioned semantic operation a backend may offer.
type CapabilityID string

const (
	CapabilityVideoLinearRetime CapabilityID = "video.linear_retime.v1"
	// CapabilityAudioVarispeed changes speed and pitch together.
	CapabilityAudioVarispeed CapabilityID = "audio.linear_retime.v1"
	// CapabilityAudioTimeStretch preserves pitch and is deliberately not interchangeable with
	// varispeed.
	CapabilityAudioTimeStretch CapabilityID = "audio.time_stretch_preserve_pitch.v1"

	// Deprecated aliases keep incubation consumers source-compatible while v3 is integrated.
	BackendFeatureLinearVideoRetime = CapabilityVideoLinearRetime
	BackendFeatureLinearAudioRetime = CapabilityAudioVarispeed
)

// BackendFeature is the pre-v3 name for CapabilityID.
type BackendFeature = CapabilityID

// CapabilityRequirements is derived by Resolve and enters the semantic plan fingerprint.
type CapabilityRequirements struct {
	TrackKinds []TrackKind    `json:"track_kinds,omitempty"`
	AssetKinds []AssetKind    `json:"asset_kinds,omitempty"`
	Features   []CapabilityID `json:"features,omitempty"`
}

// BackendCapabilities is supplied to CheckCapabilities during preflight. It is never embedded in
// a semantic RenderPlan. Empty slices mean no declared support, not "support everything".
type BackendCapabilities struct {
	BackendID        string         `json:"backend_id"`
	BuildFingerprint string         `json:"build_fingerprint,omitempty"`
	TrackKinds       []TrackKind    `json:"track_kinds,omitempty"`
	AssetKinds       []AssetKind    `json:"asset_kinds,omitempty"`
	Features         []CapabilityID `json:"features,omitempty"`
	MaxWidth         int            `json:"max_width,omitempty"`
	MaxHeight        int            `json:"max_height,omitempty"`
}

// PlannedClip is a backend-neutral layer reference. TimelineRange and SourceRange preserve the
// authored ranges for audit and source-time mapping. VisibleTimelineRange and VisibleSourceRange
// contain the exact timeline intersection and its linear projection into source time, so a backend
// never has to rediscover clipping or reconstruct timing from frame-index floats.
type PlannedClip struct {
	TrackID              ID                         `json:"track_id"`
	ClipID               ID                         `json:"clip_id"`
	AssetID              ID                         `json:"asset_id"`
	TrackIndex           int                        `json:"track_index"`
	ClipIndex            int                        `json:"clip_index"`
	TimelineRange        TimeRange                  `json:"timeline_range"`
	SourceRange          TimeRange                  `json:"source_range"`
	VisibleTimelineRange TimeRange                  `json:"visible_timeline_range"`
	VisibleSourceRange   TimeRange                  `json:"visible_source_range"`
	Properties           map[string]json.RawMessage `json:"properties,omitempty"`
}

// ResourceRef is a backend-neutral logical resource. URI and machine paths are materialized only
// into an ExecutionManifest after the semantic plan has been compiled.
type ResourceRef struct {
	AssetID     ID        `json:"asset_id"`
	Kind        AssetKind `json:"kind"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// RenderPlan is the deterministic semantic boundary shared by preview and export backends.
type RenderPlan struct {
	SchemaVersion        int                    `json:"schema_version"`
	SemanticProfile      string                 `json:"semantic_profile"`
	DocumentID           ID                     `json:"document_id"`
	Revision             Revision               `json:"revision"`
	DocumentHash         string                 `json:"document_hash"`
	TimelineID           ID                     `json:"timeline_id"`
	RequestedRange       TimeRange              `json:"requested_range"`
	Target               RenderTarget           `json:"target"`
	FrameRange           TickRange              `json:"frame_range"`
	SampleRange          TickRange              `json:"sample_range"`
	RequiredCapabilities CapabilityRequirements `json:"required_capabilities"`
	Video                []PlannedClip          `json:"video,omitempty"`
	Audio                []PlannedClip          `json:"audio,omitempty"`
	Resources            []ResourceRef          `json:"resources,omitempty"`
	Fingerprint          string                 `json:"fingerprint"`
}

// Resolve validates a document and compiles the selected timeline range into a backend-neutral
// plan. It performs no I/O and returns stable diagnostics rather than backend-specific errors.
func Resolve(document Document, renderRange TimeRange, target RenderTarget) (RenderPlan, []Diagnostic) {
	plan := RenderPlan{
		SchemaVersion:   RenderPlanSchemaVersion,
		SemanticProfile: RenderSemanticProfile,
		DocumentID:      document.ID,
		Revision:        document.Revision,
		RequestedRange:  renderRange,
		Target:          target,
	}
	diagnostics := ValidateDocument(document)
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}

	if err := renderRange.Validate(); err != nil || renderRange.Duration.Value <= 0 {
		message := "render range must have a valid, positive duration"
		if err != nil {
			message += ": " + err.Error()
		}
		add(CodeRenderRangeInvalid, "/range", message)
	}
	validateRenderTarget(target, add)

	timeline, timelineIndex, ok := selectTimeline(document.Timelines, target.TimelineID, add)
	if ok {
		plan.TimelineID = timeline.ID
		plan.Target.TimelineID = timeline.ID
	}

	documentHash, err := DocumentHash(document)
	if err != nil {
		add(CodeJSONInvalid, "", err.Error())
	} else {
		plan.DocumentHash = documentHash
	}
	if HasErrorDiagnostics(diagnostics) || !ok {
		return plan, diagnostics
	}

	frameRange, err := CompileFrameTickRange(renderRange, plan.Target.FrameClock)
	if err != nil {
		code := CodeRenderRangeInvalid
		if errors.Is(err, ErrTimeOverflow) || errors.Is(err, ErrInvalidTickRange) {
			code = CodeTimeIndexOverflow
		}
		add(code, "/frame_range", fmt.Sprintf("cannot compile frame range: %v", err))
	} else {
		plan.FrameRange = frameRange
	}
	sampleRange, err := CompileSampleTickRange(renderRange, plan.Target.SampleClock)
	if err != nil {
		code := CodeRenderRangeInvalid
		if errors.Is(err, ErrTimeOverflow) || errors.Is(err, ErrInvalidTickRange) {
			code = CodeTimeIndexOverflow
		}
		add(code, "/sample_range", fmt.Sprintf("cannot compile sample range: %v", err))
	} else {
		plan.SampleRange = sampleRange
	}
	if HasErrorDiagnostics(diagnostics) {
		return plan, diagnostics
	}

	assets := make(map[ID]Asset, len(document.Assets))
	for _, asset := range document.Assets {
		assets[asset.ID] = asset
	}
	resources := make(map[ID]ResourceRef)
	requiredTracks := make(map[TrackKind]struct{})
	requiredAssets := make(map[AssetKind]struct{})
	requiredFeatures := make(map[CapabilityID]struct{})
	for trackIndex, track := range timeline.Tracks {
		for clipIndex, clip := range track.Clips {
			if clip.Disabled {
				continue
			}
			visibleTimelineRange, intersects, err := intersectRanges(clip.TimelineRange, renderRange)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/timeline_range", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot intersect clip with render range: %v", err))
				continue
			}
			if !intersects {
				continue
			}
			asset, exists := assets[clip.AssetID]
			if !exists {
				continue // ValidateDocument already emitted the stable reference diagnostic.
			}
			retimeComparison, err := clip.SourceRange.Duration.Compare(clip.TimelineRange.Duration)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot resolve clip playback rate: %v", err))
				continue
			}
			if retimeComparison != 0 {
				requiredFeatures[retimeCapabilityForTrack(track.Kind)] = struct{}{}
			}
			visibleOffset, err := visibleTimelineRange.Start.Sub(clip.TimelineRange.Start)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/timeline_range", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot map visible timeline range: %v", err))
				continue
			}
			visibleSourceOffset, err := scaleTimeByRatio(visibleOffset, clip.SourceRange.Duration, clip.TimelineRange.Duration)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/source_range", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot map visible source offset: %v", err))
				continue
			}
			visibleSourceStart, err := clip.SourceRange.Start.Add(visibleSourceOffset)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/source_range", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot map visible source range: %v", err))
				continue
			}
			visibleSourceDuration, err := scaleTimeByRatio(visibleTimelineRange.Duration, clip.SourceRange.Duration, clip.TimelineRange.Duration)
			if err != nil {
				add(CodeTimeRangeInvalid, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/source_range", timelineIndex, trackIndex, clipIndex), fmt.Sprintf("cannot map visible source duration: %v", err))
				continue
			}
			visibleSourceRange := TimeRange{
				Start:    visibleSourceStart,
				Duration: visibleSourceDuration,
			}
			planned := PlannedClip{
				TrackID:              track.ID,
				ClipID:               clip.ID,
				AssetID:              clip.AssetID,
				TrackIndex:           trackIndex,
				ClipIndex:            clipIndex,
				TimelineRange:        clip.TimelineRange,
				SourceRange:          clip.SourceRange,
				VisibleTimelineRange: visibleTimelineRange,
				VisibleSourceRange:   visibleSourceRange,
				Properties:           cloneRawMap(clip.Properties),
			}
			switch track.Kind {
			case TrackKindVideo:
				plan.Video = append(plan.Video, planned)
			case TrackKindAudio:
				plan.Audio = append(plan.Audio, planned)
			}
			requiredTracks[track.Kind] = struct{}{}
			requiredAssets[asset.Kind] = struct{}{}
			resources[asset.ID] = ResourceRef{AssetID: asset.ID, Kind: asset.Kind, ContentHash: asset.ContentHash}
		}
	}
	if HasErrorDiagnostics(diagnostics) {
		return plan, diagnostics
	}

	resourceIDs := make([]string, 0, len(resources))
	for assetID := range resources {
		resourceIDs = append(resourceIDs, string(assetID))
	}
	sort.Strings(resourceIDs)
	for _, assetID := range resourceIDs {
		plan.Resources = append(plan.Resources, resources[ID(assetID)])
	}
	plan.RequiredCapabilities = normalizedRequirements(requiredTracks, requiredAssets, requiredFeatures)

	fingerprintInput := plan
	fingerprintInput.Fingerprint = ""
	fingerprint, err := canonicalHash(fingerprintInput)
	if err != nil {
		add(CodeJSONInvalid, "", err.Error())
		return plan, diagnostics
	}
	plan.Fingerprint = fingerprint
	return plan, diagnostics
}

func selectTimeline(timelines []Timeline, requested ID, add func(string, string, string)) (Timeline, int, bool) {
	if requested != "" {
		for index, timeline := range timelines {
			if timeline.ID == requested {
				return timeline, index, true
			}
		}
		add(CodeRenderTimelineMissing, "/target/timeline_id", fmt.Sprintf("timeline %q does not exist", requested))
		return Timeline{}, -1, false
	}
	if len(timelines) == 1 {
		return timelines[0], 0, true
	}
	if len(timelines) == 0 {
		add(CodeRenderTimelineMissing, "/target/timeline_id", "document has no timeline to render")
	} else {
		add(CodeRenderTimelineRequired, "/target/timeline_id", "timeline_id is required when a document has multiple timelines")
	}
	return Timeline{}, -1, false
}

func validateRenderTarget(target RenderTarget, add func(string, string, string)) {
	if target.Width <= 0 || target.Height <= 0 {
		add(CodeRenderTargetInvalid, "/target", "render target width and height must be positive")
	}
	if err := target.FrameClock.Validate(); err != nil {
		add(CodeRenderTargetInvalid, "/target/frame_clock", err.Error())
	}
	if err := target.SampleClock.Validate(); err != nil {
		add(CodeRenderTargetInvalid, "/target/sample_clock", err.Error())
	}
	if target.Channels <= 0 {
		add(CodeRenderTargetInvalid, "/target/channels", "channel count must be positive")
	}
	if strings.TrimSpace(target.PixelFormat) == "" {
		add(CodeRenderTargetInvalid, "/target/pixel_format", "pixel format is required")
	}
	if strings.TrimSpace(target.ColorSpace) == "" {
		add(CodeRenderTargetInvalid, "/target/color_space", "color space is required")
	}
}

// CheckCapabilities compares a backend's offered capabilities with a frozen semantic plan.
// Backend identity and ordering never affect the plan fingerprint.
func CheckCapabilities(plan RenderPlan, offered BackendCapabilities) []Diagnostic {
	var diagnostics []Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}
	if plan.SchemaVersion != RenderPlanSchemaVersion {
		add(CodeRenderPlanUnsupported, "/schema_version", fmt.Sprintf("render plan schema %d is not supported; expected %d", plan.SchemaVersion, RenderPlanSchemaVersion))
		return diagnostics
	}
	if plan.SemanticProfile != RenderSemanticProfile {
		add(CodeRenderPlanUnsupported, "/semantic_profile", fmt.Sprintf("semantic profile %q is not supported", plan.SemanticProfile))
		return diagnostics
	}

	offered = normalizedCapabilities(offered)
	if (offered.MaxWidth > 0 && plan.Target.Width > offered.MaxWidth) || (offered.MaxHeight > 0 && plan.Target.Height > offered.MaxHeight) {
		add(CodeCapabilityResolution, "/target", fmt.Sprintf("target %dx%d exceeds backend %q limit %dx%d", plan.Target.Width, plan.Target.Height, offered.BackendID, offered.MaxWidth, offered.MaxHeight))
	}
	for index, required := range plan.RequiredCapabilities.TrackKinds {
		if !supportsTrack(offered, required) {
			add(CodeCapabilityTrack, fmt.Sprintf("/required_capabilities/track_kinds/%d", index), fmt.Sprintf("backend %q does not support %s tracks", offered.BackendID, required))
		}
	}
	for index, required := range plan.RequiredCapabilities.AssetKinds {
		if !supportsAsset(offered, required) {
			add(CodeCapabilityAsset, fmt.Sprintf("/required_capabilities/asset_kinds/%d", index), fmt.Sprintf("backend %q does not support %s assets", offered.BackendID, required))
		}
	}
	for index, required := range plan.RequiredCapabilities.Features {
		if !supportsFeature(offered, required) {
			add(CodeCapabilityFeature, fmt.Sprintf("/required_capabilities/features/%d", index), fmt.Sprintf("backend %q does not support required feature %q", offered.BackendID, required))
		}
	}
	return diagnostics
}

func normalizedCapabilities(capabilities BackendCapabilities) BackendCapabilities {
	clone := capabilities
	clone.TrackKinds = uniqueTrackKinds(capabilities.TrackKinds)
	clone.AssetKinds = uniqueAssetKinds(capabilities.AssetKinds)
	clone.Features = uniqueCapabilityIDs(capabilities.Features)
	sort.Slice(clone.TrackKinds, func(i, j int) bool { return clone.TrackKinds[i] < clone.TrackKinds[j] })
	sort.Slice(clone.AssetKinds, func(i, j int) bool { return clone.AssetKinds[i] < clone.AssetKinds[j] })
	sort.Slice(clone.Features, func(i, j int) bool { return clone.Features[i] < clone.Features[j] })
	return clone
}

// CanonicalBackendCapabilities returns a defensive copy with set-like fields deduplicated and
// sorted. Execution manifests use this form so backend declaration ordering cannot change hashes.
func CanonicalBackendCapabilities(capabilities BackendCapabilities) BackendCapabilities {
	return normalizedCapabilities(capabilities)
}

func normalizedRequirements(tracks map[TrackKind]struct{}, assets map[AssetKind]struct{}, features map[CapabilityID]struct{}) CapabilityRequirements {
	result := CapabilityRequirements{
		TrackKinds: make([]TrackKind, 0, len(tracks)),
		AssetKinds: make([]AssetKind, 0, len(assets)),
		Features:   make([]CapabilityID, 0, len(features)),
	}
	for value := range tracks {
		result.TrackKinds = append(result.TrackKinds, value)
	}
	for value := range assets {
		result.AssetKinds = append(result.AssetKinds, value)
	}
	for value := range features {
		result.Features = append(result.Features, value)
	}
	sort.Slice(result.TrackKinds, func(i, j int) bool { return result.TrackKinds[i] < result.TrackKinds[j] })
	sort.Slice(result.AssetKinds, func(i, j int) bool { return result.AssetKinds[i] < result.AssetKinds[j] })
	sort.Slice(result.Features, func(i, j int) bool { return result.Features[i] < result.Features[j] })
	return result
}

func uniqueTrackKinds(values []TrackKind) []TrackKind {
	seen := make(map[TrackKind]struct{}, len(values))
	result := make([]TrackKind, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueAssetKinds(values []AssetKind) []AssetKind {
	seen := make(map[AssetKind]struct{}, len(values))
	result := make([]AssetKind, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueCapabilityIDs(values []CapabilityID) []CapabilityID {
	seen := make(map[CapabilityID]struct{}, len(values))
	result := make([]CapabilityID, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneRawMap(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		clone[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}

func supportsTrack(capabilities BackendCapabilities, kind TrackKind) bool {
	for _, supported := range capabilities.TrackKinds {
		if supported == kind {
			return true
		}
	}
	return false
}

func supportsAsset(capabilities BackendCapabilities, kind AssetKind) bool {
	for _, supported := range capabilities.AssetKinds {
		if supported == kind {
			return true
		}
	}
	return false
}

func supportsFeature(capabilities BackendCapabilities, feature CapabilityID) bool {
	for _, supported := range capabilities.Features {
		if supported == feature {
			return true
		}
	}
	return false
}

func retimeCapabilityForTrack(kind TrackKind) CapabilityID {
	if kind == TrackKindAudio {
		return CapabilityAudioVarispeed
	}
	return CapabilityVideoLinearRetime
}

func rangesIntersect(left, right TimeRange) (bool, error) {
	_, intersects, err := intersectRanges(left, right)
	return intersects, err
}

func intersectRanges(left, right TimeRange) (TimeRange, bool, error) {
	leftEnd, err := left.End()
	if err != nil {
		return TimeRange{}, false, err
	}
	rightEnd, err := right.End()
	if err != nil {
		return TimeRange{}, false, err
	}
	start := left.Start
	if comparison, compareErr := right.Start.Compare(start); compareErr != nil {
		return TimeRange{}, false, compareErr
	} else if comparison > 0 {
		start = right.Start
	}
	end := leftEnd
	if comparison, compareErr := rightEnd.Compare(end); compareErr != nil {
		return TimeRange{}, false, compareErr
	} else if comparison < 0 {
		end = rightEnd
	}
	duration, err := end.Sub(start)
	if err != nil {
		return TimeRange{}, false, err
	}
	if comparison, compareErr := duration.Compare(RationalTime{Value: 0, Rate: 1}); compareErr != nil {
		return TimeRange{}, false, compareErr
	} else if comparison <= 0 {
		return TimeRange{}, false, nil
	}
	return TimeRange{Start: start, Duration: duration}, true, nil
}
