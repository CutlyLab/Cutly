package sfntglyf

import (
	"bytes"
	"context"
	"errors"
	"math"

	gotextfont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/CutlyLab/Cutly/internal/buildidentity"
)

// BindShapeProvider constructs the pinned go-text parser only after replaying the complete
// LayoutGraph from its private, validated OutlineSet lineage. No font bytes or go-text values are
// returned to callers.
func BindShapeProvider(ctx context.Context, graph *LayoutGraph, limits ShapeProviderLimits) (
	result *ShapeProvider, receipt ShapeProviderReceipt, err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = ShapeProviderReceipt{}
			err = fontError(CodeAllocationFailed, "shape_provider/bind/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = ShapeProviderReceipt{}
		}
	}()
	if err = contextFailure(ctx, "shape_provider/bind/context"); err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	normalized, err := normalizeShapeProviderLimits(limits)
	if err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	if err := validateShapeDependencyIdentity(); err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	// Inspect only already-published scalar lineage first. This computes the exact portable
	// provider charge and rejects insufficient limits before the replay or go-text parser can do
	// proportional work.
	source, core, err := inspectShapeLayoutGraph(graph)
	if err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	receipt = makeShapeProviderReceipt(graph.receipt, source.receipt, core.receipt, core)
	receipt, err = finalizeShapeProviderReceiptFromGraph(ctx, graph.receipt, receipt)
	if err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	if receipt.WorkUnits > normalized.MaxWorkUnits || receipt.WorkingBytes > normalized.MaxWorkingBytes ||
		receipt.RetainedBytes > normalized.MaxRetainedBytes {
		return nil, ShapeProviderReceipt{}, fontError(CodeShapeResourceLimit, "shape_provider/resources/pre_admission", ErrResourceLimit)
	}
	replayedSource, replayedCore, err := replayShapeLayoutGraph(ctx, graph)
	if err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	if replayedSource != source || replayedCore != core {
		return nil, ShapeProviderReceipt{}, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/replay_identity", ErrShapeProviderInvalid)
	}
	gsubLookupExpansion, gsubTopLevelApplications, err := shapeLayoutAdmissionEnvelope(graph)
	if err != nil {
		return nil, ShapeProviderReceipt{}, err
	}

	loader, err := opentype.NewLoader(bytes.NewReader(core.encoded))
	if err != nil || loader.Type != opentype.TrueType {
		return nil, ShapeProviderReceipt{}, fontError(
			CodeShapeProviderInvalid, "shape_provider/bind/go_text/loader",
			errors.Join(ErrShapeProviderInvalid, err),
		)
	}
	base, err := gotextfont.NewFont(loader)
	if err != nil || base == nil {
		return nil, ShapeProviderReceipt{}, fontError(
			CodeShapeProviderInvalid, "shape_provider/bind/go_text/font",
			errors.Join(ErrShapeProviderInvalid, err),
		)
	}
	if err := crossValidateGoTextFont(ctx, graph, core, base); err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	if err = contextFailure(ctx, "shape_provider/bind/publish"); err != nil {
		return nil, ShapeProviderReceipt{}, err
	}
	result = &ShapeProvider{
		graph: graph, source: source, base: base,
		gsubLookupExpansion: gsubLookupExpansion, gsubTopLevelApplications: gsubTopLevelApplications,
		receipt: receipt,
	}
	return result, receipt, nil
}

