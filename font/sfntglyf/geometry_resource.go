package sfntglyf

import (
	"context"
	"fmt"
)

type geometryTopologyStats struct {
	expandedGlyphInstances      uint64
	maxExpandedGlyphsPerRoot    uint64
	transformPointVisits        uint64
	maxTransformVisitsPerGlyph  uint64
	maxExpandedPointsPerGlyph   uint64
	maxExpandedContoursPerGlyph uint64
	maxComponentDepth           uint32
}

// geometryWorkMeter makes MaxWorkUnits an execution budget rather than only
// a final receipt check. It retains the independently proved exact/canonical
// lower bounds until the corresponding observed charges overtake them, so a
// partial phase cannot consume work that every successful completion needs.
// The meter is operation-local and is never retained by GeometryFont.
type geometryWorkMeter struct {
	limit                 uint64
	fixed                 uint64
	minimumExactOps       uint64
	minimumExactLimbs     uint64
	minimumCanonicalBytes uint64
	exactOps              uint64
	exactLimbs            uint64
	canonicalBytes        uint64
}

func newGeometryWorkMeter(source *OutlineFont, topology geometryTopologyStats, exactLower, canonicalLower, limit uint64) (*geometryWorkMeter, error) {
	fixed, ok := geometryWorkBase(source, topology, 0, 0)
	if !ok {
		return nil, fontError(CodeGeometryResourceLimit, "geometry/resources/work_topology_lower_bound", ErrResourceLimit)
	}
	result := &geometryWorkMeter{
		limit: limit, fixed: fixed,
		minimumExactOps: exactLower, minimumExactLimbs: exactLower,
		minimumCanonicalBytes: canonicalLower,
	}
	minimum, ok := result.projected(0, 0, 0)
	if !ok || minimum > limit {
		return nil, fontError(CodeGeometryResourceLimit, "geometry/resources/work_topology_lower_bound", ErrResourceLimit)
	}
	return result, nil
}

func (value *geometryWorkMeter) chargeExactOperation(path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.exactOps, 1)
	if !ok || !value.accepts(next, value.exactLimbs, value.canonicalBytes) {
		return geometryWorkResourceError(path, "exact_operations")
	}
	value.exactOps = next
	return nil
}

func (value *geometryWorkMeter) chargeExactLimbs(units uint64, path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.exactLimbs, units)
	if !ok || !value.accepts(value.exactOps, next, value.canonicalBytes) {
		return geometryWorkResourceError(path, "exact_limbs")
	}
	value.exactLimbs = next
	return nil
}

func (value *geometryWorkMeter) chargeCanonicalBytes(bytes uint64, path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.canonicalBytes, bytes)
	if !ok || !value.accepts(value.exactOps, value.exactLimbs, next) {
		return geometryWorkResourceError(path, "canonical_bytes")
	}
	value.canonicalBytes = next
	return nil
}

func (value *geometryWorkMeter) accepts(exactOps, exactLimbs, canonicalBytes uint64) bool {
	work, ok := value.projected(exactOps, exactLimbs, canonicalBytes)
	return ok && work <= value.limit
}

func (value *geometryWorkMeter) projected(exactOps, exactLimbs, canonicalBytes uint64) (uint64, bool) {
	if value == nil {
		return 0, false
	}
	if exactOps < value.minimumExactOps {
		exactOps = value.minimumExactOps
	}
	if exactLimbs < value.minimumExactLimbs {
		exactLimbs = value.minimumExactLimbs
	}
	if canonicalBytes < value.minimumCanonicalBytes {
		canonicalBytes = value.minimumCanonicalBytes
	}
	return outlineWeightedSum(value.fixed,
		[2]uint64{exactOps, 8},
		[2]uint64{exactLimbs, 1},
		[2]uint64{canonicalBytes, 1},
	)
}

func (value *geometryWorkMeter) bindFinal(exactOps, exactLimbs, canonicalBytes uint64) (uint64, bool) {
	if value == nil || value.exactOps != exactOps || value.exactLimbs != exactLimbs || value.canonicalBytes != canonicalBytes ||
		exactOps < value.minimumExactOps || exactLimbs < value.minimumExactLimbs || canonicalBytes < value.minimumCanonicalBytes {
		return 0, false
	}
	work, ok := outlineWeightedSum(value.fixed,
		[2]uint64{exactOps, 8},
		[2]uint64{exactLimbs, 1},
		[2]uint64{canonicalBytes, 1},
	)
	return work, ok && work <= value.limit
}

func geometryWorkResourceError(path, charge string) error {
	return fontError(CodeGeometryResourceLimit, path+"/work/"+charge, ErrResourceLimit)
}

