package sfntglyf

type gposFoundationStatsReceipt struct {
	TableBytes           uint64 `json:"table_bytes"`
	NodeCount            uint64 `json:"node_count"`
	EdgeCount            uint64 `json:"edge_count"`
	AliasReferences      uint64 `json:"alias_references"`
	MaxStructuralDepth   uint32 `json:"max_structural_depth"`
	DirectBytes          uint64 `json:"direct_bytes"`
	CoverageTables       uint64 `json:"coverage_tables"`
	CoverageGlyphs       uint64 `json:"coverage_glyphs"`
	CoverageFormat1      uint64 `json:"coverage_format_1_glyphs"`
	CoverageRanges       uint64 `json:"coverage_ranges"`
	ClassDefTables       uint64 `json:"class_def_tables"`
	ClassAssignments     uint64 `json:"class_assignments"`
	ClassFormat1Values   uint64 `json:"class_format_1_values"`
	ClassRanges          uint64 `json:"class_ranges"`
	DeviceTables         uint64 `json:"device_tables"`
	DeviceDeltas         uint64 `json:"device_deltas"`
	DeviceWords          uint64 `json:"device_words"`
	OrderedRecords       uint64 `json:"ordered_records"`
	ScriptRecords        uint64 `json:"script_records"`
	ScriptTables         uint64 `json:"script_tables"`
	LangSysRecords       uint64 `json:"lang_sys_records"`
	LangSysTables        uint64 `json:"lang_sys_tables"`
	LangSysFeatures      uint64 `json:"lang_sys_features"`
	FeatureRecords       uint64 `json:"feature_records"`
	FeatureTables        uint64 `json:"feature_tables"`
	FeatureLookups       uint64 `json:"feature_lookups"`
	FeatureParamTables   uint64 `json:"feature_param_tables"`
	LookupReferences     uint64 `json:"lookup_references"`
	LookupTables         uint64 `json:"lookup_tables"`
	SubtableReferences   uint64 `json:"subtable_references"`
	GPOSSubtables        uint64 `json:"gpos_subtables"`
	ValueRecords         uint64 `json:"value_records"`
	PairSetTables        uint64 `json:"pair_set_tables"`
	PairValueRecords     uint64 `json:"pair_value_records"`
	ClassPairRecords     uint64 `json:"class_pair_records"`
	AnchorTables         uint64 `json:"anchor_tables"`
	AnchorReferences     uint64 `json:"anchor_references"`
	MarkArrayTables      uint64 `json:"mark_array_tables"`
	MarkRecords          uint64 `json:"mark_records"`
	BaseArrayTables      uint64 `json:"base_array_tables"`
	BaseRecords          uint64 `json:"base_records"`
	LigatureArrayTables  uint64 `json:"ligature_array_tables"`
	LigatureAttachTables uint64 `json:"ligature_attach_tables"`
	LigatureComponents   uint64 `json:"ligature_component_records"`
	Mark2ArrayTables     uint64 `json:"mark_2_array_tables"`
	Mark2Records         uint64 `json:"mark_2_records"`
	ContextRuleSetRefs   uint64 `json:"context_rule_set_references"`
	ContextRuleSets      uint64 `json:"context_rule_sets"`
	ContextRuleRefs      uint64 `json:"context_rule_references"`
	ContextRules         uint64 `json:"context_rules"`
	ContextGlyphs        uint64 `json:"context_glyph_references"`
	ContextClasses       uint64 `json:"context_class_references"`
	SequenceLookups      uint64 `json:"sequence_lookup_records"`
	ExtensionSubtables   uint64 `json:"extension_subtables"`
	LogicalEdges         uint64 `json:"logical_lookup_edges"`
	LogicalDepth         uint32 `json:"logical_lookup_depth"`
	LogicalExpansion     uint64 `json:"logical_expansion"`
}

type gposFoundationReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`

	ParentProfile               string `json:"parent_profile"`
	ParentDescriptorHashProfile string `json:"parent_descriptor_hash_profile"`
	ParentDescriptorHash        string `json:"parent_descriptor_hash"`
	ParentFoundationHashProfile string `json:"parent_foundation_hash_profile"`
	ParentFoundationHash        string `json:"parent_foundation_hash"`
	SourceProfile               string `json:"source_profile"`
	SourceOutlineSetHashProfile string `json:"source_outline_set_hash_profile"`
	SourceOutlineSetHash        string `json:"source_outline_set_hash"`
	UpstreamContentHashProfile  string `json:"upstream_content_hash_profile"`
	UpstreamContentHash         string `json:"upstream_content_hash"`

	DescriptorHashProfile string `json:"descriptor_hash_profile"`
	DescriptorHash        string `json:"descriptor_hash"`
	FoundationHashProfile string `json:"foundation_hash_profile"`
	FoundationHash        string `json:"foundation_hash"`

	CapabilityStatus   string `json:"capability_status"`
	VariationStatus    string `json:"variation_status"`
	GDEFStatus         string `json:"gdef_status"`
	GSUBStatus         string `json:"gsub_status"`
	GPOSStatus         string `json:"gpos_status"`
	LogicalGraphStatus string `json:"logical_graph_status"`

	Tables [3]layoutFoundationTableReceipt `json:"tables"`

	GlyphCount       uint32                     `json:"glyph_count"`
	GPOSPresent      bool                       `json:"gpos_present"`
	GPOSMajorVersion uint16                     `json:"gpos_major_version"`
	GPOSMinorVersion uint16                     `json:"gpos_minor_version"`
	Stats            gposFoundationStatsReceipt `json:"stats"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

