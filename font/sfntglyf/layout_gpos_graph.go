package sfntglyf

import "context"

func gposVisitSequenceLookupRecords(ctx context.Context, path string, subtable gposSubtable, visit func(gsubSequenceLookupRecord) error) error {
	switch typed := subtable.(type) {
	case *gposContext:
		if typed.context == nil {
			return fontError(codeLayoutInvariant, path+"/context", ErrInvariantFailed)
		}
		return gsubVisitSequenceLookupRecords(ctx, path+"/context", typed.context, visit)
	case *gposChainedContext:
		if typed.context == nil {
			return fontError(codeLayoutInvariant, path+"/chained_context", ErrInvariantFailed)
		}
		return gsubVisitSequenceLookupRecords(ctx, path+"/chained_context", typed.context, visit)
	case *gposExtension:
		if typed.subtable == nil || typed.extensionLookupType == 9 {
			return fontError(codeLayoutInvariant, path+"/extension", ErrInvariantFailed)
		}
		return gposVisitSequenceLookupRecords(ctx, path+"/extension", typed.subtable, visit)
	case *gposSingle, *gposPair, *gposCursive, *gposMarkBase, *gposMarkLigature, *gposMarkMark:
		return nil
	default:
		return fontError(codeLayoutInvariant, path+"/subtable", ErrInvariantFailed)
	}
}

func buildGPOSLogicalLookupGraph(ctx context.Context, lookups []*gposLookup, meter *layoutResourceMeter) (gsubLogicalLookupGraph, error) {
	var zero gsubLogicalLookupGraph
	if err := contextFailure(ctx, "layout/gpos/logical/context"); err != nil {
		return zero, err
	}
	var edgeCount uint64
	for lookupIndex, lookup := range lookups {
		if lookupIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos/logical/count"); err != nil {
				return zero, err
			}
		}
		if lookup == nil {
			return zero, fontError(codeLayoutInvariant, "layout/gpos/logical/lookup", ErrInvariantFailed)
		}
		for subtableIndex, subtable := range lookup.subtables {
			if subtableIndex%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, "layout/gpos/logical/count/subtables"); err != nil {
					return zero, err
				}
			}
			err := gposVisitSequenceLookupRecords(ctx, "layout/gpos/logical/count/walk", subtable, func(gsubSequenceLookupRecord) error {
				if edgeCount%layoutContextCheckStride == 0 {
					if err := contextFailure(ctx, "layout/gpos/logical/count/records"); err != nil {
						return err
					}
				}
				next, ok := checkedAdd(edgeCount, 1)
				if !ok || next > meter.limits.maxLogicalLookupEdges {
					return fontError(codeLayoutResource, "layout/gpos/logical/count", ErrResourceLimit)
				}
				edgeCount = next
				return nil
			})
			if err != nil {
				return zero, err
			}
		}
	}
	if edgeCount == 0 {
		return zero, nil
	}
	if err := meter.chargeLogicalEdges(edgeCount, "layout/gpos/logical"); err != nil {
		return zero, err
	}
	edgeBytes, err := layoutRetainedArrayBytes(edgeCount, gsubLogicalLookupEdgeRetainedBytes, "layout/gpos/logical/edges")
	if err != nil {
		return zero, err
	}
	topologyBytes, err := layoutRetainedArrayBytes(uint64(len(lookups)), 2, "layout/gpos/logical/topological")
	if err != nil {
		return zero, err
	}
	retained, ok := checkedAdd(edgeBytes, topologyBytes)
	if !ok {
		return zero, fontError(codeLayoutResource, "layout/gpos/logical/retained", ErrResourceLimit)
	}
	if err := meter.reserveRetained(retained+32, "layout/gpos/logical"); err != nil {
		return zero, err
	}
	if err := contextFailure(ctx, "layout/gpos/logical/allocate"); err != nil {
		return zero, err
	}
	edges, err := layoutMakeSlice[gsubLogicalLookupEdge](edgeCount, "layout/gpos/logical/edges")
	if err != nil {
		return zero, err
	}
	writeIndex := uint64(0)
	for lookupIndex, lookup := range lookups {
		if lookupIndex%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout/gpos/logical/project/lookups"); err != nil {
				return zero, err
			}
		}
		for subtableIndex, subtable := range lookup.subtables {
			if subtableIndex%layoutContextCheckStride == 0 {
				if err := contextFailure(ctx, "layout/gpos/logical/project/subtables"); err != nil {
					return zero, err
				}
			}
			var recordOrdinal uint64
			err := gposVisitSequenceLookupRecords(ctx, "layout/gpos/logical/project/walk", subtable, func(record gsubSequenceLookupRecord) error {
				if writeIndex%layoutContextCheckStride == 0 {
					if err := contextFailure(ctx, "layout/gpos/logical/project"); err != nil {
						return err
					}
				}
				if writeIndex >= uint64(len(edges)) || lookupIndex > int(^uint16(0)) || subtableIndex > int(^uint16(0)) || int(record.lookupIndex) >= len(lookups) {
					return fontError(codeLayoutInvariant, "layout/gpos/logical/project", ErrInvariantFailed)
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
		return zero, fontError(codeLayoutInvariant, "layout/gpos/logical/cardinality", ErrInvariantFailed)
	}
	if err := layoutCancelableSort(ctx, "layout/gpos/logical/sort", edges, func(left, right int) bool {
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
	return finishLayoutLogicalLookupGraph(ctx, len(lookups), edges, meter, "layout/gpos/logical")
}
