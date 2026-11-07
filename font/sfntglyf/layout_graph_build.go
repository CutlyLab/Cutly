package sfntglyf

import (
	"context"
	"errors"
)

// BuildLayoutGraph atomically validates and publishes one immutable static
// OpenType GDEF+GSUB+GPOS graph. It intentionally performs no shaping.
func BuildLayoutGraph(ctx context.Context, source *OutlineSet, limits LayoutGraphLimits) (
	result *LayoutGraph, receipt LayoutGraphReceipt, err error,
) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = LayoutGraphReceipt{}
			err = fontError(CodeAllocationFailed, "layout_graph/build/allocation", ErrAllocationFailed)
		}
		if err != nil {
			result = nil
			receipt = LayoutGraphReceipt{}
		}
	}()
	if err = contextFailure(ctx, "layout_graph/build/context"); err != nil {
		return nil, LayoutGraphReceipt{}, err
	}
	normalized, err := normalizeLayoutGraphLimits(limits)
	if err != nil {
		return nil, LayoutGraphReceipt{}, err
	}
	terminal, _, err := buildGPOSFoundation(ctx, source, layoutGraphFoundationLimits(normalized))
	if err != nil {
		return nil, LayoutGraphReceipt{}, mapLayoutGraphValidationError(err)
	}
	if err := validateLayoutGraphTerminal(terminal, source); err != nil {
		return nil, LayoutGraphReceipt{}, err
	}
	receipt, err = makeLayoutGraphReceipt(terminal)
	if err != nil {
		return nil, LayoutGraphReceipt{}, err
	}
	if receipt.ValidationCanonicalBinaryBytes > normalized.MaxValidationCanonicalBinaryBytes {
		return nil, LayoutGraphReceipt{}, fontError(CodeLayoutGraphResource, "layout_graph/resources/validation_canonical", ErrResourceLimit)
	}
	receipt.GraphHash, receipt.CanonicalBinaryBytes, err = hashLayoutGraph(ctx, receipt, normalized.MaxCanonicalBinaryBytes)
	if err != nil {
		return nil, LayoutGraphReceipt{}, mapLayoutGraphHashError(err)
	}
	receipt.WorkUnits, err = layoutGraphCheckedAdd(terminal.receipt.WorkUnits, receipt.CanonicalBinaryBytes, "layout_graph/resources/work")
	if err != nil || receipt.WorkUnits > normalized.MaxWorkUnits {
		return nil, LayoutGraphReceipt{}, fontError(CodeLayoutGraphResource, "layout_graph/resources/work", errors.Join(ErrResourceLimit, err))
	}
	receipt.WorkingBytes = terminal.receipt.WorkingBytes
	if receipt.WorkingBytes < layoutGraphFixedWorkingBytes {
		receipt.WorkingBytes = layoutGraphFixedWorkingBytes
	}
	if receipt.WorkingBytes > normalized.MaxWorkingBytes {
		return nil, LayoutGraphReceipt{}, fontError(CodeLayoutGraphResource, "layout_graph/resources/working", ErrResourceLimit)
	}
	receipt.RetainedBytes, err = layoutGraphCheckedAdd(terminal.receipt.RetainedBytes, layoutGraphFixedRetainedBytes, "layout_graph/resources/retained")
	if err == nil {
		receipt.RetainedBytes, err = layoutGraphCheckedAdd(receipt.RetainedBytes, layoutGraphReceiptStringBytes(receipt), "layout_graph/resources/retained")
	}
	if err != nil || receipt.RetainedBytes > normalized.MaxRetainedBytes {
		return nil, LayoutGraphReceipt{}, fontError(CodeLayoutGraphResource, "layout_graph/resources/retained", errors.Join(ErrResourceLimit, err))
	}
	if err = contextFailure(ctx, "layout_graph/build/publish"); err != nil {
		return nil, LayoutGraphReceipt{}, err
	}
	result = &LayoutGraph{terminal: terminal, receipt: receipt}
	return result, receipt, nil
}

