package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	OutlineSetProfileSchemaVersion    = 1
	OutlineSetProfileV1               = "cutly.font.static-glyf-outline-set-q26_6.v1"
	OutlineSetOutlineProfileV1        = "cutly.font-outline.q26_6-nearest-even.v1"
	OutlineSetReceiptSchemaVersion    = 1
	OutlineSetReceiptProfileV1        = "cutly.font.static-glyf-outline-set-q26_6.receipt.v1"
	OutlineSetDescriptorHashProfileV1 = "cutly.hash.sha256.font-static-glyf-outline-set-q26_6-descriptor-json.v1"
	OutlineSetHashProfileV1           = "cutly.hash.sha256.font-static-glyf-outline-set-q26_6-be.v1"

	OutlineSetGeometryStatusV1     = "source_exact_geometry_revalidated"
	OutlineSetImpliedPointStatusV1 = "truetype_implied_on_curve_exact_canonical"
	OutlineSetCommandStatusV1      = "move_line_quad_close_q26_6_materialized"
	OutlineSetRoundingStatusV1     = "font_space_symmetric_nearest_even_once"
	OutlineSetHintingStatusV1      = "opaque_bounds_validated_unexecuted"
	OutlineSetLayoutStatusV1       = "gdef_gsub_gpos_opaque_not_shape_authorized"
	OutlineSetRasterStatusV1       = "not_rasterized"
	OutlineSetCapabilityStatusV1   = "not_advertised"
)

const (
	DefaultMaxOutlineSetExpandedPoints   uint64 = DefaultMaxGeometryExpandedPoints
	DefaultMaxOutlineSetExpandedContours uint64 = DefaultMaxGeometryExpandedContours
	DefaultMaxOutlineSetExpandedEdges    uint64 = DefaultMaxGeometryExpandedEdges
	DefaultMaxOutlineSetExpandedGlyphs   uint64 = DefaultMaxGeometryExpandedGlyphs
	DefaultMaxOutlineSetTransformVisits  uint64 = DefaultMaxGeometryTransformVisits
	DefaultMaxOutlineSetImpliedPoints    uint64 = DefaultMaxGeometryExpandedPoints
	DefaultMaxOutlineSetDrawingCommands  uint64 = DefaultMaxGeometryExpandedPoints
	DefaultMaxOutlineSetCommands         uint64 = DefaultMaxGeometryExpandedPoints + 2*DefaultMaxGeometryExpandedContours
	DefaultMaxOutlineSetCommandPoints    uint64 = 2*DefaultMaxGeometryExpandedPoints + DefaultMaxGeometryExpandedContours
	DefaultMaxOutlineSetCommandsPerGlyph uint64 = 262_144
	DefaultMaxOutlineSetPointsPerGlyph   uint64 = 262_144
	DefaultMaxOutlineSetExactBits        uint32 = DefaultMaxGeometryExactBits + 16
	DefaultMaxOutlineSetExactOps         uint64 = 1 << 35
	DefaultMaxOutlineSetExactLimbUnits   uint64 = 1 << 38
	DefaultMaxOutlineSetCanonicalBytes   uint64 = 1 << 30
	DefaultMaxOutlineSetWorkUnits        uint64 = 1 << 46
	DefaultMaxOutlineSetWorkingBytes     uint64 = 1 << 30
	DefaultMaxOutlineSetRetainedBytes    uint64 = (1 << 31) - 1

	HardMaxOutlineSetExpandedPoints   uint64 = HardMaxGeometryExpandedPoints
	HardMaxOutlineSetExpandedContours uint64 = HardMaxGeometryExpandedContours
	HardMaxOutlineSetExpandedEdges    uint64 = HardMaxGeometryExpandedEdges
	HardMaxOutlineSetExpandedGlyphs   uint64 = HardMaxGeometryExpandedGlyphs
	HardMaxOutlineSetTransformVisits  uint64 = HardMaxGeometryTransformVisits
	HardMaxOutlineSetImpliedPoints    uint64 = 1 << 34
	HardMaxOutlineSetDrawingCommands  uint64 = 1 << 34
	HardMaxOutlineSetCommands         uint64 = 1 << 36
	HardMaxOutlineSetCommandPoints    uint64 = 1 << 36
	HardMaxOutlineSetCommandsPerGlyph uint64 = 1 << 32
	HardMaxOutlineSetPointsPerGlyph   uint64 = 1 << 32
	HardMaxOutlineSetExactBits        uint32 = HardMaxGeometryExactBits + 16
	HardMaxOutlineSetExactOps         uint64 = HardMaxGeometryExactOps
	HardMaxOutlineSetExactLimbUnits   uint64 = HardMaxGeometryExactLimbUnits
	HardMaxOutlineSetCanonicalBytes   uint64 = HardMaxGeometryCanonicalBytes
	HardMaxOutlineSetWorkUnits        uint64 = HardMaxGeometryWorkUnits
	HardMaxOutlineSetWorkingBytes     uint64 = HardMaxGeometryWorkingBytes
	HardMaxOutlineSetRetainedBytes    uint64 = (1 << 31) - 1
)

