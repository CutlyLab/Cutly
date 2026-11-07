package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	OutlineProfileSchemaVersion    = 1
	OutlineProfileV1               = "cutly.font.static-glyf-structure-unhinted.v1"
	OutlineReceiptSchemaVersion    = 1
	OutlineReceiptProfileV1        = "cutly.font.static-glyf-structure-unhinted.receipt.v1"
	OutlineDescriptorHashProfileV1 = "cutly.hash.sha256.font-static-glyf-structure-descriptor-json.v1"
	OutlineGraphHashProfileV1      = "cutly.hash.sha256.font-static-glyf-graph-be.v1"

	OutlineGeometryStatusV1   = "structure_validated_geometry_not_authorized"
	OutlineHintingStatusV1    = "opaque_bounds_validated_unexecuted"
	OutlineLayoutStatusV1     = "gdef_gsub_gpos_opaque_not_shape_authorized"
	OutlineMaxpStatusV1       = "seven_outline_maxima_exact"
	OutlineMetricsStatusV1    = "use_my_metrics_recorded_not_validated"
	OutlineRoundXYStatusV1    = "rejected_by_profile"
	OutlinePointMatchStatusV1 = "real_control_points_only_phantom_rejected"
	OutlineOffsetStatusV1     = "scaled_unscaled_framing_validated_geometry_deferred"
)

const (
	DefaultMaxOutlineRawPoints                uint64 = 8_388_608
	DefaultMaxOutlineRawContours              uint64 = 2_097_152
	DefaultMaxOutlineComponentRecords         uint64 = 1_048_576
	DefaultMaxOutlineComponentsPerGlyph       uint64 = 4_096
	DefaultMaxOutlineInstructionBytes         uint64 = 16 << 20
	DefaultMaxOutlineComponentDepth           uint32 = 32
	DefaultMaxOutlineExpandedPointsPerGlyph   uint64 = 65_535
	DefaultMaxOutlineExpandedContoursPerGlyph uint64 = 65_535
	DefaultMaxOutlineExpandedEdgesPerGlyph    uint64 = 100_000
	DefaultMaxOutlineExpandedPointInstances   uint64 = 16_777_216
	DefaultMaxOutlineExpandedContourInstances uint64 = 4_194_304
	DefaultMaxOutlineExpandedEdgeVisits       uint64 = 16_777_216
	DefaultMaxOutlineCanonicalBinaryBytes     uint64 = 512 << 20
	DefaultMaxOutlineWorkUnits                uint64 = 1 << 40
	DefaultMaxOutlineWorkingBytes             uint64 = 512 << 20
	DefaultMaxOutlineRetainedBytes            uint64 = 512 << 20

	HardMaxOutlineRawPoints                uint64 = 1 << 26
	HardMaxOutlineRawContours              uint64 = 1 << 24
	HardMaxOutlineComponentRecords         uint64 = 1 << 24
	HardMaxOutlineComponentsPerGlyph       uint64 = 65_535
	HardMaxOutlineInstructionBytes         uint64 = 1 << 30
	HardMaxOutlineComponentDepth           uint32 = 64
	HardMaxOutlineExpandedPointsPerGlyph   uint64 = 1 << 24
	HardMaxOutlineExpandedContoursPerGlyph uint64 = 1 << 24
	HardMaxOutlineExpandedEdgesPerGlyph    uint64 = 1 << 28
	HardMaxOutlineExpandedPointInstances   uint64 = 1 << 34
	HardMaxOutlineExpandedContourInstances uint64 = 1 << 34
	HardMaxOutlineExpandedEdgeVisits       uint64 = 1 << 36
	HardMaxOutlineCanonicalBinaryBytes     uint64 = 1 << 30
	HardMaxOutlineWorkUnits                uint64 = 1 << 60
	HardMaxOutlineWorkingBytes             uint64 = 2 << 30
	HardMaxOutlineRetainedBytes            uint64 = 2 << 30
)

type OutlineLimits struct {
	MaxRawPoints                uint64 `json:"max_raw_points"`
	MaxRawContours              uint64 `json:"max_raw_contours"`
	MaxComponentRecords         uint64 `json:"max_component_records"`
	MaxComponentsPerGlyph       uint64 `json:"max_components_per_glyph"`
	MaxInstructionBytes         uint64 `json:"max_instruction_bytes"`
	MaxComponentDepth           uint32 `json:"max_component_depth"`
	MaxExpandedPointsPerGlyph   uint64 `json:"max_expanded_points_per_glyph"`
	MaxExpandedContoursPerGlyph uint64 `json:"max_expanded_contours_per_glyph"`
	MaxExpandedEdgesPerGlyph    uint64 `json:"max_expanded_edges_per_glyph"`
	MaxExpandedPointInstances   uint64 `json:"max_expanded_point_instances"`
	MaxExpandedContourInstances uint64 `json:"max_expanded_contour_instances"`
	MaxExpandedEdgeVisits       uint64 `json:"max_expanded_edge_visits"`
	MaxCanonicalBinaryBytes     uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits                uint64 `json:"max_work_units"`
	MaxWorkingBytes             uint64 `json:"max_working_bytes"`
	MaxRetainedBytes            uint64 `json:"max_retained_bytes"`
}

