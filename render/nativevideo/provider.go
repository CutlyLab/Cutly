// Package nativevideo binds the FFmpeg video decoder to the strict native
// source contract used by rastercompositor. It freezes document clip scopes
// and rejects a request that attempts to select another asset, clip, time, or
// semantic profile.
package nativevideo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

var (
	ErrInvalid       = errors.New("nativevideo: invalid input")
	ErrMismatch      = errors.New("nativevideo: request or binding mismatch")
	ErrMissing       = errors.New("nativevideo: required binding is missing")
	ErrExtra         = errors.New("nativevideo: binding is not referenced")
	ErrResourceLimit = errors.New("nativevideo: resource limit exceeded")
	ErrSourceFailed  = errors.New("nativevideo: decoder source failed")
)

const (
	DefaultMaxBindings      = 1024
	DefaultMaxClipScopes    = 65536
	DefaultMaxDocumentBytes = 64 << 20
	HardMaxBindings         = 65536
	HardMaxClipScopes       = 1 << 20
	HardMaxDocumentBytes    = 1 << 30
)

type Limits struct {
	MaxBindings               int `json:"max_bindings"`
	MaxClipScopes             int `json:"max_clip_scopes"`
	MaxCanonicalDocumentBytes int `json:"max_canonical_document_bytes"`
}

func DefaultLimits() Limits {
	return Limits{MaxBindings: DefaultMaxBindings, MaxClipScopes: DefaultMaxClipScopes, MaxCanonicalDocumentBytes: DefaultMaxDocumentBytes}
}

// FrameSource is the narrow immutable decoder contract. *videodecode.Source
// implements it. Alternate implementations are inside the trusted provider
// boundary and must make equally strong identity and pixel-causality claims.
type FrameSource interface {
	Frame(context.Context, videoedit.RationalTime) (*cpucompositor.Surface, videodecode.FrameReceipt, error)
	Identity() (videodecode.SourceIdentity, error)
}

type Binding struct {
	DocumentHash string        `json:"document_hash"`
	AssetID      documentv2.ID `json:"asset_id"`
	Source       FrameSource   `json:"-"`
}

type clipKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type frozenAsset struct {
	id          documentv2.ID
	index       int
	contentHash string
}

type allowedClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	asset         frozenAsset
	clip          documentv2.Clip
}

type boundSource struct {
	identity videodecode.SourceIdentity
	source   FrameSource
}

type Provider struct {
	documentHash string
	clips        map[clipKey]allowedClip
	sources      map[documentv2.ID]boundSource
}

var _ rastercompositor.NativeSourceProvider = (*Provider)(nil)

func Profiles() rastercompositor.AssetSourceProfiles {
	return rastercompositor.AssetSourceProfiles{
		DecodeProfile:        videodecode.AdapterSemanticProfileV1,
		SelectionProfile:     videodecode.SelectionSemanticProfileV1,
		ColorProfile:         videodecode.ColorSemanticProfileV1,
		NormalizationProfile: videodecode.NormalizationSemanticProfileV1,
	}
}

