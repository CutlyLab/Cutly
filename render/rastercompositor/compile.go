package rastercompositor

import (
	"fmt"
	"math"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
)

const (
	BackendID                          = ProfileV1
	DiagnosticCodeConfigInvalid        = "COMPOSITOR.RASTER_REFERENCE.CONFIG_INVALID"
	DiagnosticCodeFreezeInvalid        = "COMPOSITOR.RASTER_REFERENCE.FREEZE_INVALID"
	DiagnosticCodeAssetUnpinned        = "COMPOSITOR.RASTER_REFERENCE.ASSET_UNPINNED"
	DiagnosticCodeDocumentLimit        = "COMPOSITOR.RASTER_REFERENCE.DOCUMENT_LIMIT"
	DefaultMaxLayers                   = 1024
	DefaultMaxSourceRequests           = 2048
	HardMaxLayers                      = 4096
	HardMaxSourceRequests              = 8192
	DefaultMaxDocumentNodes            = 262144
	HardMaxDocumentNodes               = 4 << 20
	DefaultMaxTracksPerTimeline        = 4096
	HardMaxTracksPerTimeline           = 65536
	DefaultMaxSourceBytes       uint64 = 512 << 20
	HardMaxSourceBytes          uint64 = cpucompositor.HardMaxSurfaceBytes
	DefaultTargetWorking        uint64 = 384 << 20
	HardMaxTargetWorking        uint64 = 2 << 30
	DefaultMaxDocumentBytes     uint64 = 128 << 20
	HardMaxDocumentBytes        uint64 = 1 << 30
	targetFramePeakMultiplier          = 6
)

type SourceCapabilities struct {
	Video             bool `json:"video"`
	Image             bool `json:"image"`
	LinearVideoRetime bool `json:"linear_video_retime"`
}

// AssetSourceProfiles freezes the provider semantics accepted for one asset kind. These are
// exact allowlisted identifiers, not provider-authored log labels. A worker with different
// decode, selection, color or geometry-normalization semantics requires a different Config.
type AssetSourceProfiles struct {
	DecodeProfile        string `json:"decode_profile"`
	SelectionProfile     string `json:"selection_profile"`
	ColorProfile         string `json:"color_profile"`
	NormalizationProfile string `json:"normalization_profile"`
}

type SourceProfiles struct {
	Video AssetSourceProfiles `json:"video"`
	Image AssetSourceProfiles `json:"image"`
}

const (
	DefaultVideoDecodeProfileV1    = "cutly.decode.video-native-rgba16.v1"
	DefaultVideoSelectionProfileV1 = "cutly.selection.exact-source-time.v1"
	DefaultImageDecodeProfileV1    = "cutly.decode.image-native-rgba16.v1"
	DefaultImageSelectionProfileV1 = "cutly.selection.static-image.v1"
	DefaultColorProfileV1          = "cutly.color.linear-premultiplied-rgba16.v1"
	DefaultNormalizationProfileV1  = "cutly.normalization.native-geometry.v1"
)

func DefaultSourceProfiles() SourceProfiles {
	return SourceProfiles{
		Video: AssetSourceProfiles{
			DecodeProfile: DefaultVideoDecodeProfileV1, SelectionProfile: DefaultVideoSelectionProfileV1,
			ColorProfile: DefaultColorProfileV1, NormalizationProfile: DefaultNormalizationProfileV1,
		},
		Image: AssetSourceProfiles{
			DecodeProfile: DefaultImageDecodeProfileV1, SelectionProfile: DefaultImageSelectionProfileV1,
			ColorProfile: DefaultColorProfileV1, NormalizationProfile: DefaultNormalizationProfileV1,
		},
	}
}

