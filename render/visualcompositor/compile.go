package visualcompositor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	BackendID                   = ProfileV1
	DiagnosticCodeConfigInvalid = "COMPOSITOR.VISUAL_REFERENCE_V1.CONFIG_INVALID"
	DiagnosticCodeCanceled      = "COMPOSITOR.VISUAL_REFERENCE_V1.CANCELED"
	DiagnosticCodeDocumentLimit = "COMPOSITOR.VISUAL_REFERENCE_V1.DOCUMENT_LIMIT"
	DiagnosticCodeFreezeInvalid = "COMPOSITOR.VISUAL_REFERENCE_V1.FREEZE_INVALID"
	DiagnosticCodeAssetUnpinned = "COMPOSITOR.VISUAL_REFERENCE_V1.ASSET_UNPINNED"
	DiagnosticCodeInvariant     = "COMPOSITOR.VISUAL_REFERENCE_V1.INVARIANT_FAILED"
	DiagnosticCodeStaticText    = "COMPOSITOR.VISUAL_REFERENCE_V2.STATIC_TEXT_UNSUPPORTED"
)

type frozenAsset struct {
	index                int
	kind                 documentv2.AssetKind
	contentHash          string
	semanticHash         string
	viewport             documentv2.RasterViewport
	canonicalSourceBytes uint64
	pathCount            uint64
	commandCount         uint64
	conceptualEdgeCount  uint64
	textProfile          string
	textBindingHash      string
	textSemanticHash     string
	textLayoutInputHash  string
	textFontAssetID      documentv2.ID
	textFontAssetIndex   int
	textFontContentHash  string
	textFill             documentv2.ColorRGBA16
}

type clipScopeKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type frozenClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	trackKind     documentv2.TrackKind
	assetID       documentv2.ID
}

type transitionScopeKey struct {
	timeline   documentv2.ID
	transition documentv2.ID
}

type frozenTransition struct {
	index          int
	profile        string
	trackID        documentv2.ID
	trackIndex     int
	outgoingClipID documentv2.ID
	incomingClipID documentv2.ID
	trackKind      documentv2.TrackKind
}

type Program struct {
	evaluator             visualEvaluator
	domainScope           *evaluator.DomainScope
	domainV2              bool
	domainScopeHash       string
	documentHash          string
	config                Config
	capabilities          videoedit.BackendCapabilities
	assets                map[documentv2.ID]frozenAsset
	clips                 map[clipScopeKey]frozenClip
	timelines             map[documentv2.ID]int
	transitions           map[transitionScopeKey]frozenTransition
	needsNative           bool
	needsExpression       bool
	needsExpressionHandle bool
	needsText             bool
}

func Capabilities(config Config) (videoedit.BackendCapabilities, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	result := videoedit.BackendCapabilities{
		BackendID: profileForConfig(normalized), BuildFingerprint: normalized.BuildFingerprint,
		MaxWidth: normalized.Width, MaxHeight: normalized.Height,
	}
	visual := normalized.Source.Video || normalized.Source.Image || normalized.Source.VectorExpression || normalized.Source.StaticTextV2
	if normalized.Source.Video || normalized.Source.Image {
		result.TrackKinds = append(result.TrackKinds, videoedit.TrackKind(documentv2.TrackKindVideo))
	}
	if normalized.Source.Video {
		result.AssetKinds = append(result.AssetKinds, videoedit.AssetKind(documentv2.AssetKindVideo))
	}
	if normalized.Source.Image {
		result.AssetKinds = append(result.AssetKinds, videoedit.AssetKind(documentv2.AssetKindImage))
	}
	if normalized.Source.VectorExpression {
		result.TrackKinds = append(result.TrackKinds, videoedit.TrackKind(documentv2.TrackKindVector))
		result.AssetKinds = append(result.AssetKinds, videoedit.AssetKind(documentv2.AssetKindVector))
		result.Features = append(result.Features,
			documentv2.CapabilityVectorPathViewportPXV2,
			documentv2.CapabilityVectorTransitionScopeV1,
		)
	}
	if normalized.Source.StaticTextV2 {
		result.TrackKinds = append(result.TrackKinds, videoedit.TrackKind(documentv2.TrackKindText))
		result.AssetKinds = append(result.AssetKinds,
			videoedit.AssetKind(documentv2.AssetKindText),
			videoedit.AssetKind(documentv2.AssetKindFont),
		)
		result.Features = append(result.Features, documentv2.CapabilityTextPinnedFontViewportPXV2)
	}
	if visual {
		result.Features = append(result.Features,
			documentv2.CapabilityTransformV1,
			documentv2.CapabilityCropV1,
			documentv2.CapabilityOpacityV1,
			documentv2.CapabilityKeyframesV1,
			documentv2.CapabilityInterpolationHoldV1,
			documentv2.CapabilityInterpolationLinearV1,
			documentv2.CapabilityInterpolationCubicV1,
		)
	}
	if normalized.Source.Video || normalized.Source.Image {
		result.Features = append(result.Features,
			videoedit.CapabilityID(documentv2.TransitionDissolveV1),
			videoedit.CapabilityID(documentv2.TransitionFadeV1),
			videoedit.CapabilityID(documentv2.TransitionWipeV1),
		)
	} else if normalized.Source.VectorExpression {
		result.Features = append(result.Features, videoedit.CapabilityID(documentv2.TransitionDissolveV1))
	}
	if normalized.Source.LinearVideoRetime {
		result.Features = append(result.Features, videoedit.CapabilityVideoLinearRetime)
	}
	return videoedit.CanonicalBackendCapabilities(result), nil
}

