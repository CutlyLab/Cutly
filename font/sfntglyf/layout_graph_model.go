package sfntglyf

// LayoutTableTag names one of the three atomically validated OpenType layout
// tables. LayoutGraph.Table accepts only these values.
type LayoutTableTag string

const (
	LayoutTableGDEF LayoutTableTag = "GDEF"
	LayoutTableGSUB LayoutTableTag = "GSUB"
	LayoutTableGPOS LayoutTableTag = "GPOS"
)

type LayoutGraphTableReceipt struct {
	Tag         LayoutTableTag `json:"tag"`
	Present     bool           `json:"present"`
	FileOffset  uint32         `json:"file_offset"`
	Length      uint32         `json:"length"`
	Checksum    uint32         `json:"checksum"`
	HashProfile string         `json:"hash_profile"`
	ContentHash string         `json:"content_hash"`

	MajorVersion uint16 `json:"major_version"`
	MinorVersion uint16 `json:"minor_version"`
	ScriptCount  uint64 `json:"script_count"`
	FeatureCount uint64 `json:"feature_count"`
	LookupCount  uint64 `json:"lookup_count"`
	NodeCount    uint64 `json:"node_count"`
	EdgeCount    uint64 `json:"edge_count"`
	LogicalEdges uint64 `json:"logical_edges"`
	LogicalDepth uint32 `json:"logical_depth"`
	Expansion    uint64 `json:"logical_expansion"`
}

// LayoutGraphReceipt binds the public graph to the complete immutable font
// lineage and each independently canonical private validation stage.
type LayoutGraphReceipt struct {
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
	GraphHashProfile      string `json:"graph_hash_profile"`
	GraphHash             string `json:"graph_hash"`

	GDEFFoundationHashProfile string `json:"gdef_foundation_hash_profile"`
	GDEFFoundationHash        string `json:"gdef_foundation_hash"`
	GSUBFoundationHashProfile string `json:"gsub_foundation_hash_profile"`
	GSUBFoundationHash        string `json:"gsub_foundation_hash"`
	GPOSFoundationHashProfile string `json:"gpos_foundation_hash_profile"`
	GPOSFoundationHash        string `json:"gpos_foundation_hash"`

	ValidationProfile     string `json:"validation_profile"`
	CapabilityStatus      string `json:"capability_status"`
	LookupExecutionStatus string `json:"lookup_execution_status"`
	VariableLayoutStatus  string `json:"variable_layout_status"`

	Tables [3]LayoutGraphTableReceipt `json:"tables"`

	GlyphCount                     uint32 `json:"glyph_count"`
	ValidationCanonicalBinaryBytes uint64 `json:"validation_canonical_binary_bytes"`
	CanonicalBinaryBytes           uint64 `json:"canonical_binary_bytes"`
	WorkUnits                      uint64 `json:"work_units"`
	WorkingBytes                   uint64 `json:"working_bytes"`
	RetainedBytes                  uint64 `json:"retained_bytes"`
}

// LayoutGraph owns the terminal validation foundation. Its fields remain
// private so callers can only observe immutable scalar views.
type LayoutGraph struct {
	terminal *gposValidationFoundation
	receipt  LayoutGraphReceipt
}

func (value *LayoutGraph) Receipt() LayoutGraphReceipt {
	if value == nil {
		return LayoutGraphReceipt{}
	}
	return value.receipt
}

func (value *LayoutGraph) GlyphCount() uint32 {
	if value == nil {
		return 0
	}
	return value.receipt.GlyphCount
}

type LayoutTableView struct {
	graph *LayoutGraph
	index uint8
}

func (value *LayoutGraph) Table(tag LayoutTableTag) (LayoutTableView, bool) {
	if value == nil || value.terminal == nil {
		return LayoutTableView{}, false
	}
	index := uint8(0)
	switch tag {
	case LayoutTableGDEF:
		index = 0
	case LayoutTableGSUB:
		index = 1
	case LayoutTableGPOS:
		index = 2
	default:
		return LayoutTableView{}, false
	}
	return LayoutTableView{graph: value, index: index}, true
}

