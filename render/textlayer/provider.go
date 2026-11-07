package textlayer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textraster"
)

type clipScopeKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type frozenAsset struct {
	id                      documentv2.ID
	index                   int
	kind                    documentv2.AssetKind
	textProfile             string
	bindingHash             string
	fill                    documentv2.ColorRGBA16
	viewport                documentv2.RasterViewport
	fontAssetID             documentv2.ID
	fontAssetIndex          int
	fontContentHash         string
	textSemanticHashProfile string
	textSemanticHash        string
	layoutInputHashProfile  string
	layoutInputHash         string
}

type allowedClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	trackKind     documentv2.TrackKind
	asset         frozenAsset
	timelineRange documentv2.TimeRange
	sourceRange   documentv2.TimeRange
}

type fontRequirement struct {
	index       int
	contentHash string
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
	assets       map[documentv2.ID]assetEntry
	receipts     []AssetReceipt
	surfaceBytes uint64
}

var _ TextLayerProvider = (*Provider)(nil)

// Compile borrows document and every ShapeProvider only until return. Callers must not mutate
// document concurrently. Success retains no provider or intermediate text trust root.
func Compile(ctx context.Context, document documentv2.Document, bindings []FontBinding, config Config) (
	result *Provider,
	err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			err = providerError(CodeInvariantFailed, "compile/panic", ErrInvariantFailed)
		}
		if err != nil {
			result = nil
		}
	}()
	if err = contextFailure(ctx, "compile/context"); err != nil {
		return nil, err
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if len(bindings) > normalized.Limits.MaxBindings {
		return nil, providerError(CodeResourceLimit, "compile/bindings", ErrResourceLimit)
	}
	for timelineIndex, timeline := range document.Timelines {
		if timelineIndex&255 == 0 {
			if err := contextFailure(ctx, "compile/tracks"); err != nil {
				return nil, err
			}
		}
		if len(timeline.Tracks) > normalized.Limits.MaxTracksPerTimeline {
			return nil, providerError(CodeResourceLimit, fmt.Sprintf("compile/timelines/%d/tracks", timelineIndex), ErrResourceLimit)
		}
	}
	freezeCanonicalLimit := min64(normalized.Limits.MaxCanonicalDocumentBytes, normalized.Limits.MaxCompileWorkingBytes)
	snapshot, diagnostics := evaluator.FreezeSnapshot(ctx, document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: freezeCanonicalLimit,
		MaxDocumentNodes:          uint64(normalized.Limits.MaxDocumentNodes),
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		if canceled := contextFailure(ctx, "compile/freeze"); canceled != nil {
			return nil, canceled
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Code == string(evaluator.CodeCompileInvalid) {
				return nil, providerError(CodeResourceLimit, "compile/document", ErrResourceLimit)
			}
		}
		return nil, providerError(CodeDocumentInvalid, "compile/document", ErrDocumentInvalid)
	}
	documentHash := snapshot.DocumentHash()
	if !canonicalSHA256(documentHash) || snapshot.CanonicalBytes() == 0 ||
		snapshot.CanonicalBytes() > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, providerError(CodeInvariantFailed, "compile/snapshot", ErrInvariantFailed)
	}

	snapshotAssets := snapshot.Assets()
	assets := make(map[documentv2.ID]frozenAsset, len(snapshotAssets))
	for index, asset := range snapshotAssets {
		if index&255 == 0 {
			if err := contextFailure(ctx, "compile/assets"); err != nil {
				return nil, err
			}
		}
		if asset.Index != index {
			return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/assets/%d/index", index), ErrInvariantFailed)
		}
		assets[asset.ID] = frozenAsset{
			id: asset.ID, index: index, kind: asset.Kind, textProfile: asset.TextProfile,
			bindingHash: asset.TextBindingHash, fill: asset.TextFill, viewport: asset.TextViewport,
			fontAssetID: asset.TextFontAssetID, fontAssetIndex: asset.TextFontAssetIndex,
			fontContentHash:         asset.TextFontContentHash,
			textSemanticHashProfile: asset.TextSemanticHashProfile, textSemanticHash: asset.TextSemanticHash,
			layoutInputHashProfile: asset.TextLayoutInputHashProfile, layoutInputHash: asset.TextLayoutInputHash,
		}
	}

	clips := make(map[clipScopeKey]allowedClip)
	clipKinds := make(map[documentv2.ID]documentv2.TrackKind)
	requiredAssets := make(map[documentv2.ID]frozenAsset)
	requiredFonts := make(map[documentv2.ID]fontRequirement)
	snapshotTimelines := snapshot.Timelines()
	for timelineIndex, timeline := range snapshotTimelines {
		if timelineIndex&255 == 0 {
			if err := contextFailure(ctx, "compile/timelines"); err != nil {
				return nil, err
			}
		}
		if timeline.Index != timelineIndex || timelineIndex >= len(document.Timelines) ||
			document.Timelines[timelineIndex].ID != timeline.ID {
			return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/timelines/%d", timelineIndex), ErrInvariantFailed)
		}
		for trackIndex, track := range timeline.Tracks {
			if trackIndex&255 == 0 {
				if err := contextFailure(ctx, "compile/tracks"); err != nil {
					return nil, err
				}
			}
			if track.Index != trackIndex || trackIndex >= len(document.Timelines[timelineIndex].Tracks) ||
				document.Timelines[timelineIndex].Tracks[trackIndex].ID != track.ID {
				return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/timelines/%d/tracks/%d", timelineIndex, trackIndex), ErrInvariantFailed)
			}
			for clipIndex, clip := range track.Clips {
				if clipIndex&255 == 0 {
					if err := contextFailure(ctx, "compile/clips"); err != nil {
						return nil, err
					}
				}
				if clip.Index != clipIndex || clipIndex >= len(document.Timelines[timelineIndex].Tracks[trackIndex].Clips) {
					return nil, providerError(CodeInvariantFailed, "compile/clip/index", ErrInvariantFailed)
				}
				original := document.Timelines[timelineIndex].Tracks[trackIndex].Clips[clipIndex]
				if original.ID != clip.ID || original.AssetID != clip.AssetID || original.TimelineRange != clip.TimelineRange ||
					original.SourceRange != clip.SourceRange || original.Disabled != clip.Disabled {
					return nil, providerError(CodeInvariantFailed, "compile/clip/snapshot", ErrInvariantFailed)
				}
				clipKinds[clip.ID] = track.Kind
				if track.Kind != documentv2.TrackKindText || clip.Disabled {
					continue
				}
				asset, exists := assets[clip.AssetID]
				path := fmt.Sprintf("compile/timelines/%d/tracks/%d/clips/%d", timelineIndex, trackIndex, clipIndex)
				if !exists || asset.kind != documentv2.AssetKindText {
					return nil, providerError(CodeDocumentInvalid, path+"/asset_id", ErrDocumentInvalid)
				}
				if asset.textProfile != documentv2.TextProfilePinnedFontViewportPXV2 {
					return nil, providerError(CodeProfileUnsupported, fmt.Sprintf("compile/assets/%d/text/profile", asset.index), ErrProfileUnsupported)
				}
				if clip.HasTextFillLane {
					return nil, providerError(CodeProfileUnsupported, path+"/keyframe_lanes/text.fill", ErrProfileUnsupported)
				}
				if hasRenderingExtension(original) {
					return nil, providerError(CodeProfileUnsupported, path+"/property_set/extensions", ErrProfileUnsupported)
				}
				if len(clips) >= normalized.Limits.MaxClipScopes {
					return nil, providerError(CodeResourceLimit, "compile/enabled_text_clips", ErrResourceLimit)
				}
				key := clipScopeKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}
				clips[key] = allowedClip{
					timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex,
					trackKind: track.Kind, asset: asset, timelineRange: clip.TimelineRange, sourceRange: clip.SourceRange,
				}
				if _, exists := requiredAssets[asset.id]; !exists {
					if len(requiredAssets) >= normalized.Limits.MaxTextAssets {
						return nil, providerError(CodeResourceLimit, "compile/enabled_text_assets", ErrResourceLimit)
					}
					if asset.textSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
						asset.layoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
						!canonicalSHA256(asset.bindingHash) || !canonicalSHA256(asset.textSemanticHash) ||
						!canonicalSHA256(asset.layoutInputHash) || !canonicalSHA256(asset.fontContentHash) ||
						asset.viewport.WidthPixels == 0 || asset.viewport.HeightPixels == 0 || asset.fontAssetIndex < 0 {
						return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/assets/%d/text", asset.index), ErrInvariantFailed)
					}
					requiredAssets[asset.id] = asset
				}
				requirement := fontRequirement{index: asset.fontAssetIndex, contentHash: asset.fontContentHash}
				if prior, exists := requiredFonts[asset.fontAssetID]; exists {
					if prior != requirement {
						return nil, providerError(CodeInvariantFailed, path+"/font_asset_id", ErrInvariantFailed)
					}
				} else if len(requiredFonts) >= normalized.Limits.MaxFontAssets {
					return nil, providerError(CodeResourceLimit, "compile/enabled_font_assets", ErrResourceLimit)
				}
				requiredFonts[asset.fontAssetID] = requirement
			}
		}
		for transitionIndex, transition := range timeline.Transitions {
			if transitionIndex&255 == 0 {
				if err := contextFailure(ctx, "compile/transitions"); err != nil {
					return nil, err
				}
			}
			if transition.PreDuration.Value == 0 && transition.PostDuration.Value == 0 {
				continue
			}
			if clipKinds[transition.OutgoingClipID] == documentv2.TrackKindText ||
				clipKinds[transition.IncomingClipID] == documentv2.TrackKindText {
				return nil, providerError(CodeProfileUnsupported, fmt.Sprintf("compile/timelines/%d/transitions/%d", timelineIndex, transitionIndex), ErrProfileUnsupported)
			}
		}
	}
	var totalSurfaceBytes, surfaceVisits uint64
	for _, asset := range requiredAssets {
		surfaceVisits++
		if surfaceVisits&255 == 0 {
			if err := contextFailure(ctx, "compile/surfaces"); err != nil {
				return nil, err
			}
		}
		bytes, ok := surfaceByteCount(asset.viewport.WidthPixels, asset.viewport.HeightPixels)
		if !ok {
			return nil, providerError(CodeResourceLimit, "compile/surfaces", ErrResourceLimit)
		}
		totalSurfaceBytes, ok = checkedAdd(totalSurfaceBytes, bytes)
		if !ok || totalSurfaceBytes > normalized.Limits.MaxTotalStaticSurfaceBytes {
			return nil, providerError(CodeResourceLimit, "compile/surfaces", ErrResourceLimit)
		}
	}
	baseWorking, ok := checkedAdd(snapshot.CanonicalBytes(), totalSurfaceBytes)
	if !ok || baseWorking > normalized.Limits.MaxCompileWorkingBytes {
		return nil, providerError(CodeResourceLimit, "compile/working", ErrResourceLimit)
	}
	stageBudget := normalized.Limits.MaxCompileWorkingBytes - baseWorking
	stageLimits, ok := capStageLimits(normalized.Limits, stageBudget)
	if !ok {
		return nil, providerError(CodeResourceLimit, "compile/working/stage", ErrResourceLimit)
	}

	providers, err := authorizeBindings(ctx, documentHash, bindings, requiredFonts, normalized.Limits)
	if err != nil {
		return nil, err
	}
	assetIDs := make([]documentv2.ID, 0, len(requiredAssets))
	for assetID := range requiredAssets {
		assetIDs = append(assetIDs, assetID)
	}
	sort.Slice(assetIDs, func(left, right int) bool { return assetIDs[left] < assetIDs[right] })
	entries := make(map[documentv2.ID]assetEntry, len(assetIDs))
	receipts := make([]AssetReceipt, 0, len(assetIDs))
	var retained uint64
	for _, assetID := range assetIDs {
		if err := contextFailure(ctx, "compile/assets/bind"); err != nil {
			return nil, err
		}
		asset := requiredAssets[assetID]
		provider := providers[asset.fontAssetID]
		frozen, frozenReceipt, bindErr := snapshot.BindTextSource(ctx, asset.id, asset.index, stageLimits.FrozenText)
		if bindErr != nil {
			return nil, mapFrozenError(ctx, fmt.Sprintf("compile/assets/%d/frozen", asset.index), bindErr)
		}
		if frozen == nil || frozenReceipt.DocumentHash != documentHash || frozenReceipt.AssetID != asset.id ||
			frozenReceipt.AssetIndex != uint64(asset.index) || frozenReceipt.BindingHash != asset.bindingHash ||
			frozenReceipt.FontAssetID != asset.fontAssetID || frozenReceipt.FontAssetIndex != uint64(asset.fontAssetIndex) ||
			frozenReceipt.FontContentHash != asset.fontContentHash || frozenReceipt.TextSemanticHash != asset.textSemanticHash ||
			frozenReceipt.LayoutInputHash != asset.layoutInputHash {
			return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/assets/%d/frozen/receipt", asset.index), ErrInvariantFailed)
		}
		source, assembly, assemblyErr := textassembly.Assemble(ctx, textassembly.Request{Text: frozen, Provider: provider}, stageLimits.Assembly)
		if assemblyErr != nil {
			return nil, mapAssemblyError(ctx, fmt.Sprintf("compile/assets/%d/assembly", asset.index), assemblyErr)
		}
		if source == nil || source.Receipt() != assembly || assembly.DocumentHash != documentHash ||
			assembly.SchemaVersion != textassembly.SchemaVersion || assembly.Profile != textassembly.ReceiptProfileV1 ||
			assembly.DescriptorHashProfile != textassembly.DescriptorHashProfileV1 || assembly.DescriptorHash != textassembly.DescriptorHash() ||
			assembly.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || !canonicalSHA256(assembly.AssemblyHash) ||
			assembly.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
			assembly.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 ||
			assembly.CapabilityStatus != textassembly.CapabilityStatusV1 ||
			assembly.FrozenTextBindingHash != asset.bindingHash || assembly.TextAssetID != string(asset.id) ||
			assembly.FontAssetID != string(asset.fontAssetID) || assembly.FontContentHash != asset.fontContentHash ||
			assembly.TextSemanticHash != asset.textSemanticHash || assembly.LayoutInputHash != asset.layoutInputHash ||
			assembly.WidthPixels != asset.viewport.WidthPixels || assembly.HeightPixels != asset.viewport.HeightPixels ||
			assembly.FillR != asset.fill.R || assembly.FillG != asset.fill.G || assembly.FillB != asset.fill.B || assembly.FillA != asset.fill.A {
			return nil, providerError(CodeInvariantFailed, fmt.Sprintf("compile/assets/%d/assembly/receipt", asset.index), ErrInvariantFailed)
		}
		if assembly.RetainedBytes >= stageBudget {
			return nil, providerError(CodeResourceLimit, "compile/working/raster", ErrResourceLimit)
		}
		rasterLimits := stageLimits.Raster
		rasterWorkingBudget := stageBudget - assembly.RetainedBytes
		rasterLimits.MaxWorkingBytes = min64(rasterLimits.MaxWorkingBytes, rasterWorkingBudget)
		rasterLimits.Raster.MaxWorkingBytes = min64(rasterLimits.Raster.MaxWorkingBytes, rasterWorkingBudget)
		surface, raster, rasterErr := textassembly.RasterizeStatic(ctx, source, rasterLimits)
		if rasterErr != nil {
			return nil, mapRasterError(ctx, fmt.Sprintf("compile/assets/%d/raster", asset.index), rasterErr)
		}
		identity, surfaceBytes, validateErr := validateSurface(ctx, asset, assembly, surface, raster)
		if validateErr != nil {
			return nil, validateErr
		}
		retained, ok = checkedAdd(retained, surfaceBytes)
		if !ok || retained > normalized.Limits.MaxTotalStaticSurfaceBytes {
			return nil, providerError(CodeResourceLimit, "compile/surfaces/retained", ErrResourceLimit)
		}
		rasterPeak, peakOK := checkedAdd(assembly.RetainedBytes, raster.WorkingBytes)
		if !peakOK {
			return nil, providerError(CodeResourceLimit, "compile/working/raster", ErrResourceLimit)
		}
		stageWorking := max64(frozenReceipt.WorkingBytes, max64(assembly.WorkingBytes, rasterPeak))
		working, workingOK := sum64(snapshot.CanonicalBytes(), retained, stageWorking)
		if !workingOK || working > normalized.Limits.MaxCompileWorkingBytes {
			return nil, providerError(CodeResourceLimit, "compile/working", ErrResourceLimit)
		}
		receipt := AssetReceipt{
			AssetID: asset.id, AssetIndex: asset.index, AssetKind: asset.kind,
			FrozenTextBindingHashProfile: evaluator.FrozenTextSourceBindingHashProfileV1,
			FrozenTextBindingHash:        asset.bindingHash,
			TextSemanticHashProfile:      asset.textSemanticHashProfile, TextSemanticHash: asset.textSemanticHash,
			LayoutInputHashProfile: asset.layoutInputHashProfile, LayoutInputHash: asset.layoutInputHash,
			FontAssetID: asset.fontAssetID, FontAssetIndex: asset.fontAssetIndex,
			FontContentHashProfile: sfntglyf.ContentHashProfileV1, FontContentHash: asset.fontContentHash,
			RasterViewport: asset.viewport, SurfaceBytes: surfaceBytes,
			Assembly: assembly, Raster: raster, SurfaceIdentity: identity,
		}
		entries[asset.id] = assetEntry{surface: surface, receipt: receipt}
		receipts = append(receipts, receipt)
		// frozen, source and provider are intentionally iteration-local. Only surface and scalar
		// receipts enter the published Provider.
	}
	if retained != totalSurfaceBytes {
		return nil, providerError(CodeInvariantFailed, "compile/surfaces/retained", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, "compile/publish"); err != nil {
		return nil, err
	}
	return &Provider{
		documentHash: documentHash, config: normalized, clips: clips, assets: entries,
		receipts: receipts, surfaceBytes: retained,
	}, nil
}

