package rastercompositor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const NativeSourceContractVersion = 1

// NativeSourceRequest is the complete immutable source-selection request. DocumentHash scopes
// otherwise document-local IDs when a provider serves multiple compiled Programs.
type NativeSourceRequest struct {
	ContractVersion      int                     `json:"contract_version"`
	DocumentHash         string                  `json:"document_hash"`
	TimelineID           documentv2.ID           `json:"timeline_id"`
	TimelineIndex        int                     `json:"timeline_index"`
	TrackID              documentv2.ID           `json:"track_id"`
	ClipID               documentv2.ID           `json:"clip_id"`
	AssetID              documentv2.ID           `json:"asset_id"`
	AssetIndex           int                     `json:"asset_index"`
	AssetKind            documentv2.AssetKind    `json:"asset_kind"`
	AssetContentHash     string                  `json:"asset_content_hash"`
	TrackIndex           int                     `json:"track_index"`
	ClipIndex            int                     `json:"clip_index"`
	TimelineTime         documentv2.RationalTime `json:"timeline_time"`
	LocalTime            documentv2.RationalTime `json:"local_time"`
	SourceTime           documentv2.RationalTime `json:"source_time"`
	SourceProfile        string                  `json:"source_profile"`
	SurfaceProfile       string                  `json:"surface_profile"`
	DecodeProfile        string                  `json:"decode_profile"`
	SelectionProfile     string                  `json:"selection_profile"`
	ColorProfile         string                  `json:"color_profile"`
	NormalizationProfile string                  `json:"normalization_profile"`
}

// NativeSourceFrame is a provider-owned immutable normalized surface plus the claims required to
// validate and bind it. Request must exactly echo the input request. Surface is never serialized.
type NativeSourceFrame struct {
	ContractVersion      int                    `json:"contract_version"`
	Request              NativeSourceRequest    `json:"request"`
	Width                int                    `json:"width"`
	Height               int                    `json:"height"`
	AssetContentHash     string                 `json:"asset_content_hash"`
	PixelHash            string                 `json:"pixel_hash"`
	DecodeProfile        string                 `json:"decode_profile"`
	SelectionProfile     string                 `json:"selection_profile"`
	ColorProfile         string                 `json:"color_profile"`
	NormalizationProfile string                 `json:"normalization_profile"`
	SourceProfile        string                 `json:"source_profile"`
	SurfaceProfile       string                 `json:"surface_profile"`
	Surface              *cpucompositor.Surface `json:"-"`
}

// SourceReceipt is the validated, serializable form retained in Result. Receipt order is source
// request order: evaluator layer order, then outgoing before incoming for a transition.
type SourceReceipt struct {
	ContractVersion      int                 `json:"contract_version"`
	Request              NativeSourceRequest `json:"request"`
	Width                int                 `json:"width"`
	Height               int                 `json:"height"`
	AssetContentHash     string              `json:"asset_content_hash"`
	PixelHash            string              `json:"pixel_hash"`
	DecodeProfile        string              `json:"decode_profile"`
	SelectionProfile     string              `json:"selection_profile"`
	ColorProfile         string              `json:"color_profile"`
	NormalizationProfile string              `json:"normalization_profile"`
	SourceProfile        string              `json:"source_profile"`
	SurfaceProfile       string              `json:"surface_profile"`
}

type NativeSourceProvider interface {
	// Frame may be called concurrently by independent renders. A provider must either be safe for
	// concurrent immutable reads or serialize internally without changing results.
	Frame(context.Context, NativeSourceRequest) (NativeSourceFrame, error)
}

type NativeSourceProviderFunc func(context.Context, NativeSourceRequest) (NativeSourceFrame, error)

func (function NativeSourceProviderFunc) Frame(ctx context.Context, request NativeSourceRequest) (NativeSourceFrame, error) {
	if function == nil {
		return NativeSourceFrame{}, ErrProviderUnavailable
	}
	return function(ctx, request)
}

