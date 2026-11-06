package documentv2

import (
	"fmt"
	"sort"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

// Capability IDs are semantic contracts, not implementation names. A backend may advertise one
// only when it consumes the corresponding v2 fields exactly as specified by the evaluator profile.
const (
	CapabilityTransformV1                videoedit.CapabilityID = videoedit.CapabilityID(PropertyTransformV1)
	CapabilityOpacityV1                  videoedit.CapabilityID = videoedit.CapabilityID(PropertyOpacityV1)
	CapabilityCropV1                     videoedit.CapabilityID = videoedit.CapabilityID(PropertyCropV1)
	CapabilityTextV1                     videoedit.CapabilityID = "cutly.text.v1"
	CapabilityTextPinnedFontViewportPXV2 videoedit.CapabilityID = videoedit.CapabilityID(TextProfilePinnedFontViewportPXV2)
	CapabilityAudioGainV1                videoedit.CapabilityID = videoedit.CapabilityID(PropertyAudioGainV1)
	CapabilityAudioPanV1                 videoedit.CapabilityID = videoedit.CapabilityID(PropertyAudioPanV1)
	CapabilityAudioRouteV1               videoedit.CapabilityID = videoedit.CapabilityID(PropertyAudioRouteV1)
	CapabilityAudioChannelMatrixV1       videoedit.CapabilityID = videoedit.CapabilityID(PropertyAudioChannelMatrixV1)
	CapabilityAudioSamplePeakLimiterV1   videoedit.CapabilityID = videoedit.CapabilityID(PropertyAudioSamplePeakLimiterV1)
	CapabilityKeyframesV1                videoedit.CapabilityID = "cutly.keyframes.v1"
	CapabilityInterpolationHoldV1        videoedit.CapabilityID = "cutly.interpolation.hold.v1"
	CapabilityInterpolationLinearV1      videoedit.CapabilityID = "cutly.interpolation.linear.v1"
	CapabilityInterpolationCubicV1       videoedit.CapabilityID = "cutly.interpolation.cubic-bezier.v1"
	CapabilityVectorPathV1               videoedit.CapabilityID = videoedit.CapabilityID(VectorProfilePathV1)
	CapabilityVectorPathViewportPXV2     videoedit.CapabilityID = videoedit.CapabilityID(VectorProfilePathViewportPXV2)
	CapabilityVectorTransitionScopeV1    videoedit.CapabilityID = videoedit.CapabilityID(VectorTransitionScopeProfileV1)
)

const (
	CodeCapabilityTrack        = "CAPABILITY.V2.TRACK_UNSUPPORTED"
	CodeCapabilityAsset        = "CAPABILITY.V2.ASSET_UNSUPPORTED"
	CodeCapabilityFeature      = "CAPABILITY.V2.FEATURE_UNSUPPORTED"
	CodeV1SemanticIncompatible = "DOC.V2.V1_SEMANTIC_INCOMPATIBLE"
)

// CapabilityRequirements is the canonical, backend-independent semantic requirement set for a
// complete v2 document. Track/asset kinds deliberately use v2 types; offered v1 backend kinds are
// compared by their stable wire strings so existing backends reject new kinds without guessing.
type CapabilityRequirements struct {
	TrackKinds []TrackKind              `json:"track_kinds,omitempty"`
	AssetKinds []AssetKind              `json:"asset_kinds,omitempty"`
	Features   []videoedit.CapabilityID `json:"features,omitempty"`
}

// RequiredCapabilities derives all rendering requirements from a semantically valid document.
// It never mutates the document, and all set-like outputs are sorted and unique. Invalid documents
// return no usable requirements: callers must not negotiate capabilities around validation errors.
func RequiredCapabilities(document Document) (CapabilityRequirements, []Diagnostic) {
	diagnostics := Validate(document)
	if HasErrorDiagnostics(diagnostics) {
		return CapabilityRequirements{}, diagnostics
	}

	trackKinds := make(map[TrackKind]struct{})
	assetKinds := make(map[AssetKind]struct{})
	features := make(map[videoedit.CapabilityID]struct{})
	assets := make(map[ID]Asset, len(document.Assets))
	for _, asset := range document.Assets {
		assets[asset.ID] = asset
	}

	addFeature := func(value videoedit.CapabilityID) {
		if value != "" {
			features[value] = struct{}{}
		}
	}
	for _, timeline := range document.Timelines {
		transitionClips := make(map[ID]struct {
			track TrackKind
			asset Asset
		})
		for _, track := range timeline.Tracks {
			for _, clip := range track.Clips {
				transitionClips[clip.ID] = struct {
					track TrackKind
					asset Asset
				}{track: track.Kind, asset: assets[clip.AssetID]}
			}
		}
		for _, transition := range timeline.Transitions {
			// A zero-width transition is the schema-level spelling of a hard cut. Keeping it
			// capability-free lets a v1 adapter drop the inert record without changing pixels.
			if !transitionIsHardCut(transition) {
				addFeature(videoedit.CapabilityID(transition.Profile))
				outgoing, outgoingOK := transitionClips[transition.OutgoingClipID]
				incoming, incomingOK := transitionClips[transition.IncomingClipID]
				if outgoingOK && incomingOK && outgoing.track == TrackKindVector && incoming.track == TrackKindVector &&
					vectorTransitionAsset(outgoing.asset) && vectorTransitionAsset(incoming.asset) {
					addFeature(CapabilityVectorTransitionScopeV1)
				}
			}
		}
		for _, track := range timeline.Tracks {
			if track.AudioBusID != "" {
				addFeature(CapabilityAudioRouteV1)
			}
			for _, clip := range track.Clips {
				if clip.Disabled {
					continue
				}
				// Annotation tracks carry non-rendering intent and do not require a renderer feature.
				if track.Kind != TrackKindAnnotation {
					trackKinds[track.Kind] = struct{}{}
				}
				asset, exists := assets[clip.AssetID]
				if exists && track.Kind != TrackKindAnnotation {
					assetKinds[asset.Kind] = struct{}{}
					addAssetCapabilities(asset, assetKinds, addFeature)
				}
				if exists && ((track.Kind == TrackKindVideo && asset.Kind == AssetKindVideo) ||
					(track.Kind == TrackKindAudio && (asset.Kind == AssetKindAudio || asset.Kind == AssetKindVideo))) {
					comparison, err := clip.SourceRange.Duration.Compare(clip.TimelineRange.Duration)
					if err == nil && comparison != 0 {
						if track.Kind == TrackKindAudio {
							addFeature(videoedit.CapabilityAudioVarispeed)
						} else {
							addFeature(videoedit.CapabilityVideoLinearRetime)
						}
					}
				}
				addPropertyCapabilities(clip.PropertySet, addFeature)
				for _, lane := range clip.KeyframeLanes {
					addFeature(CapabilityKeyframesV1)
					addFeature(capabilityForKeyframeTargetAndAsset(lane.Target, asset))
					for _, keyframe := range lane.Keyframes {
						addFeature(capabilityForInterpolation(keyframe.Interpolation))
					}
				}
			}
		}
	}
	if len(document.AudioBuses) != 0 {
		addFeature(CapabilityAudioRouteV1)
		addFeature(CapabilityAudioGainV1)
		addFeature(CapabilityAudioPanV1)
		for _, bus := range document.AudioBuses {
			if bus.Properties.SamplePeakLimiter != nil {
				addFeature(CapabilityAudioSamplePeakLimiterV1)
			}
		}
	}

	return canonicalRequirements(trackKinds, assetKinds, features), diagnostics
}

func addAssetCapabilities(asset Asset, assetKinds map[AssetKind]struct{}, add func(videoedit.CapabilityID)) {
	switch asset.Kind {
	case AssetKindText:
		if asset.Text != nil && asset.Text.Profile == TextProfilePinnedFontViewportPXV2 {
			add(CapabilityTextPinnedFontViewportPXV2)
		} else {
			add(CapabilityTextV1)
		}
		if asset.Text != nil {
			// A pinned font is a material dependency of text shaping even though no timeline clip
			// directly references the font asset.
			assetKinds[AssetKindFont] = struct{}{}
		}
	case AssetKindVector:
		if asset.Vector != nil {
			add(videoedit.CapabilityID(asset.Vector.Profile))
		}
	}
}

func capabilityForKeyframeTargetAndAsset(target KeyframeTarget, asset Asset) videoedit.CapabilityID {
	if target == KeyframeTargetTextFill && asset.Kind == AssetKindText && asset.Text != nil &&
		asset.Text.Profile == TextProfilePinnedFontViewportPXV2 {
		return CapabilityTextPinnedFontViewportPXV2
	}
	return capabilityForKeyframeTarget(target)
}

func addPropertyCapabilities(properties *PropertySet, add func(videoedit.CapabilityID)) {
	if properties == nil {
		return
	}
	if properties.Transform != nil {
		add(CapabilityTransformV1)
	}
	if properties.Opacity != nil {
		add(CapabilityOpacityV1)
	}
	if properties.Crop != nil {
		add(CapabilityCropV1)
	}
	if properties.AudioGain != nil {
		add(CapabilityAudioGainV1)
	}
	if properties.AudioPan != nil {
		add(CapabilityAudioPanV1)
	}
	if properties.AudioChannelMatrix != nil {
		add(CapabilityAudioChannelMatrixV1)
	}
	for _, extension := range properties.Extensions {
		if extension.AffectsRendering {
			// Validate has already required a canonical, versioned token.
			add(videoedit.CapabilityID(extension.RequiredCapability))
		}
	}
}

func capabilityForKeyframeTarget(target KeyframeTarget) videoedit.CapabilityID {
	switch target {
	case KeyframeTargetTransformPosition, KeyframeTargetTransformScale,
		KeyframeTargetTransformRotation, KeyframeTargetTransformAnchor:
		return CapabilityTransformV1
	case KeyframeTargetOpacity:
		return CapabilityOpacityV1
	case KeyframeTargetCrop:
		return CapabilityCropV1
	case KeyframeTargetAudioGain:
		return CapabilityAudioGainV1
	case KeyframeTargetAudioPan:
		return CapabilityAudioPanV1
	case KeyframeTargetTextFill:
		return CapabilityTextV1
	default:
		return ""
	}
}

func capabilityForInterpolation(value Interpolation) videoedit.CapabilityID {
	switch value {
	case InterpolationHold:
		return CapabilityInterpolationHoldV1
	case InterpolationLinear:
		return CapabilityInterpolationLinearV1
	case InterpolationCubic:
		return CapabilityInterpolationCubicV1
	default:
		return ""
	}
}

func canonicalRequirements(
	tracks map[TrackKind]struct{},
	assets map[AssetKind]struct{},
	features map[videoedit.CapabilityID]struct{},
) CapabilityRequirements {
	result := CapabilityRequirements{
		TrackKinds: make([]TrackKind, 0, len(tracks)),
		AssetKinds: make([]AssetKind, 0, len(assets)),
		Features:   make([]videoedit.CapabilityID, 0, len(features)),
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

// CheckCapabilities compares a v2 requirement set with an offered backend declaration. It is a
// schema-gate preflight helper; v2 semantics are intentionally not injected into RenderPlan v3.
func CheckCapabilities(required CapabilityRequirements, offered videoedit.BackendCapabilities) []Diagnostic {
	offered = videoedit.CanonicalBackendCapabilities(offered)
	tracks := make(map[string]struct{}, len(offered.TrackKinds))
	assets := make(map[string]struct{}, len(offered.AssetKinds))
	features := make(map[videoedit.CapabilityID]struct{}, len(offered.Features))
	for _, value := range offered.TrackKinds {
		tracks[string(value)] = struct{}{}
	}
	for _, value := range offered.AssetKinds {
		assets[string(value)] = struct{}{}
	}
	for _, value := range offered.Features {
		features[value] = struct{}{}
	}
	var diagnostics []Diagnostic
	for index, value := range required.TrackKinds {
		if _, exists := tracks[string(value)]; !exists {
			diagnostics = append(diagnostics, capabilityDiagnostic(
				CodeCapabilityTrack, fmt.Sprintf("/required_capabilities/track_kinds/%d", index),
				fmt.Sprintf("backend %q does not support %s tracks", offered.BackendID, value),
			))
		}
	}
	for index, value := range required.AssetKinds {
		if _, exists := assets[string(value)]; !exists {
			diagnostics = append(diagnostics, capabilityDiagnostic(
				CodeCapabilityAsset, fmt.Sprintf("/required_capabilities/asset_kinds/%d", index),
				fmt.Sprintf("backend %q does not support %s assets", offered.BackendID, value),
			))
		}
	}
	for index, value := range required.Features {
		if _, exists := features[value]; !exists {
			diagnostics = append(diagnostics, capabilityDiagnostic(
				CodeCapabilityFeature, fmt.Sprintf("/required_capabilities/features/%d", index),
				fmt.Sprintf("backend %q does not support required feature %q", offered.BackendID, value),
			))
		}
	}
	return diagnostics
}

// PreflightCapabilities preserves authored provenance that a bare requirement set cannot carry.
// Known schema features use the generic requirement diagnostics; an unrecognized rendering
// extension gets a dedicated error at its document path when the selected backend has no declared
// consumer. Disabled clips remain non-rendering and therefore do not participate.
func PreflightCapabilities(document Document, offered videoedit.BackendCapabilities) []Diagnostic {
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) {
		return diagnostics
	}
	offered = videoedit.CanonicalBackendCapabilities(offered)
	offeredFeatures := make(map[videoedit.CapabilityID]struct{}, len(offered.Features))
	for _, feature := range offered.Features {
		offeredFeatures[feature] = struct{}{}
	}

	extensionCapabilities := make(map[videoedit.CapabilityID]struct{})
	var extensionDiagnostics []Diagnostic
	for timelineIndex, timeline := range document.Timelines {
		for trackIndex, track := range timeline.Tracks {
			for clipIndex, clip := range track.Clips {
				if clip.Disabled || clip.PropertySet == nil {
					continue
				}
				for extensionIndex, extension := range clip.PropertySet.Extensions {
					if !extension.AffectsRendering {
						continue
					}
					capability := videoedit.CapabilityID(extension.RequiredCapability)
					extensionCapabilities[capability] = struct{}{}
					if _, supported := offeredFeatures[capability]; supported {
						continue
					}
					extensionDiagnostics = append(extensionDiagnostics, Diagnostic{
						Code: CodeExtensionNeedsConsumer, Severity: SeverityError,
						Path:    fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/property_set/extensions/%d/required_capability", timelineIndex, trackIndex, clipIndex, extensionIndex),
						Message: fmt.Sprintf("backend %q has no declared consumer for rendering extension %q (capability %q)", offered.BackendID, extension.Namespace, capability),
					})
				}
			}
		}
	}

	for _, diagnostic := range CheckCapabilities(required, offered) {
		if diagnostic.Code == CodeCapabilityFeature {
			var featureIndex int
			if _, err := fmt.Sscanf(diagnostic.Path, "/required_capabilities/features/%d", &featureIndex); err == nil &&
				featureIndex >= 0 && featureIndex < len(required.Features) {
				if _, isExtension := extensionCapabilities[required.Features[featureIndex]]; isExtension {
					continue // The document-path diagnostic below is strictly more actionable.
				}
			}
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	diagnostics = append(diagnostics, extensionDiagnostics...)
	return diagnostics
}

func capabilityDiagnostic(code, path, message string) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message}
}

// V1CompatibilityDiagnostics is the explicit gate for a future v2-to-RenderPlan-v3 adapter. A
// caller may down-convert only documents that use no v2 rendering semantics. This keeps M2 fields
// out of the v3 fingerprint rather than silently dropping them.
func V1CompatibilityDiagnostics(document Document) []Diagnostic {
	diagnostics := Validate(document)
	add := func(path, message string) {
		diagnostics = append(diagnostics, Diagnostic{
			Code: CodeV1SemanticIncompatible, Severity: SeverityError, Path: path, Message: message,
		})
	}
	for assetIndex, asset := range document.Assets {
		if asset.Kind == AssetKindText || asset.Kind == AssetKindVector || asset.Text != nil || asset.Vector != nil {
			add(fmt.Sprintf("/assets/%d", assetIndex), "text/vector assets require the v2 evaluator")
		}
	}
	if len(document.AudioBuses) != 0 {
		add("/audio_buses", "audio bus routing requires the v2 evaluator")
	}
	for timelineIndex, timeline := range document.Timelines {
		for transitionIndex, transition := range timeline.Transitions {
			if !transitionIsHardCut(transition) {
				add(fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), "non-zero transitions require the v2 evaluator")
			}
		}
		for trackIndex, track := range timeline.Tracks {
			trackPath := fmt.Sprintf("/timelines/%d/tracks/%d", timelineIndex, trackIndex)
			if track.Kind != TrackKindVideo && track.Kind != TrackKindAudio {
				add(trackPath+"/kind", fmt.Sprintf("%s tracks are not representable in Document v1", track.Kind))
			}
			if track.AudioBusID != "" {
				add(trackPath+"/audio_bus_id", "explicit audio routing requires the v2 evaluator")
			}
			for clipIndex, clip := range track.Clips {
				clipPath := fmt.Sprintf("%s/clips/%d", trackPath, clipIndex)
				if !propertySetEmpty(clip.PropertySet) {
					add(clipPath+"/property_set", "typed properties are not representable in Document v1")
				}
				if len(clip.KeyframeLanes) != 0 {
					add(clipPath+"/keyframe_lanes", "keyframe lanes require the v2 evaluator")
				}
			}
		}
	}
	return diagnostics
}

func IsV1Compatible(document Document) bool {
	return !HasErrorDiagnostics(V1CompatibilityDiagnostics(document))
}

func propertySetEmpty(value *PropertySet) bool {
	return value == nil || value.Transform == nil && value.Opacity == nil && value.Crop == nil &&
		value.AudioGain == nil && value.AudioPan == nil && value.AudioChannelMatrix == nil && len(value.Extensions) == 0
}

func transitionIsHardCut(value Transition) bool {
	return value.PreDuration.Value == 0 && value.PostDuration.Value == 0
}
