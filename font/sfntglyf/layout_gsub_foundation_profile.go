package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	gsubFoundationSchemaVersion         = 1
	gsubFoundationProfileV1             = "cutly.font.opentype-layout-gsub-foundation.v1"
	gsubFoundationReceiptProfileV1      = "cutly.font.opentype-layout-gsub-foundation.receipt.v1"
	gsubFoundationDescriptorHashV1      = "cutly.hash.sha256.font-opentype-layout-gsub-foundation-descriptor-json.v1"
	gsubFoundationCanonicalHashV1       = "cutly.hash.sha256.font-opentype-layout-gsub-foundation-be.v1"
	gsubFoundationCapabilityStatusV1    = "private_validation_only_not_advertised"
	gsubFoundationGDEFStatusV1          = "parent_foundation_revalidated_same_live_rebind"
	gsubFoundationGSUBValidatedStatusV1 = "strict_static_lookups_1_through_8_validated"
	gsubFoundationGSUBAbsentStatusV1    = "absent"
	gsubFoundationGPOSStatusV1          = "opaque_content_bound_validation_deferred"
	gsubFoundationVariationStatusV1     = "feature_variations_rejected"
	gsubFoundationLogicalGraphStatusV1  = "deterministic_acyclic_bounded_expansion"
	gsubFoundationFixedRetainedBytes    = uint64(4_096)
	gsubFoundationFixedWorkingBytes     = uint64(65_536)
	defaultGSUBFoundationCanonicalBytes = uint64(512 << 20)
	hardGSUBFoundationCanonicalBytes    = uint64(1 << 30)
)

// gsubFoundationLimits remains private with the foundation itself. The nested
// GDEF c-a limits preserve its frozen contract; the additive fields gate all
// GSUB-specific semantic and logical-graph work.
type gsubFoundationLimits struct {
	LayoutFoundation        layoutFoundationLimits `json:"layout_foundation"`
	MaxOrderedRecords       uint64                 `json:"max_ordered_records"`
	MaxSubstituteGlyphs     uint64                 `json:"max_substitute_glyphs"`
	MaxLigatureComponents   uint64                 `json:"max_ligature_components"`
	MaxFeatureCharacters    uint64                 `json:"max_feature_characters"`
	MaxContextGlyphs        uint64                 `json:"max_context_glyphs"`
	MaxSequenceLookups      uint64                 `json:"max_sequence_lookup_records"`
	MaxLogicalLookupEdges   uint64                 `json:"max_logical_lookup_edges"`
	MaxLogicalLookupDepth   uint32                 `json:"max_logical_lookup_depth"`
	MaxLogicalExpansion     uint64                 `json:"max_logical_expansion"`
	MaxCanonicalBinaryBytes uint64                 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits            uint64                 `json:"max_work_units"`
	MaxWorkingBytes         uint64                 `json:"max_working_bytes"`
	MaxRetainedBytes        uint64                 `json:"max_retained_bytes"`
}

func defaultGSUBFoundationLimits() gsubFoundationLimits {
	return gsubFoundationLimits{
		LayoutFoundation:        defaultLayoutFoundationLimits(),
		MaxOrderedRecords:       defaultLayoutMaxOrderedRecords,
		MaxSubstituteGlyphs:     defaultLayoutMaxSubstituteGlyphs,
		MaxLigatureComponents:   defaultLayoutMaxLigComponents,
		MaxFeatureCharacters:    defaultLayoutMaxFeatureChars,
		MaxContextGlyphs:        defaultLayoutMaxContextGlyphs,
		MaxSequenceLookups:      defaultLayoutMaxSequenceLookups,
		MaxLogicalLookupEdges:   defaultLayoutMaxLogicalEdges,
		MaxLogicalLookupDepth:   defaultMaxLayoutFoundationDepth,
		MaxLogicalExpansion:     defaultLayoutMaxLogicalExpansion,
		MaxCanonicalBinaryBytes: defaultGSUBFoundationCanonicalBytes,
		MaxWorkUnits:            defaultMaxLayoutFoundationWorkUnits,
		MaxWorkingBytes:         defaultMaxLayoutFoundationWorkingBytes,
		MaxRetainedBytes:        defaultMaxLayoutFoundationRetainedBytes,
	}
}

