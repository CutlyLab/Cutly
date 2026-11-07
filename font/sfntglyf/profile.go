package sfntglyf

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.font.sfnt-single-face-static-glyf-core.v1"
	ReceiptSchemaVersion = 1
	ReceiptProfileV1     = "cutly.font.sfnt-single-face-static-glyf-core.receipt.v1"
	ContentHashProfileV1 = "cutly.hash.sha256.font-sfnt-complete-bytes.v1"
	TableHashProfileV1   = "cutly.hash.sha256.font-sfnt-table-bytes.v1"
	CmapProfileV1        = "cutly.font.cmap-unicode-format4-12.v1"
	LayoutTableStatusV1  = "opaque_unvalidated_not_shape_authorized"
	GlyfTableStatusV1    = "opaque_ranges_not_outline_authorized"
	DeclaredStatusV1     = "declared_unverified"
)

const (
	DefaultMaxEncodedBytes  uint64 = 64 << 20
	DefaultMaxTables        uint32 = 128
	DefaultMaxGlyphs        uint32 = 65_535
	DefaultMaxCmapRecords   uint32 = 4
	DefaultMaxCmapGroups    uint64 = 131_072
	DefaultMaxCmapMappings  uint64 = 1_114_112
	DefaultMaxWorkUnits     uint64 = 1 << 32
	DefaultMaxWorkingBytes  uint64 = 256 << 20
	DefaultMaxRetainedBytes uint64 = 128 << 20

	HardMaxEncodedBytes  uint64 = 1 << 30
	HardMaxTables        uint32 = 4095
	HardMaxGlyphs        uint32 = 65_535
	HardMaxCmapRecords   uint32 = 4
	HardMaxCmapGroups    uint64 = 1 << 20
	HardMaxCmapMappings  uint64 = 1 << 22
	HardMaxWorkUnits     uint64 = 1 << 60
	HardMaxWorkingBytes  uint64 = 2 << 30
	HardMaxRetainedBytes uint64 = 2 << 30

	logicalFixedRetainedBytes uint64 = 16 << 10
	logicalTableBytes         uint64 = 256
	logicalMappingBytes       uint64 = 8
	logicalFixedWorkingBytes  uint64 = 64 << 10
)

