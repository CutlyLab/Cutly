package referencevisual

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const StageBindingProfileV1 = "cutly.reference-visual.stage-binding.v1"

// StageBinding is the wrapper's live in-process handoff receipt. It records identity continuity
// across stages whose dependency receipts do not themselves carry their input identity. Like all
// serialized receipts in this package it is structural evidence, not offline pixel provenance.
type StageBinding struct {
	Profile            string `json:"profile"`
	VisualPixelHash    string `json:"visual_pixel_hash"`
	OutputPixelHash    string `json:"output_pixel_hash"`
	PNGSourcePixelHash string `json:"png_source_pixel_hash"`
	PNGArtifactHash    string `json:"png_artifact_hash"`
}

// Result is the complete unsigned receipt for one v1 image/vector or v2 image/vector/Text PNG
// export. Runtime-owned Surface, Frame and Artifact values remain immutable and are omitted by
// dependency JSON tags.
type Result struct {
	SchemaVersion    int                     `json:"schema_version"`
	Profile          string                  `json:"profile"`
	DocumentHash     string                  `json:"document_hash"`
	BuildFingerprint string                  `json:"build_fingerprint,omitempty"`
	TimelineID       documentv2.ID           `json:"timeline_id"`
	TimelineTime     documentv2.RationalTime `json:"timeline_time"`
	SourceManifest   SourceManifest          `json:"source_manifest"`
	Visual           visualcompositor.Result `json:"visual"`
	Output           color.OutputResult      `json:"output"`
	PNG              pngencode.Result        `json:"png"`
	StageBinding     StageBinding            `json:"stage_binding"`
}

type FrameResult struct {
	FrameClock            videoedit.FrameClock `json:"frame_clock"`
	CompositionFrameIndex int64                `json:"composition_frame_index"`
	Result                Result               `json:"result"`
}

// SurfaceFrameResult is the pre-color, pre-PNG result for one exact composition frame. Result
// remains in the canonical linear-BT.709 premultiplied RGBA16 surface domain and carries the same
// fully replayed source receipt that a complete RenderFrame export would consume.
type SurfaceFrameResult struct {
	FrameClock            videoedit.FrameClock    `json:"frame_clock"`
	CompositionFrameIndex int64                   `json:"composition_frame_index"`
	Result                visualcompositor.Result `json:"result"`
}

func (program *Program) RenderAt(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime) (result Result, err error) {
	defer program.recoverDomainPanic(&err, func() { result = Result{} })
	visual, err := program.RenderSurfaceAt(ctx, timelineID, at)
	if err != nil {
		return Result{}, err
	}
	return program.finishValidated(ctx, visual)
}

// RenderSurfaceAt renders and strictly validates one canonical visual surface without performing
// the later RGBA8 conversion or PNG encoding stages. It is the embedding boundary for pre-codec
// surface chunk providers. Failure and cancellation return a zero result.
func (program *Program) RenderSurfaceAt(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime) (result visualcompositor.Result, err error) {
	defer program.recoverDomainPanic(&err, func() { result = visualcompositor.Result{} })
	if err := program.validateRuntime(ctx); err != nil {
		return visualcompositor.Result{}, err
	}
	visual, err := program.visual.RenderAt(ctx, timelineID, at, program.providers())
	if err != nil {
		return visualcompositor.Result{}, classifyRenderDependency("visual", err)
	}
	if visual.TimelineID != timelineID || visual.TimelineTime != at {
		return visualcompositor.Result{}, pipelineError(CodeStageMismatch, "visual/request", ErrStageMismatch)
	}
	if err := program.validateVisualLive(ctx, visual); err != nil {
		return visualcompositor.Result{}, err
	}
	if err := contextError(ctx, "visual/publish"); err != nil {
		return visualcompositor.Result{}, err
	}
	return visual, nil
}

