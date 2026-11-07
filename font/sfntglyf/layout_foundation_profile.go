package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	layoutFoundationSchemaVersion         = 1
	layoutFoundationProfileV1             = "cutly.font.opentype-layout-gdef-foundation.v1"
	layoutFoundationReceiptProfileV1      = "cutly.font.opentype-layout-gdef-foundation.receipt.v1"
	layoutFoundationDescriptorHashV1      = "cutly.hash.sha256.font-opentype-layout-gdef-foundation-descriptor-json.v1"
	layoutFoundationCanonicalHashV1       = "cutly.hash.sha256.font-opentype-layout-gdef-foundation-be.v1"
	layoutFoundationLayoutStatusV1        = "gdef_validated_gsub_gpos_opaque_not_shape_authorized"
	layoutFoundationCapabilityStatusV1    = "not_advertised"
	layoutFoundationVariationStatusV1     = "item_variation_store_and_variation_index_rejected"
	layoutFoundationDeviceStatusV1        = "validated_not_applied"
	layoutFoundationCaretOrderStatusV1    = "base_coordinate_verified_or_point_order_preserved"
	layoutFoundationGSUBTableStatusV1     = "opaque_content_bound_validation_deferred"
	layoutFoundationGPOSTableStatusV1     = "opaque_content_bound_validation_deferred"
	layoutFoundationGDEFAbsentStatusV1    = "absent"
	layoutFoundationGDEFValidatedStatusV1 = "strict_static_nonvariable_validated"
)

const (
	defaultMaxLayoutFoundationTableBytes        uint64 = 64 << 20
	defaultMaxLayoutFoundationNodes             uint64 = 1 << 20
	defaultMaxLayoutFoundationEdges             uint64 = 1 << 22
	defaultMaxLayoutFoundationAliases           uint64 = 1 << 22
	defaultMaxLayoutFoundationDepth             uint32 = 64
	defaultMaxLayoutFoundationCoverageTables    uint64 = 1 << 18
	defaultMaxLayoutFoundationCoverageGlyphs    uint64 = 1 << 24
	defaultMaxLayoutFoundationClassDefTables    uint64 = 1 << 18
	defaultMaxLayoutFoundationClassAssignments  uint64 = 1 << 24
	defaultMaxLayoutFoundationDeviceTables      uint64 = 1 << 18
	defaultMaxLayoutFoundationDeviceDeltas      uint64 = 1 << 24
	defaultMaxLayoutFoundationAttachPointTables uint64 = 65_535
	defaultMaxLayoutFoundationAttachmentPoints  uint64 = 1 << 24
	defaultMaxLayoutFoundationLigGlyphTables    uint64 = 65_535
	defaultMaxLayoutFoundationCaretValues       uint64 = 1 << 24
	defaultMaxLayoutFoundationMarkGlyphSets     uint64 = 65_535
	defaultMaxLayoutFoundationCanonicalBytes    uint64 = 512 << 20
	defaultMaxLayoutFoundationWorkUnits         uint64 = 1 << 48
	defaultMaxLayoutFoundationWorkingBytes      uint64 = 1 << 30
	defaultMaxLayoutFoundationRetainedBytes     uint64 = (1 << 31) - 1
	hardMaxLayoutFoundationTableBytes           uint64 = 1 << 30
	hardMaxLayoutFoundationNodes                uint64 = 1 << 24
	hardMaxLayoutFoundationEdges                uint64 = 1 << 26
	hardMaxLayoutFoundationAliases              uint64 = 1 << 26
	hardMaxLayoutFoundationDepth                uint32 = 256
	hardMaxLayoutFoundationCoverageTables       uint64 = 1 << 22
	hardMaxLayoutFoundationCoverageGlyphs       uint64 = 1 << 34
	hardMaxLayoutFoundationClassDefTables       uint64 = 1 << 22
	hardMaxLayoutFoundationClassAssignments     uint64 = 1 << 34
	hardMaxLayoutFoundationDeviceTables         uint64 = 1 << 22
	hardMaxLayoutFoundationDeviceDeltas         uint64 = 1 << 34
	hardMaxLayoutFoundationAttachPointTables    uint64 = 65_535
	hardMaxLayoutFoundationAttachmentPoints     uint64 = 1 << 32
	hardMaxLayoutFoundationLigGlyphTables       uint64 = 65_535
	hardMaxLayoutFoundationCaretValues          uint64 = 1 << 34
	hardMaxLayoutFoundationMarkGlyphSets        uint64 = 65_535
	hardMaxLayoutFoundationCanonicalBytes       uint64 = 1 << 30
	hardMaxLayoutFoundationWorkUnits            uint64 = 1 << 60
	hardMaxLayoutFoundationWorkingBytes         uint64 = (1 << 31) - 1
	hardMaxLayoutFoundationRetainedBytes        uint64 = (1 << 31) - 1
)

