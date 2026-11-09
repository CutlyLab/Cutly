package referencevisual

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/nativevideo"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	DomainProfileV3                         = "cutly.reference-visual.domain-temporal-png-vector-text-video-export.v3"
	DomainManifestProfileV3                 = "cutly.reference-visual.domain-source-manifest.v3"
	DefaultMaxVideoFrameSurfaceBytes uint64 = 64 << 20
)

// ConfigV3 is additive; Config and all V1/V2 wire profiles remain unchanged.
// Video limits bound wrapper metadata and returned surfaces, not the host decoder's
// internal work or snapshots. Hosts must independently configure media/decode limits.
// Pipeline runtime limits are per render, not a global concurrent-render budget.
type ConfigV3 struct {
	Static                    Config             `json:"static"`
	VideoLimits               nativevideo.Limits `json:"video_limits"`
	MaxVideoFrameSurfaceBytes uint64             `json:"max_video_frame_surface_bytes"`
}

func DefaultConfigV3(width, height int) ConfigV3 {
	return ConfigV3{Static: DefaultConfigV2(width, height), VideoLimits: nativevideo.DefaultLimits(), MaxVideoFrameSurfaceBytes: DefaultMaxVideoFrameSurfaceBytes}
}

type VideoAssetReceiptV3 struct {
	AssetID    documentv2.ID              `json:"asset_id"`
	AssetIndex int                        `json:"asset_index"`
	Identity   videodecode.SourceIdentity `json:"identity"`
}

// SourceManifestV3 records full original document identity, scope, immutable static
// manifests and pinned native decoder identities. It is unsigned structural evidence.
type SourceManifestV3 struct {
	SchemaVersion             int                                 `json:"schema_version"`
	Profile                   string                              `json:"profile"`
	DocumentHash              string                              `json:"document_hash"`
	DomainScope               visualcompositor.DomainScopeBinding `json:"domain_scope"`
	Static                    SourceManifest                      `json:"static"`
	Video                     []VideoAssetReceiptV3               `json:"video"`
	MaxVideoFrameSurfaceBytes uint64                              `json:"max_video_frame_surface_bytes"`
}

// ProgramV3 retains immutable static providers and borrows host-owned nativevideo
// FrameSources until all renders and validations finish. It never closes a Source.
// Sources must support arbitrary-order/concurrent calls and return immutable Surfaces;
// media/decode.Source satisfies this contract (it serializes its own decoder calls).
type ProgramV3 struct {
	config     ConfigV3
	visual     *visualcompositor.Program
	native     *nativepngsource.Provider
	video      *nativevideo.Provider
	expression *expressionlayer.Provider
	text       *textlayer.Provider
	manifest   SourceManifestV3
}

