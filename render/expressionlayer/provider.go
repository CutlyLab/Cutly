package expressionlayer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

type clipScopeKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type frozenAsset struct {
	index  int
	id     documentv2.ID
	kind   documentv2.AssetKind
	vector documentv2.VectorSource
}

type allowedClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	trackKind     documentv2.TrackKind
	asset         frozenAsset
	clip          documentv2.Clip
}

type transitionScopeKey struct {
	timeline   documentv2.ID
	transition documentv2.ID
	role       TransitionHandleRole
}

type allowedTransitionHandle struct {
	timelineIndex   int
	trackID         documentv2.ID
	trackIndex      int
	transitionIndex int
	transition      documentv2.Transition
	role            TransitionHandleRole
	clipKey         clipScopeKey
}

type assetEntry struct {
	surface *cpucompositor.Surface
	receipt AssetReceipt
}

// Provider is immutable after Compile and safe for concurrent Frame and Manifest calls.
type Provider struct {
	documentHash string
	config       Config
	clips        map[clipScopeKey]allowedClip
	transitions  map[transitionScopeKey]allowedTransitionHandle
	assets       map[documentv2.ID]assetEntry
	receipts     []AssetReceipt
}

var _ ExpressionLayerProvider = (*Provider)(nil)

// Compile borrows the complete caller Document graph until return. Callers must not mutate it
// concurrently. Successful compilation retains only a canonical deep-frozen decode, immutable
// raster Surfaces and value-only receipts; failure never returns a partial Provider.
func Compile(ctx context.Context, document documentv2.Document, config Config) (*Provider, error) {
	if ctx == nil {
		return nil, providerError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, err
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := preflightDocument(ctx, document, normalized.Limits); err != nil {
		return nil, err
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, providerError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if uint64(len(canonical)) > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, providerError(CodeResourceLimit, "document/canonical_bytes", ErrResourceLimit)
	}
	documentHash, err := encodedSHA256(ctx, canonical, "document/hash")
	if err != nil {
		return nil, err
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, providerError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	canonical = nil

	assetsByID := make(map[documentv2.ID]frozenAsset, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "document/assets"); err != nil {
				return nil, err
			}
		}
		entry := frozenAsset{index: index, id: asset.ID, kind: asset.Kind}
		if asset.Vector != nil {
			entry.vector = *asset.Vector
		}
		assetsByID[asset.ID] = entry
	}

	required := make(map[documentv2.ID]frozenAsset)
	clips := make(map[clipScopeKey]allowedClip)
	// Keep the transition lookup compact. The authoritative allowedClip value lives in clips;
	// this secondary index retains only its stable key rather than copying Clip and VectorSource
	// slice headers for every enabled clip.
	clipsByID := make(map[documentv2.ID]clipScopeKey)
	transitions := make(map[transitionScopeKey]allowedTransitionHandle)
	maxTransitionHandleScopes := normalized.Limits.MaxClipScopes * 2
	var totalPaths, totalCommands, totalEdges, retainedBytes uint64
	var visits uint64
	for timelineIndex, timeline := range frozen.Timelines {
		if err := periodicContext(ctx, &visits, "document/clip_scopes"); err != nil {
			return nil, err
		}
		for trackIndex, track := range timeline.Tracks {
			if err := periodicContext(ctx, &visits, "document/clip_scopes"); err != nil {
				return nil, err
			}
			if track.Kind != documentv2.TrackKindVector {
				continue
			}
			for clipIndex, clip := range track.Clips {
				if err := periodicContext(ctx, &visits, "document/clip_scopes"); err != nil {
					return nil, err
				}
				if clip.Disabled {
					continue
				}
				asset, exists := assetsByID[clip.AssetID]
				path := fmt.Sprintf("document/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex)
				if !exists || asset.kind != documentv2.AssetKindVector {
					return nil, providerError(CodeDocumentInvalid, path+"/asset_id", ErrDocumentInvalid)
				}
				if asset.vector.Profile != documentv2.VectorProfilePathViewportPXV2 {
					return nil, providerError(CodeProfileUnsupported, fmt.Sprintf("document/assets/%d/vector/profile", asset.index), ErrProfileUnsupported)
				}
				if asset.vector.Viewport == nil {
					return nil, providerError(CodeDocumentInvalid, fmt.Sprintf("document/assets/%d/vector/viewport", asset.index), ErrDocumentInvalid)
				}
				if len(clips) >= normalized.Limits.MaxClipScopes {
					return nil, providerError(CodeResourceLimit, "document/enabled_vector_clip_scopes", ErrResourceLimit)
				}
				key := clipScopeKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}
				allowed := allowedClip{
					timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex,
					trackKind: track.Kind, asset: asset, clip: clip,
				}
				clips[key] = allowed
				clipsByID[clip.ID] = key
				if _, exists := required[asset.id]; exists {
					continue
				}
				if len(required) >= normalized.Limits.MaxExpressionAssets {
					return nil, providerError(CodeResourceLimit, "document/enabled_vector_assets", ErrResourceLimit)
				}
				paths, commands, edges, measureErr := expressionCounts(ctx, asset.vector, fmt.Sprintf("document/assets/%d/vector", asset.index))
				if measureErr != nil {
					return nil, measureErr
				}
				if totalPaths, err = addWithin(totalPaths, paths, normalized.Limits.MaxTotalPaths, "document/enabled_vector_paths"); err != nil {
					return nil, err
				}
				if totalCommands, err = addWithin(totalCommands, commands, normalized.Limits.MaxTotalCommands, "document/enabled_vector_commands"); err != nil {
					return nil, err
				}
				if totalEdges, err = addWithin(totalEdges, edges, normalized.Limits.MaxTotalEdges, "document/enabled_vector_edges"); err != nil {
					return nil, err
				}
				viewportBytes, ok := checkedSurfaceBytes(asset.vector.Viewport.WidthPixels, asset.vector.Viewport.HeightPixels)
				if !ok {
					return nil, providerError(CodeResourceLimit, fmt.Sprintf("document/assets/%d/vector/viewport", asset.index), ErrResourceLimit)
				}
				if retainedBytes, err = addWithin(retainedBytes, viewportBytes, normalized.Limits.MaxRetainedBytes, "document/enabled_vector_retained_bytes"); err != nil {
					return nil, err
				}
				required[asset.id] = asset
			}
		}
		for transitionIndex, transition := range timeline.Transitions {
			if err := periodicContext(ctx, &visits, "document/transition_handle_scopes"); err != nil {
				return nil, err
			}
			// A zero-width transition is an inert hard cut and has no active handle interval.
			// Do not retain unreachable provider scopes for it.
			if transition.PreDuration.Value == 0 && transition.PostDuration.Value == 0 {
				continue
			}
			outgoingKey, outgoingOK := clipsByID[transition.OutgoingClipID]
			incomingKey, incomingOK := clipsByID[transition.IncomingClipID]
			outgoing := clips[outgoingKey]
			incoming := clips[incomingKey]
			if !outgoingOK || !incomingOK || outgoing.trackKind != documentv2.TrackKindVector ||
				incoming.trackKind != documentv2.TrackKindVector || outgoing.trackIndex != incoming.trackIndex {
				continue
			}
			if len(transitions) > maxTransitionHandleScopes-2 {
				return nil, providerError(CodeResourceLimit, "document/enabled_vector_transition_handle_scopes", ErrResourceLimit)
			}
			transitions[transitionScopeKey{timeline: timeline.ID, transition: transition.ID, role: TransitionHandleOutgoing}] = allowedTransitionHandle{
				timelineIndex: timelineIndex, trackID: timeline.Tracks[outgoing.trackIndex].ID,
				trackIndex: outgoing.trackIndex, transitionIndex: transitionIndex,
				transition: transition, role: TransitionHandleOutgoing, clipKey: outgoingKey,
			}
			transitions[transitionScopeKey{timeline: timeline.ID, transition: transition.ID, role: TransitionHandleIncoming}] = allowedTransitionHandle{
				timelineIndex: timelineIndex, trackID: timeline.Tracks[incoming.trackIndex].ID,
				trackIndex: incoming.trackIndex, transitionIndex: transitionIndex,
				transition: transition, role: TransitionHandleIncoming, clipKey: incomingKey,
			}
		}
	}

	assetIDs := make([]documentv2.ID, 0, len(required))
	for assetID := range required {
		assetIDs = append(assetIDs, assetID)
	}
	sort.Slice(assetIDs, func(left, right int) bool { return assetIDs[left] < assetIDs[right] })
	entries := make(map[documentv2.ID]assetEntry, len(assetIDs))
	receipts := make([]AssetReceipt, 0, len(assetIDs))
	var actualRetained uint64
	for _, assetID := range assetIDs {
		if err := contextFailure(ctx, "raster/assets"); err != nil {
			return nil, err
		}
		asset := required[assetID]
		path := fmt.Sprintf("document/assets/%d/vector", asset.index)
		surface, rasterReceipt, rasterErr := vectorraster.Rasterize(ctx, asset.vector, normalized.Limits.Vector)
		if rasterErr != nil {
			if canceled := contextFailure(ctx, path+"/raster"); canceled != nil {
				return nil, canceled
			}
			switch {
			case errors.Is(rasterErr, vectorraster.ErrCanceled), errors.Is(rasterErr, context.Canceled), errors.Is(rasterErr, context.DeadlineExceeded):
				return nil, providerError(CodeCanceled, path+"/raster", errors.Join(ErrCanceled, rasterErr))
			case errors.Is(rasterErr, vectorraster.ErrResourceLimit), errors.Is(rasterErr, vectorraster.ErrExactLimit):
				return nil, providerError(CodeResourceLimit, path+"/raster", errors.Join(ErrResourceLimit, rasterErr))
			case errors.Is(rasterErr, vectorraster.ErrProfileUnsupported):
				return nil, providerError(CodeProfileUnsupported, path+"/profile", errors.Join(ErrProfileUnsupported, rasterErr))
			default:
				return nil, providerError(CodeRasterFailed, path+"/raster", errors.Join(ErrRasterFailed, rasterErr))
			}
		}
		receipt, receiptErr := validateRasterResult(ctx, asset, surface, rasterReceipt, path)
		if receiptErr != nil {
			return nil, receiptErr
		}
		var ok bool
		actualRetained, ok = checkedAdd(actualRetained, receipt.SurfaceBytes)
		if !ok || actualRetained > normalized.Limits.MaxRetainedBytes {
			return nil, providerError(CodeResourceLimit, "raster/retained_bytes", ErrResourceLimit)
		}
		entries[assetID] = assetEntry{surface: surface, receipt: receipt}
		receipts = append(receipts, receipt)
	}
	if actualRetained != retainedBytes {
		return nil, providerError(CodeInvariantFailed, "raster/retained_bytes", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return nil, err
	}
	return &Provider{
		documentHash: documentHash, config: normalized, clips: clips, transitions: transitions,
		assets: entries, receipts: receipts,
	}, nil
}

func validateRasterResult(
	ctx context.Context,
	asset frozenAsset,
	surface *cpucompositor.Surface,
	rasterReceipt vectorraster.Receipt,
	path string,
) (AssetReceipt, error) {
	if surface == nil || asset.vector.Viewport == nil {
		return AssetReceipt{}, providerError(CodeInvariantFailed, path+"/surface", ErrInvariantFailed)
	}
	viewport := *asset.vector.Viewport
	canonicalSource, canonicalErr := videoedit.CanonicalJSON(asset.vector)
	if canonicalErr != nil {
		return AssetReceipt{}, providerError(CodeHashFailed, path+"/semantic_hash", errors.Join(ErrHashFailed, canonicalErr))
	}
	semanticHash, hashErr := encodedSHA256(ctx, canonicalSource, path+"/semantic_hash")
	if hashErr != nil {
		return AssetReceipt{}, hashErr
	}
	paths, commands, conceptualEdges, countErr := expressionCounts(ctx, asset.vector, path)
	if countErr != nil {
		return AssetReceipt{}, providerError(CodeInvariantFailed, path+"/counts", errors.Join(ErrInvariantFailed, countErr))
	}
	if rasterReceipt.SchemaVersion != vectorraster.ReceiptSchemaVersion ||
		rasterReceipt.Profile != vectorraster.ReceiptProfileV1 ||
		rasterReceipt.RasterProfile != vectorraster.ProfileV1 ||
		rasterReceipt.SourceProfile != documentv2.VectorProfilePathViewportPXV2 ||
		rasterReceipt.SourceHashProfile != vectorraster.SourceHashProfileV1 ||
		!canonicalSHA256(rasterReceipt.SourceSemanticHash) || rasterReceipt.SourceSemanticHash != semanticHash ||
		rasterReceipt.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		rasterReceipt.KernelProfile != cpucompositor.KernelProfileV1 ||
		rasterReceipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		rasterReceipt.WidthPixels != viewport.WidthPixels || rasterReceipt.HeightPixels != viewport.HeightPixels ||
		rasterReceipt.PathCount != paths || rasterReceipt.CommandCount != commands ||
		rasterReceipt.ConceptualEdgeCount != conceptualEdges ||
		rasterReceipt.CanonicalSourceBytes != uint64(len(canonicalSource)) ||
		surface.Width() != int(viewport.WidthPixels) || surface.Height() != int(viewport.HeightPixels) {
		return AssetReceipt{}, providerError(CodeInvariantFailed, path+"/raster_receipt", ErrInvariantFailed)
	}
	surfaceBytes, ok := checkedSurfaceBytes(viewport.WidthPixels, viewport.HeightPixels)
	if !ok || rasterReceipt.RetainedBytes != surfaceBytes {
		return AssetReceipt{}, providerError(CodeInvariantFailed, path+"/retained_bytes", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, path+"/surface_hash"); err != nil {
		return AssetReceipt{}, err
	}
	pixelHash, err := surface.CanonicalHash()
	if err != nil {
		return AssetReceipt{}, providerError(CodeSurfaceFailed, path+"/surface_hash", errors.Join(ErrSurfaceFailed, err))
	}
	if err := contextFailure(ctx, path+"/surface_hash"); err != nil {
		return AssetReceipt{}, err
	}
	if pixelHash != rasterReceipt.PixelHash || !canonicalSHA256(pixelHash) {
		return AssetReceipt{}, providerError(CodeInvariantFailed, path+"/pixel_hash", ErrInvariantFailed)
	}
	identity := SurfaceIdentity{
		WidthPixels: viewport.WidthPixels, HeightPixels: viewport.HeightPixels,
		PixelHashProfile: SurfaceHashProfileV1, PixelHash: pixelHash,
		SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}
	return AssetReceipt{
		AssetID: asset.id, AssetIndex: asset.index, AssetKind: asset.kind,
		AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		AssetSemanticHash:        rasterReceipt.SourceSemanticHash, RasterViewport: viewport,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		SourceProfile:     SourceProfileV1, SelectionProfile: SelectionProfileV1,
		RasterProfile: vectorraster.ProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		PathCount: rasterReceipt.PathCount, CommandCount: rasterReceipt.CommandCount,
		ConceptualEdgeCount: rasterReceipt.ConceptualEdgeCount, EffectiveEdgeCount: rasterReceipt.EffectiveEdgeCount,
		SurfaceBytes: surfaceBytes, SurfaceIdentity: identity, RasterReceipt: rasterReceipt,
	}, nil
}

func (provider *Provider) Frame(ctx context.Context, request Request) (Frame, error) {
	if provider == nil {
		return Frame{}, providerError(CodeProviderInvalid, "provider", ErrProviderInvalid)
	}
	if ctx == nil {
		return Frame{}, providerError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return Frame{}, err
	}
	allowed, err := provider.validateRequest(request)
	if err != nil {
		return Frame{}, err
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.surface == nil || entry.receipt.AssetID != allowed.asset.id {
		return Frame{}, providerError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if err := contextFailure(ctx, "response"); err != nil {
		return Frame{}, err
	}
	return Frame{
		ContractVersion: ContractVersion, Request: request,
		AssetSemanticHashProfile: entry.receipt.AssetSemanticHashProfile,
		AssetSemanticHash:        entry.receipt.AssetSemanticHash,
		RasterReceipt:            entry.receipt.RasterReceipt,
		SurfaceIdentity:          entry.receipt.SurfaceIdentity,
		Surface:                  entry.surface,
	}, nil
}

func (provider *Provider) validateRequest(request Request) (allowedClip, error) {
	if request.ContractVersion != ContractVersion || request.DocumentHash != provider.documentHash {
		return allowedClip{}, providerError(CodeRequestMismatch, "request/document", ErrRequestMismatch)
	}
	if request.TimelineIndex < 0 || request.TrackIndex < 0 || request.ClipIndex < 0 || request.AssetIndex < 0 ||
		request.TrackKind != documentv2.TrackKindVector || request.AssetKind != documentv2.AssetKindVector {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/scope", ErrRequestInvalid)
	}
	if request.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 || !canonicalSHA256(request.AssetSemanticHash) ||
		request.RasterViewport.WidthPixels == 0 || request.RasterViewport.HeightPixels == 0 {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/expression", ErrRequestInvalid)
	}
	if request.SourceProfile != SourceProfileV1 || request.SelectionProfile != SelectionProfileV1 ||
		request.ExpressionProfile != documentv2.VectorProfilePathViewportPXV2 ||
		request.EvaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 ||
		request.RasterProfile != vectorraster.ProfileV1 || request.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		request.KernelProfile != cpucompositor.KernelProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return allowedClip{}, providerError(CodeRequestMismatch, "request/profiles", ErrRequestMismatch)
	}
	if request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/times", ErrRequestInvalid)
	}
	allowed, exists := provider.clips[clipScopeKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists {
		return allowedClip{}, providerError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists {
		return allowedClip{}, providerError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if request.TimelineIndex != allowed.timelineIndex || request.TrackIndex != allowed.trackIndex || request.ClipIndex != allowed.clipIndex ||
		request.TrackKind != allowed.trackKind || request.AssetID != allowed.asset.id ||
		request.AssetIndex != allowed.asset.index || request.AssetKind != allowed.asset.kind ||
		request.AssetSemanticHash != entry.receipt.AssetSemanticHash ||
		request.RasterViewport != entry.receipt.RasterViewport {
		return allowedClip{}, providerError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	visibleEnd, err := allowed.clip.TimelineRange.End()
	if err != nil {
		return allowedClip{}, providerError(CodeInvariantFailed, "request/timeline_time", errors.Join(ErrInvariantFailed, err))
	}
	fromStart, err := request.TimelineTime.Compare(allowed.clip.TimelineRange.Start)
	if err != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	beforeEnd, err := request.TimelineTime.Compare(visibleEnd)
	if err != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	if fromStart < 0 || beforeEnd >= 0 {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/timeline_time", ErrRequestInvalid)
	}
	expectedLocal, err := evaluator.ClipLocalTime(allowed.clip, request.TimelineTime)
	if err != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	expectedSource, err := evaluator.SourceTime(allowed.clip, request.TimelineTime)
	if err != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	if !equivalentTime(request.LocalTime, expectedLocal) || !equivalentTime(request.SourceTime, expectedSource) {
		return allowedClip{}, providerError(CodeRequestMismatch, "request/times", ErrRequestMismatch)
	}
	return allowed, nil
}

func expressionCounts(ctx context.Context, source documentv2.VectorSource, path string) (uint64, uint64, uint64, error) {
	paths := uint64(len(source.Paths))
	var commands, edges uint64
	for pathIndex, vectorPath := range source.Paths {
		if pathIndex&255 == 0 {
			if err := contextFailure(ctx, path+"/paths"); err != nil {
				return 0, 0, 0, err
			}
		}
		var ok bool
		commands, ok = checkedAdd(commands, uint64(len(vectorPath.Commands)))
		if !ok {
			return 0, 0, 0, providerError(CodeResourceLimit, path+"/commands", ErrResourceLimit)
		}
		open := false
		for commandIndex, command := range vectorPath.Commands {
			if commandIndex&1023 == 0 {
				if err := contextFailure(ctx, path+"/commands"); err != nil {
					return 0, 0, 0, err
				}
			}
			ok = true
			switch command.Op {
			case documentv2.VectorMoveTo:
				if open {
					edges, ok = checkedAdd(edges, 1)
				}
				open = true
			case documentv2.VectorLineTo:
				edges, ok = checkedAdd(edges, 1)
			case documentv2.VectorClose:
				edges, ok = checkedAdd(edges, 1)
				open = false
			default:
				return 0, 0, 0, providerError(CodeProfileUnsupported, path+"/commands", ErrProfileUnsupported)
			}
			if !ok {
				return 0, 0, 0, providerError(CodeResourceLimit, path+"/edges", ErrResourceLimit)
			}
		}
		if open {
			edges, ok = checkedAdd(edges, 1)
			if !ok {
				return 0, 0, 0, providerError(CodeResourceLimit, path+"/edges", ErrResourceLimit)
			}
		}
	}
	return paths, commands, edges, nil
}

func addWithin(current, delta, limit uint64, path string) (uint64, error) {
	result, ok := checkedAdd(current, delta)
	if !ok || result > limit {
		return 0, providerError(CodeResourceLimit, path, ErrResourceLimit)
	}
	return result, nil
}

func checkedSurfaceBytes(width, height uint32) (uint64, bool) {
	pixels, ok := checkedMul(uint64(width), uint64(height))
	if !ok {
		return 0, false
	}
	return checkedMul(pixels, cpucompositor.BytesPerPixel)
}

func encodedSHA256(ctx context.Context, encoded []byte, path string) (string, error) {
	digest := sha256.New()
	const chunkBytes = 64 << 10
	for offset := 0; offset < len(encoded); offset += chunkBytes {
		if err := contextFailure(ctx, path); err != nil {
			return "", err
		}
		end := offset + chunkBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = digest.Write(encoded[offset:end])
	}
	if err := contextFailure(ctx, path); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return providerError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return providerError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func periodicContext(ctx context.Context, visits *uint64, path string) error {
	if *visits&1023 == 0 {
		if err := contextFailure(ctx, path); err != nil {
			return err
		}
	}
	*visits++
	return nil
}

func equivalentTime(left, right documentv2.RationalTime) bool {
	comparison, err := left.Compare(right)
	return err == nil && comparison == 0
}

func (provider *Provider) DocumentHash() string {
	if provider == nil {
		return ""
	}
	return provider.documentHash
}

func (provider *Provider) Config() Config {
	if provider == nil {
		return Config{}
	}
	return provider.config
}

func (provider *Provider) Manifest() Manifest {
	if provider == nil {
		return Manifest{}
	}
	return Manifest{
		SchemaVersion: ManifestSchemaVersion, Profile: ProfileV1, ContractVersion: ContractVersion,
		DocumentHash: provider.documentHash, SourceProfile: SourceProfileV1,
		SelectionProfile: SelectionProfileV1, AssetSemanticHashProfile: vectorraster.SourceHashProfileV1,
		ExpressionProfile: documentv2.VectorProfilePathViewportPXV2,
		EvaluatorProfile:  documentv2.EvaluatorVectorPathViewportPXV2,
		RasterProfile:     vectorraster.ProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
		KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		PixelHashProfile: SurfaceHashProfileV1, Assets: append([]AssetReceipt(nil), provider.receipts...),
	}
}