func validateLayoutGraphTerminal(value *gposValidationFoundation, source *OutlineSet) error {
	if value == nil || value.parent == nil || value.parent.parent == nil || value.parent.parent.source != source ||
		value.parent.receipt.FoundationHash != value.receipt.ParentFoundationHash ||
		value.receipt.DescriptorHash != gposFoundationDescriptorIdentityV1 ||
		value.parent.receipt.DescriptorHash != gsubFoundationDescriptorIdentityV1 ||
		value.parent.parent.receipt.DescriptorHash != layoutFoundationDescriptorIdentityV1 {
		return fontError(CodeLayoutGraphInvariant, "layout_graph/build/terminal", ErrInvariantFailed)
	}
	if value.parent.parent.receipt.SourceOutlineSetHash != value.receipt.SourceOutlineSetHash ||
		value.parent.receipt.SourceOutlineSetHash != value.receipt.SourceOutlineSetHash ||
		value.parent.parent.receipt.UpstreamContentHash != value.receipt.UpstreamContentHash ||
		value.parent.receipt.UpstreamContentHash != value.receipt.UpstreamContentHash {
		return fontError(CodeLayoutGraphInvariant, "layout_graph/build/lineage", ErrInvariantFailed)
	}
	return nil
}

func makeLayoutGraphReceipt(terminal *gposValidationFoundation) (LayoutGraphReceipt, error) {
	parent := terminal.parent
	gdefParent := parent.parent
	validationBytes, err := layoutGraphCheckedAdd(gdefParent.receipt.CanonicalBinaryBytes, parent.receipt.CanonicalBinaryBytes, "layout_graph/receipt/validation_bytes")
	if err == nil {
		validationBytes, err = layoutGraphCheckedAdd(validationBytes, terminal.receipt.CanonicalBinaryBytes, "layout_graph/receipt/validation_bytes")
	}
	if err != nil {
		return LayoutGraphReceipt{}, err
	}
	result := LayoutGraphReceipt{
		SchemaVersion: LayoutGraphSchemaVersion, Profile: LayoutGraphReceiptProfileV1,
		SourceProfile:                  terminal.receipt.SourceProfile,
		SourceDescriptorHashProfile:    gdefParent.receipt.SourceDescriptorHashProfile,
		SourceDescriptorHash:           gdefParent.receipt.SourceDescriptorHash,
		SourceOutlineSetHashProfile:    terminal.receipt.SourceOutlineSetHashProfile,
		SourceOutlineSetHash:           terminal.receipt.SourceOutlineSetHash,
		UpstreamContentHashProfile:     terminal.receipt.UpstreamContentHashProfile,
		UpstreamContentHash:            terminal.receipt.UpstreamContentHash,
		DescriptorHashProfile:          LayoutGraphDescriptorHashProfileV1,
		DescriptorHash:                 layoutGraphDescriptorIdentityV1,
		GraphHashProfile:               LayoutGraphCanonicalHashProfileV1,
		GDEFFoundationHashProfile:      gdefParent.receipt.FoundationHashProfile,
		GDEFFoundationHash:             gdefParent.receipt.FoundationHash,
		GSUBFoundationHashProfile:      parent.receipt.FoundationHashProfile,
		GSUBFoundationHash:             parent.receipt.FoundationHash,
		GPOSFoundationHashProfile:      terminal.receipt.FoundationHashProfile,
		GPOSFoundationHash:             terminal.receipt.FoundationHash,
		ValidationProfile:              LayoutGraphValidationProfileV1,
		CapabilityStatus:               LayoutGraphCapabilityStatusV1,
		LookupExecutionStatus:          LayoutGraphLookupExecutionStatusV1,
		VariableLayoutStatus:           LayoutGraphVariableLayoutStatusV1,
		GlyphCount:                     terminal.receipt.GlyphCount,
		ValidationCanonicalBinaryBytes: validationBytes,
	}
	models := [3]struct {
		binding                    layoutFoundationTableReceipt
		major, minor               uint16
		scripts, features, lookups uint64
		nodes, edges               uint64
		logicalEdges               uint64
		logicalDepth               uint32
		expansion                  uint64
	}{
		{binding: terminal.receipt.Tables[0]},
		{binding: terminal.receipt.Tables[1]},
		{binding: terminal.receipt.Tables[2]},
	}
	if gdef, ok := gdefParent.gdef.(*gdefFoundation); ok && gdef != nil {
		models[0].major, models[0].minor = gdef.majorVersion, gdef.minorVersion
		models[0].nodes, models[0].edges = uint64(len(gdef.nodes)), uint64(len(gdef.edges))
	}
	if gsub := parent.gsub; gsub != nil {
		models[1].major, models[1].minor = gsub.majorVersion, gsub.minorVersion
		models[1].scripts, models[1].features, models[1].lookups = uint64(len(gsub.scripts)), uint64(len(gsub.features)), uint64(len(gsub.lookups))
		models[1].nodes, models[1].edges = uint64(len(gsub.nodes)), uint64(len(gsub.edges))
		models[1].logicalEdges, models[1].logicalDepth, models[1].expansion = uint64(len(gsub.logical.edges)), gsub.logical.maxDepth, gsub.logical.maxExpansion
	}
	if gpos := terminal.gpos; gpos != nil {
		models[2].major, models[2].minor = gpos.majorVersion, gpos.minorVersion
		models[2].scripts, models[2].features, models[2].lookups = uint64(len(gpos.scripts)), uint64(len(gpos.features)), uint64(len(gpos.lookups))
		models[2].nodes, models[2].edges = uint64(len(gpos.nodes)), uint64(len(gpos.edges))
		models[2].logicalEdges, models[2].logicalDepth, models[2].expansion = uint64(len(gpos.logical.edges)), gpos.logical.maxDepth, gpos.logical.maxExpansion
	}
	for index, model := range models {
		result.Tables[index] = LayoutGraphTableReceipt{
			Tag: LayoutTableTag(model.binding.Tag), Present: model.binding.Present,
			FileOffset: model.binding.FileOffset, Length: model.binding.Length, Checksum: model.binding.Checksum,
			HashProfile: model.binding.HashProfile, ContentHash: model.binding.ContentHash,
			MajorVersion: model.major, MinorVersion: model.minor,
			ScriptCount: model.scripts, FeatureCount: model.features, LookupCount: model.lookups,
			NodeCount: model.nodes, EdgeCount: model.edges, LogicalEdges: model.logicalEdges,
			LogicalDepth: model.logicalDepth, Expansion: model.expansion,
		}
	}
	if result.Tables[0].Tag != LayoutTableGDEF || result.Tables[1].Tag != LayoutTableGSUB || result.Tables[2].Tag != LayoutTableGPOS {
		return LayoutGraphReceipt{}, fontError(CodeLayoutGraphInvariant, "layout_graph/receipt/table_order", ErrInvariantFailed)
	}
	return result, nil
}