func computeGeometryTopology(ctx context.Context, source *OutlineFont, limits GeometryLimits) (geometryTopologyStats, error) {
	if source.receipt.ExpandedPointInstances > limits.MaxExpandedPointInstances ||
		source.receipt.ExpandedContourInstances > limits.MaxExpandedContourInstances ||
		source.receipt.ExpandedEdgeVisits > limits.MaxExpandedEdgeVisits ||
		uint64(len(source.glyphs)) > limits.MaxExpandedGlyphInstances {
		return geometryTopologyStats{}, fontError(CodeGeometryResourceLimit, "geometry/topology/source", ErrResourceLimit)
	}
	count := len(source.glyphs)
	visits := make([]uint64, count)
	instances := make([]uint64, count)
	done := make([]bool, count)
	var visit func(uint32) (uint64, uint64, error)
	visit = func(glyphID uint32) (uint64, uint64, error) {
		if uint64(glyphID) >= uint64(count) {
			return 0, 0, fontError(CodeGeometryInvariant, "geometry/topology/glyph_id", ErrInvariantFailed)
		}
		if done[glyphID] {
			return visits[glyphID], instances[glyphID], nil
		}
		glyph := source.glyphs[glyphID]
		var total uint64
		totalInstances := uint64(1)
		for index, component := range glyph.components {
			if index%1024 == 0 {
				if err := contextFailure(ctx, fmt.Sprintf("geometry/topology/%d/%d", glyphID, index)); err != nil {
					return 0, 0, err
				}
			}
			childVisits, childInstances, err := visit(component.glyphID)
			if err != nil {
				return 0, 0, err
			}
			var ok bool
			totalInstances, ok = checkedAdd(totalInstances, childInstances)
			if !ok || totalInstances > limits.MaxExpandedGlyphInstances {
				return 0, 0, fontError(CodeGeometryResourceLimit, "geometry/topology/expanded_glyph_instances", ErrResourceLimit)
			}
			componentVisits, ok := checkedAdd(source.glyphs[component.glyphID].expandedPoints, childVisits)
			if !ok {
				return 0, 0, fontError(CodeGeometryResourceLimit, "geometry/topology/transform_visits", ErrResourceLimit)
			}
			total, ok = checkedAdd(total, componentVisits)
			if !ok || total > limits.MaxTransformPointVisits {
				return 0, 0, fontError(CodeGeometryResourceLimit, "geometry/topology/transform_visits", ErrResourceLimit)
			}
		}
		done[glyphID], visits[glyphID], instances[glyphID] = true, total, totalInstances
		return total, totalInstances, nil
	}
	result := geometryTopologyStats{}
	for glyphID, glyph := range source.glyphs {
		if glyphID%256 == 0 {
			if err := contextFailure(ctx, "geometry/topology/roots"); err != nil {
				return geometryTopologyStats{}, err
			}
		}
		value, glyphInstances, err := visit(uint32(glyphID))
		if err != nil {
			return geometryTopologyStats{}, err
		}
		var ok bool
		result.expandedGlyphInstances, ok = checkedAdd(result.expandedGlyphInstances, glyphInstances)
		if !ok || result.expandedGlyphInstances > limits.MaxExpandedGlyphInstances {
			return geometryTopologyStats{}, fontError(CodeGeometryResourceLimit, "geometry/topology/expanded_glyph_instances", ErrResourceLimit)
		}
		result.transformPointVisits, ok = checkedAdd(result.transformPointVisits, value)
		if !ok || result.transformPointVisits > limits.MaxTransformPointVisits {
			return geometryTopologyStats{}, fontError(CodeGeometryResourceLimit, "geometry/topology/transform_visits", ErrResourceLimit)
		}
		if value > result.maxTransformVisitsPerGlyph {
			result.maxTransformVisitsPerGlyph = value
		}
		if glyphInstances > result.maxExpandedGlyphsPerRoot {
			result.maxExpandedGlyphsPerRoot = glyphInstances
		}
		if glyph.expandedPoints > result.maxExpandedPointsPerGlyph {
			result.maxExpandedPointsPerGlyph = glyph.expandedPoints
		}
		if glyph.expandedContours > result.maxExpandedContoursPerGlyph {
			result.maxExpandedContoursPerGlyph = glyph.expandedContours
		}
		if glyph.componentDepth > result.maxComponentDepth {
			result.maxComponentDepth = glyph.componentDepth
		}
	}
	return result, nil
}

func geometryWorkingBytes(glyphCount uint64, topology geometryTopologyStats, exactBits uint32) (uint64, bool) {
	dyadicBytes, ok := checkedAdd(64, (uint64(exactBits)+7)/8)
	if !ok {
		return 0, false
	}
	exactPointBytes, ok := checkedMultiply(dyadicBytes, 2)
	if ok {
		exactPointBytes, ok = checkedAdd(exactPointBytes, 32)
	}
	if !ok {
		return 0, false
	}
	working, ok := checkedMultiply(topology.maxExpandedPointsPerGlyph, exactPointBytes)
	if ok {
		working, ok = checkedMultiply(working, 3)
	}
	if ok {
		working, ok = checkedAdd(working, 69_632)
	}
	glyphScratch, glyphScratchOK := checkedMultiply(24, glyphCount)
	if ok && glyphScratchOK {
		working, ok = checkedAdd(working, glyphScratch)
	} else {
		ok = false
	}
	contours, contoursOK := checkedMultiply(topology.maxExpandedContoursPerGlyph, 8)
	if ok && contoursOK {
		working, ok = checkedAdd(working, contours)
	} else {
		ok = false
	}
	depth, depthOK := checkedAdd(uint64(topology.maxComponentDepth), 1)
	if depthOK {
		depth, depthOK = checkedMultiply(depth, 128)
	}
	if ok && depthOK {
		working, ok = checkedAdd(working, depth)
	} else {
		ok = false
	}
	return working, ok
}