func nilProvider(provider NativeSourceProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (program *Program) sourceFrame(
	ctx context.Context,
	clip evaluator.ClipEvaluation,
	provider NativeSourceProvider,
	path string,
) (NativeSourceFrame, SourceReceipt, error) {
	asset, assetExists := program.assets[clip.AssetID]
	timelineIndex, timelineExists := program.timelines[clip.TimelineID]
	if !assetExists || !timelineExists || (asset.Kind != documentv2.AssetKindVideo && asset.Kind != documentv2.AssetKindImage) ||
		!canonicalSHA256(asset.ContentHash) {
		return NativeSourceFrame{}, SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/scope", ErrSourceInvalid)
	}
	profiles, profilesOK := program.sourceProfiles(asset.Kind)
	if !profilesOK {
		return NativeSourceFrame{}, SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/profiles", ErrSourceInvalid)
	}
	request := NativeSourceRequest{
		ContractVersion: NativeSourceContractVersion,
		DocumentHash:    program.evaluator.DocumentHash(),
		TimelineID:      clip.TimelineID, TimelineIndex: timelineIndex,
		TrackID: clip.TrackID, ClipID: clip.ClipID, AssetID: clip.AssetID,
		AssetIndex: asset.Index, AssetKind: asset.Kind, AssetContentHash: asset.ContentHash,
		TrackIndex: clip.TrackIndex, ClipIndex: clip.ClipIndex,
		TimelineTime: clip.TimelineTime, LocalTime: clip.LocalTime, SourceTime: clip.SourceTime,
		SourceProfile: SourceProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
		DecodeProfile: profiles.DecodeProfile, SelectionProfile: profiles.SelectionProfile,
		ColorProfile: profiles.ColorProfile, NormalizationProfile: profiles.NormalizationProfile,
	}
	if err := validateSourceRequest(request); err != nil {
		return NativeSourceFrame{}, SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/request", errors.Join(ErrSourceInvalid, err))
	}
	if err := contextError(ctx, path); err != nil {
		return NativeSourceFrame{}, SourceReceipt{}, err
	}
	frame, err := provider.Frame(ctx, request)
	if err != nil {
		if canceled := contextError(ctx, path); canceled != nil {
			return NativeSourceFrame{}, SourceReceipt{}, canceled
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return NativeSourceFrame{}, SourceReceipt{}, compositorError(CodeCanceled, path, errors.Join(ErrCanceled, err))
		}
		return NativeSourceFrame{}, SourceReceipt{}, compositorError(CodeSourceFailed, path, errors.Join(ErrSourceFailed, err))
	}
	if err := contextError(ctx, path); err != nil {
		return NativeSourceFrame{}, SourceReceipt{}, err
	}
	receipt, err := program.validateSourceFrame(ctx, request, frame, path+"/response")
	if err != nil {
		return NativeSourceFrame{}, SourceReceipt{}, err
	}
	return frame, receipt, nil
}

func (program *Program) validateSourceFrame(
	ctx context.Context,
	request NativeSourceRequest,
	frame NativeSourceFrame,
	path string,
) (SourceReceipt, error) {
	if frame.ContractVersion != NativeSourceContractVersion || frame.Request != request {
		return SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/request", ErrSourceMismatch)
	}
	if frame.Surface == nil || frame.Width <= 0 || frame.Height <= 0 ||
		frame.Width != frame.Surface.Width() || frame.Height != frame.Surface.Height() {
		return SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/surface", ErrSourceInvalid)
	}
	if frame.SourceProfile != SourceProfileV1 || frame.SourceProfile != request.SourceProfile ||
		frame.SurfaceProfile != cpucompositor.SurfaceProfileV1 || frame.SurfaceProfile != request.SurfaceProfile {
		return SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/profiles", ErrSourceMismatch)
	}
	if frame.AssetContentHash != request.AssetContentHash || frame.DecodeProfile != request.DecodeProfile ||
		frame.SelectionProfile != request.SelectionProfile || frame.ColorProfile != request.ColorProfile ||
		frame.NormalizationProfile != request.NormalizationProfile {
		return SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/bindings", ErrSourceMismatch)
	}
	if !canonicalSHA256(frame.AssetContentHash) || !canonicalSHA256(frame.PixelHash) ||
		!canonicalProfileToken(frame.DecodeProfile) || !canonicalProfileToken(frame.SelectionProfile) ||
		!canonicalProfileToken(frame.ColorProfile) || !canonicalProfileToken(frame.NormalizationProfile) {
		return SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/bindings", ErrSourceInvalid)
	}
	if frame.Width > program.config.Limits.Raster.MaxWidth || frame.Height > program.config.Limits.Raster.MaxHeight {
		return SourceReceipt{}, compositorError(CodeSourceLimit, path+"/dimensions", ErrSourceLimit)
	}
	width, height := uint64(frame.Width), uint64(frame.Height)
	if height != 0 && width > math.MaxUint64/height {
		return SourceReceipt{}, compositorError(CodeSourceLimit, path+"/pixel_count", ErrSourceLimit)
	}
	pixels := width * height
	if pixels > math.MaxUint64/cpucompositor.BytesPerPixel || pixels*cpucompositor.BytesPerPixel > program.config.Limits.MaxSourceBytes {
		return SourceReceipt{}, compositorError(CodeSourceLimit, path+"/bytes", ErrSourceLimit)
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return SourceReceipt{}, err
	}
	actualHash, err := frame.Surface.CanonicalHash()
	if err != nil {
		return SourceReceipt{}, compositorError(CodeSourceInvalid, path+"/hash", errors.Join(ErrSourceInvalid, err))
	}
	if err := contextError(ctx, path+"/hash"); err != nil {
		return SourceReceipt{}, err
	}
	if actualHash != frame.PixelHash {
		return SourceReceipt{}, compositorError(CodeSourceMismatch, path+"/pixel_hash", ErrSourceMismatch)
	}
	return SourceReceipt{
		ContractVersion: frame.ContractVersion,
		Request:         frame.Request, Width: frame.Width, Height: frame.Height,
		AssetContentHash: frame.AssetContentHash, PixelHash: frame.PixelHash,
		DecodeProfile: frame.DecodeProfile, SelectionProfile: frame.SelectionProfile,
		ColorProfile:         frame.ColorProfile,
		NormalizationProfile: frame.NormalizationProfile,
		SourceProfile:        frame.SourceProfile, SurfaceProfile: frame.SurfaceProfile,
	}, nil
}

func validateSourceRequest(request NativeSourceRequest) error {
	if request.ContractVersion != NativeSourceContractVersion || !canonicalSHA256(request.DocumentHash) || strings.TrimSpace(string(request.TimelineID)) == "" ||
		strings.TrimSpace(string(request.TrackID)) == "" || strings.TrimSpace(string(request.ClipID)) == "" ||
		strings.TrimSpace(string(request.AssetID)) == "" || request.TimelineIndex < 0 || request.TrackIndex < 0 || request.ClipIndex < 0 || request.AssetIndex < 0 ||
		(request.AssetKind != documentv2.AssetKindVideo && request.AssetKind != documentv2.AssetKindImage) || !canonicalSHA256(request.AssetContentHash) ||
		request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil ||
		request.SourceProfile != SourceProfileV1 || request.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		!canonicalProfileToken(request.DecodeProfile) || !canonicalProfileToken(request.SelectionProfile) ||
		!canonicalProfileToken(request.ColorProfile) || !canonicalProfileToken(request.NormalizationProfile) {
		return ErrSourceInvalid
	}
	return nil
}

func (program *Program) sourceProfiles(kind documentv2.AssetKind) (AssetSourceProfiles, bool) {
	if program == nil {
		return AssetSourceProfiles{}, false
	}
	switch kind {
	case documentv2.AssetKindVideo:
		return program.config.SourceProfiles.Video, program.config.Source.Video
	case documentv2.AssetKindImage:
		return program.config.SourceProfiles.Image, program.config.Source.Image
	default:
		return AssetSourceProfiles{}, false
	}
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

// Source metadata fields are semantic profile IDs, not arbitrary provider logs or build output.
func canonicalProfileToken(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for index, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) || character > unicode.MaxASCII {
			return false
		}
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' || character == ':') {
			return false
		}
		if index == 0 && !(character >= 'a' && character <= 'z') {
			return false
		}
	}
	version := strings.LastIndex(value, ".v")
	if version <= 0 || version+2 >= len(value) {
		return false
	}
	for _, character := range value[version+2:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func contextError(ctx context.Context, path string) error {
	if ctx == nil {
		return compositorError(CodeInvalidConfig, path, ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return compositorError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func sourceRequestCount(layers []evaluator.VisualLayer) (int, error) {
	count := 0
	for index, layer := range layers {
		switch layer.Kind {
		case evaluator.VisualLayerClip:
			if layer.Clip == nil || layer.Transition != nil {
				return 0, fmt.Errorf("layer %d is malformed", index)
			}
			count++
		case evaluator.VisualLayerTransition:
			if layer.Transition == nil || layer.Clip != nil || layer.Transition.Outgoing == nil || layer.Transition.Incoming == nil {
				return 0, fmt.Errorf("layer %d is malformed", index)
			}
			count += 2
		default:
			return 0, fmt.Errorf("layer %d has unknown kind %q", index, layer.Kind)
		}
	}
	return count, nil
}
