package sfntglyf

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	LayoutSourceSchemaVersion                  = 1
	LayoutSourceProfileV1                      = "cutly.font.static-glyf-layout-source-q26_6.v1"
	LayoutSourceReceiptProfileV1               = "cutly.font.static-glyf-layout-source-q26_6.receipt.v1"
	LayoutSourceDescriptorHashProfileV1        = "cutly.hash.sha256.font-static-glyf-layout-source-q26_6-descriptor-json.v1"
	LayoutSourceHashProfileV1                  = "cutly.hash.sha256.font-static-glyf-layout-source-binding-be.v1"
	LayoutSourceBindingStatusV1                = "shape-provider-layout-outline-font-lineage-replayed"
	LayoutSourceLayoutStatusV1                 = "gdef-gsub-gpos-validated-and-shape-provider-bound"
	LayoutSourceRasterStatusV1                 = "not-rasterized"
	LayoutSourceCapabilityStatusV1             = "layout-source-only-not-text-render-capability"
	layoutSourceFixedWorkingBytes       uint64 = 64 << 10
	layoutSourceFixedRetainedBytes      uint64 = 4 << 10
	layoutSourceDigestStringBytes       uint64 = 71
)

const (
	DefaultMaxLayoutSourceGlyphs                  uint64 = uint64(DefaultMaxGlyphs)
	DefaultMaxLayoutSourceContours                uint64 = DefaultMaxOutlineSetExpandedContours
	DefaultMaxLayoutSourceCommands                uint64 = DefaultMaxOutlineSetCommands
	DefaultMaxLayoutSourceCommandPointOccurrences uint64 = DefaultMaxOutlineSetCommandPoints
	DefaultMaxLayoutSourceCommandsPerGlyph        uint64 = DefaultMaxOutlineSetCommandsPerGlyph
	DefaultMaxLayoutSourceCanonicalBytes          uint64 = 1 << 20
	DefaultMaxLayoutSourceWorkUnits               uint64 = 1 << 60
	DefaultMaxLayoutSourceWorkingBytes            uint64 = 4 << 30
	DefaultMaxLayoutSourceRetainedBytes           uint64 = 2 << 30

	HardMaxLayoutSourceGlyphs                  uint64 = uint64(HardMaxGlyphs)
	HardMaxLayoutSourceContours                uint64 = HardMaxOutlineSetExpandedContours
	HardMaxLayoutSourceCommands                uint64 = HardMaxOutlineSetCommands
	HardMaxLayoutSourceCommandPointOccurrences uint64 = HardMaxOutlineSetCommandPoints
	HardMaxLayoutSourceCommandsPerGlyph        uint64 = HardMaxOutlineSetCommandsPerGlyph
	HardMaxLayoutSourceCanonicalBytes          uint64 = 64 << 20
	HardMaxLayoutSourceWorkUnits               uint64 = 1 << 62
	HardMaxLayoutSourceWorkingBytes            uint64 = 8 << 30
	HardMaxLayoutSourceRetainedBytes           uint64 = 4 << 30
)

type LayoutSourceLimits struct {
	MaxGlyphs                  uint64 `json:"max_glyphs"`
	MaxContours                uint64 `json:"max_contours"`
	MaxCommands                uint64 `json:"max_commands"`
	MaxCommandPointOccurrences uint64 `json:"max_command_point_occurrences"`
	MaxCommandsPerGlyph        uint64 `json:"max_commands_per_glyph"`
	MaxCanonicalBinaryBytes    uint64 `json:"max_canonical_binary_bytes"`
	MaxWorkUnits               uint64 `json:"max_work_units"`
	MaxWorkingBytes            uint64 `json:"max_working_bytes"`
	MaxRetainedBytes           uint64 `json:"max_retained_bytes"`
}

func DefaultLayoutSourceLimits() LayoutSourceLimits {
	return LayoutSourceLimits{
		MaxGlyphs: DefaultMaxLayoutSourceGlyphs, MaxContours: DefaultMaxLayoutSourceContours,
		MaxCommands:                DefaultMaxLayoutSourceCommands,
		MaxCommandPointOccurrences: DefaultMaxLayoutSourceCommandPointOccurrences,
		MaxCommandsPerGlyph:        DefaultMaxLayoutSourceCommandsPerGlyph,
		MaxCanonicalBinaryBytes:    DefaultMaxLayoutSourceCanonicalBytes,
		MaxWorkUnits:               DefaultMaxLayoutSourceWorkUnits,
		MaxWorkingBytes:            DefaultMaxLayoutSourceWorkingBytes,
		MaxRetainedBytes:           DefaultMaxLayoutSourceRetainedBytes,
	}
}

