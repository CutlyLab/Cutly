package referencevisual

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

// ResultV3 is a separate wire contract; serialized hashes are unsigned claims.
type ResultV3 struct {
	SchemaVersion    int                     `json:"schema_version"`
	Profile          string                  `json:"profile"`
	DocumentHash     string                  `json:"document_hash"`
	BuildFingerprint string                  `json:"build_fingerprint,omitempty"`
	TimelineID       documentv2.ID           `json:"timeline_id"`
	TimelineTime     documentv2.RationalTime `json:"timeline_time"`
	SourceManifest   SourceManifestV3        `json:"source_manifest"`
	Visual           visualcompositor.Result `json:"visual"`
	Output           color.OutputResult      `json:"output"`
	PNG              pngencode.Result        `json:"png"`
	StageBinding     StageBinding            `json:"stage_binding"`
}

type FrameResultV3 struct {
	FrameClock            videoedit.FrameClock `json:"frame_clock"`
	CompositionFrameIndex int64                `json:"composition_frame_index"`
	Result                ResultV3             `json:"result"`
}

func (p *ProgramV3) validateRuntime(ctx context.Context) error {
	if err := contextError(ctx, "v3/runtime"); err != nil {
		return err
	}
	if p == nil || p.visual == nil || p.native == nil || p.video == nil || p.expression == nil || p.text == nil {
		return pipelineError(CodeProgramUnavailable, "v3/program", ErrProgramUnavailable)
	}
	normalized, visual, err := normalizeConfigV3(p.config)
	if err != nil || !reflect.DeepEqual(normalized, p.config) || p.visual.Config() != visual || p.visual.DocumentHash() != p.DocumentHash() || p.video.DocumentHash() != p.DocumentHash() || p.native.DocumentHash() != p.DocumentHash() || p.expression.DocumentHash() != p.DocumentHash() || p.text.DocumentHash() != p.DocumentHash() || p.manifest.Profile != DomainManifestProfileV3 || p.manifest.SchemaVersion != 3 || !reflect.DeepEqual(p.manifest.DomainScope, p.visual.ScopeBinding()) || !validCombinedManifestMode(p.manifest.Static, p.config.Static, true) {
		return pipelineError(CodeProgramUnavailable, "v3/invariant", ErrProgramUnavailable)
	}
	return nil
}

func (p *ProgramV3) RenderSurfaceAt(ctx context.Context, timeline documentv2.ID, at documentv2.RationalTime) (result visualcompositor.Result, err error) {
	defer recoverV3(&err, func() { result = visualcompositor.Result{} })
	if err = p.validateRuntime(ctx); err != nil {
		return visualcompositor.Result{}, err
	}
	value, err := p.visual.RenderAt(ctx, timeline, at, p.providers())
	if err != nil {
		return visualcompositor.Result{}, classifyRenderDependency("v3/visual", err)
	}
	if value.TimelineID != timeline || value.TimelineTime != at {
		return visualcompositor.Result{}, pipelineError(CodeStageMismatch, "v3/request", ErrStageMismatch)
	}
	if err = p.validateSources(value.Sources); err != nil {
		return visualcompositor.Result{}, err
	}
	if err = contextError(ctx, "v3/surface/publish"); err != nil {
		return visualcompositor.Result{}, err
	}
	return value, nil
}

func (p *ProgramV3) RenderSurfaceFrame(ctx context.Context, timeline documentv2.ID, clock videoedit.FrameClock, index int64) (result SurfaceFrameResult, err error) {
	defer recoverV3(&err, func() { result = SurfaceFrameResult{} })
	if err = p.validateRuntime(ctx); err != nil {
		return SurfaceFrameResult{}, err
	}
	at, err := videoedit.FrameTimeAt(clock, index)
	if err != nil {
		return SurfaceFrameResult{}, pipelineError(CodeRenderFailed, "v3/frame/clock", errors.Join(ErrRenderFailed, err))
	}
	surface, err := p.RenderSurfaceAt(ctx, timeline, at)
	if err != nil {
		return SurfaceFrameResult{}, err
	}
	return SurfaceFrameResult{FrameClock: clock, CompositionFrameIndex: index, Result: surface}, nil
}