// layoutFoundationLimits is intentionally private until the complete
// GDEF/GSUB/GPOS graph contract is published atomically.
type layoutFoundationLimits struct {
	MaxLayoutTableBytes     uint64 `json:"max_layout_table_bytes"`
	MaxNodes                uint64 `json:"max_nodes"`
	MaxEdges                uint64 `json:"max_edges"`
	MaxAliasReferences      uint64 `json:"max_alias_references"`
	MaxStructuralDepth      uint32 `json:"max_structural_depth"`
	MaxCoverageTables       uint64 `json:"max_coverage_tables"`
	MaxCoveredGlyphs        uint64 `json:"max_covered_glyph_instances"`
	MaxClassDefTables       uint64 `json:"max_class_def_tables"`
	MaxClassAssignments     uint64 `json:"max_class_assignments"`
	MaxDeviceTables         uint64 `json:"max_device_tables"`
	MaxDeviceDeltaValues    uint64 `json:"max_device_delta_values"`
	MaxAttachPointTables    uint64 `json:"max_attach_point_tables"`
	MaxAttachmentPoints     uint64 `json:"max_attachment_point_indices"`
	MaxLigGlyphTables       uint64 `json:"max_lig_glyph_tables"`
	MaxCaretValues          uint64 `json:"max_caret_value_references"`
	MaxMarkGlyphSets        uint64 `json:"max_mark_glyph_sets"`
	MaxCanonicalBinaryBytes uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits            uint64 `json:"max_work_units"`
	MaxWorkingBytes         uint64 `json:"max_working_bytes"`
	MaxRetainedBytes        uint64 `json:"max_retained_bytes"`
}

func defaultLayoutFoundationLimits() layoutFoundationLimits {
	return layoutFoundationLimits{
		MaxLayoutTableBytes: defaultMaxLayoutFoundationTableBytes,
		MaxNodes:            defaultMaxLayoutFoundationNodes, MaxEdges: defaultMaxLayoutFoundationEdges,
		MaxAliasReferences: defaultMaxLayoutFoundationAliases, MaxStructuralDepth: defaultMaxLayoutFoundationDepth,
		MaxCoverageTables: defaultMaxLayoutFoundationCoverageTables, MaxCoveredGlyphs: defaultMaxLayoutFoundationCoverageGlyphs,
		MaxClassDefTables: defaultMaxLayoutFoundationClassDefTables, MaxClassAssignments: defaultMaxLayoutFoundationClassAssignments,
		MaxDeviceTables: defaultMaxLayoutFoundationDeviceTables, MaxDeviceDeltaValues: defaultMaxLayoutFoundationDeviceDeltas,
		MaxAttachPointTables: defaultMaxLayoutFoundationAttachPointTables, MaxAttachmentPoints: defaultMaxLayoutFoundationAttachmentPoints,
		MaxLigGlyphTables: defaultMaxLayoutFoundationLigGlyphTables, MaxCaretValues: defaultMaxLayoutFoundationCaretValues,
		MaxMarkGlyphSets:        defaultMaxLayoutFoundationMarkGlyphSets,
		MaxCanonicalBinaryBytes: defaultMaxLayoutFoundationCanonicalBytes,
		MaxWorkUnits:            defaultMaxLayoutFoundationWorkUnits, MaxWorkingBytes: defaultMaxLayoutFoundationWorkingBytes,
		MaxRetainedBytes: defaultMaxLayoutFoundationRetainedBytes,
	}
}

