package sfntglyf

import "math"

type outlineSetWorkMeter struct {
	limit                 uint64
	maxExactOps           uint64
	maxExactLimbs         uint64
	fixed                 uint64
	minimumExactOps       uint64
	minimumExactLimbs     uint64
	minimumCanonicalBytes uint64
	exactOps              uint64
	exactLimbs            uint64
	canonicalBytes        uint64
}

func newOutlineSetWorkMeter(source *GeometryFont, topology geometryTopologyStats, commands outlineSetTopologyStats,
	exactLower, limbLower, canonicalLower uint64, limits OutlineSetLimits) (*outlineSetWorkMeter, error) {
	fixed, ok := outlineSetWorkBase(source, topology, commands, 0, 0, 0)
	if !ok {
		return nil, outlineSetResource("outline_set/resources/work_base")
	}
	result := &outlineSetWorkMeter{
		limit: limits.MaxWorkUnits, maxExactOps: limits.MaxExactOps, maxExactLimbs: limits.MaxExactLimbUnits,
		fixed: fixed, minimumExactOps: exactLower, minimumExactLimbs: limbLower,
		minimumCanonicalBytes: canonicalLower,
	}
	work, ok := result.projected(0, 0, 0)
	if !ok || work > limits.MaxWorkUnits || exactLower > limits.MaxExactOps || limbLower > limits.MaxExactLimbUnits {
		return nil, outlineSetResource("outline_set/resources/work_lower_bound")
	}
	return result, nil
}

func (value *outlineSetWorkMeter) chargeExactOperation(path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.exactOps, 1)
	if !ok || next > value.maxExactOps || !value.accepts(next, value.exactLimbs, value.canonicalBytes) {
		return outlineSetResource(path + "/work/exact_operations")
	}
	value.exactOps = next
	return nil
}

func (value *outlineSetWorkMeter) chargeExactLimbs(units uint64, path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.exactLimbs, units)
	if !ok || next > value.maxExactLimbs || !value.accepts(value.exactOps, next, value.canonicalBytes) {
		return outlineSetResource(path + "/work/exact_limbs")
	}
	value.exactLimbs = next
	return nil
}

func (value *outlineSetWorkMeter) chargeCanonicalBytes(bytes uint64, path string) error {
	if value == nil {
		return nil
	}
	next, ok := checkedAdd(value.canonicalBytes, bytes)
	if !ok || !value.accepts(value.exactOps, value.exactLimbs, next) {
		return outlineSetResource(path + "/work/canonical_bytes")
	}
	value.canonicalBytes = next
	return nil
}

func (value *outlineSetWorkMeter) accepts(exactOps, exactLimbs, canonicalBytes uint64) bool {
	work, ok := value.projected(exactOps, exactLimbs, canonicalBytes)
	return ok && work <= value.limit
}

func (value *outlineSetWorkMeter) projected(exactOps, exactLimbs, canonicalBytes uint64) (uint64, bool) {
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
		[2]uint64{exactOps, 8}, [2]uint64{exactLimbs, 1}, [2]uint64{canonicalBytes, 1})
}

func (value *outlineSetWorkMeter) bindFinal(exactOps, exactLimbs, canonicalBytes uint64) (uint64, bool) {
	if value == nil || value.exactOps != exactOps || value.exactLimbs != exactLimbs || value.canonicalBytes != canonicalBytes ||
		exactOps < value.minimumExactOps || exactLimbs < value.minimumExactLimbs || canonicalBytes < value.minimumCanonicalBytes {
		return 0, false
	}
	work, ok := outlineWeightedSum(value.fixed,
		[2]uint64{exactOps, 8}, [2]uint64{exactLimbs, 1}, [2]uint64{canonicalBytes, 1})
	return work, ok && work <= value.limit
}

func outlineSetWorkBase(source *GeometryFont, topology geometryTopologyStats, commands outlineSetTopologyStats,
	exactOps, exactLimbs, canonicalBytes uint64) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	receipt := source.receipt
	return outlineWeightedSum(0,
		[2]uint64{receipt.SourceCanonicalBinaryBytes, 1},
		[2]uint64{uint64(receipt.GlyphCount), 64},
		[2]uint64{receipt.ExpandedPointInstances, 16},
		[2]uint64{receipt.ExpandedContourInstances, 8},
		[2]uint64{receipt.ExpandedEdgeVisits, 32},
		[2]uint64{topology.expandedGlyphInstances, 16},
		[2]uint64{topology.transformPointVisits, 12},
		[2]uint64{commands.implied, 16},
		[2]uint64{commands.drawing, 12},
		[2]uint64{commands.commands, 8},
		[2]uint64{commands.commandPoints, 2},
		[2]uint64{exactOps, 8},
		[2]uint64{exactLimbs, 1},
		[2]uint64{canonicalBytes, 1},
	)
}

