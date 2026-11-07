package sfntglyf

var layoutFoundationTableTags = [3][4]byte{
	{'G', 'D', 'E', 'F'},
	{'G', 'S', 'U', 'B'},
	{'G', 'P', 'O', 'S'},
}

type layoutTableBinding struct {
	tag         [4]byte
	present     bool
	fileOffset  uint32
	length      uint32
	checksum    uint32
	hashProfile string
	contentHash string
}

// layoutSourceBinding contains only scalar evidence from a completed live
// rebind. The resource fields are execution evidence, not caller claims.
type layoutSourceBinding struct {
	outlineSetProfile               string
	outlineSetDescriptorHashProfile string
	outlineSetDescriptorHash        string
	outlineSetHashProfile           string
	outlineSetHash                  string
	fontContentHashProfile          string
	fontContentHash                 string
	glyphCount                      uint32
	realPointInstances              uint64
	coreWorkUnits                   uint64
	coreWorkingBytes                uint64
	outlineSetWorkUnits             uint64
	outlineSetWorkingBytes          uint64
	outlineSetRetainedBytes         uint64
}

func bindLayoutFoundationSource(set *OutlineSet, evidence outlineSetRebindEvidence) (
	layoutSourceBinding, [3]layoutTableBinding, error) {
	if set == nil || set.source == nil || set.source.source == nil || evidence.font == nil || evidence.outline == nil ||
		evidence.font != set.source.source.font ||
		evidence.contentHash != evidence.font.receipt.ContentHash {
		return layoutSourceBinding{}, [3]layoutTableBinding{}, layoutFoundationHashInvariant("layout_foundation/source/binding")
	}
	binding := layoutSourceBinding{
		outlineSetProfile:               set.receipt.Profile,
		outlineSetDescriptorHashProfile: set.receipt.DescriptorHashProfile,
		outlineSetDescriptorHash:        set.receipt.DescriptorHash,
		outlineSetHashProfile:           set.receipt.OutlineSetHashProfile,
		outlineSetHash:                  set.receipt.OutlineSetHash,
		fontContentHashProfile:          evidence.font.receipt.ContentHashProfile,
		fontContentHash:                 evidence.contentHash,
		glyphCount:                      set.receipt.GlyphCount, realPointInstances: set.receipt.ExpandedPointInstances,
		coreWorkUnits: evidence.coreWorkUnits, coreWorkingBytes: evidence.coreWorkingBytes,
		outlineSetWorkUnits: set.receipt.WorkUnits, outlineSetWorkingBytes: set.receipt.WorkingBytes,
		outlineSetRetainedBytes: set.receipt.RetainedBytes,
	}
	input := [3]outlineSetRebindTableEvidence{evidence.gdef, evidence.gsub, evidence.gpos}
	var tables [3]layoutTableBinding
	for index, table := range input {
		tables[index].tag = layoutFoundationTableTags[index]
		if !table.present {
			continue
		}
		if table.record.tag != layoutFoundationTableTags[index] || !validLayoutFoundationDigest(table.contentHash) {
			return layoutSourceBinding{}, [3]layoutTableBinding{}, layoutFoundationHashInvariant("layout_foundation/source/table_binding")
		}
		tables[index].present = true
		tables[index].fileOffset = table.record.offset
		tables[index].length = table.record.length
		tables[index].checksum = table.record.checksum
		tables[index].hashProfile = TableHashProfileV1
		tables[index].contentHash = table.contentHash
	}
	return binding, tables, nil
}

type layoutFoundationTableReceipt struct {
	Tag         string `json:"tag"`
	Present     bool   `json:"present"`
	FileOffset  uint32 `json:"file_offset"`
	Length      uint32 `json:"length"`
	Checksum    uint32 `json:"checksum"`
	HashProfile string `json:"hash_profile"`
	ContentHash string `json:"content_hash"`
}