func normalizeLayoutFoundationLimits(value layoutFoundationLimits) (layoutFoundationLimits, error) {
	defaults := defaultLayoutFoundationLimits()
	normalizeLayoutFoundationUint64(&value.MaxLayoutTableBytes, defaults.MaxLayoutTableBytes)
	normalizeLayoutFoundationUint64(&value.MaxNodes, defaults.MaxNodes)
	normalizeLayoutFoundationUint64(&value.MaxEdges, defaults.MaxEdges)
	normalizeLayoutFoundationUint64(&value.MaxAliasReferences, defaults.MaxAliasReferences)
	if value.MaxStructuralDepth == 0 {
		value.MaxStructuralDepth = defaults.MaxStructuralDepth
	}
	normalizeLayoutFoundationUint64(&value.MaxCoverageTables, defaults.MaxCoverageTables)
	normalizeLayoutFoundationUint64(&value.MaxCoveredGlyphs, defaults.MaxCoveredGlyphs)
	normalizeLayoutFoundationUint64(&value.MaxClassDefTables, defaults.MaxClassDefTables)
	normalizeLayoutFoundationUint64(&value.MaxClassAssignments, defaults.MaxClassAssignments)
	normalizeLayoutFoundationUint64(&value.MaxDeviceTables, defaults.MaxDeviceTables)
	normalizeLayoutFoundationUint64(&value.MaxDeviceDeltaValues, defaults.MaxDeviceDeltaValues)
	normalizeLayoutFoundationUint64(&value.MaxAttachPointTables, defaults.MaxAttachPointTables)
	normalizeLayoutFoundationUint64(&value.MaxAttachmentPoints, defaults.MaxAttachmentPoints)
	normalizeLayoutFoundationUint64(&value.MaxLigGlyphTables, defaults.MaxLigGlyphTables)
	normalizeLayoutFoundationUint64(&value.MaxCaretValues, defaults.MaxCaretValues)
	normalizeLayoutFoundationUint64(&value.MaxMarkGlyphSets, defaults.MaxMarkGlyphSets)
	normalizeLayoutFoundationUint64(&value.MaxCanonicalBinaryBytes, defaults.MaxCanonicalBinaryBytes)
	normalizeLayoutFoundationUint64(&value.MaxWorkUnits, defaults.MaxWorkUnits)
	normalizeLayoutFoundationUint64(&value.MaxWorkingBytes, defaults.MaxWorkingBytes)
	normalizeLayoutFoundationUint64(&value.MaxRetainedBytes, defaults.MaxRetainedBytes)
	if value.MaxLayoutTableBytes > hardMaxLayoutFoundationTableBytes || value.MaxNodes > hardMaxLayoutFoundationNodes ||
		value.MaxEdges > hardMaxLayoutFoundationEdges || value.MaxAliasReferences > hardMaxLayoutFoundationAliases ||
		value.MaxStructuralDepth > hardMaxLayoutFoundationDepth || value.MaxCoverageTables > hardMaxLayoutFoundationCoverageTables ||
		value.MaxCoveredGlyphs > hardMaxLayoutFoundationCoverageGlyphs || value.MaxClassDefTables > hardMaxLayoutFoundationClassDefTables ||
		value.MaxClassAssignments > hardMaxLayoutFoundationClassAssignments || value.MaxDeviceTables > hardMaxLayoutFoundationDeviceTables ||
		value.MaxDeviceDeltaValues > hardMaxLayoutFoundationDeviceDeltas || value.MaxAttachPointTables > hardMaxLayoutFoundationAttachPointTables ||
		value.MaxAttachmentPoints > hardMaxLayoutFoundationAttachmentPoints || value.MaxLigGlyphTables > hardMaxLayoutFoundationLigGlyphTables ||
		value.MaxCaretValues > hardMaxLayoutFoundationCaretValues || value.MaxMarkGlyphSets > hardMaxLayoutFoundationMarkGlyphSets ||
		value.MaxCanonicalBinaryBytes > hardMaxLayoutFoundationCanonicalBytes || value.MaxWorkUnits > hardMaxLayoutFoundationWorkUnits ||
		value.MaxWorkingBytes > hardMaxLayoutFoundationWorkingBytes || value.MaxRetainedBytes > hardMaxLayoutFoundationRetainedBytes {
		return layoutFoundationLimits{}, fontError(CodeLimitsInvalid, "layout_foundation/limits", ErrLimitsInvalid)
	}
	return value, nil
}