func outlineSetMaterializationExactLower(source *GeometryFont, commands outlineSetTopologyStats) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	realRounds, ok := checkedMultiply(source.receipt.ExpandedPointInstances, 2)
	impliedOps, impliedOK := checkedMultiply(commands.implied, 6)
	if !ok || !impliedOK {
		return 0, false
	}
	return checkedAdd(realRounds, impliedOps)
}

func outlineSetCanonicalBytes(source *GeometryFont, commands outlineSetTopologyStats) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	receipt := source.receipt
	result := uint64(8) // schema and glyph count
	for _, text := range []string{
		OutlineSetHashProfileV1, OutlineSetProfileV1, OutlineSetOutlineProfileV1, OutlineSetDescriptorHash(),
		receipt.Profile, receipt.DescriptorHashProfile, receipt.DescriptorHash,
		receipt.GeometryHashProfile, receipt.GeometryHash,
		receipt.SourceGraphHashProfile, receipt.SourceGraphHash,
		receipt.UpstreamContentHashProfile, receipt.UpstreamContentHash,
	} {
		var ok bool
		result, ok = checkedAdd(result, 8+uint64(len(text)))
		if !ok {
			return 0, false
		}
	}
	var ok bool
	result, ok = checkedAdd(result, 16) // two source canonical u64 values
	if !ok {
		return 0, false
	}
	result, ok = outlineWeightedSum(result,
		[2]uint64{uint64(receipt.GlyphCount), 20},
		[2]uint64{commands.commands, 2},
		[2]uint64{commands.commandPoints, 16},
	)
	if !ok {
		return 0, false
	}
	return checkedAdd(result, 56)
}

func outlineSetWorkingBytes(glyphCount uint64, topology geometryTopologyStats, exactBits uint32) (uint64, bool) {
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
		working, ok = checkedAdd(working, 98_304)
	}
	glyphScratch, glyphOK := checkedMultiply(glyphCount, 64)
	if ok && glyphOK {
		working, ok = checkedAdd(working, glyphScratch)
	} else {
		ok = false
	}
	contours, contourOK := checkedMultiply(topology.maxExpandedContoursPerGlyph, 8)
	if ok && contourOK {
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

func outlineSetRetainedLowerBound(source *GeometryFont, glyphCount, commands uint64) (uint64, bool) {
	if source == nil {
		return 0, false
	}
	base, ok := checkedAdd(source.receipt.RetainedBytes, 32_768)
	if !ok {
		return 0, false
	}
	result, ok := outlineWeightedSum(base, [2]uint64{glyphCount, 32}, [2]uint64{commands, 40})
	if !ok || result > uint64(math.MaxInt32) {
		return 0, false
	}
	commandBytes, commandOK := checkedMultiply(commands, 40)
	glyphBytes, glyphOK := checkedMultiply(glyphCount, 32)
	if !commandOK || !glyphOK || commandBytes > uint64(math.MaxInt32) || glyphBytes > uint64(math.MaxInt32) {
		return 0, false
	}
	return result, true
}

func outlineSetRetainedBytes(source *GeometryFont, glyphCount, commands uint64, receipt OutlineSetReceipt) (uint64, bool) {
	base, ok := outlineSetRetainedLowerBound(source, glyphCount, commands)
	if !ok {
		return 0, false
	}
	result, ok := checkedAdd(base, outlineSetReceiptStringBytes(receipt))
	if !ok || result > uint64(math.MaxInt32) {
		return 0, false
	}
	return result, true
}

func outlineSetReceiptStringBytes(value OutlineSetReceipt) uint64 {
	values := []string{
		value.Profile, value.OutlineProfile, value.SourceProfile, value.SourceDescriptorHashProfile,
		value.SourceDescriptorHash, value.SourceGeometryHashProfile, value.SourceGeometryHash,
		value.SourceGraphHashProfile, value.SourceGraphHash, value.UpstreamContentHashProfile,
		value.UpstreamContentHash, value.DescriptorHashProfile, value.DescriptorHash,
		value.OutlineSetHashProfile, value.OutlineSetHash, value.GeometryStatus,
		value.ImpliedPointStatus, value.CommandStatus, value.RoundingStatus, value.HintingStatus,
		value.LayoutStatus, value.RasterStatus, value.CapabilityStatus,
	}
	var total uint64
	for _, text := range values {
		total += uint64(len(text))
	}
	return total
}
