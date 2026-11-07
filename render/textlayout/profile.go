package textlayout

import "github.com/CutlyLab/Cutly/core/documentv2"

const (
	ProfileSchemaVersion            = 1
	ProfileV1                       = "cutly.layout.text-unwrapped-top-baseline-logical-align.v1"
	NormalizedShapedStreamProfileV1 = "cutly.text-shaped-stream.logical-pen-q26_6.v1"
	ReceiptSchemaVersion            = 1
	ReceiptProfileV1                = "cutly.layout.text-unwrapped-top-baseline-logical-align.receipt.v1"
	GlyphStreamHashProfileV1        = "cutly.hash.sha256.text-normalized-shaped-glyph-stream-be.v1"
	OutlineSetHashProfileV1         = "cutly.hash.sha256.text-scaled-outline-set-be.v1"
	PlacementHashProfileV1          = "cutly.hash.sha256.text-glyph-placement-stream-be.v1"
	LayoutHashProfileV1             = "cutly.hash.sha256.text-layout-binding-be.v1"
)

type ProfileDescriptor struct {
	SchemaVersion                int    `json:"schema_version"`
	Profile                      string `json:"profile"`
	TextProfile                  string `json:"text_profile"`
	RunInputProfile              string `json:"run_input_profile"`
	ShapedStreamProfile          string `json:"shaped_stream_profile"`
	FontProfile                  string `json:"font_profile"`
	OutlineProfile               string `json:"outline_profile"`
	ReceiptProfile               string `json:"receipt_profile"`
	GlyphStreamHashProfile       string `json:"glyph_stream_hash_profile"`
	OutlineSetHashProfile        string `json:"outline_set_hash_profile"`
	PlacementHashProfile         string `json:"placement_hash_profile"`
	LayoutHashProfile            string `json:"layout_hash_profile"`
	TextSemanticHashProfile      string `json:"text_semantic_hash_profile"`
	LayoutInputHashProfile       string `json:"layout_input_hash_profile"`
	UnicodeTableProfile          string `json:"unicode_table_profile"`
	UnicodeTableHashProfile      string `json:"unicode_table_hash_profile"`
	UnicodeTableHash             string `json:"unicode_table_hash"`
	MaxLanguageBytes             int    `json:"max_language_bytes"`
	MaxShapeProviderProfileBytes int    `json:"max_shape_provider_profile_bytes"`
	CoordinateSemantics          string `json:"coordinate_semantics"`
	ProviderNormalization        string `json:"provider_normalization"`
	LanguageSemantics            string `json:"language_semantics"`
	LineSemantics                string `json:"line_semantics"`
	ClusterSemantics             string `json:"cluster_semantics"`
	AdvanceSemantics             string `json:"advance_semantics"`
	BaselineSemantics            string `json:"baseline_semantics"`
	AlignmentSemantics           string `json:"alignment_semantics"`
	PlacementSemantics           string `json:"placement_semantics"`
	ContourSemantics             string `json:"contour_semantics"`
	BindingSemantics             string `json:"binding_semantics"`
	CanonicalEncoding            string `json:"canonical_encoding"`
	GlyphStreamPreimage          string `json:"glyph_stream_preimage"`
	OutlineSetPreimage           string `json:"outline_set_preimage"`
	PlacementPreimage            string `json:"placement_preimage"`
	LayoutPreimage               string `json:"layout_preimage"`
	HashSemantics                string `json:"hash_semantics"`
	ResourceSemantics            string `json:"resource_semantics"`
	CancellationSemantics        string `json:"cancellation_semantics"`
	OwnershipSemantics           string `json:"ownership_semantics"`
	FailureSemantics             string `json:"failure_semantics"`
	ExcludedResponsibilities     string `json:"excluded_responsibilities"`
	DefaultLimits                Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:                ProfileSchemaVersion,
	Profile:                      ProfileV1,
	TextProfile:                  documentv2.TextProfilePinnedFontViewportPXV2,
	RunInputProfile:              documentv2.TextRunInputProfileV1,
	ShapedStreamProfile:          NormalizedShapedStreamProfileV1,
	FontProfile:                  documentv2.TextFontProfileStaticGlyfV1,
	OutlineProfile:               documentv2.TextOutlineProfileQ26_6V1,
	ReceiptProfile:               ReceiptProfileV1,
	GlyphStreamHashProfile:       GlyphStreamHashProfileV1,
	OutlineSetHashProfile:        OutlineSetHashProfileV1,
	PlacementHashProfile:         PlacementHashProfileV1,
	LayoutHashProfile:            LayoutHashProfileV1,
	TextSemanticHashProfile:      documentv2.TextSemanticHashProfileV1,
	LayoutInputHashProfile:       documentv2.TextLayoutInputHashProfileV1,
	UnicodeTableProfile:          documentv2.TextUnicodeValidationTableProfileV1,
	UnicodeTableHashProfile:      documentv2.TextUnicodeValidationTableHashProfileV1,
	UnicodeTableHash:             documentv2.TextUnicodeValidationTableHashV1,
	MaxLanguageBytes:             HardMaxLanguageBytes,
	MaxShapeProviderProfileBytes: maxProfileTokenBytes,
	CoordinateSemantics:          "Shaped advances, offsets and provider-normalized static-glyf outline points enter as signed font-space Q26.6. Each complete value is multiplied by the exact positive font_size and divided by units_per_em, then rounded once to viewport-pixel Q26.6 with symmetric nearest-even. Outlines remain y-up; glyph placement maps viewport x=origin_x+outline_x and viewport y=baseline_y-outline_y. Parsing raw design-unit glyf coordinates and applying compound transforms before the one Q26.6 normalization round belong to the external font/shape provider.",
	ProviderNormalization:        "Input glyph records are post-shape normalized into original-text logical order and actual signed pen traversal. LTR records retain provider order. For a raw RTL stream in reverse cluster order, the external provider reverses the complete glyph-record sequence and maps each raw record to x_advance=-raw_x_advance, x_offset=raw_x_offset-raw_x_advance, y_advance=0 and y_offset=raw_y_offset. With the normalized pen starting at the raw total advance, this preserves the algebraic raw glyph origins in input font-space Q26.6 before viewport scaling. It makes no equality claim after T1 independently scales and nearest-even rounds each complete advance/offset. Build does not perform this conversion; ShapeProviderProfile must version it and nonnormalized RTL input is rejected.",
	LanguageSemantics:            "language is an explicit 2-to-8-letter primary BCP-47-style subtag followed by zero or more hyphen-separated 1-to-8 alphanumeric subtags, with a hard total UTF-8 byte limit of 128. Locale inference and empty tags are forbidden.",
	LineSemantics:                "The Lines array must exactly partition the original UTF-8 Text at U+000A bytes, excluding the delimiter from each [byte_start,byte_end) range while retaining leading, trailing and consecutive empty lines. An empty line has exactly zero glyphs and a nonempty line has at least one glyph. There is no automatic wrapping.",
	ClusterSemantics:             "Every cluster is a UTF-8 scalar boundary in its line and an absolute byte offset into the original complete Text. Post-normalization clusters are monotone nondecreasing for both LTR and RTL. Every nonempty line has at least one glyph and its first/minimum cluster equals byte_start.",
	AdvanceSemantics:             "This horizontal profile requires y_advance=0. Individual x_advance values may have either sign and are scaled independently before pen accumulation. Only the overflow-checked scaled line sum A is sign-gated: A>=0 for LTR and A<=0 for RTL.",
	BaselineSemantics:            "hhea_ascender is the complete signed int16 metric, including zero and negative values. Line i baseline is computed directly as nearest-even(64*(hhea_ascender*font_size/units_per_em+i*line_height)); rounded baselines are never accumulated.",
	AlignmentSemantics:           "Let W=viewport_width*64, left=min(0,A), right=max(0,A). The direct initial pen is -left for LTR start and RTL end, W-right for RTL start and LTR end, and nearest-even((W-left-right)/2) for center. The center half may lie on a 1/128-pixel boundary and is rounded exactly once to Q26.6. Ink bounds, bearings and glyph offsets do not affect alignment.",
	PlacementSemantics:           "Before each pen advance, origin_x=pen_x+scaled_x_offset and baseline_y=line_baseline-scaled_y_offset; positive y_offset moves ink upward. Then add the signed independently scaled x_advance.",
	ContourSemantics:             "Every referenced nonzero glyph ID has exactly one outline in strict ID order. An external provider may certify any glyph as no-ink by supplying no commands; this package cannot infer whitespace because it performs no codepoint-to-glyph mapping. A nonempty outline is a sequence of explicit contours, each beginning with move_to and ending with close; zero-segment move_to/close contours are accepted as explicit degenerate geometry. There is no implicit contour closure or TrueType off-curve midpoint synthesis.",
	BindingSemantics:             "TextSemanticHash, LayoutInputHash, FontContentHash and ShapeProviderFingerprint are canonical caller-supplied claims, not signatures or provider attestations. Build checks their spelling, the exact frozen Unicode table identifiers and a lowercase provider-profile grammar with exactly one terminal .v[1-9][0-9]* version segment and a 256-byte maximum, but cannot recompute them without the source snapshot, font bytes and provider receipt. Assembly must derive Input from a T0-validated immutable snapshot and live-validate those bindings before treating this receipt as render evidence.",
	CanonicalEncoding:            "Every hash preimage starts with its length-prefixed hash-profile token. str is uint64 byte length followed by exact bytes; u8/u32/u64 are unsigned big-endian; i32/i64 are two's-complement big-endian. Schema versions, IDs and dimensions use u32; counts and byte offsets use u64; hhea_ascender uses i32; Q26.6 and Rational components use i64. Outline op tags are move_to=1,line_to=2,quad_to=3,close=4. Arrays retain declared order. No padding, JSON, Unicode normalization or host integer encoding is used.",
	GlyphStreamPreimage:          "str(domain),u32(profile_schema),str(layout_profile),str(text_profile),str(run_input_profile),str(shaped_stream_profile),str(font_profile),str(outline_profile),binding,text/language/script/direction strings,u32(units_per_em),u64(line_count),then per line u64(byte_start,byte_end,glyph_count),then per glyph u32(glyph_id),u64(cluster),i64(x_advance,y_advance,x_offset,y_offset). Binding order is semantic-hash profile/value, layout-input-hash profile/value, font hash, Unicode table profile/hash-profile/hash, shape-provider profile/fingerprint.",
	OutlineSetPreimage:           "str(domain),u32(profile_schema),str(layout_profile),str(font_profile),str(outline_profile),str(font_hash),i64(font_size numerator,denominator),u32(units_per_em),u64(outline_count),then per outline u32(glyph_id),u64(command_count),then per command u8(op),u64(point_count),then each independently scaled point i64(x,y).",
	PlacementPreimage:            "str(domain),u32(profile_schema),str(layout_profile),str(glyph_stream_hash),u32(viewport width,height),i64(font_size numerator,denominator,line_height numerator,denominator),u32(units_per_em),i32(hhea_ascender),str(direction),str(alignment),u64(line_count),then per line u32(index),u64(byte_start,byte_end),i64(baseline,total_advance,initial_pen),u64(glyph_count),then each placement u32(line_index),u64(glyph_index),u32(glyph_id),u64(cluster),i64(origin_x,baseline_y).",
	LayoutPreimage:               "str(domain),u32(profile_schema,receipt_schema),str(receipt/layout/text/run/shaped-stream/font/outline profiles),binding,str(each glyph/outline/placement hash profile and value),str(layout_hash_profile),u32(viewport width,height),i64(font_size numerator,denominator,line_height numerator,denominator),str(language,script,direction,alignment),u32(units_per_em),i32(hhea_ascender),u64(line/glyph/unique-glyph/contour/command counts,canonical bytes,work units,working bytes,retained bytes).",
	HashSemantics:                "SHA-256 hashes use the profile-defined length-prefixed big-endian binary encodings. The receipt separately records the concrete supplied shaped glyph stream, scaled outline set, placement stream and complete layout binding hashes. They prove deterministic transformation of those supplied values only; none authenticates a Document, font or external shape provider.",
	ResourceSemantics:            "The byte fields are deterministic portable logical charges, not Go allocator size-class, GC-heap or RSS measurements. All charge arithmetic is checked before proportional allocation. retained_bytes=1024+glyph_count*48+unique_glyph_count*32+command_count*48+owned_receipt_string_bytes. working_base=retained_bytes+65536+unique_glyph_count*1. work_units=glyph_count*10+command_count*4+text_utf8_bytes+line_count+unique_glyph_count+glyph_count*bits.Len64(unique_glyph_count), where bits.Len64(0)=0. canonical_binary_bytes is the checked sum of the four exact hash-preimage byte counts; working_bytes=working_base+canonical_binary_bytes. The fixed 1024 covers the portable Layout-with-Receipt record and compile-time dominates its Go size; owned_receipt_string_bytes is the exact sum of all retained string byte lengths, including four output digests. The fixed 65536 is a portable charge for bounded hash/exact scratch; the closure flag array is charged one byte per outline. Caller Input, slice/allocator bookkeeping, size-class slack and GC overhead are excluded. Hosts needing a true memory ceiling must add allocator/process isolation. max_exact_bits bounds every arbitrary-precision integer operand and intermediate used by exact scaling, baselines, alignment and rounding, including pre-round fractions; fixed-width Q26.6 add/sub overflow is checked separately in int64 arithmetic.",
	CancellationSemantics:        "A non-nil context is checked before validation, throughout text, line, glyph, outline, scaling, placement and hashing loops, and immediately before publication. Cancellation returns no Layout or Receipt.",
	OwnershipSemantics:           "Build borrows Input slices until return and callers must not mutate them concurrently. A successful Layout owns immutable scaled outlines and placements, rebuilds fixed receipt profiles from package constants, and clones every dynamic caller string retained by its Receipt so a small claim cannot retain a larger caller backing buffer. Read views expose values and indexed access but no mutable slices.",
	FailureSemantics:             "Unknown profiles, malformed UTF-8 segmentation or clusters, noncanonical upstream claim spellings, vertical advances, invalid direction/alignment, glyph zero, missing/extra/duplicate outlines, open contours, exact/resource overflow, cancellation and invariant drift fail closed with nil Layout and zero Receipt. A successful Build does not prove that upstream claims match external bytes.",
	ExcludedResponsibilities:     "T0 frozen-script/control/default-ignorable revalidation, font files, URI resolution, recomputing or authenticating upstream hashes, cmap, GSUB/GPOS shaping, bidi inference, fallback, missing-glyph substitution, raw glyf/compound normalization, implied TrueType points, outline rasterization, coverage, paint, transforms, compositing, encoding, persistence and publication are outside this package.",
	DefaultLimits:                DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

type Receipt struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	LayoutProfile            string `json:"layout_profile"`
	TextProfile              string `json:"text_profile"`
	RunInputProfile          string `json:"run_input_profile"`
	ShapedStreamProfile      string `json:"shaped_stream_profile"`
	FontProfile              string `json:"font_profile"`
	OutlineProfile           string `json:"outline_profile"`
	TextSemanticHashProfile  string `json:"text_semantic_hash_profile"`
	TextSemanticHash         string `json:"text_semantic_hash"`
	LayoutInputHashProfile   string `json:"layout_input_hash_profile"`
	LayoutInputHash          string `json:"layout_input_hash"`
	FontContentHash          string `json:"font_content_hash"`
	UnicodeTableProfile      string `json:"unicode_table_profile"`
	UnicodeTableHashProfile  string `json:"unicode_table_hash_profile"`
	UnicodeTableHash         string `json:"unicode_table_hash"`
	ShapeProviderProfile     string `json:"shape_provider_profile"`
	ShapeProviderFingerprint string `json:"shape_provider_fingerprint"`
	GlyphStreamHashProfile   string `json:"glyph_stream_hash_profile"`
	GlyphStreamHash          string `json:"glyph_stream_hash"`
	OutlineSetHashProfile    string `json:"outline_set_hash_profile"`
	OutlineSetHash           string `json:"outline_set_hash"`
	PlacementHashProfile     string `json:"placement_hash_profile"`
	PlacementHash            string `json:"placement_hash"`
	LayoutHashProfile        string `json:"layout_hash_profile"`
	LayoutHash               string `json:"layout_hash"`
	WidthPixels              uint32 `json:"width_pixels"`
	HeightPixels             uint32 `json:"height_pixels"`
	LineCount                uint64 `json:"line_count"`
	GlyphCount               uint64 `json:"glyph_count"`
	UniqueGlyphCount         uint64 `json:"unique_glyph_count"`
	ContourCount             uint64 `json:"contour_count"`
	CommandCount             uint64 `json:"command_count"`
	CanonicalBinaryBytes     uint64 `json:"canonical_binary_bytes"`
	WorkUnits                uint64 `json:"work_units"`
	WorkingBytes             uint64 `json:"working_bytes"`
	RetainedBytes            uint64 `json:"retained_bytes"`
}
