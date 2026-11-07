package sfntglyf

import (
	"context"
	"errors"
)

// BindLayoutSource publishes one raster-facing, receipt-bound view over the
// exact OutlineSet retained by this ShapeProvider. The complete private source
// lineage is replayed before publication; no raw font or go-text object leaves
// the provider.
func (provider *ShapeProvider) BindLayoutSource(ctx context.Context, limits LayoutSourceLimits) (
	result *LayoutSource, receipt LayoutSourceReceipt, err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = LayoutSourceReceipt{}
			err = fontError(CodeAllocationFailed, "layout_source/bind/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = LayoutSourceReceipt{}
		}
	}()
	if err = contextFailure(ctx, "layout_source/bind/context"); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	normalized, err := normalizeLayoutSourceLimits(limits)
	if err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	if provider == nil || provider.graph == nil || provider.source == nil || provider.base == nil ||
		provider.gsubLookupExpansion == 0 || provider.receipt.ProviderHash == "" {
		return nil, LayoutSourceReceipt{}, layoutSourceInvalid("layout_source/bind/provider", nil)
	}
	if err := validateShapeDependencyIdentity(); err != nil {
		return nil, LayoutSourceReceipt{}, layoutSourceInvalid("layout_source/bind/dependency", err)
	}

	source, core, err := inspectShapeLayoutGraph(provider.graph)
	if err != nil {
		return nil, LayoutSourceReceipt{}, mapLayoutSourceBindingError(ctx, "layout_source/bind/lineage/inspect", err)
	}
	if source != provider.source || !layoutSourceLineageMatches(provider, source, core) {
		return nil, LayoutSourceReceipt{}, layoutSourceInvalid("layout_source/bind/lineage/binding", nil)
	}
	if err := validateLayoutSourceProviderReceipt(ctx, provider, source, core); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	receipt, err = makeLayoutSourceReceipt(ctx, provider, source, core)
	if err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	if err := admitLayoutSource(receipt, normalized); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}

	// This is the sole live replay. BuildLayoutGraph recursively rebinds the
	// complete OutlineSet -> GeometryFont -> OutlineFont -> Font lineage before
	// validating GDEF/GSUB/GPOS again under receipt-exact limits.
	replayedSource, replayedCore, err := replayShapeLayoutGraph(ctx, provider.graph)
	if err != nil {
		return nil, LayoutSourceReceipt{}, mapLayoutSourceBindingError(ctx, "layout_source/bind/lineage/replay", err)
	}
	if replayedSource != source || replayedCore != core || !layoutSourceLineageMatches(provider, replayedSource, replayedCore) {
		return nil, LayoutSourceReceipt{}, layoutSourceInvalid("layout_source/bind/lineage/replay_identity", nil)
	}
	if err := validateLayoutSourceProviderReceipt(ctx, provider, replayedSource, replayedCore); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	if err := validateLayoutSourceProviderOperations(ctx, provider, replayedCore); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	actual, err := makeLayoutSourceReceipt(ctx, provider, replayedSource, replayedCore)
	if err != nil {
		return nil, LayoutSourceReceipt{}, mapLayoutSourceBindingError(ctx, "layout_source/bind/receipt/replay", err)
	}
	if actual != receipt {
		return nil, LayoutSourceReceipt{}, layoutSourceInvariant("layout_source/bind/receipt/replay", nil)
	}
	if err = contextFailure(ctx, "layout_source/bind/publish"); err != nil {
		return nil, LayoutSourceReceipt{}, err
	}
	result = &LayoutSource{provider: provider, outlines: source, receipt: receipt}
	return result, receipt, nil
}