func (program *Program) RenderFrame(ctx context.Context, timelineID documentv2.ID, clock videoedit.FrameClock, compositionFrameIndex int64) (frameResult FrameResult, err error) {
	defer program.recoverDomainPanic(&err, func() { frameResult = FrameResult{} })
	visualFrame, err := program.RenderSurfaceFrame(ctx, timelineID, clock, compositionFrameIndex)
	if err != nil {
		return FrameResult{}, err
	}
	result, err := program.finishValidated(ctx, visualFrame.Result)
	if err != nil {
		return FrameResult{}, err
	}
	if err := contextError(ctx, "frame/publish"); err != nil {
		return FrameResult{}, err
	}
	return FrameResult{FrameClock: clock, CompositionFrameIndex: compositionFrameIndex, Result: result}, nil
}

// RenderSurfaceFrame resolves one composition-wide frame index, renders its canonical visual
// surface and repeats the complete live provider/receipt/hash validation. It performs no output
// color conversion or image encoding. Failure and cancellation return a zero result.
func (program *Program) RenderSurfaceFrame(ctx context.Context, timelineID documentv2.ID, clock videoedit.FrameClock, compositionFrameIndex int64) (result SurfaceFrameResult, err error) {
	defer program.recoverDomainPanic(&err, func() { result = SurfaceFrameResult{} })
	if err := program.validateRuntime(ctx); err != nil {
		return SurfaceFrameResult{}, err
	}
	expectedTime, err := videoedit.FrameTimeAt(clock, compositionFrameIndex)
	if err != nil {
		return SurfaceFrameResult{}, pipelineError(CodeRenderFailed, "frame/clock", errors.Join(ErrRenderFailed, err))
	}
	visualFrame, err := program.visual.RenderFrame(ctx, timelineID, clock, compositionFrameIndex, program.providers())
	if err != nil {
		return SurfaceFrameResult{}, classifyRenderDependency("visual", err)
	}
	if visualFrame.CompositionFrameIndex != compositionFrameIndex || visualFrame.Result.TimelineID != timelineID ||
		visualFrame.Result.TimelineTime != expectedTime {
		return SurfaceFrameResult{}, pipelineError(CodeStageMismatch, "visual/frame", ErrStageMismatch)
	}
	if err := program.validateVisualLive(ctx, visualFrame.Result); err != nil {
		return SurfaceFrameResult{}, err
	}
	if err := contextError(ctx, "visual/frame/publish"); err != nil {
		return SurfaceFrameResult{}, err
	}
	return SurfaceFrameResult{FrameClock: clock, CompositionFrameIndex: compositionFrameIndex, Result: visualFrame.Result}, nil
}

func (program *Program) providers() visualcompositor.Providers {
	return visualcompositor.Providers{
		Native: program.native, Expression: program.expression, ExpressionHandle: program.expression, Text: program.text,
	}
}

