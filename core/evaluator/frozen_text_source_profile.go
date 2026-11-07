package evaluator

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

const (
	FrozenTextSourceSchemaVersion                = 1
	FrozenTextSourceProfileV1                    = "cutly.evaluator.frozen-text-source.v1"
	FrozenTextSourceReceiptProfileV1             = "cutly.evaluator.frozen-text-source.receipt.v1"
	FrozenTextSourceDescriptorHashProfileV1      = "cutly.hash.sha256.evaluator-frozen-text-source-descriptor-json.v1"
	FrozenTextSourceBindingHashProfileV1         = "cutly.hash.sha256.evaluator-frozen-text-source-binding-be.v1"
	FrozenTextSourceCanonicalSourceHashProfileV1 = "cutly.hash.sha256.text-source-canonical-json.v1"
	FrozenTextSourceDocumentHashProfileV1        = "cutly.hash.sha256.document-v2-canonical-json.v1"
	FrozenTextSourceBindingStatusV1              = "snapshot-asset-font-text-lineage-live-replayed"
	FrozenTextSourceCapabilityStatusV1           = "frozen-text-source-only-not-shaped-laid-out-or-rasterized"

	DefaultMaxFrozenTextSourceBytes          uint64 = documentv2.TextMaxUTF8Bytes
	DefaultMaxFrozenTextSourceCanonicalBytes uint64 = 8 << 20
	DefaultMaxFrozenTextSemanticBytes        uint64 = 16 << 20
	DefaultMaxFrozenTextLayoutInputBytes     uint64 = 16 << 20
	DefaultMaxFrozenTextCanonicalBinaryBytes uint64 = 64 << 20
	DefaultMaxFrozenTextWorkUnits            uint64 = 128 << 20
	DefaultMaxFrozenTextWorkingBytes         uint64 = 128 << 20
	DefaultMaxFrozenTextRetainedBytes        uint64 = 16 << 20

	hardMaxFrozenTextSourceBytes          uint64 = documentv2.TextMaxUTF8Bytes
	hardMaxFrozenTextSourceCanonicalBytes uint64 = 64 << 20
	hardMaxFrozenTextSemanticBytes        uint64 = 128 << 20
	hardMaxFrozenTextLayoutInputBytes     uint64 = 128 << 20
	hardMaxFrozenTextCanonicalBinaryBytes uint64 = 512 << 20
	hardMaxFrozenTextWorkUnits            uint64 = 1 << 40
	hardMaxFrozenTextWorkingBytes         uint64 = 1 << 30
	hardMaxFrozenTextRetainedBytes        uint64 = 256 << 20

	frozenTextFixedWorkingBytes  uint64 = 64 << 10
	frozenTextFixedRetainedBytes uint64 = 2 << 10
)

var (
	ErrFrozenTextSourceInputInvalid  = errors.New("evaluator: invalid frozen text source input")
	ErrFrozenTextSourceLimitsInvalid = errors.New("evaluator: invalid frozen text source limits")
	ErrFrozenTextSourceBinding       = errors.New("evaluator: frozen text source binding failed")
	ErrFrozenTextSourceResourceLimit = errors.New("evaluator: frozen text source resource limit exceeded")
	ErrFrozenTextSourceCanceled      = errors.New("evaluator: frozen text source binding canceled")
	ErrFrozenTextSourceInvariant     = errors.New("evaluator: frozen text source invariant failed")
)

const (
	CodeFrozenTextSourceInputInvalid  ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_INPUT_INVALID"
	CodeFrozenTextSourceLimitsInvalid ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_LIMITS_INVALID"
	CodeFrozenTextSourceBinding       ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_BINDING"
	CodeFrozenTextSourceResourceLimit ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_RESOURCE_LIMIT"
	CodeFrozenTextSourceCanceled      ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_CANCELED"
	CodeFrozenTextSourceInvariant     ErrorCode = "EVALUATOR.V2.FROZEN_TEXT_SOURCE_INVARIANT"
)

