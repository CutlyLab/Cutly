package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	LayoutGraphSchemaVersion           = 1
	LayoutGraphProfileV1               = "cutly.font.opentype-layout-graph.v1"
	LayoutGraphReceiptProfileV1        = "cutly.font.opentype-layout-graph.receipt.v1"
	LayoutGraphDescriptorHashProfileV1 = "cutly.hash.sha256.font-opentype-layout-graph-descriptor-json.v1"
	LayoutGraphCanonicalHashProfileV1  = "cutly.hash.sha256.font-opentype-layout-graph-be.v1"
	LayoutGraphValidationProfileV1     = "cutly.font.opentype-layout-gdef-gsub-gpos-static.v1"
	LayoutGraphCapabilityStatusV1      = "validated_public_graph_not_text_render_capability"
	LayoutGraphLookupExecutionStatusV1 = "not_executed"
	LayoutGraphVariableLayoutStatusV1  = "feature_variations_item_variation_store_and_variation_index_rejected"
	defaultLayoutGraphCanonicalBytes   = uint64(1 << 20)
	hardLayoutGraphCanonicalBytes      = uint64(64 << 20)
	layoutGraphFixedRetainedBytes      = uint64(2_048)
	layoutGraphFixedWorkingBytes       = uint64(32_768)
)

// LayoutGraphLimits bounds the complete atomic GDEF+GSUB+GPOS validation and
// the small domain-separated public graph receipt. Structural table limits
// remain fixed by the validated profile; callers may lower the aggregate
// portable work, working and retained budgets without widening acceptance.
type LayoutGraphLimits struct {
	MaxValidationCanonicalBinaryBytes uint64 `json:"max_validation_canonical_binary_bytes"`
	MaxCanonicalBinaryBytes           uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits                      uint64 `json:"max_work_units"`
	MaxWorkingBytes                   uint64 `json:"max_working_bytes"`
	MaxRetainedBytes                  uint64 `json:"max_retained_bytes"`
}

func DefaultLayoutGraphLimits() LayoutGraphLimits {
	return LayoutGraphLimits{
		MaxValidationCanonicalBinaryBytes: defaultGPOSFoundationCanonicalBytes,
		MaxCanonicalBinaryBytes:           defaultLayoutGraphCanonicalBytes,
		MaxWorkUnits:                      defaultMaxLayoutFoundationWorkUnits,
		MaxWorkingBytes:                   defaultMaxLayoutFoundationWorkingBytes,
		MaxRetainedBytes:                  defaultMaxLayoutFoundationRetainedBytes,
	}
}

func normalizeLayoutGraphLimits(value LayoutGraphLimits) (LayoutGraphLimits, error) {
	defaults := DefaultLayoutGraphLimits()
	normalizeLayoutFoundationUint64(&value.MaxValidationCanonicalBinaryBytes, defaults.MaxValidationCanonicalBinaryBytes)
	normalizeLayoutFoundationUint64(&value.MaxCanonicalBinaryBytes, defaults.MaxCanonicalBinaryBytes)
	normalizeLayoutFoundationUint64(&value.MaxWorkUnits, defaults.MaxWorkUnits)
	normalizeLayoutFoundationUint64(&value.MaxWorkingBytes, defaults.MaxWorkingBytes)
	normalizeLayoutFoundationUint64(&value.MaxRetainedBytes, defaults.MaxRetainedBytes)
	if value.MaxValidationCanonicalBinaryBytes > hardGPOSFoundationCanonicalBytes ||
		value.MaxCanonicalBinaryBytes > hardLayoutGraphCanonicalBytes ||
		value.MaxWorkUnits > hardMaxLayoutFoundationWorkUnits ||
		value.MaxWorkingBytes > hardMaxLayoutFoundationWorkingBytes ||
		value.MaxRetainedBytes > hardMaxLayoutFoundationRetainedBytes {
		return LayoutGraphLimits{}, fontError(CodeLayoutGraphLimits, "layout_graph/limits", ErrLimitsInvalid)
	}
	return value, nil
}

func layoutGraphFoundationLimits(value LayoutGraphLimits) gposFoundationLimits {
	result := defaultGPOSFoundationLimits()
	result.MaxCanonicalBinaryBytes = value.MaxValidationCanonicalBinaryBytes
	result.MaxWorkUnits = value.MaxWorkUnits
	result.MaxWorkingBytes = value.MaxWorkingBytes
	result.MaxRetainedBytes = value.MaxRetainedBytes
	return result
}

