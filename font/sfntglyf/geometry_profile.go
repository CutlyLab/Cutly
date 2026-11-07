package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	GeometryProfileSchemaVersion    = 1
	GeometryProfileV1               = "cutly.font.static-glyf-control-geometry-exact.v1"
	GeometryReceiptSchemaVersion    = 1
	GeometryReceiptProfileV1        = "cutly.font.static-glyf-control-geometry-exact.receipt.v1"
	GeometryDescriptorHashProfileV1 = "cutly.hash.sha256.font-static-glyf-control-geometry-descriptor-json.v1"
	GeometryHashProfileV1           = "cutly.hash.sha256.font-static-glyf-control-geometry-be.v1"

	GeometryValidatedStatusV1  = "exact_real_control_geometry_validated_commands_not_authorized"
	GeometryBBoxStatusV1       = "simple_exact_compound_floor_min_ceil_max_verified"
	GeometryHeadBBoxStatusV1   = "contour_glyph_union_exact"
	GeometryHheaStatusV1       = "contour_glyph_extrema_exact"
	GeometryMetricsStatusV1    = "use_my_metrics_last_identity_exact"
	GeometryHintingStatusV1    = "opaque_bounds_validated_unexecuted"
	GeometryLayoutStatusV1     = "gdef_gsub_gpos_opaque_not_shape_authorized"
	GeometryCommandStatusV1    = "implied_points_q26_6_commands_not_materialized"
	GeometryCapabilityStatusV1 = "not_advertised"
	GeometryHeadLSBSetV1       = "set_verified_all_glyphs"
	GeometryHeadLSBUnsetV1     = "unset_not_asserted"
)

const (
	DefaultMaxGeometryExpandedPoints   uint64 = 16_777_216
	DefaultMaxGeometryExpandedContours uint64 = 4_194_304
	DefaultMaxGeometryExpandedEdges    uint64 = 16_777_216
	DefaultMaxGeometryExpandedGlyphs   uint64 = 1 << 28
	DefaultMaxGeometryTransformVisits  uint64 = 1 << 28
	DefaultMaxGeometryExactBits        uint32 = 512
	DefaultMaxGeometryExactOps         uint64 = 1 << 34
	DefaultMaxGeometryExactLimbUnits   uint64 = 1 << 36
	DefaultMaxGeometryCanonicalBytes   uint64 = 512 << 20
	DefaultMaxGeometryWorkUnits        uint64 = 1 << 44
	DefaultMaxGeometryWorkingBytes     uint64 = 1 << 30
	DefaultMaxGeometryRetainedBytes    uint64 = 512 << 20

	HardMaxGeometryExpandedPoints   uint64 = 1 << 34
	HardMaxGeometryExpandedContours uint64 = 1 << 34
	HardMaxGeometryExpandedEdges    uint64 = 1 << 36
	HardMaxGeometryExpandedGlyphs   uint64 = 1 << 40
	HardMaxGeometryTransformVisits  uint64 = 1 << 40
	HardMaxGeometryExactBits        uint32 = 4096
	HardMaxGeometryExactOps         uint64 = 1 << 48
	HardMaxGeometryExactLimbUnits   uint64 = 1 << 52
	HardMaxGeometryCanonicalBytes   uint64 = 1 << 30
	HardMaxGeometryWorkUnits        uint64 = 1 << 60
	HardMaxGeometryWorkingBytes     uint64 = 2 << 30
	HardMaxGeometryRetainedBytes    uint64 = 2 << 30
)

type GeometryLimits struct {
	MaxExpandedPointInstances   uint64 `json:"max_expanded_point_instances"`
	MaxExpandedContourInstances uint64 `json:"max_expanded_contour_instances"`
	MaxExpandedEdgeVisits       uint64 `json:"max_expanded_edge_visits"`
	MaxExpandedGlyphInstances   uint64 `json:"max_expanded_glyph_instances"`
	MaxTransformPointVisits     uint64 `json:"max_transform_point_visits"`
	MaxExactBits                uint32 `json:"max_exact_bits"`
	MaxExactOps                 uint64 `json:"max_exact_ops"`
	MaxExactLimbUnits           uint64 `json:"max_exact_limb_units"`
	MaxCanonicalBinaryBytes     uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits                uint64 `json:"max_work_units"`
	MaxWorkingBytes             uint64 `json:"max_working_bytes"`
	MaxRetainedBytes            uint64 `json:"max_retained_bytes"`
}

