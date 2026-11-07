package referenceimage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

// Result is the complete unsigned receipt for one static-PNG export. The nested Surface, Frame
// and Artifact values are immutable dependency-owned objects and are intentionally omitted from
// JSON; their hashes and all serializable stage receipts remain present.
type Result struct {
	SchemaVersion    int                      `json:"schema_version"`
	Profile          string                   `json:"profile"`
	DocumentHash     string                   `json:"document_hash"`
	BuildFingerprint string                   `json:"build_fingerprint,omitempty"`
	TimelineID       documentv2.ID            `json:"timeline_id"`
	TimelineTime     documentv2.RationalTime  `json:"timeline_time"`
	SourceManifest   nativepngsource.Manifest `json:"source_manifest"`
	Raster           rastercompositor.Result  `json:"raster"`
	Output           color.OutputResult       `json:"output"`
	PNG              pngencode.Result         `json:"png"`
}

type FrameResult struct {
	FrameClock            videoedit.FrameClock `json:"frame_clock"`
	CompositionFrameIndex int64                `json:"composition_frame_index"`
	Result                Result               `json:"result"`
}

func (program *Program) RenderAt(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime) (Result, error) {
	if err := program.validateRuntime(ctx); err != nil {
		return Result{}, err
	}
	rasterResult, err := program.raster.RenderAt(ctx, timelineID, at, program.provider)
	if err != nil {
		return Result{}, classifyRenderDependency("raster", err)
	}
	if rasterResult.TimelineID != timelineID || rasterResult.TimelineTime != at {
		return Result{}, pipelineError(CodeStageMismatch, "raster/request", ErrStageMismatch)
	}
	return program.finish(ctx, rasterResult)
}

func (program *Program) RenderFrame(ctx context.Context, timelineID documentv2.ID, clock videoedit.FrameClock, compositionFrameIndex int64) (FrameResult, error) {
	if err := program.validateRuntime(ctx); err != nil {
		return FrameResult{}, err
	}
	expectedTime, err := videoedit.FrameTimeAt(clock, compositionFrameIndex)
	if err != nil {
		return FrameResult{}, pipelineError(CodeRenderFailed, "frame/clock", errors.Join(ErrRenderFailed, err))
	}
	rasterFrame, err := program.raster.RenderFrame(ctx, timelineID, clock, compositionFrameIndex, program.provider)
	if err != nil {
		return FrameResult{}, classifyRenderDependency("raster", err)
	}
	if rasterFrame.CompositionFrameIndex != compositionFrameIndex || rasterFrame.Result.TimelineID != timelineID ||
		rasterFrame.Result.TimelineTime != expectedTime {
		return FrameResult{}, pipelineError(CodeStageMismatch, "raster/frame", ErrStageMismatch)
	}
	result, err := program.finish(ctx, rasterFrame.Result)
	if err != nil {
		return FrameResult{}, err
	}
	return FrameResult{FrameClock: clock, CompositionFrameIndex: compositionFrameIndex, Result: result}, nil
}

