package sfntglyf

import "context"

func gsubVisitSequenceLookupRecords(ctx context.Context, path string, subtable gsubSubtable, visit func(gsubSequenceLookupRecord) error) error {
	var steps uint64
	return gsubVisitSequenceLookupRecordsInner(ctx, path, subtable, visit, &steps)
}

func gsubVisitSequenceLookupRecordsInner(ctx context.Context, path string, subtable gsubSubtable,
	visit func(gsubSequenceLookupRecord) error, steps *uint64) error {
	checkpoint := func(segment string) error {
		if *steps%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+segment); err != nil {
				return err
			}
		}
		(*steps)++
		return nil
	}
	switch typed := subtable.(type) {
	case *gsubContext:
		switch typed.format {
		case 1:
			for _, set := range typed.ruleSets {
				if err := checkpoint("/rule_sets"); err != nil {
					return err
				}
				if set == nil {
					continue
				}
				for _, rule := range set.rules {
					if err := checkpoint("/rules"); err != nil {
						return err
					}
					for _, record := range rule.records {
						if err := checkpoint("/records"); err != nil {
							return err
						}
						if err := visit(record); err != nil {
							return err
						}
					}
				}
			}
		case 2:
			for _, set := range typed.classRuleSets {
				if err := checkpoint("/class_rule_sets"); err != nil {
					return err
				}
				if set == nil {
					continue
				}
				for _, rule := range set.rules {
					if err := checkpoint("/class_rules"); err != nil {
						return err
					}
					for _, record := range rule.records {
						if err := checkpoint("/records"); err != nil {
							return err
						}
						if err := visit(record); err != nil {
							return err
						}
					}
				}
			}
		case 3:
			for _, record := range typed.records {
				if err := checkpoint("/records"); err != nil {
					return err
				}
				if err := visit(record); err != nil {
					return err
				}
			}
		default:
			return fontError(codeLayoutInvariant, path+"/context_format", ErrInvariantFailed)
		}
	case *gsubChainedContext:
		switch typed.format {
		case 1:
			for _, set := range typed.ruleSets {
				if err := checkpoint("/chain_rule_sets"); err != nil {
					return err
				}
				if set == nil {
					continue
				}
				for _, rule := range set.rules {
					if err := checkpoint("/chain_rules"); err != nil {
						return err
					}
					for _, record := range rule.records {
						if err := checkpoint("/records"); err != nil {
							return err
						}
						if err := visit(record); err != nil {
							return err
						}
					}
				}
			}
		case 2:
			for _, set := range typed.classRuleSets {
				if err := checkpoint("/chain_class_rule_sets"); err != nil {
					return err
				}
				if set == nil {
					continue
				}
				for _, rule := range set.rules {
					if err := checkpoint("/chain_class_rules"); err != nil {
						return err
					}
					for _, record := range rule.records {
						if err := checkpoint("/records"); err != nil {
							return err
						}
						if err := visit(record); err != nil {
							return err
						}
					}
				}
			}
		case 3:
			for _, record := range typed.records {
				if err := checkpoint("/records"); err != nil {
					return err
				}
				if err := visit(record); err != nil {
					return err
				}
			}
		default:
			return fontError(codeLayoutInvariant, path+"/chained_format", ErrInvariantFailed)
		}
	case *gsubExtension:
		if typed.subtable == nil || typed.extensionLookupType == 7 {
			return fontError(codeLayoutInvariant, path+"/extension", ErrInvariantFailed)
		}
		return gsubVisitSequenceLookupRecordsInner(ctx, path+"/extension", typed.subtable, visit, steps)
	case *gsubSingle, *gsubMultiple, *gsubAlternate, *gsubLigatureSubst, *gsubReverseChain:
		return nil
	default:
		return fontError(codeLayoutInvariant, path+"/subtable", ErrInvariantFailed)
	}
	return nil
}

func gsubLogicalRecordCount(ctx context.Context, lookups []*gsubLookup, limit uint64) (uint64, error) {
	var count uint64
	for lookupIndex, lookup := range lookups {
		if lookupIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub/logical/count"); err != nil {
				return 0, err
			}
		}
		if lookup == nil {
			return 0, fontError(codeLayoutInvariant, "layout/gsub/logical/lookup", ErrInvariantFailed)
		}
		for subtableIndex, subtable := range lookup.subtables {
			if subtableIndex%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, "layout/gsub/logical/count/subtables"); err != nil {
					return 0, err
				}
			}
			err := gsubVisitSequenceLookupRecords(ctx, "layout/gsub/logical/count/walk", subtable, func(gsubSequenceLookupRecord) error {
				if count%layoutContextCheckStride == 0 {
					if err := contextFailure(ctx, "layout/gsub/logical/count/records"); err != nil {
						return err
					}
				}
				next, ok := checkedAdd(count, 1)
				if !ok || next > limit {
					return fontError(codeLayoutResource, "layout/gsub/logical/count", ErrResourceLimit)
				}
				count = next
				return nil
			})
			if err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

