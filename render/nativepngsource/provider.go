package nativepngsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

// Binding lends one complete encoded PNG only for Compile. The bytes must remain immutable until
// Compile returns and are never retained.
type Binding struct {
	DocumentHash string        `json:"document_hash"`
	AssetID      documentv2.ID `json:"asset_id"`
	EncodedPNG   []byte        `json:"-"`
}

type AssetReceipt struct {
	AssetID              documentv2.ID `json:"asset_id"`
	AssetIndex           int           `json:"asset_index"`
	EncodedContentHash   string        `json:"encoded_content_hash"`
	PixelHash            string        `json:"pixel_hash"`
	Width                int           `json:"width"`
	Height               int           `json:"height"`
	SRGBRenderingIntent  uint8         `json:"srgb_rendering_intent"`
	DecodeProfile        string        `json:"decode_profile"`
	SelectionProfile     string        `json:"selection_profile"`
	ColorProfile         string        `json:"color_profile"`
	NormalizationProfile string        `json:"normalization_profile"`
	ContentHashProfile   string        `json:"content_hash_profile"`
	SourceProfile        string        `json:"source_profile"`
	SurfaceProfile       string        `json:"surface_profile"`
}

type Manifest struct {
	SchemaVersion         int            `json:"schema_version"`
	Profile               string         `json:"profile"`
	DocumentHash          string         `json:"document_hash"`
	SourceContractVersion int            `json:"source_contract_version"`
	SourceProfile         string         `json:"source_profile"`
	SurfaceProfile        string         `json:"surface_profile"`
	Assets                []AssetReceipt `json:"assets,omitempty"`
}

type clipScopeKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type frozenAsset struct {
	index       int
	id          documentv2.ID
	kind        documentv2.AssetKind
	contentHash string
}

type allowedClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	asset         frozenAsset
	clip          documentv2.Clip
}

type assetEntry struct {
	surface *cpucompositor.Surface
	receipt AssetReceipt
}

type indexedBinding struct {
	index   int
	binding Binding
}

// Provider is immutable after Compile and safe for concurrent Frame and Manifest calls.
type Provider struct {
	documentHash string
	config       Config
	clips        map[clipScopeKey]allowedClip
	assets       map[documentv2.ID]assetEntry
	receipts     []AssetReceipt
}

var _ rastercompositor.NativeSourceProvider = (*Provider)(nil)