type FrozenTextSourceLimits struct {
	MaxTextBytes                  uint64 `json:"max_text_bytes"`
	MaxCanonicalSourceBytes       uint64 `json:"max_canonical_source_bytes"`
	MaxTextSemanticCanonicalBytes uint64 `json:"max_text_semantic_canonical_bytes"`
	MaxLayoutInputCanonicalBytes  uint64 `json:"max_layout_input_canonical_bytes"`
	MaxCanonicalBinaryBytes       uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits                  uint64 `json:"max_work_units"`
	MaxWorkingBytes               uint64 `json:"max_working_bytes"`
	MaxRetainedBytes              uint64 `json:"max_retained_bytes"`
}

func DefaultFrozenTextSourceLimits() FrozenTextSourceLimits {
	return FrozenTextSourceLimits{
		MaxTextBytes:                  DefaultMaxFrozenTextSourceBytes,
		MaxCanonicalSourceBytes:       DefaultMaxFrozenTextSourceCanonicalBytes,
		MaxTextSemanticCanonicalBytes: DefaultMaxFrozenTextSemanticBytes,
		MaxLayoutInputCanonicalBytes:  DefaultMaxFrozenTextLayoutInputBytes,
		MaxCanonicalBinaryBytes:       DefaultMaxFrozenTextCanonicalBinaryBytes,
		MaxWorkUnits:                  DefaultMaxFrozenTextWorkUnits,
		MaxWorkingBytes:               DefaultMaxFrozenTextWorkingBytes,
		MaxRetainedBytes:              DefaultMaxFrozenTextRetainedBytes,
	}
}

type FrozenTextSourceProfileDescriptor struct {
	SchemaVersion              int                    `json:"schema_version"`
	Profile                    string                 `json:"profile"`
	ReceiptProfile             string                 `json:"receipt_profile"`
	DescriptorHashProfile      string                 `json:"descriptor_hash_profile"`
	BindingHashProfile         string                 `json:"binding_hash_profile"`
	DocumentHashProfile        string                 `json:"document_hash_profile"`
	CanonicalSourceHashProfile string                 `json:"canonical_source_hash_profile"`
	TextSemanticHashProfile    string                 `json:"text_semantic_hash_profile"`
	LayoutInputHashProfile     string                 `json:"layout_input_hash_profile"`
	DescriptorHashSemantics    string                 `json:"descriptor_hash_semantics"`
	BindingHashSemantics       string                 `json:"binding_hash_semantics"`
	BindingEncoding            string                 `json:"binding_encoding"`
	SourceBindingSemantics     string                 `json:"source_binding_semantics"`
	DocumentBindingSemantics   string                 `json:"document_binding_semantics"`
	CanonicalReplaySemantics   string                 `json:"canonical_replay_semantics"`
	OwnershipSemantics         string                 `json:"ownership_semantics"`
	ReceiptSemantics           string                 `json:"receipt_semantics"`
	ResourceSemantics          string                 `json:"resource_semantics"`
	CancellationSemantics      string                 `json:"cancellation_semantics"`
	FailureSemantics           string                 `json:"failure_semantics"`
	CapabilitySemantics        string                 `json:"capability_semantics"`
	DefaultLimits              FrozenTextSourceLimits `json:"default_limits"`
}