type OutlineSetLimits struct {
	MaxExpandedPointInstances          uint64 `json:"max_expanded_point_instances"`
	MaxExpandedContourInstances        uint64 `json:"max_expanded_contour_instances"`
	MaxExpandedEdgeVisits              uint64 `json:"max_expanded_edge_visits"`
	MaxExpandedGlyphInstances          uint64 `json:"max_expanded_glyph_instances"`
	MaxTransformPointVisits            uint64 `json:"max_transform_point_visits"`
	MaxImpliedPointInstances           uint64 `json:"max_implied_point_instances"`
	MaxDrawingCommandInstances         uint64 `json:"max_drawing_command_instances"`
	MaxCommandInstances                uint64 `json:"max_command_instances"`
	MaxCommandPointOccurrences         uint64 `json:"max_command_point_occurrences"`
	MaxCommandsPerGlyph                uint64 `json:"max_commands_per_glyph"`
	MaxCommandPointOccurrencesPerGlyph uint64 `json:"max_command_point_occurrences_per_glyph"`
	MaxExactBits                       uint32 `json:"max_exact_bits"`
	MaxExactOps                        uint64 `json:"max_exact_ops"`
	MaxExactLimbUnits                  uint64 `json:"max_exact_limb_units"`
	MaxCanonicalBinaryBytes            uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits                       uint64 `json:"max_work_units"`
	MaxWorkingBytes                    uint64 `json:"max_working_bytes"`
	MaxRetainedBytes                   uint64 `json:"max_retained_bytes"`
}

func DefaultOutlineSetLimits() OutlineSetLimits {
	return OutlineSetLimits{
		MaxExpandedPointInstances:          DefaultMaxOutlineSetExpandedPoints,
		MaxExpandedContourInstances:        DefaultMaxOutlineSetExpandedContours,
		MaxExpandedEdgeVisits:              DefaultMaxOutlineSetExpandedEdges,
		MaxExpandedGlyphInstances:          DefaultMaxOutlineSetExpandedGlyphs,
		MaxTransformPointVisits:            DefaultMaxOutlineSetTransformVisits,
		MaxImpliedPointInstances:           DefaultMaxOutlineSetImpliedPoints,
		MaxDrawingCommandInstances:         DefaultMaxOutlineSetDrawingCommands,
		MaxCommandInstances:                DefaultMaxOutlineSetCommands,
		MaxCommandPointOccurrences:         DefaultMaxOutlineSetCommandPoints,
		MaxCommandsPerGlyph:                DefaultMaxOutlineSetCommandsPerGlyph,
		MaxCommandPointOccurrencesPerGlyph: DefaultMaxOutlineSetPointsPerGlyph,
		MaxExactBits:                       DefaultMaxOutlineSetExactBits,
		MaxExactOps:                        DefaultMaxOutlineSetExactOps,
		MaxExactLimbUnits:                  DefaultMaxOutlineSetExactLimbUnits,
		MaxCanonicalBinaryBytes:            DefaultMaxOutlineSetCanonicalBytes,
		MaxWorkUnits:                       DefaultMaxOutlineSetWorkUnits,
		MaxWorkingBytes:                    DefaultMaxOutlineSetWorkingBytes,
		MaxRetainedBytes:                   DefaultMaxOutlineSetRetainedBytes,
	}
}