func capStageLimits(value Limits, budget uint64) (Limits, bool) {
	if budget < 64<<10 {
		return Limits{}, false
	}
	value.FrozenText.MaxWorkingBytes = min64(value.FrozenText.MaxWorkingBytes, budget)
	value.Assembly.MaxWorkingBytes = min64(value.Assembly.MaxWorkingBytes, budget)
	value.Assembly.Shape.MaxWorkingBytes = min64(value.Assembly.Shape.MaxWorkingBytes, budget)
	value.Assembly.LayoutSource.MaxWorkingBytes = min64(value.Assembly.LayoutSource.MaxWorkingBytes, budget)
	value.Assembly.Layout.MaxWorkingBytes = min64(value.Assembly.Layout.MaxWorkingBytes, budget)
	value.Raster.MaxWorkingBytes = min64(value.Raster.MaxWorkingBytes, budget)
	value.Raster.Raster.MaxWorkingBytes = min64(value.Raster.Raster.MaxWorkingBytes, budget)
	return value, true
}

func authorizeBindings(
	ctx context.Context,
	documentHash string,
	bindings []FontBinding,
	required map[documentv2.ID]fontRequirement,
	limits Limits,
) (map[documentv2.ID]*sfntglyf.ShapeProvider, error) {
	if len(bindings) > limits.MaxBindings {
		return nil, providerError(CodeResourceLimit, "compile/bindings", ErrResourceLimit)
	}
	result := make(map[documentv2.ID]*sfntglyf.ShapeProvider, len(bindings))
	for index, binding := range bindings {
		if index&255 == 0 {
			if err := contextFailure(ctx, "compile/bindings"); err != nil {
				return nil, err
			}
		}
		path := fmt.Sprintf("compile/bindings/%d", index)
		if binding.DocumentHash != documentHash || !canonicalSHA256(binding.DocumentHash) ||
			binding.FontAssetID == "" || binding.Provider == nil {
			return nil, providerError(CodeBindingInvalid, path, ErrBindingInvalid)
		}
		requirement, requiredBinding := required[binding.FontAssetID]
		if !requiredBinding {
			return nil, providerError(CodeBindingInvalid, path+"/font_asset_id", ErrBindingInvalid)
		}
		if _, duplicate := result[binding.FontAssetID]; duplicate {
			return nil, providerError(CodeBindingInvalid, path+"/font_asset_id", ErrBindingInvalid)
		}
		receipt := binding.Provider.Receipt()
		if receipt.SchemaVersion != sfntglyf.ShapeProviderSchemaVersion ||
			receipt.Profile != sfntglyf.ShapeProviderReceiptProfileV1 ||
			receipt.ProviderProfile != sfntglyf.ShapeProviderProfileV1 ||
			receipt.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
			receipt.FontContentHash != requirement.contentHash || !canonicalSHA256(receipt.FontContentHash) {
			return nil, providerError(CodeBindingInvalid, path+"/provider", ErrBindingInvalid)
		}
		result[binding.FontAssetID] = binding.Provider
	}
	if len(result) != len(required) {
		return nil, providerError(CodeBindingInvalid, "compile/bindings/missing", ErrBindingInvalid)
	}
	return result, nil
}