var frozenTextSourceDescriptorV1 = FrozenTextSourceProfileDescriptor{
	SchemaVersion:              FrozenTextSourceSchemaVersion,
	Profile:                    FrozenTextSourceProfileV1,
	ReceiptProfile:             FrozenTextSourceReceiptProfileV1,
	DescriptorHashProfile:      FrozenTextSourceDescriptorHashProfileV1,
	BindingHashProfile:         FrozenTextSourceBindingHashProfileV1,
	DocumentHashProfile:        FrozenTextSourceDocumentHashProfileV1,
	CanonicalSourceHashProfile: FrozenTextSourceCanonicalSourceHashProfileV1,
	TextSemanticHashProfile:    documentv2.TextSemanticHashProfileV1,
	LayoutInputHashProfile:     documentv2.TextLayoutInputHashProfileV1,
	DescriptorHashSemantics:    "SHA-256 over u64be(len(descriptor-hash-profile)), exact profile UTF-8, u64be(len(descriptor JSON)), and the exact minified encoding/json bytes emitted from this ordered map-free descriptor struct in declared field order.",
	BindingHashSemantics:       "Streaming SHA-256 over the fixed field order in binding_encoding. It binds the descriptor identity, opaque document anchor, selected asset/font identities, every TextSource scalar, all frozen profiles, the three replayed identities and actual/upper byte counts, statuses, and portable resource charges. BindingHash itself is excluded.",
	BindingEncoding:            "str is u64be UTF-8 byte length followed by exact bytes; u16/u32/u64 are unsigned big-endian and i64 is its two's-complement u64 bit pattern. Order: binding profile, receipt profile, source profile, descriptor-hash profile, descriptor hash, document-hash profile, document hash, schema u32, document canonical bytes, asset ID/kind/content hash, asset index, text/evaluator profiles, font ID/kind/hash, font/run/layout/outline/raster/paint/coverage profiles, font index, font-size numerator/denominator, line-height numerator/denominator, viewport width/height, fill RGBA u16, language/script/direction/alignment, Unicode version/validation profile/hash profile/table hash, source hash profile/hash, semantic hash profile/hash, layout hash profile/hash, binding profile, binding status, capability status, source actual/upper bytes, semantic actual/upper bytes, layout actual/upper bytes, text bytes, binding canonical bytes, total canonical bytes, work, working, retained. Raw text is committed by canonical source hash and is not duplicated verbatim in the binding preimage; its exact UTF-8 byte length is bound as text bytes.",
	SourceBindingSemantics:     "BindTextSource accepts one exact asset ID and index, resolves it in O(1) through the aligned compact slot table to a dense private scalar record, then live-checks the record asset index/ID, public SnapshotAsset metadata, text kind/profile, pinned font ID/index/kind/content hash, frozen profile set, and private binding hash before publication. Snapshots without v2 text retain neither slots nor records.",
	DocumentBindingSemantics:   "DocumentHash is the opaque canonical CompileSnapshot trust anchor retained by FrozenSnapshot. A single FrozenTextSource view does not expose the Document bytes and is not independent proof of the complete Document.",
	CanonicalReplaySemantics:   "CompileSnapshot and BindTextSource call the same identity helper. It rebuilds a local TextSource and the semantic/layout envelopes, encodes all three with videoedit.CanonicalJSON, and checks their exact byte counts and SHA-256 identities against the private record and SnapshotAsset metadata.",
	OwnershipSemantics:         "The dense private record and successful FrozenTextSource contain scalar value fields only. Publication clones every dynamic string and exposes only value getters and a scalar receipt; no snapshot pointer, TextSource pointer, raw bytes, reader, slice, map, or backing store is exposed.",
	ReceiptSemantics:           "A serialized FrozenTextSourceReceipt and its public SHA-256 binding are unsigned structural evidence and never authority. Only a live opaque *FrozenTextSource returned by a successful BindTextSource call authorizes downstream use.",
	ResourceSemantics:          "All arithmetic is checked uint64. TextBytes=len UTF-8 text. CanonicalBinaryBytes=source actual+semantic actual+layout actual+binding canonical. WorkUnits=CanonicalBinaryBytes+TextBytes. WorkingBytes=65536+3*(source upper+semantic upper+layout upper)+binding canonical. WorkingBytes is an exact portable logical admission charge for the three sequential CanonicalJSON replay stages, not an allocator/RSS upper-bound guarantee. RetainedBytes=2048+2*sum of UTF-8 byte lengths for this exact retained string set: documentHash, assetID, assetKind, assetContentHash, textProfile, evaluatorProfile, text, fontAssetID, fontAssetKind, fontContentHash, fontProfile, runInputProfile, layoutProfile, outlineProfile, rasterProfile, paintProfile, coverageHashProfile, language, script, direction, alignment, unicodeVersion, unicodeValidationProfile, unicodeValidationHashProfile, unicodeValidationTableHash, canonicalSourceHashProfile, canonicalSourceHash, textSemanticHashProfile, textSemanticHash, layoutInputHashProfile, layoutInputHash, bindingHashProfile, receiptProfile, sourceProfile, descriptorHashProfile, descriptorHash, documentHashProfile, bindingStatus, capabilityStatus, bindingHash. Before canonical replay, exact stored charges and checked JSON 6*n+2 upper counts are admitted; actual encoded counts are checked again after replay.",
	CancellationSemantics:      "A non-nil context is checked before validation, during private hashing, around every canonical replay, and immediately before publication. Cancellation returns a nil source and zero receipt.",
	FailureSemantics:           "Invalid selection, drifted metadata or private lineage, canonical mismatch, resource overflow, cancellation, and unexpected panic fail closed with a nil source and zero receipt; unexpected panic is classified as an invariant failure.",
	CapabilitySemantics:        "This view authorizes access only to one frozen TextSource input and its pinned font identity. It does not attest font bytes, shaping output, layout, outlines, coverage, pixels, or a complete Document.",
	DefaultLimits:              DefaultFrozenTextSourceLimits(),
}