type OutlineSetProfileDescriptor struct {
	SchemaVersion            int              `json:"schema_version"`
	Profile                  string           `json:"profile"`
	OutlineProfile           string           `json:"outline_profile"`
	ReceiptProfile           string           `json:"receipt_profile"`
	SourceProfile            string           `json:"source_profile"`
	DescriptorHashProfile    string           `json:"descriptor_hash_profile"`
	OutlineSetHashProfile    string           `json:"outline_set_hash_profile"`
	ContourSemantics         string           `json:"contour_semantics"`
	ImpliedPointSemantics    string           `json:"implied_point_semantics"`
	CommandSemantics         string           `json:"command_semantics"`
	RoundingSemantics        string           `json:"rounding_semantics"`
	SourceBindingSemantics   string           `json:"source_binding_semantics"`
	DescriptorHashSemantics  string           `json:"descriptor_hash_semantics"`
	HashSemantics            string           `json:"hash_semantics"`
	OutlineSetHashPreimage   string           `json:"outline_set_hash_preimage"`
	ResourceSemantics        string           `json:"resource_semantics"`
	OwnershipSemantics       string           `json:"ownership_semantics"`
	CancellationSemantics    string           `json:"cancellation_semantics"`
	FailureSemantics         string           `json:"failure_semantics"`
	CapabilitySemantics      string           `json:"capability_semantics"`
	ExcludedResponsibilities string           `json:"excluded_responsibilities"`
	DefaultLimits            OutlineSetLimits `json:"default_limits"`
}

