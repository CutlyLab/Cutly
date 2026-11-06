package sfntglyf

// gposFoundation is a private, immutable validation model. It deliberately
// carries no lookup executor and cannot shape or mutate a glyph buffer.
type gposFoundation struct {
	majorVersion uint16
	minorVersion uint16
	scripts      []layoutScriptRecord
	features     []layoutFeatureRecord
	lookups      []*gposLookup
	logical      gsubLogicalLookupGraph
	nodes        []layoutNodeEvidence
	edges        []layoutEdgeEvidence
}

type gposLookup struct {
	lookupType       uint16
	flags            uint16
	markFilteringSet *uint16
	subtables        []gposSubtable
}

type gposSubtable interface {
	gposLookupType() uint16
}

type gposValueRecord struct {
	xPlacement int16
	yPlacement int16
	xAdvance   int16
	yAdvance   int16
	xPlaDevice *gdefDevice
	yPlaDevice *gdefDevice
	xAdvDevice *gdefDevice
	yAdvDevice *gdefDevice
}

type gposSingle struct {
	format      uint16
	coverage    *gdefCoverage
	valueFormat uint16
	values      []gposValueRecord
}

func (*gposSingle) gposLookupType() uint16 { return 1 }

type gposPair struct {
	format       uint16
	coverage     *gdefCoverage
	valueFormat1 uint16
	valueFormat2 uint16
	pairSets     []*gposPairSet
	classDef1    *layoutClassDef
	classDef2    *layoutClassDef
	class1Count  uint16
	class2Count  uint16
	classRecords []gposClassPairRecord
}

func (*gposPair) gposLookupType() uint16 { return 2 }

type gposPairSet struct {
	records []gposPairValueRecord
}

type gposPairValueRecord struct {
	secondGlyph uint16
	value1      gposValueRecord
	value2      gposValueRecord
}

type gposClassPairRecord struct {
	value1 gposValueRecord
	value2 gposValueRecord
}

type gposAnchor struct {
	format     uint16
	x          int16
	y          int16
	pointIndex uint16
	xDevice    *gdefDevice
	yDevice    *gdefDevice
}

type gposCursive struct {
	coverage *gdefCoverage
	records  []gposEntryExitRecord
}

func (*gposCursive) gposLookupType() uint16 { return 3 }

type gposEntryExitRecord struct {
	entry *gposAnchor
	exit  *gposAnchor
}

type gposMarkRecord struct {
	class  uint16
	anchor *gposAnchor
}

type gposMarkArray struct {
	records []gposMarkRecord
}

type gposBaseArray struct {
	records [][]*gposAnchor
}

type gposMarkBase struct {
	markCoverage *gdefCoverage
	baseCoverage *gdefCoverage
	classCount   uint16
	marks        *gposMarkArray
	bases        *gposBaseArray
}

func (*gposMarkBase) gposLookupType() uint16 { return 4 }

type gposLigatureArray struct {
	attaches []*gposLigatureAttach
}

type gposLigatureAttach struct {
	components [][]*gposAnchor
}

type gposMarkLigature struct {
	markCoverage     *gdefCoverage
	ligatureCoverage *gdefCoverage
	classCount       uint16
	marks            *gposMarkArray
	ligatures        *gposLigatureArray
}

func (*gposMarkLigature) gposLookupType() uint16 { return 5 }

type gposMark2Array struct {
	records [][]*gposAnchor
}

type gposMarkMark struct {
	mark1Coverage *gdefCoverage
	mark2Coverage *gdefCoverage
	classCount    uint16
	marks1        *gposMarkArray
	marks2        *gposMark2Array
}

func (*gposMarkMark) gposLookupType() uint16 { return 6 }

// Contextual positioning uses the byte-identical common structures defined
// for contextual substitution. Keeping the wrapper private makes the reuse
// explicit without claiming substitution semantics for a GPOS lookup.
type gposContext struct{ context *gsubContext }

func (*gposContext) gposLookupType() uint16 { return 7 }

type gposChainedContext struct{ context *gsubChainedContext }

func (*gposChainedContext) gposLookupType() uint16 { return 8 }

type gposExtension struct {
	extensionLookupType uint16
	subtable            gposSubtable
}

func (*gposExtension) gposLookupType() uint16 { return 9 }