func (value LayoutTableView) valid() bool {
	return value.graph != nil && value.graph.terminal != nil && value.index < 3
}

func (value LayoutTableView) Receipt() LayoutGraphTableReceipt {
	if !value.valid() {
		return LayoutGraphTableReceipt{}
	}
	return value.graph.receipt.Tables[value.index]
}

func (value LayoutTableView) Tag() LayoutTableTag  { return value.Receipt().Tag }
func (value LayoutTableView) Present() bool        { return value.Receipt().Present }
func (value LayoutTableView) MajorVersion() uint16 { return value.Receipt().MajorVersion }
func (value LayoutTableView) MinorVersion() uint16 { return value.Receipt().MinorVersion }

func (value LayoutTableView) gdef() *gdefFoundation {
	if !value.valid() || value.index != 0 || value.graph.terminal.parent == nil || value.graph.terminal.parent.parent == nil {
		return nil
	}
	result, _ := value.graph.terminal.parent.parent.gdef.(*gdefFoundation)
	return result
}

func (value LayoutTableView) gsub() *gsubFoundation {
	if !value.valid() || value.index != 1 || value.graph.terminal.parent == nil {
		return nil
	}
	return value.graph.terminal.parent.gsub
}

func (value LayoutTableView) gpos() *gposFoundation {
	if !value.valid() || value.index != 2 {
		return nil
	}
	return value.graph.terminal.gpos
}

func (value LayoutTableView) scriptRecords() []layoutScriptRecord {
	if typed := value.gsub(); typed != nil {
		return typed.scripts
	}
	if typed := value.gpos(); typed != nil {
		return typed.scripts
	}
	return nil
}

func (value LayoutTableView) featureRecords() []layoutFeatureRecord {
	if typed := value.gsub(); typed != nil {
		return typed.features
	}
	if typed := value.gpos(); typed != nil {
		return typed.features
	}
	return nil
}

func (value LayoutTableView) ScriptCount() uint64 { return uint64(len(value.scriptRecords())) }

func (value LayoutTableView) Script(index uint64) (LayoutScriptView, bool) {
	records := value.scriptRecords()
	if index >= uint64(len(records)) {
		return LayoutScriptView{}, false
	}
	return LayoutScriptView{record: &records[index]}, true
}

func (value LayoutTableView) FeatureCount() uint64 { return uint64(len(value.featureRecords())) }

func (value LayoutTableView) Feature(index uint64) (LayoutFeatureView, bool) {
	records := value.featureRecords()
	if index >= uint64(len(records)) {
		return LayoutFeatureView{}, false
	}
	return LayoutFeatureView{record: &records[index]}, true
}

func (value LayoutTableView) LookupCount() uint64 {
	if typed := value.gsub(); typed != nil {
		return uint64(len(typed.lookups))
	}
	if typed := value.gpos(); typed != nil {
		return uint64(len(typed.lookups))
	}
	return 0
}

func (value LayoutTableView) Lookup(index uint64) (LayoutLookupView, bool) {
	if index >= value.LookupCount() {
		return LayoutLookupView{}, false
	}
	return LayoutLookupView{table: value, index: index}, true
}

func (value LayoutTableView) nodeEvidence() []layoutNodeEvidence {
	if typed := value.gdef(); typed != nil {
		return typed.nodes
	}
	if typed := value.gsub(); typed != nil {
		return typed.nodes
	}
	if typed := value.gpos(); typed != nil {
		return typed.nodes
	}
	return nil
}

func (value LayoutTableView) edgeEvidence() []layoutEdgeEvidence {
	if typed := value.gdef(); typed != nil {
		return typed.edges
	}
	if typed := value.gsub(); typed != nil {
		return typed.edges
	}
	if typed := value.gpos(); typed != nil {
		return typed.edges
	}
	return nil
}

