package referenceimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

// Binding lends one complete encoded PNG only until Compile returns. Successful compilation
// retains neither this slice nor EncodedPNG; it owns strict decoded immutable source surfaces.
type Binding struct {
	AssetID    documentv2.ID `json:"asset_id"`
	EncodedPNG []byte        `json:"-"`
}

// Config exposes each allocation boundary but deliberately exposes no source capabilities or
// profile injection. The assembly always compiles image-only nativepngsource profiles.
type Config struct {
	Width            int                     `json:"width"`
	Height           int                     `json:"height"`
	SourceLimits     nativepngsource.Limits  `json:"source_limits"`
	CompositorLimits rastercompositor.Limits `json:"compositor_limits"`
	OutputLimits     color.OutputLimits      `json:"output_limits"`
	PNGLimits        pngencode.Limits        `json:"png_limits"`
	BuildFingerprint string                  `json:"build_fingerprint,omitempty"`
}

func DefaultConfig(width, height int) Config {
	return Config{
		Width: width, Height: height,
		SourceLimits:     nativepngsource.DefaultLimits(),
		CompositorLimits: rastercompositor.DefaultLimits(),
		OutputLimits:     color.DefaultOutputLimits(), PNGLimits: pngencode.DefaultLimits(),
	}
}

// Program contains only immutable dependency Programs/providers and value receipts. It is safe
// for concurrent RenderAt, RenderFrame and accessor calls.
type Program struct {
	documentHash string
	config       Config
	raster       *rastercompositor.Program
	provider     *nativepngsource.Provider
	manifest     nativepngsource.Manifest
	diagnostics  []documentv2.Diagnostic
}