func buildGSUBLogicalLookupGraph(ctx context.Context, lookups []*gsubLookup, meter *layoutResourceMeter) (gsubLogicalLookupGraph, error) {
	var zero gsubLogicalLookupGraph
	if err := contextFailure(ctx, "layout/gsub/logical/context"); err != nil {
		return zero, err
	}
	edgeCount, err := gsubLogicalRecordCount(ctx, lookups, meter.limits.maxLogicalLookupEdges)
	if err != nil {
		return zero, err
	}
	// Preserve the frozen Phase 0-2 receipt: simple-only lookup lists have no
	// logical invocation evidence or additional resource charge.
	if edgeCount == 0 {
		return zero, nil
	}
	if err := meter.chargeLogicalEdges(edgeCount, "layout/gsub/logical"); err != nil {
		return zero, err
	}
	edgeBytes, err := layoutRetainedArrayBytes(edgeCount, gsubLogicalLookupEdgeRetainedBytes, "layout/gsub/logical/edges")
	if err != nil {
		return zero, err
	}
	topologyBytes, err := layoutRetainedArrayBytes(uint64(len(lookups)), 2, "layout/gsub/logical/topological")
	if err != nil {
		return zero, err
	}
	retained, ok := checkedAdd(edgeBytes, topologyBytes)
	if !ok {
		return zero, fontError(codeLayoutResource, "layout/gsub/logical/retained", ErrResourceLimit)
	}
	if err := meter.reserveRetained(retained+32, "layout/gsub/logical"); err != nil {
		return zero, err
	}
	if err := contextFailure(ctx, "layout/gsub/logical/allocate"); err != nil {
		return zero, err
	}
	edges, err := layoutMakeSlice[gsubLogicalLookupEdge](edgeCount, "layout/gsub/logical/edges")
	if err != nil {
		return zero, err
	}
	writeIndex := uint64(0)
	for lookupIndex, lookup := range lookups {
		if lookupIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gsub/logical/project"); err != nil {
				return zero, err
			}
		}
		for subtableIndex, subtable := range lookup.subtables {
			var recordOrdinal uint64
			err := gsubVisitSequenceLookupRecords(ctx, "layout/gsub/logical/project/walk", subtable, func(record gsubSequenceLookupRecord) error {
				if writeIndex%layoutContextCheckStride == 0 {
					if err := contextFailure(ctx, "layout/gsub/logical/project/records"); err != nil {
						return err
					}
				}
				if writeIndex >= uint64(len(edges)) || lookupIndex > int(^uint16(0)) || subtableIndex > int(^uint16(0)) || int(record.lookupIndex) >= len(lookups) {
					return fontError(codeLayoutInvariant, "layout/gsub/logical/project", ErrInvariantFailed)
				}
				edges[writeIndex] = gsubLogicalLookupEdge{
					fromLookup: uint16(lookupIndex), toLookup: record.lookupIndex,
					subtable: uint16(subtableIndex), recordOrdinal: recordOrdinal,
				}
				writeIndex++
				recordOrdinal++
				return nil
			})
			if err != nil {
				return zero, err
			}
		}
	}
	if writeIndex != edgeCount {
		return zero, fontError(codeLayoutInvariant, "layout/gsub/logical/cardinality", ErrInvariantFailed)
	}
	if err := layoutCancelableSort(ctx, "layout/gsub/logical/sort", edges, func(left, right int) bool {
		if edges[left].fromLookup != edges[right].fromLookup {
			return edges[left].fromLookup < edges[right].fromLookup
		}
		if edges[left].subtable != edges[right].subtable {
			return edges[left].subtable < edges[right].subtable
		}
		if edges[left].recordOrdinal != edges[right].recordOrdinal {
			return edges[left].recordOrdinal < edges[right].recordOrdinal
		}
		return edges[left].toLookup < edges[right].toLookup
	}); err != nil {
		return zero, err
	}
	return finishGSUBLogicalLookupGraph(ctx, lookups, edges, meter)
}

type gsubLookupMinHeap []uint16

func (value *gsubLookupMinHeap) push(item uint16) {
	*value = append(*value, item)
	index := len(*value) - 1
	for index > 0 {
		parent := (index - 1) / 2
		if (*value)[parent] <= (*value)[index] {
			break
		}
		(*value)[parent], (*value)[index] = (*value)[index], (*value)[parent]
		index = parent
	}
}

func (value *gsubLookupMinHeap) pop() uint16 {
	result := (*value)[0]
	last := (*value)[len(*value)-1]
	*value = (*value)[:len(*value)-1]
	if len(*value) == 0 {
		return result
	}
	(*value)[0] = last
	for index := 0; ; {
		left := 2*index + 1
		if left >= len(*value) {
			break
		}
		right := left + 1
		smallest := left
		if right < len(*value) && (*value)[right] < (*value)[left] {
			smallest = right
		}
		if (*value)[index] <= (*value)[smallest] {
			break
		}
		(*value)[index], (*value)[smallest] = (*value)[smallest], (*value)[index]
		index = smallest
	}
	return result
}