func (program *Program) validateRuntime(ctx context.Context) error {
	if program == nil || program.raster == nil || program.provider == nil || program.documentHash == "" ||
		program.manifest.DocumentHash != program.documentHash {
		return pipelineError(CodeProgramUnavailable, "program", ErrProgramUnavailable)
	}
	if ctx == nil {
		return pipelineError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextError(ctx, "context"); err != nil {
		return err
	}
	if !program.runtimeInvariant() {
		return pipelineError(CodeProgramUnavailable, "program/invariant", ErrProgramUnavailable)
	}
	return nil
}

func (program *Program) runtimeInvariant() bool {
	normalized, expectedRaster, err := normalizeConfig(program.config)
	if err != nil || normalized != program.config || program.raster.DocumentHash() != program.documentHash ||
		program.raster.Config() != expectedRaster {
		return false
	}
	providerConfig := program.provider.Config()
	providerManifest := program.provider.Manifest()
	if providerConfig.Limits != program.config.SourceLimits || program.provider.DocumentHash() != program.documentHash ||
		providerManifest.SchemaVersion != nativepngsource.ManifestSchemaVersion ||
		providerManifest.Profile != nativepngsource.ProfileV1 ||
		providerManifest.DocumentHash != program.documentHash ||
		providerManifest.SourceContractVersion != rastercompositor.NativeSourceContractVersion ||
		!validSourceManifest(providerManifest, providerConfig.Limits) ||
		!equalManifest(providerManifest, program.manifest) {
		return false
	}
	return true
}

func (program *Program) finish(ctx context.Context, rasterResult rastercompositor.Result) (Result, error) {
	if err := program.validateRaster(ctx, rasterResult); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "color"); err != nil {
		return Result{}, err
	}
	output, err := color.ConvertSurfaceToRGBA8(ctx, rasterResult.Surface, program.config.OutputLimits)
	if err != nil {
		return Result{}, classifyColorDependency(err)
	}
	if err := program.validateOutput(rasterResult, output); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "png"); err != nil {
		return Result{}, err
	}
	pngResult, err := pngencode.Encode(ctx, output, program.config.PNGLimits)
	if err != nil {
		return Result{}, classifyPNGDependency(err)
	}
	if err := program.validatePNG(output, pngResult); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "publish"); err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: ResultSchemaVersion, Profile: ProfileV1,
		DocumentHash: program.documentHash, BuildFingerprint: program.config.BuildFingerprint,
		TimelineID: rasterResult.TimelineID, TimelineTime: rasterResult.TimelineTime,
		SourceManifest: cloneManifest(program.manifest), Raster: rasterResult, Output: output, PNG: pngResult,
	}
	if err := program.validateResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (program *Program) validateRaster(ctx context.Context, value rastercompositor.Result) error {
	config := program.config
	if value.DocumentHash != program.documentHash || value.Width != config.Width || value.Height != config.Height ||
		value.BuildFingerprint != config.BuildFingerprint || value.CompositorProfile != rastercompositor.ProfileV1 ||
		value.SourceContractVersion != rastercompositor.NativeSourceContractVersion || value.SourceProfile != rastercompositor.SourceProfileV1 ||
		value.SurfaceProfile != cpucompositor.SurfaceProfileV1 || value.RasterProfile != cpuraster.ProfileV1 ||
		value.KernelProfile != cpucompositor.KernelProfileV1 || value.LayerOrder != rastercompositor.LayerOrderProfile ||
		value.TimelineID == "" || value.TimelineTime.Validate() != nil || value.Surface == nil ||
		value.Surface.Width() != config.Width || value.Surface.Height() != config.Height || !canonicalSHA256(value.PixelHash) {
		return pipelineError(CodeStageMismatch, "raster/receipt", ErrStageMismatch)
	}
	actualHash, err := value.Surface.CanonicalHash()
	if err != nil || actualHash != value.PixelHash {
		return pipelineError(CodeStageMismatch, "raster/pixel_hash", errors.Join(ErrStageMismatch, err))
	}
	assets := make(map[documentv2.ID]nativepngsource.AssetReceipt, len(program.manifest.Assets))
	for _, asset := range program.manifest.Assets {
		assets[asset.AssetID] = asset
	}
	profiles := nativepngsource.ImageSourceProfiles()
	if len(value.Sources) > program.config.CompositorLimits.MaxSourceRequests {
		return pipelineError(CodeResourceLimit, "raster/sources", ErrResourceLimit)
	}
	for index, receipt := range value.Sources {
		path := fmt.Sprintf("raster/sources/%d", index)
		asset, exists := assets[receipt.Request.AssetID]
		request := receipt.Request
		if !exists || receipt.ContractVersion != rastercompositor.NativeSourceContractVersion || request.ContractVersion != receipt.ContractVersion ||
			request.DocumentHash != program.documentHash || request.TimelineID != value.TimelineID || request.TimelineTime != value.TimelineTime ||
			request.TimelineIndex < 0 || request.TrackIndex < 0 || request.ClipIndex < 0 || request.AssetIndex != asset.AssetIndex ||
			strings.TrimSpace(string(request.TrackID)) == "" || strings.TrimSpace(string(request.ClipID)) == "" ||
			request.AssetKind != documentv2.AssetKindImage ||
			request.AssetContentHash != asset.EncodedContentHash || receipt.AssetContentHash != asset.EncodedContentHash ||
			receipt.Width != asset.Width || receipt.Height != asset.Height || receipt.PixelHash != asset.PixelHash ||
			request.SourceProfile != rastercompositor.SourceProfileV1 || receipt.SourceProfile != rastercompositor.SourceProfileV1 ||
			request.SurfaceProfile != cpucompositor.SurfaceProfileV1 || receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
			request.DecodeProfile != profiles.DecodeProfile || receipt.DecodeProfile != profiles.DecodeProfile ||
			request.SelectionProfile != profiles.SelectionProfile || receipt.SelectionProfile != profiles.SelectionProfile ||
			request.ColorProfile != profiles.ColorProfile || receipt.ColorProfile != profiles.ColorProfile ||
			request.NormalizationProfile != profiles.NormalizationProfile || receipt.NormalizationProfile != profiles.NormalizationProfile ||
			request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil || !canonicalSHA256(receipt.PixelHash) {
			return pipelineError(CodeStageMismatch, path, ErrStageMismatch)
		}
		frame, frameErr := program.provider.Frame(ctx, request)
		if frameErr != nil {
			if errors.Is(frameErr, context.Canceled) || errors.Is(frameErr, context.DeadlineExceeded) ||
				errors.Is(frameErr, nativepngsource.ErrCanceled) {
				return pipelineError(CodeCanceled, path+"/replay", errors.Join(ErrCanceled, frameErr))
			}
			return pipelineError(CodeStageMismatch, path+"/replay", errors.Join(ErrStageMismatch, frameErr))
		}
		expected := rastercompositor.SourceReceipt{
			ContractVersion: frame.ContractVersion, Request: frame.Request,
			Width: frame.Width, Height: frame.Height, AssetContentHash: frame.AssetContentHash,
			PixelHash: frame.PixelHash, DecodeProfile: frame.DecodeProfile,
			SelectionProfile: frame.SelectionProfile, ColorProfile: frame.ColorProfile,
			NormalizationProfile: frame.NormalizationProfile, SourceProfile: frame.SourceProfile,
			SurfaceProfile: frame.SurfaceProfile,
		}
		if receipt != expected || frame.Surface == nil || frame.Surface.Width() != receipt.Width ||
			frame.Surface.Height() != receipt.Height {
			return pipelineError(CodeStageMismatch, path+"/replay", ErrStageMismatch)
		}
	}
	return nil
}

