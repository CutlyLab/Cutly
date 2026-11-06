package sfntglyf

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

var (
	errLayoutInputInvalid = errors.New("sfntglyf: layout foundation input is invalid")
	errLayoutInvalid      = errors.New("sfntglyf: layout table is invalid")
	errLayoutUnsupported  = errors.New("sfntglyf: layout feature is unsupported")
)

const (
	codeLayoutInputInvalid ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.INPUT_INVALID"
	codeLayoutInvalid      ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.TABLE_INVALID"
	codeLayoutUnsupported  ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.UNSUPPORTED"
	codeLayoutLimits       ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.LIMITS_INVALID"
	codeLayoutResource     ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.RESOURCE_LIMIT"
	codeLayoutInvariant    ErrorCode = "FONT.STATIC_GLYF_LAYOUT_FOUNDATION_V1.INVARIANT_FAILED"
)

type layoutCancelableSortSignal struct{ err error }

func layoutCancelableSort(ctx context.Context, path string, value any, less func(left, right int) bool) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if signal, ok := recovered.(layoutCancelableSortSignal); ok {
				err = signal.err
				return
			}
			panic(recovered)
		}
	}()
	var comparisons uint64
	sort.Slice(value, func(left, right int) bool {
		comparisons++
		if comparisons%layoutContextCheckStride == 0 {
			if checkErr := contextFailure(ctx, path); checkErr != nil {
				panic(layoutCancelableSortSignal{err: checkErr})
			}
		}
		return less(left, right)
	})
	return contextFailure(ctx, path+"/complete")
}

const (
	defaultLayoutMaxNodes            uint64 = defaultMaxLayoutFoundationNodes
	defaultLayoutMaxEdges            uint64 = defaultMaxLayoutFoundationEdges
	defaultLayoutMaxAliasReferences  uint64 = defaultMaxLayoutFoundationAliases
	defaultLayoutMaxCoverageGlyphs   uint64 = defaultMaxLayoutFoundationCoverageGlyphs
	defaultLayoutMaxClassAssignments uint64 = defaultMaxLayoutFoundationClassAssignments
	defaultLayoutMaxAttachmentPoints uint64 = defaultMaxLayoutFoundationAttachmentPoints
	defaultLayoutMaxCarets           uint64 = defaultMaxLayoutFoundationCaretValues
	defaultLayoutMaxMarkGlyphSets    uint64 = defaultMaxLayoutFoundationMarkGlyphSets
	defaultLayoutMaxDeviceDeltas     uint64 = defaultMaxLayoutFoundationDeviceDeltas
	defaultLayoutMaxWorkUnits        uint64 = defaultMaxLayoutFoundationWorkUnits
	defaultLayoutMaxWorkingBytes     uint64 = defaultMaxLayoutFoundationWorkingBytes
	defaultLayoutMaxRetainedBytes    uint64 = defaultMaxLayoutFoundationRetainedBytes
	hardLayoutMaxNodes               uint64 = hardMaxLayoutFoundationNodes
	hardLayoutMaxEdges               uint64 = hardMaxLayoutFoundationEdges
	hardLayoutMaxAliasReferences     uint64 = hardMaxLayoutFoundationAliases
	hardLayoutMaxCoverageGlyphs      uint64 = hardMaxLayoutFoundationCoverageGlyphs
	hardLayoutMaxClassAssignments    uint64 = hardMaxLayoutFoundationClassAssignments
	hardLayoutMaxAttachmentPoints    uint64 = hardMaxLayoutFoundationAttachmentPoints
	hardLayoutMaxCarets              uint64 = hardMaxLayoutFoundationCaretValues
	hardLayoutMaxMarkGlyphSets       uint64 = hardMaxLayoutFoundationMarkGlyphSets
	hardLayoutMaxDeviceDeltas        uint64 = hardMaxLayoutFoundationDeviceDeltas
	hardLayoutMaxWorkUnits           uint64 = hardMaxLayoutFoundationWorkUnits
	hardLayoutMaxWorkingBytes        uint64 = hardMaxLayoutFoundationWorkingBytes
	hardLayoutMaxRetainedBytes       uint64 = hardMaxLayoutFoundationRetainedBytes
	layoutContextCheckStride                = 256
	defaultLayoutMaxOrderedRecords   uint64 = 1 << 22
	defaultLayoutMaxSubstituteGlyphs uint64 = 1 << 24
	defaultLayoutMaxLigComponents    uint64 = 1 << 24
	defaultLayoutMaxFeatureChars     uint64 = 1 << 20
	defaultLayoutMaxContextGlyphs    uint64 = 1 << 24
	defaultLayoutMaxSequenceLookups  uint64 = 1 << 24
	defaultLayoutMaxLogicalEdges     uint64 = 1 << 24
	defaultLayoutMaxLogicalExpansion uint64 = 1 << 24
	hardLayoutMaxOrderedRecords      uint64 = 1 << 30
	hardLayoutMaxSubstituteGlyphs    uint64 = 1 << 34
	hardLayoutMaxLigComponents       uint64 = 1 << 34
	hardLayoutMaxFeatureChars        uint64 = 1 << 32
	hardLayoutMaxContextGlyphs       uint64 = 1 << 34
	hardLayoutMaxSequenceLookups     uint64 = 1 << 34
	hardLayoutMaxLogicalEdges        uint64 = 1 << 34
	hardLayoutMaxLogicalExpansion    uint64 = 1 << 40
)

// layoutDecodeLimits is deliberately private. It is an operation-local guard for the
// common-layout reader and is not a rendering or shaping capability contract.
type layoutDecodeLimits struct {
	maxTableBytes         uint64
	maxNodes              uint64
	maxEdges              uint64
	maxAliasReferences    uint64
	maxStructuralDepth    uint32
	maxCoverageTables     uint64
	maxCoverageGlyphs     uint64
	maxClassDefTables     uint64
	maxClassAssignments   uint64
	maxDeviceTables       uint64
	maxAttachmentPoints   uint64
	maxAttachPointTables  uint64
	maxLigGlyphTables     uint64
	maxCarets             uint64
	maxMarkGlyphSets      uint64
	maxDeviceDeltas       uint64
	maxOrderedRecords     uint64
	maxSubstituteGlyphs   uint64
	maxLigatureComponents uint64
	maxFeatureCharacters  uint64
	maxContextGlyphs      uint64
	maxSequenceLookups    uint64
	maxLogicalLookupEdges uint64
	maxLogicalLookupDepth uint32
	maxLogicalExpansion   uint64
	maxWorkUnits          uint64
	maxWorkingBytes       uint64
	maxRetainedBytes      uint64
	// The foundation budgets are optional exact shared-operation gates. They
	// are zero for standalone decoder calls and never change the decoder's
	// public/private contract; the foundation builder supplies both pairs.
	foundationWorkBase      uint64
	foundationWorkLimit     uint64
	foundationWorkingSource uint64
	foundationWorkingLimit  uint64
	foundationRetainedBase  uint64
	foundationRetainedLimit uint64
}

