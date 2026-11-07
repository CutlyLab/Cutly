package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/CutlyLab/Cutly/internal/buildidentity"
)

const (
	ShapeProviderSchemaVersion                          = 1
	ShapeProviderProfileV1                              = buildidentity.TextShapeProviderProfileV1
	ShapeProviderReceiptProfileV1                       = "cutly.shape.go-text-harfbuzz-static-glyf.provider-receipt.v1"
	ShapeReceiptProfileV1                               = "cutly.shape.go-text-harfbuzz-static-glyf.receipt.v1"
	ShapeOutputProfileV1                                = "cutly.text-shaped-stream.logical-pen-q26_6.v1"
	ShapeProviderDescriptorHashProfileV1                = "cutly.hash.sha256.text-shape-provider-descriptor-json.v1"
	ShapeProviderHashProfileV1                          = "cutly.hash.sha256.text-shape-provider-binding-be.v1"
	ShapeInputHashProfileV1                             = "cutly.hash.sha256.text-shape-input-be.v1"
	ShapeOutputHashProfileV1                            = "cutly.hash.sha256.text-shaped-glyph-stream-be.v1"
	ShapeClusterPolicyV1                                = "harfbuzz.monotone-characters.absolute-utf8-byte-offsets.v1"
	ShapeFeaturePolicyV1                                = "go-text-harfbuzz-default-features-no-caller-overrides.v1"
	ShapeDirectionNormalizationProfileV1                = "cutly.hb-raw-to-logical-signed-pen.v1"
	ShapeProviderBindingStatusV1                        = "live_bound_shape_provider_not_text_render_capability"
	ShapeProviderCapabilityStatusV1                     = "shape_only_not_text_render_capability"
	ShapeGoTextModuleVersionV1                          = "v0.3.5"
	ShapeEmbeddedHarfBuzzVersionV1                      = "12.3.0"
	ShapeEmbeddedHarfBuzzCommitV1                       = "5a31dd02f0b32de12336f72db9297bfe94cf0da1"
	ShapeScriptLatin                                    = "Latn"
	ShapeScriptHan                                      = "Hani"
	ShapeScriptArabic                                   = "Arab"
	ShapeDirectionLTR                    ShapeDirection = "ltr"
	ShapeDirectionRTL                    ShapeDirection = "rtl"
)

const (
	DefaultMaxShapeTextBytes        uint64 = 64 << 10
	DefaultMaxShapeLines            uint64 = 1_024
	DefaultMaxShapeScalarsPerLine   uint64 = 32_768
	DefaultMaxShapeScalars          uint64 = 262_144
	DefaultMaxShapeGlyphsPerLine    uint64 = 32_768
	DefaultMaxShapeGlyphs           uint64 = 262_144
	DefaultMaxShapeCanonicalBytes   uint64 = 128 << 20
	DefaultMaxShapeWorkUnits        uint64 = 1 << 32
	DefaultMaxShapeWorkingBytes     uint64 = 512 << 20
	DefaultMaxShapeRetainedBytes    uint64 = 256 << 20
	HardMaxShapeTextBytes           uint64 = 1 << 20
	HardMaxShapeLines               uint64 = 65_536
	HardMaxShapeScalarsPerLine      uint64 = 65_536
	HardMaxShapeScalars             uint64 = 4 << 20
	HardMaxShapeGlyphsPerLine       uint64 = 262_144
	HardMaxShapeGlyphs              uint64 = 4 << 20
	HardMaxShapeCanonicalBytes      uint64 = 2 << 30
	HardMaxShapeWorkUnits           uint64 = 1 << 60
	HardMaxShapeWorkingBytes        uint64 = 4 << 30
	HardMaxShapeRetainedBytes       uint64 = 2 << 30
	shapeLogicalFixedWorkingBytes   uint64 = 64 << 10
	shapeLogicalFixedRetainedBytes  uint64 = 2 << 10
	shapeLogicalLineBytes           uint64 = 24
	shapeLogicalGlyphBytes          uint64 = 48
	shapeLogicalBufferGlyphBytes    uint64 = 64
	shapeDigestStringBytes          uint64 = 71
	shapeNormalizationMaxExpansion  uint64 = 4
	shapeHarfBuzzMaxLenFactor       uint64 = 64
	shapeHarfBuzzMaxLenMinimum      uint64 = 16_384
	shapeHarfBuzzMaxOpsFactor       uint64 = 1_024
	shapeHarfBuzzMaxOpsMinimum      uint64 = 16_384
	defaultMaxShapeProviderWork     uint64 = 1 << 40
	defaultMaxShapeProviderWorking  uint64 = 512 << 20
	defaultMaxShapeProviderRetained uint64 = 1 << 30
	hardMaxShapeProviderWork        uint64 = 1 << 60
	hardMaxShapeProviderWorking     uint64 = 4 << 30
	hardMaxShapeProviderRetained    uint64 = 2 << 30
)