func layoutGraphCheckedAdd(left, right uint64, path string) (uint64, error) {
	result, ok := checkedAdd(left, right)
	if !ok {
		return 0, fontError(CodeLayoutGraphResource, path, ErrResourceLimit)
	}
	return result, nil
}

func layoutGraphReceiptStringBytes(value LayoutGraphReceipt) uint64 {
	strings := []string{
		value.Profile, value.SourceProfile, value.SourceDescriptorHashProfile, value.SourceDescriptorHash,
		value.SourceOutlineSetHashProfile, value.SourceOutlineSetHash, value.UpstreamContentHashProfile,
		value.UpstreamContentHash, value.DescriptorHashProfile, value.DescriptorHash,
		value.GraphHashProfile, value.GraphHash, value.GDEFFoundationHashProfile, value.GDEFFoundationHash,
		value.GSUBFoundationHashProfile, value.GSUBFoundationHash, value.GPOSFoundationHashProfile,
		value.GPOSFoundationHash, value.ValidationProfile, value.CapabilityStatus,
		value.LookupExecutionStatus, value.VariableLayoutStatus,
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
		for _, text := range []string{string(table.Tag), table.HashProfile, table.ContentHash} {
			next, ok := checkedAdd(total, uint64(len(text)))
			if !ok {
				return ^uint64(0)
			}
			total = next
		}
	}
	return total
}

func mapLayoutGraphValidationError(err error) error {
	if err == nil || errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) ||
		errors.Is(err, ErrAllocationFailed) {
		return err
	}
	if errors.Is(err, ErrResourceLimit) {
		return fontError(CodeLayoutGraphResource, "layout_graph/validation", errors.Join(ErrResourceLimit, err))
	}
	if errors.Is(err, ErrLimitsInvalid) {
		return fontError(CodeLayoutGraphLimits, "layout_graph/validation", errors.Join(ErrLimitsInvalid, err))
	}
	if errors.Is(err, ErrInvariantFailed) {
		return fontError(CodeLayoutGraphInvariant, "layout_graph/validation", errors.Join(ErrInvariantFailed, err))
	}
	return fontError(CodeLayoutGraphInvalid, "layout_graph/validation", errors.Join(ErrLayoutGraphInvalid, err))
}

func mapLayoutGraphHashError(err error) error {
	if err == nil || errors.Is(err, ErrCanceled) || errors.Is(err, ErrContextInvalid) || errors.Is(err, ErrAllocationFailed) {
		return err
	}
	if errors.Is(err, ErrResourceLimit) {
		return fontError(CodeLayoutGraphResource, "layout_graph/hash", errors.Join(ErrResourceLimit, err))
	}
	return fontError(CodeLayoutGraphInvariant, "layout_graph/hash", errors.Join(ErrInvariantFailed, err))
}