func (program *Program) validateRuntime(ctx context.Context) error {
	if program == nil || program.visual == nil || program.native == nil || program.expression == nil ||
		program.documentHash == "" || program.manifest.DocumentHash != program.documentHash {
		return pipelineError(CodeProgramUnavailable, "program", ErrProgramUnavailable)
	}
	if (program.v2 && program.text == nil) || (!program.v2 && program.text != nil) {
		return pipelineError(CodeProgramUnavailable, "program/text", ErrProgramUnavailable)
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
	return contextError(ctx, "visual/publish")
}

func (program *Program) runtimeInvariant() bool {
	normalized, expectedVisual, err := normalizeConfigMode(program.config, program.v2)
	if err != nil || !equalConfig(normalized, program.config) || program.visual.DocumentHash() != program.documentHash ||
		program.visual.Config() != expectedVisual || program.native.DocumentHash() != program.documentHash ||
		program.expression.DocumentHash() != program.documentHash || program.native.Config().Limits != program.config.NativeLimits ||
		program.expression.Config().Limits != program.config.ExpressionLimits {
		return false
	}
	nativeManifest := program.native.Manifest()
	expressionManifest := program.expression.Manifest()
	if !equalNativeManifest(nativeManifest, program.manifest.NativePNG) ||
		!equalExpressionManifest(expressionManifest, program.manifest.Expression) ||
		!validCombinedManifestMode(program.manifest, program.config, program.v2) {
		return false
	}
	if program.v2 {
		if program.config.TextConfig == nil || program.manifest.Text == nil || program.text.DocumentHash() != program.documentHash ||
			program.text.Config() != *program.config.TextConfig || !equalTextManifest(program.text.Manifest(), *program.manifest.Text) {
			return false
		}
	}
	if program.domainScope != nil {
		binding := program.visual.ScopeBinding()
		if !program.v2 || program.manifest.DomainScope == nil || !equalScopeBindings(&binding, program.manifest.DomainScope) || !equalDomainScopes(program.visual.Scope(), *program.domainScope) {
			return false
		}
	} else if program.manifest.DomainScope != nil || program.visual.DomainScopeHash() != "" {
		return false
	}
	return true
}

func (program *Program) profile() string {
	if program != nil && program.domainScope != nil {
		return DomainProfileV1
	}
	if program != nil && program.v2 {
		return ProfileV2
	}
	return ProfileV1
}

func (program *Program) finishValidated(ctx context.Context, visual visualcompositor.Result) (Result, error) {
	if err := contextError(ctx, "color"); err != nil {
		return Result{}, err
	}
	output, err := color.ConvertSurfaceToRGBA8(ctx, visual.Surface, program.config.OutputLimits)
	if err != nil {
		return Result{}, classifyColorDependency(err)
	}
	if err := program.validateOutput(visual, output); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "png"); err != nil {
		return Result{}, err
	}
	png, err := pngencode.Encode(ctx, output, program.config.PNGLimits)
	if err != nil {
		return Result{}, classifyPNGDependency(err)
	}
	if err := program.validatePNG(output, png); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "png/validate"); err != nil {
		return Result{}, err
	}
	binding := StageBinding{
		Profile: StageBindingProfileV1, VisualPixelHash: visual.PixelHash, OutputPixelHash: output.PixelHash,
		PNGSourcePixelHash: png.SourcePixelHash, PNGArtifactHash: png.ArtifactHash,
	}
	if err := contextError(ctx, "publish"); err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: ResultSchemaVersion, Profile: program.profile(), DocumentHash: program.documentHash,
		BuildFingerprint: program.config.BuildFingerprint, TimelineID: visual.TimelineID, TimelineTime: visual.TimelineTime,
		SourceManifest: cloneSourceManifest(program.manifest), Visual: visual, Output: output, PNG: png, StageBinding: binding,
	}
	if err := program.validateResult(result); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "publish"); err != nil {
		return Result{}, err
	}
	return result, nil
}

// ValidateResult is the strict live boundary. It requires every runtime object, re-renders from
// the Program's concrete providers, then independently repeats color conversion and PNG encoding.
func (program *Program) ValidateResult(ctx context.Context, value Result) (err error) {
	defer program.recoverDomainPanic(&err, nil)
	if err := program.validateRuntime(ctx); err != nil {
		return err
	}
	if err := program.validateResult(value); err != nil {
		return err
	}
	if err := program.validateVisualLive(ctx, value.Visual); err != nil {
		return err
	}
	if err := program.validateOutput(value.Visual, value.Output); err != nil {
		return err
	}
	if err := program.validatePNG(value.Output, value.PNG); err != nil {
		return err
	}
	trustedVisual, err := program.visual.RenderAt(ctx, value.TimelineID, value.TimelineTime, program.providers())
	if err != nil {
		return classifyRenderDependency("validate/visual", err)
	}
	if !equalVisualReceipt(trustedVisual, value.Visual) {
		return pipelineError(CodeStageMismatch, "validate/visual", ErrStageMismatch)
	}
	trustedOutput, err := color.ConvertSurfaceToRGBA8(ctx, trustedVisual.Surface, program.config.OutputLimits)
	if err != nil {
		return classifyColorDependency(err)
	}
	if !equalOutputReceipt(trustedOutput, value.Output) {
		return pipelineError(CodeStageMismatch, "validate/color", ErrStageMismatch)
	}
	trustedPNG, err := pngencode.Encode(ctx, trustedOutput, program.config.PNGLimits)
	if err != nil {
		return classifyPNGDependency(err)
	}
	if !equalPNGReceipt(trustedPNG, value.PNG) {
		return pipelineError(CodeStageMismatch, "validate/png", ErrStageMismatch)
	}
	// Both immutable artifacts were independently re-hashed by validatePNG, and their canonical
	// SHA-256 receipts match. Avoid allocating two artifact-sized defensive copies here.
	return contextError(ctx, "validate/publish")
}