func validateLayoutSourceProviderReceipt(
	ctx context.Context, provider *ShapeProvider, source *OutlineSet, core *Font,
) error {
	if !layoutSourceLineageMatches(provider, source, core) {
		return layoutSourceInvalid("layout_source/bind/provider/lineage", nil)
	}
	expected := makeShapeProviderReceipt(provider.graph.receipt, source.receipt, core.receipt, core)
	expected, err := finalizeShapeProviderReceiptFromGraph(ctx, provider.graph.receipt, expected)
	if err != nil {
		return mapLayoutSourceBindingError(ctx, "layout_source/bind/provider/hash", err)
	}
	// Reconstructing the entire receipt is intentional: ShapeProvider's legacy
	// binding preimage uses fixed schema/hash-profile constants. Exact receipt
	// equality prevents either of those public fields (or any other provider
	// identity/resource field) from being changed without rejection here.
	if expected != provider.receipt {
		return layoutSourceInvalid("layout_source/bind/provider/receipt", nil)
	}
	return nil
}

func validateLayoutSourceProviderOperations(ctx context.Context, provider *ShapeProvider, core *Font) error {
	if err := contextFailure(ctx, "layout_source/bind/provider/operations"); err != nil {
		return err
	}
	expansion, applications, err := shapeLayoutAdmissionEnvelope(provider.graph)
	if err != nil {
		return mapLayoutSourceBindingError(ctx, "layout_source/bind/provider/admission", err)
	}
	if expansion != provider.gsubLookupExpansion || applications != provider.gsubTopLevelApplications {
		return layoutSourceInvalid("layout_source/bind/provider/admission", nil)
	}
	// The base font remains private, but it is part of the retained provider
	// trust root. Re-cross-validate it only after scalar LayoutSource admission
	// so insufficient caller limits fail before this proportional replay.
	if err := crossValidateGoTextFont(ctx, provider.graph, core, provider.base); err != nil {
		return mapLayoutSourceBindingError(ctx, "layout_source/bind/provider/go_text", err)
	}
	return nil
}

func layoutSourceLineageMatches(provider *ShapeProvider, source *OutlineSet, core *Font) bool {
	if provider == nil || source == nil || core == nil || provider.graph == nil || provider.source != source ||
		source.source == nil || source.source.source == nil || source.source.source.font != core {
		return false
	}
	graphReceipt, setReceipt, fontReceipt := provider.graph.receipt, source.receipt, core.receipt
	if !shapeProviderReceiptMatchesLineage(provider.receipt, graphReceipt, setReceipt, fontReceipt) ||
		fontReceipt.Profile != ReceiptProfileV1 || setReceipt.Profile != OutlineSetReceiptProfileV1 ||
		graphReceipt.Profile != LayoutGraphReceiptProfileV1 ||
		fontReceipt.NumGlyphs != core.numGlyphs || fontReceipt.UnitsPerEm != core.unitsPerEm ||
		fontReceipt.HheaAscender != core.hheaAscender || provider.receipt.UnitsPerEm != core.unitsPerEm ||
		provider.receipt.GlyphCount != core.numGlyphs || setReceipt.GlyphCount != core.numGlyphs ||
		graphReceipt.GlyphCount != core.numGlyphs || uint64(len(source.glyphs)) != uint64(core.numGlyphs) ||
		uint64(len(source.commands)) != setReceipt.CommandInstances || source.GlyphCount() != core.numGlyphs {
		return false
	}
	if setReceipt.OutlineProfile != OutlineSetOutlineProfileV1 ||
		setReceipt.CommandStatus != OutlineSetCommandStatusV1 ||
		setReceipt.RoundingStatus != OutlineSetRoundingStatusV1 ||
		setReceipt.HintingStatus != OutlineSetHintingStatusV1 ||
		graphReceipt.VariableLayoutStatus != LayoutGraphVariableLayoutStatusV1 {
		return false
	}
	return true
}