func (value LayoutTableView) logicalGraph() gsubLogicalLookupGraph {
	if typed := value.gsub(); typed != nil {
		return typed.logical
	}
	if typed := value.gpos(); typed != nil {
		return typed.logical
	}
	return gsubLogicalLookupGraph{}
}

func (value LayoutTableView) NodeCount() uint64 { return uint64(len(value.nodeEvidence())) }

func (value LayoutTableView) Node(index uint64) (LayoutNodeView, bool) {
	nodes := value.nodeEvidence()
	if index >= uint64(len(nodes)) {
		return LayoutNodeView{}, false
	}
	return LayoutNodeView{evidence: nodes[index]}, true
}

func (value LayoutTableView) EdgeCount() uint64 { return uint64(len(value.edgeEvidence())) }

func (value LayoutTableView) Edge(index uint64) (LayoutEdgeView, bool) {
	edges := value.edgeEvidence()
	if index >= uint64(len(edges)) {
		return LayoutEdgeView{}, false
	}
	return LayoutEdgeView{evidence: edges[index]}, true
}

func (value LayoutTableView) LogicalEdgeCount() uint64 {
	return uint64(len(value.logicalGraph().edges))
}

func (value LayoutTableView) LogicalEdge(index uint64) (LayoutLogicalEdgeView, bool) {
	edges := value.logicalGraph().edges
	if index >= uint64(len(edges)) {
		return LayoutLogicalEdgeView{}, false
	}
	return LayoutLogicalEdgeView{edge: edges[index]}, true
}

func (value LayoutTableView) TopologicalLookupCount() uint64 {
	return uint64(len(value.logicalGraph().topological))
}

func (value LayoutTableView) TopologicalLookup(index uint64) (uint16, bool) {
	order := value.logicalGraph().topological
	if index >= uint64(len(order)) {
		return 0, false
	}
	return order[index], true
}

func (value LayoutTableView) LogicalDepth() uint32     { return value.logicalGraph().maxDepth }
func (value LayoutTableView) LogicalExpansion() uint64 { return value.logicalGraph().maxExpansion }

// GlyphClass and MarkAttachmentClass expose the normalized GDEF class values.
// Class zero is a valid result; ok distinguishes a GDEF view and glyph domain.
func (value LayoutTableView) GlyphClass(glyphID uint32) (class uint16, ok bool) {
	gdef := value.gdef()
	if gdef == nil || value.graph == nil || glyphID >= value.graph.receipt.GlyphCount {
		return 0, false
	}
	return gdef.glyphClasses.classAt(glyphID), true
}

func (value LayoutTableView) MarkAttachmentClass(glyphID uint32) (class uint16, ok bool) {
	gdef := value.gdef()
	if gdef == nil || value.graph == nil || glyphID >= value.graph.receipt.GlyphCount {
		return 0, false
	}
	return gdef.markAttachClasses.classAt(glyphID), true
}

type LayoutTag [4]byte

func layoutPublicTag(value uint32) LayoutTag {
	return LayoutTag{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}
}

func (value LayoutTag) String() string { return string(value[:]) }

type LayoutScriptView struct{ record *layoutScriptRecord }

func (value LayoutScriptView) Tag() LayoutTag {
	if value.record == nil {
		return LayoutTag{}
	}
	return layoutPublicTag(value.record.tag)
}

func (value LayoutScriptView) DefaultLanguage() (LayoutLanguageView, bool) {
	if value.record == nil || value.record.script == nil || value.record.script.defaultLanguage == nil {
		return LayoutLanguageView{}, false
	}
	return LayoutLanguageView{language: value.record.script.defaultLanguage, defaultLanguage: true}, true
}

func (value LayoutScriptView) LanguageCount() uint64 {
	if value.record == nil || value.record.script == nil {
		return 0
	}
	return uint64(len(value.record.script.languages))
}