func defaultLayoutDecodeLimits() layoutDecodeLimits {
	return layoutDecodeLimits{
		maxTableBytes: defaultMaxLayoutFoundationTableBytes,
		maxNodes:      defaultLayoutMaxNodes, maxEdges: defaultLayoutMaxEdges,
		maxAliasReferences:   defaultLayoutMaxAliasReferences,
		maxStructuralDepth:   defaultMaxLayoutFoundationDepth,
		maxCoverageTables:    defaultMaxLayoutFoundationCoverageTables,
		maxCoverageGlyphs:    defaultLayoutMaxCoverageGlyphs,
		maxClassDefTables:    defaultMaxLayoutFoundationClassDefTables,
		maxClassAssignments:  defaultLayoutMaxClassAssignments,
		maxDeviceTables:      defaultMaxLayoutFoundationDeviceTables,
		maxAttachmentPoints:  defaultLayoutMaxAttachmentPoints,
		maxAttachPointTables: defaultMaxLayoutFoundationAttachPointTables,
		maxLigGlyphTables:    defaultMaxLayoutFoundationLigGlyphTables,
		maxCarets:            defaultLayoutMaxCarets, maxMarkGlyphSets: defaultLayoutMaxMarkGlyphSets,
		maxDeviceDeltas: defaultLayoutMaxDeviceDeltas, maxWorkUnits: defaultLayoutMaxWorkUnits,
		maxOrderedRecords:     defaultLayoutMaxOrderedRecords,
		maxSubstituteGlyphs:   defaultLayoutMaxSubstituteGlyphs,
		maxLigatureComponents: defaultLayoutMaxLigComponents,
		maxFeatureCharacters:  defaultLayoutMaxFeatureChars,
		maxContextGlyphs:      defaultLayoutMaxContextGlyphs,
		maxSequenceLookups:    defaultLayoutMaxSequenceLookups,
		maxLogicalLookupEdges: defaultLayoutMaxLogicalEdges,
		maxLogicalLookupDepth: defaultMaxLayoutFoundationDepth,
		maxLogicalExpansion:   defaultLayoutMaxLogicalExpansion,
		maxWorkingBytes:       defaultLayoutMaxWorkingBytes, maxRetainedBytes: defaultLayoutMaxRetainedBytes,
	}
}

func normalizeLayoutDecodeLimits(value layoutDecodeLimits) (layoutDecodeLimits, error) {
	defaults := defaultLayoutDecodeLimits()
	if value.maxTableBytes == 0 {
		value.maxTableBytes = defaults.maxTableBytes
	}
	if value.maxNodes == 0 {
		value.maxNodes = defaults.maxNodes
	}
	if value.maxEdges == 0 {
		value.maxEdges = defaults.maxEdges
	}
	if value.maxAliasReferences == 0 {
		value.maxAliasReferences = defaults.maxAliasReferences
	}
	if value.maxStructuralDepth == 0 {
		value.maxStructuralDepth = defaults.maxStructuralDepth
	}
	if value.maxCoverageTables == 0 {
		value.maxCoverageTables = defaults.maxCoverageTables
	}
	if value.maxCoverageGlyphs == 0 {
		value.maxCoverageGlyphs = defaults.maxCoverageGlyphs
	}
	if value.maxClassDefTables == 0 {
		value.maxClassDefTables = defaults.maxClassDefTables
	}
	if value.maxClassAssignments == 0 {
		value.maxClassAssignments = defaults.maxClassAssignments
	}
	if value.maxDeviceTables == 0 {
		value.maxDeviceTables = defaults.maxDeviceTables
	}
	if value.maxAttachmentPoints == 0 {
		value.maxAttachmentPoints = defaults.maxAttachmentPoints
	}
	if value.maxAttachPointTables == 0 {
		value.maxAttachPointTables = defaults.maxAttachPointTables
	}
	if value.maxLigGlyphTables == 0 {
		value.maxLigGlyphTables = defaults.maxLigGlyphTables
	}
	if value.maxCarets == 0 {
		value.maxCarets = defaults.maxCarets
	}
	if value.maxMarkGlyphSets == 0 {
		value.maxMarkGlyphSets = defaults.maxMarkGlyphSets
	}
	if value.maxDeviceDeltas == 0 {
		value.maxDeviceDeltas = defaults.maxDeviceDeltas
	}
	if value.maxOrderedRecords == 0 {
		value.maxOrderedRecords = defaults.maxOrderedRecords
	}
	if value.maxSubstituteGlyphs == 0 {
		value.maxSubstituteGlyphs = defaults.maxSubstituteGlyphs
	}
	if value.maxLigatureComponents == 0 {
		value.maxLigatureComponents = defaults.maxLigatureComponents
	}
	if value.maxFeatureCharacters == 0 {
		value.maxFeatureCharacters = defaults.maxFeatureCharacters
	}
	if value.maxContextGlyphs == 0 {
		value.maxContextGlyphs = defaults.maxContextGlyphs
	}
	if value.maxSequenceLookups == 0 {
		value.maxSequenceLookups = defaults.maxSequenceLookups
	}
	if value.maxLogicalLookupEdges == 0 {
		value.maxLogicalLookupEdges = defaults.maxLogicalLookupEdges
	}
	if value.maxLogicalLookupDepth == 0 {
		value.maxLogicalLookupDepth = defaults.maxLogicalLookupDepth
	}
	if value.maxLogicalExpansion == 0 {
		value.maxLogicalExpansion = defaults.maxLogicalExpansion
	}
	if value.maxWorkUnits == 0 {
		value.maxWorkUnits = defaults.maxWorkUnits
	}
	if value.maxWorkingBytes == 0 {
		value.maxWorkingBytes = defaults.maxWorkingBytes
	}
	if value.maxRetainedBytes == 0 {
		value.maxRetainedBytes = defaults.maxRetainedBytes
	}
	if value.maxTableBytes > hardMaxLayoutFoundationTableBytes ||
		value.maxNodes > hardLayoutMaxNodes || value.maxEdges > hardLayoutMaxEdges ||
		value.maxAliasReferences > hardLayoutMaxAliasReferences ||
		value.maxStructuralDepth > hardMaxLayoutFoundationDepth ||
		value.maxCoverageTables > hardMaxLayoutFoundationCoverageTables ||
		value.maxCoverageGlyphs > hardLayoutMaxCoverageGlyphs ||
		value.maxClassDefTables > hardMaxLayoutFoundationClassDefTables ||
		value.maxClassAssignments > hardLayoutMaxClassAssignments ||
		value.maxDeviceTables > hardMaxLayoutFoundationDeviceTables ||
		value.maxAttachmentPoints > hardLayoutMaxAttachmentPoints || value.maxCarets > hardLayoutMaxCarets ||
		value.maxAttachPointTables > hardMaxLayoutFoundationAttachPointTables ||
		value.maxLigGlyphTables > hardMaxLayoutFoundationLigGlyphTables ||
		value.maxMarkGlyphSets > hardLayoutMaxMarkGlyphSets || value.maxDeviceDeltas > hardLayoutMaxDeviceDeltas ||
		value.maxOrderedRecords > hardLayoutMaxOrderedRecords ||
		value.maxSubstituteGlyphs > hardLayoutMaxSubstituteGlyphs ||
		value.maxLigatureComponents > hardLayoutMaxLigComponents ||
		value.maxFeatureCharacters > hardLayoutMaxFeatureChars ||
		value.maxContextGlyphs > hardLayoutMaxContextGlyphs ||
		value.maxSequenceLookups > hardLayoutMaxSequenceLookups ||
		value.maxLogicalLookupEdges > hardLayoutMaxLogicalEdges ||
		value.maxLogicalLookupDepth > hardMaxLayoutFoundationDepth ||
		value.maxLogicalExpansion > hardLayoutMaxLogicalExpansion ||
		value.maxWorkUnits > hardLayoutMaxWorkUnits || value.maxWorkingBytes > hardLayoutMaxWorkingBytes ||
		value.maxRetainedBytes > hardLayoutMaxRetainedBytes {
		return layoutDecodeLimits{}, fontError(codeLayoutLimits, "layout/limits", ErrLimitsInvalid)
	}
	return value, nil
}