// Compile atomically freezes all enabled image request scopes and strict PNG surfaces. It borrows
// the complete Document object graph, Binding slice and every encoded byte slice until return;
// callers must not mutate any of them concurrently. A successful Provider retains no
// caller-owned mutable storage.
func Compile(ctx context.Context, document documentv2.Document, bindings []Binding, config Config) (*Provider, error) {
	if ctx == nil {
		return nil, sourceError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, err
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if len(bindings) > normalized.Limits.MaxBindings {
		return nil, sourceError(CodeResourceLimit, "bindings", ErrResourceLimit)
	}
	var totalEncoded uint64
	for index, binding := range bindings {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "bindings"); err != nil {
				return nil, err
			}
		}
		length := uint64(len(binding.EncodedPNG))
		if length > normalized.Limits.MaxTotalEncodedBytes-totalEncoded {
			return nil, sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/total_encoded_bytes", index), ErrResourceLimit)
		}
		totalEncoded += length
	}
	if err := preflightDocument(ctx, document, normalized.Limits); err != nil {
		return nil, err
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if uint64(len(canonical)) > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, sourceError(CodeResourceLimit, "document/canonical_bytes", ErrResourceLimit)
	}
	if err := contextFailure(ctx, "document"); err != nil {
		return nil, err
	}
	documentHash, err := encodedSHA256(ctx, canonical, "document/hash")
	if err != nil {
		return nil, err
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	// The frozen value was decoded from these already-canonical bytes, so hashing them is exactly
	// DocumentHash(frozen) without a second document-sized canonicalization allocation.
	canonical = nil

	assetsByID := make(map[documentv2.ID]frozenAsset, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "document/assets"); err != nil {
				return nil, err
			}
		}
		assetsByID[asset.ID] = frozenAsset{index: index, id: asset.ID, kind: asset.Kind, contentHash: asset.ContentHash}
	}
	required := make(map[documentv2.ID]struct{})
	clips := make(map[clipScopeKey]allowedClip)
	var scopeVisits uint64
	for timelineIndex, timeline := range frozen.Timelines {
		if scopeVisits&1023 == 0 {
			if err := contextFailure(ctx, "document/clip_scopes"); err != nil {
				return nil, err
			}
		}
		scopeVisits++
		for trackIndex, track := range timeline.Tracks {
			if scopeVisits&1023 == 0 {
				if err := contextFailure(ctx, "document/clip_scopes"); err != nil {
					return nil, err
				}
			}
			scopeVisits++
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for clipIndex, clip := range track.Clips {
				if scopeVisits&1023 == 0 {
					if err := contextFailure(ctx, "document/clip_scopes"); err != nil {
						return nil, err
					}
				}
				scopeVisits++
				if clip.Disabled {
					continue
				}
				asset, exists := assetsByID[clip.AssetID]
				if !exists {
					return nil, sourceError(CodeDocumentInvalid, fmt.Sprintf("document/timelines/%d/tracks/%d/clips/%d/asset_id", timelineIndex, trackIndex, clipIndex), ErrDocumentInvalid)
				}
				if asset.kind != documentv2.AssetKindImage {
					continue
				}
				if !canonicalSHA256(asset.contentHash) {
					return nil, sourceError(CodeDocumentInvalid, fmt.Sprintf("document/assets/%d/content_hash", asset.index), ErrDocumentInvalid)
				}
				if len(clips) >= normalized.Limits.MaxClipScopes {
					return nil, sourceError(CodeResourceLimit, "document/enabled_image_clip_scopes", ErrResourceLimit)
				}
				key := clipScopeKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}
				clips[key] = allowedClip{
					timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex,
					asset: asset, clip: clip,
				}
				if _, exists := required[asset.id]; !exists {
					if len(required) >= normalized.Limits.MaxBindings {
						return nil, sourceError(CodeResourceLimit, "document/referenced_images", ErrResourceLimit)
					}
					required[asset.id] = struct{}{}
				}
			}
		}
	}

	bindingByID := make(map[documentv2.ID]indexedBinding, len(bindings))
	for index, binding := range bindings {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "bindings"); err != nil {
				return nil, err
			}
		}
		path := fmt.Sprintf("bindings/%d", index)
		if binding.DocumentHash != documentHash {
			return nil, sourceError(CodeDocumentMismatch, path+"/document_hash", ErrDocumentMismatch)
		}
		if strings.TrimSpace(string(binding.AssetID)) == "" {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		if _, duplicate := bindingByID[binding.AssetID]; duplicate {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		asset, exists := assetsByID[binding.AssetID]
		if !exists || asset.kind != documentv2.AssetKindImage {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		if _, expected := required[binding.AssetID]; !expected {
			return nil, sourceError(CodeBindingExtra, path, ErrBindingExtra)
		}
		bindingByID[binding.AssetID] = indexedBinding{index: index, binding: binding}
	}

	assetIDs := make([]documentv2.ID, 0, len(required))
	for assetID := range required {
		if _, exists := bindingByID[assetID]; !exists {
			return nil, sourceError(CodeBindingMissing, "bindings", ErrBindingMissing)
		}
		assetIDs = append(assetIDs, assetID)
	}
	sort.Slice(assetIDs, func(left, right int) bool { return assetIDs[left] < assetIDs[right] })

	entries := make(map[documentv2.ID]assetEntry, len(assetIDs))
	receipts := make([]AssetReceipt, 0, len(assetIDs))
	var retainedBytes uint64
	for _, assetID := range assetIDs {
		selected := bindingByID[assetID]
		path := fmt.Sprintf("bindings/%d", selected.index)
		if err := contextFailure(ctx, path); err != nil {
			return nil, err
		}
		encoded := selected.binding.EncodedPNG
		if uint64(len(encoded)) > normalized.Limits.PNG.MaxEncodedBytes {
			return nil, sourceError(CodeResourceLimit, path+"/encoded_bytes", ErrResourceLimit)
		}
		encodedHash, hashErr := encodedSHA256(ctx, encoded, path+"/content_hash")
		if hashErr != nil {
			return nil, hashErr
		}
		asset := assetsByID[assetID]
		if encodedHash != asset.contentHash {
			return nil, sourceError(CodeContentMismatch, path+"/content_hash", ErrContentMismatch)
		}
		decoded, decodeErr := pngdecode.Decode(ctx, encoded, normalized.Limits.PNG)
		if decodeErr != nil {
			switch {
			case errors.Is(decodeErr, pngdecode.ErrCanceled), errors.Is(decodeErr, context.Canceled), errors.Is(decodeErr, context.DeadlineExceeded):
				return nil, sourceError(CodeCanceled, path+"/decode", errors.Join(ErrCanceled, decodeErr))
			case errors.Is(decodeErr, pngdecode.ErrResourceLimit):
				return nil, sourceError(CodeResourceLimit, path+"/decode", errors.Join(ErrResourceLimit, decodeErr))
			default:
				return nil, sourceError(CodeDecodeFailed, path+"/decode", errors.Join(ErrDecodeFailed, decodeErr))
			}
		}
		if decoded.EncodedContentHash != encodedHash || decoded.DecodeProfile != pngdecode.ProfileV1 ||
			decoded.NormalizationProfile != color.ProfileRGBA8SRGBV1 || decoded.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
			decoded.ContentHashProfile != pngdecode.ContentHashProfileV1 || decoded.Surface == nil {
			return nil, sourceError(CodeDecodeFailed, path+"/response", ErrDecodeFailed)
		}
		surfaceBytes, ok := checkedSurfaceBytes(decoded.Width, decoded.Height)
		if !ok || surfaceBytes > normalized.Limits.MaxRetainedBytes-retainedBytes {
			return nil, sourceError(CodeResourceLimit, path+"/retained_surface_bytes", ErrResourceLimit)
		}
		retainedBytes += surfaceBytes
		receipt := AssetReceipt{
			AssetID: assetID, AssetIndex: asset.index,
			EncodedContentHash: decoded.EncodedContentHash, PixelHash: decoded.PixelHash,
			Width: decoded.Width, Height: decoded.Height, SRGBRenderingIntent: decoded.SRGBRenderingIntent,
			DecodeProfile: decoded.DecodeProfile, SelectionProfile: SelectionProfileV1,
			ColorProfile: decoded.NormalizationProfile, NormalizationProfile: NormalizationProfileV1,
			ContentHashProfile: decoded.ContentHashProfile,
			SourceProfile:      rastercompositor.SourceProfileV1, SurfaceProfile: decoded.SurfaceProfile,
		}
		entries[assetID] = assetEntry{surface: decoded.Surface, receipt: receipt}
		receipts = append(receipts, receipt)
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return nil, err
	}
	return &Provider{
		documentHash: documentHash, config: normalized, clips: clips, assets: entries, receipts: receipts,
	}, nil
}

func (provider *Provider) Frame(ctx context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
	if provider == nil {
		return rastercompositor.NativeSourceFrame{}, sourceError(CodeProviderInvalid, "provider", ErrProviderInvalid)
	}
	if ctx == nil {
		return rastercompositor.NativeSourceFrame{}, sourceError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return rastercompositor.NativeSourceFrame{}, err
	}
	if err := provider.validateRequest(request); err != nil {
		return rastercompositor.NativeSourceFrame{}, err
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.surface == nil {
		return rastercompositor.NativeSourceFrame{}, sourceError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if err := contextFailure(ctx, "response"); err != nil {
		return rastercompositor.NativeSourceFrame{}, err
	}
	return rastercompositor.NativeSourceFrame{
		ContractVersion: rastercompositor.NativeSourceContractVersion, Request: request,
		Width: entry.receipt.Width, Height: entry.receipt.Height,
		AssetContentHash: entry.receipt.EncodedContentHash, PixelHash: entry.receipt.PixelHash,
		DecodeProfile: entry.receipt.DecodeProfile, SelectionProfile: entry.receipt.SelectionProfile,
		ColorProfile: entry.receipt.ColorProfile, NormalizationProfile: entry.receipt.NormalizationProfile,
		SourceProfile: entry.receipt.SourceProfile, SurfaceProfile: entry.receipt.SurfaceProfile,
		Surface: entry.surface,
	}, nil
}

func (provider *Provider) validateRequest(request rastercompositor.NativeSourceRequest) error {
	profiles := ImageSourceProfiles()
	if request.ContractVersion != rastercompositor.NativeSourceContractVersion || request.DocumentHash != provider.documentHash {
		return sourceError(CodeRequestMismatch, "request/document", ErrRequestMismatch)
	}
	if request.AssetKind != documentv2.AssetKindImage {
		return sourceError(CodeRequestInvalid, "request/asset_kind", ErrRequestInvalid)
	}
	if request.SourceProfile != rastercompositor.SourceProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		request.DecodeProfile != profiles.DecodeProfile || request.SelectionProfile != profiles.SelectionProfile ||
		request.ColorProfile != profiles.ColorProfile || request.NormalizationProfile != profiles.NormalizationProfile {
		return sourceError(CodeRequestMismatch, "request/profiles", ErrRequestMismatch)
	}
	if request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil {
		return sourceError(CodeRequestInvalid, "request/times", ErrRequestInvalid)
	}
	allowed, exists := provider.clips[clipScopeKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists {
		return sourceError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	if request.TimelineIndex != allowed.timelineIndex || request.TrackIndex != allowed.trackIndex || request.ClipIndex != allowed.clipIndex ||
		request.AssetID != allowed.asset.id || request.AssetIndex != allowed.asset.index ||
		request.AssetContentHash != allowed.asset.contentHash {
		return sourceError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	expectedLocal, err := evaluator.ClipLocalTime(allowed.clip, request.TimelineTime)
	if err != nil {
		return sourceError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	expectedSource, err := evaluator.SourceTime(allowed.clip, request.TimelineTime)
	if err != nil {
		return sourceError(CodeRequestInvalid, "request/timeline_time", errors.Join(ErrRequestInvalid, err))
	}
	if !equivalentTime(request.LocalTime, expectedLocal) || !equivalentTime(request.SourceTime, expectedSource) {
		return sourceError(CodeRequestMismatch, "request/times", ErrRequestMismatch)
	}
	return nil
}

func equivalentTime(left, right documentv2.RationalTime) bool {
	comparison, err := left.Compare(right)
	return err == nil && comparison == 0
}

func checkedSurfaceBytes(width, height int) (uint64, bool) {
	if width <= 0 || height <= 0 {
		return 0, false
	}
	w, h := uint64(width), uint64(height)
	if w > math.MaxUint64/h {
		return 0, false
	}
	pixels := w * h
	if pixels > math.MaxUint64/cpucompositor.BytesPerPixel {
		return 0, false
	}
	return pixels * cpucompositor.BytesPerPixel, true
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
		return sourceError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return sourceError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
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
		SchemaVersion: ManifestSchemaVersion, Profile: ProfileV1, DocumentHash: provider.documentHash,
		SourceContractVersion: rastercompositor.NativeSourceContractVersion,
		SourceProfile:         rastercompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		Assets: append([]AssetReceipt(nil), provider.receipts...),
	}
}