func hasRenderingExtension(clip documentv2.Clip) bool {
	if clip.PropertySet == nil {
		return false
	}
	for _, extension := range clip.PropertySet.Extensions {
		if extension.AffectsRendering {
			return true
		}
	}
	return false
}

func validateSurface(
	ctx context.Context,
	asset frozenAsset,
	assembly textassembly.Receipt,
	surface *cpucompositor.Surface,
	raster textassembly.RasterReceipt,
) (SurfaceIdentity, uint64, error) {
	path := fmt.Sprintf("compile/assets/%d/surface", asset.index)
	if surface == nil || raster.SchemaVersion != textassembly.SchemaVersion ||
		raster.Profile != textassembly.RasterReceiptProfileV1 ||
		raster.DescriptorHashProfile != textassembly.DescriptorHashProfileV1 ||
		raster.DescriptorHash != textassembly.DescriptorHash() ||
		raster.BindingHashProfile != textassembly.RasterBindingHashProfileV1 || !canonicalSHA256(raster.BindingHash) ||
		raster.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || raster.AssemblyHash != assembly.AssemblyHash ||
		raster.InnerReceiptProfile != textraster.ReceiptProfileV1 || raster.InnerRasterProfile != textraster.ProfileV1 ||
		raster.InnerBindingHashProfile != textraster.BindingHashProfileV1 || !canonicalSHA256(raster.InnerBindingHash) ||
		raster.InnerShapingStatus != textraster.ShapingProvenanceStatusV1 ||
		raster.ShapingProvenanceStatus != textassembly.ShapingProvenanceStatusV1 ||
		raster.PaintProvenanceStatus != textassembly.PaintProvenanceStatusV1 ||
		raster.CoverageHashProfile != textraster.CoverageHashProfileV1 || !canonicalSHA256(raster.CoverageHash) ||
		raster.WidthPixels != asset.viewport.WidthPixels || raster.HeightPixels != asset.viewport.HeightPixels ||
		surface.Width() != int(asset.viewport.WidthPixels) || surface.Height() != int(asset.viewport.HeightPixels) ||
		raster.PixelHashProfile != SurfaceHashProfileV1 || !canonicalSHA256(raster.PixelHash) ||
		raster.CanonicalBinaryBytes == 0 || raster.WorkUnits == 0 || raster.WorkingBytes == 0 || raster.RetainedBytes == 0 {
		return SurfaceIdentity{}, 0, providerError(CodeInvariantFailed, path, ErrInvariantFailed)
	}
	if err := contextFailure(ctx, path+"/hash"); err != nil {
		return SurfaceIdentity{}, 0, err
	}
	pixelHash, err := surface.CanonicalHashContext(ctx)
	if err != nil {
		if canceled := contextFailure(ctx, path+"/hash"); canceled != nil {
			return SurfaceIdentity{}, 0, canceled
		}
		return SurfaceIdentity{}, 0, providerError(CodeSurfaceFailed, path+"/hash", errors.Join(ErrSurfaceFailed, err))
	}
	if err := contextFailure(ctx, path+"/hash"); err != nil {
		return SurfaceIdentity{}, 0, err
	}
	if pixelHash != raster.PixelHash || !canonicalSHA256(pixelHash) {
		return SurfaceIdentity{}, 0, providerError(CodeInvariantFailed, path+"/hash", ErrInvariantFailed)
	}
	bytes, ok := surfaceByteCount(asset.viewport.WidthPixels, asset.viewport.HeightPixels)
	if !ok {
		return SurfaceIdentity{}, 0, providerError(CodeResourceLimit, path+"/bytes", ErrResourceLimit)
	}
	return SurfaceIdentity{
		WidthPixels: asset.viewport.WidthPixels, HeightPixels: asset.viewport.HeightPixels,
		PixelHashProfile: SurfaceHashProfileV1, PixelHash: pixelHash,
		SurfaceProfile: cpucompositor.SurfaceProfileV1,
	}, bytes, nil
}

