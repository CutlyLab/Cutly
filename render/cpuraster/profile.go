package cpuraster

import "github.com/CutlyLab/Cutly/render/cpucompositor"

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.raster.cpu-affine-bilinear-linear-rgba16.v1"
	TrigProfileV1        = "cutly.trig.pi-decimal-taylor40-q48.v1"

	TrigFractionBits = 48
	TaylorTerms      = 40
	DefaultExactBits = 512
	HardMaxExactBits = 4096
	HardMaxPixels    = 64 << 20
	HardMaxWorking   = 2 << 30
)

// Limits bounds host work without changing pixel semantics. MaxWorkingBytes covers the output
// []Pixel scratch frame and the immutable Surface copy; the caller-owned source Surface and a
// fixed-count arbitrary-precision affine/sampling scratch set are not counted. MaxExactBits bounds
// the latter's inputs. MaxOutputPixels is also a CPU-work bound for the exact reference sampler.
type Limits struct {
	MaxWidth        int    `json:"max_width"`
	MaxHeight       int    `json:"max_height"`
	MaxOutputPixels uint64 `json:"max_output_pixels"`
	MaxWorkingBytes uint64 `json:"max_working_bytes"`
	MaxExactBits    int    `json:"max_exact_bits"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: 4096, MaxHeight: 4096, MaxOutputPixels: 4 << 20,
		MaxWorkingBytes: 128 << 20, MaxExactBits: DefaultExactBits,
	}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	InputSurfaceProfile      string `json:"input_surface_profile"`
	OutputSurfaceProfile     string `json:"output_surface_profile"`
	CoordinateSystem         string `json:"coordinate_system"`
	TransformOrder           string `json:"transform_order"`
	DegenerateScale          string `json:"degenerate_scale"`
	CropSemantics            string `json:"crop_semantics"`
	SamplingSemantics        string `json:"sampling_semantics"`
	EdgeSemantics            string `json:"edge_semantics"`
	Quantization             string `json:"quantization"`
	TrigProfile              string `json:"trig_profile"`
	TrigPiDecimal            string `json:"trig_pi_decimal"`
	TrigTaylorTerms          int    `json:"trig_taylor_terms"`
	TrigFractionBits         int    `json:"trig_fraction_bits"`
	TrigSemantics            string `json:"trig_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	InputSurfaceProfile:      cpucompositor.SurfaceProfileV1,
	OutputSurfaceProfile:     cpucompositor.SurfaceProfileV1,
	CoordinateSystem:         "Source and destination use continuous square-pixel edge coordinates: top-left edge is (0,0), pixel (x,y) has center (x+1/2,y+1/2), +x is right and +y is down.",
	TransformOrder:           "Crop is selected in normalized full-source space first. Let a=(anchor.x*sourceWidth,anchor.y*sourceHeight). Forward geometry is p'=position+a+R_clockwise(rotation)*diag(scale.x,scale.y)*(p-a). Rasterization inverse-maps every destination pixel center through that expression; anchor remains relative to the full source.",
	DegenerateScale:          "If either scale component is exactly zero, the affine image has zero area and the complete output is transparent. Negative scale components mirror through the anchor.",
	CropSemantics:            "The crop rectangle is half-open in continuous source edge coordinates. Bilinear taps whose pixel centers fall outside the crop are transparent rather than clamped; an inverse-mapped filter footprint may still overlap an in-crop tap when its center is on or beyond an edge, producing a deterministic premultiplied-alpha crop boundary.",
	SamplingSemantics:        "Inverse-map each destination pixel center, subtract (1/2,1/2) to address the source pixel-center lattice, and combine the four floor/ceil taps with exact rational separable bilinear weights in linear-light premultiplied RGBA. Identity mapping reproduces source pixels exactly.",
	EdgeSemantics:            "A tap outside the full source or outside the half-open crop contributes transparent black. There is no edge extension, repeat, mirror, host filter or hidden resize policy.",
	Quantization:             "Form each complete exact weighted channel sum, then round once to nearest uint16; an exact half chooses the even integer. No intermediate pixel rounding, float, SIMD approximation, dithering or clamp is permitted.",
	TrigProfile:              TrigProfileV1,
	TrigPiDecimal:            piDecimalV1,
	TrigTaylorTerms:          TaylorTerms,
	TrigFractionBits:         TrigFractionBits,
	TrigSemantics:            "Reduce exact rational degrees to [-180,180), reflect to [-90,90], multiply by the frozen 101-significant-digit decimal pi rational, evaluate sine and cosine with exactly 40 alternating Taylor terms, apply quadrant signs, then round each once to a signed int64 coefficient divided by 2^48 using nearest-even. Those Q48 rationals are normative; inverse mapping divides by their exact determinant cos²+sin² rather than assuming rounded coefficients remain perfectly orthonormal.",
	CancellationSemantics:    "Check context before validation and allocation, at least every 64 destination pixels, before and after immutable Surface construction, and before return. Failure never returns a partial Surface.",
	AllocationSemantics:      "Validate input/output dimensions, output pixel count, exact-value bit bounds and two complete output-frame bytes before allocation. The source Surface is caller-owned and excluded from MaxWorkingBytes. A fixed-count common-denominator big.Int sampler scratch is also excluded; its coefficient size is bounded by MaxExactBits and the frozen Q48/Taylor profile, while it is reused across all pixels rather than retained per pixel.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "Media decode, color normalization, source frame selection, opacity, layer compositing, masks, perspective, non-square pixels, motion blur, text/vector rasterization, output conversion, encoding and muxing are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