func normalizeConfigV3(value ConfigV3) (ConfigV3, visualcompositor.Config, error) {
	normalized, visual, err := normalizeConfigMode(value.Static, true)
	if err != nil {
		return ConfigV3{}, visualcompositor.Config{}, err
	}
	value.Static = normalized
	defaults := nativevideo.DefaultLimits()
	if value.VideoLimits.MaxBindings == 0 {
		value.VideoLimits.MaxBindings = defaults.MaxBindings
	}
	if value.VideoLimits.MaxClipScopes == 0 {
		value.VideoLimits.MaxClipScopes = defaults.MaxClipScopes
	}
	if value.VideoLimits.MaxCanonicalDocumentBytes == 0 {
		value.VideoLimits.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
	}
	if value.MaxVideoFrameSurfaceBytes == 0 {
		value.MaxVideoFrameSurfaceBytes = DefaultMaxVideoFrameSurfaceBytes
	}
	l := value.VideoLimits
	if l.MaxBindings <= 0 || l.MaxBindings > nativevideo.HardMaxBindings || l.MaxClipScopes <= 0 || l.MaxClipScopes > nativevideo.HardMaxClipScopes || l.MaxCanonicalDocumentBytes <= 0 || l.MaxCanonicalDocumentBytes > nativevideo.HardMaxDocumentBytes || value.MaxVideoFrameSurfaceBytes < cpucompositor.BytesPerPixel || value.MaxVideoFrameSurfaceBytes > HardMaxPipelineBytes {
		return ConfigV3{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/v3/video", ErrConfigInvalid)
	}
	if uint64(l.MaxCanonicalDocumentBytes) < visual.Limits.MaxCanonicalDocumentBytes {
		visual.Limits.MaxCanonicalDocumentBytes = uint64(l.MaxCanonicalDocumentBytes)
	}
	value.Static.CompositorLimits = visual.Limits
	visual.Source.Video = true
	visual.Source.LinearVideoRetime = true
	visual.NativeProfiles.Video = nativevideo.Profiles()
	if _, err := visualcompositor.Capabilities(visual); err != nil {
		return ConfigV3{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/v3/visual", errors.Join(ErrConfigInvalid, err))
	}
	// Existing aggregate accounting bounds six target surfaces. Reserve two additional
	// intrinsic video surfaces for simultaneous dissolve handles before any source call.
	frame, output, artifact, row, ok := configuredRuntimeSizes(value.Static)
	targets, targetOK := checkedMul(frame, 6)
	pngPeak, pngOK := checkedSum(frame, output, artifact, row)
	if pngPeak > targets {
		targets = pngPeak
	}
	intrinsic, intrinsicOK := checkedMul(value.MaxVideoFrameSurfaceBytes, 2)
	nodes, nodeOK := checkedMul(uint64(visual.Limits.MaxDocumentNodes), metadataBytesPerDocumentNode)
	// Provider scopes/identities are additional to static wrapper metadata.
	videoMetadata, videoOK := checkedSum(uint64(l.MaxClipScopes)*256, uint64(l.MaxBindings)*512)
	peak, peakOK := checkedSum(value.Static.PipelineLimits.MaxRetainedSourceBytes, targets, intrinsic, nodes, videoMetadata)
	if !ok || !targetOK || !pngOK || !intrinsicOK || !nodeOK || !videoOK || !peakOK || peak > value.Static.PipelineLimits.MaxRuntimeWorkingBytes {
		return ConfigV3{}, visualcompositor.Config{}, pipelineError(CodeResourceLimit, "config/v3/runtime", ErrResourceLimit)
	}
	canonical, canonicalOK := checkedMul(visual.Limits.MaxCanonicalDocumentBytes, canonicalCompileMultiplier)
	transient := value.Static.NativeLimits.PNG.MaxWorkingBytes
	if value.Static.ExpressionLimits.Vector.MaxWorkingBytes > transient {
		transient = value.Static.ExpressionLimits.Vector.MaxWorkingBytes
	}
	compilePeak, compileOK := checkedSum(value.Static.PipelineLimits.MaxRetainedSourceBytes, transient, canonical, nodes, videoMetadata)
	textPeak, textOK := checkedSum(value.Static.NativeLimits.MaxRetainedBytes, value.Static.ExpressionLimits.MaxRetainedBytes, value.Static.TextConfig.Limits.MaxCompileWorkingBytes, nodes, videoMetadata)
	if !canonicalOK || !compileOK || !textOK || compilePeak > value.Static.PipelineLimits.MaxCompileWorkingBytes || textPeak > value.Static.PipelineLimits.MaxCompileWorkingBytes {
		return ConfigV3{}, visualcompositor.Config{}, pipelineError(CodeResourceLimit, "config/v3/compile", ErrResourceLimit)
	}
	return value, visual, nil
}

// CapabilitiesV3 returns the fixed configuration-owned offer, independent of any document.
func CapabilitiesV3(config ConfigV3) (videoedit.BackendCapabilities, error) {
	_, visual, err := normalizeConfigV3(config)
	if err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	return visualcompositor.Capabilities(visual)
}

// PreflightDomainV3 runs the same resource-free checked temporal compositor path as
// CompileDomainV3. One authored timeline and all enabled visual clips are required.
func PreflightDomainV3(ctx context.Context, document documentv2.Document, scope evaluator.DomainScope, config ConfigV3) (program *visualcompositor.Program, diagnostics []documentv2.Diagnostic, err error) {
	defer recoverV3(&err, func() { program = nil; diagnostics = nil })
	if err = contextError(ctx, "preflight/v3"); err != nil {
		return nil, nil, err
	}
	normalized, visual, err := normalizeConfigV3(config)
	if err != nil {
		return nil, nil, err
	}
	program, diagnostics = visualcompositor.CompileDomainV2(ctx, document, scope, visual)
	diagnostics = cloneDiagnostics(diagnostics)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, classifyVisualCompile(ctx, diagnostics)
	}
	if err = preflightVideoScopesV3(ctx, document, normalized.VideoLimits); err != nil {
		return nil, diagnostics, err
	}
	if err = contextError(ctx, "preflight/v3/publish"); err != nil {
		return nil, diagnostics, err
	}
	return program, diagnostics, nil
}

// CompileDomainV3 consumes full original document/hash/indexes and explicit bindings.
// PNG/font inputs are borrowed only until return; video Sources are host-owned and
// borrowed for Program lifetime. No IO/probe/decoder opening or capability inference
// occurs here. Every source compiler sees the full document, never a cropped graph.
func CompileDomainV3(ctx context.Context, document documentv2.Document, scope evaluator.DomainScope, pngs []Binding, fonts []textlayer.FontBinding, videos []nativevideo.Binding, config ConfigV3) (program *ProgramV3, diagnostics []documentv2.Diagnostic, err error) {
	defer recoverV3(&err, func() { program = nil; diagnostics = nil })
	if err = contextError(ctx, "compile/v3"); err != nil {
		return nil, nil, err
	}
	normalized, visualConfig, err := normalizeConfigV3(config)
	if err != nil {
		return nil, nil, err
	}
	if err = preflightBindings(ctx, pngs, normalized.Static.NativeLimits); err != nil {
		return nil, nil, err
	}
	if len(fonts) > normalized.Static.TextConfig.Limits.MaxBindings || len(videos) > normalized.VideoLimits.MaxBindings {
		return nil, nil, pipelineError(CodeResourceLimit, "bindings/v3/count", ErrResourceLimit)
	}
	// Binding labels are caller-sized too. Charge them before hashing map keys,
	// allocating binding-sized metadata or invoking any font/source callback.
	var bindingBytes uint64
	charge := func(values ...string) bool {
		for _, value := range values {
			var ok bool
			bindingBytes, ok = checkedAdd(bindingBytes, uint64(len(value)))
			if !ok || bindingBytes > visualConfig.Limits.MaxCanonicalDocumentBytes {
				return false
			}
		}
		return true
	}
	for _, b := range pngs {
		if !charge(string(b.AssetID)) {
			return nil, nil, pipelineError(CodeResourceLimit, "bindings/v3/bytes", ErrResourceLimit)
		}
	}
	for _, b := range fonts {
		if !charge(b.DocumentHash, string(b.FontAssetID)) {
			return nil, nil, pipelineError(CodeResourceLimit, "bindings/v3/bytes", ErrResourceLimit)
		}
	}
	for _, b := range videos {
		if !charge(b.DocumentHash, string(b.AssetID)) {
			return nil, nil, pipelineError(CodeResourceLimit, "bindings/v3/bytes", ErrResourceLimit)
		}
	}
	visual, ds := visualcompositor.CompileDomainV2(ctx, document, scope, visualConfig)
	diagnostics = cloneDiagnostics(ds)
	if visual == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, classifyVisualCompile(ctx, diagnostics)
	}
	normalized.Static.CompositorLimits = visual.Config().Limits
	if visual.Config() != visualConfig {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/v3/config", ErrStageMismatch)
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "compile/v3/document", errors.Join(ErrDocumentInvalid, err))
	}
	hash, err := hashCanonical(ctx, canonical, "compile/v3/hash")
	if err != nil {
		return nil, diagnostics, err
	}
	if hash != visual.DocumentHash() || uint64(len(canonical)) > visual.Config().Limits.MaxCanonicalDocumentBytes {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/v3/hash", ErrStageMismatch)
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "compile/v3/freeze", errors.Join(ErrDocumentInvalid, err))
	}
	canonical = nil
	// Admit all nativevideo scopes on the full frozen graph before any font or
	// source callback. nativevideo.Compile repeats these checks defensively, but
	// its later invocation must not become the first resource-admission boundary.
	if err = preflightVideoScopesV3(ctx, frozen, normalized.VideoLimits); err != nil {
		return nil, diagnostics, err
	}
	nativeBindings := make([]nativepngsource.Binding, len(pngs))
	for i, b := range pngs {
		nativeBindings[i] = nativepngsource.Binding{DocumentHash: hash, AssetID: b.AssetID, EncodedPNG: b.EncodedPNG}
	}
	native, err := nativepngsource.Compile(ctx, frozen, nativeBindings, nativepngsource.Config{Limits: normalized.Static.NativeLimits})
	if err != nil {
		return nil, diagnostics, classifyNativeCompile(err)
	}
	expression, err := expressionlayer.Compile(ctx, frozen, expressionlayer.Config{Limits: normalized.Static.ExpressionLimits})
	if err != nil {
		return nil, diagnostics, classifyExpressionCompile(err)
	}
	text, err := textlayer.Compile(ctx, frozen, fonts, *normalized.Static.TextConfig)
	if err != nil {
		return nil, diagnostics, classifyTextCompile(err)
	}
	// Check all binding authorization before invoking even an Identity callback.
	required := enabledAssets(frozen, documentv2.TrackKindVideo, documentv2.AssetKindVideo)
	if len(videos) != len(required) {
		return nil, diagnostics, pipelineError(CodeNativeSourceCompile, "bindings/v3/video", nativevideo.ErrMissing)
	}
	seen := make(map[documentv2.ID]bool, len(videos))
	for _, b := range videos {
		if b.DocumentHash != hash || nilVideoSource(b.Source) || seen[b.AssetID] || required[b.AssetID].ID == "" {
			return nil, diagnostics, pipelineError(CodeNativeSourceCompile, "bindings/v3/video", nativevideo.ErrMismatch)
		}
		seen[b.AssetID] = true
	}
	indexes := make(map[documentv2.ID]int, len(frozen.Assets))
	for i, a := range frozen.Assets {
		indexes[a.ID] = i
	}
	borrowed := make([]nativevideo.Binding, len(videos))
	receipts := make([]VideoAssetReceiptV3, 0, len(videos))
	for i, b := range videos {
		if err = contextError(ctx, "compile/v3/video/identity"); err != nil {
			return nil, diagnostics, err
		}
		identity, identityErr := b.Source.Identity()
		if identityErr != nil {
			return nil, diagnostics, classifyVideoV3(ctx, identityErr)
		}
		borrowed[i] = nativevideo.Binding{DocumentHash: hash, AssetID: b.AssetID, Source: boundedVideoV3{source: b.Source, identity: identity, maxBytes: normalized.MaxVideoFrameSurfaceBytes}}
		receipts = append(receipts, VideoAssetReceiptV3{AssetID: b.AssetID, AssetIndex: indexes[b.AssetID], Identity: identity})
	}
	video, err := nativevideo.Compile(ctx, frozen, borrowed, normalized.VideoLimits)
	if err != nil {
		return nil, diagnostics, classifyVideoV3(ctx, err)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].AssetID < receipts[j].AssetID })
	nm, em, tm := native.Manifest(), expression.Manifest(), text.Manifest()
	if native.DocumentHash() != hash || expression.DocumentHash() != hash || text.DocumentHash() != hash || !validNativeManifest(ctx, nm, frozen, normalized.Static.NativeLimits) || !validExpressionManifest(ctx, em, frozen, normalized.Static.ExpressionLimits) || !validTextManifest(ctx, tm, frozen, normalized.Static.TextConfig.Limits) {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/v3/manifests", ErrStageMismatch)
	}
	nr, nok := nativeRetainedBytes(nm)
	er, eok := expressionRetainedBytes(em)
	tr, tok := textRetainedBytes(tm)
	total, ok := checkedSum(nr, er, tr)
	if !nok || !eok || !tok || !ok || total > normalized.Static.PipelineLimits.MaxRetainedSourceBytes {
		return nil, diagnostics, pipelineError(CodeResourceLimit, "compile/v3/retained", ErrResourceLimit)
	}
	static := SourceManifest{SchemaVersion: ManifestSchemaVersion, Profile: ManifestProfileV2, DocumentHash: hash, NativePNG: nm, Expression: em, Text: &tm, TextProviderProfile: textlayer.ProfileV1, TextContractVersion: textlayer.ContractVersion, TextRetainedSurfaceBytes: tr, ExpressionHandleProfile: expressionlayer.TransitionHandleProfileV2, ExpressionHandleContractVersion: expressionlayer.TransitionHandleContractVersion, VectorTransitionScopeProfile: documentv2.VectorTransitionScopeProfileV1, NativeRetainedSurfaceBytes: nr, ExpressionRetainedSurfaceBytes: er, TotalRetainedSurfaceBytes: total}
	if !validCombinedManifestMode(static, normalized.Static, true) {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/v3/static", ErrStageMismatch)
	}
	manifest := SourceManifestV3{SchemaVersion: 3, Profile: DomainManifestProfileV3, DocumentHash: hash, DomainScope: visual.ScopeBinding(), Static: static, Video: receipts, MaxVideoFrameSurfaceBytes: normalized.MaxVideoFrameSurfaceBytes}
	if err = contextError(ctx, "compile/v3/publish"); err != nil {
		return nil, diagnostics, err
	}
	return &ProgramV3{config: normalized, visual: visual, native: native, video: video, expression: expression, text: text, manifest: manifest}, diagnostics, nil
}

