package referencevisual

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textassembly"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/vectorraster"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	DefaultMaxRetainedSourceBytes uint64 = 1 << 30
	DefaultMaxCompileWorkingBytes uint64 = 3 << 30
	DefaultMaxRuntimeWorkingBytes uint64 = 3 << 30
	HardMaxPipelineBytes          uint64 = 8 << 30
	metadataBytesPerDocumentNode  uint64 = 256
	canonicalCompileMultiplier    uint64 = 4
)

// PipelineLimits bound aggregate memory that is outside any one dependency's local view.
type PipelineLimits struct {
	MaxRetainedSourceBytes uint64 `json:"max_retained_source_bytes"`
	MaxCompileWorkingBytes uint64 `json:"max_compile_working_bytes"`
	MaxRuntimeWorkingBytes uint64 `json:"max_runtime_working_bytes"`
}

func DefaultPipelineLimits() PipelineLimits {
	return PipelineLimits{
		MaxRetainedSourceBytes: DefaultMaxRetainedSourceBytes,
		MaxCompileWorkingBytes: DefaultMaxCompileWorkingBytes,
		MaxRuntimeWorkingBytes: DefaultMaxRuntimeWorkingBytes,
	}
}

// Binding lends one complete encoded PNG only until Compile returns.
type Binding struct {
	AssetID    documentv2.ID `json:"asset_id"`
	EncodedPNG []byte        `json:"-"`
}

// Config exposes every stage limit but no profile or source-capability injection.
type Config struct {
	Width            int                     `json:"width"`
	Height           int                     `json:"height"`
	NativeLimits     nativepngsource.Limits  `json:"native_limits"`
	ExpressionLimits expressionlayer.Limits  `json:"expression_limits"`
	TextConfig       *textlayer.Config       `json:"text_config,omitempty"`
	CompositorLimits visualcompositor.Limits `json:"compositor_limits"`
	OutputLimits     color.OutputLimits      `json:"output_limits"`
	PNGLimits        pngencode.Limits        `json:"png_limits"`
	PipelineLimits   PipelineLimits          `json:"pipeline_limits"`
	BuildFingerprint string                  `json:"build_fingerprint,omitempty"`
}

func defaultTextConfigV2() textlayer.Config {
	result := textlayer.DefaultConfig()
	// The standalone provider deliberately exposes a larger hard/default envelope. The assembled
	// pipeline narrows it so native, expression and Text compiles have one finite aggregate budget.
	result.Limits.MaxCompileWorkingBytes = 2 << 30
	return result
}

func defaultPipelineLimitsV2() PipelineLimits {
	return PipelineLimits{
		MaxRetainedSourceBytes: 2 << 30,
		MaxCompileWorkingBytes: 4 << 30,
		MaxRuntimeWorkingBytes: DefaultMaxRuntimeWorkingBytes,
	}
}

func DefaultConfig(width, height int) Config {
	return Config{
		Width: width, Height: height,
		NativeLimits: nativepngsource.DefaultLimits(), ExpressionLimits: expressionlayer.DefaultLimits(),
		CompositorLimits: visualcompositor.DefaultLimits(), OutputLimits: color.DefaultOutputLimits(),
		PNGLimits: pngencode.DefaultLimits(), PipelineLimits: DefaultPipelineLimits(),
	}
}

// DefaultConfigV2 enables the additive pinned-font static Text provider while preserving every
// v1 native image/vector/output default. DefaultConfig intentionally remains Text-free.
func DefaultConfigV2(width, height int) Config {
	result := DefaultConfig(width, height)
	textConfig := defaultTextConfigV2()
	result.TextConfig = &textConfig
	result.PipelineLimits = defaultPipelineLimitsV2()
	return result
}

// SourceManifest binds every enabled provider cache to the same frozen document and declares the
// transition-aware expression extension that is intentionally absent from the frozen v1
// expression manifest. Text fields are nil/omitted in v1 and mandatory, even when empty, in v2.
type SourceManifest struct {
	SchemaVersion                   int                                  `json:"schema_version"`
	Profile                         string                               `json:"profile"`
	DocumentHash                    string                               `json:"document_hash"`
	NativePNG                       nativepngsource.Manifest             `json:"native_png"`
	Expression                      expressionlayer.Manifest             `json:"expression"`
	ExpressionHandleProfile         string                               `json:"expression_handle_profile"`
	ExpressionHandleContractVersion int                                  `json:"expression_handle_contract_version"`
	VectorTransitionScopeProfile    string                               `json:"vector_transition_scope_profile"`
	NativeRetainedSurfaceBytes      uint64                               `json:"native_retained_surface_bytes"`
	ExpressionRetainedSurfaceBytes  uint64                               `json:"expression_retained_surface_bytes"`
	Text                            *textlayer.Manifest                  `json:"text,omitempty"`
	TextProviderProfile             string                               `json:"text_provider_profile,omitempty"`
	TextContractVersion             int                                  `json:"text_contract_version,omitempty"`
	TextRetainedSurfaceBytes        uint64                               `json:"text_retained_surface_bytes,omitempty"`
	DomainScope                     *visualcompositor.DomainScopeBinding `json:"domain_scope,omitempty"`
	TotalRetainedSurfaceBytes       uint64                               `json:"total_retained_surface_bytes"`
}

// Program retains only immutable dependency programs/providers and value receipts.
type Program struct {
	documentHash string
	config       Config
	visual       *visualcompositor.Program
	native       *nativepngsource.Provider
	expression   *expressionlayer.Provider
	text         *textlayer.Provider
	v2           bool
	manifest     SourceManifest
	diagnostics  []documentv2.Diagnostic
	domainScope  *evaluator.DomainScope
}

func Compile(ctx context.Context, document documentv2.Document, bindings []Binding, config Config) (*Program, []documentv2.Diagnostic, error) {
	return compile(ctx, document, bindings, nil, config, false)
}

// CompileV2 adds the pinned-font static Text path. Font bindings remain explicit document-scoped
// authorization edges and are borrowed, together with their ShapeProviders, only until return.
func CompileV2(
	ctx context.Context,
	document documentv2.Document,
	pngBindings []Binding,
	fontBindings []textlayer.FontBinding,
	config Config,
) (*Program, []documentv2.Diagnostic, error) {
	return compile(ctx, document, pngBindings, fontBindings, config, true)
}

func compile(
	ctx context.Context,
	document documentv2.Document,
	bindings []Binding,
	fontBindings []textlayer.FontBinding,
	config Config,
	v2 bool,
) (*Program, []documentv2.Diagnostic, error) {
	return compileMode(ctx, document, bindings, fontBindings, config, v2, nil)
}