type ShapeDirection string

type ShapeProviderLimits struct {
	MaxWorkUnits     uint64 `json:"max_work_units"`
	MaxWorkingBytes  uint64 `json:"max_working_bytes"`
	MaxRetainedBytes uint64 `json:"max_retained_bytes"`
}

func DefaultShapeProviderLimits() ShapeProviderLimits {
	return ShapeProviderLimits{
		MaxWorkUnits:     defaultMaxShapeProviderWork,
		MaxWorkingBytes:  defaultMaxShapeProviderWorking,
		MaxRetainedBytes: defaultMaxShapeProviderRetained,
	}
}

// ShapeLimits are portable logical admission limits. They bound Cutly-owned input/output and
// account for go-text's fixed HarfBuzz buffer expansion and operation envelopes; they are not an
// allocator, GC-heap, RSS, or preemptible-execution guarantee.
type ShapeLimits struct {
	MaxTextBytes      uint64 `json:"max_text_bytes"`
	MaxLines          uint64 `json:"max_lines"`
	MaxScalarsPerLine uint64 `json:"max_scalars_per_line"`
	MaxScalars        uint64 `json:"max_scalars"`
	MaxGlyphsPerLine  uint64 `json:"max_glyphs_per_line"`
	MaxGlyphs         uint64 `json:"max_glyphs"`
	MaxCanonicalBytes uint64 `json:"max_canonical_bytes"`
	MaxWorkUnits      uint64 `json:"max_work_units"`
	MaxWorkingBytes   uint64 `json:"max_working_bytes"`
	MaxRetainedBytes  uint64 `json:"max_retained_bytes"`
}

func DefaultShapeLimits() ShapeLimits {
	return ShapeLimits{
		MaxTextBytes: DefaultMaxShapeTextBytes, MaxLines: DefaultMaxShapeLines,
		MaxScalarsPerLine: DefaultMaxShapeScalarsPerLine, MaxScalars: DefaultMaxShapeScalars,
		MaxGlyphsPerLine: DefaultMaxShapeGlyphsPerLine, MaxGlyphs: DefaultMaxShapeGlyphs,
		MaxCanonicalBytes: DefaultMaxShapeCanonicalBytes, MaxWorkUnits: DefaultMaxShapeWorkUnits,
		MaxWorkingBytes: DefaultMaxShapeWorkingBytes, MaxRetainedBytes: DefaultMaxShapeRetainedBytes,
	}
}

type ShapeProviderProfileDescriptor struct {
	SchemaVersion                 int                 `json:"schema_version"`
	Profile                       string              `json:"profile"`
	ProviderReceiptProfile        string              `json:"provider_receipt_profile"`
	ShapeReceiptProfile           string              `json:"shape_receipt_profile"`
	OutputProfile                 string              `json:"output_profile"`
	DescriptorHashProfile         string              `json:"descriptor_hash_profile"`
	ProviderHashProfile           string              `json:"provider_hash_profile"`
	InputHashProfile              string              `json:"input_hash_profile"`
	OutputHashProfile             string              `json:"output_hash_profile"`
	DependencyLockProfile         string              `json:"dependency_lock_profile"`
	DependencyFingerprintProfile  string              `json:"dependency_fingerprint_profile"`
	DependencyFingerprint         string              `json:"dependency_fingerprint"`
	GoTextModuleVersion           string              `json:"go_text_module_version"`
	EmbeddedHarfBuzzVersion       string              `json:"embedded_harfbuzz_version"`
	EmbeddedHarfBuzzCommit        string              `json:"embedded_harfbuzz_commit"`
	LayoutGraphProfile            string              `json:"layout_graph_profile"`
	FontProfile                   string              `json:"font_profile"`
	OutlineSetProfile             string              `json:"outline_set_profile"`
	ClusterPolicy                 string              `json:"cluster_policy"`
	FeaturePolicy                 string              `json:"feature_policy"`
	DirectionNormalizationProfile string              `json:"direction_normalization_profile"`
	SourceBindingSemantics        string              `json:"source_binding_semantics"`
	InputSemantics                string              `json:"input_semantics"`
	ShapingSemantics              string              `json:"shaping_semantics"`
	MissingGlyphSemantics         string              `json:"missing_glyph_semantics"`
	CoordinateSemantics           string              `json:"coordinate_semantics"`
	CanonicalHashSemantics        string              `json:"canonical_hash_semantics"`
	ResourceSemantics             string              `json:"resource_semantics"`
	CancellationSemantics         string              `json:"cancellation_semantics"`
	OwnershipSemantics            string              `json:"ownership_semantics"`
	FailureSemantics              string              `json:"failure_semantics"`
	CapabilitySemantics           string              `json:"capability_semantics"`
	ExcludedResponsibilities      string              `json:"excluded_responsibilities"`
	DefaultLimits                 ShapeLimits         `json:"default_limits"`
	DefaultProviderLimits         ShapeProviderLimits `json:"default_provider_limits"`
}

