package color

import "github.com/CutlyLab/Cutly/render/cpucompositor"

const (
	OutputProfileSchemaVersion       = 1
	ProfileLinearRGBA16ToRGBA8SRGBV1 = "cutly.color.linear-bt709-premultiplied-rgba16-to-rgba8-srgb-straight.v1"
	OutputRGBAHashProfileV1          = "cutly.hash.sha256.top-down-tightly-packed-rgba8.v1"
	OutputTransferLUTSHA256V1        = "sha256:f037b4deaa34ab49c64d53dbb016c4f8659fc5409e99dbbb02e60930ba73da78"

	OutputPixelLayoutRGBA8 = "rgba8"
	OutputRowOrderTopDown  = "top-down"
	OutputAlphaStraight    = "straight"

	BytesPerOutputRGBA8Pixel      uint64 = 4
	HardMaxOutputDimension               = 65536
	DefaultMaxOutputRGBA8Bytes    uint64 = 256 << 20
	HardMaxOutputRGBA8Bytes       uint64 = 1 << 30
	OutputCanonicalWriteChunkSize        = 64 << 10
)

// OutputLimits bounds the one complete RGBA8 frame allocated by ConvertSurfaceToRGBA8.
// Caller-owned Surface storage and transient fixed-size writer scratch are excluded.
type OutputLimits struct {
	MaxWidth       int    `json:"max_width"`
	MaxHeight      int    `json:"max_height"`
	MaxOutputBytes uint64 `json:"max_output_bytes"`
}

func DefaultOutputLimits() OutputLimits {
	return OutputLimits{MaxWidth: 8192, MaxHeight: 8192, MaxOutputBytes: DefaultMaxOutputRGBA8Bytes}
}

// MetadataRGBA8SRGBOutputV1 describes every byte-level and color field of the v1 output frame.
func MetadataRGBA8SRGBOutputV1() Metadata {
	return Metadata{
		Profile: ProfileLinearRGBA16ToRGBA8SRGBV1, PixelLayout: OutputPixelLayoutRGBA8,
		RowOrder: OutputRowOrderTopDown, AlphaMode: OutputAlphaStraight,
		Primaries: PrimariesBT709, WhitePoint: WhitePointD65, Transfer: TransferSRGB,
		Range: RangeFull, ChromaSubsampling: ChromaNone,
		ICCProfile: MetadataAbsent, HDRMetadata: MetadataAbsent,
	}
}

// OutputProfileDescriptor freezes conversion out of the reference working surface. Text is
// normative for v1; changing any pixel-affecting rule requires a new profile.
type OutputProfileDescriptor struct {
	SchemaVersion            int          `json:"schema_version"`
	Profile                  string       `json:"profile"`
	InputSurfaceProfile      string       `json:"input_surface_profile"`
	InputSemantics           string       `json:"input_semantics"`
	OutputMetadata           Metadata     `json:"output_metadata"`
	OutputLayout             string       `json:"output_layout"`
	OutputColorimetry        string       `json:"output_colorimetry"`
	OutputAlpha              string       `json:"output_alpha"`
	AlphaQuantization        string       `json:"alpha_quantization"`
	Unpremultiplication      string       `json:"unpremultiplication"`
	TransferQuantization     string       `json:"transfer_quantization"`
	TransparentSemantics     string       `json:"transparent_semantics"`
	ForwardLUTSHA256         string       `json:"forward_lut_sha256"`
	OutputLUTSerialization   string       `json:"output_lut_serialization"`
	OutputLUTSHA256          string       `json:"output_lut_sha256"`
	RawHashProfile           string       `json:"raw_hash_profile"`
	HashSemantics            string       `json:"hash_semantics"`
	CancellationSemantics    string       `json:"cancellation_semantics"`
	AllocationSemantics      string       `json:"allocation_semantics"`
	HardMaxDimension         int          `json:"hard_max_dimension"`
	DefaultLimits            OutputLimits `json:"default_limits"`
	FailureSemantics         string       `json:"failure_semantics"`
	ExcludedResponsibilities string       `json:"excluded_responsibilities"`
}

