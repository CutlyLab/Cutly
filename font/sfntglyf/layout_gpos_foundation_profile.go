package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	gposFoundationSchemaVersion         = 1
	gposFoundationProfileV1             = "cutly.font.opentype-layout-gpos-foundation.v1"
	gposFoundationReceiptProfileV1      = "cutly.font.opentype-layout-gpos-foundation.receipt.v1"
	gposFoundationDescriptorHashV1      = "cutly.hash.sha256.font-opentype-layout-gpos-foundation-descriptor-json.v1"
	gposFoundationCanonicalHashV1       = "cutly.hash.sha256.font-opentype-layout-gpos-foundation-be.v1"
	gposFoundationCapabilityStatusV1    = "private_validation_only_not_advertised"
	gposFoundationGDEFStatusV1          = "parent_foundation_revalidated_same_live_rebind"
	gposFoundationGSUBStatusV1          = "parent_foundation_redecoded_and_canonical_hash_matched_same_live_rebind"
	gposFoundationGPOSValidatedStatusV1 = "strict_static_lookups_1_through_9_validated"
	gposFoundationGPOSAbsentStatusV1    = "absent"
	gposFoundationVariationStatusV1     = "feature_variations_and_variation_index_rejected"
	gposFoundationLogicalGraphStatusV1  = "deterministic_acyclic_bounded_expansion"
	gposFoundationFixedRetainedBytes    = uint64(4_096)
	gposFoundationFixedWorkingBytes     = uint64(65_536)
	defaultGPOSFoundationCanonicalBytes = uint64(512 << 20)
	hardGPOSFoundationCanonicalBytes    = uint64(1 << 30)
)

// gposFoundationLimits is private because validation is not a public shaping
// capability. The nested parent limits preserve every frozen GDEF/GSUB gate.
type gposFoundationLimits struct {
	GSUBFoundation          gsubFoundationLimits `json:"gsub_foundation"`
	MaxCanonicalBinaryBytes uint64               `json:"max_canonical_binary_bytes"`
	MaxWorkUnits            uint64               `json:"max_work_units"`
	MaxWorkingBytes         uint64               `json:"max_working_bytes"`
	MaxRetainedBytes        uint64               `json:"max_retained_bytes"`
}

func defaultGPOSFoundationLimits() gposFoundationLimits {
	return gposFoundationLimits{
		GSUBFoundation:          defaultGSUBFoundationLimits(),
		MaxCanonicalBinaryBytes: defaultGPOSFoundationCanonicalBytes,
		MaxWorkUnits:            defaultMaxLayoutFoundationWorkUnits,
		MaxWorkingBytes:         defaultMaxLayoutFoundationWorkingBytes,
		MaxRetainedBytes:        defaultMaxLayoutFoundationRetainedBytes,
	}
}

func normalizeGPOSFoundationLimits(value gposFoundationLimits) (gposFoundationLimits, error) {
	defaults := defaultGPOSFoundationLimits()
	var err error
	value.GSUBFoundation, err = normalizeGSUBFoundationLimits(value.GSUBFoundation)
	if err != nil {
		return gposFoundationLimits{}, err
	}
	normalizeLayoutFoundationUint64(&value.MaxCanonicalBinaryBytes, defaults.MaxCanonicalBinaryBytes)
	normalizeLayoutFoundationUint64(&value.MaxWorkUnits, defaults.MaxWorkUnits)
	normalizeLayoutFoundationUint64(&value.MaxWorkingBytes, defaults.MaxWorkingBytes)
	normalizeLayoutFoundationUint64(&value.MaxRetainedBytes, defaults.MaxRetainedBytes)
	if value.MaxCanonicalBinaryBytes > hardGPOSFoundationCanonicalBytes ||
		value.MaxWorkUnits > hardMaxLayoutFoundationWorkUnits ||
		value.MaxWorkingBytes > hardMaxLayoutFoundationWorkingBytes ||
		value.MaxRetainedBytes > hardMaxLayoutFoundationRetainedBytes {
		return gposFoundationLimits{}, fontError(codeLayoutLimits, "layout/gpos_foundation/limits", ErrLimitsInvalid)
	}
	return value, nil
}

type gposFoundationProfileDescriptor struct {
	SchemaVersion            int                  `json:"schema_version"`
	Profile                  string               `json:"profile"`
	ReceiptProfile           string               `json:"receipt_profile"`
	ParentProfile            string               `json:"parent_profile"`
	DescriptorHashProfile    string               `json:"descriptor_hash_profile"`
	FoundationHashProfile    string               `json:"foundation_hash_profile"`
	SourceBindingSemantics   string               `json:"source_binding_semantics"`
	LookupSemantics          string               `json:"lookup_semantics"`
	ContextCompatibility     string               `json:"context_compatibility"`
	LogicalGraphSemantics    string               `json:"logical_graph_semantics"`
	CanonicalHashSemantics   string               `json:"canonical_hash_semantics"`
	ResourceSemantics        string               `json:"resource_semantics"`
	OwnershipSemantics       string               `json:"ownership_semantics"`
	CancellationSemantics    string               `json:"cancellation_semantics"`
	FailureSemantics         string               `json:"failure_semantics"`
	CapabilitySemantics      string               `json:"capability_semantics"`
	ExcludedResponsibilities string               `json:"excluded_responsibilities"`
	DefaultLimits            gposFoundationLimits `json:"default_limits"`
}

