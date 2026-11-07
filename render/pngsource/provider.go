package pngsource

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
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/referencecompositor"
)

// Binding provides the complete immutable bytes for one Document image asset. EncodedPNG is
// borrowed and must remain read-only until Compile returns; the Provider never retains it.
type Binding struct {
	DocumentHash string        `json:"document_hash"`
	AssetID      documentv2.ID `json:"asset_id"`
	EncodedPNG   []byte        `json:"-"`
}

type AssetReceipt struct {
	AssetID              documentv2.ID `json:"asset_id"`
	EncodedContentHash   string        `json:"encoded_content_hash"`
	PixelHash            string        `json:"pixel_hash"`
	Width                int           `json:"width"`
	Height               int           `json:"height"`
	SRGBRenderingIntent  uint8         `json:"srgb_rendering_intent"`
	DecodeProfile        string        `json:"decode_profile"`
	NormalizationProfile string        `json:"normalization_profile"`
	ContentHashProfile   string        `json:"content_hash_profile"`
	SourceProfile        string        `json:"source_profile"`
	SurfaceProfile       string        `json:"surface_profile"`
}

type Manifest struct {
	SchemaVersion  int            `json:"schema_version"`
	Profile        string         `json:"profile"`
	DocumentHash   string         `json:"document_hash"`
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	SourceProfile  string         `json:"source_profile"`
	SurfaceProfile string         `json:"surface_profile"`
	Assets         []AssetReceipt `json:"assets,omitempty"`
}

type clipScopeKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type allowedClip struct {
	assetID    documentv2.ID
	trackIndex int
	clipIndex  int
}

type assetEntry struct {
	surface *cpucompositor.Surface
	receipt AssetReceipt
}

// Provider is immutable after Compile and safe for concurrent Surface and Manifest calls.
type Provider struct {
	documentHash string
	config       Config
	assets       map[documentv2.ID]assetEntry
	clips        map[clipScopeKey]allowedClip
	receipts     []AssetReceipt
}

var _ referencecompositor.SourceProvider = (*Provider)(nil)

