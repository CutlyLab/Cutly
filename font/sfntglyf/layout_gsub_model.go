package sfntglyf

type gsubFoundation struct {
	majorVersion uint16
	minorVersion uint16
	scripts      []layoutScriptRecord
	features     []layoutFeatureRecord
	lookups      []*gsubLookup
	logical      gsubLogicalLookupGraph
	nodes        []layoutNodeEvidence
	edges        []layoutEdgeEvidence
}

type gsubLookup struct {
	lookupType       uint16
	flags            uint16
	markFilteringSet *uint16
	subtables        []gsubSubtable
}

type gsubSubtable interface {
	gsubLookupType() uint16
}

type gsubSingle struct {
	format      uint16
	coverage    *gdefCoverage
	delta       int16
	substitutes []uint16
}

func (*gsubSingle) gsubLookupType() uint16 { return 1 }

type gsubMultiple struct {
	coverage  *gdefCoverage
	sequences []*gsubSequence
}

func (*gsubMultiple) gsubLookupType() uint16 { return 2 }

type gsubSequence struct {
	glyphs []uint16
}

type gsubAlternate struct {
	coverage *gdefCoverage
	sets     []*gsubAlternateSet
}

func (*gsubAlternate) gsubLookupType() uint16 { return 3 }

type gsubAlternateSet struct {
	glyphs []uint16
}

type gsubLigatureSubst struct {
	coverage *gdefCoverage
	sets     []*gsubLigatureSet
}

func (*gsubLigatureSubst) gsubLookupType() uint16 { return 4 }

type gsubLigatureSet struct {
	ligatures []*gsubLigature
}

type gsubLigature struct {
	glyph      uint16
	components []uint16
}

type gsubSequenceLookupRecord struct {
	sequenceIndex uint16
	lookupIndex   uint16
}

type gsubContext struct {
	format        uint16
	coverage      *gdefCoverage
	ruleSets      []*gsubSequenceRuleSet
	classDef      *layoutClassDef
	classRuleSets []*gsubClassSequenceRuleSet
	coverages     []*gdefCoverage
	records       []gsubSequenceLookupRecord
}

func (*gsubContext) gsubLookupType() uint16 { return 5 }

type gsubSequenceRuleSet struct {
	rules []*gsubSequenceRule
}

type gsubSequenceRule struct {
	inputGlyphs []uint16
	records     []gsubSequenceLookupRecord
}

type gsubClassSequenceRuleSet struct {
	rules []*gsubClassSequenceRule
}

type gsubClassSequenceRule struct {
	inputClasses []uint16
	records      []gsubSequenceLookupRecord
}

type gsubChainedContext struct {
	format             uint16
	coverage           *gdefCoverage
	ruleSets           []*gsubChainedRuleSet
	backtrackClassDef  *layoutClassDef
	inputClassDef      *layoutClassDef
	lookaheadClassDef  *layoutClassDef
	classRuleSets      []*gsubChainedClassRuleSet
	backtrackCoverages []*gdefCoverage
	inputCoverages     []*gdefCoverage
	lookaheadCoverages []*gdefCoverage
	records            []gsubSequenceLookupRecord
}

func (*gsubChainedContext) gsubLookupType() uint16 { return 6 }

type gsubChainedRuleSet struct {
	rules []*gsubChainedRule
}

type gsubChainedRule struct {
	backtrackGlyphs []uint16
	inputGlyphs     []uint16
	lookaheadGlyphs []uint16
	records         []gsubSequenceLookupRecord
}

type gsubChainedClassRuleSet struct {
	rules []*gsubChainedClassRule
}

type gsubChainedClassRule struct {
	backtrackClasses []uint16
	inputClasses     []uint16
	lookaheadClasses []uint16
	records          []gsubSequenceLookupRecord
}

type gsubExtension struct {
	extensionLookupType uint16
	subtable            gsubSubtable
}

func (*gsubExtension) gsubLookupType() uint16 { return 7 }

type gsubReverseChain struct {
	coverage           *gdefCoverage
	backtrackCoverages []*gdefCoverage
	lookaheadCoverages []*gdefCoverage
	substitutes        []uint16
}

func (*gsubReverseChain) gsubLookupType() uint16 { return 8 }

// gsubLogicalLookupGraph is the deterministic, private invocation graph formed
// by contextual SequenceLookupRecords. Edges retain record multiplicity because
// it contributes to bounded expansion even when endpoints are identical.
type gsubLogicalLookupGraph struct {
	edges        []gsubLogicalLookupEdge
	topological  []uint16
	maxDepth     uint32
	maxExpansion uint64
}

type gsubLogicalLookupEdge struct {
	fromLookup    uint16
	toLookup      uint16
	subtable      uint16
	recordOrdinal uint64
}

const gsubLogicalLookupEdgeRetainedBytes uint64 = 16