// preflightVideoScopesV3 runs only after full graph admission/validation. Count
// exactly nativevideo.Compile's enabled video-track/video-asset scopes, including
// separate clips referencing the same asset; disabled and other-kind clips do not
// consume this budget. No host-owned object is inspected or called here.
func preflightVideoScopesV3(ctx context.Context, document documentv2.Document, limits nativevideo.Limits) error {
	videoAssets := make(map[documentv2.ID]bool)
	for _, asset := range document.Assets {
		if err := contextError(ctx, "compile/v3/video/admission"); err != nil {
			return err
		}
		if asset.Kind == documentv2.AssetKindVideo {
			videoAssets[asset.ID] = true
		}
	}
	required := make(map[documentv2.ID]bool)
	scopes := 0
	for _, timeline := range document.Timelines {
		for _, track := range timeline.Tracks {
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for _, clip := range track.Clips {
				if err := contextError(ctx, "compile/v3/video/admission"); err != nil {
					return err
				}
				if clip.Disabled || !videoAssets[clip.AssetID] {
					continue
				}
				if scopes >= limits.MaxClipScopes {
					return pipelineError(CodeResourceLimit, "compile/v3/video/clip_scopes", errors.Join(ErrResourceLimit, nativevideo.ErrResourceLimit))
				}
				scopes++
				if !required[clip.AssetID] && len(required) >= limits.MaxBindings {
					return pipelineError(CodeResourceLimit, "compile/v3/video/assets", errors.Join(ErrResourceLimit, nativevideo.ErrResourceLimit))
				}
				required[clip.AssetID] = true
			}
		}
	}
	return contextError(ctx, "compile/v3/video/admission")
}