// shapeLayoutAdmissionEnvelope derives a scalar-only upper bound from the already validated,
// private GSUB model. LayoutGraphReceipt.Expansion counts contextual lookup dispatches, not the
// length of MultipleSubstitution sequences, so both dimensions are required. A single top-level
// lookup can dispatch at most Expansion+1 lookup applications; each application can grow one
// glyph by at most the longest MultipleSubstitution sequence. Saturating above the public hard
// glyph limit is safe because every nonempty Shape call will then fail admission.
//
// Arabic stch may be declared in either GSUB or GPOS. go-text runs its width-driven tile
// post-process after the guarded lookup passes, so both private feature lists must reject it
// before any go-text font is parsed or any HarfBuzz call is reachable.
func shapeLayoutAdmissionEnvelope(graph *LayoutGraph) (lookupExpansion, topLevelApplications uint64, err error) {
	if graph == nil || graph.terminal == nil || graph.terminal.parent == nil {
		return 0, 0, fontError(CodeShapeProviderInvalid, "shape_provider/admission/gsub", ErrShapeProviderInvalid)
	}
	const stretchFeatureTag uint32 = 0x73746368 // "stch"
	gpos := graph.terminal.gpos
	if gpos != nil {
		if uint64(len(gpos.features)) != graph.receipt.Tables[2].FeatureCount {
			return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gpos/model", ErrShapeInvariant)
		}
		for _, record := range gpos.features {
			if record.feature == nil {
				return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gpos/feature", ErrShapeInvariant)
			}
			if record.tag == stretchFeatureTag {
				return 0, 0, fontError(CodeShapeProviderInvalid, "shape_provider/admission/gpos/stch", ErrShapeProviderInvalid)
			}
			for _, lookupIndex := range record.feature.lookupIndices {
				if uint64(lookupIndex) >= uint64(len(gpos.lookups)) {
					return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gpos/feature_lookup", ErrShapeInvariant)
				}
			}
		}
	}
	gsub := graph.terminal.parent.gsub
	if gsub == nil {
		return 1, 0, nil
	}
	if uint64(len(gsub.features)) != graph.receipt.Tables[1].FeatureCount ||
		uint64(len(gsub.lookups)) != graph.receipt.Tables[1].LookupCount ||
		gsub.logical.maxExpansion != graph.receipt.Tables[1].Expansion {
		return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/model", ErrShapeInvariant)
	}
	// Count every FeatureRecord reference, including records that alias the same physical
	// Feature table. go-text retains the tags separately and can execute the shared lookup in
	// different shaping stages, so physical-table deduplication would understate R.
	for _, record := range gsub.features {
		if record.feature == nil {
			return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/feature", ErrShapeInvariant)
		}
		if record.tag == stretchFeatureTag {
			return 0, 0, fontError(CodeShapeProviderInvalid, "shape_provider/admission/gsub/stch", ErrShapeProviderInvalid)
		}
		for _, lookupIndex := range record.feature.lookupIndices {
			if uint64(lookupIndex) >= uint64(len(gsub.lookups)) {
				return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/feature_lookup", ErrShapeInvariant)
			}
		}
		topLevelApplications = shapeSaturatingAdd(
			topLevelApplications, uint64(len(record.feature.lookupIndices)), HardMaxShapeGlyphs+1,
		)
	}
	maxSequence := uint64(1)
	var inspectSubtable func(gsubSubtable) error
	inspectSubtable = func(subtable gsubSubtable) error {
		switch typed := subtable.(type) {
		case *gsubMultiple:
			for _, sequence := range typed.sequences {
				if sequence == nil || len(sequence.glyphs) == 0 {
					return fontError(CodeShapeInvariant, "shape_provider/admission/gsub/multiple", ErrShapeInvariant)
				}
				if uint64(len(sequence.glyphs)) > maxSequence {
					maxSequence = uint64(len(sequence.glyphs))
				}
			}
		case *gsubExtension:
			if typed.subtable == nil {
				return fontError(CodeShapeInvariant, "shape_provider/admission/gsub/extension", ErrShapeInvariant)
			}
			return inspectSubtable(typed.subtable)
		}
		return nil
	}
	for _, lookup := range gsub.lookups {
		if lookup == nil {
			return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/lookup", ErrShapeInvariant)
		}
		for _, subtable := range lookup.subtables {
			if subtable == nil {
				return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/subtable", ErrShapeInvariant)
			}
			if err := inspectSubtable(subtable); err != nil {
				return 0, 0, err
			}
		}
	}
	contextualApplications, ok := checkedAdd(gsub.logical.maxExpansion, 1)
	if !ok {
		return 0, 0, fontError(CodeShapeInvariant, "shape_provider/admission/gsub/expansion", ErrShapeInvariant)
	}
	lookupExpansion = shapeSaturatingPow(maxSequence, contextualApplications, HardMaxShapeGlyphs+1)
	return lookupExpansion, topLevelApplications, nil
}

func shapeSaturatingAdd(left, right, ceiling uint64) uint64 {
	if left >= ceiling || right >= ceiling-left {
		return ceiling
	}
	return left + right
}

func shapeSaturatingMultiply(left, right, ceiling uint64) uint64 {
	if left == 0 || right == 0 {
		return 0
	}
	if left > ceiling/right {
		return ceiling
	}
	result := left * right
	if result > ceiling {
		return ceiling
	}
	return result
}

