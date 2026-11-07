package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func hashGPOSFoundation(ctx context.Context, parent *gsubValidationFoundation, source layoutSourceBinding,
	tables [3]layoutTableBinding, gpos *gposFoundation, gposBytes []byte, stats layoutDecodeStats,
	limit uint64) (result string, canonicalBytes uint64, err error) {
	defer func() {
		if recover() != nil {
			result, canonicalBytes = "", 0
			err = fontError(CodeAllocationFailed, "layout/gpos_foundation/hash/allocation", ErrAllocationFailed)
		}
	}()
	if err := validateGPOSFoundationHashInput(ctx, parent, source, tables, gpos, gposBytes, stats); err != nil {
		return "", 0, err
	}
	if limit == 0 {
		return "", 0, fontError(codeLayoutResource, "layout/gpos_foundation/hash/canonical_bytes", ErrResourceLimit)
	}
	sink := layoutFoundationHashSink{ctx: ctx, hash: sha256.New(), limit: limit}
	if err := writeGPOSFoundationHashHeader(&sink, parent, source, tables, gpos); err != nil {
		return "", 0, err
	}
	nodes := []layoutNodeEvidence(nil)
	edges := []layoutEdgeEvidence(nil)
	logical := gsubLogicalLookupGraph{}
	if gpos != nil {
		nodes, edges, logical = gpos.nodes, gpos.edges, gpos.logical
	}
	if err := sink.u64(uint64(len(nodes)), "layout/gpos_foundation/hash/nodes/count"); err != nil {
		return "", 0, err
	}
	for index, node := range nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/nodes"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gpos_foundation/hash/nodes/%d", index)
		for _, write := range []func() error{
			func() error { return sink.u32(node.tableTag, path+"/table_tag") },
			func() error { return sink.u64(node.start, path+"/start") },
			func() error { return sink.u64(node.end, path+"/end") },
			func() error { return sink.u16(uint16(node.kind), path+"/kind") },
			func() error { return sink.u64(node.paramFirst, path+"/param_first") },
			func() error { return sink.u64(node.paramSecond, path+"/param_second") },
			func() error { return sink.u64(node.end-node.start, path+"/payload_length") },
			func() error { return sink.raw(gposBytes[int(node.start):int(node.end)], path+"/payload") },
		} {
			if err := write(); err != nil {
				return "", 0, err
			}
		}
	}
	if err := sink.u64(uint64(len(edges)), "layout/gpos_foundation/hash/edges/count"); err != nil {
		return "", 0, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/edges"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gpos_foundation/hash/edges/%d", index)
		for _, write := range []func() error{
			func() error { return sink.u32(edge.tableTag, path+"/table_tag") },
			func() error { return sink.u64(edge.parentStart, path+"/parent_start") },
			func() error { return sink.u64(edge.childStart, path+"/child_start") },
			func() error { return sink.u16(uint16(edge.role), path+"/role") },
			func() error { return sink.u32(edge.ordinal, path+"/ordinal") },
			func() error { return sink.u32(edge.rawOffset, path+"/raw_offset") },
		} {
			if err := write(); err != nil {
				return "", 0, err
			}
		}
	}
	if err := sink.u64(uint64(len(logical.edges)), "layout/gpos_foundation/hash/logical/edge_count"); err != nil {
		return "", 0, err
	}
	for index, edge := range logical.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/logical/edges"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gpos_foundation/hash/logical/edges/%d", index)
		if err := sink.u16(edge.fromLookup, path+"/from"); err != nil {
			return "", 0, err
		}
		if err := sink.u16(edge.toLookup, path+"/to"); err != nil {
			return "", 0, err
		}
		if err := sink.u16(edge.subtable, path+"/subtable"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(edge.recordOrdinal, path+"/record_ordinal"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u64(uint64(len(logical.topological)), "layout/gpos_foundation/hash/logical/topological_count"); err != nil {
		return "", 0, err
	}
	for index, lookup := range logical.topological {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/logical/topological"); err != nil {
				return "", 0, err
			}
		}
		if err := sink.u16(lookup, fmt.Sprintf("layout/gpos_foundation/hash/logical/topological/%d", index)); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u32(logical.maxDepth, "layout/gpos_foundation/hash/logical/max_depth"); err != nil {
		return "", 0, err
	}
	if err := sink.u64(logical.maxExpansion, "layout/gpos_foundation/hash/logical/max_expansion"); err != nil {
		return "", 0, err
	}
	if err := writeGPOSFoundationStats(&sink, stats); err != nil {
		return "", 0, err
	}
	if err := contextFailure(ctx, "layout/gpos_foundation/hash/finalize"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.count, nil
}

func writeGPOSFoundationHashHeader(sink *layoutFoundationHashSink, parent *gsubValidationFoundation,
	source layoutSourceBinding, tables [3]layoutTableBinding, gpos *gposFoundation) error {
	writes := []func() error{
		func() error {
			return sink.str(gposFoundationCanonicalHashV1, "layout/gpos_foundation/hash/hash_profile")
		},
		func() error { return sink.u32(gposFoundationSchemaVersion, "layout/gpos_foundation/hash/schema") },
		func() error { return sink.str(gposFoundationProfileV1, "layout/gpos_foundation/hash/profile") },
		func() error {
			return sink.str(gposFoundationDescriptorIdentityV1, "layout/gpos_foundation/hash/descriptor")
		},
		func() error { return sink.str(parent.receipt.Profile, "layout/gpos_foundation/hash/parent/profile") },
		func() error {
			return sink.str(parent.receipt.DescriptorHash, "layout/gpos_foundation/hash/parent/descriptor")
		},
		func() error {
			return sink.str(parent.receipt.FoundationHashProfile, "layout/gpos_foundation/hash/parent/hash_profile")
		},
		func() error {
			return sink.str(parent.receipt.FoundationHash, "layout/gpos_foundation/hash/parent/hash")
		},
		func() error { return sink.str(source.outlineSetProfile, "layout/gpos_foundation/hash/source/profile") },
		func() error {
			return sink.str(source.outlineSetDescriptorHash, "layout/gpos_foundation/hash/source/descriptor")
		},
		func() error {
			return sink.str(source.outlineSetHashProfile, "layout/gpos_foundation/hash/source/set_hash_profile")
		},
		func() error { return sink.str(source.outlineSetHash, "layout/gpos_foundation/hash/source/set_hash") },
		func() error {
			return sink.str(source.fontContentHashProfile, "layout/gpos_foundation/hash/source/content_hash_profile")
		},
		func() error {
			return sink.str(source.fontContentHash, "layout/gpos_foundation/hash/source/content_hash")
		},
		func() error { return sink.u32(source.glyphCount, "layout/gpos_foundation/hash/source/glyph_count") },
		func() error {
			return sink.u64(source.realPointInstances, "layout/gpos_foundation/hash/source/real_points")
		},
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	for index, table := range tables {
		path := fmt.Sprintf("layout/gpos_foundation/hash/tables/%d", index)
		present := uint8(0)
		if table.present {
			present = 1
		}
		for _, write := range []func() error{
			func() error { return sink.tag(table.tag, path+"/tag") },
			func() error { return sink.u8(present, path+"/present") },
			func() error { return sink.u32(table.fileOffset, path+"/file_offset") },
			func() error { return sink.u32(table.length, path+"/length") },
			func() error { return sink.u32(table.checksum, path+"/checksum") },
			func() error { return sink.str(table.hashProfile, path+"/hash_profile") },
			func() error { return sink.str(table.contentHash, path+"/content_hash") },
		} {
			if err := write(); err != nil {
				return err
			}
		}
	}
	present := uint8(0)
	var major, minor uint16
	if gpos != nil {
		present, major, minor = 1, gpos.majorVersion, gpos.minorVersion
	}
	if err := sink.u8(present, "layout/gpos_foundation/hash/gpos/present"); err != nil {
		return err
	}
	if err := sink.u16(major, "layout/gpos_foundation/hash/gpos/major"); err != nil {
		return err
	}
	return sink.u16(minor, "layout/gpos_foundation/hash/gpos/minor")
}

func writeGPOSFoundationStats(sink *layoutFoundationHashSink, stats layoutDecodeStats) error {
	if err := sink.u32(stats.maxStructuralDepth, "layout/gpos_foundation/hash/stats/max_structural_depth"); err != nil {
		return err
	}
	if err := sink.u32(stats.logicalLookupDepth, "layout/gpos_foundation/hash/stats/logical_depth"); err != nil {
		return err
	}
	values := []uint64{
		stats.tableBytes, stats.gposBytes, stats.nodes, stats.edges, stats.aliasReferences, stats.directBytes,
		stats.coverageTables, stats.coverageFormat1Glyphs, stats.coverageRanges, stats.coverageGlyphs,
		stats.classDefTables, stats.classFormat1Values, stats.classRanges, stats.classAssignments,
		stats.deviceTables, stats.deviceDeltas, stats.deviceWords, stats.orderedRecords,
		stats.scriptRecords, stats.scriptTables, stats.langSysRecords, stats.langSysTables,
		stats.langSysFeatureReferences, stats.featureRecords, stats.featureTables, stats.featureLookupReferences,
		stats.featureParamTables, stats.lookupReferences, stats.lookupTables, stats.subtableReferences,
		stats.gposSubtables, stats.valueRecords, stats.pairSetTables, stats.pairValueRecords,
		stats.classPairRecords, stats.anchorTables, stats.anchorReferences, stats.markArrayTables,
		stats.markRecords, stats.baseArrayTables, stats.baseRecords, stats.ligatureArrayTables,
		stats.ligatureAttachTables, stats.ligatureComponentRecords, stats.mark2ArrayTables, stats.mark2Records,
		stats.contextRuleSetReferences, stats.contextRuleSets, stats.contextRuleReferences, stats.contextRules,
		stats.contextGlyphReferences, stats.contextClassReferences, stats.sequenceLookupRecords,
		stats.extensionSubtables, stats.logicalLookupEdges, stats.logicalExpansion,
		stats.workUnits, stats.workingBytes, stats.retainedBytes,
	}
	for index, value := range values {
		if err := sink.u64(value, fmt.Sprintf("layout/gpos_foundation/hash/stats/%d", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateGPOSFoundationHashInput(ctx context.Context, parent *gsubValidationFoundation,
	source layoutSourceBinding, tables [3]layoutTableBinding, gpos *gposFoundation,
	gposBytes []byte, stats layoutDecodeStats) error {
	if err := contextFailure(ctx, "layout/gpos_foundation/hash/context"); err != nil {
		return err
	}
	if parent == nil || parent.parent == nil || parent.parent.sourceBinding != source || parent.parent.tables != tables ||
		parent.receipt.Profile != gsubFoundationReceiptProfileV1 ||
		!validLayoutFoundationDigest(parent.receipt.FoundationHash) ||
		!validLayoutFoundationDigest(gposFoundationDescriptorIdentityV1) {
		return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/parent", ErrInvariantFailed)
	}
	if gpos == nil {
		if tables[2].present || len(gposBytes) != 0 || stats != (layoutDecodeStats{}) {
			return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/absent", ErrInvariantFailed)
		}
		return nil
	}
	if !tables[2].present || tables[2].length == 0 || uint64(len(gposBytes)) != uint64(tables[2].length) ||
		gpos.majorVersion != 1 || (gpos.minorVersion != 0 && gpos.minorVersion != 1) ||
		stats.tableBytes != uint64(len(gposBytes)) || stats.gposBytes != uint64(len(gposBytes)) ||
		stats.gdefBytes != 0 || stats.gsubBytes != 0 || uint64(len(gpos.nodes)) != stats.nodes ||
		uint64(len(gpos.edges)) != stats.edges || uint64(len(gpos.logical.edges)) != stats.logicalLookupEdges ||
		gpos.logical.maxDepth != stats.logicalLookupDepth || gpos.logical.maxExpansion != stats.logicalExpansion {
		return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/binding", ErrInvariantFailed)
	}
	var directBytes uint64
	for index, node := range gpos.nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/validate/nodes"); err != nil {
				return err
			}
		}
		if node.tableTag != layoutGPOSTag || node.end <= node.start || node.end > uint64(len(gposBytes)) ||
			(index != 0 && node.start < gpos.nodes[index-1].end) {
			return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/nodes", ErrInvariantFailed)
		}
		var ok bool
		directBytes, ok = checkedAdd(directBytes, node.end-node.start)
		if !ok {
			return fontError(codeLayoutResource, "layout/gpos_foundation/hash/direct_bytes", ErrResourceLimit)
		}
	}
	if directBytes != stats.directBytes {
		return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/direct_bytes", ErrInvariantFailed)
	}
	for index, edge := range gpos.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos_foundation/hash/validate/edges"); err != nil {
				return err
			}
		}
		if edge.tableTag != layoutGPOSTag || edge.rawOffset == 0 ||
			!gsubFoundationNodeAt(gpos.nodes, edge.parentStart) || !gsubFoundationNodeAt(gpos.nodes, edge.childStart) ||
			(index != 0 && compareGSUBPhysicalEdge(gpos.edges[index-1], edge) >= 0) {
			return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/edges", ErrInvariantFailed)
		}
	}
	aliases, err := gsubFoundationAliasCount(ctx, gpos.edges)
	if err != nil {
		return err
	}
	if aliases != stats.aliasReferences {
		return fontError(codeLayoutInvariant, "layout/gpos_foundation/hash/aliases", ErrInvariantFailed)
	}
	return validateLayoutLogicalHashInput(ctx, gpos.logical, len(gpos.lookups),
		func(index int) (int, bool) {
			if index < 0 || index >= len(gpos.lookups) || gpos.lookups[index] == nil {
				return 0, false
			}
			return len(gpos.lookups[index].subtables), true
		}, "layout/gpos_foundation/hash/logical")
}