// Compile freezes the complete document and every dispatch binding. Any diagnostic with error
// severity returns a nil Program; cancellation likewise never publishes partial state.
func Compile(ctx context.Context, document documentv2.Document, config Config) (*Program, []documentv2.Diagnostic) {
	return compile(ctx, document, config, nil)
}

func compile(ctx context.Context, document documentv2.Document, config Config, domain *evaluator.DomainScope) (*Program, []documentv2.Diagnostic) {
	return compileMode(ctx, document, config, domain, false)
}

func compileMode(ctx context.Context, document documentv2.Document, config Config, domain *evaluator.DomainScope, domainV2 bool) (*Program, []documentv2.Diagnostic) {
	if ctx == nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeConfigInvalid, "/visual_compositor/context", ErrInvalidConfig)}
	}
	if err := contextError(ctx, "compile"); err != nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeCanceled, "/visual_compositor", err)}
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeConfigInvalid, "/visual_compositor/config", err)}
	}
	capabilities, err := Capabilities(normalized)
	if err != nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeConfigInvalid, "/visual_compositor/config/source", err)}
	}
	limitPath, limitErr := preflightDocument(ctx, document, normalized.Limits)
	if limitErr != nil {
		if errors.Is(limitErr, ErrCanceled) {
			return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeCanceled, "/visual_compositor", limitErr)}
		}
		return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeDocumentLimit, "/"+strings.TrimPrefix(limitPath, "/"), limitErr)}
	}
	if domain != nil && !domainV2 {
		if err := validateDomainProviderClosure(ctx, document, *domain); err != nil {
			return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeDomainInvalid, "/scope", err)}
		}
	}
	if normalized.Source.StaticTextV2 && !domainV2 {
		if subsetPath, subsetErr := validateStaticTextSubsetMode(ctx, document, domain); subsetErr != nil {
			if errors.Is(subsetErr, ErrCanceled) {
				return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeCanceled, "/visual_compositor", subsetErr)}
			}
			return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeStaticText, "/"+strings.TrimPrefix(subsetPath, "/"), subsetErr)}
		}
	}
	var compiled visualEvaluator
	var snapshot *evaluator.FrozenSnapshot
	var diagnostics []documentv2.Diagnostic
	limits := evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: normalized.Limits.MaxCanonicalDocumentBytes,
		MaxDocumentNodes:          uint64(normalized.Limits.MaxDocumentNodes),
	}
	if domain == nil {
		value, frozen, ds := evaluator.CompileSnapshot(ctx, document, capabilities, limits)
		if value != nil {
			compiled = value
		}
		snapshot, diagnostics = frozen, ds
	} else {
		compiler := evaluator.CompileDomainSnapshot
		if domainV2 {
			compiler = evaluator.CompileDomainSnapshotV2
		}
		value, frozen, ds := compiler(ctx, document, *domain, capabilities, limits)
		if value != nil {
			compiled = value
			owned := value.Scope()
			domain = &owned
		}
		snapshot, diagnostics = frozen, ds
	}
	if compiled == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeCanceled, "/visual_compositor", errors.Join(ErrCanceled, ctx.Err()))}
		}
		return nil, diagnostics
	}
	if snapshot == nil || snapshot.DocumentHash() == "" || snapshot.DocumentHash() != compiled.DocumentHash() ||
		snapshot.CanonicalBytes() > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, "/document_hash", ErrInvariantFailed))
	}
	if domainV2 {
		if err := validateDomainProviderClosure(ctx, document, *domain); err != nil {
			return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeDomainInvalid, "/scope", err))
		}
		if normalized.Source.StaticTextV2 {
			if path, err := validateStaticTextSubsetMode(ctx, document, domain); err != nil {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeStaticText, path, err))
			}
		}
	}
	selected := map[documentv2.ID]bool(nil)
	if domain != nil {
		selected = domainClipSet(*domain)
	}
	domainHash := ""
	if domain != nil {
		var err error
		domainHash, err = hashDomainScope(*domain)
		if err != nil {
			return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeDomainInvalid, "/scope", err))
		}
	}
	documentHash := snapshot.DocumentHash()
	frozenAssets := snapshot.Assets()
	frozenTimelines := snapshot.Timelines()

	assets := make(map[documentv2.ID]frozenAsset, len(frozenAssets))
	for index, asset := range frozenAssets {
		if index&255 == 0 {
			if err := contextError(ctx, "compile/assets"); err != nil {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, "/assets", err))
			}
		}
		if asset.Index != index {
			return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/assets/%d/index", index), ErrInvariantFailed))
		}
		entry := frozenAsset{index: index, kind: asset.Kind, contentHash: asset.ContentHash}
		if asset.Kind == documentv2.AssetKindVector && asset.VectorProfile == documentv2.VectorProfilePathViewportPXV2 {
			if asset.VectorViewport.WidthPixels == 0 || asset.VectorViewport.HeightPixels == 0 {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/assets/%d/vector/viewport", index), ErrInvariantFailed))
			}
			entry.semanticHash = asset.VectorSemanticHash
			entry.viewport = asset.VectorViewport
			entry.canonicalSourceBytes = asset.CanonicalSourceBytes
			entry.pathCount, entry.commandCount, entry.conceptualEdgeCount = asset.PathCount, asset.CommandCount, asset.ConceptualEdgeCount
		}
		if asset.Kind == documentv2.AssetKindText && asset.TextProfile == documentv2.TextProfilePinnedFontViewportPXV2 {
			entry.textProfile = asset.TextProfile
			entry.textBindingHash = asset.TextBindingHash
			entry.textSemanticHash = asset.TextSemanticHash
			entry.textLayoutInputHash = asset.TextLayoutInputHash
			entry.textFontAssetID = asset.TextFontAssetID
			entry.textFontAssetIndex = asset.TextFontAssetIndex
			entry.textFontContentHash = asset.TextFontContentHash
			entry.textFill = asset.TextFill
			entry.viewport = asset.TextViewport
		}
		assets[asset.ID] = entry
	}
	timelines := make(map[documentv2.ID]int, len(frozenTimelines))
	clips := make(map[clipScopeKey]frozenClip)
	clipsByID := make(map[documentv2.ID]clipScopeKey)
	needsNative, needsExpression, needsText := false, false, false
	for timelineIndex, timeline := range frozenTimelines {
		if timelineIndex&255 == 0 {
			if err := contextError(ctx, "compile/clip_scopes"); err != nil {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, "/timelines", err))
			}
		}
		if timeline.Index != timelineIndex {
			return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/index", timelineIndex), ErrInvariantFailed))
		}
		if domain != nil && timeline.ID != domain.Selection.TimelineID {
			continue
		}
		timelines[timeline.ID] = timelineIndex
		for trackIndex, track := range timeline.Tracks {
			if trackIndex&255 == 0 {
				if err := contextError(ctx, "compile/clip_scopes"); err != nil {
					return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, fmt.Sprintf("/timelines/%d/tracks", timelineIndex), err))
				}
			}
			if track.Index != trackIndex {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/tracks/%d/index", timelineIndex, trackIndex), ErrInvariantFailed))
			}
			for clipIndex, clip := range track.Clips {
				if clipIndex&255 == 0 {
					if err := contextError(ctx, "compile/clip_scopes"); err != nil {
						return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, fmt.Sprintf("/timelines/%d/tracks/%d/clips", timelineIndex, trackIndex), err))
					}
				}
				if clip.Index != clipIndex {
					return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/index", timelineIndex, trackIndex, clipIndex), ErrInvariantFailed))
				}
				if domain != nil && !selected[clip.ID] {
					continue
				}
				asset, exists := assets[clip.AssetID]
				if !exists {
					return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/asset_id", timelineIndex, trackIndex, clipIndex), ErrInvariantFailed))
				}
				scope := frozenClip{timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex, trackKind: track.Kind, assetID: clip.AssetID}
				key := clipScopeKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}
				clips[key] = scope
				clipsByID[clip.ID] = key
				if clip.Disabled {
					continue
				}
				switch track.Kind {
				case documentv2.TrackKindVideo:
					if (asset.kind != documentv2.AssetKindVideo && asset.kind != documentv2.AssetKindImage) || !canonicalSHA256(asset.contentHash) {
						return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeAssetUnpinned, fmt.Sprintf("/assets/%d/content_hash", asset.index), ErrSourceInvalid))
					}
					needsNative = true
				case documentv2.TrackKindVector:
					if asset.kind != documentv2.AssetKindVector || !canonicalSHA256(asset.semanticHash) || asset.viewport.WidthPixels == 0 || asset.viewport.HeightPixels == 0 {
						return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/assets/%d/vector", asset.index), ErrInvariantFailed))
					}
					needsExpression = true
				case documentv2.TrackKindText:
					if !normalized.Source.StaticTextV2 || clip.HasTextFillLane || asset.kind != documentv2.AssetKindText ||
						asset.textProfile != documentv2.TextProfilePinnedFontViewportPXV2 || !canonicalSHA256(asset.textBindingHash) ||
						!canonicalSHA256(asset.textSemanticHash) || !canonicalSHA256(asset.textLayoutInputHash) ||
						asset.textFontAssetID == "" || asset.textFontAssetIndex < 0 || !canonicalSHA256(asset.textFontContentHash) ||
						asset.viewport.WidthPixels == 0 || asset.viewport.HeightPixels == 0 {
						return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeStaticText, fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex), ErrUnsupportedLayer))
					}
					needsText = true
				}
			}
		}
	}
	transitions := make(map[transitionScopeKey]frozenTransition)
	selectedTransitions := map[documentv2.ID]bool(nil)
	if domainV2 {
		selectedTransitions = make(map[documentv2.ID]bool, len(domain.Selection.TransitionIDs))
		for _, id := range domain.Selection.TransitionIDs {
			selectedTransitions[id] = true
		}
	}
	needsExpressionHandle := false
	for timelineIndex, timeline := range frozenTimelines {
		if domain != nil && (!domainV2 || timeline.ID != domain.Selection.TimelineID) {
			continue
		}
		for transitionIndex, transition := range timeline.Transitions {
			if domain != nil && !selectedTransitions[transition.ID] {
				continue
			}
			if transitionIndex&255 == 0 {
				if err := contextError(ctx, "compile/transition_scopes"); err != nil {
					return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, fmt.Sprintf("/timelines/%d/transitions", timelineIndex), err))
				}
			}
			outgoingKey, outgoingOK := clipsByID[transition.OutgoingClipID]
			incomingKey, incomingOK := clipsByID[transition.IncomingClipID]
			outgoing := clips[outgoingKey]
			incoming := clips[incomingKey]
			if transition.Index != transitionIndex || !outgoingOK || !incomingOK || outgoingKey.track != incomingKey.track || outgoing.trackIndex != incoming.trackIndex {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), ErrInvariantFailed))
			}
			transitions[transitionScopeKey{timeline: timeline.ID, transition: transition.ID}] = frozenTransition{
				index: transitionIndex, profile: transition.Profile, trackID: outgoingKey.track,
				trackIndex: outgoing.trackIndex, outgoingClipID: transition.OutgoingClipID,
				incomingClipID: transition.IncomingClipID, trackKind: outgoing.trackKind,
			}
			if outgoing.trackKind == documentv2.TrackKindText &&
				(transition.PreDuration.Value != 0 || transition.PostDuration.Value != 0) {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeStaticText, fmt.Sprintf("/timelines/%d/transitions/%d", timelineIndex, transitionIndex), ErrUnsupportedLayer))
			}
			if outgoing.trackKind == documentv2.TrackKindVector &&
				(transition.PreDuration.Value != 0 || transition.PostDuration.Value != 0) {
				needsExpressionHandle = true
			}
		}
	}
	if err := contextError(ctx, "compile/publish"); err != nil {
		return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, "/visual_compositor", err))
	}
	return &Program{
		evaluator: compiled, domainScope: domain, domainV2: domainV2, domainScopeHash: domainHash, documentHash: documentHash, config: normalized, capabilities: capabilities,
		assets: assets, clips: clips, timelines: timelines, transitions: transitions,
		needsNative: needsNative, needsExpression: needsExpression,
		needsExpressionHandle: needsExpressionHandle, needsText: needsText,
	}, diagnostics
}