// Compile borrows immutable decoder Sources for Provider lifetime. It does not
// close them. Callers must not close a Source while renders can still reach
// this Provider.
func Compile(ctx context.Context, document documentv2.Document, bindings []Binding, limits Limits) (*Provider, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if len(bindings) > limits.MaxBindings {
		return nil, ErrResourceLimit
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, fmt.Errorf("%w: document: %v", ErrInvalid, err)
	}
	if len(canonical) > limits.MaxCanonicalDocumentBytes {
		return nil, ErrResourceLimit
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: freeze document: %v", ErrInvalid, err)
	}
	documentHash, err := documentv2.DocumentHash(frozen)
	if err != nil {
		return nil, fmt.Errorf("%w: document hash: %v", ErrInvalid, err)
	}
	assets := make(map[documentv2.ID]frozenAsset, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		if err := periodicContext(ctx, index); err != nil {
			return nil, err
		}
		if asset.Kind == documentv2.AssetKindVideo {
			assets[asset.ID] = frozenAsset{id: asset.ID, index: index, contentHash: asset.ContentHash}
		}
	}
	required := make(map[documentv2.ID]bool)
	clips := make(map[clipKey]allowedClip)
	visits := 0
	for timelineIndex, timeline := range frozen.Timelines {
		for trackIndex, track := range timeline.Tracks {
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for clipIndex, clip := range track.Clips {
				visits++
				if err := periodicContext(ctx, visits); err != nil {
					return nil, err
				}
				if clip.Disabled {
					continue
				}
				asset, video := assets[clip.AssetID]
				if !video {
					continue
				}
				if len(clips) >= limits.MaxClipScopes {
					return nil, ErrResourceLimit
				}
				clips[clipKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}] = allowedClip{
					timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex, asset: asset, clip: clip,
				}
				if !required[asset.id] && len(required) >= limits.MaxBindings {
					return nil, ErrResourceLimit
				}
				required[asset.id] = true
			}
		}
	}
	byID := make(map[documentv2.ID]Binding, len(bindings))
	for index, binding := range bindings {
		if err := periodicContext(ctx, index); err != nil {
			return nil, err
		}
		if binding.DocumentHash != documentHash || nilFrameSource(binding.Source) {
			return nil, fmt.Errorf("%w: binding %d", ErrMismatch, index)
		}
		if _, duplicate := byID[binding.AssetID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate asset %q", ErrInvalid, binding.AssetID)
		}
		if !required[binding.AssetID] {
			return nil, fmt.Errorf("%w: asset %q", ErrExtra, binding.AssetID)
		}
		byID[binding.AssetID] = binding
	}
	ids := make([]documentv2.ID, 0, len(required))
	for id := range required {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	sources := make(map[documentv2.ID]boundSource, len(ids))
	for index, id := range ids {
		if err := periodicContext(ctx, index); err != nil {
			return nil, err
		}
		binding, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("%w: asset %q", ErrMissing, id)
		}
		identity, err := binding.Source.Identity()
		if err != nil {
			return nil, fmt.Errorf("%w: identity for %q: %v", ErrSourceFailed, id, err)
		}
		if !canonicalSHA256(identity.AdapterFingerprint) || !canonicalSHA256(identity.ProbeFingerprint) ||
			!canonicalSHA256(identity.IndexFingerprint) || !canonicalSHA256(identity.SemanticsFingerprint) ||
			!canonicalSHA256(identity.ResourceHash) || identity.StreamIndex < 0 || identity.ResourceHash != assets[id].contentHash ||
			identity.Profiles.Decode != videodecode.AdapterSemanticProfileV1 || identity.Profiles.Selection != videodecode.SelectionSemanticProfileV1 ||
			identity.Profiles.Color != videodecode.ColorSemanticProfileV1 || identity.Profiles.Normalization != videodecode.NormalizationSemanticProfileV1 {
			return nil, fmt.Errorf("%w: decoder identity for %q", ErrMismatch, id)
		}
		sources[id] = boundSource{identity: identity, source: binding.Source}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Provider{documentHash: documentHash, clips: clips, sources: sources}, nil
}