type layoutDecodeStats struct {
	tableBytes                uint64
	gdefBytes                 uint64
	gsubBytes                 uint64
	gposBytes                 uint64
	nodes                     uint64
	edges                     uint64
	aliasReferences           uint64
	maxStructuralDepth        uint32
	directBytes               uint64
	coverageTables            uint64
	coverageFormat1Glyphs     uint64
	coverageRanges            uint64
	coverageGlyphs            uint64
	classDefTables            uint64
	classFormat1Values        uint64
	classRanges               uint64
	classAssignments          uint64
	deviceTables              uint64
	deviceDeltas              uint64
	deviceWords               uint64
	attachLists               uint64
	attachPointTables         uint64
	attachmentGlyphReferences uint64
	attachmentPoints          uint64
	ligCaretLists             uint64
	ligGlyphTables            uint64
	ligatureGlyphReferences   uint64
	caretValueTables          uint64
	caretValueReferences      uint64
	caretPointReferences      uint64
	markGlyphSetsTables       uint64
	markGlyphSetReferences    uint64
	markGlyphSets             uint64
	scriptRecords             uint64
	scriptTables              uint64
	langSysRecords            uint64
	langSysTables             uint64
	langSysFeatureReferences  uint64
	featureRecords            uint64
	featureTables             uint64
	featureLookupReferences   uint64
	featureParamTables        uint64
	featureCharacters         uint64
	contextRuleSetReferences  uint64
	contextRuleSets           uint64
	contextRuleReferences     uint64
	contextRules              uint64
	contextGlyphReferences    uint64
	contextClassReferences    uint64
	sequenceLookupRecords     uint64
	extensionSubtables        uint64
	reverseSubtables          uint64
	logicalLookupEdges        uint64
	logicalLookupDepth        uint32
	logicalExpansion          uint64
	lookupReferences          uint64
	lookupTables              uint64
	subtableReferences        uint64
	gsubSubtables             uint64
	gposSubtables             uint64
	valueRecords              uint64
	pairSetTables             uint64
	pairValueRecords          uint64
	classPairRecords          uint64
	anchorTables              uint64
	anchorReferences          uint64
	markArrayTables           uint64
	markRecords               uint64
	baseArrayTables           uint64
	baseRecords               uint64
	ligatureArrayTables       uint64
	ligatureAttachTables      uint64
	ligatureComponentRecords  uint64
	mark2ArrayTables          uint64
	mark2Records              uint64
	sequenceReferences        uint64
	sequenceTables            uint64
	alternateSetReferences    uint64
	alternateSetTables        uint64
	ligatureSetReferences     uint64
	ligatureSetTables         uint64
	ligatureReferences        uint64
	ligatureTables            uint64
	substituteGlyphReferences uint64
	ligatureComponents        uint64
	orderedRecords            uint64
	workUnits                 uint64
	workingBytes              uint64
	retainedBytes             uint64
}

type layoutResourceMeter struct {
	limits    layoutDecodeLimits
	numGlyphs uint64
	stats     layoutDecodeStats
}

func newLayoutResourceMeter(encodedBytes, numGlyphs uint64, limits layoutDecodeLimits) *layoutResourceMeter {
	return newLayoutResourceMeterForTable(encodedBytes, numGlyphs, limits, layoutGDEFTag)
}

func newLayoutResourceMeterForTable(encodedBytes, numGlyphs uint64, limits layoutDecodeLimits, tag uint32) *layoutResourceMeter {
	stats := layoutDecodeStats{tableBytes: encodedBytes}
	if tag == layoutGDEFTag {
		stats.gdefBytes = encodedBytes
	} else if tag == layoutGSUBTag {
		stats.gsubBytes = encodedBytes
	} else if tag == layoutGPOSTag {
		stats.gposBytes = encodedBytes
	}
	return &layoutResourceMeter{
		limits: limits, numGlyphs: numGlyphs,
		stats: stats,
	}
}

func (value *layoutResourceMeter) chargeNode(directBytes uint64, path string) error {
	if err := value.charge(&value.stats.nodes, 1, value.limits.maxNodes, path+"/nodes"); err != nil {
		return err
	}
	return value.observe(&value.stats.directBytes, directBytes, path+"/direct_bytes")
}

func (value *layoutResourceMeter) chargeEdges(count uint64, path string) error {
	return value.charge(&value.stats.edges, count, value.limits.maxEdges, path+"/edges")
}

func (value *layoutResourceMeter) chargeAlias(path string) error {
	return value.charge(&value.stats.aliasReferences, 1, value.limits.maxAliasReferences, path+"/aliases")
}

func (value *layoutResourceMeter) chargeCoverage(count uint64, path string) error {
	return value.charge(&value.stats.coverageGlyphs, count, value.limits.maxCoverageGlyphs, path+"/coverage_glyphs")
}

func (value *layoutResourceMeter) chargeCoverageTable(path string) error {
	return value.charge(&value.stats.coverageTables, 1, value.limits.maxCoverageTables, path+"/coverage_tables")
}

func (value *layoutResourceMeter) chargeClasses(count uint64, path string) error {
	return value.charge(&value.stats.classAssignments, count, value.limits.maxClassAssignments, path+"/class_assignments")
}

func (value *layoutResourceMeter) chargeClassDefTable(path string) error {
	return value.charge(&value.stats.classDefTables, 1, value.limits.maxClassDefTables, path+"/class_def_tables")
}