// Compile atomically freezes the complete reference-image pipeline. The Document graph, binding
// slice and encoded bytes must not be mutated concurrently until this method returns.
func Compile(ctx context.Context, document documentv2.Document, bindings []Binding, config Config) (*Program, []documentv2.Diagnostic, error) {
	if ctx == nil {
		return nil, nil, pipelineError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextError(ctx, "context"); err != nil {
		return nil, nil, err
	}
	normalized, rasterConfig, err := normalizeConfig(config)
	if err != nil {
		return nil, nil, err
	}

	rasterProgram, diagnostics := rastercompositor.Compile(document, rasterConfig)
	diagnostics = cloneDiagnostics(diagnostics)
	if rasterProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, classifyCompositorCompile(diagnostics)
	}
	if err := contextError(ctx, "document"); err != nil {
		return nil, diagnostics, err
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	maxCanonical := normalized.SourceLimits.MaxCanonicalDocumentBytes
	if normalized.CompositorLimits.MaxCanonicalDocumentBytes < maxCanonical {
		maxCanonical = normalized.CompositorLimits.MaxCanonicalDocumentBytes
	}
	if uint64(len(canonical)) > maxCanonical {
		return nil, diagnostics, pipelineError(CodeResourceLimit, "document/canonical_bytes", ErrResourceLimit)
	}
	documentHash, err := hashCanonical(ctx, canonical)
	if err != nil {
		return nil, diagnostics, err
	}
	frozenDocument, err := documentv2.Decode(canonical)
	canonical = nil
	if err != nil {
		return nil, diagnostics, pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if rasterProgram.DocumentHash() != documentHash {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/compositor/document_hash", ErrStageMismatch)
	}
	rasterNormalized := rasterProgram.Config()
	if rasterNormalized.Width != normalized.Width || rasterNormalized.Height != normalized.Height ||
		rasterNormalized.Source != (rastercompositor.SourceCapabilities{Image: true}) ||
		rasterNormalized.SourceProfiles.Image != nativepngsource.ImageSourceProfiles() ||
		rasterNormalized.SourceProfiles.Video != (rastercompositor.AssetSourceProfiles{}) ||
		rasterNormalized.BuildFingerprint != normalized.BuildFingerprint {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/compositor/config", ErrStageMismatch)
	}
	normalized.CompositorLimits = rasterNormalized.Limits

	nativeBindings := make([]nativepngsource.Binding, len(bindings))
	for index := range bindings {
		if index&1023 == 0 {
			if err := contextError(ctx, "bindings"); err != nil {
				return nil, diagnostics, err
			}
		}
		nativeBindings[index] = nativepngsource.Binding{
			DocumentHash: documentHash, AssetID: bindings[index].AssetID, EncodedPNG: bindings[index].EncodedPNG,
		}
	}
	provider, err := nativepngsource.Compile(ctx, frozenDocument, nativeBindings, nativepngsource.Config{Limits: normalized.SourceLimits})
	if err != nil {
		return nil, diagnostics, classifyCompileDependency(CodeSourceCompile, "source", ErrSourceCompile, err)
	}
	if err := contextError(ctx, "publish"); err != nil {
		return nil, diagnostics, err
	}
	providerConfig := provider.Config()
	manifest := provider.Manifest()
	if provider.DocumentHash() != documentHash || manifest.DocumentHash != documentHash ||
		manifest.SchemaVersion != nativepngsource.ManifestSchemaVersion || manifest.Profile != nativepngsource.ProfileV1 ||
		manifest.SourceContractVersion != rastercompositor.NativeSourceContractVersion ||
		!validSourceManifestAgainstDocument(ctx, manifest, frozenDocument, providerConfig.Limits) {
		if err := contextError(ctx, "compile/source/manifest"); err != nil {
			return nil, diagnostics, err
		}
		return nil, diagnostics, pipelineError(CodeStageMismatch, "compile/source/manifest", ErrStageMismatch)
	}
	normalized.SourceLimits = providerConfig.Limits
	program := &Program{
		documentHash: documentHash, config: normalized, raster: rasterProgram, provider: provider,
		manifest: cloneManifest(manifest), diagnostics: cloneDiagnostics(diagnostics),
	}
	return program, cloneDiagnostics(diagnostics), nil
}

func normalizeConfig(value Config) (Config, rastercompositor.Config, error) {
	result := value
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, rastercompositor.Config{}, pipelineError(CodeConfigInvalid, "config/dimensions", ErrConfigInvalid)
	}
	if result.SourceLimits == (nativepngsource.Limits{}) {
		result.SourceLimits = nativepngsource.DefaultLimits()
	} else {
		result.SourceLimits = fillSourceLimitDefaults(result.SourceLimits)
	}
	if result.CompositorLimits == (rastercompositor.Limits{}) {
		result.CompositorLimits = rastercompositor.DefaultLimits()
	}
	if result.OutputLimits == (color.OutputLimits{}) {
		result.OutputLimits = color.DefaultOutputLimits()
	}
	if result.PNGLimits == (pngencode.Limits{}) {
		result.PNGLimits = pngencode.DefaultLimits()
	} else {
		result.PNGLimits = fillPNGLimitDefaults(result.PNGLimits)
	}
	if err := validateOutputAndPNGGates(result); err != nil {
		return Config{}, rastercompositor.Config{}, err
	}
	rasterConfig := rastercompositor.Config{
		Width: result.Width, Height: result.Height, Limits: result.CompositorLimits,
		Source:           rastercompositor.SourceCapabilities{Image: true},
		SourceProfiles:   rastercompositor.SourceProfiles{Image: nativepngsource.ImageSourceProfiles()},
		BuildFingerprint: result.BuildFingerprint,
	}
	return result, rasterConfig, nil
}

func fillSourceLimitDefaults(value nativepngsource.Limits) nativepngsource.Limits {
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
	// Per-field strict-PNG defaults are intentionally owned by pngdecode/nativepngsource. A
	// partially populated nested value is validated there instead of being silently repaired.
	if value.PNG == (pngdecode.Limits{}) {
		value.PNG = defaults.PNG
	}
	return value
}

func fillPNGLimitDefaults(value pngencode.Limits) pngencode.Limits {
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

func validateOutputAndPNGGates(config Config) error {
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
	width, height := uint64(config.Width), uint64(config.Height)
	if height != 0 && width > math.MaxUint64/height {
		return pipelineError(CodeResourceLimit, "config/pixel_count", ErrResourceLimit)
	}
	pixels := width * height
	if pixels > math.MaxUint64/color.BytesPerOutputRGBA8Pixel {
		return pipelineError(CodeResourceLimit, "config/output_bytes", ErrResourceLimit)
	}
	raw := pixels * color.BytesPerOutputRGBA8Pixel
	if raw > output.MaxOutputBytes || raw > png.MaxRawRGBABytes {
		return pipelineError(CodeResourceLimit, "config/output_bytes", ErrResourceLimit)
	}
	if raw > math.MaxUint64-height {
		return pipelineError(CodeResourceLimit, "config/scanline_bytes", ErrResourceLimit)
	}
	scanline := raw + height
	blocks := scanline / pngencode.StoredBlockMaxBytes
	if scanline%pngencode.StoredBlockMaxBytes != 0 {
		blocks++
	}
	if scanline > math.MaxUint64-pngencode.ZlibFixedBytes || blocks > (math.MaxUint64-pngencode.ZlibFixedBytes-scanline)/5 {
		return pipelineError(CodeResourceLimit, "config/idat_bytes", ErrResourceLimit)
	}
	idat := pngencode.ZlibFixedBytes + scanline + blocks*5
	if idat > pngencode.HardMaxIDATBytes || idat > math.MaxUint64-pngencode.FixedArtifactBytes {
		return pipelineError(CodeResourceLimit, "config/idat_bytes", ErrResourceLimit)
	}
	artifact := idat + pngencode.FixedArtifactBytes
	if width > uint64(maxInt())/color.BytesPerOutputRGBA8Pixel || artifact > png.MaxArtifactBytes || artifact > uint64(maxInt()) {
		return pipelineError(CodeResourceLimit, "config/artifact_bytes", ErrResourceLimit)
	}
	stride := width * color.BytesPerOutputRGBA8Pixel
	if artifact > math.MaxUint64-stride || artifact+stride > png.MaxWorkingBytes {
		return pipelineError(CodeResourceLimit, "config/png_working_bytes", ErrResourceLimit)
	}
	return nil
}

func hashCanonical(ctx context.Context, canonical []byte) (string, error) {
	digest := sha256.New()
	for offset := 0; offset < len(canonical); {
		if err := contextError(ctx, "document/hash"); err != nil {
			return "", err
		}
		end := offset + 64<<10
		if end > len(canonical) {
			end = len(canonical)
		}
		_, _ = digest.Write(canonical[offset:end])
		offset = end
	}
	if err := contextError(ctx, "document/hash"); err != nil {
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

func classifyCompileDependency(code ErrorCode, path string, sentinel, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nativepngsource.ErrCanceled) {
		return pipelineError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	if errors.Is(err, nativepngsource.ErrResourceLimit) {
		return pipelineError(CodeResourceLimit, path, errors.Join(ErrResourceLimit, err))
	}
	if errors.Is(err, nativepngsource.ErrConfigInvalid) {
		return pipelineError(CodeConfigInvalid, path, errors.Join(ErrConfigInvalid, err))
	}
	if errors.Is(err, nativepngsource.ErrDocumentInvalid) || errors.Is(err, nativepngsource.ErrDocumentMismatch) {
		return pipelineError(CodeDocumentInvalid, path, errors.Join(ErrDocumentInvalid, err))
	}
	return pipelineError(code, path, errors.Join(sentinel, err))
}

func classifyCompositorCompile(diagnostics []documentv2.Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == rastercompositor.DiagnosticCodeConfigInvalid {
			return pipelineError(CodeConfigInvalid, "compositor/config", errors.Join(ErrConfigInvalid, ErrCompositorCompile))
		}
		if diagnostic.Code == rastercompositor.DiagnosticCodeDocumentLimit {
			return pipelineError(CodeResourceLimit, "document", errors.Join(ErrResourceLimit, ErrCompositorCompile))
		}
	}
	return pipelineError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, ErrCompositorCompile))
}

func (program *Program) DocumentHash() string {
	if program == nil {
		return ""
	}
	return program.documentHash
}
func (program *Program) Config() Config {
	if program == nil {
		return Config{}
	}
	return program.config
}
func (program *Program) Diagnostics() []documentv2.Diagnostic {
	if program == nil {
		return nil
	}
	return cloneDiagnostics(program.diagnostics)
}
func (program *Program) SourceManifest() nativepngsource.Manifest {
	if program == nil {
		return nativepngsource.Manifest{}
	}
	return cloneManifest(program.manifest)
}

func cloneManifest(value nativepngsource.Manifest) nativepngsource.Manifest {
	value.Assets = append([]nativepngsource.AssetReceipt(nil), value.Assets...)
	return value
}

func cloneDiagnostics(values []documentv2.Diagnostic) []documentv2.Diagnostic {
	if values == nil {
		return nil
	}
	result := make([]documentv2.Diagnostic, len(values))
	for index := range values {
		result[index] = values[index]
		result[index].Fixes = append([]videoedit.CandidateFix(nil), values[index].Fixes...)
		for fixIndex := range result[index].Fixes {
			result[index].Fixes[fixIndex].Operation = append([]byte(nil), values[index].Fixes[fixIndex].Operation...)
			result[index].Fixes[fixIndex].SideEffects = append([]string(nil), values[index].Fixes[fixIndex].SideEffects...)
		}
	}
	return result
}

func maxInt() int { return int(^uint(0) >> 1) }

func validSourceManifest(value nativepngsource.Manifest, limits nativepngsource.Limits) bool {
	if value.SchemaVersion != nativepngsource.ManifestSchemaVersion || value.Profile != nativepngsource.ProfileV1 ||
		!canonicalSHA256(value.DocumentHash) ||
		value.SourceContractVersion != rastercompositor.NativeSourceContractVersion ||
		value.SourceProfile != rastercompositor.SourceProfileV1 || value.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		return false
	}
	if len(value.Assets) > limits.MaxBindings {
		return false
	}
	profiles := nativepngsource.ImageSourceProfiles()
	seen := make(map[documentv2.ID]struct{}, len(value.Assets))
	seenIndexes := make(map[int]struct{}, len(value.Assets))
	previous := ""
	var retainedBytes uint64
	for _, asset := range value.Assets {
		id := string(asset.AssetID)
		if id == "" || (previous != "" && id <= previous) || asset.AssetIndex < 0 || asset.Width <= 0 || asset.Height <= 0 ||
			asset.Width > limits.PNG.MaxWidth || asset.Height > limits.PNG.MaxHeight ||
			asset.SRGBRenderingIntent > 3 || !canonicalSHA256(asset.EncodedContentHash) || !canonicalSHA256(asset.PixelHash) ||
			asset.DecodeProfile != profiles.DecodeProfile || asset.SelectionProfile != profiles.SelectionProfile ||
			asset.ColorProfile != profiles.ColorProfile || asset.NormalizationProfile != profiles.NormalizationProfile ||
			asset.ContentHashProfile != pngdecode.ContentHashProfileV1 ||
			asset.SourceProfile != rastercompositor.SourceProfileV1 || asset.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
			return false
		}
		if _, duplicate := seen[asset.AssetID]; duplicate {
			return false
		}
		if _, duplicate := seenIndexes[asset.AssetIndex]; duplicate {
			return false
		}
		width, height := uint64(asset.Width), uint64(asset.Height)
		if height != 0 && width > math.MaxUint64/height {
			return false
		}
		pixels := width * height
		if pixels > math.MaxUint64/cpucompositor.BytesPerPixel {
			return false
		}
		bytes := pixels * cpucompositor.BytesPerPixel
		if bytes > limits.MaxRetainedBytes-retainedBytes {
			return false
		}
		retainedBytes += bytes
		seen[asset.AssetID] = struct{}{}
		seenIndexes[asset.AssetIndex] = struct{}{}
		previous = id
	}
	return true
}

