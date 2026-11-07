package sfntglyf

type gsubFoundationReceipt struct {
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

	GlyphCount       uint32 `json:"glyph_count"`
	GSUBPresent      bool   `json:"gsub_present"`
	GSUBMajorVersion uint16 `json:"gsub_major_version"`
	GSUBMinorVersion uint16 `json:"gsub_minor_version"`

	TableBytes          uint64 `json:"table_bytes"`
	NodeCount           uint64 `json:"node_count"`
	EdgeCount           uint64 `json:"edge_count"`
	AliasReferenceCount uint64 `json:"alias_reference_count"`
	MaxStructuralDepth  uint32 `json:"max_structural_depth"`
	DirectBytes         uint64 `json:"direct_bytes"`

	CoverageTableCount       uint64 `json:"coverage_table_count"`
	CoverageFormat1Glyphs    uint64 `json:"coverage_format1_glyphs"`
	CoverageRangeRecords     uint64 `json:"coverage_range_records"`
	CoveredGlyphInstances    uint64 `json:"covered_glyph_instances"`
	ClassDefTableCount       uint64 `json:"class_def_table_count"`
	ClassFormat1Values       uint64 `json:"class_format1_values"`
	ClassRangeRecords        uint64 `json:"class_range_records"`
	ClassAssignmentInstances uint64 `json:"class_assignment_instances"`

	OrderedRecords           uint64 `json:"ordered_records"`
	ScriptRecords            uint64 `json:"script_records"`
	ScriptTables             uint64 `json:"script_tables"`
	LangSysRecords           uint64 `json:"lang_sys_records"`
	LangSysTables            uint64 `json:"lang_sys_tables"`
	LangSysFeatureReferences uint64 `json:"lang_sys_feature_references"`
	FeatureRecords           uint64 `json:"feature_records"`
	FeatureTables            uint64 `json:"feature_tables"`
	FeatureLookupReferences  uint64 `json:"feature_lookup_references"`
	FeatureParamTables       uint64 `json:"feature_param_tables"`
	FeatureCharacters        uint64 `json:"feature_characters"`
	LookupReferences         uint64 `json:"lookup_references"`
	LookupTables             uint64 `json:"lookup_tables"`
	SubtableReferences       uint64 `json:"subtable_references"`
	GSUBSubtables            uint64 `json:"gsub_subtables"`

	SequenceReferences        uint64 `json:"sequence_references"`
	SequenceTables            uint64 `json:"sequence_tables"`
	AlternateSetReferences    uint64 `json:"alternate_set_references"`
	AlternateSetTables        uint64 `json:"alternate_set_tables"`
	LigatureSetReferences     uint64 `json:"ligature_set_references"`
	LigatureSetTables         uint64 `json:"ligature_set_tables"`
	LigatureReferences        uint64 `json:"ligature_references"`
	LigatureTables            uint64 `json:"ligature_tables"`
	SubstituteGlyphReferences uint64 `json:"substitute_glyph_references"`
	LigatureComponents        uint64 `json:"ligature_components"`

	ContextRuleSetReferences uint64 `json:"context_rule_set_references"`
	ContextRuleSets          uint64 `json:"context_rule_sets"`
	ContextRuleReferences    uint64 `json:"context_rule_references"`
	ContextRules             uint64 `json:"context_rules"`
	ContextGlyphReferences   uint64 `json:"context_glyph_references"`
	ContextClassReferences   uint64 `json:"context_class_references"`
	SequenceLookupRecords    uint64 `json:"sequence_lookup_records"`
	ExtensionSubtables       uint64 `json:"extension_subtables"`
	ReverseSubtables         uint64 `json:"reverse_subtables"`
	LogicalLookupEdges       uint64 `json:"logical_lookup_edges"`
	LogicalLookupDepth       uint32 `json:"logical_lookup_depth"`
	LogicalExpansion         uint64 `json:"logical_expansion"`

	CanonicalBinaryBytes uint64 `json:"canonical_binary_bytes"`
	WorkUnits            uint64 `json:"work_units"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}

// gsubValidationFoundation remains a private validation artifact. It neither
// implements nor implies lookup execution or public shaping capability.
type gsubValidationFoundation struct {
	parent  *layoutFoundation
	gsub    *gsubFoundation
	receipt gsubFoundationReceipt
}

func makeGSUBFoundationReceipt(parent *layoutFoundation, source layoutSourceBinding,
	tables [3]layoutTableBinding, gsub *gsubFoundation, stats layoutDecodeStats,
	foundationHash string, canonicalBytes uint64) gsubFoundationReceipt {
	result := gsubFoundationReceipt{
		SchemaVersion: gsubFoundationSchemaVersion, Profile: gsubFoundationReceiptProfileV1,
		ParentProfile:               parent.receipt.Profile,
		ParentDescriptorHashProfile: parent.receipt.DescriptorHashProfile,
		ParentDescriptorHash:        parent.receipt.DescriptorHash,
		ParentFoundationHashProfile: parent.receipt.FoundationHashProfile,
		ParentFoundationHash:        parent.receipt.FoundationHash,
		SourceProfile:               source.outlineSetProfile,
		SourceOutlineSetHashProfile: source.outlineSetHashProfile,
		SourceOutlineSetHash:        source.outlineSetHash,
		UpstreamContentHashProfile:  source.fontContentHashProfile,
		UpstreamContentHash:         source.fontContentHash,
		DescriptorHashProfile:       gsubFoundationDescriptorHashV1,
		DescriptorHash:              gsubFoundationDescriptorIdentityV1,
		FoundationHashProfile:       gsubFoundationCanonicalHashV1,
		FoundationHash:              foundationHash,
		CapabilityStatus:            gsubFoundationCapabilityStatusV1,
		VariationStatus:             gsubFoundationVariationStatusV1,
		GDEFStatus:                  gsubFoundationGDEFStatusV1,
		GPOSStatus:                  gsubFoundationGPOSStatusV1,
		LogicalGraphStatus:          gsubFoundationLogicalGraphStatusV1,
		GlyphCount:                  source.glyphCount,
		TableBytes:                  stats.tableBytes, NodeCount: stats.nodes, EdgeCount: stats.edges,
		AliasReferenceCount: stats.aliasReferences, MaxStructuralDepth: stats.maxStructuralDepth,
		DirectBytes:        stats.directBytes,
		CoverageTableCount: stats.coverageTables, CoverageFormat1Glyphs: stats.coverageFormat1Glyphs,
		CoverageRangeRecords: stats.coverageRanges, CoveredGlyphInstances: stats.coverageGlyphs,
		ClassDefTableCount: stats.classDefTables, ClassFormat1Values: stats.classFormat1Values,
		ClassRangeRecords: stats.classRanges, ClassAssignmentInstances: stats.classAssignments,
		OrderedRecords: stats.orderedRecords, ScriptRecords: stats.scriptRecords, ScriptTables: stats.scriptTables,
		LangSysRecords: stats.langSysRecords, LangSysTables: stats.langSysTables,
		LangSysFeatureReferences: stats.langSysFeatureReferences,
		FeatureRecords:           stats.featureRecords, FeatureTables: stats.featureTables,
		FeatureLookupReferences: stats.featureLookupReferences, FeatureParamTables: stats.featureParamTables,
		FeatureCharacters: stats.featureCharacters, LookupReferences: stats.lookupReferences,
		LookupTables: stats.lookupTables, SubtableReferences: stats.subtableReferences,
		GSUBSubtables: stats.gsubSubtables, SequenceReferences: stats.sequenceReferences,
		SequenceTables: stats.sequenceTables, AlternateSetReferences: stats.alternateSetReferences,
		AlternateSetTables: stats.alternateSetTables, LigatureSetReferences: stats.ligatureSetReferences,
		LigatureSetTables: stats.ligatureSetTables, LigatureReferences: stats.ligatureReferences,
		LigatureTables: stats.ligatureTables, SubstituteGlyphReferences: stats.substituteGlyphReferences,
		LigatureComponents:       stats.ligatureComponents,
		ContextRuleSetReferences: stats.contextRuleSetReferences, ContextRuleSets: stats.contextRuleSets,
		ContextRuleReferences: stats.contextRuleReferences, ContextRules: stats.contextRules,
		ContextGlyphReferences: stats.contextGlyphReferences, ContextClassReferences: stats.contextClassReferences,
		SequenceLookupRecords: stats.sequenceLookupRecords, ExtensionSubtables: stats.extensionSubtables,
		ReverseSubtables: stats.reverseSubtables, LogicalLookupEdges: stats.logicalLookupEdges,
		LogicalLookupDepth: stats.logicalLookupDepth, LogicalExpansion: stats.logicalExpansion,
		CanonicalBinaryBytes: canonicalBytes,
	}
	if gsub == nil {
		result.GSUBStatus = gsubFoundationGSUBAbsentStatusV1
	} else {
		result.GSUBPresent = true
		result.GSUBMajorVersion = gsub.majorVersion
		result.GSUBMinorVersion = gsub.minorVersion
		result.GSUBStatus = gsubFoundationGSUBValidatedStatusV1
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

func gsubFoundationStatsFromReceipt(value gsubFoundationReceipt) layoutDecodeStats {
	return layoutDecodeStats{
		tableBytes: value.TableBytes, gsubBytes: value.TableBytes,
		nodes: value.NodeCount, edges: value.EdgeCount, aliasReferences: value.AliasReferenceCount,
		maxStructuralDepth: value.MaxStructuralDepth, directBytes: value.DirectBytes,
		coverageTables: value.CoverageTableCount, coverageFormat1Glyphs: value.CoverageFormat1Glyphs,
		coverageRanges: value.CoverageRangeRecords, coverageGlyphs: value.CoveredGlyphInstances,
		classDefTables: value.ClassDefTableCount, classFormat1Values: value.ClassFormat1Values,
		classRanges: value.ClassRangeRecords, classAssignments: value.ClassAssignmentInstances,
		orderedRecords: value.OrderedRecords, scriptRecords: value.ScriptRecords, scriptTables: value.ScriptTables,
		langSysRecords: value.LangSysRecords, langSysTables: value.LangSysTables,
		langSysFeatureReferences: value.LangSysFeatureReferences,
		featureRecords:           value.FeatureRecords, featureTables: value.FeatureTables,
		featureLookupReferences: value.FeatureLookupReferences, featureParamTables: value.FeatureParamTables,
		featureCharacters: value.FeatureCharacters, lookupReferences: value.LookupReferences,
		lookupTables: value.LookupTables, subtableReferences: value.SubtableReferences,
		gsubSubtables: value.GSUBSubtables, sequenceReferences: value.SequenceReferences,
		sequenceTables: value.SequenceTables, alternateSetReferences: value.AlternateSetReferences,
		alternateSetTables: value.AlternateSetTables, ligatureSetReferences: value.LigatureSetReferences,
		ligatureSetTables: value.LigatureSetTables, ligatureReferences: value.LigatureReferences,
		ligatureTables: value.LigatureTables, substituteGlyphReferences: value.SubstituteGlyphReferences,
		ligatureComponents:       value.LigatureComponents,
		contextRuleSetReferences: value.ContextRuleSetReferences, contextRuleSets: value.ContextRuleSets,
		contextRuleReferences: value.ContextRuleReferences, contextRules: value.ContextRules,
		contextGlyphReferences: value.ContextGlyphReferences, contextClassReferences: value.ContextClassReferences,
		sequenceLookupRecords: value.SequenceLookupRecords, extensionSubtables: value.ExtensionSubtables,
		reverseSubtables: value.ReverseSubtables, logicalLookupEdges: value.LogicalLookupEdges,
		logicalLookupDepth: value.LogicalLookupDepth, logicalExpansion: value.LogicalExpansion,
	}
}

func gsubFoundationReceiptStringBytes(value gsubFoundationReceipt) uint64 {
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
