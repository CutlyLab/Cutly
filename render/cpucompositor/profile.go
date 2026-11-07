package cpucompositor

const (
	// SurfaceProfileV1 names the in-memory and canonical-hash surface contract. It is intentionally
	// not an input/output color profile: conversion into and out of this working domain is a later,
	// independently versioned M2 stage.
	SurfaceProfileV1 = "cutly.surface.linear-bt709-premultiplied-rgba16.v1"
	// KernelProfileV1 names the quantization and blend rules implemented by this package.
	KernelProfileV1 = "cutly.compositor.cpu-kernel.v1"
)

// ProfileDescriptor is the machine-readable discovery contract for agents and backends. Text is
// normative for v1; changing a rule requires a new profile rather than an implementation tweak.
type ProfileDescriptor struct {
	Profile                string `json:"profile"`
	SurfaceProfile         string `json:"surface_profile"`
	Layout                 string `json:"layout"`
	ChannelEncoding        string `json:"channel_encoding"`
	WorkingPrimaries       string `json:"working_primaries"`
	LightDomain            string `json:"light_domain"`
	AlphaSemantics         string `json:"alpha_semantics"`
	PixelInvariant         string `json:"pixel_invariant"`
	Quantization           string `json:"quantization"`
	OpacitySemantics       string `json:"opacity_semantics"`
	DissolveSemantics      string `json:"dissolve_semantics"`
	SourceOverSemantics    string `json:"source_over_semantics"`
	LayerOrder             string `json:"layer_order"`
	CanonicalSerialization string `json:"canonical_serialization"`
	ExcludedStages         string `json:"excluded_stages"`
}

var profileV1 = ProfileDescriptor{
	Profile:        KernelProfileV1,
	SurfaceProfile: SurfaceProfileV1,
	Layout:         "Top-down, row-major, tightly packed pixels; each pixel is R,G,B,A in that order.",
	ChannelEncoding: "Each channel is an unsigned 16-bit integer in [0,65535]; 65535 denotes exact " +
		"one in the working domain.",
	WorkingPrimaries:       "ITU-R BT.709 / sRGB chromaticities with D65 white point. Inputs with other primaries must be converted or rejected by a separately versioned normalization stage.",
	LightDomain:            "RGB values are already transfer-decoded linear-light BT.709 values. This kernel never applies a transfer function, matrix, range conversion, chroma reconstruction, ICC transform, or tone map.",
	AlphaSemantics:         "RGB is premultiplied by alpha before entering the kernel. Transparent pixels are exactly 0,0,0,0.",
	PixelInvariant:         "For every pixel R<=A, G<=A and B<=A. Invalid input fails before an output is allocated or published.",
	Quantization:           "Every operation forms its complete exact non-negative rational channel result, then rounds once to the nearest integer; exact half ties choose the even integer. No float, SIMD-dependent approximation, intermediate channel rounding, dithering, or hidden clamp is permitted.",
	OpacitySemantics:       "Multiply premultiplied R,G,B,A by the exact evaluator opacity in [0,1], then apply the v1 quantizer once per channel.",
	DissolveSemantics:      "For evaluator weights wo,wi in [0,1] with wo+wi=1, evaluate outgoing*wo+incoming*wi per premultiplied linear-light channel, then apply the v1 quantizer once.",
	SourceOverSemantics:    "Porter-Duff source-over: out.rgb=src.rgb+dst.rgb*(1-src.a), out.a=src.a+dst.a*(1-src.a), with 65535 as exact one and one v1 quantization per output channel.",
	LayerOrder:             "Layer slices are back-to-front: index 0 is the bottom layer and each later layer is source-over composited on top in array order. Host map iteration and parallel completion order never define z-order.",
	CanonicalSerialization: "Hash serialization is row-major R,G,B,A with every uint16 encoded big-endian, exactly 8 bytes per pixel and no header, stride padding, or architecture-dependent bytes. Width, height, surface profile ID, and kernel profile ID are excluded and must be bound by the containing render/evidence manifest.",
	ExcludedStages:         "Media decode, YUV/RGB and transfer conversion, chroma upsampling, transform/resampling, masks, text/vector rasterization, output conversion, dithering, encoding and muxing are outside this profile and must be separately versioned.",
}

// Descriptor returns the immutable v1 kernel contract by value.
func Descriptor() ProfileDescriptor { return profileV1 }