func validSourceManifestAgainstDocument(ctx context.Context, value nativepngsource.Manifest, document documentv2.Document, limits nativepngsource.Limits) bool {
	if ctx == nil || !validSourceManifest(value, limits) {
		return false
	}
	required := make(map[documentv2.ID]struct{}, len(value.Assets))
	assets := make(map[documentv2.ID]documentv2.Asset, len(document.Assets))
	for index, asset := range document.Assets {
		if index&1023 == 0 && ctx.Err() != nil {
			return false
		}
		assets[asset.ID] = asset
	}
	visits := 0
	for _, timeline := range document.Timelines {
		for _, track := range timeline.Tracks {
			if visits&1023 == 0 && ctx.Err() != nil {
				return false
			}
			visits++
			if track.Kind != documentv2.TrackKindVideo {
				continue
			}
			for _, clip := range track.Clips {
				if visits&1023 == 0 && ctx.Err() != nil {
					return false
				}
				visits++
				if clip.Disabled {
					continue
				}
				asset, exists := assets[clip.AssetID]
				if exists && asset.Kind == documentv2.AssetKindImage {
					required[asset.ID] = struct{}{}
				}
			}
		}
	}
	if len(required) != len(value.Assets) {
		return false
	}
	for _, receipt := range value.Assets {
		if receipt.AssetIndex < 0 || receipt.AssetIndex >= len(document.Assets) {
			return false
		}
		asset := document.Assets[receipt.AssetIndex]
		if asset.ID != receipt.AssetID || asset.Kind != documentv2.AssetKindImage ||
			asset.ContentHash != receipt.EncodedContentHash {
			return false
		}
		if _, exists := required[receipt.AssetID]; !exists {
			return false
		}
	}
	return true
}