type Limits struct {
	MaxEncodedBytes  uint64 `json:"max_encoded_bytes"`
	MaxTables        uint32 `json:"max_tables"`
	MaxGlyphs        uint32 `json:"max_glyphs"`
	MaxCmapRecords   uint32 `json:"max_cmap_records"`
	MaxCmapGroups    uint64 `json:"max_cmap_groups"`
	MaxCmapMappings  uint64 `json:"max_cmap_mappings"`
	MaxWorkUnits     uint64 `json:"max_work_units"`
	MaxWorkingBytes  uint64 `json:"max_working_bytes"`
	MaxRetainedBytes uint64 `json:"max_retained_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxEncodedBytes:  DefaultMaxEncodedBytes,
		MaxTables:        DefaultMaxTables,
		MaxGlyphs:        DefaultMaxGlyphs,
		MaxCmapRecords:   DefaultMaxCmapRecords,
		MaxCmapGroups:    DefaultMaxCmapGroups,
		MaxCmapMappings:  DefaultMaxCmapMappings,
		MaxWorkUnits:     DefaultMaxWorkUnits,
		MaxWorkingBytes:  DefaultMaxWorkingBytes,
		MaxRetainedBytes: DefaultMaxRetainedBytes,
	}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ReceiptProfile           string `json:"receipt_profile"`
	ContentHashProfile       string `json:"content_hash_profile"`
	TableHashProfile         string `json:"table_hash_profile"`
	CmapProfile              string `json:"cmap_profile"`
	ContainerSemantics       string `json:"container_semantics"`
	ChecksumSemantics        string `json:"checksum_semantics"`
	AcceptedTables           string `json:"accepted_tables"`
	RejectedTables           string `json:"rejected_tables"`
	CoreTableSemantics       string `json:"core_table_semantics"`
	CmapSemantics            string `json:"cmap_semantics"`
	LayoutTableSemantics     string `json:"layout_table_semantics"`
	OwnershipSemantics       string `json:"ownership_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	ResourceSemantics        string `json:"resource_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	CapabilitySemantics      string `json:"capability_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
	DefaultLimits            Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	ReceiptProfile:           ReceiptProfileV1,
	ContentHashProfile:       ContentHashProfileV1,
	TableHashProfile:         TableHashProfileV1,
	CmapProfile:              CmapProfileV1,
	ContainerSemantics:       "Accept only one complete sfnt whose version is 0x00010000. The table directory must use exact representable binary-search fields and strictly ascending unique printable tags. Every top-level table offset is four-byte aligned after the directory; zero-length tables may share a physical boundary, while padded nonempty ranges are disjoint and fill the complete four-byte-padded file in physical order. Every directory/table gap and physical padding byte is zero. Requiring physical final padding is a canonical profile narrowing. Collections, OTTO/CFF, Apple true/typ1, WOFF/WOFF2 and trailing bytes fail closed.",
	ChecksumSemantics:        "Verify every table checksum as a big-endian uint32 sum with zero padding. For head, checksumAdjustment bytes are treated as zero for the directory checksum. The complete padded font must sum to 0xB1B0AFBA and head.checkSumAdjustment must equal 0xB1B0AFBA minus the complete-font sum computed with that field zero.",
	AcceptedTables:           "Required: cmap,glyf,head,hhea,hmtx,loca,maxp. Optional opaque layout: GDEF,GPOS,GSUB. Optional ignored metadata/hint data: DSIG,OS/2,cvt ,fpgm,gasp,name,post,prep. Every accepted table remains content-hashed; ignored means it has no semantic effect in this gate.",
	RejectedTables:           "All unlisted tags fail closed. This explicitly rejects CFF/CFF2; fvar/avar/gvar/cvar/HVAR/VVAR/MVAR and this profile's unneeded STAT metadata; COLR/CPAL/SVG /sbix/CBDT/CBLC/EBDT/EBLC/EBSC; mort/morx/kerx/ankr/feat/trak/just/lcar/opbd/prop/bsln; and legacy kern. STAT rejection is a profile allowlist decision, not evidence by itself that a font is variable.",
	CoreTableSemantics:       "head/maxp/hhea have exact v1 lengths and versions; reserved fields and cross-table counts are checked. head flags 5 through 10 and 15 are rejected, while historical fontDirectionHint values -2 through 2 are accepted. Requiring lowestRecPPEM>0 and a nonzero hhea caret-slope vector are fail-closed canonical profile narrowings. hmtx has exactly numberOfHMetrics long metrics plus the required trailing bearings, and hhea.advanceWidthMax is verified. Other hhea extrema and head/maxp outline-derived declarations remain declared_unverified. loca has exactly numGlyphs+1 monotone offsets, begins at zero and ends exactly at glyf length; short-form values are doubled and therefore even, while long-form byte offsets may be odd. glyf ranges, including an all-empty zero-length table, remain opaque; no glyph header or body is decoded.",
	CmapSemantics:            "Accept only strictly ordered Unicode records (platform 0 encoding 3/4 or platform 3 encoding 1/10) pointing respectively to format 4/12. Language is zero. Exact subtable aliases are allowed; partial overlaps fail. As a canonical profile narrowing, unreferenced bytes between the record array and referenced subtables, between subtables, or after the last subtable must be zero; all remain content-hashed. Format 4 search fields, sentinel, ordered disjoint segments and idRangeOffset addressing are checked; format 12 groups are exact-length, ordered, scalar-only and nonoverlapping. Every nonzero GID is <numGlyphs. Same-format records must map identically. If format 12 exists, every nonzero format 4 BMP mapping must exist there with the same GID and only format 12 becomes canonical; otherwise format 4 is canonical. Subtables are never merged.",
	LayoutTableSemantics:     "GDEF/GPOS/GSUB are only top-level bounds/checksum/hash validated and are reported as opaque_unvalidated_not_shape_authorized. Font exposes no encoded/table byte view. A later profile must strictly walk their accepted lookup graph and bind the same content hash before any bytes may enter a shaper.",
	OwnershipSemantics:       "Parse borrows encoded bytes until return. Success owns a complete immutable copy plus decoded cmap and receipt metadata; accessors return values or defensive slices. Caller mutation after return cannot affect Font, and concurrent reads are safe.",
	CancellationSemantics:    "A non-nil context is checked before work and throughout directory, padding, checksum, table, loca, cmap decode and hash loops, before owned-copy allocation and immediately before publication. Cancellation returns nil Font and a zero Receipt.",
	ResourceSemantics:        "Counts and every offset/add/multiply are checked in uint64 before int conversion or allocation. work_units is 8 per encoded byte + 64 per sfnt table + 4 per glyph + 32 per cmap record + 16 per segment/group across unique cmap subtables + 2 per mapping slot across unique cmap subtables. receipt segment/group counts and mapping-slot charges likewise deduplicate exact subtable aliases. retained_bytes is a deterministic portable logical charge of 16KiB + encoded bytes + 256 per table + 8 per final canonical cmap mapping. working_bytes is 64KiB + 16 per mapping slot examined across unique cmap subtables. These are portable workload charges, not allocator/GC/RSS measurements; hard process isolation is required for a physical ceiling.",
	FailureSemantics:         "Unknown profiles/containers/tables, malformed offsets, nonzero padding, checksum drift, inconsistent metrics/loca/cmap, resource overflow, cancellation and invariant drift fail closed with nil Font and zero Receipt. No partial or repaired font is returned.",
	CapabilitySemantics:      "This T1b1a gate is parser evidence only. It does not advertise cutly.text.pinned-font-viewport-px.v2 and is not sufficient to construct a shape-provider fingerprint or render evidence.",
	ExcludedResponsibilities: "Simple/compound glyf body decoding, implied points, component transforms, GSUB/GPOS/GDEF validation or execution, HarfBuzz/go-text, RTL traversal canonicalization, textlayout assembly, outline rasterization, paint, fallback, system fonts, TTC/WOFF/CFF/variable/color fonts, signatures and publication are outside this profile.",
	DefaultLimits:            DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