func shapeSaturatingPow(base, exponent, ceiling uint64) uint64 {
	result := uint64(1)
	for exponent != 0 {
		if exponent&1 != 0 {
			result = shapeSaturatingMultiply(result, base, ceiling)
		}
		exponent >>= 1
		if exponent != 0 {
			base = shapeSaturatingMultiply(base, base, ceiling)
		}
	}
	return result
}

func makeShapeProviderReceipt(
	graphReceipt LayoutGraphReceipt, setReceipt OutlineSetReceipt, fontReceipt Receipt, core *Font,
) ShapeProviderReceipt {
	return ShapeProviderReceipt{
		SchemaVersion: ShapeProviderSchemaVersion, Profile: ShapeProviderReceiptProfileV1,
		ProviderProfile:              ShapeProviderProfileV1,
		DescriptorHashProfile:        ShapeProviderDescriptorHashProfileV1,
		DescriptorHash:               ShapeProviderDescriptorHash(),
		ProviderHashProfile:          ShapeProviderHashProfileV1,
		DependencyLockProfile:        buildidentity.TextShapeCandidateProfileV1,
		DependencyFingerprintProfile: buildidentity.TextShapeFingerprintProfileV1,
		DependencyFingerprint:        buildidentity.TextShapeCandidateFingerprint(),
		GoTextModuleVersion:          ShapeGoTextModuleVersionV1,
		EmbeddedHarfBuzzVersion:      ShapeEmbeddedHarfBuzzVersionV1,
		EmbeddedHarfBuzzCommit:       ShapeEmbeddedHarfBuzzCommitV1,
		BindingStatus:                ShapeProviderBindingStatusV1,
		CapabilityStatus:             ShapeProviderCapabilityStatusV1,
		LayoutGraphProfile:           graphReceipt.Profile,
		LayoutGraphHashProfile:       graphReceipt.GraphHashProfile,
		LayoutGraphHash:              graphReceipt.GraphHash,
		OutlineSetHashProfile:        setReceipt.OutlineSetHashProfile,
		OutlineSetHash:               setReceipt.OutlineSetHash,
		FontContentHashProfile:       fontReceipt.ContentHashProfile,
		FontContentHash:              fontReceipt.ContentHash,
		ClusterPolicy:                ShapeClusterPolicyV1,
		FeaturePolicy:                ShapeFeaturePolicyV1,
		DirectionNormalization:       ShapeDirectionNormalizationProfileV1,
		UnitsPerEm:                   core.unitsPerEm,
		GlyphCount:                   core.numGlyphs,
		EncodedBytes:                 uint64(len(core.encoded)),
		CmapMappingCount:             uint64(len(core.mappings)),
	}

}

func finalizeShapeProviderReceiptFromGraph(
	ctx context.Context, graphReceipt LayoutGraphReceipt, receipt ShapeProviderReceipt,
) (ShapeProviderReceipt, error) {
	work, working, retained, err := shapeProviderResources(graphReceipt, receipt)
	if err != nil {
		return ShapeProviderReceipt{}, err
	}
	receipt.WorkUnits, receipt.WorkingBytes, receipt.RetainedBytes = work, working, retained
	_, canonical, err := hashShapeProviderBinding(ctx, receipt)
	if err != nil {
		return ShapeProviderReceipt{}, err
	}
	receipt.CanonicalBinaryBytes = canonical
	receipt.WorkUnits, err = shapeCheckedAdd(receipt.WorkUnits, canonical, "shape_provider/resources/work")
	if err != nil {
		return ShapeProviderReceipt{}, err
	}
	receipt.ProviderHash, receipt.CanonicalBinaryBytes, err = hashShapeProviderBinding(ctx, receipt)
	if err != nil {
		return ShapeProviderReceipt{}, err
	}
	return receipt, nil
}