// ValidateSerializedReceipt validates frozen scope, profiles, geometry and hash-chain claims while
// accepting omitted Surface/Frame/Artifact runtime objects. It intentionally does not claim that
// serialized hashes prove pixel causality or provenance.
func (program *Program) ValidateSerializedReceipt(ctx context.Context, value Result) (err error) {
	defer program.recoverDomainPanic(&err, nil)
	if err := program.validateRuntime(ctx); err != nil {
		return err
	}
	if err := program.validateResult(value); err != nil {
		return err
	}
	if err := program.visual.ValidateSerializedReceipt(ctx, value.Visual); err != nil {
		if canceled := contextError(ctx, "serialized/visual"); canceled != nil {
			return canceled
		}
		return pipelineError(CodeStageMismatch, "serialized/visual", errors.Join(ErrStageMismatch, err))
	}
	if err := program.validateSerializedSourcesAgainstManifest(value.Visual.Sources); err != nil {
		return err
	}
	if err := program.validateSerializedOutput(value.Output); err != nil {
		return err
	}
	if err := program.validateSerializedPNG(value.Output, value.PNG); err != nil {
		return err
	}
	return contextError(ctx, "serialized/publish")
}

func (program *Program) validateSerializedSourcesAgainstManifest(sources []visualcompositor.SourceReceipt) error {
	nativeAssets := make(map[documentv2.ID]nativepngsource.AssetReceipt, len(program.manifest.NativePNG.Assets))
	for _, asset := range program.manifest.NativePNG.Assets {
		nativeAssets[asset.AssetID] = asset
	}
	expressionAssets := make(map[documentv2.ID]expressionlayer.AssetReceipt, len(program.manifest.Expression.Assets))
	for _, asset := range program.manifest.Expression.Assets {
		expressionAssets[asset.AssetID] = asset
	}
	textAssets := make(map[documentv2.ID]textlayer.AssetReceipt)
	if program.manifest.Text != nil {
		textAssets = make(map[documentv2.ID]textlayer.AssetReceipt, len(program.manifest.Text.Assets))
		for _, asset := range program.manifest.Text.Assets {
			textAssets[asset.AssetID] = asset
		}
	}
	profiles := nativepngsource.ImageSourceProfiles()
	for index, source := range sources {
		path := fmt.Sprintf("serialized/visual/sources/%d", index)
		switch source.Kind {
		case visualcompositor.SourceKindNative:
			if source.Native == nil {
				return pipelineError(CodeStageMismatch, path, ErrStageMismatch)
			}
			receipt := *source.Native
			asset, ok := nativeAssets[receipt.Request.AssetID]
			if !ok || receipt.Request.AssetIndex != asset.AssetIndex || receipt.Request.AssetKind != documentv2.AssetKindImage ||
				receipt.Request.AssetContentHash != asset.EncodedContentHash || receipt.AssetContentHash != asset.EncodedContentHash ||
				receipt.Width != asset.Width || receipt.Height != asset.Height || receipt.PixelHash != asset.PixelHash ||
				receipt.Request.DecodeProfile != profiles.DecodeProfile || receipt.DecodeProfile != profiles.DecodeProfile ||
				receipt.Request.SelectionProfile != profiles.SelectionProfile || receipt.SelectionProfile != profiles.SelectionProfile ||
				receipt.Request.ColorProfile != profiles.ColorProfile || receipt.ColorProfile != profiles.ColorProfile ||
				receipt.Request.NormalizationProfile != profiles.NormalizationProfile || receipt.NormalizationProfile != profiles.NormalizationProfile ||
				receipt.Request.SourceProfile != rastercompositor.SourceProfileV1 || receipt.SourceProfile != rastercompositor.SourceProfileV1 ||
				receipt.Request.SurfaceProfile != cpucompositor.SurfaceProfileV1 || receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
				return pipelineError(CodeStageMismatch, path+"/native_manifest", ErrStageMismatch)
			}
		case visualcompositor.SourceKindExpression:
			if source.Expression == nil {
				return pipelineError(CodeStageMismatch, path, ErrStageMismatch)
			}
			receipt := source.Expression
			var assetID documentv2.ID
			var assetIndex int
			var semanticHash string
			var viewport documentv2.RasterViewport
			if receipt.VisibleRequest != nil {
				assetID, assetIndex = receipt.VisibleRequest.AssetID, receipt.VisibleRequest.AssetIndex
				semanticHash, viewport = receipt.VisibleRequest.AssetSemanticHash, receipt.VisibleRequest.RasterViewport
			} else if receipt.TransitionRequest != nil {
				assetID, assetIndex = receipt.TransitionRequest.AssetID, receipt.TransitionRequest.AssetIndex
				semanticHash, viewport = receipt.TransitionRequest.AssetSemanticHash, receipt.TransitionRequest.RasterViewport
			} else {
				return pipelineError(CodeStageMismatch, path+"/expression_request", ErrStageMismatch)
			}
			asset, ok := expressionAssets[assetID]
			if !ok || assetIndex != asset.AssetIndex || semanticHash != asset.AssetSemanticHash || viewport != asset.RasterViewport ||
				receipt.AssetSemanticHashProfile != asset.AssetSemanticHashProfile || receipt.AssetSemanticHash != asset.AssetSemanticHash ||
				receipt.RasterReceipt != asset.RasterReceipt || receipt.SurfaceIdentity != asset.SurfaceIdentity {
				return pipelineError(CodeStageMismatch, path+"/expression_manifest", ErrStageMismatch)
			}
		case visualcompositor.SourceKindText:
			if !program.v2 || program.manifest.Text == nil || source.Text == nil {
				return pipelineError(CodeStageMismatch, path+"/text", ErrStageMismatch)
			}
			receipt := source.Text
			request := receipt.Request
			asset, ok := textAssets[request.AssetID]
			if !ok || receipt.ContractVersion != textlayer.ContractVersion || request.ContractVersion != textlayer.ContractVersion ||
				request.DocumentHash != program.documentHash || request.AssetIndex != asset.AssetIndex || request.AssetKind != documentv2.AssetKindText ||
				request.FrozenTextBindingHashProfile != asset.FrozenTextBindingHashProfile || request.FrozenTextBindingHash != asset.FrozenTextBindingHash ||
				request.TextSemanticHashProfile != asset.TextSemanticHashProfile || request.TextSemanticHash != asset.TextSemanticHash ||
				request.LayoutInputHashProfile != asset.LayoutInputHashProfile || request.LayoutInputHash != asset.LayoutInputHash ||
				request.FontAssetID != asset.FontAssetID || request.FontAssetIndex != asset.FontAssetIndex ||
				request.FontContentHashProfile != asset.FontContentHashProfile || request.FontContentHash != asset.FontContentHash ||
				request.RasterViewport != asset.RasterViewport || request.SourceProfile != program.manifest.Text.SourceProfile ||
				request.SelectionProfile != program.manifest.Text.SelectionProfile || request.TextProfile != program.manifest.Text.TextProfile ||
				request.EvaluatorProfile != program.manifest.Text.EvaluatorProfile || request.RasterProfile != program.manifest.Text.RasterProfile ||
				request.PaintProfile != program.manifest.Text.PaintProfile || request.SurfaceProfile != program.manifest.Text.SurfaceProfile ||
				receipt.Assembly != asset.Assembly || receipt.Raster != asset.Raster || receipt.SurfaceIdentity != asset.SurfaceIdentity {
				return pipelineError(CodeStageMismatch, path+"/text_manifest", ErrStageMismatch)
			}
		default:
			return pipelineError(CodeStageMismatch, path+"/kind", ErrStageMismatch)
		}
	}
	return nil
}