// layoutFoundationReceipt remains private and deliberately contains only
// comparable fields. In particular, its fixed table array is map-free and its
// equality can be checked without a custom comparator.
type layoutFoundationReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`

	SourceProfile               string `json:"source_profile"`
	SourceDescriptorHashProfile string `json:"source_descriptor_hash_profile"`
	SourceDescriptorHash        string `json:"source_descriptor_hash"`
	SourceOutlineSetHashProfile string `json:"source_outline_set_hash_profile"`
	SourceOutlineSetHash        string `json:"source_outline_set_hash"`
	UpstreamContentHashProfile  string `json:"upstream_content_hash_profile"`
	UpstreamContentHash         string `json:"upstream_content_hash"`

	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	FoundationHashProfile string `json:"foundation_hash_profile"`
	FoundationHash        string `json:"foundation_hash"`

	LayoutStatus     string `json:"layout_status"`
	CapabilityStatus string `json:"capability_status"`
	VariationStatus  string `json:"variation_status"`
	DeviceStatus     string `json:"device_status"`
	CaretOrderStatus string `json:"caret_order_status"`
	GDEFStatus       string `json:"gdef_status"`
	GSUBStatus       string `json:"gsub_status"`
	GPOSStatus       string `json:"gpos_status"`

	Tables [3]layoutFoundationTableReceipt `json:"tables"`

	GlyphCount         uint32 `json:"glyph_count"`
	RealPointInstances uint64 `json:"real_point_instances"`
	GDEFPresent        bool   `json:"gdef_present"`
	GDEFMajorVersion   uint16 `json:"gdef_major_version"`
	GDEFMinorVersion   uint16 `json:"gdef_minor_version"`

	NodeCount           uint64 `json:"node_count"`
	EdgeCount           uint64 `json:"edge_count"`
	AliasReferenceCount uint64 `json:"alias_reference_count"`
	MaxStructuralDepth  uint32 `json:"max_structural_depth"`
	DirectBytes         uint64 `json:"direct_bytes"`

	CoverageTableCount        uint64 `json:"coverage_table_count"`
	CoverageFormat1Glyphs     uint64 `json:"coverage_format1_glyphs"`
	CoverageRangeRecords      uint64 `json:"coverage_range_records"`
	CoveredGlyphInstances     uint64 `json:"covered_glyph_instances"`
	ClassDefTableCount        uint64 `json:"class_def_table_count"`
	ClassFormat1Values        uint64 `json:"class_format1_values"`
	ClassRangeRecords         uint64 `json:"class_range_records"`
	ClassAssignmentInstances  uint64 `json:"class_assignment_instances"`
	DeviceTableCount          uint64 `json:"device_table_count"`
	DeviceDeltaValues         uint64 `json:"device_delta_values"`
	DeviceWordCount           uint64 `json:"device_word_count"`
	AttachListCount           uint64 `json:"attach_list_count"`
	AttachPointTableCount     uint64 `json:"attach_point_table_count"`
	AttachmentGlyphReferences uint64 `json:"attachment_glyph_references"`
	AttachmentPointIndices    uint64 `json:"attachment_point_indices"`
	LigCaretListCount         uint64 `json:"lig_caret_list_count"`
	LigGlyphTableCount        uint64 `json:"lig_glyph_table_count"`
	LigatureGlyphReferences   uint64 `json:"ligature_glyph_references"`
	CaretValueTableCount      uint64 `json:"caret_value_table_count"`
	CaretValueReferences      uint64 `json:"caret_value_references"`
	CaretPointReferences      uint64 `json:"caret_point_references"`
	MarkGlyphSetsTableCount   uint64 `json:"mark_glyph_sets_table_count"`
	MarkGlyphSetReferences    uint64 `json:"mark_glyph_set_references"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

type layoutFoundationGDEFModel interface {
	layoutFoundationGDEFModel()
}

// layoutFoundation is not an API surface. The final public LayoutGraph model
// is intentionally deferred until complete GSUB and GPOS validation.
type layoutFoundation struct {
	source          *OutlineSet
	sourceBinding   layoutSourceBinding
	tables          [3]layoutTableBinding
	realPointCounts []uint64
	gdef            layoutFoundationGDEFModel
	receipt         layoutFoundationReceipt
}

// The following records are an ephemeral, normalized projection used only by
// the foundation hash. Payload is already kind-specific canonical big-endian
// data; it is streamed and is not retained by layoutFoundation.
type layoutFoundationCanonicalNodeKey struct {
	tag    [4]byte
	offset uint32
	kind   uint16
	param0 uint64
	param1 uint64
}

type layoutFoundationCanonicalNode struct {
	key        layoutFoundationCanonicalNodeKey
	spanStart  uint32
	spanLength uint32
	payload    []byte
}

type layoutFoundationCanonicalEdge struct {
	from      layoutFoundationCanonicalNodeKey
	role      uint16
	ordinal   uint32
	rawOffset uint32
	to        layoutFoundationCanonicalNodeKey
}

type layoutFoundationCanonicalEvidence struct {
	source           layoutSourceBinding
	tables           [3]layoutTableBinding
	gdefPresent      bool
	gdefMajorVersion uint16
	gdefMinorVersion uint16
	gdef             *gdefFoundation
	nodes            []layoutFoundationCanonicalNode
	edges            []layoutFoundationCanonicalEdge
	resources        layoutFoundationResourceStats
}