func (value *layoutResourceMeter) chargeAttachmentPoints(count uint64, path string) error {
	return value.charge(&value.stats.attachmentPoints, count, value.limits.maxAttachmentPoints, path+"/attachment_points")
}

func (value *layoutResourceMeter) chargeAttachPointTable(path string) error {
	return value.charge(&value.stats.attachPointTables, 1, value.limits.maxAttachPointTables, path+"/attach_point_tables")
}

func (value *layoutResourceMeter) chargeLigGlyphTable(path string) error {
	return value.charge(&value.stats.ligGlyphTables, 1, value.limits.maxLigGlyphTables, path+"/lig_glyph_tables")
}

func (value *layoutResourceMeter) chargeCarets(count uint64, path string) error {
	return value.charge(&value.stats.caretValueReferences, count, value.limits.maxCarets, path+"/carets")
}

func (value *layoutResourceMeter) chargeMarkGlyphSets(count uint64, path string) error {
	return value.charge(&value.stats.markGlyphSets, count, value.limits.maxMarkGlyphSets, path+"/mark_glyph_sets")
}

func (value *layoutResourceMeter) chargeDeviceDeltas(count uint64, path string) error {
	return value.charge(&value.stats.deviceDeltas, count, value.limits.maxDeviceDeltas, path+"/device_deltas")
}

func (value *layoutResourceMeter) chargeDeviceTable(path string) error {
	return value.charge(&value.stats.deviceTables, 1, value.limits.maxDeviceTables, path+"/device_tables")
}

func (value *layoutResourceMeter) chargeOrdered(target *uint64, count uint64, path string) error {
	if err := value.charge(&value.stats.orderedRecords, count, value.limits.maxOrderedRecords, path+"/ordered_records"); err != nil {
		return err
	}
	return value.observe(target, count, path)
}

func (value *layoutResourceMeter) chargeSubstitutes(count uint64, path string) error {
	return value.charge(&value.stats.substituteGlyphReferences, count, value.limits.maxSubstituteGlyphs, path+"/substitute_glyphs")
}

func (value *layoutResourceMeter) chargeLigatureComponents(count uint64, path string) error {
	return value.charge(&value.stats.ligatureComponents, count, value.limits.maxLigatureComponents, path+"/ligature_components")
}

func (value *layoutResourceMeter) chargeFeatureCharacters(count uint64, path string) error {
	return value.charge(&value.stats.featureCharacters, count, value.limits.maxFeatureCharacters, path+"/feature_characters")
}

func (value *layoutResourceMeter) chargeContextGlyphs(count uint64, path string) error {
	return value.charge(&value.stats.contextGlyphReferences, count, value.limits.maxContextGlyphs, path+"/context_glyphs")
}

func (value *layoutResourceMeter) chargeContextClasses(count uint64, path string) error {
	return value.charge(&value.stats.contextClassReferences, count, value.limits.maxContextGlyphs, path+"/context_classes")
}

func (value *layoutResourceMeter) chargeSequenceLookups(count uint64, path string) error {
	return value.charge(&value.stats.sequenceLookupRecords, count, value.limits.maxSequenceLookups, path+"/sequence_lookups")
}

func (value *layoutResourceMeter) chargeLogicalEdges(count uint64, path string) error {
	return value.charge(&value.stats.logicalLookupEdges, count, value.limits.maxLogicalLookupEdges, path+"/logical_edges")
}

func (value *layoutReader) chargeLookupSubtable(path string) error {
	target := &value.meter.stats.gsubSubtables
	if value.tag == layoutGPOSTag {
		target = &value.meter.stats.gposSubtables
	}
	return value.meter.charge(target, 1, value.meter.limits.maxOrderedRecords, path+"/tables")
}

func (value *layoutResourceMeter) setLogicalExpansion(count uint64, path string) error {
	if count > value.limits.maxLogicalExpansion {
		return fontError(codeLayoutResource, path+"/logical_expansion", ErrResourceLimit)
	}
	value.stats.logicalExpansion = count
	return value.recompute(path)
}

func (value *layoutResourceMeter) setLogicalDepth(depth uint32, path string) error {
	if depth > value.limits.maxLogicalLookupDepth {
		return fontError(codeLayoutResource, path+"/logical_depth", ErrResourceLimit)
	}
	value.stats.logicalLookupDepth = depth
	return nil
}

func (value *layoutResourceMeter) reserveRetained(bytes uint64, path string) error {
	next, ok := checkedAdd(value.stats.retainedBytes, bytes)
	if !ok || next > value.limits.maxRetainedBytes {
		return fontError(codeLayoutResource, path+"/retained_bytes", ErrResourceLimit)
	}
	value.stats.retainedBytes = next
	return value.recompute(path)
}

func (value *layoutResourceMeter) charge(target *uint64, count, limit uint64, path string) error {
	next, ok := checkedAdd(*target, count)
	if !ok || next > limit {
		return fontError(codeLayoutResource, path, ErrResourceLimit)
	}
	*target = next
	return value.recompute(path)
}

func (value *layoutResourceMeter) observe(target *uint64, count uint64, path string) error {
	next, ok := checkedAdd(*target, count)
	if !ok {
		return fontError(codeLayoutResource, path, ErrResourceLimit)
	}
	*target = next
	return value.recompute(path)
}