// gposValidationFoundation is the terminal private validation artifact for
// this phase. It deliberately has no public accessor or shaping method.
type gposValidationFoundation struct {
	parent  *gsubValidationFoundation
	gpos    *gposFoundation
	receipt gposFoundationReceipt
}

func gposStatsReceipt(value layoutDecodeStats) gposFoundationStatsReceipt {
	return gposFoundationStatsReceipt{
		TableBytes: value.tableBytes, NodeCount: value.nodes, EdgeCount: value.edges,
		AliasReferences: value.aliasReferences, MaxStructuralDepth: value.maxStructuralDepth,
		DirectBytes: value.directBytes, CoverageTables: value.coverageTables,
		CoverageGlyphs: value.coverageGlyphs, CoverageFormat1: value.coverageFormat1Glyphs,
		CoverageRanges: value.coverageRanges, ClassDefTables: value.classDefTables,
		ClassAssignments: value.classAssignments, ClassFormat1Values: value.classFormat1Values,
		ClassRanges: value.classRanges, DeviceTables: value.deviceTables,
		DeviceDeltas: value.deviceDeltas, DeviceWords: value.deviceWords,
		OrderedRecords: value.orderedRecords, ScriptRecords: value.scriptRecords,
		ScriptTables: value.scriptTables, LangSysRecords: value.langSysRecords,
		LangSysTables: value.langSysTables, LangSysFeatures: value.langSysFeatureReferences,
		FeatureRecords: value.featureRecords, FeatureTables: value.featureTables,
		FeatureLookups: value.featureLookupReferences, FeatureParamTables: value.featureParamTables,
		LookupReferences: value.lookupReferences, LookupTables: value.lookupTables,
		SubtableReferences: value.subtableReferences, GPOSSubtables: value.gposSubtables,
		ValueRecords: value.valueRecords, PairSetTables: value.pairSetTables,
		PairValueRecords: value.pairValueRecords, ClassPairRecords: value.classPairRecords,
		AnchorTables: value.anchorTables, AnchorReferences: value.anchorReferences,
		MarkArrayTables: value.markArrayTables, MarkRecords: value.markRecords,
		BaseArrayTables: value.baseArrayTables, BaseRecords: value.baseRecords,
		LigatureArrayTables: value.ligatureArrayTables, LigatureAttachTables: value.ligatureAttachTables,
		LigatureComponents: value.ligatureComponentRecords, Mark2ArrayTables: value.mark2ArrayTables,
		Mark2Records: value.mark2Records, ContextRuleSetRefs: value.contextRuleSetReferences,
		ContextRuleSets: value.contextRuleSets, ContextRuleRefs: value.contextRuleReferences,
		ContextRules: value.contextRules, ContextGlyphs: value.contextGlyphReferences,
		ContextClasses: value.contextClassReferences, SequenceLookups: value.sequenceLookupRecords,
		ExtensionSubtables: value.extensionSubtables, LogicalEdges: value.logicalLookupEdges,
		LogicalDepth: value.logicalLookupDepth, LogicalExpansion: value.logicalExpansion,
	}
}

func gposStatsFromReceipt(value gposFoundationStatsReceipt) layoutDecodeStats {
	return layoutDecodeStats{
		tableBytes: value.TableBytes, gposBytes: value.TableBytes,
		nodes: value.NodeCount, edges: value.EdgeCount, aliasReferences: value.AliasReferences,
		maxStructuralDepth: value.MaxStructuralDepth, directBytes: value.DirectBytes,
		coverageTables: value.CoverageTables, coverageGlyphs: value.CoverageGlyphs,
		coverageFormat1Glyphs: value.CoverageFormat1, coverageRanges: value.CoverageRanges,
		classDefTables: value.ClassDefTables, classAssignments: value.ClassAssignments,
		classFormat1Values: value.ClassFormat1Values, classRanges: value.ClassRanges,
		deviceTables: value.DeviceTables, deviceDeltas: value.DeviceDeltas, deviceWords: value.DeviceWords,
		orderedRecords: value.OrderedRecords, scriptRecords: value.ScriptRecords,
		scriptTables: value.ScriptTables, langSysRecords: value.LangSysRecords,
		langSysTables: value.LangSysTables, langSysFeatureReferences: value.LangSysFeatures,
		featureRecords: value.FeatureRecords, featureTables: value.FeatureTables,
		featureLookupReferences: value.FeatureLookups, featureParamTables: value.FeatureParamTables,
		lookupReferences: value.LookupReferences, lookupTables: value.LookupTables,
		subtableReferences: value.SubtableReferences, gposSubtables: value.GPOSSubtables,
		valueRecords: value.ValueRecords, pairSetTables: value.PairSetTables,
		pairValueRecords: value.PairValueRecords, classPairRecords: value.ClassPairRecords,
		anchorTables: value.AnchorTables, anchorReferences: value.AnchorReferences,
		markArrayTables: value.MarkArrayTables, markRecords: value.MarkRecords,
		baseArrayTables: value.BaseArrayTables, baseRecords: value.BaseRecords,
		ligatureArrayTables: value.LigatureArrayTables, ligatureAttachTables: value.LigatureAttachTables,
		ligatureComponentRecords: value.LigatureComponents, mark2ArrayTables: value.Mark2ArrayTables,
		mark2Records: value.Mark2Records, contextRuleSetReferences: value.ContextRuleSetRefs,
		contextRuleSets: value.ContextRuleSets, contextRuleReferences: value.ContextRuleRefs,
		contextRules: value.ContextRules, contextGlyphReferences: value.ContextGlyphs,
		contextClassReferences: value.ContextClasses, sequenceLookupRecords: value.SequenceLookups,
		extensionSubtables: value.ExtensionSubtables, logicalLookupEdges: value.LogicalEdges,
		logicalLookupDepth: value.LogicalDepth, logicalExpansion: value.LogicalExpansion,
	}
}