func DefaultOutlineLimits() OutlineLimits {
	return OutlineLimits{
		MaxRawPoints: DefaultMaxOutlineRawPoints, MaxRawContours: DefaultMaxOutlineRawContours,
		MaxComponentRecords:         DefaultMaxOutlineComponentRecords,
		MaxComponentsPerGlyph:       DefaultMaxOutlineComponentsPerGlyph,
		MaxInstructionBytes:         DefaultMaxOutlineInstructionBytes,
		MaxComponentDepth:           DefaultMaxOutlineComponentDepth,
		MaxExpandedPointsPerGlyph:   DefaultMaxOutlineExpandedPointsPerGlyph,
		MaxExpandedContoursPerGlyph: DefaultMaxOutlineExpandedContoursPerGlyph,
		MaxExpandedEdgesPerGlyph:    DefaultMaxOutlineExpandedEdgesPerGlyph,
		MaxExpandedPointInstances:   DefaultMaxOutlineExpandedPointInstances,
		MaxExpandedContourInstances: DefaultMaxOutlineExpandedContourInstances,
		MaxExpandedEdgeVisits:       DefaultMaxOutlineExpandedEdgeVisits,
		MaxCanonicalBinaryBytes:     DefaultMaxOutlineCanonicalBinaryBytes,
		MaxWorkUnits:                DefaultMaxOutlineWorkUnits, MaxWorkingBytes: DefaultMaxOutlineWorkingBytes,
		MaxRetainedBytes: DefaultMaxOutlineRetainedBytes,
	}
}

type OutlineProfileDescriptor struct {
	SchemaVersion            int           `json:"schema_version"`
	Profile                  string        `json:"profile"`
	ReceiptProfile           string        `json:"receipt_profile"`
	UpstreamProfile          string        `json:"upstream_profile"`
	DescriptorHashProfile    string        `json:"descriptor_hash_profile"`
	GraphHashProfile         string        `json:"graph_hash_profile"`
	GlyphBodySemantics       string        `json:"glyph_body_semantics"`
	SimpleSemantics          string        `json:"simple_semantics"`
	CompoundSemantics        string        `json:"compound_semantics"`
	InstructionSemantics     string        `json:"instruction_semantics"`
	GraphSemantics           string        `json:"graph_semantics"`
	MaxpSemantics            string        `json:"maxp_semantics"`
	DescriptorHashSemantics  string        `json:"descriptor_hash_semantics"`
	HashSemantics            string        `json:"hash_semantics"`
	GraphHashPreimage        string        `json:"graph_hash_preimage"`
	ResourceSemantics        string        `json:"resource_semantics"`
	OwnershipSemantics       string        `json:"ownership_semantics"`
	CancellationSemantics    string        `json:"cancellation_semantics"`
	FailureSemantics         string        `json:"failure_semantics"`
	CapabilitySemantics      string        `json:"capability_semantics"`
	ExcludedResponsibilities string        `json:"excluded_responsibilities"`
	DefaultLimits            OutlineLimits `json:"default_limits"`
}

