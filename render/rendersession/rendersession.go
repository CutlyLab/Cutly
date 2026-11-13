// Package rendersession owns one compiled Document v2 render pipeline and renders many frames
// through it. It exists because the reference-visual export boundary re-verifies the complete
// receipt chain for every frame and hosts historically re-compiled the whole program per frame;
// both are correct and deliberately slow. A session compiles exactly once, renders frames
// through the same visualcompositor program and providers, and replaces redundant per-frame
// re-verification with a production verification profile:
//
//   - the compiled pipeline identity is checked once at Open, not re-derived per frame;
//   - each frame's surface keeps exactly the hash the compositor computed (no re-hash, no
//     provider replay);
//   - output color and PNG receipts keep the hashes their own stages computed;
//   - a configurable deep-validation interval re-renders selected frames through the full
//     reference path and requires byte equality with the session result.
//
// The session never changes pixel mathematics: the differential gate in this package's tests
// asserts the production path is byte-identical to referencevisual for every frame it serves.
package rendersession

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/nativevideo"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	// ProfileV1 identifies the production render-session profile.
	ProfileV1 = "cutly.render-session.production.v1"
)

var (
	ErrSessionInvalid = errors.New("rendersession: session is invalid")
	ErrRenderFailed   = errors.New("rendersession: frame render failed")
	ErrStageMismatch  = errors.New("rendersession: stage identity mismatch")
	ErrDeepValidation = errors.New("rendersession: deep validation diverged from the reference path")
)

// Options bounds one render session. DeepValidationInterval selects how often a frame is
// additionally re-rendered through the complete reference path with byte-equality required:
// zero (default) disables it, one validates every frame, N validates every Nth frame and
// always the first frame served by the session.
type Options struct {
	DeepValidationInterval int
}

// Session is one compiled reference pipeline reused across frames. A Session is safe for
// concurrent frame renders; every Render call is independent and cancellation-scoped.
type Session struct {
	reference  *referencevisual.Program
	visual     *visualcompositor.Program
	providers  visualcompositor.Providers
	scratch    *cpucompositor.Scratch
	config     referencevisual.Config
	deepEvery  int
	served     atomic.Int64
	deepChecks atomic.Int64
}

// OpenVideo is Open for the additive host-bound temporal-video mode: the document may contain
// video assets whose decoder sources the host supplies as authorized bindings. Everything
// else — compile gates, production verification profile, deep validation — is identical.
func OpenVideo(ctx context.Context, document documentv2.Document, pngBindings []referencevisual.Binding, videoBindings []nativevideo.Binding, config referencevisual.Config, options Options) (*Session, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrSessionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.DeepValidationInterval < 0 {
		return nil, fmt.Errorf("%w: negative deep-validation interval", ErrSessionInvalid)
	}
	program, diagnostics, err := referencevisual.CompileV2Video(ctx, document, pngBindings, nil, videoBindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, fmt.Errorf("%w: reference compile diagnostics=%+v err=%v", ErrSessionInvalid, diagnostics, err)
	}
	visual, providers := program.CompiledPipeline()
	if visual == nil {
		return nil, fmt.Errorf("%w: compiled pipeline is unavailable", ErrSessionInvalid)
	}
	scratch, err := cpucompositor.NewScratch(program.Config().CompositorLimits.Target, cpucompositor.DefaultScratchSurfaces)
	if err != nil {
		return nil, fmt.Errorf("%w: scratch pool: %v", ErrSessionInvalid, err)
	}
	return &Session{
		reference: program, visual: visual, providers: providers, scratch: scratch,
		config:    program.Config(),
		deepEvery: options.DeepValidationInterval,
	}, nil
}

// Open compiles the document exactly once through the complete reference gates and keeps the
// compiled pipeline for frame reuse. The returned session shares the reference program's
// immutable state; it owns no mutable rendering state.
func Open(ctx context.Context, document documentv2.Document, bindings []referencevisual.Binding, config referencevisual.Config, options Options) (*Session, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrSessionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.DeepValidationInterval < 0 {
		return nil, fmt.Errorf("%w: negative deep-validation interval", ErrSessionInvalid)
	}
	program, diagnostics, err := referencevisual.Compile(ctx, document, bindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, fmt.Errorf("%w: reference compile diagnostics=%+v err=%v", ErrSessionInvalid, diagnostics, err)
	}
	visual, providers := program.CompiledPipeline()
	if visual == nil {
		return nil, fmt.Errorf("%w: compiled pipeline is unavailable", ErrSessionInvalid)
	}
	scratch, err := cpucompositor.NewScratch(program.Config().CompositorLimits.Target, cpucompositor.DefaultScratchSurfaces)
	if err != nil {
		return nil, fmt.Errorf("%w: scratch pool: %v", ErrSessionInvalid, err)
	}
	return &Session{
		reference: program, visual: visual, providers: providers, scratch: scratch,
		config:    program.Config(),
		deepEvery: options.DeepValidationInterval,
	}, nil
}

// FrameResult is one production frame: the canonical visual surface receipt from the shared
// compositor plus the output-color and PNG artifacts. Hashes are the ones computed by the
// producing stages; the session does not re-hash them.
type FrameResult struct {
	Profile      string                  `json:"profile"`
	DocumentHash string                  `json:"document_hash"`
	TimelineID   documentv2.ID           `json:"timeline_id"`
	TimelineTime documentv2.RationalTime `json:"timeline_time"`
	Visual       visualcompositor.Result `json:"visual"`
	Output       color.OutputResult      `json:"output"`
	PNG          pngencode.Result        `json:"png"`
}