func normalizeShapeProviderLimits(value ShapeProviderLimits) (ShapeProviderLimits, error) {
	defaults := DefaultShapeProviderLimits()
	if value.MaxWorkUnits == 0 {
		value.MaxWorkUnits = defaults.MaxWorkUnits
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if value.MaxRetainedBytes == 0 {
		value.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.MaxWorkUnits > hardMaxShapeProviderWork || value.MaxWorkingBytes > hardMaxShapeProviderWorking ||
		value.MaxRetainedBytes > hardMaxShapeProviderRetained {
		return ShapeProviderLimits{}, fontError(CodeShapeLimitsInvalid, "shape_provider/limits", ErrLimitsInvalid)
	}
	return value, nil
}

func validateShapeDependencyIdentity() error {
	identity := buildidentity.TextShapeCandidate()
	if identity.SchemaVersion != buildidentity.TextShapeCandidateSchemaVersion ||
		identity.Profile != buildidentity.TextShapeCandidateProfileV1 ||
		identity.Status != buildidentity.TextShapeCandidateStatus ||
		identity.ProviderProfile != ShapeProviderProfileV1 ||
		identity.FingerprintProfile != buildidentity.TextShapeFingerprintProfileV1 ||
		identity.RequiredCGOEnabled || !identity.ImportedByCutly || identity.CapabilityAdvertised {
		return fontError(CodeShapeProviderInvalid, "shape_provider/dependency/identity", ErrShapeProviderInvalid)
	}
	moduleOK := false
	for _, module := range identity.Modules {
		if module.ModulePath == "github.com/go-text/typesetting" {
			moduleOK = module.Version == ShapeGoTextModuleVersionV1 &&
				module.ModuleSum == "h1:XZPUooClHY0Vf/rFyUyuPRNEkawARaFzLMQcXLSEyPk=" &&
				module.GoModSum == "h1:XZO1hD+nQVyvVa5IicQk7FsCa4PFQaJ2soWAP1f//68="
			break
		}
	}
	embeddedOK := false
	for _, embedded := range identity.EmbeddedSources {
		if embedded.Name == "HarfBuzz" {
			embeddedOK = embedded.Version == ShapeEmbeddedHarfBuzzVersionV1 &&
				embedded.Commit == ShapeEmbeddedHarfBuzzCommitV1 &&
				embedded.PortedByModule == "github.com/go-text/typesetting@"+ShapeGoTextModuleVersionV1
			break
		}
	}
	if !moduleOK || !embeddedOK {
		return fontError(CodeShapeProviderInvalid, "shape_provider/dependency/pin", ErrShapeProviderInvalid)
	}
	return nil
}

func replayShapeLayoutGraph(ctx context.Context, graph *LayoutGraph) (*OutlineSet, *Font, error) {
	source, core, err := inspectShapeLayoutGraph(graph)
	if err != nil {
		return nil, nil, err
	}
	receipt := graph.receipt
	limits := LayoutGraphLimits{
		MaxValidationCanonicalBinaryBytes: receipt.ValidationCanonicalBinaryBytes,
		MaxCanonicalBinaryBytes:           receipt.CanonicalBinaryBytes,
		MaxWorkUnits:                      receipt.WorkUnits,
		MaxWorkingBytes:                   receipt.WorkingBytes,
		MaxRetainedBytes:                  receipt.RetainedBytes,
	}
	rebuilt, rebuiltReceipt, err := BuildLayoutGraph(ctx, source, limits)
	if err != nil {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/replay", errors.Join(ErrShapeProviderInvalid, err))
	}
	if rebuilt == nil || rebuiltReceipt != receipt || rebuilt.terminal == nil || rebuilt.terminal.parent == nil ||
		rebuilt.terminal.parent.parent == nil || rebuilt.terminal.parent.parent.source != source {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/replay_receipt", ErrShapeProviderInvalid)
	}
	return source, core, nil
}

func inspectShapeLayoutGraph(graph *LayoutGraph) (*OutlineSet, *Font, error) {
	if graph == nil || graph.terminal == nil || graph.terminal.parent == nil || graph.terminal.parent.parent == nil {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/graph", ErrShapeProviderInvalid)
	}
	source := graph.terminal.parent.parent.source
	if source == nil || source.source == nil || source.source.source == nil || source.source.source.font == nil {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/source", ErrShapeProviderInvalid)
	}
	if err := validateLayoutGraphTerminal(graph.terminal, source); err != nil {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/terminal", errors.Join(ErrShapeProviderInvalid, err))
	}
	receipt := graph.receipt
	core := source.source.source.font
	if source.receipt.OutlineSetHash != receipt.SourceOutlineSetHash ||
		core.receipt.ContentHash != receipt.UpstreamContentHash || core.numGlyphs != receipt.GlyphCount {
		return nil, nil, fontError(CodeShapeProviderInvalid, "shape_provider/lineage/hashes", ErrShapeProviderInvalid)
	}
	return source, core, nil
}

func crossValidateGoTextFont(ctx context.Context, graph *LayoutGraph, core *Font, base *gotextfont.Font) error {
	if base.Upem() != core.unitsPerEm {
		return fontError(CodeShapeInvariant, "shape_provider/go_text/upem", ErrShapeInvariant)
	}
	face := gotextfont.NewFace(base)
	for glyphID := uint32(0); glyphID < core.numGlyphs; glyphID++ {
		if glyphID&1023 == 0 {
			if err := contextFailure(ctx, "shape_provider/go_text/metrics"); err != nil {
				return err
			}
		}
		metric, ok := core.HorizontalMetric(glyphID)
		if !ok || face.HorizontalAdvance(gotextfont.GID(glyphID)) != float32(metric.AdvanceWidth) {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/metrics", ErrShapeInvariant)
		}
	}
	for index, mapping := range core.mappings {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "shape_provider/go_text/cmap"); err != nil {
				return err
			}
		}
		glyph, ok := base.NominalGlyph(rune(mapping.codepoint))
		if !ok || uint32(glyph) != mapping.glyphID {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/cmap", ErrShapeInvariant)
		}
	}
	if err := crossValidateGoTextGDEF(ctx, graph, base); err != nil {
		return err
	}
	if err := crossValidateGoTextLayout(ctx, graph, LayoutTableGSUB, base.GSUB.Layout, base.GSUB.Lookups); err != nil {
		return err
	}
	if err := crossValidateGoTextLayout(ctx, graph, LayoutTableGPOS, base.GPOS.Layout, base.GPOS.Lookups); err != nil {
		return err
	}
	return nil
}