func compileDiagnostic(code, path string, err error) documentv2.Diagnostic {
	return documentv2.Diagnostic{Code: code, Severity: documentv2.SeverityError, Path: path, Message: err.Error()}
}

// validateStaticTextSubset is intentionally independent of textlayer.Compile. The compositor
// refuses dynamic paint and Text transitions before advertising or freezing its own evaluator
// program, so a permissive or mismatched provider can never broaden this profile.
func validateStaticTextSubset(ctx context.Context, document documentv2.Document) (string, error) {
	return validateStaticTextSubsetMode(ctx, document, nil)
}

func validateStaticTextSubsetMode(ctx context.Context, document documentv2.Document, domain *evaluator.DomainScope) (string, error) {
	if err := contextError(ctx, "compile/static_text/preflight"); err != nil {
		return "", err
	}
	selected := map[documentv2.ID]bool(nil)
	if domain != nil {
		selected = domainClipSet(*domain)
	}
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for assetIndex, asset := range document.Assets {
		if assetIndex&255 == 0 {
			if err := contextError(ctx, "compile/static_text/assets"); err != nil {
				return "", err
			}
		}
		assets[asset.ID] = asset
	}
	for timelineIndex, timeline := range document.Timelines {
		if timelineIndex&255 == 0 {
			if err := contextError(ctx, "compile/static_text/timelines"); err != nil {
				return "", err
			}
		}
		if domain != nil && timeline.ID != domain.Selection.TimelineID {
			continue
		}
		clipKinds := make(map[documentv2.ID]documentv2.TrackKind)
		for trackIndex, track := range timeline.Tracks {
			if trackIndex&255 == 0 {
				if err := contextError(ctx, "compile/static_text/tracks"); err != nil {
					return "", err
				}
			}
			for clipIndex, clip := range track.Clips {
				if clipIndex&255 == 0 {
					if err := contextError(ctx, "compile/static_text/clips"); err != nil {
						return "", err
					}
				}
				if domain != nil && !selected[clip.ID] {
					continue
				}
				clipKinds[clip.ID] = track.Kind
				if track.Kind != documentv2.TrackKindText || clip.Disabled {
					continue
				}
				path := fmt.Sprintf("document/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex)
				asset, exists := assets[clip.AssetID]
				if !exists || asset.Kind != documentv2.AssetKindText || asset.Text == nil ||
					asset.Text.Profile != documentv2.TextProfilePinnedFontViewportPXV2 {
					return path + "/asset_id", ErrUnsupportedLayer
				}
				for laneIndex, lane := range clip.KeyframeLanes {
					if laneIndex&255 == 0 {
						if err := contextError(ctx, "compile/static_text/keyframe_lanes"); err != nil {
							return "", err
						}
					}
					switch lane.Target {
					case documentv2.KeyframeTargetTransformPosition,
						documentv2.KeyframeTargetTransformScale,
						documentv2.KeyframeTargetTransformRotation,
						documentv2.KeyframeTargetTransformAnchor,
						documentv2.KeyframeTargetCrop,
						documentv2.KeyframeTargetOpacity:
						// Static Text accepts the same evaluator-owned visual lanes as other
						// compositor sources. Paint remains frozen in the Text asset.
					default:
						return fmt.Sprintf("%s/keyframe_lanes/%d/target", path, laneIndex), ErrUnsupportedLayer
					}
				}
				if clip.PropertySet != nil {
					if clip.PropertySet.AudioGain != nil || clip.PropertySet.AudioPan != nil || clip.PropertySet.AudioChannelMatrix != nil ||
						len(clip.PropertySet.Extensions) != 0 {
						return path + "/property_set", ErrUnsupportedLayer
					}
				}
			}
		}
		if domain != nil {
			continue
		}
		for transitionIndex, transition := range timeline.Transitions {
			if transitionIndex&255 == 0 {
				if err := contextError(ctx, "compile/static_text/transitions"); err != nil {
					return "", err
				}
			}
			if transition.PreDuration.Value == 0 && transition.PostDuration.Value == 0 {
				continue
			}
			if clipKinds[transition.OutgoingClipID] == documentv2.TrackKindText ||
				clipKinds[transition.IncomingClipID] == documentv2.TrackKindText {
				return fmt.Sprintf("document/timelines/%d/transitions/%d", timelineIndex, transitionIndex), ErrUnsupportedLayer
			}
		}
	}
	return "", contextError(ctx, "compile/static_text/publish")
}

func (program *Program) DocumentHash() string {
	if program == nil {
		return ""
	}
	return program.documentHash
}

func (program *Program) Config() Config {
	if program == nil {
		return Config{}
	}
	return program.config
}

func (program *Program) Capabilities() videoedit.BackendCapabilities {
	if program == nil {
		return videoedit.BackendCapabilities{}
	}
	return videoedit.CanonicalBackendCapabilities(program.capabilities)
}
