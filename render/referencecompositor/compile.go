package referencecompositor

import (
	"math"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	BackendID         = "cutly.reference-compositor.normalized-surface.v1"
	CodeConfigInvalid = "COMPOSITOR.REFERENCE.CONFIG_INVALID"
)

// SourceCapabilities declares only source-selection operations that this assembly can safely
// delegate. A provider cannot inject arbitrary Document capabilities through this API.
type SourceCapabilities struct {
	Video             bool `json:"video"`
	Image             bool `json:"image"`
	LinearVideoRetime bool `json:"linear_video_retime"`
}

// Config freezes the target surface and host allocation boundary. A zero Limits value selects a
// fresh copy of cpucompositor.DefaultLimits; source capabilities remain explicit and fail closed.
type Config struct {
	Width            int                  `json:"width"`
	Height           int                  `json:"height"`
	Limits           cpucompositor.Limits `json:"limits"`
	Source           SourceCapabilities   `json:"source"`
	BuildFingerprint string               `json:"build_fingerprint,omitempty"`
}

// DefaultConfig supports normalized video and image surfaces but not temporal video retiming.
func DefaultConfig(width, height int) Config {
	return Config{
		Width: width, Height: height, Limits: cpucompositor.DefaultLimits(),
		Source: SourceCapabilities{Video: true, Image: true},
	}
}

type Program struct {
	evaluator    *evaluator.Program
	config       Config
	capabilities videoedit.BackendCapabilities
}

// Capabilities returns exactly the semantics consumed by this assembly plus the explicitly
// delegated video/image source-selection contract. In particular it never advertises transform,
// crop, text, vector, color normalization, decode, or arbitrary extension capabilities.
func Capabilities(config Config) (videoedit.BackendCapabilities, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return videoedit.BackendCapabilities{}, err
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: BackendID, BuildFingerprint: normalized.BuildFingerprint,
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo},
		Features: []videoedit.CapabilityID{
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

// Compile validates/freeze-copies the complete Document through evaluator.Compile. Any authored
// semantic not present in Capabilities produces a diagnostic and no Program; the assembly never
// reaches runtime with a transform, crop, text/vector source, or render extension it would ignore.
func Compile(document documentv2.Document, config Config) (*Program, []documentv2.Diagnostic) {
	normalized, configErr := normalizeConfig(config)
	if configErr != nil {
		return nil, []documentv2.Diagnostic{{
			Code: CodeConfigInvalid, Severity: documentv2.SeverityError,
			Path: "/reference_compositor/config", Message: configErr.Error(),
		}}
	}
	capabilities, capabilityErr := Capabilities(normalized)
	if capabilityErr != nil {
		return nil, []documentv2.Diagnostic{{
			Code: CodeConfigInvalid, Severity: documentv2.SeverityError,
			Path: "/reference_compositor/config/source", Message: capabilityErr.Error(),
		}}
	}
	compiled, diagnostics := evaluator.Compile(document, capabilities)
	if compiled == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	return &Program{evaluator: compiled, config: normalized, capabilities: capabilities}, diagnostics
}

func normalizeConfig(config Config) (Config, error) {
	result := config
	if result.Limits == (cpucompositor.Limits{}) {
		result.Limits = cpucompositor.DefaultLimits()
	}
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, compositorError(CodeInvalidConfig, "config.dimensions", ErrInvalidConfig)
	}
	if result.Limits.MaxWidth <= 0 || result.Limits.MaxHeight <= 0 ||
		result.Limits.MaxBytes < cpucompositor.BytesPerPixel || result.Limits.MaxBytes > cpucompositor.HardMaxSurfaceBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config.limits", ErrInvalidConfig)
	}
	if result.Width > result.Limits.MaxWidth || result.Height > result.Limits.MaxHeight {
		return Config{}, compositorError(CodeInvalidConfig, "config.dimensions", ErrInvalidConfig)
	}
	width, height := uint64(result.Width), uint64(result.Height)
	if height != 0 && width > math.MaxUint64/height {
		return Config{}, compositorError(CodeInvalidConfig, "config.pixel_count", ErrInvalidConfig)
	}
	pixels := width * height
	if pixels > math.MaxUint64/cpucompositor.BytesPerPixel || pixels*cpucompositor.BytesPerPixel > result.Limits.MaxBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config.bytes", ErrInvalidConfig)
	}
	if strings.TrimSpace(result.BuildFingerprint) != result.BuildFingerprint {
		return Config{}, compositorError(CodeInvalidConfig, "config.build_fingerprint", ErrInvalidConfig)
	}
	return result, nil
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
