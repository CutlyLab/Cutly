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
}

type Program struct {
	evaluator             *evaluator.Program
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
}

func Capabilities(config Config) (videoedit.BackendCapabilities, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	result := videoedit.BackendCapabilities{
		BackendID: BackendID, BuildFingerprint: normalized.BuildFingerprint,
		MaxWidth: normalized.Width, MaxHeight: normalized.Height,
	}
	visual := normalized.Source.Video || normalized.Source.Image || normalized.Source.VectorExpression
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
	if visual {
		result.Features = append(result.Features,
			documentv2.CapabilityTransformV1,
			documentv2.CapabilityCropV1,
			documentv2.CapabilityOpacityV1,
			documentv2.CapabilityKeyframesV1,
			documentv2.CapabilityInterpolationHoldV1,
			documentv2.CapabilityInterpolationLinearV1,
			documentv2.CapabilityInterpolationCubicV1,
			videoedit.CapabilityID(documentv2.TransitionDissolveV1),
		)
	}
	if normalized.Source.LinearVideoRetime {
		result.Features = append(result.Features, videoedit.CapabilityVideoLinearRetime)
	}
	return videoedit.CanonicalBackendCapabilities(result), nil
}

// Compile freezes the complete document and every dispatch binding. Any diagnostic with error
// severity returns a nil Program; cancellation likewise never publishes partial state.
func Compile(ctx context.Context, document documentv2.Document, config Config) (*Program, []documentv2.Diagnostic) {
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
	compiled, snapshot, diagnostics := evaluator.CompileSnapshot(
		ctx, document, capabilities, evaluator.SnapshotLimits{
			MaxCanonicalDocumentBytes: normalized.Limits.MaxCanonicalDocumentBytes,
			MaxDocumentNodes:          uint64(normalized.Limits.MaxDocumentNodes),
		},
	)
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
		assets[asset.ID] = entry
	}
	timelines := make(map[documentv2.ID]int, len(frozenTimelines))
	clips := make(map[clipScopeKey]frozenClip)
	clipsByID := make(map[documentv2.ID]clipScopeKey)
	needsNative, needsExpression := false, false
	for timelineIndex, timeline := range frozenTimelines {
		if timelineIndex&255 == 0 {
			if err := contextError(ctx, "compile/clip_scopes"); err != nil {
				return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeCanceled, "/timelines", err))
			}
		}
		if timeline.Index != timelineIndex {
			return nil, append(diagnostics, compileDiagnostic(DiagnosticCodeInvariant, fmt.Sprintf("/timelines/%d/index", timelineIndex), ErrInvariantFailed))
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
				}
			}
		}
	}
	transitions := make(map[transitionScopeKey]frozenTransition)
	needsExpressionHandle := false
	for timelineIndex, timeline := range frozenTimelines {
		for transitionIndex, transition := range timeline.Transitions {
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
				incomingClipID: transition.IncomingClipID,
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
		evaluator: compiled, documentHash: documentHash, config: normalized, capabilities: capabilities,
		assets: assets, clips: clips, timelines: timelines, transitions: transitions,
		needsNative: needsNative, needsExpression: needsExpression,
		needsExpressionHandle: needsExpressionHandle,
	}, diagnostics
}

func compileDiagnostic(code, path string, err error) documentv2.Diagnostic {
	return documentv2.Diagnostic{Code: code, Severity: documentv2.SeverityError, Path: path, Message: err.Error()}
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