func crossValidateGoTextGDEF(ctx context.Context, graph *LayoutGraph, base *gotextfont.Font) error {
	table, ok := graph.Table(LayoutTableGDEF)
	if !ok {
		return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef", ErrShapeInvariant)
	}
	if !table.Present() {
		if base.GDEF.GlyphClassDef != nil || base.GDEF.MarkAttachClass != nil ||
			len(base.GDEF.MarkGlyphSetsDef.Coverages) != 0 {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/absent", ErrShapeInvariant)
		}
		return nil
	}
	for glyphID := uint32(0); glyphID < graph.receipt.GlyphCount; glyphID++ {
		if glyphID&1023 == 0 {
			if err := contextFailure(ctx, "shape_provider/go_text/gdef/classes"); err != nil {
				return err
			}
		}
		wantGlyph, valid := table.GlyphClass(glyphID)
		if !valid {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/glyph_class", ErrShapeInvariant)
		}
		gotGlyph := uint16(0)
		if base.GDEF.GlyphClassDef != nil {
			if class, found := base.GDEF.GlyphClassDef.Class(tables.GlyphID(glyphID)); found {
				gotGlyph = class
			}
		}
		wantMark, valid := table.MarkAttachmentClass(glyphID)
		if !valid {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/mark_class", ErrShapeInvariant)
		}
		gotMark := uint16(0)
		if base.GDEF.MarkAttachClass != nil {
			if class, found := base.GDEF.MarkAttachClass.Class(tables.GlyphID(glyphID)); found {
				gotMark = class
			}
		}
		if gotGlyph != wantGlyph || gotMark != wantMark {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/classes", ErrShapeInvariant)
		}
	}
	gdef := table.gdef()
	if gdef == nil || len(gdef.markGlyphSets) != len(base.GDEF.MarkGlyphSetsDef.Coverages) {
		return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/mark_sets", ErrShapeInvariant)
	}
	for setIndex, wantCoverage := range gdef.markGlyphSets {
		gotCoverage := base.GDEF.MarkGlyphSetsDef.Coverages[setIndex]
		if wantCoverage == nil || gotCoverage == nil || uint64(wantCoverage.glyphCount) != uint64(gotCoverage.Len()) {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/mark_set", ErrShapeInvariant)
		}
		for ordinal := uint64(0); ordinal < uint64(wantCoverage.glyphCount); ordinal++ {
			if ordinal&1023 == 0 {
				if err := contextFailure(ctx, "shape_provider/go_text/gdef/mark_set"); err != nil {
					return err
				}
			}
			glyph, valid := wantCoverage.glyphAt(ordinal)
			gotOrdinal, found := gotCoverage.Index(tables.GlyphID(glyph))
			if !valid || !found || uint64(gotOrdinal) != ordinal {
				return fontError(CodeShapeInvariant, "shape_provider/go_text/gdef/mark_set", ErrShapeInvariant)
			}
		}
	}
	return nil
}