func normalizeGSUBFoundationLimits(value gsubFoundationLimits) (gsubFoundationLimits, error) {
	defaults := defaultGSUBFoundationLimits()
	var err error
	value.LayoutFoundation, err = normalizeLayoutFoundationLimits(value.LayoutFoundation)
	if err != nil {
		return gsubFoundationLimits{}, err
	}
	normalizeLayoutFoundationUint64(&value.MaxOrderedRecords, defaults.MaxOrderedRecords)
	normalizeLayoutFoundationUint64(&value.MaxSubstituteGlyphs, defaults.MaxSubstituteGlyphs)
	normalizeLayoutFoundationUint64(&value.MaxLigatureComponents, defaults.MaxLigatureComponents)
	normalizeLayoutFoundationUint64(&value.MaxFeatureCharacters, defaults.MaxFeatureCharacters)
	normalizeLayoutFoundationUint64(&value.MaxContextGlyphs, defaults.MaxContextGlyphs)
	normalizeLayoutFoundationUint64(&value.MaxSequenceLookups, defaults.MaxSequenceLookups)
	normalizeLayoutFoundationUint64(&value.MaxLogicalLookupEdges, defaults.MaxLogicalLookupEdges)
	if value.MaxLogicalLookupDepth == 0 {
		value.MaxLogicalLookupDepth = defaults.MaxLogicalLookupDepth
	}
	normalizeLayoutFoundationUint64(&value.MaxLogicalExpansion, defaults.MaxLogicalExpansion)
	normalizeLayoutFoundationUint64(&value.MaxCanonicalBinaryBytes, defaults.MaxCanonicalBinaryBytes)
	normalizeLayoutFoundationUint64(&value.MaxWorkUnits, defaults.MaxWorkUnits)
	normalizeLayoutFoundationUint64(&value.MaxWorkingBytes, defaults.MaxWorkingBytes)
	normalizeLayoutFoundationUint64(&value.MaxRetainedBytes, defaults.MaxRetainedBytes)
	if value.MaxOrderedRecords > hardLayoutMaxOrderedRecords ||
		value.MaxSubstituteGlyphs > hardLayoutMaxSubstituteGlyphs ||
		value.MaxLigatureComponents > hardLayoutMaxLigComponents ||
		value.MaxFeatureCharacters > hardLayoutMaxFeatureChars ||
		value.MaxContextGlyphs > hardLayoutMaxContextGlyphs ||
		value.MaxSequenceLookups > hardLayoutMaxSequenceLookups ||
		value.MaxLogicalLookupEdges > hardLayoutMaxLogicalEdges ||
		value.MaxLogicalLookupDepth > hardMaxLayoutFoundationDepth ||
		value.MaxLogicalExpansion > hardLayoutMaxLogicalExpansion ||
		value.MaxCanonicalBinaryBytes > hardGSUBFoundationCanonicalBytes ||
		value.MaxWorkUnits > hardMaxLayoutFoundationWorkUnits ||
		value.MaxWorkingBytes > hardMaxLayoutFoundationWorkingBytes ||
		value.MaxRetainedBytes > hardMaxLayoutFoundationRetainedBytes {
		return gsubFoundationLimits{}, fontError(codeLayoutLimits, "layout/gsub_foundation/limits", ErrLimitsInvalid)
	}
	return value, nil
}

type gsubFoundationProfileDescriptor struct {
	SchemaVersion            int                  `json:"schema_version"`
	Profile                  string               `json:"profile"`
	ReceiptProfile           string               `json:"receipt_profile"`
	ParentProfile            string               `json:"parent_profile"`
	DescriptorHashProfile    string               `json:"descriptor_hash_profile"`
	FoundationHashProfile    string               `json:"foundation_hash_profile"`
	SourceBindingSemantics   string               `json:"source_binding_semantics"`
	LookupSemantics          string               `json:"lookup_semantics"`
	LogicalGraphSemantics    string               `json:"logical_graph_semantics"`
	CanonicalHashSemantics   string               `json:"canonical_hash_semantics"`
	ResourceSemantics        string               `json:"resource_semantics"`
	OwnershipSemantics       string               `json:"ownership_semantics"`
	CancellationSemantics    string               `json:"cancellation_semantics"`
	FailureSemantics         string               `json:"failure_semantics"`
	CapabilitySemantics      string               `json:"capability_semantics"`
	ExcludedResponsibilities string               `json:"excluded_responsibilities"`
	DefaultLimits            gsubFoundationLimits `json:"default_limits"`
}