var gposFoundationDescriptorV1 = gposFoundationProfileDescriptor{
	SchemaVersion: gposFoundationSchemaVersion,
	Profile:       gposFoundationProfileV1, ReceiptProfile: gposFoundationReceiptProfileV1,
	ParentProfile:            gsubFoundationReceiptProfileV1,
	DescriptorHashProfile:    gposFoundationDescriptorHashV1,
	FoundationHashProfile:    gposFoundationCanonicalHashV1,
	SourceBindingSemantics:   "The frozen GDEF foundation and private GSUB foundation are completed first, followed by a third live OutlineSet to GeometryFont to OutlineFont to Font rebind. GDEF and GSUB are decoded again from that one live view; the recomputed GSUB canonical hash must equal the parent receipt before GPOS can publish. Fixed GDEF, GSUB and GPOS table identities and hashes must remain identical across all stages.",
	LookupSemantics:          "GPOS 1.0 and static 1.1 with null FeatureVariations are accepted. Lookup types one through nine are decoded: both single and pair formats, cursive, mark-to-base, mark-to-ligature, mark-to-mark, all contextual and chained-contextual formats, and non-nested extensions. ValueFormat reserved bits, immediate-parent Device offsets, Device padding, size parameters, coverage cardinality, second-glyph ordering, classes, glyphs, lookup and sequence indices, exact aliases, direct ownership, GDEF lookup flags and per-reference Anchor format-two point domains are checked. VariationIndex is rejected.",
	ContextCompatibility:     "Context formats share the byte-identical GSUB structural decoder. This strict frozen profile requires format-one rule-set count to equal Coverage cardinality and format-two class-set count to equal maximum class plus one, even though the general OpenType processing specification describes ignoring certain extra entries. This intentional stricter acceptance boundary is part of the descriptor.",
	LogicalGraphSemantics:    "Every contextual PositionLookupRecord becomes a deterministic multiplicity-preserving edge. Canonical ordering, minimum-index topological order, cycle rejection, dependency depth and additive expansion gates are identical to the private GSUB foundation and traverse extension wrappers.",
	CanonicalHashSemantics:   "The canonical hash streams fixed-width big-endian scalars and length-prefixed UTF-8. It binds the parent GSUB foundation hash, source lineage, all fixed table identities, GPOS version, sorted table-tagged physical nodes and edges with raw spans, the logical DAG and every decoder count. No pointer, map, native integer, float or JSON enters the graph preimage.",
	ResourceSemantics:        "Every encoded count and checked product is gated before proportional allocation. Decoder work, working and retained charges cover ValueRecords, pair matrices, anchors, mark/base/ligature arrays, contextual structures, physical evidence and logical edges. Final work is parent_work plus decoder_work plus canonical bytes; retained is parent_retained plus decoder_retained plus fixed model and receipt strings; working is the maximum of parent working and fixed scratch plus decoder working.",
	OwnershipSemantics:       "Success retains the immutable GSUB parent, owned normalized GPOS records, scalar bindings and receipt. Raw table views, readers and canonical payload slices do not escape. Input mutation after success cannot alter retained data or receipt.",
	CancellationSemantics:    "A non-nil context is checked before work, throughout all three live binding stages, GDEF/GSUB/GPOS decoding, sorting, graph traversal, hashing and immediately before private publication. Cancellation returns nil foundation and a strictly zero receipt.",
	FailureSemantics:         "Malformed data, lineage or binding drift, variation data, offset ambiguity, type punning, overlap, physical or logical cycles, resource exhaustion, cancellation and internal drift fail closed with zero output.",
	CapabilitySemantics:      "This descriptor, receipt and model remain private. They do not expose LayoutGraph, lookup execution, shaping, Text capability or raw table access.",
	ExcludedResponsibilities: "Lookup execution, glyph-buffer mutation, script shaping policy, bidi, clustering, fallback, rasterization, paint, encoding, persistence and public LayoutGraph publication remain outside this phase.",
	DefaultLimits:            defaultGPOSFoundationLimits(),
}

func gposFoundationDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(gposFoundationDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var gposFoundationDescriptorIdentityV1 = computeGPOSFoundationDescriptorHash()

func computeGPOSFoundationDescriptorHash() string {
	canonical := gposFoundationDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(gposFoundationDescriptorHashV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(gposFoundationDescriptorHashV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