// Limits freezes all host policy that can reject otherwise valid rendering semantics. Target
// limits apply to each CPU blend surface, Raster to each affine call, MaxSourceBytes to each
// provider response, MaxTargetWorkingBytes to the profile's fixed target-frame peak,
// MaxDocumentNodes to the complete caller-owned object graph, and MaxCanonicalDocumentBytes to
// both its conservative pre-encode upper bound and exact canonical bytes. MaxTracksPerTimeline
// bounds evaluator work before the active-layer runtime gate.
type Limits struct {
	Target                    cpucompositor.Limits `json:"target"`
	Raster                    cpuraster.Limits     `json:"raster"`
	MaxLayers                 int                  `json:"max_layers"`
	MaxSourceRequests         int                  `json:"max_source_requests"`
	MaxDocumentNodes          int                  `json:"max_document_nodes"`
	MaxTracksPerTimeline      int                  `json:"max_tracks_per_timeline"`
	MaxSourceBytes            uint64               `json:"max_source_bytes"`
	MaxTargetWorkingBytes     uint64               `json:"max_target_working_bytes"`
	MaxCanonicalDocumentBytes uint64               `json:"max_canonical_document_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		Target: cpucompositor.DefaultLimits(), Raster: cpuraster.DefaultLimits(),
		MaxLayers: DefaultMaxLayers, MaxSourceRequests: DefaultMaxSourceRequests,
		MaxDocumentNodes:     DefaultMaxDocumentNodes,
		MaxTracksPerTimeline: DefaultMaxTracksPerTimeline,
		MaxSourceBytes:       DefaultMaxSourceBytes, MaxTargetWorkingBytes: DefaultTargetWorking,
		MaxCanonicalDocumentBytes: DefaultMaxDocumentBytes,
	}
}

type Config struct {
	Width            int                `json:"width"`
	Height           int                `json:"height"`
	Limits           Limits             `json:"limits"`
	Source           SourceCapabilities `json:"source"`
	SourceProfiles   SourceProfiles     `json:"source_profiles"`
	BuildFingerprint string             `json:"build_fingerprint,omitempty"`
}

func DefaultConfig(width, height int) Config {
	return Config{
		Width: width, Height: height, Limits: DefaultLimits(),
		Source:         SourceCapabilities{Video: true, Image: true},
		SourceProfiles: DefaultSourceProfiles(),
	}
}

type frozenAsset struct {
	Index       int
	Kind        documentv2.AssetKind
	ContentHash string
}

type Program struct {
	evaluator    *evaluator.Program
	config       Config
	capabilities videoedit.BackendCapabilities
	assets       map[documentv2.ID]frozenAsset
	timelines    map[documentv2.ID]int
}

func Capabilities(config Config) (videoedit.BackendCapabilities, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: BackendID, BuildFingerprint: normalized.BuildFingerprint,
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo},
		Features: []videoedit.CapabilityID{
			documentv2.CapabilityTransformV1,
			documentv2.CapabilityCropV1,
			documentv2.CapabilityOpacityV1,
			documentv2.CapabilityKeyframesV1,
			documentv2.CapabilityInterpolationHoldV1,
			documentv2.CapabilityInterpolationLinearV1,
			documentv2.CapabilityInterpolationCubicV1,
			videoedit.CapabilityID(documentv2.TransitionDissolveV1),
		},
		MaxWidth: normalized.Width, MaxHeight: normalized.Height,
	}
	if normalized.Source.Video {
		capabilities.AssetKinds = append(capabilities.AssetKinds, videoedit.AssetKindVideo)
	}
	if normalized.Source.Image {
		capabilities.AssetKinds = append(capabilities.AssetKinds, videoedit.AssetKindImage)
	}
	if normalized.Source.LinearVideoRetime {
		if !normalized.Source.Video {
			return videoedit.BackendCapabilities{}, compositorError(CodeInvalidConfig, "config.source.linear_video_retime", ErrInvalidConfig)
		}
		capabilities.Features = append(capabilities.Features, videoedit.CapabilityVideoLinearRetime)
	}
	return videoedit.CanonicalBackendCapabilities(capabilities), nil
}

// Compile performs capability preflight and freezes the complete Document in evaluator.Program.
// No Program is returned when any authored rendering semantic is unsupported.
func Compile(document documentv2.Document, config Config) (*Program, []documentv2.Diagnostic) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeConfigInvalid, Severity: documentv2.SeverityError,
			Path: "/raster_compositor/config", Message: err.Error(),
		}}
	}
	capabilities, err := Capabilities(normalized)
	if err != nil {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeConfigInvalid, Severity: documentv2.SeverityError,
			Path: "/raster_compositor/config/source", Message: err.Error(),
		}}
	}
	limitPath, limitErr := preflightDocumentLimits(document, normalized.Limits)
	if limitErr != nil {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeDocumentLimit, Severity: documentv2.SeverityError,
			Path: limitPath, Message: limitErr.Error(),
		}}
	}
	canonical, freezeErr := documentv2.CanonicalJSON(document)
	if freezeErr != nil {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeFreezeInvalid, Severity: documentv2.SeverityError,
			Path: "/", Message: fmt.Sprintf("could not canonicalize render document: %v", freezeErr),
		}}
	}
	if uint64(len(canonical)) > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeDocumentLimit, Severity: documentv2.SeverityError,
			Path: "/", Message: fmt.Sprintf("canonical document bytes %d exceed limit %d", len(canonical), normalized.Limits.MaxCanonicalDocumentBytes),
		}}
	}
	frozen, freezeErr := documentv2.Decode(canonical)
	if freezeErr != nil {
		return nil, []documentv2.Diagnostic{{
			Code: DiagnosticCodeFreezeInvalid, Severity: documentv2.SeverityError,
			Path: "/", Message: fmt.Sprintf("could not freeze render document: %v", freezeErr),
		}}
	}
	assets := make(map[documentv2.ID]frozenAsset, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		assets[asset.ID] = frozenAsset{Index: index, Kind: asset.Kind, ContentHash: asset.ContentHash}
		if (asset.Kind == documentv2.AssetKindVideo || asset.Kind == documentv2.AssetKindImage) && !canonicalSHA256(asset.ContentHash) {
			return nil, []documentv2.Diagnostic{{
				Code: DiagnosticCodeAssetUnpinned, Severity: documentv2.SeverityError,
				Path:    fmt.Sprintf("/assets/%d/content_hash", index),
				Message: "native raster source assets must carry a canonical immutable SHA-256 content hash",
			}}
		}
	}
	timelines := make(map[documentv2.ID]int, len(frozen.Timelines))
	for index, timeline := range frozen.Timelines {
		timelines[timeline.ID] = index
	}
	compiled, diagnostics := evaluator.Compile(frozen, capabilities)
	if compiled == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	return &Program{
		evaluator: compiled, config: normalized, capabilities: capabilities,
		assets: assets, timelines: timelines,
	}, diagnostics
}

func normalizeConfig(config Config) (Config, error) {
	result := config
	if result.Limits == (Limits{}) {
		result.Limits = DefaultLimits()
	}
	if result.Limits.Target == (cpucompositor.Limits{}) {
		result.Limits.Target = cpucompositor.DefaultLimits()
	}
	if result.Limits.Raster == (cpuraster.Limits{}) {
		result.Limits.Raster = cpuraster.DefaultLimits()
	}
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, compositorError(CodeInvalidConfig, "config.dimensions", ErrInvalidConfig)
	}
	target := result.Limits.Target
	if target.MaxWidth <= 0 || target.MaxHeight <= 0 || target.MaxBytes < cpucompositor.BytesPerPixel || target.MaxBytes > cpucompositor.HardMaxSurfaceBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config.limits.target", ErrInvalidConfig)
	}
	raster := result.Limits.Raster
	if raster.MaxWidth <= 0 || raster.MaxHeight <= 0 || raster.MaxOutputPixels == 0 || raster.MaxOutputPixels > cpuraster.HardMaxPixels ||
		raster.MaxWorkingBytes < cpucompositor.BytesPerPixel*2 || raster.MaxWorkingBytes > cpuraster.HardMaxWorking ||
		raster.MaxExactBits <= 0 || raster.MaxExactBits > cpuraster.HardMaxExactBits {
		return Config{}, compositorError(CodeInvalidConfig, "config.limits.raster", ErrInvalidConfig)
	}
	if result.Limits.MaxLayers <= 0 || result.Limits.MaxLayers > HardMaxLayers ||
		result.Limits.MaxSourceRequests <= 0 || result.Limits.MaxSourceRequests > HardMaxSourceRequests ||
		result.Limits.MaxDocumentNodes <= 0 || result.Limits.MaxDocumentNodes > HardMaxDocumentNodes ||
		result.Limits.MaxTracksPerTimeline <= 0 || result.Limits.MaxTracksPerTimeline > HardMaxTracksPerTimeline ||
		result.Limits.MaxSourceBytes < cpucompositor.BytesPerPixel || result.Limits.MaxSourceBytes > HardMaxSourceBytes ||
		result.Limits.MaxTargetWorkingBytes < cpucompositor.BytesPerPixel*targetFramePeakMultiplier || result.Limits.MaxTargetWorkingBytes > HardMaxTargetWorking ||
		result.Limits.MaxCanonicalDocumentBytes == 0 || result.Limits.MaxCanonicalDocumentBytes > HardMaxDocumentBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config.limits", ErrInvalidConfig)
	}
	if result.Width > target.MaxWidth || result.Height > target.MaxHeight || result.Width > raster.MaxWidth || result.Height > raster.MaxHeight {
		return Config{}, compositorError(CodeInvalidConfig, "config.dimensions", ErrInvalidConfig)
	}
	width, height := uint64(result.Width), uint64(result.Height)
	if height != 0 && width > math.MaxUint64/height {
		return Config{}, compositorError(CodeInvalidConfig, "config.pixel_count", ErrInvalidConfig)
	}
	pixels := width * height
	if pixels > raster.MaxOutputPixels || pixels > math.MaxUint64/cpucompositor.BytesPerPixel {
		return Config{}, compositorError(CodeResourceLimit, "config.pixel_count", ErrResourceLimit)
	}
	frameBytes := pixels * cpucompositor.BytesPerPixel
	if frameBytes > target.MaxBytes || frameBytes > math.MaxUint64/targetFramePeakMultiplier ||
		frameBytes*targetFramePeakMultiplier > result.Limits.MaxTargetWorkingBytes ||
		frameBytes > math.MaxUint64/2 || frameBytes*2 > raster.MaxWorkingBytes {
		return Config{}, compositorError(CodeResourceLimit, "config.working_bytes", ErrResourceLimit)
	}
	if !canonicalBuildFingerprint(result.BuildFingerprint) {
		return Config{}, compositorError(CodeInvalidConfig, "config.build_fingerprint", ErrInvalidConfig)
	}
	if result.SourceProfiles == (SourceProfiles{}) {
		result.SourceProfiles = DefaultSourceProfiles()
	}
	if result.Source.Video {
		if err := validateAssetSourceProfiles(result.SourceProfiles.Video); err != nil {
			return Config{}, compositorError(CodeInvalidConfig, "config.source_profiles.video", errorsJoinInvalidConfig(err))
		}
	}
	if result.Source.Image {
		if err := validateAssetSourceProfiles(result.SourceProfiles.Image); err != nil {
			return Config{}, compositorError(CodeInvalidConfig, "config.source_profiles.image", errorsJoinInvalidConfig(err))
		}
	}
	return result, nil
}

func canonicalBuildFingerprint(value string) bool {
	if len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validateAssetSourceProfiles(profiles AssetSourceProfiles) error {
	values := []string{profiles.DecodeProfile, profiles.SelectionProfile, profiles.ColorProfile, profiles.NormalizationProfile}
	for _, value := range values {
		if !canonicalProfileToken(value) {
			return fmt.Errorf("profile %q is not a canonical versioned identifier", value)
		}
	}
	return nil
}

// Kept local so configuration errors preserve both the stable sentinel and useful detail without
// exposing a second public error taxonomy.
func errorsJoinInvalidConfig(detail error) error {
	return fmt.Errorf("%w: %v", ErrInvalidConfig, detail)
}

func (program *Program) DocumentHash() string {
	if program == nil || program.evaluator == nil {
		return ""
	}
	return program.evaluator.DocumentHash()
}

func (program *Program) Config() Config {
	if program == nil {
		return Config{}
	}
	return program.config
}

func (program *Program) Capabilities() videoedit.BackendCapabilities {
	if program == nil {
		return videoedit.BackendCapabilities{}
	}
	return videoedit.CanonicalBackendCapabilities(program.capabilities)
}