type goTextLayoutLookup interface {
	lookupFlag() uint16
	markFilteringSet() uint16
	subtableCount() int
}

type goTextGSUBLookup gotextfont.GSUBLookup

func (value goTextGSUBLookup) lookupFlag() uint16       { return value.Flag }
func (value goTextGSUBLookup) markFilteringSet() uint16 { return value.MarkFilteringSet }
func (value goTextGSUBLookup) subtableCount() int       { return len(value.Subtables) }

type goTextGPOSLookup gotextfont.GPOSLookup

func (value goTextGPOSLookup) lookupFlag() uint16       { return value.Flag }
func (value goTextGPOSLookup) markFilteringSet() uint16 { return value.MarkFilteringSet }
func (value goTextGPOSLookup) subtableCount() int       { return len(value.Subtables) }

func crossValidateGoTextLayout[T gotextfont.GSUBLookup | gotextfont.GPOSLookup](
	ctx context.Context, graph *LayoutGraph, tag LayoutTableTag, layout gotextfont.Layout, raw []T,
) error {
	table, ok := graph.Table(tag)
	if !ok || table.ScriptCount() != uint64(len(layout.Scripts)) ||
		table.FeatureCount() != uint64(len(layout.Features)) || table.LookupCount() != uint64(len(raw)) ||
		len(layout.FeatureVariations) != 0 {
		return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/cardinality", ErrShapeInvariant)
	}
	for index, script := range layout.Scripts {
		if index&255 == 0 {
			if err := contextFailure(ctx, "shape_provider/go_text/layout/scripts"); err != nil {
				return err
			}
		}
		view, valid := table.Script(uint64(index))
		if !valid || uint32(script.Tag) != tagUint32(view.Tag()) ||
			(script.DefaultLangSys != nil) != hasDefaultLanguage(view) ||
			len(script.LangSys) != int(view.LanguageCount()) || len(script.LangSysRecords) != len(script.LangSys) {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/script", ErrShapeInvariant)
		}
		if script.DefaultLangSys != nil {
			languageView, _ := view.DefaultLanguage()
			if !equalGoTextLangSys(languageView, *script.DefaultLangSys) {
				return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/default_language", ErrShapeInvariant)
			}
		}
		for languageIndex, language := range script.LangSys {
			languageView, exists := view.Language(uint64(languageIndex))
			if !exists || uint32(script.LangSysRecords[languageIndex].Tag) != tagUint32(languageView.Tag()) ||
				!equalGoTextLangSys(languageView, language) {
				return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/language", ErrShapeInvariant)
			}
		}
	}
	for index, feature := range layout.Features {
		view, valid := table.Feature(uint64(index))
		if !valid || uint32(feature.Tag) != tagUint32(view.Tag()) ||
			len(feature.LookupListIndices) != int(view.LookupCount()) {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/feature", ErrShapeInvariant)
		}
		for lookupIndex, got := range feature.LookupListIndices {
			want, exists := view.LookupIndex(uint64(lookupIndex))
			if !exists || got != want {
				return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/feature_lookup", ErrShapeInvariant)
			}
		}
	}
	for index, lookup := range raw {
		view, valid := table.Lookup(uint64(index))
		if !valid {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/lookup", ErrShapeInvariant)
		}
		var normalized goTextLayoutLookup
		switch typed := any(lookup).(type) {
		case gotextfont.GSUBLookup:
			normalized = goTextGSUBLookup(typed)
		case gotextfont.GPOSLookup:
			normalized = goTextGPOSLookup(typed)
		default:
			return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/lookup_type", ErrShapeInvariant)
		}
		wantMarkSet, hasMarkSet := view.MarkFilteringSet()
		if normalized.lookupFlag() != view.Flags() || normalized.subtableCount() != int(view.SubtableCount()) ||
			hasMarkSet != (normalized.lookupFlag()&gotextfont.UseMarkFilteringSet != 0) ||
			(hasMarkSet && normalized.markFilteringSet() != wantMarkSet) {
			return fontError(CodeShapeInvariant, "shape_provider/go_text/layout/lookup_metadata", ErrShapeInvariant)
		}
	}
	return nil
}