func equalVisualReceipt(left, right visualcompositor.Result) bool {
	left.Surface, right.Surface = nil, nil
	return reflect.DeepEqual(left, right)
}

func equalOutputReceipt(left, right color.OutputResult) bool {
	left.Frame, right.Frame = nil, nil
	return left == right
}

func equalPNGReceipt(left, right pngencode.Result) bool {
	left.Artifact, right.Artifact = nil, nil
	return left == right
}

func (program *Program) validateVisualLive(ctx context.Context, value visualcompositor.Result) error {
	if value.DocumentHash != program.documentHash || value.Width != program.config.Width || value.Height != program.config.Height ||
		value.BuildFingerprint != program.config.BuildFingerprint || value.TimelineID == "" || value.TimelineTime.Validate() != nil ||
		value.Surface == nil || value.Surface.Width() != program.config.Width || value.Surface.Height() != program.config.Height ||
		len(value.Sources) > program.config.CompositorLimits.MaxSourceRequests {
		return pipelineError(CodeStageMismatch, "visual/receipt", ErrStageMismatch)
	}
	if err := program.visual.ValidateResult(ctx, value); err != nil {
		if canceled := contextError(ctx, "visual/validate"); canceled != nil {
			return canceled
		}
		return pipelineError(CodeStageMismatch, "visual/validate", errors.Join(ErrStageMismatch, err))
	}
	if err := contextError(ctx, "visual/pixel_hash"); err != nil {
		return err
	}
	actualHash, err := value.Surface.CanonicalHashContext(ctx)
	if err != nil {
		if canceled := contextError(ctx, "visual/pixel_hash"); canceled != nil {
			return canceled
		}
	}
	if err != nil || actualHash != value.PixelHash {
		return pipelineError(CodeStageMismatch, "visual/pixel_hash", errors.Join(ErrStageMismatch, err))
	}
	if err := contextError(ctx, "visual/pixel_hash"); err != nil {
		return err
	}
	for index, receipt := range value.Sources {
		if err := contextError(ctx, fmt.Sprintf("visual/sources/%d", index)); err != nil {
			return err
		}
		if err := program.replaySource(ctx, receipt, fmt.Sprintf("visual/sources/%d", index)); err != nil {
			return err
		}
	}
	return contextError(ctx, "visual/publish")
}