func geometryWorkBase(source *OutlineFont, topology geometryTopologyStats, exactOps, exactLimbs uint64) (uint64, bool) {
	return outlineWeightedSum(0,
		[2]uint64{source.receipt.CanonicalBinaryBytes, 1},
		[2]uint64{uint64(len(source.glyphs)), 64},
		[2]uint64{source.receipt.ExpandedPointInstances, 16},
		[2]uint64{source.receipt.ExpandedContourInstances, 8},
		[2]uint64{source.receipt.ExpandedEdgeVisits, 32},
		[2]uint64{topology.expandedGlyphInstances, 16},
		[2]uint64{topology.transformPointVisits, 12},
		[2]uint64{exactOps, 8},
		[2]uint64{exactLimbs, 1},
	)
}

func geometryExactOperationLowerBound(source *OutlineFont) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	exactLower, ok := checkedMultiply(source.receipt.ExpandedPointInstances, 2)
	componentLower, componentOK := checkedMultiply(source.receipt.ExpandedEdgeVisits, 4)
	if !ok || !componentOK {
		return 0, false
	}
	return checkedAdd(exactLower, componentLower)
}

// geometrySourceRebindWorkLowerBound uses only already-owned scalar receipt
// evidence and slice length. It is an admission check, not trust in that
// evidence: validateGeometrySource still recounts and rehashes everything.
func geometrySourceRebindWorkLowerBound(source *OutlineFont) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	topology := geometryTopologyStats{}
	var ok bool
	topology.expandedGlyphInstances, ok = checkedAdd(uint64(len(source.glyphs)), source.receipt.ExpandedEdgeVisits)
	if !ok {
		return 0, false
	}
	exactLower, ok := geometryExactOperationLowerBound(source)
	if !ok {
		return 0, false
	}
	canonicalLower, ok := geometryCanonicalLowerBound(source, GeometryHeadLSBUnsetV1)
	if !ok {
		return 0, false
	}
	work, ok := geometryWorkBase(source, topology, exactLower, exactLower)
	if !ok {
		return 0, false
	}
	return checkedAdd(work, canonicalLower)
}

func geometryCanonicalLowerBound(source *OutlineFont, headLSBStatus string) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	header := uint64(4 + 4)
	for _, text := range []string{
		GeometryHashProfileV1, GeometryProfileV1, GeometryDescriptorHash(), source.receipt.Profile,
		source.receipt.DescriptorHash, source.receipt.GraphHash, source.receipt.UpstreamContentProfile,
		source.receipt.UpstreamContentHash,
	} {
		var ok bool
		header, ok = checkedAdd(header, 8+uint64(len(text)))
		if !ok {
			return 0, false
		}
	}
	// Each glyph has 50 fixed bytes. Each contour endpoint has four bytes. A
	// point has two minimal zero dyadics (13 bytes each) plus on-curve byte.
	result, ok := outlineWeightedSum(header,
		[2]uint64{uint64(len(source.glyphs)), 50},
		[2]uint64{source.receipt.ExpandedContourInstances, 4},
		[2]uint64{source.receipt.ExpandedPointInstances, 27},
	)
	if !ok {
		return 0, false
	}
	return checkedAdd(result, 28+8+uint64(len(headLSBStatus)))
}

func geometryRetainedLowerBound(source *OutlineFont) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	base, ok := checkedAdd(source.receipt.RetainedBytes, 32_768)
	if !ok {
		return 0, false
	}
	return outlineWeightedSum(base, [2]uint64{uint64(len(source.glyphs)), 64})
}

func geometryRetainedBytes(source *OutlineFont, receipt GeometryReceipt) (uint64, bool) {
	strings := geometryReceiptStringBytes(receipt)
	base, ok := checkedAdd(source.receipt.RetainedBytes, 32_768)
	if !ok {
		return 0, false
	}
	return outlineWeightedSum(base, [2]uint64{uint64(len(source.glyphs)), 64}, [2]uint64{strings, 1})
}

func geometryReceiptStringBytes(value GeometryReceipt) uint64 {
	values := []string{
		value.Profile, value.SourceProfile, value.SourceDescriptorHashProfile, value.SourceDescriptorHash,
		value.SourceGraphHashProfile, value.SourceGraphHash, value.UpstreamContentHashProfile,
		value.UpstreamContentHash, value.DescriptorHashProfile, value.DescriptorHash,
		value.GeometryHashProfile, value.GeometryHash, value.GeometryStatus, value.BBoxStatus,
		value.HeadBBoxStatus, value.HheaStatus, value.HeadLSBStatus, value.MetricsStatus,
		value.HintingStatus, value.LayoutStatus, value.CommandStatus, value.CapabilityStatus,
	}
	var total uint64
	for _, text := range values {
		total += uint64(len(text))
	}
	return total
}