func (p *ProgramV3) RenderAt(ctx context.Context, timeline documentv2.ID, at documentv2.RationalTime) (result ResultV3, err error) {
	defer recoverV3(&err, func() { result = ResultV3{} })
	surface, err := p.RenderSurfaceAt(ctx, timeline, at)
	if err != nil {
		return ResultV3{}, err
	}
	return p.finish(ctx, surface)
}

func (p *ProgramV3) RenderFrame(ctx context.Context, timeline documentv2.ID, clock videoedit.FrameClock, index int64) (result FrameResultV3, err error) {
	defer recoverV3(&err, func() { result = FrameResultV3{} })
	surface, err := p.RenderSurfaceFrame(ctx, timeline, clock, index)
	if err != nil {
		return FrameResultV3{}, err
	}
	value, err := p.finish(ctx, surface.Result)
	if err != nil {
		return FrameResultV3{}, err
	}
	return FrameResultV3{FrameClock: clock, CompositionFrameIndex: index, Result: value}, nil
}

func (p *ProgramV3) outputValidator() *Program { return &Program{config: p.config.Static} }

func (p *ProgramV3) finish(ctx context.Context, visual visualcompositor.Result) (ResultV3, error) {
	output, err := color.ConvertSurfaceToRGBA8(ctx, visual.Surface, p.config.Static.OutputLimits)
	if err != nil {
		return ResultV3{}, classifyColorDependency(err)
	}
	validator := p.outputValidator()
	if err = validator.validateOutput(visual, output); err != nil {
		return ResultV3{}, err
	}
	png, err := pngencode.Encode(ctx, output, p.config.Static.PNGLimits)
	if err != nil {
		return ResultV3{}, classifyPNGDependency(err)
	}
	if err = validator.validatePNG(output, png); err != nil {
		return ResultV3{}, err
	}
	value := ResultV3{SchemaVersion: 3, Profile: DomainProfileV3, DocumentHash: p.DocumentHash(), BuildFingerprint: p.config.Static.BuildFingerprint, TimelineID: visual.TimelineID, TimelineTime: visual.TimelineTime, SourceManifest: p.SourceManifest(), Visual: visual, Output: output, PNG: png, StageBinding: StageBinding{Profile: StageBindingProfileV1, VisualPixelHash: visual.PixelHash, OutputPixelHash: output.PixelHash, PNGSourcePixelHash: png.SourcePixelHash, PNGArtifactHash: png.ArtifactHash}}
	if err = p.validateReceipt(ctx, value); err != nil {
		return ResultV3{}, err
	}
	if err = contextError(ctx, "v3/output/publish"); err != nil {
		return ResultV3{}, err
	}
	return value, nil
}

// validateSources binds each static receipt to its cached manifest and each video
// receipt to the full original asset and bounded intrinsic surface dimensions.
func (p *ProgramV3) validateSources(sources []visualcompositor.SourceReceipt) error {
	validator := &Program{documentHash: p.DocumentHash(), config: p.config.Static, manifest: p.manifest.Static, v2: true}
	for _, s := range sources {
		if s.Native == nil || s.Native.Request.AssetKind != documentv2.AssetKindVideo {
			if err := validator.validateSerializedSourcesAgainstManifest([]visualcompositor.SourceReceipt{s}); err != nil {
				return err
			}
			continue
		}
		r := s.Native
		index := sort.Search(len(p.manifest.Video), func(i int) bool { return p.manifest.Video[i].AssetID >= r.Request.AssetID })
		found := index < len(p.manifest.Video) && p.manifest.Video[index].AssetID == r.Request.AssetID
		if found {
			asset := p.manifest.Video[index]
			pixels, ok := checkedMul(uint64(r.Width), uint64(r.Height))
			bytes, bytesOK := checkedMul(pixels, 8)
			if r.Width <= 0 || r.Height <= 0 || !ok || !bytesOK || bytes > p.config.MaxVideoFrameSurfaceBytes || r.Request.DocumentHash != p.DocumentHash() || r.Request.AssetIndex != asset.AssetIndex || r.Request.AssetContentHash != asset.Identity.ResourceHash || r.AssetContentHash != asset.Identity.ResourceHash || r.DecodeProfile != asset.Identity.Profiles.Decode || r.SelectionProfile != asset.Identity.Profiles.Selection || r.ColorProfile != asset.Identity.Profiles.Color || r.NormalizationProfile != asset.Identity.Profiles.Normalization {
				return pipelineError(CodeStageMismatch, "v3/video/manifest", ErrStageMismatch)
			}
		}
		if !found {
			return pipelineError(CodeStageMismatch, "v3/video/asset", ErrStageMismatch)
		}
	}
	return nil
}