// Compile freezes the document and all referenced static image surfaces. It either returns one
// complete immutable Provider or nil; decoded prefix state is never exposed on failure.
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
	if err := preflightClipScopes(ctx, document, normalized.Limits.MaxClipScopes); err != nil {
		return nil, err
	}

	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if err := contextFailure(ctx, "document"); err != nil {
		return nil, err
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	documentHash, err := documentv2.DocumentHash(frozen)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document/hash", errors.Join(ErrDocumentInvalid, err))
	}

	assetsByID := make(map[documentv2.ID]documentv2.Asset, len(frozen.Assets))
	for _, asset := range frozen.Assets {
		assetsByID[asset.ID] = asset
	}
	required := make(map[documentv2.ID]struct{})
	clips := make(map[clipScopeKey]allowedClip)
	for timelineIndex, timeline := range frozen.Timelines {
		for trackIndex, track := range timeline.Tracks {
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for clipIndex, clip := range track.Clips {
				if clip.Disabled {
					continue
				}
				if len(clips) >= normalized.Limits.MaxClipScopes {
					return nil, sourceError(CodeResourceLimit, "document/enabled_video_clip_scopes", ErrResourceLimit)
				}
				asset, exists := assetsByID[clip.AssetID]
				if !exists || asset.Kind != documentv2.AssetKindImage {
					return nil, sourceError(CodeDocumentInvalid,
						fmt.Sprintf("document/timelines/%d/tracks/%d/clips/%d/asset_id", timelineIndex, trackIndex, clipIndex),
						ErrDocumentInvalid)
				}
				if !canonicalSHA256(asset.ContentHash) {
					return nil, sourceError(CodeDocumentInvalid,
						"document/assets/content_hash", ErrDocumentInvalid)
				}
				required[asset.ID] = struct{}{}
				clips[clipScopeKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}] = allowedClip{
					assetID: asset.ID, trackIndex: trackIndex, clipIndex: clipIndex,
				}
			}
		}
	}
	if len(required) > normalized.Limits.MaxBindings {
		return nil, sourceError(CodeResourceLimit, "document/referenced_images", ErrResourceLimit)
	}
	if err := validateRetainedBudget(normalized, len(required)); err != nil {
		return nil, err
	}

	bindingByID := make(map[documentv2.ID]Binding, len(bindings))
	for index, binding := range bindings {
		if binding.DocumentHash != documentHash {
			return nil, sourceError(CodeDocumentMismatch, fmt.Sprintf("bindings/%d/document_hash", index), ErrDocumentMismatch)
		}
		if strings.TrimSpace(string(binding.AssetID)) == "" {
			return nil, sourceError(CodeBindingInvalid, fmt.Sprintf("bindings/%d/asset_id", index), ErrBindingInvalid)
		}
		if _, duplicate := bindingByID[binding.AssetID]; duplicate {
			return nil, sourceError(CodeBindingInvalid, fmt.Sprintf("bindings/%d/asset_id", index), ErrBindingInvalid)
		}
		asset, exists := assetsByID[binding.AssetID]
		if !exists || asset.Kind != documentv2.AssetKindImage {
			return nil, sourceError(CodeBindingInvalid, fmt.Sprintf("bindings/%d/asset_id", index), ErrBindingInvalid)
		}
		if _, expected := required[binding.AssetID]; !expected {
			return nil, sourceError(CodeBindingExtra, fmt.Sprintf("bindings/%d", index), ErrBindingExtra)
		}
		bindingByID[binding.AssetID] = binding
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
	for index, assetID := range assetIDs {
		if err := contextFailure(ctx, fmt.Sprintf("bindings/%d", index)); err != nil {
			return nil, err
		}
		encodedPNG := bindingByID[assetID].EncodedPNG
		if uint64(len(encodedPNG)) > normalized.Limits.PNG.MaxEncodedBytes {
			return nil, sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/encoded_bytes", index), ErrResourceLimit)
		}
		encodedHash, hashErr := encodedSHA256(ctx, encodedPNG, fmt.Sprintf("bindings/%d/content_hash", index))
		if hashErr != nil {
			return nil, hashErr
		}
		asset := assetsByID[assetID]
		if encodedHash != asset.ContentHash {
			return nil, sourceError(CodeContentMismatch, fmt.Sprintf("bindings/%d/content_hash", index), ErrContentMismatch)
		}
		decoded, decodeErr := pngdecode.Decode(ctx, encodedPNG, normalized.Limits.PNG)
		if decodeErr != nil {
			if errors.Is(decodeErr, pngdecode.ErrCanceled) || errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, context.DeadlineExceeded) {
				return nil, sourceError(CodeCanceled, fmt.Sprintf("bindings/%d/decode", index), errors.Join(ErrCanceled, decodeErr))
			}
			if errors.Is(decodeErr, pngdecode.ErrResourceLimit) {
				return nil, sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/decode", index), errors.Join(ErrResourceLimit, decodeErr))
			}
			return nil, sourceError(CodeDecodeFailed, fmt.Sprintf("bindings/%d/decode", index), errors.Join(ErrDecodeFailed, decodeErr))
		}
		if err := contextFailure(ctx, fmt.Sprintf("bindings/%d", index)); err != nil {
			return nil, err
		}
		if decoded.EncodedContentHash != encodedHash {
			return nil, sourceError(CodeContentMismatch, fmt.Sprintf("bindings/%d/content_hash", index), ErrContentMismatch)
		}
		if decoded.Width != normalized.Width || decoded.Height != normalized.Height {
			return nil, sourceError(CodeDimensionMismatch, fmt.Sprintf("bindings/%d/dimensions", index), ErrDimensionMismatch)
		}
		receipt := AssetReceipt{
			AssetID: assetID, EncodedContentHash: decoded.EncodedContentHash, PixelHash: decoded.PixelHash,
			Width: decoded.Width, Height: decoded.Height, SRGBRenderingIntent: decoded.SRGBRenderingIntent,
			DecodeProfile: decoded.DecodeProfile, NormalizationProfile: decoded.NormalizationProfile,
			ContentHashProfile: decoded.ContentHashProfile,
			SourceProfile:      referencecompositor.SourceProfileV1, SurfaceProfile: decoded.SurfaceProfile,
		}
		entries[assetID] = assetEntry{surface: decoded.Surface, receipt: receipt}
		receipts = append(receipts, receipt)
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return nil, err
	}
	return &Provider{
		documentHash: documentHash, config: normalized,
		assets: entries, clips: clips, receipts: receipts,
	}, nil
}