func (program *Program) validateOutput(rasterResult rastercompositor.Result, value color.OutputResult) error {
	if value.Width != program.config.Width || value.Height != program.config.Height ||
		value.Stride != program.config.Width*int(color.BytesPerOutputRGBA8Pixel) || value.Frame == nil ||
		value.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 || value.OutputProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		value.OutputMetadata != color.MetadataRGBA8SRGBOutputV1() || value.HashProfile != color.OutputRGBAHashProfileV1 ||
		!canonicalSHA256(value.PixelHash) || rasterResult.Surface == nil {
		return pipelineError(CodeStageMismatch, "color/receipt", ErrStageMismatch)
	}
	if value.Frame.Width() != value.Width || value.Frame.Height() != value.Height || value.Frame.Stride() != value.Stride {
		return pipelineError(CodeStageMismatch, "color/frame", ErrStageMismatch)
	}
	hash, err := value.Frame.PixelHash()
	if err != nil || hash != value.PixelHash {
		return pipelineError(CodeStageMismatch, "color/pixel_hash", errors.Join(ErrStageMismatch, err))
	}
	return nil
}

func (program *Program) validatePNG(output color.OutputResult, value pngencode.Result) error {
	expected, err := expectedPNGLayout(program.config.Width, program.config.Height)
	if err != nil {
		return err
	}
	if value.Profile != pngencode.ProfileV1 || value.InputColorProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		value.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 || value.InputPixelHashProfile != color.OutputRGBAHashProfileV1 ||
		value.FilterProfile != pngencode.FilterProfileV1 || value.DeflateProfile != pngencode.DeflateProfileV1 ||
		value.RoundTripDecodeProfile != pngdecode.ProfileV1 || value.ArtifactHashProfile != pngdecode.ContentHashProfileV1 ||
		value.Width != program.config.Width || value.Height != program.config.Height || value.Stride != expected.stride ||
		value.SRGBRenderingIntent != pngencode.SRGBRenderingIntent || value.RawRGBABytes != expected.raw ||
		value.ScanlineBytes != expected.scanline || value.StoredBlockCount != expected.blocks || value.IDATBytes != expected.idat ||
		value.ArtifactBytes != expected.artifact || value.WorkingBytes != expected.working || value.SourcePixelHash != output.PixelHash ||
		!canonicalSHA256(value.ArtifactHash) || value.Artifact == nil || value.Artifact.Size() != value.ArtifactBytes {
		return pipelineError(CodeStageMismatch, "png/receipt", ErrStageMismatch)
	}
	hash, hashErr := value.Artifact.CanonicalHash()
	if hashErr != nil || hash != value.ArtifactHash {
		return pipelineError(CodeStageMismatch, "png/artifact_hash", errors.Join(ErrStageMismatch, hashErr))
	}
	return nil
}