func nilVideoSource(source nativevideo.FrameSource) bool {
	if source == nil {
		return true
	}
	v := reflect.ValueOf(source)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

type boundedVideoV3 struct {
	source   nativevideo.FrameSource
	identity videodecode.SourceIdentity
	maxBytes uint64
}

func (b boundedVideoV3) Identity() (videodecode.SourceIdentity, error) { return b.identity, nil }
func (b boundedVideoV3) Frame(ctx context.Context, at videoedit.RationalTime) (*cpucompositor.Surface, videodecode.FrameReceipt, error) {
	surface, receipt, err := b.source.Frame(ctx, at)
	if err != nil {
		return nil, videodecode.FrameReceipt{}, err
	}
	if err = contextError(ctx, "video/v3/frame"); err != nil {
		return nil, videodecode.FrameReceipt{}, err
	}
	if surface == nil || surface.Width() <= 0 || surface.Height() <= 0 {
		return nil, videodecode.FrameReceipt{}, nativevideo.ErrMismatch
	}
	pixels, ok := checkedMul(uint64(surface.Width()), uint64(surface.Height()))
	bytes, bytesOK := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok || !bytesOK || bytes > b.maxBytes {
		return nil, videodecode.FrameReceipt{}, nativevideo.ErrResourceLimit
	}
	return surface, receipt, nil
}

type mixedNativeV3 struct {
	png   *nativepngsource.Provider
	video *nativevideo.Provider
}

func (p mixedNativeV3) Frame(ctx context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
	switch request.AssetKind {
	case documentv2.AssetKindImage:
		return p.png.Frame(ctx, request)
	case documentv2.AssetKindVideo:
		return p.video.Frame(ctx, request)
	default:
		return rastercompositor.NativeSourceFrame{}, nativevideo.ErrMismatch
	}
}
func (p *ProgramV3) providers() visualcompositor.Providers {
	return visualcompositor.Providers{Native: mixedNativeV3{p.native, p.video}, Expression: p.expression, ExpressionHandle: p.expression, Text: p.text}
}
func recoverV3(err *error, zero func()) {
	if recover() != nil {
		if zero != nil {
			zero()
		}
		*err = pipelineError(CodeStageMismatch, "v3/panic", ErrStageMismatch)
	}
}
func classifyVideoV3(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return pipelineError(CodeCanceled, "video/v3", errors.Join(ErrCanceled, ctx.Err(), err))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return pipelineError(CodeCanceled, "video/v3", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, nativevideo.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "video/v3", errors.Join(ErrResourceLimit, err))
	}
	return pipelineError(CodeNativeSourceCompile, "video/v3", errors.Join(ErrNativeSourceCompile, err))
}