func (provider *Provider) Frame(ctx context.Context, request Request) (result Frame, err error) {
	defer func() {
		if recover() != nil {
			result = Frame{}
			err = providerError(CodeInvariantFailed, "frame/panic", ErrInvariantFailed)
		}
		if err != nil {
			result = Frame{}
		}
	}()
	if provider == nil {
		return Frame{}, providerError(CodeProviderInvalid, "provider", ErrProviderInvalid)
	}
	if err := contextFailure(ctx, "frame/request"); err != nil {
		return Frame{}, err
	}
	allowed, err := provider.validateRequest(request)
	if err != nil {
		return Frame{}, err
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.surface == nil || entry.receipt.AssetID != allowed.asset.id {
		return Frame{}, providerError(CodeAssetUnavailable, "frame/request/asset_id", ErrAssetUnavailable)
	}
	if err := contextFailure(ctx, "frame/publish"); err != nil {
		return Frame{}, err
	}
	receipt := FrameReceipt{
		ContractVersion: ContractVersion, Request: request, Assembly: entry.receipt.Assembly,
		Raster: entry.receipt.Raster, SurfaceIdentity: entry.receipt.SurfaceIdentity,
	}
	return Frame{Receipt: receipt, Surface: entry.surface}, nil
}

func (provider *Provider) validateRequest(request Request) (allowedClip, error) {
	if request.ContractVersion != ContractVersion || request.DocumentHash != provider.documentHash {
		return allowedClip{}, providerError(CodeRequestMismatch, "frame/request/document", ErrRequestMismatch)
	}
	if request.TimelineIndex < 0 || request.TrackIndex < 0 || request.ClipIndex < 0 || request.AssetIndex < 0 ||
		request.FontAssetIndex < 0 || request.TrackKind != documentv2.TrackKindText ||
		request.AssetKind != documentv2.AssetKindText {
		return allowedClip{}, providerError(CodeRequestInvalid, "frame/request/scope", ErrRequestInvalid)
	}
	if request.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
		request.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		request.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
		request.FontContentHashProfile != sfntglyf.ContentHashProfileV1 ||
		!canonicalSHA256(request.FrozenTextBindingHash) || !canonicalSHA256(request.TextSemanticHash) ||
		!canonicalSHA256(request.LayoutInputHash) || !canonicalSHA256(request.FontContentHash) ||
		request.RasterViewport.WidthPixels == 0 || request.RasterViewport.HeightPixels == 0 {
		return allowedClip{}, providerError(CodeRequestInvalid, "frame/request/identity", ErrRequestInvalid)
	}
	if request.SourceProfile != SourceProfileV1 || request.SelectionProfile != SelectionProfileV1 ||
		request.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		request.EvaluatorProfile != documentv2.EvaluatorTextPinnedFontViewportPXV2 ||
		request.RasterProfile != documentv2.TextRasterProfileSupersample8x8V1 ||
		request.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		request.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return allowedClip{}, providerError(CodeRequestMismatch, "frame/request/profiles", ErrRequestMismatch)
	}
	if request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "frame/request/times", ErrRequestInvalid)
	}
	allowed, exists := provider.clips[clipScopeKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists {
		return allowedClip{}, providerError(CodeRequestMismatch, "frame/request/scope", ErrRequestMismatch)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists {
		return allowedClip{}, providerError(CodeAssetUnavailable, "frame/request/asset_id", ErrAssetUnavailable)
	}
	asset := allowed.asset
	if request.TimelineIndex != allowed.timelineIndex || request.TrackIndex != allowed.trackIndex ||
		request.ClipIndex != allowed.clipIndex || request.TrackKind != allowed.trackKind ||
		request.AssetID != asset.id || request.AssetIndex != asset.index || request.AssetKind != asset.kind ||
		request.FrozenTextBindingHash != asset.bindingHash || request.TextSemanticHash != asset.textSemanticHash ||
		request.LayoutInputHash != asset.layoutInputHash || request.FontAssetID != asset.fontAssetID ||
		request.FontAssetIndex != asset.fontAssetIndex || request.FontContentHash != asset.fontContentHash ||
		request.RasterViewport != asset.viewport || entry.receipt.SurfaceIdentity.WidthPixels != asset.viewport.WidthPixels ||
		entry.receipt.SurfaceIdentity.HeightPixels != asset.viewport.HeightPixels {
		return allowedClip{}, providerError(CodeRequestMismatch, "frame/request/scope", ErrRequestMismatch)
	}
	end, err := allowed.timelineRange.End()
	if err != nil {
		return allowedClip{}, providerError(CodeInvariantFailed, "frame/request/timeline_time", ErrInvariantFailed)
	}
	fromStart, err := request.TimelineTime.Compare(allowed.timelineRange.Start)
	if err != nil {
		return allowedClip{}, providerError(CodeRequestInvalid, "frame/request/timeline_time", ErrRequestInvalid)
	}
	beforeEnd, err := request.TimelineTime.Compare(end)
	if err != nil || fromStart < 0 || beforeEnd >= 0 {
		return allowedClip{}, providerError(CodeRequestInvalid, "frame/request/timeline_time", ErrRequestInvalid)
	}
	clip := documentv2.Clip{TimelineRange: allowed.timelineRange, SourceRange: allowed.sourceRange}
	expectedLocal, err := evaluator.ClipLocalTime(clip, request.TimelineTime)
	if err != nil {
		return allowedClip{}, providerError(CodeInvariantFailed, "frame/request/local_time", ErrInvariantFailed)
	}
	expectedSource, err := evaluator.SourceTime(clip, request.TimelineTime)
	if err != nil {
		return allowedClip{}, providerError(CodeInvariantFailed, "frame/request/source_time", ErrInvariantFailed)
	}
	if !equivalentTime(request.LocalTime, expectedLocal) || !equivalentTime(request.SourceTime, expectedSource) {
		return allowedClip{}, providerError(CodeRequestMismatch, "frame/request/times", ErrRequestMismatch)
	}
	return allowed, nil
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
		DocumentHash: provider.documentHash, SourceProfile: SourceProfileV1, SelectionProfile: SelectionProfileV1,
		TextProfile:      documentv2.TextProfilePinnedFontViewportPXV2,
		EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
		RasterProfile:    documentv2.TextRasterProfileSupersample8x8V1,
		PaintProfile:     color.ProfileRGBA16SRGBPaintV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		PixelHashProfile: SurfaceHashProfileV1, RetainedSurfaceBytes: provider.surfaceBytes,
		Assets: append([]AssetReceipt(nil), provider.receipts...),
	}
}

