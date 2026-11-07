package color

import "github.com/CutlyLab/Cutly/render/cpucompositor"

const (
	ProfileSchemaVersion = 1
	ProfileRGBA8SRGBV1   = "cutly.color.rgba8-srgb-straight-to-linear-bt709-premultiplied-rgba16.v1"
	LUTSHA256V1          = "sha256:19e80c12c52c0dc6741aff32701ce91c64a83c2ae7387943c5325258108298e3"

	PixelLayoutRGBA8 = "rgba8"
	RowOrderTopDown  = "top-down"
	AlphaStraight    = "straight"
	PrimariesBT709   = "bt709"
	WhitePointD65    = "d65"
	TransferSRGB     = "srgb"
	RangeFull        = "full"
	ChromaNone       = "none"
	MetadataAbsent   = "absent"

	BytesPerInputPixel  uint64 = 4
	BytesPerOutputPixel uint64 = cpucompositor.BytesPerPixel
	HardMaxInputBytes   uint64 = 1 << 30
	HardMaxWorkingBytes uint64 = 2 << 30
)

// Metadata is deliberately verbose: no empty member means "use a platform default". A decoder
// or image adapter must prove every field before selecting this profile.
type Metadata struct {
	Profile           string `json:"profile"`
	PixelLayout       string `json:"pixel_layout"`
	RowOrder          string `json:"row_order"`
	AlphaMode         string `json:"alpha_mode"`
	Primaries         string `json:"primaries"`
	WhitePoint        string `json:"white_point"`
	Transfer          string `json:"transfer"`
	Range             string `json:"range"`
	ChromaSubsampling string `json:"chroma_subsampling"`
	ICCProfile        string `json:"icc_profile"`
	HDRMetadata       string `json:"hdr_metadata"`
}

// MetadataRGBA8SRGBV1 returns the only input metadata accepted by the v1 normalizer.
func MetadataRGBA8SRGBV1() Metadata {
	return Metadata{
		Profile: ProfileRGBA8SRGBV1, PixelLayout: PixelLayoutRGBA8, RowOrder: RowOrderTopDown,
		AlphaMode: AlphaStraight, Primaries: PrimariesBT709, WhitePoint: WhitePointD65,
		Transfer: TransferSRGB, Range: RangeFull, ChromaSubsampling: ChromaNone,
		ICCProfile: MetadataAbsent, HDRMetadata: MetadataAbsent,
	}
}

// Limits bounds both the final surface and the unavoidable temporary []Pixel plus copied Surface
// storage. The conversion never interprets these host limits as color semantics.
type Limits struct {
	MaxWidth        int    `json:"max_width"`
	MaxHeight       int    `json:"max_height"`
	MaxInputBytes   uint64 `json:"max_input_bytes"`
	MaxWorkingBytes uint64 `json:"max_working_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: 8192, MaxHeight: 8192,
		MaxInputBytes: 256 << 20, MaxWorkingBytes: 512 << 20,
	}
}

// ProfileDescriptor freezes every pixel-affecting rule and the boundaries this narrow profile
// does not implement. Changing one of these statements requires a new profile version.
type ProfileDescriptor struct {
	SchemaVersion            int      `json:"schema_version"`
	Profile                  string   `json:"profile"`
	InputMetadata            Metadata `json:"input_metadata"`
	InputLayout              string   `json:"input_layout"`
	InputColorimetry         string   `json:"input_colorimetry"`
	InputAlpha               string   `json:"input_alpha"`
	OutputSurfaceProfile     string   `json:"output_surface_profile"`
	LUTSerialization         string   `json:"lut_serialization"`
	LUTSHA256                string   `json:"lut_sha256"`
	LUTGeneration            string   `json:"lut_generation"`
	AlphaExpansion           string   `json:"alpha_expansion"`
	Premultiplication        string   `json:"premultiplication"`
	Quantization             string   `json:"quantization"`
	MetadataSemantics        string   `json:"metadata_semantics"`
	CancellationSemantics    string   `json:"cancellation_semantics"`
	AllocationSemantics      string   `json:"allocation_semantics"`
	DefaultLimits            Limits   `json:"default_limits"`
	ExcludedResponsibilities string   `json:"excluded_responsibilities"`
}

var profileDescriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileRGBA8SRGBV1,
	InputMetadata:            MetadataRGBA8SRGBV1(),
	InputLayout:              "Top-down, tightly packed RGBA8 bytes with exactly width*4 stride and straight alpha; no row padding or trailing bytes.",
	InputColorimetry:         "RGB uses BT.709/sRGB primaries, D65 white, the sRGB transfer function and full range. Chroma subsampling is none.",
	InputAlpha:               "Alpha is an independent linear 8-bit straight-alpha channel. RGB under alpha zero is accepted but becomes exact transparent black after premultiplication.",
	OutputSurfaceProfile:     cpucompositor.SurfaceProfileV1,
	LUTSerialization:         "The 256 uint16 entries are serialized by encoded-code order as unsigned big-endian values with no header.",
	LUTSHA256:                LUTSHA256V1,
	LUTGeneration:            "For encoded code c and v=c/255, evaluate the sRGB EOTF exactly: v/(1292/100) for c<=10, otherwise ((v+11/200)/(211/200))^(12/5). Multiply by 65535 and round once to nearest integer; an exact half chooses the even integer. The frozen LUT, not runtime pow, is normative.",
	AlphaExpansion:           "Expand 8-bit alpha a to exact RGBA16 alpha a*257.",
	Premultiplication:        "For each LUT-decoded linear RGB channel q, evaluate q*a/255 and round once to nearest integer with exact half ties choosing even. Alpha zero produces 0,0,0,0.",
	Quantization:             "No float, platform color API, intermediate RGB rounding beyond the frozen EOTF LUT, dithering, hidden clamp, or host-dependent approximation is permitted.",
	MetadataSemantics:        "Every metadata token must exactly match this profile, including explicit 'absent' tokens for ICC and HDR metadata. Empty, omitted, inferred or platform-defaulted metadata is rejected.",
	CancellationSemantics:    "Check context before allocation, once per output row, and immediately before and after final Surface construction. The final validated copy into the immutable Surface is atomic and no partial Surface is returned.",
	AllocationSemantics:      "Limits are checked before allocation. Conversion temporarily owns one []Pixel scratch frame and cpucompositor copies it into one immutable Surface, so working storage is exactly two RGBA16 frames apart from caller-owned input and fixed overhead.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "Media decode, YUV/RGB matrix and range conversion, chroma reconstruction, BT.709 transfer decoding, ICC profiles, HDR/tone mapping, resize/resampling, crop, rotation/SAR, text/vector rasterization, output transfer/YUV conversion, dithering, encoding and muxing are outside this profile.",
}

func Descriptor() ProfileDescriptor { return profileDescriptorV1 }