func (p *ProgramV3) DocumentHash() string {
	if p == nil {
		return ""
	}
	return p.manifest.DocumentHash
}
func (p *ProgramV3) Profile() string {
	if p == nil {
		return ""
	}
	return DomainProfileV3
}
func (p *ProgramV3) DomainScopeHash() string {
	if p == nil {
		return ""
	}
	return p.visual.DomainScopeHash()
}
func (p *ProgramV3) Scope() evaluator.DomainScope {
	if p == nil {
		return evaluator.DomainScope{}
	}
	return p.visual.Scope()
}
func (p *ProgramV3) Config() ConfigV3 {
	if p == nil {
		return ConfigV3{}
	}
	v := p.config
	v.Static = cloneConfig(v.Static)
	return v
}
func cloneManifestV3(v SourceManifestV3) SourceManifestV3 {
	v.Static = cloneSourceManifest(v.Static)
	v.DomainScope = cloneScopeBinding(v.DomainScope)
	v.Video = append([]VideoAssetReceiptV3(nil), v.Video...)
	return v
}
func (p *ProgramV3) SourceManifest() SourceManifestV3 {
	if p == nil {
		return SourceManifestV3{}
	}
	return cloneManifestV3(p.manifest)
}

func DomainDescriptorV3() ProfileDescriptor {
	v := DomainDescriptorV2()
	v.Profile = DomainProfileV3
	v.CompositorProfile = visualcompositor.DomainProfileV2
	v.CompileSemantics = "CompileDomainV3/PreflightDomainV3 use evaluator V2 scoped temporal authority and fixed configuration-owned video/image/vector/pinned-text offers on the full original document/hash/indexes. One authored timeline and all enabled visual clips selected; no cropped document or capability inference."
	v.ManifestSemantics = "Separate schema/profile V3 binds exact scope, static provider manifests and sorted video asset original indexes plus native decoder identities. Legacy V1/V2 descriptors, manifests and entrypoints remain unchanged."
	v.RenderSemantics = "Exact rational source-time including linear retime and dissolve handles; unified native PNG/video, vector and pinned Text dispatch; immutable RGBA16 surfaces and existing color/PNG output. Hard cuts switch ordinary layers; Text dissolves are rejected."
	v.OwnershipSemantics = "PNG/font inputs borrowed until compile returns. Host-owned nativevideo.FrameSources are borrowed for Program lifetime, never closed; host must ensure arbitrary-order concurrent safety, immutable returned surfaces and lifetime through all renders/validation."
	v.ResourceSemantics = "Admission precedes proportional document/binding allocation and callbacks. Static aggregate budgets plus two bounded intrinsic video handle surfaces per render; host decoder snapshots, decoding work and concurrent multiplication are independently host-budgeted, not included in wrapper limits."
	v.ReceiptSemantics = "Live validation replays complete trusted provider composition and PNG output. Serialized receipts are scope/profile/index/hash-chain claims, not proof of decoded pixel causality or provenance."
	v.ExcludedResponsibilities = "Decoder creation, probe/semantic scanning, reconstruction, audio output, multiple timelines, unselected enabled visual clips, dynamic Text, Text dissolves, external IO authorization, scheduling and muxing. Real FFmpeg validation depends on the host pinned media/decode build."
	return v
}