func makeGPOSFoundationReceipt(parent *gsubValidationFoundation, source layoutSourceBinding,
	tables [3]layoutTableBinding, gpos *gposFoundation, stats layoutDecodeStats,
	foundationHash string, canonicalBytes uint64) gposFoundationReceipt {
	result := gposFoundationReceipt{
		SchemaVersion: gposFoundationSchemaVersion, Profile: gposFoundationReceiptProfileV1,
		ParentProfile:               parent.receipt.Profile,
		ParentDescriptorHashProfile: parent.receipt.DescriptorHashProfile,
		ParentDescriptorHash:        parent.receipt.DescriptorHash,
		ParentFoundationHashProfile: parent.receipt.FoundationHashProfile,
		ParentFoundationHash:        parent.receipt.FoundationHash,
		SourceProfile:               source.outlineSetProfile, SourceOutlineSetHashProfile: source.outlineSetHashProfile,
		SourceOutlineSetHash: source.outlineSetHash, UpstreamContentHashProfile: source.fontContentHashProfile,
		UpstreamContentHash: source.fontContentHash, DescriptorHashProfile: gposFoundationDescriptorHashV1,
		DescriptorHash: gposFoundationDescriptorIdentityV1, FoundationHashProfile: gposFoundationCanonicalHashV1,
		FoundationHash: foundationHash, CapabilityStatus: gposFoundationCapabilityStatusV1,
		VariationStatus: gposFoundationVariationStatusV1, GDEFStatus: gposFoundationGDEFStatusV1,
		GSUBStatus: gposFoundationGSUBStatusV1, LogicalGraphStatus: gposFoundationLogicalGraphStatusV1,
		GlyphCount: source.glyphCount, Stats: gposStatsReceipt(stats), CanonicalBinaryBytes: canonicalBytes,
	}
	if gpos == nil {
		result.GPOSStatus = gposFoundationGPOSAbsentStatusV1
	} else {
		result.GPOSPresent = true
		result.GPOSMajorVersion = gpos.majorVersion
		result.GPOSMinorVersion = gpos.minorVersion
		result.GPOSStatus = gposFoundationGPOSValidatedStatusV1
	}
	for index, table := range tables {
		result.Tables[index] = layoutFoundationTableReceipt{
			Tag: string(table.tag[:]), Present: table.present, FileOffset: table.fileOffset,
			Length: table.length, Checksum: table.checksum,
			HashProfile: table.hashProfile, ContentHash: table.contentHash,
		}
	}
	return result
}

func gposFoundationReceiptStringBytes(value gposFoundationReceipt) uint64 {
	strings := []string{
		value.Profile, value.ParentProfile, value.ParentDescriptorHashProfile, value.ParentDescriptorHash,
		value.ParentFoundationHashProfile, value.ParentFoundationHash, value.SourceProfile,
		value.SourceOutlineSetHashProfile, value.SourceOutlineSetHash, value.UpstreamContentHashProfile,
		value.UpstreamContentHash, value.DescriptorHashProfile, value.DescriptorHash,
		value.FoundationHashProfile, value.FoundationHash, value.CapabilityStatus, value.VariationStatus,
		value.GDEFStatus, value.GSUBStatus, value.GPOSStatus, value.LogicalGraphStatus,
	}
	var total uint64
	for _, text := range strings {
		next, ok := checkedAdd(total, uint64(len(text)))
		if !ok {
			return ^uint64(0)
		}
		total = next
	}
	for _, table := range value.Tables {
		for _, text := range []string{table.Tag, table.HashProfile, table.ContentHash} {
			next, ok := checkedAdd(total, uint64(len(text)))
			if !ok {
				return ^uint64(0)
			}
			total = next
		}
	}
	return total
}
