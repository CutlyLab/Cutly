package textraster

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/textlayout"
)

const (
	ProfileSchemaVersion      = 1
	ProfileV1                 = documentv2.TextRasterProfileSupersample8x8V1
	ReceiptSchemaVersion      = 1
	ReceiptProfileV1          = "cutly.raster.text-glyf-unhinted-supersample8x8.receipt.v1"
	DescriptorHashProfileV1   = "cutly.hash.sha256.text-raster-descriptor-json.v1"
	BindingHashProfileV1      = "cutly.hash.sha256.text-raster-binding-be.v1"
	CoverageHashProfileV1     = documentv2.TextCoverageHashProfileV1
	PixelHashProfileV1        = "cutly.hash.sha256.surface-rgba16be-pixels.v1"
	ShapingProvenanceStatusV1 = "cutly.text-shaping-provenance.caller-layout-unverified.v1"
)

type ProfileDescriptor struct {
	SchemaVersion         int    `json:"schema_version"`
	Profile               string `json:"profile"`
	ReceiptProfile        string `json:"receipt_profile"`
	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	LayoutSourceProfile   string `json:"layout_source_profile"`
	LayoutProfile         string `json:"layout_profile"`
	OutlineProfile        string `json:"outline_profile"`
	BindingHashProfile    string `json:"binding_hash_profile"`
	CoverageHashProfile   string `json:"coverage_hash_profile"`
	PaintProfile          string `json:"paint_profile"`
	KernelProfile         string `json:"kernel_profile"`
	SurfaceProfile        string `json:"surface_profile"`
	PixelHashProfile      string `json:"pixel_hash_profile"`
	BindingSemantics      string `json:"binding_semantics"`
	CoordinateSemantics   string `json:"coordinate_semantics"`
	Sampling              string `json:"sampling"`
	PointInFill           string `json:"point_in_fill"`
	CurveSemantics        string `json:"curve_semantics"`
	Composition           string `json:"composition"`
	HashSemantics         string `json:"hash_semantics"`
	BindingEncoding       string `json:"binding_encoding"`
	ResourceSemantics     string `json:"resource_semantics"`
	CancellationSemantics string `json:"cancellation_semantics"`
	FailureSemantics      string `json:"failure_semantics"`
	DefaultLimits         Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion: ProfileSchemaVersion, Profile: ProfileV1, ReceiptProfile: ReceiptProfileV1,
	DescriptorHashProfile: DescriptorHashProfileV1,
	LayoutSourceProfile:   sfntglyf.LayoutSourceProfileV1, LayoutProfile: textlayout.ProfileV1,
	OutlineProfile: documentv2.TextOutlineProfileQ26_6V1, BindingHashProfile: BindingHashProfileV1,
	CoverageHashProfile: CoverageHashProfileV1, PaintProfile: color.ProfileRGBA16SRGBPaintV1,
	KernelProfile: cpucompositor.KernelProfileV1, SurfaceProfile: cpucompositor.SurfaceProfileV1,
	PixelHashProfile:      PixelHashProfileV1,
	BindingSemantics:      "A live receipt-bound LayoutSource and live Layout are mandatory. Font, viewport and receipt profiles are cross-checked. Layout.ShapeProviderFingerprint equals the source compiled dependency fingerprint; the concrete provider instance is separately bound by ProviderHash and LayoutSourceHash. The exact glyph closure is derived from placements; every trusted font-space command is independently scaled by the explicit font size and compared value-for-value with the Layout outline before trusted geometry is rasterized. Glyph selection, advances, offsets and placements remain caller-supplied and are not authenticated as ShapeProvider output; every receipt states caller-layout-unverified shaping provenance and must not be used as provider-authenticated shaping evidence.",
	CoordinateSemantics:   "Trusted y-up font-space Q26.6 points are scaled once with symmetric nearest-even, then placed as x=origin_x+x and top-down y=baseline_y-y with checked int64 arithmetic. Only viewport sample centers are classified, so clipping is exact.",
	Sampling:              "Every pixel has 64 equally weighted centers at Q26.6 coordinates (8*x+i)*8+4,(8*y+j)*8+4 for i,j in [0,7].",
	PointInFill:           "Each placement is classified independently with nonzero winding and a +x ray. Crossings use lower-inclusive upper-exclusive endpoints and crossing_x strictly greater than sample_x. Filled samples from placements are unioned before coverage is counted.",
	CurveSemantics:        "Line and original quadratic Bezier commands are intersected algebraically with each sample row. Quadratics are never flattened; discriminants, endpoint decisions and crossing comparisons use bounded exact integers and square-root comparisons.",
	Composition:           "The union coverage count in [0,64] scales one linear-premultiplied RGBA16 paint with one nearest-even division by 64. Uncovered pixels are transparent black and glyph overlap never paints twice.",
	HashSemantics:         "CoverageHash is SHA-256 over str(coverage hash profile), u32 width, u32 height, u64 coverage length and exact row-major u8 coverage bytes. PixelHash is SHA-256 over exactly 8 bytes per pixel: raw row-major big-endian premultiplied RGBA16 channels; dimensions and its profile are bound by the receipt. BindingHash is a domain-separated fixed-width big-endian stream over the descriptor, source, provider, explicit unverified-shaping status, layout, outline, placement, coverage, paint, pixel, dimensions, font size and admitted resources, excluding only BindingHash itself.",
	BindingEncoding:       "str is u64be UTF-8 byte length followed by exact bytes; u16/u32/u64 are unsigned big-endian and i64 is its two's-complement u64 bit pattern. BindingHash preimage is str(binding_hash_profile), u32be profile schema version, then these Receipt fields in order: schema_version, profile, raster_profile, descriptor_hash_profile, descriptor_hash, layout_source_profile, layout_source_hash_profile, layout_source_hash, shape_provider_profile, shape_provider_hash_profile, shape_provider_hash, dependency_fingerprint_profile, dependency_fingerprint, font_content_hash_profile, font_content_hash, shaping_provenance_status, layout_receipt_profile, layout_hash_profile, layout_hash, glyph_stream_hash_profile, glyph_stream_hash, outline_set_hash_profile, outline_set_hash, placement_hash_profile, placement_hash, binding_hash_profile, coverage_hash_profile, coverage_hash, paint_profile, kernel_profile, surface_profile, pixel_hash_profile, pixel_hash, font_size_numerator, font_size_denominator, width_pixels, height_pixels, fill_r, fill_g, fill_b, fill_a, placement_count, unique_glyph_count, contour_count, command_count, unique_segment_count, segment_count, sample_count, canonical_binary_bytes, work_units, working_bytes, retained_bytes. binding_hash itself is omitted.",
	ResourceSemantics:     "Let W=width, H=height, P=W*H, G=placements, U=unique glyphs, C=commands, Q=unique segments, S=placed segments, L=1+ceil(log2(8W)), K=9P+16384. CanonicalBinaryBytes is the exact sum of the CoverageHash preimage, the 8P-byte PixelHash preimage and the BindingHash preimage; pre-admission uses K. WorkUnits is (8H*S*2*(L+64)) + (64P*G) + 16C + 8S + 64P + 16P + K. WorkingBytes is 65536 + 9P + 72W + 8 + 64*(Q+S) + 96G + 128U + 48C. RetainedBytes is 8P+8192. Every operation is checked uint64 arithmetic. Limits only reject work and never select a lower quality path.",
	CancellationSemantics: "A non-nil context is checked before validation, throughout binding, geometry, sample rows, painting and hashing, and immediately before publication. Cancellation returns a nil Surface and zero Receipt.",
	FailureSemantics:      "Invalid or drifted bindings, unsupported profiles, malformed contours, overflow, exact/resource limits, allocation failure and cancellation fail closed with nil Surface and zero Receipt.",
	DefaultLimits:         DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func DescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(descriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var descriptorIdentityV1 = computeDescriptorHash()

func DescriptorHash() string { return descriptorIdentityV1 }

func computeDescriptorHash() string {
	canonical := DescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(DescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(DescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