var outlineSetDescriptorV1 = OutlineSetProfileDescriptor{
	SchemaVersion: OutlineSetProfileSchemaVersion, Profile: OutlineSetProfileV1,
	OutlineProfile: OutlineSetOutlineProfileV1, ReceiptProfile: OutlineSetReceiptProfileV1,
	SourceProfile: GeometryReceiptProfileV1, DescriptorHashProfile: OutlineSetDescriptorHashProfileV1,
	OutlineSetHashProfile:    OutlineSetHashProfileV1,
	ContourSemantics:         "For each nonempty TrueType contour, if the first raw point is on-curve it is the start; otherwise if the last is on-curve it is the start; otherwise their exact midpoint is the start. Traversal preserves raw point and contour order. Each contour emits exactly one Move first and one Close last, resets pending state, and never emits a redundant Line back to the start. Empty glyphs emit no commands.",
	ImpliedPointSemantics:    "Every circular adjacent off-curve pair, including the self-pair of a one-point off-curve contour, creates one logical exact on-curve midpoint. Midpoints are computed from already fully transformed and attached real controls as exact (a+b)/2. They never participate in compound point attachment, and numerically equal points remain distinct logical points.",
	CommandSemantics:         "An on-curve point with no pending control emits Line; an on-curve point with a pending off-curve control emits Quad and clears it; consecutive off-curve points emit Quad to their exact midpoint and retain the latter control; a final pending control emits Quad to the start. Move and Line carry one point, Quad carries control then endpoint, and Close carries none. Degenerate, duplicate, zero-length and zero-area commands are retained. Op tags are Move=1, Line=2, Quad=3, Close=4.",
	RoundingSemantics:        "Every real or implied logical point is rounded exactly once from normalized exact dyadic font-space coordinates to signed Q26.6 using symmetric nearest-even. Midpoints are formed before rounding; already-rounded endpoints are never averaged. Exact midpoint addition gates aligned temporary numerators before shifting. Because accepted source coordinates are within signed-int16 bbox range but may have different denominator shifts, this stage permits the source exact-bit ceiling plus 16 bits (528 default, 4112 hard) for the proved worst alignment growth. No float, big.Rat, clamp, wrap, saturation, post-round simplification or host rounding is allowed. Q26.6 remains y-up font space.",
	SourceBindingSemantics:   "The immutable GeometryFont receipt and compact glyph metadata are checked against profile constants and receipt-only resource formulas. The retained OutlineFont graph is recounted and rehashed, every glyph is expanded exactly once, bbox/metric declarations and compact metadata are recomputed, and the complete source geometry hash is replayed before publication.",
	DescriptorHashSemantics:  "The descriptor hash is SHA-256 over u64be(len(descriptor_hash_profile)) followed by its exact UTF-8 bytes, then u64be(len(canonical_descriptor_json)) followed by the exact minified encoding/json bytes of this ordered map-free descriptor. It is lowercase hex with the sha256: prefix.",
	HashSemantics:            "The outline-set hash is streaming SHA-256 over the declared preimage. str is u64be UTF-8 byte length plus exact bytes; u8, u32 and u64 are fixed-width big-endian; i64 is its two's-complement uint64 bit pattern. Arrays remain in glyph, contour and command order. No JSON, map, native integer or float enters the preimage.",
	OutlineSetHashPreimage:   "str(hash_profile),u32(schema),str(profile),str(outline_profile),str(descriptor_hash),str(source_profile),str(source_descriptor_hash_profile),str(source_descriptor_hash),str(source_geometry_hash_profile),str(source_geometry_hash),u64(source_geometry_canonical_bytes),str(source_graph_hash_profile),str(source_graph_hash),u64(source_graph_canonical_bytes),str(upstream_content_hash_profile),str(upstream_content_hash),u32(glyph_count); per gid: u32(gid),u64(contours),u64(commands),then per command u8(op),u8(point_count),then each point i64(x),i64(y); footer u64(expanded_points,expanded_contours,implied_points,line_commands,quad_commands,commands,command_point_occurrences).",
	ResourceSemantics:        "Let R0,R1,G,P,C,E,I,T,J,S,K,H,O,L,B be source graph bytes, source geometry bytes, glyphs, expanded real points, contours, edges, recursive glyph instances, transform point visits, implied points, drawing commands, all commands, command point occurrences, combined source/materialization exact operations, logical limb units and output canonical bytes. work=R0+R1+64G+16P+8C+32E+16I+12T+16J+12S+8K+2H+8O+L+B. A shared incremental meter reserves admitted lower bounds and charges every exact operation, logical limb and source/output geometry canonical byte before execution. retained=source_retained+32768+32G+40K+owned_receipt_string_bytes. working=98304+64G+3*max_root_points*(32+2*(64+ceil(exact_bits_limit/8)))+8*max_root_contours+128*(max_depth+1). The default and hard retained ceilings are both 2^31-1 bytes; retained, flat-command and glyph-range logical allocation charges are gated to that portable address ceiling before make on every target. Counts and arithmetic are checked before proportional allocation; values are portable logical charges, not RSS.",
	OwnershipSemantics:       "The call borrows GeometryFont and retained source data. Success owns a flat exact-size command array and per-glyph scalar ranges, retains the immutable source for provenance, exposes only indexed value views, creates no mutable cache, and supports concurrent reads.",
	CancellationSemantics:    "A non-nil context is checked before work, through source graph and geometry revalidation, flag topology, exact expansion, every contour, rounding, hashing and immediately before publication. Cancellation publishes nil and a zero receipt.",
	FailureSemantics:         "Invalid source bindings, topology or arity drift, coordinate/exact/resource overflow, cancellation, allocation failure and invariant failure return nil OutlineSet and a strictly zero receipt.",
	CapabilitySemantics:      "This stage materializes all-glyph font-space outlines only. It does not validate layout tables or shaping and does not advertise cutly.text.pinned-font-viewport-px.v2 or any Text capability.",
	ExcludedResponsibilities: "GDEF/GSUB/GPOS validation or execution, cmap-to-run shaping, bidi, fallback, go-text/HarfBuzz, textlayout assembly, scaling to viewport pixels, flattening, raster, paint, compositing, encoding, persistence and publication are outside this profile.",
	DefaultLimits:            DefaultOutlineSetLimits(),
}