func DefaultGeometryLimits() GeometryLimits {
	return GeometryLimits{
		MaxExpandedPointInstances:   DefaultMaxGeometryExpandedPoints,
		MaxExpandedContourInstances: DefaultMaxGeometryExpandedContours,
		MaxExpandedEdgeVisits:       DefaultMaxGeometryExpandedEdges,
		MaxExpandedGlyphInstances:   DefaultMaxGeometryExpandedGlyphs,
		MaxTransformPointVisits:     DefaultMaxGeometryTransformVisits,
		MaxExactBits:                DefaultMaxGeometryExactBits, MaxExactOps: DefaultMaxGeometryExactOps,
		MaxExactLimbUnits:       DefaultMaxGeometryExactLimbUnits,
		MaxCanonicalBinaryBytes: DefaultMaxGeometryCanonicalBytes,
		MaxWorkUnits:            DefaultMaxGeometryWorkUnits, MaxWorkingBytes: DefaultMaxGeometryWorkingBytes,
		MaxRetainedBytes: DefaultMaxGeometryRetainedBytes,
	}
}

type GeometryProfileDescriptor struct {
	SchemaVersion            int            `json:"schema_version"`
	Profile                  string         `json:"profile"`
	ReceiptProfile           string         `json:"receipt_profile"`
	SourceProfile            string         `json:"source_profile"`
	DescriptorHashProfile    string         `json:"descriptor_hash_profile"`
	GeometryHashProfile      string         `json:"geometry_hash_profile"`
	ExactSemantics           string         `json:"exact_semantics"`
	ExpansionSemantics       string         `json:"expansion_semantics"`
	OffsetSemantics          string         `json:"offset_semantics"`
	PointMatchSemantics      string         `json:"point_match_semantics"`
	BBoxSemantics            string         `json:"bbox_semantics"`
	MetricsSemantics         string         `json:"metrics_semantics"`
	DescriptorHashSemantics  string         `json:"descriptor_hash_semantics"`
	HashSemantics            string         `json:"hash_semantics"`
	GeometryHashPreimage     string         `json:"geometry_hash_preimage"`
	ResourceSemantics        string         `json:"resource_semantics"`
	OwnershipSemantics       string         `json:"ownership_semantics"`
	CancellationSemantics    string         `json:"cancellation_semantics"`
	FailureSemantics         string         `json:"failure_semantics"`
	CapabilitySemantics      string         `json:"capability_semantics"`
	ExcludedResponsibilities string         `json:"excluded_responsibilities"`
	DefaultLimits            GeometryLimits `json:"default_limits"`
}