func (value LayoutScriptView) Language(index uint64) (LayoutLanguageView, bool) {
	if value.record == nil || value.record.script == nil || index >= uint64(len(value.record.script.languages)) {
		return LayoutLanguageView{}, false
	}
	record := &value.record.script.languages[index]
	return LayoutLanguageView{tag: record.tag, language: record.language}, true
}

type LayoutLanguageView struct {
	tag             uint32
	language        *layoutLangSys
	defaultLanguage bool
}

func (value LayoutLanguageView) IsDefault() bool {
	return value.language != nil && value.defaultLanguage
}
func (value LayoutLanguageView) Tag() LayoutTag { return layoutPublicTag(value.tag) }

func (value LayoutLanguageView) RequiredFeatureIndex() (uint16, bool) {
	if value.language == nil || value.language.requiredFeatureIndex == 0xffff {
		return 0, false
	}
	return value.language.requiredFeatureIndex, true
}

func (value LayoutLanguageView) FeatureCount() uint64 {
	if value.language == nil {
		return 0
	}
	return uint64(len(value.language.featureIndices))
}

func (value LayoutLanguageView) FeatureIndex(index uint64) (uint16, bool) {
	if value.language == nil || index >= uint64(len(value.language.featureIndices)) {
		return 0, false
	}
	return value.language.featureIndices[index], true
}

type LayoutFeatureParameterKind string

const (
	LayoutFeatureParametersNone      LayoutFeatureParameterKind = "none"
	LayoutFeatureParametersCharacter LayoutFeatureParameterKind = "character_variant"
	LayoutFeatureParametersStylistic LayoutFeatureParameterKind = "stylistic_set"
	LayoutFeatureParametersSize      LayoutFeatureParameterKind = "optical_size"
)

type LayoutFeatureView struct{ record *layoutFeatureRecord }

func (value LayoutFeatureView) Tag() LayoutTag {
	if value.record == nil {
		return LayoutTag{}
	}
	return layoutPublicTag(value.record.tag)
}

func (value LayoutFeatureView) LookupCount() uint64 {
	if value.record == nil || value.record.feature == nil {
		return 0
	}
	return uint64(len(value.record.feature.lookupIndices))
}

func (value LayoutFeatureView) LookupIndex(index uint64) (uint16, bool) {
	if value.record == nil || value.record.feature == nil || index >= uint64(len(value.record.feature.lookupIndices)) {
		return 0, false
	}
	return value.record.feature.lookupIndices[index], true
}

func (value LayoutFeatureView) ParameterKind() LayoutFeatureParameterKind {
	if value.record == nil || value.record.params == nil {
		return LayoutFeatureParametersNone
	}
	switch value.record.params.(type) {
	case *layoutCVFeatureParams:
		return LayoutFeatureParametersCharacter
	case *layoutSSFeatureParams:
		return LayoutFeatureParametersStylistic
	case *layoutSizeFeatureParams:
		return LayoutFeatureParametersSize
	default:
		return LayoutFeatureParametersNone
	}
}

type LayoutLookupView struct {
	table LayoutTableView
	index uint64
}

func (value LayoutLookupView) Index() uint64 { return value.index }

func (value LayoutLookupView) Type() uint16 {
	if typed := value.table.gsub(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return typed.lookups[value.index].lookupType
	}
	if typed := value.table.gpos(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return typed.lookups[value.index].lookupType
	}
	return 0
}

func (value LayoutLookupView) Flags() uint16 {
	if typed := value.table.gsub(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return typed.lookups[value.index].flags
	}
	if typed := value.table.gpos(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return typed.lookups[value.index].flags
	}
	return 0
}