func compileMode(ctx context.Context, document documentv2.Document, bindings []Binding, fontBindings []textlayer.FontBinding, config Config, v2 bool, domain *evaluator.DomainScope) (*Program, []documentv2.Diagnostic, error) {
	if ctx == nil {
		return nil, nil, pipelineError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextError(ctx, "context"); err != nil {
		return nil, nil, err
	}
	normalized, visualConfig, err := normalizeConfigMode(config, v2)
	if err != nil {
		return nil, nil, err
	}
	if err := preflightBindings(ctx, bindings, normalized.NativeLimits); err != nil {
		return nil, nil, err
	}
	// The dependency's conservative typed-graph preflight must run before any wrapper-sized
	// canonical allocation. Its immutable Program is retained after the wrapper independently
	// freezes an equivalent value and cross-checks the hash used by both providers.
	preflightVisual, diagnostics := compileVisual(ctx, document, visualConfig, domain)
	diagnostics = cloneDiagnostics(diagnostics)
	if preflightVisual == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, classifyVisualCompile(ctx, diagnostics)
	}
	actualVisualConfig := preflightVisual.Config()
	if actualVisualConfig.Width != visualConfig.Width || actualVisualConfig.Height != visualConfig.Height ||
		actualVisualConfig.Source != visualConfig.Source || actualVisualConfig.NativeProfiles != visualConfig.NativeProfiles ||
		actualVisualConfig.BuildFingerprint != visualConfig.BuildFingerprint {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/visual_config", ErrStageMismatch)
	}
	normalized.CompositorLimits = actualVisualConfig.Limits
	visualConfig = actualVisualConfig
	if err := validateAggregateLimitsMode(normalized, v2); err != nil {
		return nil, diagnostics, err
	}
	if err := contextError(ctx, "document"); err != nil {
		return nil, diagnostics, err
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	canonicalLimit := minimumCanonicalLimitMode(normalized, v2)
	if uint64(len(canonical)) > canonicalLimit {
		return nil, diagnostics, pipelineError(CodeResourceLimit, "document/canonical_bytes", ErrResourceLimit)
	}
	documentHash, err := hashCanonical(ctx, canonical, "document/hash")
	if err != nil {
		return nil, diagnostics, err
	}
	frozen, err := documentv2.Decode(canonical)
	canonical = nil
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if preflightVisual.DocumentHash() != documentHash {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/preflight_visual", ErrStageMismatch)
	}
	if domain != nil {
		owned := preflightVisual.Scope()
		domain = &owned
	}
	visual := preflightVisual
	if visual.DocumentHash() != documentHash || visual.Config() != visualConfig {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/visual", ErrStageMismatch)
	}

	nativeBindings := make([]nativepngsource.Binding, len(bindings))
	for index := range bindings {
		if index&255 == 0 {
			if err := contextError(ctx, "bindings"); err != nil {
				return nil, diagnostics, err
			}
		}
		nativeBindings[index] = nativepngsource.Binding{DocumentHash: documentHash, AssetID: bindings[index].AssetID, EncodedPNG: bindings[index].EncodedPNG}
	}
	native, err := nativepngsource.Compile(ctx, frozen, nativeBindings, nativepngsource.Config{Limits: normalized.NativeLimits})
	if err != nil {
		return nil, diagnostics, classifyNativeCompile(err)
	}
	expression, err := expressionlayer.Compile(ctx, frozen, expressionlayer.Config{Limits: normalized.ExpressionLimits})
	if err != nil {
		return nil, diagnostics, classifyExpressionCompile(err)
	}
	var text *textlayer.Provider
	if v2 {
		text, err = textlayer.Compile(ctx, frozen, fontBindings, *normalized.TextConfig)
		if err != nil {
			return nil, diagnostics, classifyTextCompile(err)
		}
	}
	if err := contextError(ctx, "compile/manifests"); err != nil {
		return nil, diagnostics, err
	}
	nativeConfig := native.Config()
	expressionConfig := expression.Config()
	nativeManifest := native.Manifest()
	expressionManifest := expression.Manifest()
	var textManifest *textlayer.Manifest
	if text != nil {
		manifest := text.Manifest()
		textManifest = &manifest
	}
	manifestsValid := native.DocumentHash() == documentHash && expression.DocumentHash() == documentHash &&
		nativeConfig.Limits == normalized.NativeLimits && expressionConfig.Limits == normalized.ExpressionLimits &&
		validNativeManifest(ctx, nativeManifest, frozen, normalized.NativeLimits) &&
		validExpressionManifest(ctx, expressionManifest, frozen, normalized.ExpressionLimits)
	if v2 {
		manifestsValid = manifestsValid && text != nil && textManifest != nil && text.DocumentHash() == documentHash &&
			text.Config() == *normalized.TextConfig && validTextManifest(ctx, *textManifest, frozen, normalized.TextConfig.Limits)
	}
	if err := contextError(ctx, "compile/source_manifests"); err != nil {
		return nil, diagnostics, err
	}
	if !manifestsValid {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/source_manifests", ErrStageMismatch)
	}
	nativeRetained, nativeOK := nativeRetainedBytes(nativeManifest)
	expressionRetained, expressionOK := expressionRetainedBytes(expressionManifest)
	textRetained, textOK := uint64(0), true
	if textManifest != nil {
		textRetained, textOK = textRetainedBytes(*textManifest)
	}
	actualRetained, ok := checkedSum(nativeRetained, expressionRetained, textRetained)
	if !nativeOK || !expressionOK || !textOK || !ok || actualRetained > normalized.PipelineLimits.MaxRetainedSourceBytes {
		return nil, diagnostics, pipelineError(CodeResourceLimit, "compile/retained_source_bytes", ErrResourceLimit)
	}
	manifestProfile := ManifestProfileV1
	if v2 {
		manifestProfile = ManifestProfileV2
	}
	manifest := SourceManifest{
		SchemaVersion: ManifestSchemaVersion, Profile: manifestProfile, DocumentHash: documentHash,
		NativePNG: cloneNativeManifest(nativeManifest), Expression: cloneExpressionManifest(expressionManifest),
		ExpressionHandleProfile:         expressionlayer.TransitionHandleProfileV2,
		ExpressionHandleContractVersion: expressionlayer.TransitionHandleContractVersion,
		VectorTransitionScopeProfile:    documentv2.VectorTransitionScopeProfileV1,
		NativeRetainedSurfaceBytes:      nativeRetained, ExpressionRetainedSurfaceBytes: expressionRetained,
		TotalRetainedSurfaceBytes: actualRetained,
	}
	if textManifest != nil {
		cloned := cloneTextManifest(*textManifest)
		manifest.Text = &cloned
		manifest.TextProviderProfile = textlayer.ProfileV1
		manifest.TextContractVersion = textlayer.ContractVersion
		manifest.TextRetainedSurfaceBytes = textRetained
	}
	if domain != nil {
		manifest.Profile = DomainManifestProfileV1
		binding := preflightVisual.ScopeBinding()
		manifest.DomainScope = &binding
	}
	if !validCombinedManifestMode(manifest, normalized, v2) {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/source_manifest", ErrStageMismatch)
	}
	if err := contextError(ctx, "compile/publish"); err != nil {
		return nil, diagnostics, err
	}
	program := &Program{
		documentHash: documentHash, config: normalized, visual: visual, native: native, expression: expression,
		text: text, v2: v2, domainScope: domain, manifest: cloneSourceManifest(manifest), diagnostics: cloneDiagnostics(diagnostics),
	}
	if err := contextError(ctx, "compile/publish"); err != nil {
		return nil, diagnostics, err
	}
	return program, cloneDiagnostics(diagnostics), nil
}

func normalizeConfig(value Config) (Config, visualcompositor.Config, error) {
	return normalizeConfigMode(value, false)
}

func normalizeConfigMode(value Config, v2 bool) (Config, visualcompositor.Config, error) {
	result := value
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/dimensions", ErrConfigInvalid)
	}
	if result.NativeLimits == (nativepngsource.Limits{}) {
		result.NativeLimits = nativepngsource.DefaultLimits()
	} else {
		result.NativeLimits = fillNativeDefaults(result.NativeLimits)
	}
	if result.ExpressionLimits == (expressionlayer.Limits{}) {
		result.ExpressionLimits = expressionlayer.DefaultLimits()
	} else {
		result.ExpressionLimits = fillExpressionDefaults(result.ExpressionLimits)
	}
	if v2 {
		textConfig := defaultTextConfigV2()
		if result.TextConfig != nil {
			textConfig = fillTextDefaults(*result.TextConfig)
		}
		result.TextConfig = &textConfig
	} else if result.TextConfig != nil {
		return Config{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/text_config", ErrConfigInvalid)
	}
	if result.CompositorLimits == (visualcompositor.Limits{}) {
		result.CompositorLimits = visualcompositor.DefaultLimits()
	} else {
		defaults := visualcompositor.DefaultLimits()
		if result.CompositorLimits.Target == (cpucompositor.Limits{}) {
			result.CompositorLimits.Target = defaults.Target
		}
		if result.CompositorLimits.Raster == (cpuraster.Limits{}) {
			result.CompositorLimits.Raster = defaults.Raster
		}
	}
	if result.OutputLimits == (color.OutputLimits{}) {
		result.OutputLimits = color.DefaultOutputLimits()
	}
	if result.PNGLimits == (pngencode.Limits{}) {
		result.PNGLimits = pngencode.DefaultLimits()
	} else {
		result.PNGLimits = fillPNGDefaults(result.PNGLimits)
	}
	if result.PipelineLimits == (PipelineLimits{}) {
		if v2 {
			result.PipelineLimits = defaultPipelineLimitsV2()
		} else {
			result.PipelineLimits = DefaultPipelineLimits()
		}
	} else {
		defaults := DefaultPipelineLimits()
		if v2 {
			defaults = defaultPipelineLimitsV2()
		}
		if result.PipelineLimits.MaxRetainedSourceBytes == 0 {
			result.PipelineLimits.MaxRetainedSourceBytes = defaults.MaxRetainedSourceBytes
		}
		if result.PipelineLimits.MaxCompileWorkingBytes == 0 {
			result.PipelineLimits.MaxCompileWorkingBytes = defaults.MaxCompileWorkingBytes
		}
		if result.PipelineLimits.MaxRuntimeWorkingBytes == 0 {
			result.PipelineLimits.MaxRuntimeWorkingBytes = defaults.MaxRuntimeWorkingBytes
		}
	}
	if err := validateProviderLimits(result.NativeLimits, result.ExpressionLimits); err != nil {
		return Config{}, visualcompositor.Config{}, err
	}
	if v2 && !validTextProviderLimits(result.TextConfig.Limits) {
		return Config{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/text_config/limits", ErrConfigInvalid)
	}
	if err := validateOutputAndPNG(result); err != nil {
		return Config{}, visualcompositor.Config{}, err
	}
	visualLimits := result.CompositorLimits
	if result.NativeLimits.MaxCanonicalDocumentBytes < visualLimits.MaxCanonicalDocumentBytes {
		visualLimits.MaxCanonicalDocumentBytes = result.NativeLimits.MaxCanonicalDocumentBytes
	}
	if result.ExpressionLimits.MaxCanonicalDocumentBytes < visualLimits.MaxCanonicalDocumentBytes {
		visualLimits.MaxCanonicalDocumentBytes = result.ExpressionLimits.MaxCanonicalDocumentBytes
	}
	if v2 && result.TextConfig.Limits.MaxCanonicalDocumentBytes < visualLimits.MaxCanonicalDocumentBytes {
		visualLimits.MaxCanonicalDocumentBytes = result.TextConfig.Limits.MaxCanonicalDocumentBytes
	}
	if result.NativeLimits.MaxDocumentNodes < visualLimits.MaxDocumentNodes {
		visualLimits.MaxDocumentNodes = result.NativeLimits.MaxDocumentNodes
	}
	if result.ExpressionLimits.MaxDocumentNodes < visualLimits.MaxDocumentNodes {
		visualLimits.MaxDocumentNodes = result.ExpressionLimits.MaxDocumentNodes
	}
	if v2 && result.TextConfig.Limits.MaxDocumentNodes < visualLimits.MaxDocumentNodes {
		visualLimits.MaxDocumentNodes = result.TextConfig.Limits.MaxDocumentNodes
	}
	if result.NativeLimits.MaxTracksPerTimeline < visualLimits.MaxTracksPerTimeline {
		visualLimits.MaxTracksPerTimeline = result.NativeLimits.MaxTracksPerTimeline
	}
	if result.ExpressionLimits.MaxTracksPerTimeline < visualLimits.MaxTracksPerTimeline {
		visualLimits.MaxTracksPerTimeline = result.ExpressionLimits.MaxTracksPerTimeline
	}
	if v2 && result.TextConfig.Limits.MaxTracksPerTimeline < visualLimits.MaxTracksPerTimeline {
		visualLimits.MaxTracksPerTimeline = result.TextConfig.Limits.MaxTracksPerTimeline
	}
	result.CompositorLimits = visualLimits
	visualConfig := visualcompositor.Config{
		Width: result.Width, Height: result.Height, Limits: visualLimits,
		Source:           visualcompositor.SourceCapabilities{Image: true, VectorExpression: true, StaticTextV2: v2},
		NativeProfiles:   rastercompositor.SourceProfiles{Image: nativepngsource.ImageSourceProfiles()},
		BuildFingerprint: result.BuildFingerprint,
	}
	if _, err := visualcompositor.Capabilities(visualConfig); err != nil {
		if errors.Is(err, visualcompositor.ErrResourceLimit) {
			return Config{}, visualcompositor.Config{}, pipelineError(CodeResourceLimit, "config/compositor_limits", errors.Join(ErrResourceLimit, err))
		}
		return Config{}, visualcompositor.Config{}, pipelineError(CodeConfigInvalid, "config/compositor_limits", errors.Join(ErrConfigInvalid, err))
	}
	if err := validateAggregateLimitsMode(result, v2); err != nil {
		return Config{}, visualcompositor.Config{}, err
	}
	return result, visualConfig, nil
}

func fillTextDefaults(value textlayer.Config) textlayer.Config {
	defaults := defaultTextConfigV2()
	if value.Limits == (textlayer.Limits{}) {
		return defaults
	}
	limits := &value.Limits
	if limits.MaxBindings == 0 {
		limits.MaxBindings = defaults.Limits.MaxBindings
	}
	if limits.MaxClipScopes == 0 {
		limits.MaxClipScopes = defaults.Limits.MaxClipScopes
	}
	if limits.MaxTextAssets == 0 {
		limits.MaxTextAssets = defaults.Limits.MaxTextAssets
	}
	if limits.MaxFontAssets == 0 {
		limits.MaxFontAssets = defaults.Limits.MaxFontAssets
	}
	if limits.MaxDocumentNodes == 0 {
		limits.MaxDocumentNodes = defaults.Limits.MaxDocumentNodes
	}
	if limits.MaxTracksPerTimeline == 0 {
		limits.MaxTracksPerTimeline = defaults.Limits.MaxTracksPerTimeline
	}
	if limits.MaxCanonicalDocumentBytes == 0 {
		limits.MaxCanonicalDocumentBytes = defaults.Limits.MaxCanonicalDocumentBytes
	}
	if limits.MaxTotalStaticSurfaceBytes == 0 {
		limits.MaxTotalStaticSurfaceBytes = defaults.Limits.MaxTotalStaticSurfaceBytes
	}
	if limits.MaxCompileWorkingBytes == 0 {
		limits.MaxCompileWorkingBytes = defaults.Limits.MaxCompileWorkingBytes
	}
	if limits.FrozenText == (evaluator.FrozenTextSourceLimits{}) {
		limits.FrozenText = defaults.Limits.FrozenText
	}
	if limits.Assembly == (textassembly.Limits{}) {
		limits.Assembly = defaults.Limits.Assembly
	}
	if limits.Raster == (textassembly.RasterLimits{}) {
		limits.Raster = defaults.Limits.Raster
	}
	return value
}

func validateProviderLimits(native nativepngsource.Limits, expression expressionlayer.Limits) error {
	if native.MaxBindings <= 0 || native.MaxBindings > nativepngsource.HardMaxBindings ||
		native.MaxClipScopes <= 0 || native.MaxClipScopes > nativepngsource.HardMaxClipScopes ||
		native.MaxDocumentNodes <= 0 || native.MaxDocumentNodes > nativepngsource.HardMaxDocumentNodes ||
		native.MaxTracksPerTimeline <= 0 || native.MaxTracksPerTimeline > nativepngsource.HardMaxTracksPerTimeline ||
		native.MaxRetainedBytes < cpucompositor.BytesPerPixel || native.MaxRetainedBytes > nativepngsource.HardMaxRetainedBytes ||
		native.MaxTotalEncodedBytes < 8 || native.MaxTotalEncodedBytes > nativepngsource.HardMaxTotalEncoded ||
		native.MaxCanonicalDocumentBytes == 0 || native.MaxCanonicalDocumentBytes > nativepngsource.HardMaxDocumentBytes ||
		native.PNG.MaxWidth <= 0 || native.PNG.MaxHeight <= 0 || native.PNG.MaxEncodedBytes < 8 ||
		native.PNG.MaxEncodedBytes > pngdecode.HardMaxEncodedBytes || native.PNG.MaxWorkingBytes < 30 ||
		native.PNG.MaxWorkingBytes > pngdecode.HardMaxWorkingBytes {
		return pipelineError(CodeConfigInvalid, "config/native_limits", ErrConfigInvalid)
	}
	if expression.MaxClipScopes <= 0 || expression.MaxClipScopes > expressionlayer.HardMaxClipScopes ||
		expression.MaxExpressionAssets <= 0 || expression.MaxExpressionAssets > expressionlayer.HardMaxExpressionAssets ||
		expression.MaxDocumentNodes <= 0 || expression.MaxDocumentNodes > expressionlayer.HardMaxDocumentNodes ||
		expression.MaxTracksPerTimeline <= 0 || expression.MaxTracksPerTimeline > expressionlayer.HardMaxTracksPerTimeline ||
		expression.MaxTotalPaths == 0 || expression.MaxTotalPaths > expressionlayer.HardMaxTotalPaths ||
		expression.MaxTotalCommands == 0 || expression.MaxTotalCommands > expressionlayer.HardMaxTotalCommands ||
		expression.MaxTotalEdges == 0 || expression.MaxTotalEdges > expressionlayer.HardMaxTotalEdges ||
		expression.MaxRetainedBytes < cpucompositor.BytesPerPixel || expression.MaxRetainedBytes > expressionlayer.HardMaxRetainedBytes ||
		expression.MaxCanonicalDocumentBytes == 0 || expression.MaxCanonicalDocumentBytes > expressionlayer.HardMaxDocumentBytes ||
		vectorraster.ValidateLimits(expression.Vector) != nil || expression.Vector.MaxRetainedBytes > expression.MaxRetainedBytes {
		return pipelineError(CodeConfigInvalid, "config/expression_limits", ErrConfigInvalid)
	}
	return nil
}

func validTextProviderLimits(limits textlayer.Limits) bool {
	return limits.MaxBindings > 0 && limits.MaxBindings <= textlayer.HardMaxBindings &&
		limits.MaxClipScopes > 0 && limits.MaxClipScopes <= textlayer.HardMaxClipScopes &&
		limits.MaxTextAssets > 0 && limits.MaxTextAssets <= textlayer.HardMaxTextAssets &&
		limits.MaxFontAssets > 0 && limits.MaxFontAssets <= textlayer.HardMaxFontAssets &&
		limits.MaxDocumentNodes > 0 && limits.MaxDocumentNodes <= textlayer.HardMaxDocumentNodes &&
		limits.MaxTracksPerTimeline > 0 && limits.MaxTracksPerTimeline <= textlayer.HardMaxTracksPerTimeline &&
		limits.MaxCanonicalDocumentBytes > 0 && limits.MaxCanonicalDocumentBytes <= textlayer.HardMaxCanonicalDocumentBytes &&
		limits.MaxTotalStaticSurfaceBytes >= cpucompositor.BytesPerPixel && limits.MaxTotalStaticSurfaceBytes <= textlayer.HardMaxTotalSurfaceBytes &&
		limits.MaxCompileWorkingBytes > 0 && limits.MaxCompileWorkingBytes <= textlayer.HardMaxCompileWorkingBytes
}

func fillNativeDefaults(value nativepngsource.Limits) nativepngsource.Limits {
	defaults := nativepngsource.DefaultLimits()
	if value.MaxBindings == 0 {
		value.MaxBindings = defaults.MaxBindings
	}
	if value.MaxClipScopes == 0 {
		value.MaxClipScopes = defaults.MaxClipScopes
	}
	if value.MaxDocumentNodes == 0 {
		value.MaxDocumentNodes = defaults.MaxDocumentNodes
	}
	if value.MaxTracksPerTimeline == 0 {
		value.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
	}
	if value.MaxRetainedBytes == 0 {
		value.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.MaxTotalEncodedBytes == 0 {
		value.MaxTotalEncodedBytes = defaults.MaxTotalEncodedBytes
	}
	if value.MaxCanonicalDocumentBytes == 0 {
		value.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
	}
	if value.PNG == (pngdecode.Limits{}) {
		value.PNG = defaults.PNG
	}
	return value
}

func fillExpressionDefaults(value expressionlayer.Limits) expressionlayer.Limits {
	defaults := expressionlayer.DefaultLimits()
	if value.MaxClipScopes == 0 {
		value.MaxClipScopes = defaults.MaxClipScopes
	}
	if value.MaxExpressionAssets == 0 {
		value.MaxExpressionAssets = defaults.MaxExpressionAssets
	}
	if value.MaxDocumentNodes == 0 {
		value.MaxDocumentNodes = defaults.MaxDocumentNodes
	}
	if value.MaxTracksPerTimeline == 0 {
		value.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
	}
	if value.MaxTotalPaths == 0 {
		value.MaxTotalPaths = defaults.MaxTotalPaths
	}
	if value.MaxTotalCommands == 0 {
		value.MaxTotalCommands = defaults.MaxTotalCommands
	}
	if value.MaxTotalEdges == 0 {
		value.MaxTotalEdges = defaults.MaxTotalEdges
	}
	if value.MaxRetainedBytes == 0 {
		value.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.MaxCanonicalDocumentBytes == 0 {
		value.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
	}
	if value.Vector == (vectorraster.Limits{}) {
		value.Vector = defaults.Vector
	}
	return value
}

func fillPNGDefaults(value pngencode.Limits) pngencode.Limits {
	defaults := pngencode.DefaultLimits()
	if value.MaxWidth == 0 {
		value.MaxWidth = defaults.MaxWidth
	}
	if value.MaxHeight == 0 {
		value.MaxHeight = defaults.MaxHeight
	}
	if value.MaxRawRGBABytes == 0 {
		value.MaxRawRGBABytes = defaults.MaxRawRGBABytes
	}
	if value.MaxArtifactBytes == 0 {
		value.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	return value
}

func preflightBindings(ctx context.Context, bindings []Binding, limits nativepngsource.Limits) error {
	if len(bindings) > limits.MaxBindings {
		return pipelineError(CodeResourceLimit, "bindings/count", ErrResourceLimit)
	}
	var encoded uint64
	for index := range bindings {
		if index&1023 == 0 {
			if err := contextError(ctx, "bindings"); err != nil {
				return err
			}
		}
		var ok bool
		encoded, ok = checkedAdd(encoded, uint64(len(bindings[index].EncodedPNG)))
		if !ok || encoded > limits.MaxTotalEncodedBytes {
			return pipelineError(CodeResourceLimit, "bindings/encoded_bytes", ErrResourceLimit)
		}
	}
	return nil
}

func validateAggregateLimits(config Config) error {
	return validateAggregateLimitsMode(config, false)
}

func validateAggregateLimitsMode(config Config, v2 bool) error {
	pipeline := config.PipelineLimits
	if pipeline.MaxRetainedSourceBytes == 0 || pipeline.MaxCompileWorkingBytes == 0 || pipeline.MaxRuntimeWorkingBytes == 0 ||
		pipeline.MaxRetainedSourceBytes > HardMaxPipelineBytes || pipeline.MaxCompileWorkingBytes > HardMaxPipelineBytes || pipeline.MaxRuntimeWorkingBytes > HardMaxPipelineBytes {
		return pipelineError(CodeConfigInvalid, "config/pipeline_limits", ErrConfigInvalid)
	}
	textRetained := uint64(0)
	if v2 {
		if config.TextConfig == nil {
			return pipelineError(CodeConfigInvalid, "config/text_config", ErrConfigInvalid)
		}
		textRetained = config.TextConfig.Limits.MaxTotalStaticSurfaceBytes
	}
	retained, ok := checkedSum(config.NativeLimits.MaxRetainedBytes, config.ExpressionLimits.MaxRetainedBytes, textRetained)
	if !ok || retained > pipeline.MaxRetainedSourceBytes {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/retained", ErrResourceLimit)
	}
	canonical := minimumCanonicalLimitMode(config, v2)
	nodes := uint64(config.CompositorLimits.MaxDocumentNodes)
	if uint64(config.NativeLimits.MaxDocumentNodes) < nodes {
		nodes = uint64(config.NativeLimits.MaxDocumentNodes)
	}
	if uint64(config.ExpressionLimits.MaxDocumentNodes) < nodes {
		nodes = uint64(config.ExpressionLimits.MaxDocumentNodes)
	}
	if v2 && uint64(config.TextConfig.Limits.MaxDocumentNodes) < nodes {
		nodes = uint64(config.TextConfig.Limits.MaxDocumentNodes)
	}
	metadata, ok := checkedMul(nodes, metadataBytesPerDocumentNode)
	if !ok {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/metadata", ErrResourceLimit)
	}
	canonicalWorking, ok := checkedMul(canonical, canonicalCompileMultiplier)
	if !ok {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/canonical", ErrResourceLimit)
	}
	transient := config.NativeLimits.PNG.MaxWorkingBytes
	if config.ExpressionLimits.Vector.MaxWorkingBytes > transient {
		transient = config.ExpressionLimits.Vector.MaxWorkingBytes
	}
	compilePeak, ok := checkedSum(retained, transient, canonicalWorking, metadata)
	if !ok || compilePeak > pipeline.MaxCompileWorkingBytes {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/compile", ErrResourceLimit)
	}
	if v2 {
		// textlayer's MaxCompileWorkingBytes already includes its canonical snapshot, Text cached
		// surfaces and worst sequential bind/assembly/raster stage. Native and expression caches and
		// wrapper metadata remain live while that final provider compiles.
		textCompilePeak, textOK := checkedSum(
			config.NativeLimits.MaxRetainedBytes,
			config.ExpressionLimits.MaxRetainedBytes,
			config.TextConfig.Limits.MaxCompileWorkingBytes,
			metadata,
		)
		if !textOK || textCompilePeak > pipeline.MaxCompileWorkingBytes {
			return pipelineError(CodeResourceLimit, "config/pipeline_limits/text_compile", ErrResourceLimit)
		}
	}
	frameBytes, outputBytes, artifactBytes, rowBytes, ok := configuredRuntimeSizes(config)
	if !ok {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/runtime_shape", ErrResourceLimit)
	}
	visualPeak, ok := checkedMul(frameBytes, 6)
	if !ok || visualPeak > config.CompositorLimits.MaxTargetWorkingBytes {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/runtime_visual", ErrResourceLimit)
	}
	colorPeak, ok := checkedAdd(frameBytes, outputBytes)
	if !ok {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/runtime_color", ErrResourceLimit)
	}
	pngPeak, ok := checkedSum(frameBytes, outputBytes, artifactBytes, rowBytes)
	if !ok {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/runtime_png", ErrResourceLimit)
	}
	stagePeak := visualPeak
	if colorPeak > stagePeak {
		stagePeak = colorPeak
	}
	if pngPeak > stagePeak {
		stagePeak = pngPeak
	}
	runtimePeak, ok := checkedSum(retained, metadata, stagePeak)
	if !ok || runtimePeak > pipeline.MaxRuntimeWorkingBytes {
		return pipelineError(CodeResourceLimit, "config/pipeline_limits/runtime", ErrResourceLimit)
	}
	return nil
}

func configuredRuntimeSizes(config Config) (frameBytes, outputBytes, artifactBytes, rowBytes uint64, ok bool) {
	width, height := uint64(config.Width), uint64(config.Height)
	pixels, ok := checkedMul(width, height)
	if !ok {
		return 0, 0, 0, 0, false
	}
	frameBytes, ok = checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok {
		return 0, 0, 0, 0, false
	}
	outputBytes, ok = checkedMul(pixels, color.BytesPerOutputRGBA8Pixel)
	if !ok {
		return 0, 0, 0, 0, false
	}
	scanlineBytes, ok := checkedAdd(outputBytes, height)
	if !ok {
		return 0, 0, 0, 0, false
	}
	blocks := scanlineBytes / pngencode.StoredBlockMaxBytes
	if scanlineBytes%pngencode.StoredBlockMaxBytes != 0 {
		blocks++
	}
	blockBytes, ok := checkedMul(blocks, 5)
	if !ok {
		return 0, 0, 0, 0, false
	}
	idatBytes, ok := checkedSum(pngencode.ZlibFixedBytes, scanlineBytes, blockBytes)
	if !ok {
		return 0, 0, 0, 0, false
	}
	artifactBytes, ok = checkedAdd(idatBytes, pngencode.FixedArtifactBytes)
	if !ok {
		return 0, 0, 0, 0, false
	}
	rowBytes, ok = checkedMul(width, color.BytesPerOutputRGBA8Pixel)
	return frameBytes, outputBytes, artifactBytes, rowBytes, ok
}

func validateOutputAndPNG(config Config) error {
	output := config.OutputLimits
	if output.MaxWidth <= 0 || output.MaxHeight <= 0 || output.MaxOutputBytes < color.BytesPerOutputRGBA8Pixel ||
		output.MaxWidth > color.HardMaxOutputDimension || output.MaxHeight > color.HardMaxOutputDimension ||
		output.MaxOutputBytes > color.HardMaxOutputRGBA8Bytes {
		return pipelineError(CodeConfigInvalid, "config/output_limits", ErrConfigInvalid)
	}
	png := config.PNGLimits
	if png.MaxWidth <= 0 || png.MaxHeight <= 0 || png.MaxWidth > pngencode.HardMaxDimension || png.MaxHeight > pngencode.HardMaxDimension ||
		png.MaxRawRGBABytes < color.BytesPerOutputRGBA8Pixel || png.MaxRawRGBABytes > pngencode.HardMaxRawRGBABytes ||
		png.MaxArtifactBytes < pngencode.MinimumArtifactBytes || png.MaxArtifactBytes > pngencode.HardMaxArtifactBytes ||
		png.MaxWorkingBytes < pngencode.MinimumWorkingBytes || png.MaxWorkingBytes > pngencode.HardMaxWorkingBytes {
		return pipelineError(CodeConfigInvalid, "config/png_limits", ErrConfigInvalid)
	}
	if config.Width > output.MaxWidth || config.Height > output.MaxHeight || config.Width > png.MaxWidth || config.Height > png.MaxHeight ||
		config.Width > math.MaxInt32 || config.Height > math.MaxInt32 {
		return pipelineError(CodeResourceLimit, "config/dimensions", ErrResourceLimit)
	}
	_, raw, artifact, row, ok := configuredRuntimeSizes(config)
	if !ok || raw > output.MaxOutputBytes || raw > png.MaxRawRGBABytes || artifact > png.MaxArtifactBytes || artifact > uint64(maxInt()) {
		return pipelineError(CodeResourceLimit, "config/output_layout", ErrResourceLimit)
	}
	working, ok := checkedAdd(artifact, row)
	if !ok || working > png.MaxWorkingBytes {
		return pipelineError(CodeResourceLimit, "config/png_working_bytes", ErrResourceLimit)
	}
	return nil
}

func minimumCanonicalLimit(config Config) uint64 {
	return minimumCanonicalLimitMode(config, false)
}

func minimumCanonicalLimitMode(config Config, v2 bool) uint64 {
	result := config.NativeLimits.MaxCanonicalDocumentBytes
	if config.ExpressionLimits.MaxCanonicalDocumentBytes < result {
		result = config.ExpressionLimits.MaxCanonicalDocumentBytes
	}
	if config.CompositorLimits.MaxCanonicalDocumentBytes < result {
		result = config.CompositorLimits.MaxCanonicalDocumentBytes
	}
	if v2 && config.TextConfig != nil && config.TextConfig.Limits.MaxCanonicalDocumentBytes < result {
		result = config.TextConfig.Limits.MaxCanonicalDocumentBytes
	}
	return result
}

func (program *Program) DocumentHash() string {
	if program == nil {
		return ""
	}
	return program.documentHash
}

// Profile returns the immutable export profile selected at compile time.
func (program *Program) Profile() string {
	if program == nil {
		return ""
	}
	return program.profile()
}

func (program *Program) Config() Config {
	if program == nil {
		return Config{}
	}
	return cloneConfig(program.config)
}
func (program *Program) Diagnostics() []documentv2.Diagnostic {
	if program == nil {
		return nil
	}
	return cloneDiagnostics(program.diagnostics)
}
func (program *Program) SourceManifest() SourceManifest {
	if program == nil {
		return SourceManifest{}
	}
	return cloneSourceManifest(program.manifest)
}

func cloneSourceManifest(value SourceManifest) SourceManifest {
	if value.DomainScope != nil {
		cloned := cloneScopeBinding(*value.DomainScope)
		value.DomainScope = &cloned
	}
	value.NativePNG = cloneNativeManifest(value.NativePNG)
	value.Expression = cloneExpressionManifest(value.Expression)
	if value.Text != nil {
		cloned := cloneTextManifest(*value.Text)
		value.Text = &cloned
	}
	return value
}

func cloneConfig(value Config) Config {
	if value.TextConfig != nil {
		cloned := *value.TextConfig
		value.TextConfig = &cloned
	}
	return value
}

func equalConfig(left, right Config) bool {
	if (left.TextConfig == nil) != (right.TextConfig == nil) {
		return false
	}
	if left.TextConfig != nil && *left.TextConfig != *right.TextConfig {
		return false
	}
	left.TextConfig, right.TextConfig = nil, nil
	return left == right
}
func cloneNativeManifest(value nativepngsource.Manifest) nativepngsource.Manifest {
	value.Assets = append([]nativepngsource.AssetReceipt(nil), value.Assets...)
	return value
}
func cloneExpressionManifest(value expressionlayer.Manifest) expressionlayer.Manifest {
	value.Assets = append([]expressionlayer.AssetReceipt(nil), value.Assets...)
	return value
}

func cloneTextManifest(value textlayer.Manifest) textlayer.Manifest {
	value.Assets = append([]textlayer.AssetReceipt(nil), value.Assets...)
	return value
}

func cloneDiagnostics(values []documentv2.Diagnostic) []documentv2.Diagnostic {
	if values == nil {
		return nil
	}
	result := make([]documentv2.Diagnostic, len(values))
	for i := range values {
		result[i] = values[i]
		result[i].Fixes = append([]videoedit.CandidateFix(nil), values[i].Fixes...)
		for j := range result[i].Fixes {
			result[i].Fixes[j].Operation = append([]byte(nil), values[i].Fixes[j].Operation...)
			result[i].Fixes[j].SideEffects = append([]string(nil), values[i].Fixes[j].SideEffects...)
		}
	}
	return result
}

func hashCanonical(ctx context.Context, bytes []byte, path string) (string, error) {
	digest := sha256.New()
	for offset := 0; offset < len(bytes); {
		if err := contextError(ctx, path); err != nil {
			return "", err
		}
		end := offset + 64<<10
		if end > len(bytes) {
			end = len(bytes)
		}
		_, _ = digest.Write(bytes[offset:end])
		offset = end
	}
	if err := contextError(ctx, path); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func contextError(ctx context.Context, path string) error {
	if ctx == nil {
		return pipelineError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return pipelineError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func checkedAdd(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}
func checkedMul(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}
func checkedSum(values ...uint64) (uint64, bool) {
	var result uint64
	for _, value := range values {
		var ok bool
		result, ok = checkedAdd(result, value)
		if !ok {
			return 0, false
		}
	}
	return result, true
}
func maxInt() int { return int(^uint(0) >> 1) }

func canonicalSHA256(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, c := range value[7:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func nativeRetainedBytes(value nativepngsource.Manifest) (uint64, bool) {
	var result uint64
	for _, asset := range value.Assets {
		if asset.Width <= 0 || asset.Height <= 0 {
			return 0, false
		}
		pixels, ok := checkedMul(uint64(asset.Width), uint64(asset.Height))
		if !ok {
			return 0, false
		}
		bytes, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
		if !ok {
			return 0, false
		}
		result, ok = checkedAdd(result, bytes)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func expressionRetainedBytes(value expressionlayer.Manifest) (uint64, bool) {
	var result uint64
	for _, asset := range value.Assets {
		var ok bool
		result, ok = checkedAdd(result, asset.SurfaceBytes)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func textRetainedBytes(value textlayer.Manifest) (uint64, bool) {
	var result uint64
	for _, asset := range value.Assets {
		var ok bool
		result, ok = checkedAdd(result, asset.SurfaceBytes)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func validCombinedManifest(value SourceManifest, config Config) bool {
	return validCombinedManifestMode(value, config, false)
}

func validCombinedManifestMode(value SourceManifest, config Config, v2 bool) bool {
	nativeRetained, nativeOK := nativeRetainedBytes(value.NativePNG)
	expressionRetained, expressionOK := expressionRetainedBytes(value.Expression)
	textRetained, textOK := uint64(0), true
	if value.Text != nil {
		textRetained, textOK = textRetainedBytes(*value.Text)
	}
	retained, retainedOK := checkedSum(nativeRetained, expressionRetained, textRetained)
	if !nativeOK || !expressionOK || !textOK || !retainedOK || retained > config.PipelineLimits.MaxRetainedSourceBytes {
		return false
	}
	textAssets := 0
	if value.Text != nil {
		textAssets = len(value.Text.Assets)
	}
	seenIDs := make(map[documentv2.ID]struct{}, len(value.NativePNG.Assets)+len(value.Expression.Assets)+textAssets)
	seenIndexes := make(map[int]struct{}, len(value.NativePNG.Assets)+len(value.Expression.Assets)+textAssets)
	for _, asset := range value.NativePNG.Assets {
		if _, duplicate := seenIDs[asset.AssetID]; duplicate {
			return false
		}
		if _, duplicate := seenIndexes[asset.AssetIndex]; duplicate {
			return false
		}
		seenIDs[asset.AssetID] = struct{}{}
		seenIndexes[asset.AssetIndex] = struct{}{}
	}
	for _, asset := range value.Expression.Assets {
		if _, duplicate := seenIDs[asset.AssetID]; duplicate {
			return false
		}
		if _, duplicate := seenIndexes[asset.AssetIndex]; duplicate {
			return false
		}
		seenIDs[asset.AssetID] = struct{}{}
		seenIndexes[asset.AssetIndex] = struct{}{}
	}
	if value.Text != nil {
		for _, asset := range value.Text.Assets {
			if _, duplicate := seenIDs[asset.AssetID]; duplicate {
				return false
			}
			if _, duplicate := seenIndexes[asset.AssetIndex]; duplicate {
				return false
			}
			seenIDs[asset.AssetID] = struct{}{}
			seenIndexes[asset.AssetIndex] = struct{}{}
		}
	}
	expectedProfile := ManifestProfileV1
	textFieldsValid := value.Text == nil && value.TextProviderProfile == "" && value.TextContractVersion == 0 && value.TextRetainedSurfaceBytes == 0
	if v2 {
		expectedProfile = ManifestProfileV2
		textFieldsValid = value.Text != nil && value.Text.DocumentHash == value.DocumentHash &&
			value.TextProviderProfile == textlayer.ProfileV1 && value.TextContractVersion == textlayer.ContractVersion &&
			value.TextRetainedSurfaceBytes == textRetained
	}
	if value.DomainScope != nil {
		if !v2 || !validDomainManifestScope(*value.DomainScope) {
			return false
		}
		expectedProfile = DomainManifestProfileV1
	}
	return textFieldsValid && value.SchemaVersion == ManifestSchemaVersion && value.Profile == expectedProfile && canonicalSHA256(value.DocumentHash) &&
		value.NativePNG.DocumentHash == value.DocumentHash && value.Expression.DocumentHash == value.DocumentHash &&
		value.ExpressionHandleProfile == expressionlayer.TransitionHandleProfileV2 &&
		value.ExpressionHandleContractVersion == expressionlayer.TransitionHandleContractVersion &&
		value.VectorTransitionScopeProfile == documentv2.VectorTransitionScopeProfileV1 &&
		value.NativeRetainedSurfaceBytes == nativeRetained && value.ExpressionRetainedSurfaceBytes == expressionRetained &&
		value.TotalRetainedSurfaceBytes == retained
}

func equalSourceManifest(left, right SourceManifest) bool {
	if !equalScopeBindings(left.DomainScope, right.DomainScope) || left.SchemaVersion != right.SchemaVersion || left.Profile != right.Profile || left.DocumentHash != right.DocumentHash ||
		left.ExpressionHandleProfile != right.ExpressionHandleProfile || left.ExpressionHandleContractVersion != right.ExpressionHandleContractVersion ||
		left.VectorTransitionScopeProfile != right.VectorTransitionScopeProfile || left.NativeRetainedSurfaceBytes != right.NativeRetainedSurfaceBytes ||
		left.ExpressionRetainedSurfaceBytes != right.ExpressionRetainedSurfaceBytes || left.TextProviderProfile != right.TextProviderProfile ||
		left.TextContractVersion != right.TextContractVersion || left.TextRetainedSurfaceBytes != right.TextRetainedSurfaceBytes ||
		left.TotalRetainedSurfaceBytes != right.TotalRetainedSurfaceBytes ||
		!equalNativeManifest(left.NativePNG, right.NativePNG) || !equalExpressionManifest(left.Expression, right.Expression) {
		return false
	}
	if (left.Text == nil) != (right.Text == nil) {
		return false
	}
	return left.Text == nil || equalTextManifest(*left.Text, *right.Text)
}

func equalNativeManifest(left, right nativepngsource.Manifest) bool {
	if left.SchemaVersion != right.SchemaVersion || left.Profile != right.Profile || left.DocumentHash != right.DocumentHash || left.SourceContractVersion != right.SourceContractVersion || left.SourceProfile != right.SourceProfile || left.SurfaceProfile != right.SurfaceProfile || len(left.Assets) != len(right.Assets) {
		return false
	}
	for i := range left.Assets {
		if left.Assets[i] != right.Assets[i] {
			return false
		}
	}
	return true
}
func equalExpressionManifest(left, right expressionlayer.Manifest) bool {
	if left.SchemaVersion != right.SchemaVersion || left.Profile != right.Profile || left.ContractVersion != right.ContractVersion || left.DocumentHash != right.DocumentHash || left.SourceProfile != right.SourceProfile || left.SelectionProfile != right.SelectionProfile || left.AssetSemanticHashProfile != right.AssetSemanticHashProfile || left.ExpressionProfile != right.ExpressionProfile || left.EvaluatorProfile != right.EvaluatorProfile || left.RasterProfile != right.RasterProfile || left.PaintProfile != right.PaintProfile || left.KernelProfile != right.KernelProfile || left.SurfaceProfile != right.SurfaceProfile || left.PixelHashProfile != right.PixelHashProfile || len(left.Assets) != len(right.Assets) {
		return false
	}
	for i := range left.Assets {
		if left.Assets[i] != right.Assets[i] {
			return false
		}
	}
	return true
}

func equalTextManifest(left, right textlayer.Manifest) bool {
	if left.SchemaVersion != right.SchemaVersion || left.Profile != right.Profile || left.ContractVersion != right.ContractVersion ||
		left.DocumentHash != right.DocumentHash || left.SourceProfile != right.SourceProfile || left.SelectionProfile != right.SelectionProfile ||
		left.TextProfile != right.TextProfile || left.EvaluatorProfile != right.EvaluatorProfile || left.RasterProfile != right.RasterProfile ||
		left.PaintProfile != right.PaintProfile || left.SurfaceProfile != right.SurfaceProfile || left.PixelHashProfile != right.PixelHashProfile ||
		left.RetainedSurfaceBytes != right.RetainedSurfaceBytes || len(left.Assets) != len(right.Assets) {
		return false
	}
	for index := range left.Assets {
		if left.Assets[index] != right.Assets[index] {
			return false
		}
	}
	return true
}

func validNativeManifest(ctx context.Context, value nativepngsource.Manifest, document documentv2.Document, limits nativepngsource.Limits) bool {
	if ctx == nil || value.SchemaVersion != nativepngsource.ManifestSchemaVersion || value.Profile != nativepngsource.ProfileV1 || !canonicalSHA256(value.DocumentHash) || value.SourceContractVersion != rastercompositor.NativeSourceContractVersion || value.SourceProfile != rastercompositor.SourceProfileV1 || value.SurfaceProfile != cpucompositor.SurfaceProfileV1 || len(value.Assets) > limits.MaxBindings {
		return false
	}
	required := enabledAssets(document, documentv2.TrackKindVideo, documentv2.AssetKindImage)
	if len(required) != len(value.Assets) {
		return false
	}
	profiles := nativepngsource.ImageSourceProfiles()
	seenIndexes := make(map[int]struct{}, len(value.Assets))
	previous := ""
	var retained uint64
	for index, receipt := range value.Assets {
		if index&255 == 0 && ctx.Err() != nil {
			return false
		}
		asset, ok := required[receipt.AssetID]
		_, duplicateIndex := seenIndexes[receipt.AssetIndex]
		if !ok || string(receipt.AssetID) <= previous || receipt.AssetIndex < 0 || receipt.AssetIndex >= len(document.Assets) || duplicateIndex ||
			document.Assets[receipt.AssetIndex].ID != receipt.AssetID || asset.Kind != documentv2.AssetKindImage ||
			asset.ContentHash != receipt.EncodedContentHash || receipt.Width <= 0 || receipt.Height <= 0 ||
			receipt.Width > limits.PNG.MaxWidth || receipt.Height > limits.PNG.MaxHeight || receipt.SRGBRenderingIntent > 3 ||
			!canonicalSHA256(receipt.EncodedContentHash) || !canonicalSHA256(receipt.PixelHash) ||
			receipt.DecodeProfile != profiles.DecodeProfile || receipt.SelectionProfile != profiles.SelectionProfile ||
			receipt.ColorProfile != profiles.ColorProfile || receipt.NormalizationProfile != profiles.NormalizationProfile ||
			receipt.ContentHashProfile != pngdecode.ContentHashProfileV1 || receipt.SourceProfile != rastercompositor.SourceProfileV1 ||
			receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
			return false
		}
		pixels, ok := checkedMul(uint64(receipt.Width), uint64(receipt.Height))
		if !ok {
			return false
		}
		bytes, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
		if !ok {
			return false
		}
		retained, ok = checkedAdd(retained, bytes)
		if !ok || retained > limits.MaxRetainedBytes {
			return false
		}
		seenIndexes[receipt.AssetIndex] = struct{}{}
		previous = string(receipt.AssetID)
	}
	return true
}

func validExpressionManifest(ctx context.Context, value expressionlayer.Manifest, document documentv2.Document, limits expressionlayer.Limits) bool {
	if ctx == nil || value.SchemaVersion != expressionlayer.ManifestSchemaVersion || value.Profile != expressionlayer.ProfileV1 ||
		value.ContractVersion != expressionlayer.ContractVersion || !canonicalSHA256(value.DocumentHash) ||
		value.SourceProfile != expressionlayer.SourceProfileV1 || value.SelectionProfile != expressionlayer.SelectionProfileV1 ||
		value.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 || value.ExpressionProfile != documentv2.VectorProfilePathViewportPXV2 ||
		value.EvaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 || value.RasterProfile != vectorraster.ProfileV1 ||
		value.PaintProfile != color.ProfileRGBA16SRGBPaintV1 || value.KernelProfile != cpucompositor.KernelProfileV1 ||
		value.SurfaceProfile != cpucompositor.SurfaceProfileV1 || value.PixelHashProfile != expressionlayer.SurfaceHashProfileV1 ||
		len(value.Assets) > limits.MaxExpressionAssets {
		return false
	}
	required := enabledAssets(document, documentv2.TrackKindVector, documentv2.AssetKindVector)
	if len(required) != len(value.Assets) {
		return false
	}
	seenIndexes := make(map[int]struct{}, len(value.Assets))
	previous := ""
	var totalPaths, totalCommands, totalEdges, retained uint64
	for index, receipt := range value.Assets {
		if index&255 == 0 && ctx.Err() != nil {
			return false
		}
		asset, ok := required[receipt.AssetID]
		_, duplicateIndex := seenIndexes[receipt.AssetIndex]
		if !ok || string(receipt.AssetID) <= previous || receipt.AssetIndex < 0 || receipt.AssetIndex >= len(document.Assets) ||
			duplicateIndex || document.Assets[receipt.AssetIndex].ID != receipt.AssetID || asset.Kind != documentv2.AssetKindVector ||
			asset.Vector == nil || asset.Vector.Viewport == nil || receipt.AssetKind != documentv2.AssetKindVector ||
			receipt.RasterViewport != *asset.Vector.Viewport || receipt.AssetSemanticHashProfile != vectorraster.SourceHashProfileV1 ||
			!canonicalSHA256(receipt.AssetSemanticHash) || receipt.ExpressionProfile != documentv2.VectorProfilePathViewportPXV2 ||
			receipt.EvaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 || receipt.SourceProfile != expressionlayer.SourceProfileV1 ||
			receipt.SelectionProfile != expressionlayer.SelectionProfileV1 || receipt.RasterProfile != vectorraster.ProfileV1 ||
			receipt.PaintProfile != color.ProfileRGBA16SRGBPaintV1 || receipt.KernelProfile != cpucompositor.KernelProfileV1 ||
			receipt.SurfaceProfile != cpucompositor.SurfaceProfileV1 || !validExpressionAssetReceipt(receipt, limits.Vector) {
			return false
		}
		encoded, err := videoedit.CanonicalJSON(*asset.Vector)
		if err != nil || uint64(len(encoded)) != receipt.RasterReceipt.CanonicalSourceBytes {
			return false
		}
		digest := sha256.Sum256(encoded)
		expected := "sha256:" + hex.EncodeToString(digest[:])
		if receipt.AssetSemanticHash != expected || receipt.PathCount != uint64(len(asset.Vector.Paths)) {
			return false
		}
		var commandCount uint64
		var conceptualEdges uint64
		for _, path := range asset.Vector.Paths {
			commandCount += uint64(len(path.Commands))
			open := false
			for _, command := range path.Commands {
				switch command.Op {
				case documentv2.VectorMoveTo:
					if open {
						conceptualEdges++
					}
					open = true
				case documentv2.VectorLineTo:
					conceptualEdges++
				case documentv2.VectorClose:
					conceptualEdges++
					open = false
				}
			}
			if open {
				conceptualEdges++
			}
		}
		if receipt.CommandCount != commandCount || receipt.ConceptualEdgeCount != conceptualEdges {
			return false
		}
		var sumOK bool
		totalPaths, sumOK = checkedAdd(totalPaths, receipt.PathCount)
		if !sumOK {
			return false
		}
		totalCommands, sumOK = checkedAdd(totalCommands, receipt.CommandCount)
		if !sumOK {
			return false
		}
		totalEdges, sumOK = checkedAdd(totalEdges, receipt.ConceptualEdgeCount)
		if !sumOK {
			return false
		}
		retained, sumOK = checkedAdd(retained, receipt.SurfaceBytes)
		if !sumOK || totalPaths > limits.MaxTotalPaths || totalCommands > limits.MaxTotalCommands ||
			totalEdges > limits.MaxTotalEdges || retained > limits.MaxRetainedBytes {
			return false
		}
		seenIndexes[receipt.AssetIndex] = struct{}{}
		previous = string(receipt.AssetID)
	}
	return true
}

func validTextManifest(ctx context.Context, value textlayer.Manifest, document documentv2.Document, limits textlayer.Limits) bool {
	if ctx == nil || value.SchemaVersion != textlayer.ManifestSchemaVersion || value.Profile != textlayer.ProfileV1 ||
		value.ContractVersion != textlayer.ContractVersion || !canonicalSHA256(value.DocumentHash) ||
		value.SourceProfile != textlayer.SourceProfileV1 || value.SelectionProfile != textlayer.SelectionProfileV1 ||
		value.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		value.EvaluatorProfile != documentv2.EvaluatorTextPinnedFontViewportPXV2 ||
		value.RasterProfile != documentv2.TextRasterProfileSupersample8x8V1 ||
		value.PaintProfile != color.ProfileRGBA16SRGBPaintV1 || value.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		value.PixelHashProfile != textlayer.SurfaceHashProfileV1 || len(value.Assets) > limits.MaxTextAssets {
		return false
	}
	snapshot, diagnostics := evaluator.FreezeSnapshot(ctx, document, evaluator.SnapshotLimits{
		MaxCanonicalDocumentBytes: limits.MaxCanonicalDocumentBytes,
		MaxDocumentNodes:          uint64(limits.MaxDocumentNodes),
	})
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) || snapshot.DocumentHash() != value.DocumentHash {
		return false
	}
	frozenAssets := make(map[documentv2.ID]evaluator.SnapshotAsset, len(snapshot.Assets()))
	for _, asset := range snapshot.Assets() {
		frozenAssets[asset.ID] = asset
	}
	required := enabledAssets(document, documentv2.TrackKindText, documentv2.AssetKindText)
	if len(required) != len(value.Assets) {
		return false
	}
	assetIndexes := make(map[documentv2.ID]int, len(document.Assets))
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for index, asset := range document.Assets {
		assetIndexes[asset.ID] = index
		assets[asset.ID] = asset
	}
	seenIndexes := make(map[int]struct{}, len(value.Assets))
	previous := ""
	var retained uint64
	for index, receipt := range value.Assets {
		if index&255 == 0 && ctx.Err() != nil {
			return false
		}
		asset, ok := required[receipt.AssetID]
		_, duplicateIndex := seenIndexes[receipt.AssetIndex]
		if !ok || string(receipt.AssetID) <= previous || duplicateIndex || receipt.AssetKind != documentv2.AssetKindText ||
			receipt.AssetIndex < 0 || receipt.AssetIndex >= len(document.Assets) || document.Assets[receipt.AssetIndex].ID != receipt.AssetID ||
			asset.Text == nil || asset.Text.Viewport == nil || asset.Text.Profile != documentv2.TextProfilePinnedFontViewportPXV2 ||
			receipt.RasterViewport != *asset.Text.Viewport || receipt.FontAssetID != asset.Text.FontAssetID ||
			receipt.FontAssetIndex != assetIndexes[asset.Text.FontAssetID] || receipt.FrozenTextBindingHashProfile != evaluator.FrozenTextSourceBindingHashProfileV1 ||
			receipt.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 || receipt.LayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 ||
			receipt.FontContentHashProfile != sfntglyf.ContentHashProfileV1 || !canonicalSHA256(receipt.FrozenTextBindingHash) ||
			!canonicalSHA256(receipt.TextSemanticHash) || !canonicalSHA256(receipt.LayoutInputHash) || !canonicalSHA256(receipt.FontContentHash) {
			return false
		}
		font, fontExists := assets[receipt.FontAssetID]
		frozenAsset, frozenExists := frozenAssets[receipt.AssetID]
		pixels, pixelsOK := checkedMul(uint64(receipt.RasterViewport.WidthPixels), uint64(receipt.RasterViewport.HeightPixels))
		surfaceBytes, surfaceOK := checkedMul(pixels, cpucompositor.BytesPerPixel)
		assembly := receipt.Assembly
		raster := receipt.Raster
		identity := receipt.SurfaceIdentity
		if !fontExists || !frozenExists || font.Kind != documentv2.AssetKindFont || font.ContentHash != receipt.FontContentHash ||
			!pixelsOK || !surfaceOK || receipt.RasterViewport.WidthPixels == 0 || receipt.RasterViewport.HeightPixels == 0 ||
			receipt.SurfaceBytes != surfaceBytes || identity.WidthPixels != receipt.RasterViewport.WidthPixels ||
			identity.HeightPixels != receipt.RasterViewport.HeightPixels || identity.PixelHashProfile != textlayer.SurfaceHashProfileV1 ||
			!canonicalSHA256(identity.PixelHash) || identity.SurfaceProfile != cpucompositor.SurfaceProfileV1 ||
			assembly.SchemaVersion != textassembly.SchemaVersion || assembly.Profile != textassembly.ReceiptProfileV1 ||
			assembly.DocumentHash != value.DocumentHash || assembly.TextAssetID != string(receipt.AssetID) ||
			assembly.TextAssetIndex != uint64(receipt.AssetIndex) || assembly.FontAssetID != string(receipt.FontAssetID) ||
			assembly.FontAssetIndex != uint64(receipt.FontAssetIndex) || assembly.FontContentHash != receipt.FontContentHash ||
			assembly.FrozenTextBindingHash != receipt.FrozenTextBindingHash || assembly.TextSemanticHash != receipt.TextSemanticHash ||
			assembly.LayoutInputHash != receipt.LayoutInputHash || assembly.WidthPixels != receipt.RasterViewport.WidthPixels ||
			assembly.HeightPixels != receipt.RasterViewport.HeightPixels || assembly.FillR != asset.Text.Fill.R ||
			assembly.FillG != asset.Text.Fill.G || assembly.FillB != asset.Text.Fill.B || assembly.FillA != asset.Text.Fill.A ||
			assembly.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || !canonicalSHA256(assembly.AssemblyHash) ||
			raster.SchemaVersion != textassembly.SchemaVersion || raster.Profile != textassembly.RasterReceiptProfileV1 ||
			raster.AssemblyHashProfile != textassembly.AssemblyHashProfileV1 || raster.AssemblyHash != assembly.AssemblyHash ||
			raster.WidthPixels != receipt.RasterViewport.WidthPixels || raster.HeightPixels != receipt.RasterViewport.HeightPixels ||
			raster.PixelHash != identity.PixelHash || raster.RetainedBytes == 0 ||
			!validTextAssetReceiptDeep(receipt, frozenAsset, asset, limits) {
			return false
		}
		retained, ok = checkedAdd(retained, surfaceBytes)
		if !ok || retained > limits.MaxTotalStaticSurfaceBytes {
			return false
		}
		seenIndexes[receipt.AssetIndex] = struct{}{}
		previous = string(receipt.AssetID)
	}
	return retained == value.RetainedSurfaceBytes
}

func validExpressionAssetReceipt(value expressionlayer.AssetReceipt, limits vectorraster.Limits) bool {
	identity := value.SurfaceIdentity
	raster := value.RasterReceipt
	viewport := value.RasterViewport
	pixels, ok := checkedMul(uint64(viewport.WidthPixels), uint64(viewport.HeightPixels))
	if !ok {
		return false
	}
	retained, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok {
		return false
	}
	samples, ok := checkedMul(pixels, 64)
	if ok {
		samples, ok = checkedMul(samples, value.PathCount)
	}
	return ok && viewport.WidthPixels > 0 && viewport.HeightPixels > 0 && viewport.WidthPixels <= limits.MaxWidth && viewport.HeightPixels <= limits.MaxHeight &&
		pixels <= limits.MaxPixels && value.PathCount > 0 && value.PathCount <= limits.MaxPaths && value.CommandCount > 0 && value.CommandCount <= limits.MaxCommands &&
		value.ConceptualEdgeCount > 0 && value.ConceptualEdgeCount <= limits.MaxEdges && value.EffectiveEdgeCount <= value.ConceptualEdgeCount &&
		value.SurfaceBytes == retained && retained <= limits.MaxRetainedBytes && identity.WidthPixels == viewport.WidthPixels &&
		identity.HeightPixels == viewport.HeightPixels && identity.PixelHashProfile == expressionlayer.SurfaceHashProfileV1 &&
		canonicalSHA256(identity.PixelHash) && identity.SurfaceProfile == cpucompositor.SurfaceProfileV1 &&
		raster.SchemaVersion == vectorraster.ReceiptSchemaVersion && raster.Profile == vectorraster.ReceiptProfileV1 &&
		raster.RasterProfile == vectorraster.ProfileV1 && raster.SourceProfile == documentv2.VectorProfilePathViewportPXV2 &&
		raster.SourceHashProfile == vectorraster.SourceHashProfileV1 && raster.SourceSemanticHash == value.AssetSemanticHash &&
		raster.PaintProfile == color.ProfileRGBA16SRGBPaintV1 && raster.KernelProfile == cpucompositor.KernelProfileV1 &&
		raster.SurfaceProfile == cpucompositor.SurfaceProfileV1 && raster.WidthPixels == viewport.WidthPixels && raster.HeightPixels == viewport.HeightPixels &&
		raster.PixelHash == identity.PixelHash && raster.PathCount == value.PathCount && raster.CommandCount == value.CommandCount &&
		raster.ConceptualEdgeCount == value.ConceptualEdgeCount && raster.EffectiveEdgeCount == value.EffectiveEdgeCount && raster.SampleCount == samples &&
		raster.WorkUnits >= raster.SampleCount && raster.WorkUnits <= limits.MaxWorkUnits && raster.CanonicalSourceBytes <= limits.MaxCanonicalSourceBytes &&
		raster.RetainedBytes == retained && raster.WorkingBytes >= retained && raster.WorkingBytes <= limits.MaxWorkingBytes
}

func enabledAssets(document documentv2.Document, trackKind documentv2.TrackKind, assetKind documentv2.AssetKind) map[documentv2.ID]documentv2.Asset {
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for _, asset := range document.Assets {
		assets[asset.ID] = asset
	}
	result := make(map[documentv2.ID]documentv2.Asset)
	for _, timeline := range document.Timelines {
		for _, track := range timeline.Tracks {
			if track.Kind != trackKind {
				continue
			}
			for _, clip := range track.Clips {
				if clip.Disabled {
					continue
				}
				asset, ok := assets[clip.AssetID]
				if ok && asset.Kind == assetKind {
					result[asset.ID] = asset
				}
			}
		}
	}
	return result
}

func classifyVisualCompile(ctx context.Context, diagnostics []documentv2.Diagnostic) error {
	if ctx != nil && ctx.Err() != nil {
		return pipelineError(CodeCanceled, "visual", errors.Join(ErrCanceled, ctx.Err()))
	}
	for _, d := range diagnostics {
		if d.Code == visualcompositor.DiagnosticCodeConfigInvalid {
			return pipelineError(CodeConfigInvalid, "visual/config", errors.Join(ErrConfigInvalid, ErrVisualCompile))
		}
		if d.Code == visualcompositor.DiagnosticCodeDocumentLimit {
			return pipelineError(CodeResourceLimit, "document", errors.Join(ErrResourceLimit, ErrVisualCompile))
		}
		if d.Code == visualcompositor.DiagnosticCodeCanceled {
			return pipelineError(CodeCanceled, "visual", ErrCanceled)
		}
	}
	return pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, ErrVisualCompile))
}
func classifyNativeCompile(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nativepngsource.ErrCanceled) {
		return pipelineError(CodeCanceled, "native", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, nativepngsource.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "native", errors.Join(ErrResourceLimit, err))
	}
	if errors.Is(err, nativepngsource.ErrConfigInvalid) {
		return pipelineError(CodeConfigInvalid, "native", errors.Join(ErrConfigInvalid, err))
	}
	if errors.Is(err, nativepngsource.ErrDocumentInvalid) || errors.Is(err, nativepngsource.ErrDocumentMismatch) {
		return pipelineError(CodeDocumentInvalid, "native", errors.Join(ErrDocumentInvalid, err))
	}
	return pipelineError(CodeNativeSourceCompile, "native", errors.Join(ErrNativeSourceCompile, err))
}
func classifyExpressionCompile(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, expressionlayer.ErrCanceled) {
		return pipelineError(CodeCanceled, "expression", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, expressionlayer.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "expression", errors.Join(ErrResourceLimit, err))
	}
	if errors.Is(err, expressionlayer.ErrConfigInvalid) {
		return pipelineError(CodeConfigInvalid, "expression", errors.Join(ErrConfigInvalid, err))
	}
	if errors.Is(err, expressionlayer.ErrDocumentInvalid) || errors.Is(err, expressionlayer.ErrProfileUnsupported) {
		return pipelineError(CodeDocumentInvalid, "expression", errors.Join(ErrDocumentInvalid, err))
	}
	return pipelineError(CodeExpressionCompile, "expression", errors.Join(ErrExpressionCompile, err))
}

func classifyTextCompile(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, textlayer.ErrCanceled) {
		return pipelineError(CodeCanceled, "text", errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, textlayer.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, "text", errors.Join(ErrResourceLimit, err))
	}
	if errors.Is(err, textlayer.ErrConfigInvalid) {
		return pipelineError(CodeConfigInvalid, "text", errors.Join(ErrConfigInvalid, err))
	}
	if errors.Is(err, textlayer.ErrDocumentInvalid) || errors.Is(err, textlayer.ErrProfileUnsupported) {
		return pipelineError(CodeDocumentInvalid, "text", errors.Join(ErrDocumentInvalid, err))
	}
	return pipelineError(CodeTextSourceCompile, "text", errors.Join(ErrTextSourceCompile, err))
}