func (program *Program) replaySource(ctx context.Context, receipt visualcompositor.SourceReceipt, path string) error {
	if err := receipt.Validate(); err != nil {
		return pipelineError(CodeStageMismatch, path+"/receipt", errors.Join(ErrStageMismatch, err))
	}
	switch receipt.Kind {
	case visualcompositor.SourceKindNative:
		frame, err := program.native.Frame(ctx, receipt.Native.Request)
		if err != nil {
			return classifyReplay(path, err)
		}
		expected := rastercompositor.SourceReceipt{
			ContractVersion: frame.ContractVersion, Request: frame.Request, Width: frame.Width, Height: frame.Height,
			AssetContentHash: frame.AssetContentHash, PixelHash: frame.PixelHash, DecodeProfile: frame.DecodeProfile,
			SelectionProfile: frame.SelectionProfile, ColorProfile: frame.ColorProfile,
			NormalizationProfile: frame.NormalizationProfile, SourceProfile: frame.SourceProfile, SurfaceProfile: frame.SurfaceProfile,
		}
		if *receipt.Native != expected {
			return pipelineError(CodeStageMismatch, path+"/native/replay", ErrStageMismatch)
		}
		return validateReplaySurface(ctx, frame.Surface, frame.Width, frame.Height, frame.PixelHash, path+"/native/surface")
	case visualcompositor.SourceKindExpression:
		expression := receipt.Expression
		if expression.VisibleRequest != nil {
			frame, err := program.expression.Frame(ctx, *expression.VisibleRequest)
			if err != nil {
				return classifyReplay(path, err)
			}
			if frame.ContractVersion != expression.ContractVersion || frame.Request != *expression.VisibleRequest ||
				frame.AssetSemanticHashProfile != expression.AssetSemanticHashProfile || frame.AssetSemanticHash != expression.AssetSemanticHash ||
				frame.RasterReceipt != expression.RasterReceipt || frame.SurfaceIdentity != expression.SurfaceIdentity {
				return pipelineError(CodeStageMismatch, path+"/expression/replay", ErrStageMismatch)
			}
			return validateReplaySurface(ctx, frame.Surface, int(frame.SurfaceIdentity.WidthPixels), int(frame.SurfaceIdentity.HeightPixels), frame.SurfaceIdentity.PixelHash, path+"/expression/surface")
		}
		frame, err := program.expression.TransitionHandle(ctx, *expression.TransitionRequest)
		if err != nil {
			return classifyReplay(path, err)
		}
		if frame.ContractVersion != expression.ContractVersion || frame.Request != *expression.TransitionRequest ||
			frame.AssetSemanticHashProfile != expression.AssetSemanticHashProfile || frame.AssetSemanticHash != expression.AssetSemanticHash ||
			frame.RasterReceipt != expression.RasterReceipt || frame.SurfaceIdentity != expression.SurfaceIdentity {
			return pipelineError(CodeStageMismatch, path+"/expression_handle/replay", ErrStageMismatch)
		}
		return validateReplaySurface(ctx, frame.Surface, int(frame.SurfaceIdentity.WidthPixels), int(frame.SurfaceIdentity.HeightPixels), frame.SurfaceIdentity.PixelHash, path+"/expression_handle/surface")
	case visualcompositor.SourceKindText:
		if !program.v2 || program.text == nil || receipt.Text == nil {
			return pipelineError(CodeStageMismatch, path+"/text", ErrStageMismatch)
		}
		frame, err := program.text.Frame(ctx, receipt.Text.Request)
		if err != nil {
			return classifyReplay(path, err)
		}
		if frame.Receipt != *receipt.Text {
			return pipelineError(CodeStageMismatch, path+"/text/replay", ErrStageMismatch)
		}
		identity := frame.Receipt.SurfaceIdentity
		return validateReplaySurface(ctx, frame.Surface, int(identity.WidthPixels), int(identity.HeightPixels), identity.PixelHash, path+"/text/surface")
	default:
		return pipelineError(CodeStageMismatch, path+"/kind", ErrStageMismatch)
	}
}