func (provider *Provider) Frame(ctx context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
	if provider == nil || ctx == nil {
		return rastercompositor.NativeSourceFrame{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return rastercompositor.NativeSourceFrame{}, err
	}
	profiles := Profiles()
	if request.ContractVersion != rastercompositor.NativeSourceContractVersion || request.DocumentHash != provider.documentHash ||
		request.AssetKind != documentv2.AssetKindVideo || request.SourceProfile != rastercompositor.SourceProfileV1 ||
		request.SurfaceProfile != cpucompositor.SurfaceProfileV1 || request.DecodeProfile != profiles.DecodeProfile ||
		request.SelectionProfile != profiles.SelectionProfile || request.ColorProfile != profiles.ColorProfile ||
		request.NormalizationProfile != profiles.NormalizationProfile {
		return rastercompositor.NativeSourceFrame{}, ErrMismatch
	}
	allowed, exists := provider.clips[clipKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists || request.TimelineIndex != allowed.timelineIndex || request.TrackIndex != allowed.trackIndex ||
		request.ClipIndex != allowed.clipIndex || request.AssetID != allowed.asset.id || request.AssetIndex != allowed.asset.index ||
		request.AssetContentHash != allowed.asset.contentHash {
		return rastercompositor.NativeSourceFrame{}, ErrMismatch
	}
	expectedLocal, err := evaluator.ClipLocalTime(allowed.clip, request.TimelineTime)
	if err != nil {
		return rastercompositor.NativeSourceFrame{}, fmt.Errorf("%w: timeline time", ErrInvalid)
	}
	expectedSource, err := evaluator.SourceTime(allowed.clip, request.TimelineTime)
	if err != nil || !equivalentTime(request.LocalTime, expectedLocal) || !equivalentTime(request.SourceTime, expectedSource) {
		return rastercompositor.NativeSourceFrame{}, ErrMismatch
	}
	bound, exists := provider.sources[request.AssetID]
	if !exists {
		return rastercompositor.NativeSourceFrame{}, ErrMissing
	}
	surface, receipt, err := bound.source.Frame(ctx, request.SourceTime)
	if err != nil {
		return rastercompositor.NativeSourceFrame{}, errors.Join(ErrSourceFailed, err)
	}
	if surface == nil || receipt.ResourceHash != request.AssetContentHash || receipt.AdapterFingerprint != bound.identity.AdapterFingerprint ||
		receipt.ProbeFingerprint != bound.identity.ProbeFingerprint || receipt.IndexFingerprint != bound.identity.IndexFingerprint ||
		receipt.SemanticsFingerprint != bound.identity.SemanticsFingerprint ||
		receipt.StreamIndex != bound.identity.StreamIndex || receipt.Profiles != bound.identity.Profiles ||
		receipt.Width != surface.Width() || receipt.Height != surface.Height() {
		return rastercompositor.NativeSourceFrame{}, ErrMismatch
	}
	actualHash, err := surfaceHashWithContext(ctx, surface)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return rastercompositor.NativeSourceFrame{}, ctxErr
	}
	if err != nil || actualHash != receipt.SurfaceHash {
		return rastercompositor.NativeSourceFrame{}, ErrMismatch
	}
	if err := ctx.Err(); err != nil {
		return rastercompositor.NativeSourceFrame{}, err
	}
	return rastercompositor.NativeSourceFrame{
		ContractVersion: rastercompositor.NativeSourceContractVersion, Request: request,
		Width: surface.Width(), Height: surface.Height(), AssetContentHash: receipt.ResourceHash, PixelHash: receipt.SurfaceHash,
		DecodeProfile: profiles.DecodeProfile, SelectionProfile: profiles.SelectionProfile,
		ColorProfile: profiles.ColorProfile, NormalizationProfile: profiles.NormalizationProfile,
		SourceProfile: rastercompositor.SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1, Surface: surface,
	}, nil
}

func (provider *Provider) DocumentHash() string {
	if provider == nil {
		return ""
	}
	return provider.documentHash
}

func normalizeLimits(value Limits) (Limits, error) {
	if value.MaxBindings == 0 {
		value.MaxBindings = DefaultMaxBindings
	}
	if value.MaxClipScopes == 0 {
		value.MaxClipScopes = DefaultMaxClipScopes
	}
	if value.MaxCanonicalDocumentBytes == 0 {
		value.MaxCanonicalDocumentBytes = DefaultMaxDocumentBytes
	}
	if value.MaxBindings <= 0 || value.MaxBindings > HardMaxBindings || value.MaxClipScopes <= 0 || value.MaxClipScopes > HardMaxClipScopes ||
		value.MaxCanonicalDocumentBytes <= 0 || value.MaxCanonicalDocumentBytes > HardMaxDocumentBytes {
		return Limits{}, ErrResourceLimit
	}
	return value, nil
}

func nilFrameSource(source FrameSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func periodicContext(ctx context.Context, value int) error {
	if value&1023 == 0 {
		return ctx.Err()
	}
	return nil
}

func equivalentTime(left, right videoedit.RationalTime) bool {
	comparison, err := left.Compare(right)
	return err == nil && comparison == 0
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == sha256.Size
}

type contextCheckedWriter struct {
	ctx        context.Context
	writer     io.Writer
	sinceCheck int
}

func (writer *contextCheckedWriter) Write(data []byte) (int, error) {
	writer.sinceCheck += len(data)
	if writer.sinceCheck >= 32<<10 {
		if err := writer.ctx.Err(); err != nil {
			return 0, err
		}
		writer.sinceCheck = 0
	}
	return writer.writer.Write(data)
}

func surfaceHashWithContext(ctx context.Context, surface *cpucompositor.Surface) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hasher := sha256.New()
	writer := &contextCheckedWriter{ctx: ctx, writer: hasher}
	if err := surface.WriteCanonical(writer); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}