type LayoutSourceProfileDescriptor struct {
	SchemaVersion            int                `json:"schema_version"`
	Profile                  string             `json:"profile"`
	ReceiptProfile           string             `json:"receipt_profile"`
	DescriptorHashProfile    string             `json:"descriptor_hash_profile"`
	LayoutSourceHashProfile  string             `json:"layout_source_hash_profile"`
	ShapeProviderProfile     string             `json:"shape_provider_profile"`
	LayoutGraphProfile       string             `json:"layout_graph_profile"`
	OutlineSetProfile        string             `json:"outline_set_profile"`
	FontProfile              string             `json:"font_profile"`
	SourceBindingSemantics   string             `json:"source_binding_semantics"`
	MetricSemantics          string             `json:"metric_semantics"`
	OutlineSemantics         string             `json:"outline_semantics"`
	HashSemantics            string             `json:"hash_semantics"`
	ResourceSemantics        string             `json:"resource_semantics"`
	OwnershipSemantics       string             `json:"ownership_semantics"`
	CancellationSemantics    string             `json:"cancellation_semantics"`
	FailureSemantics         string             `json:"failure_semantics"`
	CapabilitySemantics      string             `json:"capability_semantics"`
	ExcludedResponsibilities string             `json:"excluded_responsibilities"`
	DefaultLimits            LayoutSourceLimits `json:"default_limits"`
}

var layoutSourceDescriptorV1 = LayoutSourceProfileDescriptor{
	SchemaVersion: LayoutSourceSchemaVersion, Profile: LayoutSourceProfileV1,
	ReceiptProfile:          LayoutSourceReceiptProfileV1,
	DescriptorHashProfile:   LayoutSourceDescriptorHashProfileV1,
	LayoutSourceHashProfile: LayoutSourceHashProfileV1,
	ShapeProviderProfile:    ShapeProviderProfileV1, LayoutGraphProfile: LayoutGraphProfileV1,
	OutlineSetProfile: OutlineSetProfileV1, FontProfile: ProfileV1,
	SourceBindingSemantics:   "BindLayoutSource is reachable only from one live ShapeProvider. It verifies the compiled shape dependency identity and provider hash, inspects the provider's private graph/source/font lineage, rejects scalar limits, then receipt-exactly replays the complete LayoutGraph. That replay live-rebinds OutlineSet, exact GeometryFont, OutlineFont and complete static-glyf Font bytes. The replayed source/font pointers, provider receipt, graph hash, outline hash, font hash, metrics and counts must all equal the retained lineage before publication.",
	MetricSemantics:          "units_per_em and signed hhea_ascender come from the revalidated static-glyf core and are bound into the layout-source hash. Glyph count must agree across Font, OutlineSet, LayoutGraph and ShapeProvider. Coordinates remain signed y-up font-space Q26.6 and no viewport scaling is performed.",
	OutlineSemantics:         "Every glyph is addressed only by uint32 glyph ID. A layout-bound glyph view exposes contour count and indexed Move/Line/Quad/Close command views; commands expose only scalar op, point count and copied Q26.6 points. Empty glyphs are valid. Glyph zero is viewable as font evidence but shaped-text assembly must reject it. No hint bytecode is executed.",
	HashSemantics:            "The layout-source hash is streaming SHA-256 over length-prefixed UTF-8 strings and fixed-width big-endian scalars. It binds the descriptor, provider dependency/build identity, complete graph/outline/font hashes, statuses, metrics, outline counts and portable resource charges. The hash field and canonical-byte count do not self-enter the preimage. JSON, maps, pointers, native integers, raw bytes and go-text objects never enter the preimage.",
	ResourceSemantics:        "All count limits and portable resources are admitted from scalar receipts and O(1) slice lengths before graph replay. work equals the retained provider work plus the layout-source canonical preimage bytes; working is max(provider working,64KiB); retained is provider retained plus 4KiB and receipt strings. The provider already retains the graph, outline and font lineage, so OutlineSet retained bytes are not charged twice. Counts and arithmetic are checked uint64 values and are logical portable charges, not allocator, GC or RSS guarantees.",
	OwnershipSemantics:       "LayoutSource retains its immutable ShapeProvider trust root and the exact OutlineSet already retained by it. New glyph and command views contain only a LayoutSource pointer plus scalar indices; they never return existing OutlineSet views, slices, backing arrays, raw font/table bytes, readers, Font objects or go-text types. Point returns a value copy. Successful sources support concurrent reads without mutable caches.",
	CancellationSemantics:    "A non-nil context is checked before work, throughout provider hashing and complete LayoutGraph replay, during final hashing and immediately before publication. Cancellation returns nil and a strictly zero receipt. The bridge itself performs no non-preemptible shaping call.",
	FailureSemantics:         "Nil or drifted providers, dependency drift, graph/outline/font/metric/count mutation, receipt or hash mismatch, invalid limits, overflow, resource exhaustion, cancellation, allocation panic and invariant failure fail closed with nil LayoutSource and a zero receipt.",
	CapabilitySemantics:      "This bridge exposes only receipt-bound font-space outline evidence for a later host-owned text raster provider. It does not rasterize, paint, composite, encode or advertise cutly.text.pinned-font-viewport-px.v2; final Text capability remains disabled.",
	ExcludedResponsibilities: "Raw font access, system fonts, fallback, bidi inference, shaping, glyph selection, textlayout assembly, viewport scaling, hinting, curve flattening, coverage rasterization, paint, compositing, encoding, signing and persistence are outside this profile.",
	DefaultLimits:            DefaultLayoutSourceLimits(),
}