func (provider *Provider) Surface(ctx context.Context, request referencecompositor.SourceRequest) (*cpucompositor.Surface, error) {
	if provider == nil {
		return nil, sourceError(CodeProviderInvalid, "provider", ErrProviderInvalid)
	}
	if ctx == nil {
		return nil, sourceError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return nil, err
	}
	if request.DocumentHash != provider.documentHash {
		return nil, sourceError(CodeDocumentMismatch, "request/document_hash", ErrDocumentMismatch)
	}
	if request.SourceProfile != referencecompositor.SourceProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		request.Width != provider.config.Width || request.Height != provider.config.Height {
		return nil, sourceError(CodeRequestInvalid, "request/profile_or_dimensions", ErrRequestInvalid)
	}
	if strings.TrimSpace(string(request.TimelineID)) == "" || strings.TrimSpace(string(request.TrackID)) == "" ||
		strings.TrimSpace(string(request.ClipID)) == "" || strings.TrimSpace(string(request.AssetID)) == "" ||
		request.TrackIndex < 0 || request.ClipIndex < 0 || request.TimelineTime.Validate() != nil ||
		request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil {
		return nil, sourceError(CodeRequestInvalid, "request", ErrRequestInvalid)
	}
	allowed, exists := provider.clips[clipScopeKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists || allowed.assetID != request.AssetID || allowed.trackIndex != request.TrackIndex || allowed.clipIndex != request.ClipIndex {
		return nil, sourceError(CodeRequestInvalid, "request/scope", ErrRequestInvalid)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.surface == nil {
		return nil, sourceError(CodeAssetUnavailable, "request/asset_id", ErrAssetUnavailable)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return nil, err
	}
	return entry.surface, nil
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
		SchemaVersion: ManifestSchemaVersion, Profile: ProfileV1,
		DocumentHash: provider.documentHash, Width: provider.config.Width, Height: provider.config.Height,
		SourceProfile: referencecompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		Assets: append([]AssetReceipt(nil), provider.receipts...),
	}
}

func normalizeConfig(value Config) (Config, error) {
	result := value
	if result.Limits == (Limits{}) {
		result.Limits = DefaultLimits()
	}
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, sourceError(CodeConfigInvalid, "config/dimensions", ErrConfigInvalid)
	}
	if result.Limits.MaxBindings <= 0 || result.Limits.MaxBindings > HardMaxBindings ||
		result.Limits.MaxClipScopes <= 0 || result.Limits.MaxClipScopes > HardMaxClipScopes ||
		result.Limits.MaxRetainedBytes < cpucompositor.BytesPerPixel || result.Limits.MaxRetainedBytes > HardMaxRetainedBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits", ErrConfigInvalid)
	}
	if result.Limits.PNG == (pngdecode.Limits{}) {
		result.Limits.PNG = pngdecode.DefaultLimits()
	}
	pngLimits := result.Limits.PNG
	if pngLimits.MaxWidth <= 0 || pngLimits.MaxHeight <= 0 || pngLimits.MaxEncodedBytes < 8 ||
		pngLimits.MaxEncodedBytes > pngdecode.HardMaxEncodedBytes || pngLimits.MaxWorkingBytes < 30 ||
		pngLimits.MaxWorkingBytes > pngdecode.HardMaxWorkingBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits/png", ErrConfigInvalid)
	}
	if result.Width > pngLimits.MaxWidth || result.Height > pngLimits.MaxHeight {
		return Config{}, sourceError(CodeConfigInvalid, "config/dimensions", ErrConfigInvalid)
	}
	width, height := uint64(result.Width), uint64(result.Height)
	if height != 0 && width > math.MaxUint64/height {
		return Config{}, sourceError(CodeResourceLimit, "config/dimensions", ErrResourceLimit)
	}
	pixels := width * height
	if pixels > math.MaxUint64/cpucompositor.BytesPerPixel || pixels*cpucompositor.BytesPerPixel > cpucompositor.HardMaxSurfaceBytes {
		return Config{}, sourceError(CodeResourceLimit, "config/surface_bytes", ErrResourceLimit)
	}
	// pngdecode v1 retains one RGBA8 frame, two RGBA16 frames and two filtered RGBA8
	// scanlines at peak: width*height*20 + 2*(1+width*4). Since every accepted image must
	// exactly match this target, an insufficient per-image budget can be rejected before any
	// binding hash or decode work begins.
	frameBytes, ok := checkedMultiply(pixels, 20)
	if !ok {
		return Config{}, sourceError(CodeResourceLimit, "config/limits/png/max_working_bytes", ErrResourceLimit)
	}
	rowPayloadBytes, ok := checkedMultiply(width, 4)
	if !ok || rowPayloadBytes == math.MaxUint64 {
		return Config{}, sourceError(CodeResourceLimit, "config/limits/png/max_working_bytes", ErrResourceLimit)
	}
	rowBytes, ok := checkedMultiply(rowPayloadBytes+1, 2)
	if !ok {
		return Config{}, sourceError(CodeResourceLimit, "config/limits/png/max_working_bytes", ErrResourceLimit)
	}
	requiredWorkingBytes, ok := checkedAdd(frameBytes, rowBytes)
	if !ok || requiredWorkingBytes > pngLimits.MaxWorkingBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits/png/max_working_bytes", ErrConfigInvalid)
	}
	return result, nil
}