func normalizeLayoutFoundationUint64(value *uint64, defaultValue uint64) {
	if *value == 0 {
		*value = defaultValue
	}
}

type layoutFoundationProfileDescriptor struct {
	SchemaVersion            int                    `json:"schema_version"`
	Profile                  string                 `json:"profile"`
	ReceiptProfile           string                 `json:"receipt_profile"`
	SourceProfile            string                 `json:"source_profile"`
	DescriptorHashProfile    string                 `json:"descriptor_hash_profile"`
	FoundationHashProfile    string                 `json:"foundation_hash_profile"`
	SourceBindingSemantics   string                 `json:"source_binding_semantics"`
	OffsetGraphSemantics     string                 `json:"offset_graph_semantics"`
	CommonTableSemantics     string                 `json:"common_table_semantics"`
	GDEFSemantics            string                 `json:"gdef_semantics"`
	VariationSemantics       string                 `json:"variation_semantics"`
	CanonicalHashSemantics   string                 `json:"canonical_hash_semantics"`
	ResourceSemantics        string                 `json:"resource_semantics"`
	OwnershipSemantics       string                 `json:"ownership_semantics"`
	CancellationSemantics    string                 `json:"cancellation_semantics"`
	FailureSemantics         string                 `json:"failure_semantics"`
	CapabilitySemantics      string                 `json:"capability_semantics"`
	ExcludedResponsibilities string                 `json:"excluded_responsibilities"`
	DefaultLimits            layoutFoundationLimits `json:"default_limits"`
}