func makeLayoutSourceReceipt(ctx context.Context, provider *ShapeProvider, source *OutlineSet, core *Font) (LayoutSourceReceipt, error) {
	if !layoutSourceLineageMatches(provider, source, core) {
		return LayoutSourceReceipt{}, layoutSourceInvalid("layout_source/receipt/lineage", nil)
	}
	setReceipt, graphReceipt, fontReceipt := source.receipt, provider.graph.receipt, core.receipt
	result := LayoutSourceReceipt{
		SchemaVersion: LayoutSourceSchemaVersion, Profile: LayoutSourceReceiptProfileV1,
		LayoutSourceProfile:   LayoutSourceProfileV1,
		DescriptorHashProfile: LayoutSourceDescriptorHashProfileV1,
		DescriptorHash:        LayoutSourceDescriptorHash(), LayoutSourceHashProfile: LayoutSourceHashProfileV1,
		ShapeProviderProfile:         provider.receipt.ProviderProfile,
		ShapeProviderHashProfile:     provider.receipt.ProviderHashProfile,
		ShapeProviderHash:            provider.receipt.ProviderHash,
		DependencyFingerprintProfile: provider.receipt.DependencyFingerprintProfile,
		DependencyFingerprint:        provider.receipt.DependencyFingerprint,
		LayoutGraphProfile:           graphReceipt.Profile, LayoutGraphHashProfile: graphReceipt.GraphHashProfile,
		LayoutGraphHash:   graphReceipt.GraphHash,
		OutlineSetProfile: setReceipt.Profile, OutlineSetHashProfile: setReceipt.OutlineSetHashProfile,
		OutlineSetHash:     setReceipt.OutlineSetHash,
		FontReceiptProfile: fontReceipt.Profile, FontContentHashProfile: fontReceipt.ContentHashProfile,
		FontContentHash: fontReceipt.ContentHash,
		BindingStatus:   LayoutSourceBindingStatusV1, OutlineProfile: setReceipt.OutlineProfile,
		CommandStatus: setReceipt.CommandStatus, RoundingStatus: setReceipt.RoundingStatus,
		HintingStatus: setReceipt.HintingStatus, LayoutStatus: LayoutSourceLayoutStatusV1,
		VariableLayoutStatus: graphReceipt.VariableLayoutStatus,
		RasterStatus:         LayoutSourceRasterStatusV1, CapabilityStatus: LayoutSourceCapabilityStatusV1,
		UnitsPerEm: core.unitsPerEm, HheaAscender: core.hheaAscender, GlyphCount: core.numGlyphs,
		GlyphsWithContours: setReceipt.GlyphsWithContours,
		ContourCount:       setReceipt.ExpandedContourInstances, CommandCount: setReceipt.CommandInstances,
		CommandPointOccurrences: setReceipt.CommandPointOccurrences,
		MaxCommandsPerGlyph:     setReceipt.MaxCommandsPerGlyph,
	}
	baseWork, working, retained, err := layoutSourceResources(provider.receipt, result)
	if err != nil {
		return LayoutSourceReceipt{}, err
	}
	result.WorkUnits, result.WorkingBytes, result.RetainedBytes = baseWork, working, retained
	_, canonical, err := hashLayoutSourceBinding(ctx, result)
	if err != nil {
		return LayoutSourceReceipt{}, err
	}
	result.CanonicalBinaryBytes = canonical
	result.WorkUnits, err = layoutSourceCheckedAdd(result.WorkUnits, canonical, "layout_source/resources/work")
	if err != nil {
		return LayoutSourceReceipt{}, err
	}
	var actual uint64
	result.LayoutSourceHash, actual, err = hashLayoutSourceBinding(ctx, result)
	if err != nil {
		return LayoutSourceReceipt{}, err
	}
	if actual != canonical {
		return LayoutSourceReceipt{}, layoutSourceInvariant("layout_source/hash/canonical", nil)
	}
	return result, nil
}

func admitLayoutSource(receipt LayoutSourceReceipt, limits LayoutSourceLimits) error {
	if uint64(receipt.GlyphCount) > limits.MaxGlyphs || receipt.ContourCount > limits.MaxContours ||
		receipt.CommandCount > limits.MaxCommands ||
		receipt.CommandPointOccurrences > limits.MaxCommandPointOccurrences ||
		receipt.MaxCommandsPerGlyph > limits.MaxCommandsPerGlyph {
		return fontError(CodeLayoutSourceResource, "layout_source/resources/counts", ErrResourceLimit)
	}
	if receipt.CanonicalBinaryBytes > limits.MaxCanonicalBinaryBytes ||
		receipt.WorkUnits > limits.MaxWorkUnits || receipt.WorkingBytes > limits.MaxWorkingBytes ||
		receipt.RetainedBytes > limits.MaxRetainedBytes {
		return fontError(CodeLayoutSourceResource, "layout_source/resources/portable", ErrResourceLimit)
	}
	return nil
}