func (value *layoutResourceMeter) recompute(path string) error {
	work, ok := outlineWeightedSum(value.stats.tableBytes,
		[2]uint64{value.numGlyphs, 1}, [2]uint64{value.stats.nodes, 16},
		[2]uint64{value.stats.edges, 8}, [2]uint64{value.stats.aliasReferences, 4},
		[2]uint64{value.stats.coverageGlyphs, 2},
		[2]uint64{value.stats.classAssignments, 2}, [2]uint64{value.stats.attachmentPoints, 2},
		[2]uint64{value.stats.caretValueReferences, 4}, [2]uint64{value.stats.markGlyphSets, 4},
		[2]uint64{value.stats.deviceDeltas, 2},
		[2]uint64{value.stats.orderedRecords, 8},
		[2]uint64{value.stats.substituteGlyphReferences, 4},
		[2]uint64{value.stats.ligatureComponents, 4},
		[2]uint64{value.stats.featureCharacters, 4})
	if ok {
		work, ok = outlineWeightedSum(work,
			[2]uint64{value.stats.contextGlyphReferences, 4},
			[2]uint64{value.stats.contextClassReferences, 2},
			[2]uint64{value.stats.sequenceLookupRecords, 8},
			[2]uint64{value.stats.logicalLookupEdges, 8},
			[2]uint64{value.stats.logicalExpansion, 1},
			[2]uint64{value.stats.valueRecords, 8},
			[2]uint64{value.stats.pairValueRecords, 8},
			[2]uint64{value.stats.classPairRecords, 8},
			[2]uint64{value.stats.anchorReferences, 4},
			[2]uint64{value.stats.markRecords, 8},
			[2]uint64{value.stats.baseRecords, 8},
			[2]uint64{value.stats.ligatureComponentRecords, 8},
			[2]uint64{value.stats.mark2Records, 8})
	}
	if !ok || work > value.limits.maxWorkUnits {
		return fontError(codeLayoutResource, path+"/work_units", ErrResourceLimit)
	}
	working, ok := outlineWeightedSum(4_096,
		[2]uint64{value.numGlyphs, 4}, [2]uint64{value.stats.nodes, 256},
		[2]uint64{value.stats.edges, 128}, [2]uint64{value.stats.aliasReferences, 16},
		[2]uint64{value.stats.retainedBytes, 1},
		[2]uint64{value.stats.logicalLookupEdges, 32})
	if !ok || working > value.limits.maxWorkingBytes {
		return fontError(codeLayoutResource, path+"/working_bytes", ErrResourceLimit)
	}
	value.stats.workUnits = work
	value.stats.workingBytes = working
	if value.limits.foundationWorkLimit != 0 {
		foundationWork, ok := layoutFoundationDecodeWork(value.limits.foundationWorkBase, value.stats)
		if !ok || foundationWork > value.limits.foundationWorkLimit {
			return fontError(codeLayoutResource, path+"/foundation_work_units", ErrResourceLimit)
		}
	}
	if value.limits.foundationRetainedLimit != 0 {
		foundationRetained, ok := layoutFoundationDecodeRetained(value.limits.foundationRetainedBase, value.stats)
		if !ok || foundationRetained > value.limits.foundationRetainedLimit {
			return fontError(codeLayoutResource, path+"/foundation_retained_bytes", ErrResourceLimit)
		}
	}
	if value.limits.foundationWorkingLimit != 0 {
		foundationWorking, ok := layoutFoundationDecodeWorking(value.limits.foundationWorkingSource,
			value.numGlyphs, value.stats)
		if !ok || foundationWorking > value.limits.foundationWorkingLimit {
			return fontError(codeLayoutResource, path+"/foundation_working_bytes", ErrResourceLimit)
		}
	}
	return nil
}

type layoutReader struct {
	ctx   context.Context
	data  []byte
	meter *layoutResourceMeter
	graph *layoutSpanGraph
	tag   uint32
}

func newLayoutReader(ctx context.Context, data []byte, meter *layoutResourceMeter) *layoutReader {
	return newLayoutReaderForTable(ctx, data, meter, layoutGDEFTag)
}

// newLayoutReaderForTable keeps the physical span graph table-relative while
// binding every projected node and edge to its owning OpenType table.  The
// legacy constructor above deliberately remains GDEF-specific so the frozen
// c-a evidence is byte-for-byte unchanged.
func newLayoutReaderForTable(ctx context.Context, data []byte, meter *layoutResourceMeter, tag uint32) *layoutReader {
	result := &layoutReader{ctx: ctx, data: data, meter: meter, tag: tag}
	result.graph = &layoutSpanGraph{reader: result, nodes: make(map[uint64]*layoutSpanNode)}
	return result
}

func (value *layoutReader) bytes(offset, size uint64, path string) ([]byte, error) {
	end, ok := checkedAdd(offset, size)
	if !ok || end > uint64(len(value.data)) || offset > uint64(maxInt()) || end > uint64(maxInt()) {
		return nil, fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	return value.data[int(offset):int(end)], nil
}

func (value *layoutReader) u16(offset uint64, path string) (uint16, error) {
	data, err := value.bytes(offset, 2, path)
	if err != nil {
		return 0, err
	}
	return uint16(data[0])<<8 | uint16(data[1]), nil
}

func (value *layoutReader) i16(offset uint64, path string) (int16, error) {
	result, err := value.u16(offset, path)
	return int16(result), err
}

func (value *layoutReader) u32(offset uint64, path string) (uint32, error) {
	data, err := value.bytes(offset, 4, path)
	if err != nil {
		return 0, err
	}
	return uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]), nil
}

func (value *layoutReader) u24(offset uint64, path string) (uint32, error) {
	data, err := value.bytes(offset, 3, path)
	if err != nil {
		return 0, err
	}
	return uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2]), nil
}