func mapFrozenError(ctx context.Context, path string, err error) error {
	if canceled := contextFailure(ctx, path); canceled != nil {
		return canceled
	}
	switch {
	case errors.Is(err, evaluator.ErrFrozenTextSourceCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return providerError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	case errors.Is(err, evaluator.ErrFrozenTextSourceResourceLimit):
		return providerError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	case errors.Is(err, evaluator.ErrFrozenTextSourceLimitsInvalid):
		return providerError(CodeConfigInvalid, path, errors.Join(ErrConfigInvalid, err))
	default:
		return providerError(CodeBindingInvalid, path, errors.Join(ErrBindingInvalid, err))
	}
}

func mapAssemblyError(ctx context.Context, path string, err error) error {
	if canceled := contextFailure(ctx, path); canceled != nil {
		return canceled
	}
	switch {
	case errors.Is(err, textassembly.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return providerError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	case errors.Is(err, textassembly.ErrResourceLimit):
		return providerError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	case errors.Is(err, textassembly.ErrLimitsInvalid):
		return providerError(CodeConfigInvalid, path, errors.Join(ErrConfigInvalid, err))
	case errors.Is(err, textassembly.ErrBinding), errors.Is(err, textassembly.ErrInputInvalid):
		return providerError(CodeBindingInvalid, path, errors.Join(ErrBindingInvalid, err))
	default:
		return providerError(CodeAssemblyFailed, path, errors.Join(ErrAssemblyFailed, err))
	}
}

func mapRasterError(ctx context.Context, path string, err error) error {
	mapped := mapAssemblyError(ctx, path, err)
	var typed *Error
	if errors.As(mapped, &typed) && typed.Code == CodeAssemblyFailed {
		return providerError(CodeRasterFailed, path, errors.Join(ErrRasterFailed, err))
	}
	return mapped
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

func checkedAdd(left, right uint64) (uint64, bool) {
	if right > ^uint64(0)-left {
		return 0, false
	}
	return left + right, true
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > ^uint64(0)/left {
		return 0, false
	}
	return left * right, true
}

func sum64(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		var ok bool
		total, ok = checkedAdd(total, value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func max64(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}

func min64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func surfaceByteCount(width, height uint32) (uint64, bool) {
	pixels, ok := checkedMul(uint64(width), uint64(height))
	if !ok {
		return 0, false
	}
	return checkedMul(pixels, cpucompositor.BytesPerPixel)
}

func equivalentTime(left, right documentv2.RationalTime) bool {
	comparison, err := left.Compare(right)
	return err == nil && comparison == 0
}