// RenderSurface renders one exact timeline time and returns the canonical visual result
// without color conversion or PNG encoding. It is the cheapest embedding boundary for chunk
// assemblers that consume surfaces directly.
func (session *Session) RenderSurface(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime) (visualcompositor.Result, error) {
	if session == nil || session.visual == nil || session.scratch == nil {
		return visualcompositor.Result{}, fmt.Errorf("%w: session is closed", ErrSessionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return visualcompositor.Result{}, err
	}
	visual, err := session.visual.RenderAtWithScratch(ctx, timelineID, at, session.providers, session.scratch)
	if err != nil {
		return visualcompositor.Result{}, fmt.Errorf("%w: %v", ErrRenderFailed, err)
	}
	if err := session.checkVisual(timelineID, at, visual); err != nil {
		return visualcompositor.Result{}, err
	}
	return visual, nil
}

// RenderFrame renders one exact timeline time through the complete production path: shared
// compositor, output color conversion, deterministic PNG encode. When the deep-validation
// interval selects this frame, the complete reference path re-renders it and byte equality is
// enforced before the result is returned.
func (session *Session) RenderFrame(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime) (FrameResult, error) {
	visual, err := session.RenderSurface(ctx, timelineID, at)
	if err != nil {
		return FrameResult{}, err
	}
	result, err := session.finishFrame(ctx, timelineID, at, visual)
	if err != nil {
		return FrameResult{}, err
	}
	served := session.served.Add(1)
	if session.deepEvery > 0 && (served == 1 || served%int64(session.deepEvery) == 0) {
		if err := session.deepValidate(ctx, timelineID, at, result); err != nil {
			return FrameResult{}, err
		}
		session.deepChecks.Add(1)
	}
	return result, nil
}

// finishFrame performs the production output stages over one rendered visual surface: output
// color conversion and deterministic PNG encode with receipt checks.
func (session *Session) finishFrame(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime, visual visualcompositor.Result) (FrameResult, error) {
	if err := ctx.Err(); err != nil {
		return FrameResult{}, err
	}
	output, err := color.ConvertSurfaceToRGBA8(ctx, visual.Surface, session.config.OutputLimits)
	if err != nil {
		return FrameResult{}, fmt.Errorf("%w: output color: %v", ErrRenderFailed, err)
	}
	if output.Width != session.config.Width || output.Height != session.config.Height || output.PixelHash == "" {
		return FrameResult{}, fmt.Errorf("%w: output geometry or hash receipt", ErrStageMismatch)
	}
	if err := ctx.Err(); err != nil {
		return FrameResult{}, err
	}
	encoded, err := pngencode.Encode(ctx, output, session.config.PNGLimits)
	if err != nil {
		return FrameResult{}, fmt.Errorf("%w: png encode: %v", ErrRenderFailed, err)
	}
	if encoded.ArtifactHash == "" || encoded.Artifact == nil || encoded.Artifact.Size() != encoded.ArtifactBytes {
		return FrameResult{}, fmt.Errorf("%w: png artifact receipt", ErrStageMismatch)
	}
	return FrameResult{
		Profile: ProfileV1, DocumentHash: session.reference.DocumentHash(),
		TimelineID: timelineID, TimelineTime: at,
		Visual: visual, Output: output, PNG: encoded,
	}, nil
}

// DeepValidations reports how many frames were re-verified through the full reference path.
func (session *Session) DeepValidations() int {
	if session == nil {
		return 0
	}
	return int(session.deepChecks.Load())
}

// DocumentHash returns the compiled document identity shared by every frame of the session.
func (session *Session) DocumentHash() string {
	if session == nil {
		return ""
	}
	return session.reference.DocumentHash()
}

// SourceManifest returns the reference compile's source manifest identity.
func (session *Session) SourceManifest() referencevisual.SourceManifest {
	if session == nil {
		return referencevisual.SourceManifest{}
	}
	return session.reference.SourceManifest()
}

// Close releases the session. The compiled programs are immutable and shared; close only
// detaches the session handles.
func (session *Session) Close() error {
	if session == nil {
		return fmt.Errorf("%w: session is nil", ErrSessionInvalid)
	}
	session.visual = nil
	session.providers = visualcompositor.Providers{}
	session.scratch = nil
	return nil
}

func (session *Session) checkVisual(timelineID documentv2.ID, at documentv2.RationalTime, visual visualcompositor.Result) error {
	if visual.TimelineID != timelineID || visual.TimelineTime != at ||
		visual.Surface == nil || visual.Surface.Width() != session.config.Width || visual.Surface.Height() != session.config.Height ||
		visual.PixelHash == "" || visual.DocumentHash != session.reference.DocumentHash() {
		return fmt.Errorf("%w: visual receipt geometry or identity", ErrStageMismatch)
	}
	if visual.Width != session.config.Width || visual.Height != session.config.Height {
		return fmt.Errorf("%w: visual receipt dimensions", ErrStageMismatch)
	}
	return nil
}

// deepValidate re-renders one frame through the complete validated reference path and
// requires byte equality with the production result at every artifact boundary.
func (session *Session) deepValidate(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime, produced FrameResult) error {
	reference, err := session.reference.RenderAt(ctx, timelineID, at)
	if err != nil {
		return fmt.Errorf("%w: reference re-render: %v", ErrDeepValidation, err)
	}
	if reference.Visual.PixelHash != produced.Visual.PixelHash ||
		reference.Output.PixelHash != produced.Output.PixelHash ||
		reference.PNG.ArtifactHash != produced.PNG.ArtifactHash {
		return fmt.Errorf("%w: pixel/output/png divergence at %d/%d", ErrDeepValidation, at.Value, at.Rate)
	}
	if reference.SourceManifest.DocumentHash != produced.Visual.DocumentHash {
		return fmt.Errorf("%w: document identity divergence", ErrDeepValidation)
	}
	return nil
}