var frozenTextSourceDescriptorHashV1 = mustFrozenTextSourceDescriptorHash()

func FrozenTextSourceDescriptor() FrozenTextSourceProfileDescriptor {
	return frozenTextSourceDescriptorV1
}

func FrozenTextSourceDescriptorHash() string {
	return frozenTextSourceDescriptorHashV1
}

// FrozenTextSourceDescriptorCanonicalJSON returns a defensive copy of the exact minified
// encoding/json representation of the ordered, map-free descriptor struct. Its declared field
// order is protocol-significant; this function does not claim RFC 8785/JCS key sorting.
func FrozenTextSourceDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(frozenTextSourceDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

func mustFrozenTextSourceDescriptorHash() string {
	canonical := FrozenTextSourceDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(FrozenTextSourceDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(FrozenTextSourceDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func normalizeFrozenTextSourceLimits(value FrozenTextSourceLimits) (FrozenTextSourceLimits, error) {
	if value == (FrozenTextSourceLimits{}) {
		value = DefaultFrozenTextSourceLimits()
	}
	if value.MaxTextBytes == 0 || value.MaxTextBytes > hardMaxFrozenTextSourceBytes ||
		value.MaxCanonicalSourceBytes == 0 || value.MaxCanonicalSourceBytes > hardMaxFrozenTextSourceCanonicalBytes ||
		value.MaxTextSemanticCanonicalBytes == 0 || value.MaxTextSemanticCanonicalBytes > hardMaxFrozenTextSemanticBytes ||
		value.MaxLayoutInputCanonicalBytes == 0 || value.MaxLayoutInputCanonicalBytes > hardMaxFrozenTextLayoutInputBytes ||
		value.MaxCanonicalBinaryBytes == 0 || value.MaxCanonicalBinaryBytes > hardMaxFrozenTextCanonicalBinaryBytes ||
		value.MaxWorkUnits == 0 || value.MaxWorkUnits > hardMaxFrozenTextWorkUnits ||
		value.MaxWorkingBytes < frozenTextFixedWorkingBytes || value.MaxWorkingBytes > hardMaxFrozenTextWorkingBytes ||
		value.MaxRetainedBytes < frozenTextFixedRetainedBytes || value.MaxRetainedBytes > hardMaxFrozenTextRetainedBytes {
		return FrozenTextSourceLimits{}, frozenTextSourceError(CodeFrozenTextSourceLimitsInvalid, "frozen_text_source/limits", ErrFrozenTextSourceLimitsInvalid)
	}
	return value, nil
}

func frozenTextSourceError(code ErrorCode, path string, cause error) error {
	return evaluationError(code, path, cause)
}

func frozenTextCanceled(path string, err error) error {
	return frozenTextSourceError(CodeFrozenTextSourceCanceled, path, errors.Join(ErrFrozenTextSourceCanceled, err))
}