func (value *layoutReader) childOffset(base, offset uint64, path string) (uint64, error) {
	if offset == 0 {
		return 0, fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	result, ok := checkedAdd(base, offset)
	if !ok || result >= uint64(len(value.data)) {
		return 0, fontError(codeLayoutInvalid, path, errLayoutInvalid)
	}
	return result, nil
}

func (value *layoutReader) checkContext(index uint64, path string) error {
	if index%layoutContextCheckStride != 0 {
		return nil
	}
	return contextFailure(value.ctx, path)
}

type layoutSpanKind uint8

const (
	layoutSpanGDEFHeader layoutSpanKind = iota + 1
	layoutSpanCoverage
	layoutSpanGlyphClassDef
	layoutSpanMarkAttachClassDef
	layoutSpanAttachList
	layoutSpanAttachPoint
	layoutSpanLigCaretList
	layoutSpanLigGlyph
	layoutSpanCaretValue
	layoutSpanDevice
	layoutSpanMarkGlyphSets
	// Values above are frozen by the GDEF c-a canonical evidence. New common
	// and GSUB kinds must remain append-only.
	layoutSpanGSUBHeader
	layoutSpanScriptList
	layoutSpanScript
	layoutSpanLangSys
	layoutSpanFeatureList
	layoutSpanFeature
	layoutSpanLookupList
	layoutSpanLookup
	layoutSpanFeatureParamsCV
	layoutSpanFeatureParamsSS
	layoutSpanGSUBSingle
	layoutSpanGSUBMultiple
	layoutSpanGSUBSequence
	layoutSpanGSUBAlternate
	layoutSpanGSUBAlternateSet
	layoutSpanGSUBLigatureSubst
	layoutSpanGSUBLigatureSet
	layoutSpanGSUBLigature
	layoutSpanClassDef
	// Advanced GSUB kinds are append-only so the frozen GDEF and Phase 0-2
	// physical evidence identities remain unchanged.
	layoutSpanGSUBContext
	layoutSpanGSUBSequenceRuleSet
	layoutSpanGSUBSequenceRule
	layoutSpanGSUBClassSequenceRuleSet
	layoutSpanGSUBClassSequenceRule
	layoutSpanGSUBChainContext
	layoutSpanGSUBChainRuleSet
	layoutSpanGSUBChainRule
	layoutSpanGSUBChainClassRuleSet
	layoutSpanGSUBChainClassRule
	layoutSpanGSUBExtension
	layoutSpanGSUBReverseChain
	// GPOS kinds are append-only. The contextual kinds below are shared with
	// the structurally identical GSUB contextual decoder; tableTag keeps their
	// physical identities disjoint.
	layoutSpanGPOSHeader
	layoutSpanFeatureParamsSize
	layoutSpanGPOSSingle
	layoutSpanGPOSPair
	layoutSpanGPOSPairSet
	layoutSpanGPOSCursive
	layoutSpanGPOSMarkBase
	layoutSpanGPOSMarkLigature
	layoutSpanGPOSMarkMark
	layoutSpanGPOSAnchor
	layoutSpanGPOSMarkArray
	layoutSpanGPOSBaseArray
	layoutSpanGPOSLigatureArray
	layoutSpanGPOSLigatureAttach
	layoutSpanGPOSMark2Array
	layoutSpanGPOSExtension
)

func (value layoutSpanKind) String() string {
	switch value {
	case layoutSpanGDEFHeader:
		return "gdef_header"
	case layoutSpanCoverage:
		return "coverage"
	case layoutSpanGlyphClassDef:
		return "glyph_class_def"
	case layoutSpanMarkAttachClassDef:
		return "mark_attach_class_def"
	case layoutSpanAttachList:
		return "attach_list"
	case layoutSpanAttachPoint:
		return "attach_point"
	case layoutSpanLigCaretList:
		return "lig_caret_list"
	case layoutSpanLigGlyph:
		return "lig_glyph"
	case layoutSpanCaretValue:
		return "caret_value"
	case layoutSpanDevice:
		return "device"
	case layoutSpanMarkGlyphSets:
		return "mark_glyph_sets"
	case layoutSpanGSUBHeader:
		return "gsub_header"
	case layoutSpanScriptList:
		return "script_list"
	case layoutSpanScript:
		return "script"
	case layoutSpanLangSys:
		return "lang_sys"
	case layoutSpanFeatureList:
		return "feature_list"
	case layoutSpanFeature:
		return "feature"
	case layoutSpanLookupList:
		return "lookup_list"
	case layoutSpanLookup:
		return "lookup"
	case layoutSpanFeatureParamsCV:
		return "feature_params_cv"
	case layoutSpanFeatureParamsSS:
		return "feature_params_ss"
	case layoutSpanGSUBSingle:
		return "gsub_single"
	case layoutSpanGSUBMultiple:
		return "gsub_multiple"
	case layoutSpanGSUBSequence:
		return "gsub_sequence"
	case layoutSpanGSUBAlternate:
		return "gsub_alternate"
	case layoutSpanGSUBAlternateSet:
		return "gsub_alternate_set"
	case layoutSpanGSUBLigatureSubst:
		return "gsub_ligature_subst"
	case layoutSpanGSUBLigatureSet:
		return "gsub_ligature_set"
	case layoutSpanGSUBLigature:
		return "gsub_ligature"
	case layoutSpanClassDef:
		return "class_def"
	case layoutSpanGSUBContext:
		return "gsub_context"
	case layoutSpanGSUBSequenceRuleSet:
		return "gsub_sequence_rule_set"
	case layoutSpanGSUBSequenceRule:
		return "gsub_sequence_rule"
	case layoutSpanGSUBClassSequenceRuleSet:
		return "gsub_class_sequence_rule_set"
	case layoutSpanGSUBClassSequenceRule:
		return "gsub_class_sequence_rule"
	case layoutSpanGSUBChainContext:
		return "gsub_chain_context"
	case layoutSpanGSUBChainRuleSet:
		return "gsub_chain_rule_set"
	case layoutSpanGSUBChainRule:
		return "gsub_chain_rule"
	case layoutSpanGSUBChainClassRuleSet:
		return "gsub_chain_class_rule_set"
	case layoutSpanGSUBChainClassRule:
		return "gsub_chain_class_rule"
	case layoutSpanGSUBExtension:
		return "gsub_extension"
	case layoutSpanGSUBReverseChain:
		return "gsub_reverse_chain"
	case layoutSpanGPOSHeader:
		return "gpos_header"
	case layoutSpanFeatureParamsSize:
		return "feature_params_size"
	case layoutSpanGPOSSingle:
		return "gpos_single"
	case layoutSpanGPOSPair:
		return "gpos_pair"
	case layoutSpanGPOSPairSet:
		return "gpos_pair_set"
	case layoutSpanGPOSCursive:
		return "gpos_cursive"
	case layoutSpanGPOSMarkBase:
		return "gpos_mark_base"
	case layoutSpanGPOSMarkLigature:
		return "gpos_mark_ligature"
	case layoutSpanGPOSMarkMark:
		return "gpos_mark_mark"
	case layoutSpanGPOSAnchor:
		return "gpos_anchor"
	case layoutSpanGPOSMarkArray:
		return "gpos_mark_array"
	case layoutSpanGPOSBaseArray:
		return "gpos_base_array"
	case layoutSpanGPOSLigatureArray:
		return "gpos_ligature_array"
	case layoutSpanGPOSLigatureAttach:
		return "gpos_ligature_attach"
	case layoutSpanGPOSMark2Array:
		return "gpos_mark2_array"
	case layoutSpanGPOSExtension:
		return "gpos_extension"
	default:
		return "unknown"
	}
}

type layoutEdgeRole uint8

const (
	layoutEdgeGlyphClassDef layoutEdgeRole = iota + 1
	layoutEdgeAttachList
	layoutEdgeLigCaretList
	layoutEdgeMarkAttachClassDef
	layoutEdgeMarkGlyphSets
	layoutEdgeAttachCoverage
	layoutEdgeAttachPoint
	layoutEdgeLigCaretCoverage
	layoutEdgeLigGlyph
	layoutEdgeCaretValue
	layoutEdgeCaretDevice
	layoutEdgeMarkSetCoverage
	// Values above are frozen by the GDEF c-a canonical evidence.
	layoutEdgeGSUBScriptList
	layoutEdgeGSUBFeatureList
	layoutEdgeGSUBLookupList
	layoutEdgeScript
	layoutEdgeDefaultLangSys
	layoutEdgeLangSys
	layoutEdgeFeature
	layoutEdgeFeatureParams
	layoutEdgeLookup
	layoutEdgeLookupSubtable
	layoutEdgeGSUBCoverage
	layoutEdgeGSUBSequence
	layoutEdgeGSUBAlternateSet
	layoutEdgeGSUBLigatureSet
	layoutEdgeGSUBLigature
	// Advanced GSUB roles are append-only for canonical identity stability.
	layoutEdgeGSUBContextRuleSet
	layoutEdgeGSUBContextRule
	layoutEdgeGSUBContextClassDef
	layoutEdgeGSUBChainRuleSet
	layoutEdgeGSUBChainRule
	layoutEdgeGSUBBacktrackClassDef
	layoutEdgeGSUBInputClassDef
	layoutEdgeGSUBLookaheadClassDef
	layoutEdgeGSUBBacktrackCoverage
	layoutEdgeGSUBInputCoverage
	layoutEdgeGSUBLookaheadCoverage
	layoutEdgeGSUBExtension
	// GPOS-only roles remain append-only; common Script/Feature/Lookup roles
	// above are intentionally shared and are disambiguated by tableTag.
	layoutEdgeGPOSScriptList
	layoutEdgeGPOSFeatureList
	layoutEdgeGPOSLookupList
	layoutEdgeGPOSCoverage
	layoutEdgeGPOSPairSet
	layoutEdgeGPOSClassDef1
	layoutEdgeGPOSClassDef2
	layoutEdgeGPOSEntryAnchor
	layoutEdgeGPOSExitAnchor
	layoutEdgeGPOSMarkCoverage
	layoutEdgeGPOSBaseCoverage
	layoutEdgeGPOSLigatureCoverage
	layoutEdgeGPOSMark2Coverage
	layoutEdgeGPOSMarkArray
	layoutEdgeGPOSBaseArray
	layoutEdgeGPOSLigatureArray
	layoutEdgeGPOSLigatureAttach
	layoutEdgeGPOSMark2Array
	layoutEdgeGPOSMarkAnchor
	layoutEdgeGPOSBaseAnchor
	layoutEdgeGPOSLigatureAnchor
	layoutEdgeGPOSMark2Anchor
	layoutEdgeGPOSValueDevice
	layoutEdgeGPOSAnchorDevice
	layoutEdgeGPOSExtension
)

const layoutGDEFTag uint32 = 0x47444546
const layoutGSUBTag uint32 = 0x47535542
const layoutGPOSTag uint32 = 0x47504f53

// layoutNodeEvidence and layoutEdgeEvidence are stable physical-graph snapshots for the later
// foundation hash. They contain no map iteration order or discovery IDs.
type layoutNodeEvidence struct {
	tableTag    uint32
	start       uint64
	end         uint64
	kind        layoutSpanKind
	paramFirst  uint64
	paramSecond uint64
}

type layoutEdgeEvidence struct {
	parentStart uint64
	childStart  uint64
	tableTag    uint32
	ordinal     uint32
	rawOffset   uint32
	role        layoutEdgeRole
}

// layoutEdgeEvidenceRetainedBytes is also a packing invariant: the field order
// above keeps the value at 32 bytes on every supported architecture.  The
// compile-time and runtime assertions live in layout_reader_size_test.go.
const layoutEdgeEvidenceRetainedBytes uint64 = 32

type layoutEdgeRef struct {
	role      layoutEdgeRole
	ordinal   uint32
	rawOffset uint32
}

type layoutDecodeParams struct {
	first  uint64
	second uint64
}

type layoutSpanState uint8

const (
	layoutSpanActive layoutSpanState = iota + 1
	layoutSpanComplete
)

type layoutSpanNode struct {
	start  uint64
	end    uint64
	kind   layoutSpanKind
	params layoutDecodeParams
	depth  uint32
	state  layoutSpanState
	value  any
}

type layoutSpanGraph struct {
	reader *layoutReader
	nodes  map[uint64]*layoutSpanNode
	edges  []layoutEdgeEvidence
}

// begin registers only the bytes directly owned by a table header/record array. Referenced child
// tables get independent spans, which makes exact aliasing representable without treating ordinary
// parent/child structure as containment overlap.
func (value *layoutSpanGraph) begin(parent *layoutSpanNode, edge layoutEdgeRef, start, end uint64, kind layoutSpanKind, params layoutDecodeParams, path string) (*layoutSpanNode, bool, error) {
	if end <= start || end > uint64(len(value.reader.data)) {
		return nil, false, fontError(codeLayoutInvalid, path+"/span", errLayoutInvalid)
	}
	var depth uint32
	if parent != nil {
		if parent.depth == ^uint32(0) || parent.depth+1 > value.reader.meter.limits.maxStructuralDepth {
			return nil, false, fontError(codeLayoutResource, path+"/structural_depth", ErrResourceLimit)
		}
		depth = parent.depth + 1
		if err := value.reader.meter.chargeEdges(1, path); err != nil {
			return nil, false, err
		}
		value.edges = append(value.edges, layoutEdgeEvidence{
			tableTag: value.reader.tag, parentStart: parent.start, childStart: start,
			role: edge.role, ordinal: edge.ordinal, rawOffset: edge.rawOffset,
		})
	}
	if existing, found := value.nodes[start]; found {
		if existing.end != end || existing.kind != kind || existing.params != params {
			return nil, false, fontError(codeLayoutInvalid, path+"/alias", errLayoutInvalid)
		}
		if existing.state == layoutSpanActive {
			return nil, false, fontError(codeLayoutInvalid, path+"/cycle", errLayoutInvalid)
		}
		if err := value.reader.meter.chargeAlias(path); err != nil {
			return nil, false, err
		}
		if depth > existing.depth {
			existing.depth = depth
		}
		return existing, true, nil
	}
	if err := value.reader.meter.chargeNode(end-start, path); err != nil {
		return nil, false, err
	}
	node := &layoutSpanNode{start: start, end: end, kind: kind, params: params, depth: depth, state: layoutSpanActive}
	value.nodes[start] = node
	return node, false, nil
}

func (value *layoutSpanGraph) complete(node *layoutSpanNode, decoded any, path string) error {
	if node == nil || node.state != layoutSpanActive || decoded == nil {
		return fontError(codeLayoutInvariant, path+"/complete", ErrInvariantFailed)
	}
	node.value = decoded
	node.state = layoutSpanComplete
	return nil
}

func (value *layoutSpanGraph) allComplete() bool {
	for _, node := range value.nodes {
		if node.state != layoutSpanComplete || node.value == nil {
			return false
		}
	}
	return true
}

func (value *layoutSpanGraph) validateAndSnapshot(path string) ([]layoutNodeEvidence, []layoutEdgeEvidence, error) {
	if !value.allComplete() {
		return nil, nil, fontError(codeLayoutInvariant, path+"/incomplete", ErrInvariantFailed)
	}
	if err := contextFailure(value.reader.ctx, path+"/nodes/allocate"); err != nil {
		return nil, nil, err
	}
	nodeBytes, err := layoutRetainedArrayBytes(uint64(len(value.nodes)), 48, path+"/nodes")
	if err != nil {
		return nil, nil, err
	}
	if err := value.reader.meter.reserveRetained(nodeBytes, path+"/nodes"); err != nil {
		return nil, nil, err
	}
	nodes, err := layoutMakeSlice[layoutNodeEvidence](uint64(len(value.nodes)), path+"/nodes")
	if err != nil {
		return nil, nil, err
	}
	index := 0
	for _, node := range value.nodes {
		if err := value.reader.checkContext(uint64(index), path+"/nodes/project"); err != nil {
			return nil, nil, err
		}
		nodes[index] = layoutNodeEvidence{
			tableTag: value.reader.tag, start: node.start, end: node.end, kind: node.kind,
			paramFirst: node.params.first, paramSecond: node.params.second,
		}
		index++
	}
	if err := layoutCancelableSort(value.reader.ctx, path+"/nodes/sort", nodes, func(left, right int) bool {
		if nodes[left].start != nodes[right].start {
			return nodes[left].start < nodes[right].start
		}
		if nodes[left].end != nodes[right].end {
			return nodes[left].end < nodes[right].end
		}
		if nodes[left].kind != nodes[right].kind {
			return nodes[left].kind < nodes[right].kind
		}
		if nodes[left].paramFirst != nodes[right].paramFirst {
			return nodes[left].paramFirst < nodes[right].paramFirst
		}
		return nodes[left].paramSecond < nodes[right].paramSecond
	}); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(value.reader.ctx, path+"/nodes/sorted"); err != nil {
		return nil, nil, err
	}
	for index := 1; index < len(nodes); index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(value.reader.ctx, path+"/nodes/overlap"); err != nil {
				return nil, nil, err
			}
		}
		if nodes[index].start < nodes[index-1].end {
			return nil, nil, fontError(codeLayoutInvalid, path+"/overlap", errLayoutInvalid)
		}
	}
	if err := contextFailure(value.reader.ctx, path+"/edges/allocate"); err != nil {
		return nil, nil, err
	}
	edgeBytes, err := layoutRetainedArrayBytes(uint64(len(value.edges)), layoutEdgeEvidenceRetainedBytes, path+"/edges")
	if err != nil {
		return nil, nil, err
	}
	if err := value.reader.meter.reserveRetained(edgeBytes, path+"/edges"); err != nil {
		return nil, nil, err
	}
	edges, err := layoutMakeSlice[layoutEdgeEvidence](uint64(len(value.edges)), path+"/edges")
	if err != nil {
		return nil, nil, err
	}
	copy(edges, value.edges)
	if err := layoutCancelableSort(value.reader.ctx, path+"/edges/sort", edges, func(left, right int) bool {
		if edges[left].parentStart != edges[right].parentStart {
			return edges[left].parentStart < edges[right].parentStart
		}
		if edges[left].role != edges[right].role {
			return edges[left].role < edges[right].role
		}
		if edges[left].ordinal != edges[right].ordinal {
			return edges[left].ordinal < edges[right].ordinal
		}
		if edges[left].childStart != edges[right].childStart {
			return edges[left].childStart < edges[right].childStart
		}
		return edges[left].rawOffset < edges[right].rawOffset
	}); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(value.reader.ctx, path+"/edges/sorted"); err != nil {
		return nil, nil, err
	}
	if err := value.computeDepth(nodes, edges, path+"/depth"); err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