var outlineDescriptorV1 = OutlineProfileDescriptor{
	SchemaVersion:            OutlineProfileSchemaVersion,
	Profile:                  OutlineProfileV1,
	ReceiptProfile:           OutlineReceiptProfileV1,
	UpstreamProfile:          ReceiptProfileV1,
	DescriptorHashProfile:    OutlineDescriptorHashProfileV1,
	GraphHashProfile:         OutlineGraphHashProfileV1,
	GlyphBodySemantics:       "Scan every loca-delimited glyph in the immutable upstream Font. A zero-length range is empty_range. A nonempty range is at least ten bytes and has numberOfContours >= 0 for simple or exactly -1 for compound; other negative values fail. After the defined body, only zero to three zero padding bytes are accepted. No byte or table view is exported.",
	SimpleSemantics:          "Contour endpoints are strictly increasing. Flags expand to exactly the declared raw control-point count; zero repeats, reserved bit 7, and OVERLAP_SIMPLE outside the first physical flag byte fail. X deltas are decoded completely before Y deltas, accumulated with checked arithmetic, and must remain signed-int16 design coordinates. The declared bbox equals the raw on/off-curve control-point extrema; no-point simple glyphs require a zero bbox. Raw points remain private and no implied points or commands are emitted in this stage.",
	CompoundSemantics:        "The first of one or more components uses signed XY placement. XY byte/word arguments are signed while point arguments are unsigned. Transform flags are mutually exclusive and F2DOT14 words retain disk order xscale,scale01,scale10,yscale. ROUND_XY_TO_GRID and reserved bits fail. SCALED and UNSCALED offsets are mutually exclusive and XY-only; neither defaults to UNSCALED. Point matching is limited to already accumulated and child-expanded real raw control points, excluding implied and phantom points. USE_MY_METRICS is recorded but not authorized. Component geometry and bbox validation are deferred.",
	InstructionSemantics:     "Simple and final-component compound instruction framing, bytes, totals and maxp.maxSizeOfInstructions are validated and content-bound, but bytecode is opaque and never executed. WE_HAVE_INSTRUCTIONS is accepted only on the final component. instruction_glyph_count counts glyphs whose instruction length is nonzero.",
	GraphSemantics:           "An iterative three-color walk rejects every cycle. Child-first dynamic programming counts repeated component references, per-root expanded raw points, contours, edge visits and component depth; simple depth is zero and a compound directly referencing a simple glyph has depth one. First-component point matching, phantom indexes and expansion bombs fail closed before publication.",
	MaxpSemantics:            "Observed maxPoints, maxContours, maxCompositePoints, maxCompositeContours, maxSizeOfInstructions, maxComponentElements and maxComponentDepth must exactly equal the seven maxp declarations. Per-simple point/contour/instruction counts and per-compound direct-component/instruction counts are checked against declarations before proportional allocation where possible. Hint-VM maxima remain declared_unverified. Compound bbox, head bbox and hhea outline-derived extrema remain geometry_deferred.",
	DescriptorHashSemantics:  "The descriptor hash is SHA-256 over u64be(len(descriptor_hash_profile)) followed by its exact UTF-8 bytes, then u64be(len(canonical_descriptor_json)) followed by the exact minified encoding/json bytes of this ordered map-free descriptor. It is lowercase hex with the sha256: prefix.",
	HashSemantics:            "The graph hash is SHA-256 over the exact profile-defined length-prefixed-string and big-endian fixed-width stream in glyph-ID order. It binds the upstream complete-font content hash, normalized private raw points, component records, exact instruction bytes, padding counts and derived graph counts. It uses no JSON, map order, native int, float or exported raw bytes.",
	GraphHashPreimage:        "str is u64 byte length plus exact bytes. The stream is str(graph_hash_profile),u32(profile_schema),str(outline_profile),str(upstream_content_profile),str(upstream_content_hash),u32(glyph_count),u64(glyf_bytes); then for each ascending gid: u32(gid),u8(kind empty=0/simple=1/compound=2),u32(source_bytes),i32(number_of_contours,x_min,y_min,x_max,y_max),u32(instruction_length),raw instruction bytes,u8(padding_bytes). A simple suffix is u64(contour_count), each u32(end_point), u64(point_count), each i32(x,y)+u8(on_curve), u8(overlap). A compound suffix is u64(component_count), then per component u16(flags),u32(child_gid),u8(argument_mode xy=1/point=2),i32(arg1,arg2,xscale,scale01,scale10,yscale), followed by u64(expanded_points,expanded_contours,expanded_edges),u32(component_depth). Empty glyphs have no suffix.",
	ResourceSemantics:        "Let B,G,P,C,K,I,Z,E,XP,XC,CB be glyf bytes, glyphs, raw points, raw contours, component records, instruction bytes, glyph-tail padding, all-root expanded edge visits, point instances, contour instances and canonical hash bytes. work=B+32G+12P+4C+32K+2I+Z+8E+4XP+2XC+CB. retained=upstream_retained_bytes+32768+128G+24P+8C+64K+owned_receipt_string_bytes, where owned_receipt_string_bytes is the exact UTF-8 byte sum of every receipt string. working=65536+32G+16K+16*max_expanded_points+8*max_expanded_contours+8*max_expanded_edges+CB. Every charge is fully recomputable from the receipt. These are portable logical charges, not heap/RSS ceilings.",
	OwnershipSemantics:       "DecodeOutlineFont borrows only the immutable Font pointer for the call and success retains that immutable owner plus private decoded records. Public accessors return scalar values. There is no mutable cache, raw byte view or caller-slice retention; concurrent reads are safe.",
	CancellationSemantics:    "A non-nil context is checked before work and throughout glyph scanning, coordinate decoding, graph traversal, dynamic programming, hashing and immediately before publication. Cancellation returns nil and a zero receipt.",
	FailureSemantics:         "Nil context/font, malformed framing, graph cycles, maxp mismatch, resource overflow, cancellation and invariant drift fail closed with nil OutlineFont and a strictly zero OutlineReceipt. No repaired or partial graph is published.",
	CapabilitySemantics:      "This structural gate keeps geometry unauthorized and does not advertise cutly.text.pinned-font-viewport-px.v2 or any shaping, layout or rendering capability. It does not upgrade the upstream glyf status.",
	ExcludedResponsibilities: "Implied-point synthesis, exact compound transforms, point-alignment translation, compound bbox/head/hhea extrema, outline commands, Q26.6 materialization, GDEF/GSUB/GPOS validation, go-text/HarfBuzz execution, shaping, bidi, layout, raster, paint, fallback and publication are outside this phase.",
	DefaultLimits:            DefaultOutlineLimits(),
}