var shapeProviderDescriptorV1 = ShapeProviderProfileDescriptor{
	SchemaVersion: ShapeProviderSchemaVersion, Profile: ShapeProviderProfileV1,
	ProviderReceiptProfile: ShapeProviderReceiptProfileV1, ShapeReceiptProfile: ShapeReceiptProfileV1,
	OutputProfile: ShapeOutputProfileV1, DescriptorHashProfile: ShapeProviderDescriptorHashProfileV1,
	ProviderHashProfile: ShapeProviderHashProfileV1,
	InputHashProfile:    ShapeInputHashProfileV1, OutputHashProfile: ShapeOutputHashProfileV1,
	DependencyLockProfile:        buildidentity.TextShapeCandidateProfileV1,
	DependencyFingerprintProfile: buildidentity.TextShapeFingerprintProfileV1,
	DependencyFingerprint:        buildidentity.TextShapeCandidateFingerprint(),
	GoTextModuleVersion:          ShapeGoTextModuleVersionV1, EmbeddedHarfBuzzVersion: ShapeEmbeddedHarfBuzzVersionV1,
	EmbeddedHarfBuzzCommit: ShapeEmbeddedHarfBuzzCommitV1, LayoutGraphProfile: LayoutGraphProfileV1,
	FontProfile: ProfileV1, OutlineSetProfile: OutlineSetProfileV1,
	ClusterPolicy: ShapeClusterPolicyV1, FeaturePolicy: ShapeFeaturePolicyV1,
	DirectionNormalizationProfile: ShapeDirectionNormalizationProfileV1,
	SourceBindingSemantics:        "BindShapeProvider and every admitted Shape call rebuild the complete public LayoutGraph from the exact private OutlineSet lineage retained by the supplied graph, under its receipt-exact aggregate limits. Bind performs scalar resource pre-admission before replay/parse; Shape performs allocation-free input/resource pre-admission before replay/execution. The rebuilt graph receipt, source pointer, font content hash, outline-set hash and graph hash must equal the published bindings before the privately owned complete sfnt bytes may enter go-text. No raw bytes, reader, go-text Font, Face or HarfBuzz Font crosses the API.",
	InputSemantics:                "Input is one valid UTF-8 string split only at U+000A into explicit independent horizontal lines. Language is an explicit bounded BCP-47-style tag; script is exactly Latn, Hani or Arab; direction is exactly ltr or rtl. Upstream Text v2 frozen-script/control validation remains a required assembly precondition and is not guessed or replaced here.",
	ShapingSemantics:              "Each nonempty line uses a fresh go-text Face, HarfBuzz Font and Buffer. Script, language and direction are assigned explicitly; GuessSegmentProperties is forbidden. Buffer flags are BOT|EOT, cluster level is MONOTONE_CHARACTERS, ppem and variation coordinates remain empty, and caller feature overrides are impossible. A nil feature slice selects the defaults compiled into pinned go-text v0.3.5, including its fixed ccmp/locl/mark/mkmk/rlig and horizontal calt/clig/curs/dist/kern/liga/rclt policy.",
	MissingGlyphSemantics:         "Every non-newline scalar must map to the same nonzero glyph in both the live Cutly core cmap and the privately parsed go-text cmap before shaping; every shaped glyph must remain nonzero and inside the validated glyph domain. Missing glyph substitution, system fonts and fallback are forbidden and fail the whole operation.",
	CoordinateSemantics:           "HarfBuzz XScale and YScale are exactly units_per_em*64, so positions are signed font-space Q26.6. LTR output retains provider order. RTL output reverses complete raw records and maps x_advance=-raw_x_advance, x_offset=raw_x_offset-raw_x_advance, y_advance=0 and y_offset=raw_y_offset, yielding original-text logical cluster order and signed negative pen traversal.",
	CanonicalHashSemantics:        "Input and output hashes are SHA-256 over length-prefixed UTF-8 strings and fixed-width big-endian scalars. They bind the descriptor, dependency fingerprint, complete source hashes, exact text/run properties, line byte ranges and every normalized glyph record. JSON, maps, native integers, pointers and host locale never enter a preimage.",
	ResourceSemantics:             "Bind derives its exact provider charge from the already published scalar graph/font receipts and rejects limits before live replay or go-text parsing. From the replayed private validated GSUB model it locks S, the longest MultipleSubstitution sequence, F=S^(contextual_logical_expansion+1), and R, the saturated sum of lookup references in every FeatureRecord without deduplicating aliased Feature tables; both private GSUB and GPOS feature lists reject stch before go-text parsing because its width-driven postprocess is outside HarfBuzz maxLen. Shape rejects Text byte-limit excess in O(1) before the non-cancellable UTF-8 validity scan, then scans admitted valid UTF-8 without proportional allocation. Pinned go-text v0.3.5 carries Unicode 17 data whose complete recursive canonical decomposition has maximum length 4, so each nonempty N-scalar line sets P=4*N+1 for normalization plus one BOT dotted circle, H=max(64*N,16384) from go-text's pinned HarfBuzz maxLen, and reserves min(P*F^R,max(P,H*F)), with arithmetic saturated one past the public glyph hard limit. It also charges max(N*1024,16384) operations, the reserved line buffer, upper-bound canonical streams, temporary line-input records, final line/glyph records and receipt strings. Exact input canonical bytes and fixed 71-byte digest placeholders are admitted before fixed-block streaming hashes or owned property copies. Every limit is gated before line/glyph allocation, graph replay, or go-text Shape; exact output must remain within the admitted bounds. Receipt admission fields expose the reserved glyph/canonical bounds. These are deterministic portable logical charges, not allocator, GC-heap, RSS or sandbox limits.",
	CancellationSemantics:         "A non-nil context is checked before work, throughout the complete live graph replay and all Cutly loops, immediately before and after each go-text Shape call, throughout hashing and before publication. go-text v0.3.5 exposes no context or error from Shape, so an in-progress single line is cooperatively non-preemptible; observed cancellation still discards every local result and returns nil with a zero receipt.",
	OwnershipSemantics:            "The provider privately retains the public LayoutGraph and one immutable go-text base Font parsed from the same owned sfnt snapshot. The base Font is safe for concurrent reads; every Shape call creates its own non-shareable Face, HarfBuzz Font and Buffer. Successful shaped output owns flat copied scalar records, an owned bounded Language string, and package-canonical Script and Direction strings; it never retains Text or caller property backing storage and exposes only indexed value views.",
	FailureSemantics:              "Nil context/provider/graph, dependency identity drift, source or receipt mutation, parse/model disagreement, malformed input, missing glyphs, glyph zero, vertical advances, cluster drift, an output exceeding its conservative admission bound, count or arithmetic overflow, resource exhaustion, cancellation and internal inconsistency fail closed with nil output and a strictly zero receipt.",
	CapabilitySemantics:           "This profile proves a pinned static-glyf shaping provider only. It does not rasterize, paint, composite, encode or advertise cutly.text.pinned-font-viewport-px.v2; final Text capability remains gated on live Document assembly and the text raster/provider closure.",
	ExcludedResponsibilities:      "System-font discovery, fallback, bidi inference, mixed-script segmentation, caller normalization or features, Arabic stch width-driven replication, variable/CFF/color fonts, hinting, outline rasterization, paint, final layout assembly, signatures, persistence and Text render capability publication are outside this provider.",
	DefaultLimits:                 DefaultShapeLimits(),
	DefaultProviderLimits:         DefaultShapeProviderLimits(),
}

func ShapeProviderDescriptor() ShapeProviderProfileDescriptor { return shapeProviderDescriptorV1 }

func ShapeProviderDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(shapeProviderDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var shapeProviderDescriptorIdentityV1 = computeShapeProviderDescriptorHash()

func ShapeProviderDescriptorHash() string { return shapeProviderDescriptorIdentityV1 }

func computeShapeProviderDescriptorHash() string {
	canonical := ShapeProviderDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(ShapeProviderDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(ShapeProviderDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