func LayoutSourceDescriptor() LayoutSourceProfileDescriptor { return layoutSourceDescriptorV1 }

func LayoutSourceDescriptorCanonicalJSON() []byte {
	encoded, err := json.Marshal(layoutSourceDescriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), encoded...)
}

var layoutSourceDescriptorIdentityV1 = computeLayoutSourceDescriptorHash()

func LayoutSourceDescriptorHash() string { return layoutSourceDescriptorIdentityV1 }

func computeLayoutSourceDescriptorHash() string {
	canonical := LayoutSourceDescriptorCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(LayoutSourceDescriptorHashProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(LayoutSourceDescriptorHashProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func normalizeLayoutSourceLimits(value LayoutSourceLimits) (LayoutSourceLimits, error) {
	defaults := DefaultLayoutSourceLimits()
	fields := []struct {
		value *uint64
		def   uint64
		hard  uint64
	}{
		{&value.MaxGlyphs, defaults.MaxGlyphs, HardMaxLayoutSourceGlyphs},
		{&value.MaxContours, defaults.MaxContours, HardMaxLayoutSourceContours},
		{&value.MaxCommands, defaults.MaxCommands, HardMaxLayoutSourceCommands},
		{&value.MaxCommandPointOccurrences, defaults.MaxCommandPointOccurrences, HardMaxLayoutSourceCommandPointOccurrences},
		{&value.MaxCommandsPerGlyph, defaults.MaxCommandsPerGlyph, HardMaxLayoutSourceCommandsPerGlyph},
		{&value.MaxCanonicalBinaryBytes, defaults.MaxCanonicalBinaryBytes, HardMaxLayoutSourceCanonicalBytes},
		{&value.MaxWorkUnits, defaults.MaxWorkUnits, HardMaxLayoutSourceWorkUnits},
		{&value.MaxWorkingBytes, defaults.MaxWorkingBytes, HardMaxLayoutSourceWorkingBytes},
		{&value.MaxRetainedBytes, defaults.MaxRetainedBytes, HardMaxLayoutSourceRetainedBytes},
	}
	for _, field := range fields {
		if *field.value == 0 {
			*field.value = field.def
		}
		if *field.value > field.hard {
			return LayoutSourceLimits{}, fontError(CodeLayoutSourceLimits, "layout_source/limits", ErrLimitsInvalid)
		}
	}
	return value, nil
}