func layoutSourceResources(provider ShapeProviderReceipt, receipt LayoutSourceReceipt) (uint64, uint64, uint64, error) {
	work := provider.WorkUnits
	working := provider.WorkingBytes
	if working < layoutSourceFixedWorkingBytes {
		working = layoutSourceFixedWorkingBytes
	}
	retained, err := layoutSourceCheckedAdd(provider.RetainedBytes, layoutSourceFixedRetainedBytes, "layout_source/resources/retained")
	if err == nil {
		var strings uint64
		strings, err = layoutSourceReceiptStringBytes(receipt)
		if err == nil {
			retained, err = layoutSourceCheckedAdd(retained, strings, "layout_source/resources/retained")
		}
	}
	if err != nil {
		return 0, 0, 0, err
	}
	return work, working, retained, nil
}

func layoutSourceReceiptStringBytes(value LayoutSourceReceipt) (uint64, error) {
	strings := [...]string{
		value.Profile, value.LayoutSourceProfile, value.DescriptorHashProfile, value.DescriptorHash,
		value.LayoutSourceHashProfile, value.ShapeProviderProfile, value.ShapeProviderHashProfile,
		value.ShapeProviderHash, value.DependencyFingerprintProfile, value.DependencyFingerprint,
		value.LayoutGraphProfile, value.LayoutGraphHashProfile, value.LayoutGraphHash,
		value.OutlineSetProfile, value.OutlineSetHashProfile, value.OutlineSetHash,
		value.FontReceiptProfile, value.FontContentHashProfile, value.FontContentHash,
		value.BindingStatus, value.OutlineProfile, value.CommandStatus, value.RoundingStatus,
		value.HintingStatus, value.LayoutStatus, value.VariableLayoutStatus,
		value.RasterStatus, value.CapabilityStatus,
	}
	total := layoutSourceDigestStringBytes // LayoutSourceHash is filled after resources.
	for _, text := range strings {
		var err error
		total, err = layoutSourceCheckedAdd(total, uint64(len(text)), "layout_source/resources/receipt_strings")
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func layoutSourceCheckedAdd(left, right uint64, path string) (uint64, error) {
	result, ok := checkedAdd(left, right)
	if !ok {
		return 0, fontError(CodeLayoutSourceResource, path, ErrResourceLimit)
	}
	return result, nil
}

func layoutSourceInvalid(path string, cause error) error {
	return fontError(CodeLayoutSourceInvalid, path, errors.Join(ErrLayoutSourceInvalid, cause))
}

func layoutSourceInvariant(path string, cause error) error {
	return fontError(CodeLayoutSourceInvariant, path, errors.Join(ErrLayoutSourceInvariant, cause))
}

func mapLayoutSourceBindingError(ctx context.Context, path string, cause error) error {
	if cause == nil {
		return nil
	}
	if errors.Is(cause, ErrCanceled) || errors.Is(cause, ErrContextInvalid) {
		if contextErr := contextFailure(ctx, path); contextErr != nil {
			return contextErr
		}
		if errors.Is(cause, ErrContextInvalid) {
			return fontError(CodeContextInvalid, path, errors.Join(ErrContextInvalid, cause))
		}
		return fontError(CodeCanceled, path, errors.Join(ErrCanceled, cause))
	}
	if errors.Is(cause, ErrAllocationFailed) {
		return fontError(CodeAllocationFailed, path, errors.Join(ErrAllocationFailed, cause))
	}
	if errors.Is(cause, ErrResourceLimit) {
		return fontError(CodeLayoutSourceResource, path, errors.Join(ErrResourceLimit, cause))
	}
	if errors.Is(cause, ErrLimitsInvalid) {
		return fontError(CodeLayoutSourceLimits, path, errors.Join(ErrLimitsInvalid, cause))
	}
	return layoutSourceInvalid(path, cause)
}