var layoutFoundationDescriptorV1 = layoutFoundationProfileDescriptor{
	SchemaVersion: layoutFoundationSchemaVersion, Profile: layoutFoundationProfileV1,
	ReceiptProfile: layoutFoundationReceiptProfileV1, SourceProfile: OutlineSetReceiptProfileV1,
	DescriptorHashProfile: layoutFoundationDescriptorHashV1, FoundationHashProfile: layoutFoundationCanonicalHashV1,
	SourceBindingSemantics:   "The private immutable OutlineSet to GeometryFont to OutlineFont to Font lineage is admitted from exact receipt formulas and then live-rebound. The owned complete sfnt bytes, directory records, table hashes, outline graph, exact geometry and regenerated outline commands are revalidated without exposing a raw-byte view. GDEF, GSUB and GPOS records and raw hashes are bound in fixed tag order.",
	OffsetGraphSemantics:     "Every non-null Offset16 or Offset32 is checked in uint64 and converted to an absolute table-relative offset before int conversion. Node identity is table tag, absolute offset, semantic node kind and only byte-layout-changing decode parameters. Exact same-identity aliases are shared; type punning, direct-span overlap, children landing in direct bytes, cycles and excessive structural depth fail closed. Decoding and cycle checks use explicit queues or stacks.",
	CommonTableSemantics:     "Coverage format 1 glyphs are strictly increasing; format 2 ranges are ordered, nonoverlapping and have exact contiguous coverage indexes. ClassDef formats are ordered and bounded with implicit class zero; GDEF glyph classes are at most four and mark-attachment classes at most 255. Device formats one, two and three use signed MSB-first packed deltas, checked word counts and zero unused low bits; they are validated but not applied.",
	GDEFSemantics:            "GDEF versions 1.0, 1.2 and static 1.3 are accepted. GlyphClassDef, AttachList, LigCaretList, MarkAttachClassDef and MarkGlyphSets format 1 are fully decoded. Coverage cardinalities bind child arrays. Attachment and caret format 2 indexes target expanded real contour points only, excluding implied and phantom points. Caret formats one through three and shared Device tables are accepted. Numeric base-coordinate order is checked only when every caret is format one or three; point-based order is preserved without a false axis or hinting claim.",
	VariationSemantics:       "GDEF 1.3 requires a null ItemVariationStore offset. Every VariationIndex and unknown Device format fails closed. GSUB and GPOS remain opaque content-bound data in this phase.",
	CanonicalHashSemantics:   "The foundation hash is a streaming SHA-256 over length-prefixed UTF-8 strings and fixed-width big-endian scalars. Tables use fixed GDEF, GSUB, GPOS order. Unique nodes and edges are sorted by semantic keys independent of map iteration and discovery IDs; raw offsets, direct spans, payloads and alias topology are bound. No pointer, map, native integer, float or JSON enters the graph preimage.",
	ResourceSemantics:        "All counts and arithmetic are checked before proportional allocation. Let R be core plus OutlineSet live-rebind work; N,E,A,D,CG,CA,DW,AG,PI,LG,CV,MS and CB be unique nodes, edges, alias references, unique direct bytes, covered glyph instances, ClassDef records, Device words, attach-glyph references, attachment points, ligature-glyph references, caret references, mark-set references and canonical bytes. work=R+64N+24E+8A+D+4CG+8CA+4DW+8AG+4PI+8LG+8CV+8MS+CB. Coverage format 2 and ClassDef format 2 retain compact owned ranges; Device deltas remain decoded int16 values. Let OM be the frozen portable owned-model charge: 32768+128N+48E+8G+8*coverage-format1-glyphs+24*coverage-ranges+8*class-format1-values+24*class-ranges+4*device-words+2*device-deltas+32AG+4PI+32LG+32CV+8MS. retained=source_retained+OM+receipt_string_bytes. working=max(core_working,outline_set_working,65536+OM+256N+192E+16A+16G), covering the live owned model plus decoder graph or canonical projection, normalization and alias-target scratch. Exact final work, retained and canonical size are incrementally gated before proportional allocation.",
	OwnershipSemantics:       "The builder borrows the immutable OutlineSet for the call. A private successful foundation retains that owner plus scalar bindings, typed normalized records and hashes; operation-local table views never escape. There is no raw-byte accessor, mutable cache or public foundation API.",
	CancellationSemantics:    "A non-nil context is checked before work, throughout source replay, bounded decoding, graph traversal and streaming hashing, before proportional allocation and immediately before private publication. Cancellation returns no foundation and a strictly zero receipt.",
	FailureSemantics:         "Malformed input, lineage drift, unsupported variation data, offset or span ambiguity, cycles, resource overflow, cancellation and invariant drift fail closed. No repaired or partial foundation is returned.",
	CapabilitySemantics:      "This is an internal GDEF foundation only. It exports no LayoutGraph, descriptor, receipt, shaping profile or capability and leaves GSUB and GPOS unauthorized. The complete public graph contract is deferred until both tables and their cross-table constraints are validated.",
	ExcludedResponsibilities: "GSUB and GPOS semantic decoding, lookup action execution, FeatureVariations, ItemVariationStore, shaping, bidi, fallback, viewport scaling, rasterization, paint, encoding, persistence and publication are outside this phase.",
	DefaultLimits:            defaultLayoutFoundationLimits(),
}

func layoutFoundationDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(layoutFoundationDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var layoutFoundationDescriptorIdentityV1 = computeLayoutFoundationDescriptorHash()

func computeLayoutFoundationDescriptorHash() string {
	canonical := layoutFoundationDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(layoutFoundationDescriptorHashV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(layoutFoundationDescriptorHashV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