var gsubFoundationDescriptorV1 = gsubFoundationProfileDescriptor{
	SchemaVersion: gsubFoundationSchemaVersion,
	Profile:       gsubFoundationProfileV1, ReceiptProfile: gsubFoundationReceiptProfileV1,
	ParentProfile:            layoutFoundationReceiptProfileV1,
	DescriptorHashProfile:    gsubFoundationDescriptorHashV1,
	FoundationHashProfile:    gsubFoundationCanonicalHashV1,
	SourceBindingSemantics:   "The private OutlineSet to GeometryFont to OutlineFont to Font lineage is first accepted by the frozen GDEF c-a foundation and then live-rebound again. The complete sfnt, outline graph, exact geometry, materialized commands, GDEF bytes and GDEF model are revalidated in the same rebind used for GSUB. Fixed GDEF, GSUB and GPOS table identities and hashes must equal the parent foundation bindings.",
	LookupSemantics:          "GSUB 1.0 and static 1.1 with null FeatureVariations are accepted. Script, LangSys, Feature, lookup flags and GDEF mark class/set dependencies are validated. Lookup types one through eight are fully decoded, including all contextual and chained-contextual formats, extension Offset32 binding with one underlying type per lookup, and reverse chaining. Every glyph, coverage cardinality, class domain, lookup index, sequence index, nested offset, exact alias and direct-span ownership is checked.",
	LogicalGraphSemantics:    "Every SequenceLookupRecord becomes a deterministic multiplicity-preserving logical edge. Edges are canonically ordered by source lookup, subtable and record ordinal. A deterministic minimum-index topological order is required; self and multi-lookup cycles fail closed. Reverse-topological dynamic programming gates maximum dependency depth and additive invocation expansion before publication.",
	CanonicalHashSemantics:   "The foundation hash streams fixed-width big-endian scalars and length-prefixed UTF-8. It binds the parent GDEF foundation hash, complete source lineage, fixed table identities, GSUB version, sorted physical nodes with their direct raw spans, sorted physical edges, the logical DAG and all decoder counts. No pointer, map, native integer, float or JSON enters the graph preimage.",
	ResourceSemantics:        "Every encoded count and checked offset is gated before proportional allocation. Decoder work, working and retained charges cover compact coverage/ClassDef storage, contextual arrays, substitution arrays, physical graph evidence and logical edges. Final work is parent_work+decoder_work+canonical_bytes; retained is parent_retained+decoder_retained+4096+receipt_string_bytes; working is max(parent_working,65536+decoder_working). These are deterministic portable logical charges, not RSS limits.",
	OwnershipSemantics:       "A success retains the immutable parent foundation, owned normalized GSUB records, logical DAG, scalar table bindings and receipt. Operation-local table views and canonical payload slices never escape. Caller mutation after success cannot change the retained model or receipt.",
	CancellationSemantics:    "A non-nil context is checked before work, through both live rebinds, GDEF and GSUB decoding, sorting, DAG traversal, hashing and immediately before private publication. Cancellation returns a nil foundation and strictly zero receipt.",
	FailureSemantics:         "Malformed data, lineage or table-binding drift, variation data, offset ambiguity, type punning, overlap, physical or logical cycles, expansion overflow, resource exhaustion, cancellation and internal drift fail closed with no partial output.",
	CapabilitySemantics:      "This profile, descriptor, receipt and model remain private. Validation does not expose LayoutGraph, shaping, a capability bit or any raw-byte accessor.",
	ExcludedResponsibilities: "GPOS semantic decoding, FeatureVariations, ItemVariationStore, lookup execution, glyph-buffer mutation, bidi, fallback, rasterization, paint, encoding, persistence and publication remain outside this phase.",
	DefaultLimits:            defaultGSUBFoundationLimits(),
}

func gsubFoundationDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(gsubFoundationDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var gsubFoundationDescriptorIdentityV1 = computeGSUBFoundationDescriptorHash()

func computeGSUBFoundationDescriptorHash() string {
	canonical := gsubFoundationDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(gsubFoundationDescriptorHashV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(gsubFoundationDescriptorHashV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