func (value LayoutLookupView) MarkFilteringSet() (uint16, bool) {
	if typed := value.table.gsub(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil && typed.lookups[value.index].markFilteringSet != nil {
		return *typed.lookups[value.index].markFilteringSet, true
	}
	if typed := value.table.gpos(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil && typed.lookups[value.index].markFilteringSet != nil {
		return *typed.lookups[value.index].markFilteringSet, true
	}
	return 0, false
}

func (value LayoutLookupView) SubtableCount() uint64 {
	if typed := value.table.gsub(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return uint64(len(typed.lookups[value.index].subtables))
	}
	if typed := value.table.gpos(); typed != nil && value.index < uint64(len(typed.lookups)) && typed.lookups[value.index] != nil {
		return uint64(len(typed.lookups[value.index].subtables))
	}
	return 0
}

type LayoutNodeKind uint16

func (value LayoutNodeKind) String() string { return layoutSpanKind(value).String() }

type LayoutNodeView struct{ evidence layoutNodeEvidence }

func (value LayoutNodeView) TableTag() LayoutTag     { return layoutPublicTag(value.evidence.tableTag) }
func (value LayoutNodeView) Start() uint64           { return value.evidence.start }
func (value LayoutNodeView) End() uint64             { return value.evidence.end }
func (value LayoutNodeView) Kind() LayoutNodeKind    { return LayoutNodeKind(value.evidence.kind) }
func (value LayoutNodeView) ParameterFirst() uint64  { return value.evidence.paramFirst }
func (value LayoutNodeView) ParameterSecond() uint64 { return value.evidence.paramSecond }

type LayoutEdgeRole uint16

func (value LayoutEdgeRole) String() string {
	switch layoutEdgeRole(value) {
	case layoutEdgeGlyphClassDef:
		return "glyph_class_def"
	case layoutEdgeAttachList:
		return "attach_list"
	case layoutEdgeLigCaretList:
		return "lig_caret_list"
	case layoutEdgeMarkAttachClassDef:
		return "mark_attach_class_def"
	case layoutEdgeMarkGlyphSets:
		return "mark_glyph_sets"
	case layoutEdgeAttachCoverage:
		return "attach_coverage"
	case layoutEdgeAttachPoint:
		return "attach_point"
	case layoutEdgeLigCaretCoverage:
		return "lig_caret_coverage"
	case layoutEdgeLigGlyph:
		return "lig_glyph"
	case layoutEdgeCaretValue:
		return "caret_value"
	case layoutEdgeCaretDevice:
		return "caret_device"
	case layoutEdgeMarkSetCoverage:
		return "mark_set_coverage"
	case layoutEdgeGSUBScriptList:
		return "gsub_script_list"
	case layoutEdgeGSUBFeatureList:
		return "gsub_feature_list"
	case layoutEdgeGSUBLookupList:
		return "gsub_lookup_list"
	case layoutEdgeScript:
		return "script"
	case layoutEdgeDefaultLangSys:
		return "default_lang_sys"
	case layoutEdgeLangSys:
		return "lang_sys"
	case layoutEdgeFeature:
		return "feature"
	case layoutEdgeFeatureParams:
		return "feature_params"
	case layoutEdgeLookup:
		return "lookup"
	case layoutEdgeLookupSubtable:
		return "lookup_subtable"
	case layoutEdgeGSUBCoverage:
		return "gsub_coverage"
	case layoutEdgeGSUBSequence:
		return "gsub_sequence"
	case layoutEdgeGSUBAlternateSet:
		return "gsub_alternate_set"
	case layoutEdgeGSUBLigatureSet:
		return "gsub_ligature_set"
	case layoutEdgeGSUBLigature:
		return "gsub_ligature"
	case layoutEdgeGSUBContextRuleSet:
		return "gsub_context_rule_set"
	case layoutEdgeGSUBContextRule:
		return "gsub_context_rule"
	case layoutEdgeGSUBContextClassDef:
		return "gsub_context_class_def"
	case layoutEdgeGSUBChainRuleSet:
		return "gsub_chain_rule_set"
	case layoutEdgeGSUBChainRule:
		return "gsub_chain_rule"
	case layoutEdgeGSUBBacktrackClassDef:
		return "gsub_backtrack_class_def"
	case layoutEdgeGSUBInputClassDef:
		return "gsub_input_class_def"
	case layoutEdgeGSUBLookaheadClassDef:
		return "gsub_lookahead_class_def"
	case layoutEdgeGSUBBacktrackCoverage:
		return "gsub_backtrack_coverage"
	case layoutEdgeGSUBInputCoverage:
		return "gsub_input_coverage"
	case layoutEdgeGSUBLookaheadCoverage:
		return "gsub_lookahead_coverage"
	case layoutEdgeGSUBExtension:
		return "gsub_extension"
	case layoutEdgeGPOSScriptList:
		return "gpos_script_list"
	case layoutEdgeGPOSFeatureList:
		return "gpos_feature_list"
	case layoutEdgeGPOSLookupList:
		return "gpos_lookup_list"
	case layoutEdgeGPOSCoverage:
		return "gpos_coverage"
	case layoutEdgeGPOSPairSet:
		return "gpos_pair_set"
	case layoutEdgeGPOSClassDef1:
		return "gpos_class_def_1"
	case layoutEdgeGPOSClassDef2:
		return "gpos_class_def_2"
	case layoutEdgeGPOSEntryAnchor:
		return "gpos_entry_anchor"
	case layoutEdgeGPOSExitAnchor:
		return "gpos_exit_anchor"
	case layoutEdgeGPOSMarkCoverage:
		return "gpos_mark_coverage"
	case layoutEdgeGPOSBaseCoverage:
		return "gpos_base_coverage"
	case layoutEdgeGPOSLigatureCoverage:
		return "gpos_ligature_coverage"
	case layoutEdgeGPOSMark2Coverage:
		return "gpos_mark_2_coverage"
	case layoutEdgeGPOSMarkArray:
		return "gpos_mark_array"
	case layoutEdgeGPOSBaseArray:
		return "gpos_base_array"
	case layoutEdgeGPOSLigatureArray:
		return "gpos_ligature_array"
	case layoutEdgeGPOSLigatureAttach:
		return "gpos_ligature_attach"
	case layoutEdgeGPOSMark2Array:
		return "gpos_mark_2_array"
	case layoutEdgeGPOSMarkAnchor:
		return "gpos_mark_anchor"
	case layoutEdgeGPOSBaseAnchor:
		return "gpos_base_anchor"
	case layoutEdgeGPOSLigatureAnchor:
		return "gpos_ligature_anchor"
	case layoutEdgeGPOSMark2Anchor:
		return "gpos_mark_2_anchor"
	case layoutEdgeGPOSValueDevice:
		return "gpos_value_device"
	case layoutEdgeGPOSAnchorDevice:
		return "gpos_anchor_device"
	case layoutEdgeGPOSExtension:
		return "gpos_extension"
	default:
		return "unknown"
	}
}

type LayoutEdgeView struct{ evidence layoutEdgeEvidence }

func (value LayoutEdgeView) TableTag() LayoutTag  { return layoutPublicTag(value.evidence.tableTag) }
func (value LayoutEdgeView) ParentStart() uint64  { return value.evidence.parentStart }
func (value LayoutEdgeView) ChildStart() uint64   { return value.evidence.childStart }
func (value LayoutEdgeView) Role() LayoutEdgeRole { return LayoutEdgeRole(value.evidence.role) }
func (value LayoutEdgeView) Ordinal() uint32      { return value.evidence.ordinal }
func (value LayoutEdgeView) RawOffset() uint32    { return value.evidence.rawOffset }

type LayoutLogicalEdgeView struct{ edge gsubLogicalLookupEdge }

func (value LayoutLogicalEdgeView) FromLookup() uint16    { return value.edge.fromLookup }
func (value LayoutLogicalEdgeView) ToLookup() uint16      { return value.edge.toLookup }
func (value LayoutLogicalEdgeView) Subtable() uint16      { return value.edge.subtable }
func (value LayoutLogicalEdgeView) RecordOrdinal() uint64 { return value.edge.recordOrdinal }