func (p *ProgramV3) validateReceipt(ctx context.Context, value ResultV3) error {
	if value.SchemaVersion != 3 || value.Profile != DomainProfileV3 || value.DocumentHash != p.DocumentHash() || value.BuildFingerprint != p.config.Static.BuildFingerprint || value.TimelineID != value.Visual.TimelineID || value.TimelineTime != value.Visual.TimelineTime || !reflect.DeepEqual(value.SourceManifest, p.SourceManifest()) || value.StageBinding.Profile != StageBindingProfileV1 || value.StageBinding.VisualPixelHash != value.Visual.PixelHash || value.StageBinding.OutputPixelHash != value.Output.PixelHash || value.StageBinding.PNGSourcePixelHash != value.Output.PixelHash || value.PNG.SourcePixelHash != value.Output.PixelHash || value.StageBinding.PNGArtifactHash != value.PNG.ArtifactHash {
		return pipelineError(CodeStageMismatch, "v3/receipt", ErrStageMismatch)
	}
	if err := p.visual.ValidateSerializedReceipt(ctx, value.Visual); err != nil {
		return classifyRenderDependency("v3/receipt/visual", err)
	}
	if err := p.validateSources(value.Visual.Sources); err != nil {
		return err
	}
	validator := p.outputValidator()
	if err := validator.validateSerializedOutput(value.Output); err != nil {
		return err
	}
	return validator.validateSerializedPNG(value.Output, value.PNG)
}

// ValidateSerializedReceipt checks frozen scope, indexes, manifests and hash-chain
// structure. It cannot authenticate pixels or prove offline decoder provenance.
func (p *ProgramV3) ValidateSerializedReceipt(ctx context.Context, value ResultV3) (err error) {
	defer recoverV3(&err, nil)
	if err = p.validateRuntime(ctx); err != nil {
		return err
	}
	if err = p.validateReceipt(ctx, value); err != nil {
		return err
	}
	return contextError(ctx, "v3/serialized/publish")
}

// ValidateResult independently replays composition, color and PNG with the borrowed
// host sources. A serialized receipt without runtime objects never passes this API.
func (p *ProgramV3) ValidateResult(ctx context.Context, value ResultV3) (err error) {
	defer recoverV3(&err, nil)
	if err = p.validateRuntime(ctx); err != nil {
		return err
	}
	if err = p.validateReceipt(ctx, value); err != nil {
		return err
	}
	if err = p.visual.ValidateResult(ctx, value.Visual); err != nil {
		return classifyRenderDependency("v3/validate/visual", err)
	}
	validator := p.outputValidator()
	if err = validator.validateOutput(value.Visual, value.Output); err != nil {
		return err
	}
	if err = validator.validatePNG(value.Output, value.PNG); err != nil {
		return err
	}
	trusted, err := p.RenderAt(ctx, value.TimelineID, value.TimelineTime)
	if err != nil {
		return err
	}
	if !equalVisualReceipt(trusted.Visual, value.Visual) || !equalOutputReceipt(trusted.Output, value.Output) || !equalPNGReceipt(trusted.PNG, value.PNG) {
		return pipelineError(CodeStageMismatch, "v3/validate/replay", ErrStageMismatch)
	}
	return contextError(ctx, "v3/validate/publish")
}