func hasDefaultLanguage(value LayoutScriptView) bool {
	_, ok := value.DefaultLanguage()
	return ok
}

func equalGoTextLangSys(view LayoutLanguageView, language tables.LangSys) bool {
	required, hasRequired := view.RequiredFeatureIndex()
	if hasRequired != (language.RequiredFeatureIndex != math.MaxUint16) ||
		(hasRequired && required != language.RequiredFeatureIndex) ||
		view.FeatureCount() != uint64(len(language.FeatureIndices)) {
		return false
	}
	for index, got := range language.FeatureIndices {
		want, ok := view.FeatureIndex(uint64(index))
		if !ok || got != want {
			return false
		}
	}
	return true
}

func tagUint32(value LayoutTag) uint32 {
	return uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
}

func shapeProviderResources(graph LayoutGraphReceipt, receipt ShapeProviderReceipt) (uint64, uint64, uint64, error) {
	encodedWork, ok := checkedMultiply(receipt.EncodedBytes, 8)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/work", ErrResourceLimit)
	}
	mapWork, ok := checkedMultiply(receipt.CmapMappingCount, 8)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/work", ErrResourceLimit)
	}
	glyphWork, ok := checkedMultiply(uint64(receipt.GlyphCount), 16)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/work", ErrResourceLimit)
	}
	work := graph.WorkUnits
	for _, increment := range []uint64{encodedWork, mapWork, glyphWork} {
		var err error
		work, err = shapeCheckedAdd(work, increment, "shape_provider/resources/work")
		if err != nil {
			return 0, 0, 0, err
		}
	}
	workingIncrement, ok := checkedMultiply(receipt.EncodedBytes, 2)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/working", ErrResourceLimit)
	}
	working, err := shapeCheckedAdd(shapeLogicalFixedWorkingBytes, workingIncrement, "shape_provider/resources/working")
	if err != nil {
		return 0, 0, 0, err
	}
	if working < graph.WorkingBytes {
		working = graph.WorkingBytes
	}
	retainedEncoded, ok := checkedMultiply(receipt.EncodedBytes, 4)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/retained", ErrResourceLimit)
	}
	retainedGlyphs, ok := checkedMultiply(uint64(receipt.GlyphCount), 256)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/retained", ErrResourceLimit)
	}
	retainedMaps, ok := checkedMultiply(receipt.CmapMappingCount, 16)
	if !ok {
		return 0, 0, 0, fontError(CodeShapeResourceLimit, "shape_provider/resources/retained", ErrResourceLimit)
	}
	retained := graph.RetainedBytes
	for _, increment := range []uint64{shapeLogicalFixedRetainedBytes, retainedEncoded, retainedGlyphs, retainedMaps, shapeProviderReceiptStringBytes(receipt)} {
		retained, err = shapeCheckedAdd(retained, increment, "shape_provider/resources/retained")
		if err != nil {
			return 0, 0, 0, err
		}
	}
	return work, working, retained, nil
}

func shapeProviderReceiptStringBytes(value ShapeProviderReceipt) uint64 {
	return shapeDigestStringBytes + uint64(len(value.Profile)+len(value.ProviderProfile)+len(value.DescriptorHashProfile)+
		len(value.DescriptorHash)+len(value.ProviderHashProfile)+len(value.DependencyLockProfile)+
		len(value.DependencyFingerprintProfile)+len(value.DependencyFingerprint)+len(value.GoTextModuleVersion)+
		len(value.EmbeddedHarfBuzzVersion)+len(value.EmbeddedHarfBuzzCommit)+len(value.BindingStatus)+
		len(value.CapabilityStatus)+len(value.LayoutGraphProfile)+len(value.LayoutGraphHashProfile)+
		len(value.LayoutGraphHash)+len(value.OutlineSetHashProfile)+len(value.OutlineSetHash)+
		len(value.FontContentHashProfile)+len(value.FontContentHash)+len(value.ClusterPolicy)+
		len(value.FeaturePolicy)+len(value.DirectionNormalization))
}

func shapeCheckedAdd(left, right uint64, path string) (uint64, error) {
	result, ok := checkedAdd(left, right)
	if !ok {
		return 0, fontError(CodeShapeResourceLimit, path, ErrResourceLimit)
	}
	return result, nil
}