var geometryDescriptorV1 = GeometryProfileDescriptor{
	SchemaVersion: GeometryProfileSchemaVersion, Profile: GeometryProfileV1,
	ReceiptProfile: GeometryReceiptProfileV1, SourceProfile: OutlineReceiptProfileV1,
	DescriptorHashProfile: GeometryDescriptorHashProfileV1, GeometryHashProfile: GeometryHashProfileV1,
	ExactSemantics:           "All coordinates use normalized immutable dyadics numerator/2^shift. Zero is (0,0); a nonzero numerator is odd when shift is nonzero. Every operation owns a fresh big integer, never uses float or big.Rat and never mutates or aliases an input. Shift and integer conversions are portable to 386; obvious oversize work is rejected before large allocation, every normalized result is gated by exact bits, and operations charge platform-independent logical 64-bit limbs rather than machine big.Word units.",
	ExpansionSemantics:       "Every glyph/root is expanded from phase-one private real raw controls; simple and empty controls begin as exact integers and no implied point is synthesized. The disk matrix xscale,scale01,scale10,yscale maps x'=xscale*x+scale10*y and y'=scale01*x+yscale*y. Nested components remain exact with no per-level rounding and no child header bbox substitution. Transform-point visits count repeated and depth-amplified work across every root before exact expansion.",
	OffsetSemantics:          "UNSCALED_COMPONENT_OFFSET and the phase-one default apply M*p+integerOffset. SCALED_COMPONENT_OFFSET applies M*(p+integerOffset). ROUND_XY_TO_GRID was rejected upstream and instructions remain opaque and unexecuted.",
	PointMatchSemantics:      "Point attachment transforms the child first, then translates by accumulatedParent[arg1]-transformedChild[arg2] and adds that exact translation to every child control. Indices address expanded real controls only; implied points do not exist and phantom-point attachment was rejected upstream. An empty child is valid only for XY placement.",
	BBoxSemantics:            "Simple bboxes retain the phase-one exact raw-control invariant. Compound minima are mathematical floor of exact minima and maxima are mathematical ceil of exact maxima; no-control glyphs require zero bounds. head is the exact union of glyphs with contours. Q26.6 and implied points are not used.",
	MetricsSemantics:         "hhea minLSB, minRSB and xMaxExtent are recomputed with checked arithmetic over glyphs with contours; an empty set requires zero. head flag bit 1, when set, asserts xMin equals lsb for every glyph including empty and zero-contour glyphs; when clear it is recorded as unset_not_asserted. The last flagged direct USE_MY_METRICS component wins recursively; every flagged matrix is identity and the composite hmtx advance and lsb equal the ultimate source, whose GID is bound per glyph.",
	DescriptorHashSemantics:  "The descriptor hash is SHA-256 over u64be(len(descriptor_hash_profile)) followed by its exact UTF-8 bytes, then u64be(len(canonical_descriptor_json)) followed by the exact minified encoding/json bytes of this ordered map-free descriptor. It is lowercase hex with the sha256: prefix.",
	HashSemantics:            "The geometry hash is streaming SHA-256 over the declared preimage. str is u64be(len(UTF-8 bytes)) followed by the exact UTF-8 bytes; u8, u32 and u64 are unsigned fixed-width big-endian integers; i32 is its two's-complement uint32 bit pattern encoded big-endian. A dyadic is sign u8 (zero=0,positive=1,negative=2), shift u32, magnitude length u64 and minimal unsigned big-endian magnitude; zero has shift and length zero. No JSON, map order, float or native integer enters the preimage.",
	GeometryHashPreimage:     "str(hash_profile),u32(schema),str(profile),str(descriptor_hash),str(source_profile),str(source_descriptor_hash),str(source_graph_hash),str(upstream_content_profile),str(upstream_content_hash),u32(glyph_count); per gid: u32(gid),u8(kind empty=0/simple=1/compound=2),u8(has_contours false=0/true=1),u64(contours),each u32(endpoint),u64(points),each dyadic(x),dyadic(y),u8(on_curve false=0/true=1),i32(xMin,yMin,xMax,yMax),u32(advance),i32(lsb),u32(effective_metric_source); then i32(head bbox four,hhea minLSB,minRSB,xMaxExtent),str(head_lsb_status).",
	ResourceSemantics:        "Let R,G,P,C,E,I,T,O,L,B be revalidated source-graph canonical bytes, root glyphs, all-root expanded point/contour/edge instances, all-root recursive glyph expansion instances, transform-point visits, exact operations, logical limb units and geometry canonical bytes. Before source rehash, a checked receipt-only admission lower bound includes R and all then-known G,P,C,E,I,O,L,B minima; the source graph is then rehashed and its actual canonical bytes must equal R. I=G+E because every expanded edge invokes one non-root glyph; a graph DP verifies I and gates I and T before expansion, including zero-control component DAGs. work=R+64G+16P+8C+32E+16I+12T+8O+L+B. After topology, fixed charges including verified R, I and T are bound before metrics or expansion. One shared incremental work meter always reserves the known lower bounds for O, L and B, charges every exact operation, logical limb unit and geometry canonical byte before that work proceeds, and finally must equal the receipt formula exactly. retained=source_retained+32768+64G+owned_receipt_string_bytes. working=69632+24G+3*max_root_points*(32+2*(64+ceil(exact_bits_limit/8)))+8*max_root_contours+128*(max_depth+1). Values are checked portable logical charges, not allocator or RSS measurements.",
	OwnershipSemantics:       "Validation borrows the immutable OutlineFont for the call; success retains it and compact scalar per-glyph metadata only. Exact points and scratch are operation-local, no mutable cache or slice view is exposed, and concurrent reads are safe.",
	CancellationSemantics:    "A non-nil context is checked before work, through topology preflight, expansion, exact arithmetic, attachment, bbox, metrics, hashing and immediately before publication. Cancellation publishes nil and a zero receipt.",
	FailureSemantics:         "Invalid source bindings, exact/resource overflow, geometry/bbox/metrics mismatch, cancellation, allocation failure and invariant drift fail closed with nil GeometryFont and a strictly zero receipt.",
	CapabilitySemantics:      "This stage validates exact real control geometry only. Commands, implied points, Q26.6, shaping, layout, raster and cutly.text.pinned-font-viewport-px.v2 remain unauthorized; no capability is advertised.",
	ExcludedResponsibilities: "Implied-point synthesis, Move/Line/Quad/Close materialization, Q26.6 rounding, GDEF/GSUB/GPOS, go-text/HarfBuzz shaping, bidi, fallback, raster, paint, encoding, persistence and publication are outside this profile.",
	DefaultLimits:            DefaultGeometryLimits(),
}