func (program *Program) validateResult(value Result) error {
	if value.SchemaVersion != ResultSchemaVersion || value.Profile != ProfileV1 || value.DocumentHash != program.documentHash ||
		value.BuildFingerprint != program.config.BuildFingerprint || value.TimelineID != value.Raster.TimelineID ||
		value.TimelineTime != value.Raster.TimelineTime || value.SourceManifest.DocumentHash != program.documentHash ||
		value.Raster.PixelHash == "" || value.Output.PixelHash != value.PNG.SourcePixelHash {
		return pipelineError(CodeStageMismatch, "result", ErrStageMismatch)
	}
	if !equalManifest(value.SourceManifest, program.manifest) {
		return pipelineError(CodeStageMismatch, "result/source_manifest", ErrStageMismatch)
	}
	return nil
}

type pngLayout struct {
	stride                                         int
	raw, scanline, blocks, idat, artifact, working uint64
}

func expectedPNGLayout(width, height int) (pngLayout, error) {
	if width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 || width > maxInt()/4 {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	w, h := uint64(width), uint64(height)
	if h != 0 && w > math.MaxUint64/h {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	raw := w * h * 4
	if raw > math.MaxUint64-h {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	scanline := raw + h
	blocks := scanline / pngencode.StoredBlockMaxBytes
	if scanline%pngencode.StoredBlockMaxBytes != 0 {
		blocks++
	}
	if scanline > math.MaxUint64-pngencode.ZlibFixedBytes || blocks > (math.MaxUint64-pngencode.ZlibFixedBytes-scanline)/5 {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	idat := pngencode.ZlibFixedBytes + scanline + blocks*5
	if idat > math.MaxUint64-pngencode.FixedArtifactBytes {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	artifact := idat + pngencode.FixedArtifactBytes
	stride := width * 4
	if artifact > math.MaxUint64-uint64(stride) {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	return pngLayout{stride: stride, raw: raw, scanline: scanline, blocks: blocks, idat: idat, artifact: artifact, working: artifact + uint64(stride)}, nil
}

func classifyRenderDependency(path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, rastercompositor.ErrCanceled) {
		return pipelineError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, rastercompositor.ErrResourceLimit) || errors.Is(err, rastercompositor.ErrSourceLimit) {
		return pipelineError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	}
	return pipelineError(CodeRenderFailed, path, errors.Join(ErrRenderFailed, err))
}

func classifyColorDependency(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, color.ErrCanceled) {
		return pipelineError(CodeCanceled, "color", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, color.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "color", errors.Join(ErrResourceLimit, err))
	}
	return pipelineError(CodeColorFailed, "color", errors.Join(ErrColorFailed, err))
}

func classifyPNGDependency(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, pngencode.ErrCanceled) {
		return pipelineError(CodeCanceled, "png", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, pngencode.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "png", errors.Join(ErrResourceLimit, err))
	}
	return pipelineError(CodePNGFailed, "png", errors.Join(ErrPNGFailed, err))
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func equalManifest(left, right nativepngsource.Manifest) bool {
	if left.SchemaVersion != right.SchemaVersion || left.Profile != right.Profile || left.DocumentHash != right.DocumentHash ||
		left.SourceContractVersion != right.SourceContractVersion || left.SourceProfile != right.SourceProfile ||
		left.SurfaceProfile != right.SurfaceProfile || len(left.Assets) != len(right.Assets) {
		return false
	}
	for index := range left.Assets {
		if left.Assets[index] != right.Assets[index] {
			return false
		}
	}
	return true
}