func OutlineSetDescriptor() OutlineSetProfileDescriptor { return outlineSetDescriptorV1 }

func OutlineSetDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(outlineSetDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var outlineSetDescriptorHashV1 = computeOutlineSetDescriptorHash()

func OutlineSetDescriptorHash() string { return outlineSetDescriptorHashV1 }

func computeOutlineSetDescriptorHash() string {
	canonical := OutlineSetDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(OutlineSetDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(OutlineSetDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func normalizeOutlineSetLimits(value OutlineSetLimits) (OutlineSetLimits, error) {
	defaults := DefaultOutlineSetLimits()
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
	if value.MaxImpliedPointInstances == 0 {
		value.MaxImpliedPointInstances = defaults.MaxImpliedPointInstances
	}
	if value.MaxDrawingCommandInstances == 0 {
		value.MaxDrawingCommandInstances = defaults.MaxDrawingCommandInstances
	}
	if value.MaxCommandInstances == 0 {
		value.MaxCommandInstances = defaults.MaxCommandInstances
	}
	if value.MaxCommandPointOccurrences == 0 {
		value.MaxCommandPointOccurrences = defaults.MaxCommandPointOccurrences
	}
	if value.MaxCommandsPerGlyph == 0 {
		value.MaxCommandsPerGlyph = defaults.MaxCommandsPerGlyph
	}
	if value.MaxCommandPointOccurrencesPerGlyph == 0 {
		value.MaxCommandPointOccurrencesPerGlyph = defaults.MaxCommandPointOccurrencesPerGlyph
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
	if value.MaxExpandedPointInstances > HardMaxOutlineSetExpandedPoints || value.MaxExpandedContourInstances > HardMaxOutlineSetExpandedContours ||
		value.MaxExpandedEdgeVisits > HardMaxOutlineSetExpandedEdges || value.MaxExpandedGlyphInstances > HardMaxOutlineSetExpandedGlyphs ||
		value.MaxTransformPointVisits > HardMaxOutlineSetTransformVisits || value.MaxImpliedPointInstances > HardMaxOutlineSetImpliedPoints ||
		value.MaxDrawingCommandInstances > HardMaxOutlineSetDrawingCommands || value.MaxCommandInstances > HardMaxOutlineSetCommands ||
		value.MaxCommandPointOccurrences > HardMaxOutlineSetCommandPoints || value.MaxCommandsPerGlyph > HardMaxOutlineSetCommandsPerGlyph ||
		value.MaxCommandPointOccurrencesPerGlyph > HardMaxOutlineSetPointsPerGlyph || value.MaxExactBits > HardMaxOutlineSetExactBits ||
		value.MaxExactOps > HardMaxOutlineSetExactOps || value.MaxExactLimbUnits > HardMaxOutlineSetExactLimbUnits ||
		value.MaxCanonicalBinaryBytes > HardMaxOutlineSetCanonicalBytes || value.MaxWorkUnits > HardMaxOutlineSetWorkUnits ||
		value.MaxWorkingBytes > HardMaxOutlineSetWorkingBytes || value.MaxRetainedBytes > HardMaxOutlineSetRetainedBytes {
		return OutlineSetLimits{}, fontError(CodeOutlineSetLimitsInvalid, "outline_set/limits", ErrLimitsInvalid)
	}
	return value, nil
}