// LayoutGraphProfileDescriptor freezes the public validation boundary. It is
// deliberately a graph/introspection contract, not a shaping executor.
type LayoutGraphProfileDescriptor struct {
	SchemaVersion            int               `json:"schema_version"`
	Profile                  string            `json:"profile"`
	ReceiptProfile           string            `json:"receipt_profile"`
	ValidationProfile        string            `json:"validation_profile"`
	ParentValidationProfile  string            `json:"parent_validation_profile"`
	DescriptorHashProfile    string            `json:"descriptor_hash_profile"`
	GraphHashProfile         string            `json:"graph_hash_profile"`
	AtomicBindingSemantics   string            `json:"atomic_binding_semantics"`
	PublicViewSemantics      string            `json:"public_view_semantics"`
	CanonicalHashSemantics   string            `json:"canonical_hash_semantics"`
	ResourceSemantics        string            `json:"resource_semantics"`
	OwnershipSemantics       string            `json:"ownership_semantics"`
	CancellationSemantics    string            `json:"cancellation_semantics"`
	FailureSemantics         string            `json:"failure_semantics"`
	CapabilitySemantics      string            `json:"capability_semantics"`
	ExcludedResponsibilities string            `json:"excluded_responsibilities"`
	DefaultLimits            LayoutGraphLimits `json:"default_limits"`
}

var layoutGraphDescriptorV1 = LayoutGraphProfileDescriptor{
	SchemaVersion:            LayoutGraphSchemaVersion,
	Profile:                  LayoutGraphProfileV1,
	ReceiptProfile:           LayoutGraphReceiptProfileV1,
	ValidationProfile:        LayoutGraphValidationProfileV1,
	ParentValidationProfile:  gposFoundationReceiptProfileV1,
	DescriptorHashProfile:    LayoutGraphDescriptorHashProfileV1,
	GraphHashProfile:         LayoutGraphCanonicalHashProfileV1,
	AtomicBindingSemantics:   "BuildLayoutGraph completes the terminal private GPOS foundation, which live-rebinds OutlineSet through GeometryFont, OutlineFont and Font and revalidates GDEF, GSUB and GPOS from one immutable source lineage. Public state is published only after the terminal GDEF, GSUB and GPOS hashes, fixed table identities and source hashes agree.",
	PublicViewSemantics:      "The graph exposes immutable index-based views of table identity, versions, scripts, languages, features, lookup metadata, physical typed-offset nodes and edges, and contextual lookup dependencies. Views retain normalized owned validation models and never expose raw font/table bytes, readers, maps or mutable slices.",
	CanonicalHashSemantics:   "The graph hash is a domain-separated fixed-width big-endian stream binding this descriptor, source outline/font hashes, all three fixed table identities, the independently canonical GDEF, GSUB and GPOS foundation hashes, table versions and public graph cardinalities. The foundation hashes already bind every validated normalized record, physical byte span and logical dependency edge.",
	ResourceSemantics:        "Private validation retains its exact portable work, working and retained charges. Public work adds graph canonical bytes; public retained adds a fixed graph/receipt charge and receipt strings; public working is the maximum of private validation working bytes and the fixed graph hash workspace. Zero limit fields select defaults and no limit widens the frozen structural parser envelope.",
	OwnershipSemantics:       "Success retains the immutable terminal validation foundation and a comparable scalar receipt. All views are value handles into owned immutable slices; caller mutation after success cannot change a view, receipt or hash.",
	CancellationSemantics:    "A non-nil context is checked throughout every live rebind, GDEF/GSUB/GPOS validation, logical graph traversal and public hashing and immediately before publication. Cancellation returns nil and a strictly zero receipt.",
	FailureSemantics:         "Malformed or unsupported layout data, lineage or table drift, cycles, expansion/resource exhaustion, cancellation, allocation failure and internal inconsistency fail closed with no partial graph or receipt.",
	CapabilitySemantics:      "A LayoutGraph proves static OpenType layout validation and supplies a stable live-binding input to a separately pinned shape provider. It does not execute a lookup, mutate a glyph buffer, shape text, rasterize glyphs or advertise cutly.text.pinned-font-viewport-px.v2.",
	ExcludedResponsibilities: "Script selection policy, bidi, clustering, fallback, GSUB/GPOS lookup execution, go-text or HarfBuzz binding, glyph rasterization, paint, compositing, encoding, persistence, signatures and Text capability publication are outside this profile.",
	DefaultLimits:            DefaultLayoutGraphLimits(),
}

func LayoutGraphDescriptor() LayoutGraphProfileDescriptor { return layoutGraphDescriptorV1 }

func LayoutGraphDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(layoutGraphDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var layoutGraphDescriptorIdentityV1 = computeLayoutGraphDescriptorHash()

func LayoutGraphDescriptorHash() string { return layoutGraphDescriptorIdentityV1 }

func computeLayoutGraphDescriptorHash() string {
	canonical := LayoutGraphDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(LayoutGraphDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(LayoutGraphDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