func finishGSUBLogicalLookupGraph(ctx context.Context, lookups []*gsubLookup, edges []gsubLogicalLookupEdge, meter *layoutResourceMeter) (gsubLogicalLookupGraph, error) {
	return finishLayoutLogicalLookupGraph(ctx, len(lookups), edges, meter, "layout/gsub/logical")
}

func finishLayoutLogicalLookupGraph(ctx context.Context, lookupCount int, edges []gsubLogicalLookupEdge,
	meter *layoutResourceMeter, path string) (gsubLogicalLookupGraph, error) {
	var zero gsubLogicalLookupGraph
	if lookupCount < 0 || lookupCount > int(^uint16(0))+1 || meter == nil {
		return zero, fontError(codeLayoutInvariant, path+"/input", ErrInvariantFailed)
	}
	indegree, err := layoutMakeSlice[uint64](uint64(lookupCount), path+"/indegree")
	if err != nil {
		return zero, err
	}
	outStart, err := layoutMakeSlice[uint64](uint64(lookupCount)+1, path+"/out_start")
	if err != nil {
		return zero, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/index"); err != nil {
				return zero, err
			}
		}
		if int(edge.fromLookup) >= lookupCount || int(edge.toLookup) >= lookupCount {
			return zero, fontError(codeLayoutInvariant, path+"/endpoint", ErrInvariantFailed)
		}
		indegree[edge.toLookup]++
		outStart[int(edge.fromLookup)+1]++
	}
	for index := 1; index < len(outStart); index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/out_start"); err != nil {
				return zero, err
			}
		}
		next, ok := checkedAdd(outStart[index], outStart[index-1])
		if !ok {
			return zero, fontError(codeLayoutResource, path+"/out_start", ErrResourceLimit)
		}
		outStart[index] = next
	}
	topological, err := layoutMakeSlice[uint16](uint64(lookupCount), path+"/topological")
	if err != nil {
		return zero, err
	}
	heap := make(gsubLookupMinHeap, 0, lookupCount)
	for index, count := range indegree {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/roots"); err != nil {
				return zero, err
			}
		}
		if count == 0 {
			heap.push(uint16(index))
		}
	}
	write := 0
	var traversedEdges uint64
	for len(heap) != 0 {
		if write%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/topological"); err != nil {
				return zero, err
			}
		}
		lookup := heap.pop()
		topological[write] = lookup
		write++
		for edgeIndex := outStart[lookup]; edgeIndex < outStart[int(lookup)+1]; edgeIndex++ {
			if traversedEdges%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, path+"/topological/edges"); err != nil {
					return zero, err
				}
			}
			traversedEdges++
			target := edges[edgeIndex].toLookup
			if indegree[target] == 0 {
				return zero, fontError(codeLayoutInvariant, path+"/indegree", ErrInvariantFailed)
			}
			indegree[target]--
			if indegree[target] == 0 {
				heap.push(target)
			}
		}
	}
	if write != lookupCount {
		return zero, fontError(codeLayoutInvalid, path+"/cycle", errLayoutInvalid)
	}
	depth, err := layoutMakeSlice[uint32](uint64(lookupCount), path+"/depth")
	if err != nil {
		return zero, err
	}
	expansion, err := layoutMakeSlice[uint64](uint64(lookupCount), path+"/expansion")
	if err != nil {
		return zero, err
	}
	var maximumDepth uint32
	var maximumExpansion uint64
	traversedEdges = 0
	for orderIndex := lookupCount; orderIndex > 0; orderIndex-- {
		if orderIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, path+"/expansion"); err != nil {
				return zero, err
			}
		}
		lookup := topological[orderIndex-1]
		expansion[lookup] = 1
		for edgeIndex := outStart[lookup]; edgeIndex < outStart[int(lookup)+1]; edgeIndex++ {
			if traversedEdges%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, path+"/expansion/edges"); err != nil {
					return zero, err
				}
			}
			traversedEdges++
			target := edges[edgeIndex].toLookup
			candidateDepth := depth[target] + 1
			if candidateDepth < depth[target] {
				return zero, fontError(codeLayoutResource, path+"/depth", ErrResourceLimit)
			}
			if candidateDepth > depth[lookup] {
				depth[lookup] = candidateDepth
			}
			next, ok := checkedAdd(expansion[lookup], expansion[target])
			if !ok {
				return zero, fontError(codeLayoutResource, path+"/expansion", ErrResourceLimit)
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
	if maximumExpansion == 0 {
		return zero, fontError(codeLayoutInvariant, path+"/expansion", ErrInvariantFailed)
	}
	additionalExpansion := maximumExpansion - 1
	if err := meter.setLogicalDepth(maximumDepth, path); err != nil {
		return zero, err
	}
	if err := meter.setLogicalExpansion(additionalExpansion, path); err != nil {
		return zero, err
	}
	return gsubLogicalLookupGraph{
		edges: edges, topological: topological, maxDepth: maximumDepth, maxExpansion: additionalExpansion,
	}, nil
}