func GeometryDescriptor() GeometryProfileDescriptor { return geometryDescriptorV1 }

func GeometryDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(geometryDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var geometryDescriptorHashV1 = computeGeometryDescriptorHash()

func GeometryDescriptorHash() string { return geometryDescriptorHashV1 }

func computeGeometryDescriptorHash() string {
	canonical := GeometryDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(GeometryDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(GeometryDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func normalizeGeometryLimits(value GeometryLimits) (GeometryLimits, error) {
	defaults := DefaultGeometryLimits()
	if value.MaxExpandedPointInstances == 0 {
		value.MaxExpandedPointInstances = defaults.MaxExpandedPointInstances
	}
	if value.MaxExpandedContourInstances == 0 {
		value.MaxExpandedContourInstances = defaults.MaxExpandedContourInstances
	}
	if value.MaxExpandedEdgeVisits == 0 {
		value.MaxExpandedEdgeVisits = defaults.MaxExpandedEdgeVisits
	}
	if value.MaxExpandedGlyphInstances == 0 {
		value.MaxExpandedGlyphInstances = defaults.MaxExpandedGlyphInstances
	}
	if value.MaxTransformPointVisits == 0 {
		value.MaxTransformPointVisits = defaults.MaxTransformPointVisits
	}
	if value.MaxExactBits == 0 {
		value.MaxExactBits = defaults.MaxExactBits
	}
	if value.MaxExactOps == 0 {
		value.MaxExactOps = defaults.MaxExactOps
	}
	if value.MaxExactLimbUnits == 0 {
		value.MaxExactLimbUnits = defaults.MaxExactLimbUnits
	}
	if value.MaxCanonicalBinaryBytes == 0 {
		value.MaxCanonicalBinaryBytes = defaults.MaxCanonicalBinaryBytes
	}
	if value.MaxWorkUnits == 0 {
		value.MaxWorkUnits = defaults.MaxWorkUnits
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if value.MaxRetainedBytes == 0 {
		value.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.MaxExpandedPointInstances > HardMaxGeometryExpandedPoints || value.MaxExpandedContourInstances > HardMaxGeometryExpandedContours ||
		value.MaxExpandedEdgeVisits > HardMaxGeometryExpandedEdges || value.MaxExpandedGlyphInstances > HardMaxGeometryExpandedGlyphs ||
		value.MaxTransformPointVisits > HardMaxGeometryTransformVisits ||
		value.MaxExactBits > HardMaxGeometryExactBits || value.MaxExactOps > HardMaxGeometryExactOps ||
		value.MaxExactLimbUnits > HardMaxGeometryExactLimbUnits || value.MaxCanonicalBinaryBytes > HardMaxGeometryCanonicalBytes ||
		value.MaxWorkUnits > HardMaxGeometryWorkUnits || value.MaxWorkingBytes > HardMaxGeometryWorkingBytes || value.MaxRetainedBytes > HardMaxGeometryRetainedBytes {
		return GeometryLimits{}, fontError(CodeGeometryLimitsInvalid, "geometry/limits", ErrLimitsInvalid)
	}
	return value, nil
}