func OutlineDescriptor() OutlineProfileDescriptor { return outlineDescriptorV1 }

func OutlineDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(outlineDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var outlineDescriptorHashV1 = computeOutlineDescriptorHash()

func OutlineDescriptorHash() string { return outlineDescriptorHashV1 }

func computeOutlineDescriptorHash() string {
	canonical := OutlineDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(OutlineDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(OutlineDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func normalizeOutlineLimits(value OutlineLimits) (OutlineLimits, error) {
	defaults := DefaultOutlineLimits()
	if value.MaxRawPoints == 0 {
		value.MaxRawPoints = defaults.MaxRawPoints
	}
	if value.MaxRawContours == 0 {
		value.MaxRawContours = defaults.MaxRawContours
	}
	if value.MaxComponentRecords == 0 {
		value.MaxComponentRecords = defaults.MaxComponentRecords
	}
	if value.MaxComponentsPerGlyph == 0 {
		value.MaxComponentsPerGlyph = defaults.MaxComponentsPerGlyph
	}
	if value.MaxInstructionBytes == 0 {
		value.MaxInstructionBytes = defaults.MaxInstructionBytes
	}
	if value.MaxComponentDepth == 0 {
		value.MaxComponentDepth = defaults.MaxComponentDepth
	}
	if value.MaxExpandedPointsPerGlyph == 0 {
		value.MaxExpandedPointsPerGlyph = defaults.MaxExpandedPointsPerGlyph
	}
	if value.MaxExpandedContoursPerGlyph == 0 {
		value.MaxExpandedContoursPerGlyph = defaults.MaxExpandedContoursPerGlyph
	}
	if value.MaxExpandedEdgesPerGlyph == 0 {
		value.MaxExpandedEdgesPerGlyph = defaults.MaxExpandedEdgesPerGlyph
	}
	if value.MaxExpandedPointInstances == 0 {
		value.MaxExpandedPointInstances = defaults.MaxExpandedPointInstances
	}
	if value.MaxExpandedContourInstances == 0 {
		value.MaxExpandedContourInstances = defaults.MaxExpandedContourInstances
	}
	if value.MaxExpandedEdgeVisits == 0 {
		value.MaxExpandedEdgeVisits = defaults.MaxExpandedEdgeVisits
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
	if value.MaxRawPoints > HardMaxOutlineRawPoints || value.MaxRawContours > HardMaxOutlineRawContours ||
		value.MaxComponentRecords > HardMaxOutlineComponentRecords || value.MaxComponentsPerGlyph > HardMaxOutlineComponentsPerGlyph ||
		value.MaxInstructionBytes > HardMaxOutlineInstructionBytes || value.MaxComponentDepth > HardMaxOutlineComponentDepth ||
		value.MaxExpandedPointsPerGlyph > HardMaxOutlineExpandedPointsPerGlyph || value.MaxExpandedContoursPerGlyph > HardMaxOutlineExpandedContoursPerGlyph ||
		value.MaxExpandedEdgesPerGlyph > HardMaxOutlineExpandedEdgesPerGlyph || value.MaxExpandedPointInstances > HardMaxOutlineExpandedPointInstances ||
		value.MaxExpandedContourInstances > HardMaxOutlineExpandedContourInstances || value.MaxExpandedEdgeVisits > HardMaxOutlineExpandedEdgeVisits ||
		value.MaxCanonicalBinaryBytes > HardMaxOutlineCanonicalBinaryBytes || value.MaxWorkUnits > HardMaxOutlineWorkUnits ||
		value.MaxWorkingBytes > HardMaxOutlineWorkingBytes || value.MaxRetainedBytes > HardMaxOutlineRetainedBytes {
		return OutlineLimits{}, fontError(CodeOutlineLimitsInvalid, "outline/limits", ErrLimitsInvalid)
	}
	return value, nil
}