func (value *layoutSpanGraph) computeDepth(nodes []layoutNodeEvidence, edges []layoutEdgeEvidence, path string) error {
	if err := contextFailure(value.reader.ctx, path+"/allocate"); err != nil {
		return err
	}
	indices := make(map[uint64]int, len(nodes))
	for index, node := range nodes {
		if err := value.reader.checkContext(uint64(index), path+"/index"); err != nil {
			return err
		}
		indices[node.start] = index
	}
	indegree := make([]uint64, len(nodes))
	children := make([][]int, len(nodes))
	for index, edge := range edges {
		if err := value.reader.checkContext(uint64(index), path); err != nil {
			return err
		}
		parent, parentFound := indices[edge.parentStart]
		child, childFound := indices[edge.childStart]
		if !parentFound || !childFound {
			return fontError(codeLayoutInvariant, path+"/edge_endpoint", ErrInvariantFailed)
		}
		next, ok := checkedAdd(indegree[child], 1)
		if !ok {
			return fontError(codeLayoutResource, path+"/indegree", ErrResourceLimit)
		}
		indegree[child] = next
		children[parent] = append(children[parent], child)
	}
	queue := make([]int, 0, len(nodes))
	depth := make([]uint32, len(nodes))
	for index, count := range indegree {
		if err := value.reader.checkContext(uint64(index), path+"/roots"); err != nil {
			return err
		}
		if count == 0 {
			queue = append(queue, index)
		}
	}
	processed := 0
	var maximum uint32
	for head := 0; head < len(queue); head++ {
		if err := value.reader.checkContext(uint64(head), path); err != nil {
			return err
		}
		parent := queue[head]
		processed++
		for childIndex, child := range children[parent] {
			if err := value.reader.checkContext(uint64(childIndex), path+"/children"); err != nil {
				return err
			}
			candidate := depth[parent] + 1
			if candidate > depth[child] {
				depth[child] = candidate
				if candidate > maximum {
					maximum = candidate
				}
			}
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if processed != len(nodes) {
		return fontError(codeLayoutInvalid, path+"/cycle", errLayoutInvalid)
	}
	if maximum > value.reader.meter.limits.maxStructuralDepth {
		return fontError(codeLayoutResource, path+"/structural_depth", ErrResourceLimit)
	}
	value.reader.meter.stats.maxStructuralDepth = maximum
	return value.reader.meter.recompute(path)
}

func layoutSpanEnd(start, fixed, count, elementSize uint64, path string) (uint64, error) {
	arrayBytes, ok := checkedMultiply(count, elementSize)
	if !ok {
		return 0, fontError(codeLayoutResource, path+"/array", ErrResourceLimit)
	}
	size, ok := checkedAdd(fixed, arrayBytes)
	if !ok {
		return 0, fontError(codeLayoutResource, path+"/size", ErrResourceLimit)
	}
	end, ok := checkedAdd(start, size)
	if !ok {
		return 0, fontError(codeLayoutResource, path+"/end", ErrResourceLimit)
	}
	return end, nil
}

func layoutRetainedArrayBytes(count, elementBytes uint64, path string) (uint64, error) {
	result, ok := checkedMultiply(count, elementBytes)
	if !ok {
		return 0, fontError(codeLayoutResource, path+"/retained", ErrResourceLimit)
	}
	return result, nil
}

func layoutMakeSlice[T any](count uint64, path string) (result []T, err error) {
	if count > uint64(maxInt()) {
		return nil, fontError(codeLayoutResource, path+"/count", ErrResourceLimit)
	}
	defer func() {
		if recover() != nil {
			result = nil
			err = fontError(CodeAllocationFailed, path, ErrAllocationFailed)
		}
	}()
	return make([]T, int(count)), nil
}

func layoutValueAs[T any](node *layoutSpanNode, path string) (T, error) {
	var zero T
	if node == nil || node.state != layoutSpanComplete {
		return zero, fontError(codeLayoutInvariant, path+"/alias_state", ErrInvariantFailed)
	}
	result, ok := node.value.(T)
	if !ok {
		return zero, fontError(codeLayoutInvariant, path+"/alias_type", ErrInvariantFailed)
	}
	return result, nil
}

func layoutPath(base string, index uint64) string {
	return fmt.Sprintf("%s/%d", base, index)
}