func validateReplaySurface(ctx context.Context, surface *cpucompositor.Surface, width, height int, expectedHash, path string) error {
	if surface == nil || width <= 0 || height <= 0 || surface.Width() != width || surface.Height() != height || !canonicalSHA256(expectedHash) {
		return pipelineError(CodeStageMismatch, path, ErrStageMismatch)
	}
	if err := contextError(ctx, path+"/pixel_hash"); err != nil {
		return err
	}
	hash, err := surface.CanonicalHashContext(ctx)
	if err != nil {
		if canceled := contextError(ctx, path+"/pixel_hash"); canceled != nil {
			return canceled
		}
	}
	if err != nil || hash != expectedHash {
		return pipelineError(CodeStageMismatch, path+"/pixel_hash", errors.Join(ErrStageMismatch, err))
	}
	return contextError(ctx, path+"/pixel_hash")
}

func (program *Program) validateOutput(visual visualcompositor.Result, value color.OutputResult) error {
	if value.Width != program.config.Width || value.Height != program.config.Height ||
		value.Stride != program.config.Width*int(color.BytesPerOutputRGBA8Pixel) || value.Frame == nil || visual.Surface == nil ||
		value.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 || value.OutputProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		value.OutputMetadata != color.MetadataRGBA8SRGBOutputV1() || value.HashProfile != color.OutputRGBAHashProfileV1 ||
		!canonicalSHA256(value.PixelHash) {
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

func (program *Program) validateSerializedOutput(value color.OutputResult) error {
	if value.Width != program.config.Width || value.Height != program.config.Height ||
		value.Stride != program.config.Width*int(color.BytesPerOutputRGBA8Pixel) ||
		value.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 || value.OutputProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		value.OutputMetadata != color.MetadataRGBA8SRGBOutputV1() || value.HashProfile != color.OutputRGBAHashProfileV1 ||
		!canonicalSHA256(value.PixelHash) {
		return pipelineError(CodeStageMismatch, "serialized/color", ErrStageMismatch)
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

func (program *Program) validateSerializedPNG(output color.OutputResult, value pngencode.Result) error {
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
		!canonicalSHA256(value.SourcePixelHash) || !canonicalSHA256(value.ArtifactHash) {
		return pipelineError(CodeStageMismatch, "serialized/png", ErrStageMismatch)
	}
	return nil
}

func (program *Program) validateResult(value Result) error {
	if value.SchemaVersion != ResultSchemaVersion || value.Profile != program.profile() || value.DocumentHash != program.documentHash ||
		value.BuildFingerprint != program.config.BuildFingerprint || value.TimelineID != value.Visual.TimelineID ||
		value.TimelineTime != value.Visual.TimelineTime || value.SourceManifest.DocumentHash != program.documentHash ||
		value.StageBinding.Profile != StageBindingProfileV1 || value.StageBinding.VisualPixelHash != value.Visual.PixelHash ||
		value.StageBinding.OutputPixelHash != value.Output.PixelHash || value.StageBinding.PNGSourcePixelHash != value.Output.PixelHash ||
		value.StageBinding.PNGSourcePixelHash != value.PNG.SourcePixelHash || value.StageBinding.PNGArtifactHash != value.PNG.ArtifactHash ||
		!canonicalSHA256(value.StageBinding.VisualPixelHash) || !canonicalSHA256(value.StageBinding.OutputPixelHash) ||
		!canonicalSHA256(value.StageBinding.PNGArtifactHash) || !equalSourceManifest(value.SourceManifest, program.manifest) {
		return pipelineError(CodeStageMismatch, "result", ErrStageMismatch)
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
	_, raw, artifact, row, ok := configuredRuntimeSizes(Config{Width: width, Height: height})
	if !ok {
		return pngLayout{}, pipelineError(CodeStageMismatch, "png/layout", ErrStageMismatch)
	}
	h := uint64(height)
	scanline := raw + h
	blocks := scanline / pngencode.StoredBlockMaxBytes
	if scanline%pngencode.StoredBlockMaxBytes != 0 {
		blocks++
	}
	idat := artifact - pngencode.FixedArtifactBytes
	return pngLayout{stride: int(row), raw: raw, scanline: scanline, blocks: blocks, idat: idat, artifact: artifact, working: artifact + row}, nil
}

func classifyRenderDependency(path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, visualcompositor.ErrCanceled) {
		return pipelineError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, visualcompositor.ErrResourceLimit) || errors.Is(err, visualcompositor.ErrSourceLimit) {
		return pipelineError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	}
	return pipelineError(CodeRenderFailed, path, errors.Join(ErrRenderFailed, err))
}

func classifyReplay(path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nativepngsource.ErrCanceled) || errors.Is(err, expressionlayer.ErrCanceled) || errors.Is(err, textlayer.ErrCanceled) {
		return pipelineError(CodeCanceled, path+"/replay", errors.Join(ErrCanceled, err))
	}
	return pipelineError(CodeStageMismatch, path+"/replay", errors.Join(ErrStageMismatch, err))
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
