package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

func hashGSUBFoundation(ctx context.Context, parent *layoutFoundation, source layoutSourceBinding,
	tables [3]layoutTableBinding, gsub *gsubFoundation, gsubBytes []byte, stats layoutDecodeStats,
	limit uint64) (result string, canonicalBytes uint64, err error) {
	defer func() {
		if recover() != nil {
			result, canonicalBytes = "", 0
			err = fontError(CodeAllocationFailed, "layout/gsub_foundation/hash/allocation", ErrAllocationFailed)
		}
	}()
	if err := validateGSUBFoundationHashInput(ctx, parent, source, tables, gsub, gsubBytes, stats); err != nil {
		return "", 0, err
	}
	if limit == 0 {
		return "", 0, fontError(codeLayoutResource, "layout/gsub_foundation/hash/canonical_bytes", ErrResourceLimit)
	}
	sink := layoutFoundationHashSink{ctx: ctx, hash: sha256.New(), limit: limit}
	if err := writeGSUBFoundationHashHeader(&sink, parent, source, tables, gsub); err != nil {
		return "", 0, err
	}
	nodes := []layoutNodeEvidence(nil)
	edges := []layoutEdgeEvidence(nil)
	if gsub != nil {
		nodes, edges = gsub.nodes, gsub.edges
	}
	if err := sink.u64(uint64(len(nodes)), "layout/gsub_foundation/hash/nodes/count"); err != nil {
		return "", 0, err
	}
	for index, node := range nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/nodes"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gsub_foundation/hash/nodes/%d", index)
		if err := sink.u32(node.tableTag, path+"/table_tag"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(node.start, path+"/start"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(node.end, path+"/end"); err != nil {
			return "", 0, err
		}
		if err := sink.u16(uint16(node.kind), path+"/kind"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(node.paramFirst, path+"/param_first"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(node.paramSecond, path+"/param_second"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(node.end-node.start, path+"/payload_length"); err != nil {
			return "", 0, err
		}
		if err := sink.raw(gsubBytes[int(node.start):int(node.end)], path+"/payload"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u64(uint64(len(edges)), "layout/gsub_foundation/hash/edges/count"); err != nil {
		return "", 0, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/edges"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gsub_foundation/hash/edges/%d", index)
		if err := sink.u32(edge.tableTag, path+"/table_tag"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(edge.parentStart, path+"/parent_start"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(edge.childStart, path+"/child_start"); err != nil {
			return "", 0, err
		}
		if err := sink.u16(uint16(edge.role), path+"/role"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(edge.ordinal, path+"/ordinal"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(edge.rawOffset, path+"/raw_offset"); err != nil {
			return "", 0, err
		}
	}
	logical := gsubLogicalLookupGraph{}
	if gsub != nil {
		logical = gsub.logical
	}
	if err := sink.u64(uint64(len(logical.edges)), "layout/gsub_foundation/hash/logical/edge_count"); err != nil {
		return "", 0, err
	}
	for index, edge := range logical.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/logical/edges"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout/gsub_foundation/hash/logical/edges/%d", index)
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
	if err := sink.u64(uint64(len(logical.topological)), "layout/gsub_foundation/hash/logical/topological_count"); err != nil {
		return "", 0, err
	}
	for index, lookup := range logical.topological {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/logical/topological"); err != nil {
				return "", 0, err
			}
		}
		if err := sink.u16(lookup, fmt.Sprintf("layout/gsub_foundation/hash/logical/topological/%d", index)); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u32(logical.maxDepth, "layout/gsub_foundation/hash/logical/max_depth"); err != nil {
		return "", 0, err
	}
	if err := sink.u64(logical.maxExpansion, "layout/gsub_foundation/hash/logical/max_expansion"); err != nil {
		return "", 0, err
	}
	if err := writeGSUBFoundationStats(&sink, stats); err != nil {
		return "", 0, err
	}
	if err := contextFailure(ctx, "layout/gsub_foundation/hash/finalize"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.count, nil
}

func writeGSUBFoundationHashHeader(sink *layoutFoundationHashSink, parent *layoutFoundation,
	source layoutSourceBinding, tables [3]layoutTableBinding, gsub *gsubFoundation) error {
	writes := []func() error{
		func() error {
			return sink.str(gsubFoundationCanonicalHashV1, "layout/gsub_foundation/hash/hash_profile")
		},
		func() error { return sink.u32(gsubFoundationSchemaVersion, "layout/gsub_foundation/hash/schema") },
		func() error { return sink.str(gsubFoundationProfileV1, "layout/gsub_foundation/hash/profile") },
		func() error {
			return sink.str(gsubFoundationDescriptorIdentityV1, "layout/gsub_foundation/hash/descriptor")
		},
		func() error { return sink.str(parent.receipt.Profile, "layout/gsub_foundation/hash/parent/profile") },
		func() error {
			return sink.str(parent.receipt.DescriptorHash, "layout/gsub_foundation/hash/parent/descriptor")
		},
		func() error {
			return sink.str(parent.receipt.FoundationHashProfile, "layout/gsub_foundation/hash/parent/hash_profile")
		},
		func() error {
			return sink.str(parent.receipt.FoundationHash, "layout/gsub_foundation/hash/parent/hash")
		},
		func() error { return sink.str(source.outlineSetProfile, "layout/gsub_foundation/hash/source/profile") },
		func() error {
			return sink.str(source.outlineSetDescriptorHash, "layout/gsub_foundation/hash/source/descriptor")
		},
		func() error {
			return sink.str(source.outlineSetHashProfile, "layout/gsub_foundation/hash/source/set_hash_profile")
		},
		func() error { return sink.str(source.outlineSetHash, "layout/gsub_foundation/hash/source/set_hash") },
		func() error {
			return sink.str(source.fontContentHashProfile, "layout/gsub_foundation/hash/source/content_hash_profile")
		},
		func() error {
			return sink.str(source.fontContentHash, "layout/gsub_foundation/hash/source/content_hash")
		},
		func() error { return sink.u32(source.glyphCount, "layout/gsub_foundation/hash/source/glyph_count") },
		func() error {
			return sink.u64(source.realPointInstances, "layout/gsub_foundation/hash/source/real_points")
		},
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	for index, table := range tables {
		path := fmt.Sprintf("layout/gsub_foundation/hash/tables/%d", index)
		if err := sink.tag(table.tag, path+"/tag"); err != nil {
			return err
		}
		present := uint8(0)
		if table.present {
			present = 1
		}
		if err := sink.u8(present, path+"/present"); err != nil {
			return err
		}
		if err := sink.u32(table.fileOffset, path+"/file_offset"); err != nil {
			return err
		}
		if err := sink.u32(table.length, path+"/length"); err != nil {
			return err
		}
		if err := sink.u32(table.checksum, path+"/checksum"); err != nil {
			return err
		}
		if err := sink.str(table.hashProfile, path+"/hash_profile"); err != nil {
			return err
		}
		if err := sink.str(table.contentHash, path+"/content_hash"); err != nil {
			return err
		}
	}
	present := uint8(0)
	var major, minor uint16
	if gsub != nil {
		present, major, minor = 1, gsub.majorVersion, gsub.minorVersion
	}
	if err := sink.u8(present, "layout/gsub_foundation/hash/gsub/present"); err != nil {
		return err
	}
	if err := sink.u16(major, "layout/gsub_foundation/hash/gsub/major"); err != nil {
		return err
	}
	return sink.u16(minor, "layout/gsub_foundation/hash/gsub/minor")
}

func writeGSUBFoundationStats(sink *layoutFoundationHashSink, stats layoutDecodeStats) error {
	if err := sink.u32(stats.maxStructuralDepth, "layout/gsub_foundation/hash/stats/max_structural_depth"); err != nil {
		return err
	}
	if err := sink.u32(stats.logicalLookupDepth, "layout/gsub_foundation/hash/stats/logical_depth"); err != nil {
		return err
	}
	values := []uint64{
		stats.tableBytes, stats.gsubBytes, stats.nodes, stats.edges, stats.aliasReferences, stats.directBytes,
		stats.coverageTables, stats.coverageFormat1Glyphs, stats.coverageRanges, stats.coverageGlyphs,
		stats.classDefTables, stats.classFormat1Values, stats.classRanges, stats.classAssignments,
		stats.orderedRecords, stats.scriptRecords, stats.scriptTables, stats.langSysRecords, stats.langSysTables,
		stats.langSysFeatureReferences, stats.featureRecords, stats.featureTables, stats.featureLookupReferences,
		stats.featureParamTables, stats.featureCharacters, stats.lookupReferences, stats.lookupTables,
		stats.subtableReferences, stats.gsubSubtables, stats.sequenceReferences, stats.sequenceTables,
		stats.alternateSetReferences, stats.alternateSetTables, stats.ligatureSetReferences,
		stats.ligatureSetTables, stats.ligatureReferences, stats.ligatureTables,
		stats.substituteGlyphReferences, stats.ligatureComponents,
		stats.contextRuleSetReferences, stats.contextRuleSets, stats.contextRuleReferences, stats.contextRules,
		stats.contextGlyphReferences, stats.contextClassReferences, stats.sequenceLookupRecords,
		stats.extensionSubtables, stats.reverseSubtables, stats.logicalLookupEdges, stats.logicalExpansion,
		stats.workUnits, stats.workingBytes, stats.retainedBytes,
	}
	for index, value := range values {
		if err := sink.u64(value, fmt.Sprintf("layout/gsub_foundation/hash/stats/%d", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateGSUBFoundationHashInput(ctx context.Context, parent *layoutFoundation, source layoutSourceBinding,
	tables [3]layoutTableBinding, gsub *gsubFoundation, gsubBytes []byte, stats layoutDecodeStats) error {
	if err := contextFailure(ctx, "layout/gsub_foundation/hash/context"); err != nil {
		return err
	}
	if parent == nil || parent.sourceBinding != source || parent.tables != tables ||
		parent.receipt.Profile != layoutFoundationReceiptProfileV1 ||
		!validLayoutFoundationDigest(parent.receipt.FoundationHash) ||
		!validLayoutFoundationDigest(gsubFoundationDescriptorIdentityV1) {
		return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/parent", ErrInvariantFailed)
	}
	if gsub == nil {
		if tables[1].present || len(gsubBytes) != 0 || stats != (layoutDecodeStats{}) {
			return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/absent", ErrInvariantFailed)
		}
		return nil
	}
	if !tables[1].present || tables[1].length == 0 || uint64(len(gsubBytes)) != uint64(tables[1].length) ||
		gsub.majorVersion != 1 || (gsub.minorVersion != 0 && gsub.minorVersion != 1) ||
		stats.tableBytes != uint64(len(gsubBytes)) || stats.gsubBytes != uint64(len(gsubBytes)) || stats.gdefBytes != 0 ||
		uint64(len(gsub.nodes)) != stats.nodes || uint64(len(gsub.edges)) != stats.edges ||
		uint64(len(gsub.logical.edges)) != stats.logicalLookupEdges || gsub.logical.maxDepth != stats.logicalLookupDepth ||
		gsub.logical.maxExpansion != stats.logicalExpansion {
		return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/binding", ErrInvariantFailed)
	}
	var directBytes uint64
	for index, node := range gsub.nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/validate/nodes"); err != nil {
				return err
			}
		}
		if node.tableTag != layoutGSUBTag || node.end <= node.start || node.end > uint64(len(gsubBytes)) ||
			(index != 0 && node.start < gsub.nodes[index-1].end) {
			return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/nodes", ErrInvariantFailed)
		}
		var ok bool
		directBytes, ok = checkedAdd(directBytes, node.end-node.start)
		if !ok {
			return fontError(codeLayoutResource, "layout/gsub_foundation/hash/direct_bytes", ErrResourceLimit)
		}
	}
	if directBytes != stats.directBytes {
		return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/direct_bytes", ErrInvariantFailed)
	}
	for index, edge := range gsub.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/validate/edges"); err != nil {
				return err
			}
		}
		if edge.tableTag != layoutGSUBTag || edge.rawOffset == 0 ||
			!gsubFoundationNodeAt(gsub.nodes, edge.parentStart) || !gsubFoundationNodeAt(gsub.nodes, edge.childStart) ||
			(index != 0 && compareGSUBPhysicalEdge(gsub.edges[index-1], edge) >= 0) {
			return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/edges", ErrInvariantFailed)
		}
	}
	aliases, err := gsubFoundationAliasCount(ctx, gsub.edges)
	if err != nil {
		return err
	}
	if aliases != stats.aliasReferences {
		return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/aliases", ErrInvariantFailed)
	}
	return validateGSUBLogicalHashInput(ctx, gsub)
}

func gsubFoundationNodeAt(nodes []layoutNodeEvidence, offset uint64) bool {
	index := sort.Search(len(nodes), func(index int) bool { return nodes[index].start >= offset })
	return index < len(nodes) && nodes[index].start == offset
}

func compareGSUBPhysicalEdge(left, right layoutEdgeEvidence) int {
	leftValues := [...]uint64{left.parentStart, uint64(left.role), uint64(left.ordinal), left.childStart, uint64(left.rawOffset)}
	rightValues := [...]uint64{right.parentStart, uint64(right.role), uint64(right.ordinal), right.childStart, uint64(right.rawOffset)}
	for index := range leftValues {
		if leftValues[index] < rightValues[index] {
			return -1
		}
		if leftValues[index] > rightValues[index] {
			return 1
		}
	}
	return 0
}

func gsubFoundationAliasCount(ctx context.Context, edges []layoutEdgeEvidence) (uint64, error) {
	if len(edges) < 2 {
		return 0, contextFailure(ctx, "layout/gsub_foundation/hash/aliases")
	}
	if err := contextFailure(ctx, "layout/gsub_foundation/hash/aliases/allocate"); err != nil {
		return 0, err
	}
	targets, err := layoutMakeSlice[uint64](uint64(len(edges)), "layout/gsub_foundation/hash/aliases")
	if err != nil {
		return 0, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/aliases/project"); err != nil {
				return 0, err
			}
		}
		targets[index] = edge.childStart
	}
	if err := layoutCancelableSort(ctx, "layout/gsub_foundation/hash/aliases/sort", targets,
		func(left, right int) bool { return targets[left] < targets[right] }); err != nil {
		return 0, err
	}
	var aliases uint64
	for index := 1; index < len(targets); index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub_foundation/hash/aliases/count"); err != nil {
				return 0, err
			}
		}
		if targets[index] == targets[index-1] {
			aliases++
		}
	}
	return aliases, nil
}

func validateGSUBLogicalHashInput(ctx context.Context, gsub *gsubFoundation) error {
	if gsub == nil {
		return fontError(codeLayoutInvariant, "layout/gsub_foundation/hash/logical/model", ErrInvariantFailed)
	}
	return validateLayoutLogicalHashInput(ctx, gsub.logical, len(gsub.lookups),
		func(index int) (int, bool) {
			if index < 0 || index >= len(gsub.lookups) || gsub.lookups[index] == nil {
				return 0, false
			}
			return len(gsub.lookups[index].subtables), true
		}, "layout/gsub_foundation/hash/logical")
}

func validateLayoutLogicalHashInput(ctx context.Context, logical gsubLogicalLookupGraph, lookupCount int,
	subtableCount func(int) (int, bool), path string) error {
	if lookupCount < 0 || subtableCount == nil {
		return fontError(codeLayoutInvariant, path+"/model", ErrInvariantFailed)
	}
	for index := 0; index < lookupCount; index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/lookups"); err != nil {
				return err
			}
		}
		if _, found := subtableCount(index); !found {
			return fontError(codeLayoutInvariant, path+"/lookup", ErrInvariantFailed)
		}
	}
	if len(logical.edges) == 0 {
		if len(logical.topological) != 0 || logical.maxDepth != 0 || logical.maxExpansion != 0 {
			return fontError(codeLayoutInvariant, path+"/empty", ErrInvariantFailed)
		}
		return nil
	}
	if len(logical.topological) != lookupCount {
		return fontError(codeLayoutInvariant, path+"/topological", ErrInvariantFailed)
	}
	if err := contextFailure(ctx, path+"/allocate"); err != nil {
		return err
	}
	indegree, err := layoutMakeSlice[uint64](uint64(lookupCount), path+"/indegree")
	if err != nil {
		return err
	}
	outStart, err := layoutMakeSlice[uint64](uint64(lookupCount)+1, path+"/out_start")
	if err != nil {
		return err
	}
	for index, edge := range logical.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/edges"); err != nil {
				return err
			}
		}
		from := int(edge.fromLookup)
		count, found := subtableCount(from)
		if from >= lookupCount || int(edge.toLookup) >= lookupCount || !found || int(edge.subtable) >= count ||
			(index != 0 && compareGSUBLogicalEdge(logical.edges[index-1], edge) >= 0) {
			return fontError(codeLayoutInvariant, path+"/edge", ErrInvariantFailed)
		}
		indegree[edge.toLookup]++
		outStart[int(edge.fromLookup)+1]++
	}
	for index := 1; index < len(outStart); index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/out_start"); err != nil {
				return err
			}
		}
		next, ok := checkedAdd(outStart[index], outStart[index-1])
		if !ok {
			return fontError(codeLayoutResource, path+"/out_start", ErrResourceLimit)
		}
		outStart[index] = next
	}
	heap := make(gsubLookupMinHeap, 0, lookupCount)
	for index, count := range indegree {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/roots"); err != nil {
				return err
			}
		}
		if count == 0 {
			heap.push(uint16(index))
		}
	}
	var traversedEdges uint64
	for orderIndex, want := range logical.topological {
		if orderIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/topological"); err != nil {
				return err
			}
		}
		if len(heap) == 0 || heap.pop() != want {
			return fontError(codeLayoutInvariant, path+"/order", ErrInvariantFailed)
		}
		for edgeIndex := outStart[want]; edgeIndex < outStart[int(want)+1]; edgeIndex++ {
			if traversedEdges%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, path+"/topological/edges"); err != nil {
					return err
				}
			}
			traversedEdges++
			target := logical.edges[edgeIndex].toLookup
			if indegree[target] == 0 {
				return fontError(codeLayoutInvariant, path+"/indegree", ErrInvariantFailed)
			}
			indegree[target]--
			if indegree[target] == 0 {
				heap.push(target)
			}
		}
	}
	if len(heap) != 0 {
		return fontError(codeLayoutInvariant, path+"/cardinality", ErrInvariantFailed)
	}
	depth, err := layoutMakeSlice[uint32](uint64(lookupCount), path+"/depth")
	if err != nil {
		return err
	}
	expansion, err := layoutMakeSlice[uint64](uint64(lookupCount), path+"/expansion")
	if err != nil {
		return err
	}
	var maximumDepth uint32
	var maximumExpansion uint64
	traversedEdges = 0
	for orderIndex := lookupCount; orderIndex > 0; orderIndex-- {
		if orderIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/metrics"); err != nil {
				return err
			}
		}
		lookup := logical.topological[orderIndex-1]
		expansion[lookup] = 1
		for edgeIndex := outStart[lookup]; edgeIndex < outStart[int(lookup)+1]; edgeIndex++ {
			if traversedEdges%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, path+"/metrics/edges"); err != nil {
					return err
				}
			}
			traversedEdges++
			target := logical.edges[edgeIndex].toLookup
			candidate := depth[target] + 1
			if candidate < depth[target] {
				return fontError(codeLayoutResource, path+"/depth", ErrResourceLimit)
			}
			if candidate > depth[lookup] {
				depth[lookup] = candidate
			}
			next, ok := checkedAdd(expansion[lookup], expansion[target])
			if !ok {
				return fontError(codeLayoutResource, path+"/expansion", ErrResourceLimit)
			}
			expansion[lookup] = next
		}
		if depth[lookup] > maximumDepth {
			maximumDepth = depth[lookup]
		}
		if expansion[lookup] > maximumExpansion {
			maximumExpansion = expansion[lookup]
		}
	}
	if maximumExpansion == 0 || maximumDepth != logical.maxDepth || maximumExpansion-1 != logical.maxExpansion {
		return fontError(codeLayoutInvariant, path+"/metrics", ErrInvariantFailed)
	}
	return nil
}

func compareGSUBLogicalEdge(left, right gsubLogicalLookupEdge) int {
	leftValues := [...]uint64{uint64(left.fromLookup), uint64(left.subtable), left.recordOrdinal, uint64(left.toLookup)}
	rightValues := [...]uint64{uint64(right.fromLookup), uint64(right.subtable), right.recordOrdinal, uint64(right.toLookup)}
	for index := range leftValues {
		if leftValues[index] < rightValues[index] {
			return -1
		}
		if leftValues[index] > rightValues[index] {
			return 1
		}
	}
	return 0
}