var outputDescriptorV1 = OutputProfileDescriptor{
	SchemaVersion:            OutputProfileSchemaVersion,
	Profile:                  ProfileLinearRGBA16ToRGBA8SRGBV1,
	InputSurfaceProfile:      cpucompositor.SurfaceProfileV1,
	InputSemantics:           "One immutable, top-down, linear-light BT.709/D65 premultiplied RGBA16 Cutly Surface. Every channel is in [0,65535], RGB<=A, and 65535 is exact one.",
	OutputMetadata:           MetadataRGBA8SRGBOutputV1(),
	OutputLayout:             "Top-down, row-major, tightly packed R,G,B,A bytes with exactly width*4 stride, no row padding and no trailing bytes.",
	OutputColorimetry:        "RGB uses BT.709/sRGB primaries, D65 white, the sRGB transfer function and full range. Chroma subsampling is none.",
	OutputAlpha:              "Alpha is an independent linear 8-bit straight-alpha channel.",
	AlphaQuantization:        "Compute A8 by multiplying A16 by 255/65535 and rounding the complete exact non-negative rational once to nearest integer; an exact half chooses the even integer.",
	Unpremultiplication:      "When A16>0 and A8>0, compute each straight linear channel as premultiplied_channel*65535/A16 and round the complete exact rational once to nearest uint16; an exact half chooses the even integer. The original A16, not quantized A8, is the divisor.",
	TransferQuantization:     "Map the rounded straight linear uint16 to the encoded byte whose entry in the frozen v1 sRGB8-to-linear16 forward LUT has minimum absolute distance. At an exact distance tie choose the even encoded byte. Runtime uses an immutable 65536-byte table derived from that rule; no runtime power function or platform color API is used.",
	TransparentSemantics:     "A16=0 is necessarily transparent black by the input invariant. If alpha quantization produces A8=0, output exactly 0,0,0,0 even when A16 was nonzero; hidden RGB is never emitted.",
	ForwardLUTSHA256:         LUTSHA256V1,
	OutputLUTSerialization:   "Serialize the 65536 derived encoded bytes in ascending linear uint16 code order with no header.",
	OutputLUTSHA256:          OutputTransferLUTSHA256V1,
	RawHashProfile:           OutputRGBAHashProfileV1,
	HashSemantics:            "SHA-256 covers only the complete tightly packed RGBA8 bytes. Result separately binds width, height, stride, input/output profiles and hash profile.",
	CancellationSemantics:    "Check context before resource validation, before and after immutable Surface validation, once per output row, while hashing bounded byte chunks and immediately before publication. Failure returns no Result or partial Frame.",
	AllocationSemantics:      "Validate the absolute per-axis dimension gate, caller limits, geometry, overflow and exact width*height*4 output bytes before allocation. Conversion owns one complete RGBA8 buffer. Frame.Bytes returns a complete defensive copy; ReadView.CopyRow copies into one caller-owned exact-stride row; Frame.WriteCanonical exposes only fixed 64 KiB defensive scratch chunks to external writers. No accessor exposes the mutable backing storage. Caller Surface storage, the process-wide immutable 65536-byte derived transfer lookup and accessor scratch are excluded.",
	HardMaxDimension:         HardMaxOutputDimension,
	DefaultLimits:            DefaultOutputLimits(),
	FailureSemantics:         "Any context, surface, invariant, dimension, limit, allocation or publication failure returns a zero Result. Accessors reject nil and forged zero-value Frames.",
	ExcludedResponsibilities: "Media decode, input normalization, gamut conversion, ICC, HDR/tone mapping, resize, crop, affine transform, masks, text/vector rasterization, dithering, PNG or video encoding, YUV conversion, muxing, scheduling and publication are outside this profile.",
}

func OutputDescriptor() OutputProfileDescriptor { return outputDescriptorV1 }