func validateRetainedBudget(config Config, count int) error {
	bytesPerSurface := uint64(config.Width) * uint64(config.Height) * cpucompositor.BytesPerPixel
	if count < 0 || uint64(count) > math.MaxUint64/bytesPerSurface || uint64(count)*bytesPerSurface > config.Limits.MaxRetainedBytes {
		return sourceError(CodeResourceLimit, "document/retained_surface_bytes", ErrResourceLimit)
	}
	return nil
}

// preflightClipScopes prevents an oversized render-scope index from reaching canonical document
// freezing first. It intentionally makes no semantic judgment; the frozen Document validator
// remains authoritative and the same limit is checked again while the immutable scope map is
// built.
func preflightClipScopes(ctx context.Context, document documentv2.Document, maximum int) error {
	count := 0
	visited := 0
	for _, timeline := range document.Timelines {
		for _, track := range timeline.Tracks {
			if visited&1023 == 0 {
				if err := contextFailure(ctx, "document/enabled_video_clip_scopes"); err != nil {
					return err
				}
			}
			visited++
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for _, clip := range track.Clips {
				if visited&1023 == 0 {
					if err := contextFailure(ctx, "document/enabled_video_clip_scopes"); err != nil {
						return err
					}
				}
				visited++
				if clip.Disabled {
					continue
				}
				count++
				if count > maximum {
					return sourceError(CodeResourceLimit, "document/enabled_video_clip_scopes", ErrResourceLimit)
				}
			}
		}
	}
	return contextFailure(ctx, "document/enabled_video_clip_scopes")
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

func encodedSHA256(ctx context.Context, encoded []byte, path string) (string, error) {
	hash := sha256.New()
	const chunkSize = 64 << 10
	for offset := 0; offset < len(encoded); offset += chunkSize {
		if err := contextFailure(ctx, path); err != nil {
			return "", err
		}
		end := offset + chunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = hash.Write(encoded[offset:end])
	}
	if err := contextFailure(ctx, path); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func checkedMultiply(left, right uint64) (uint64, bool) {
	if right != 0 && left > math.MaxUint64/right {
		return 0, false
	}
	return left * right, true
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}
