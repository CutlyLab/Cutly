package sfntglyf

type outlineResourceContext struct {
	upstream   Receipt
	glyfBytes  uint64
	glyphCount uint64
	limits     OutlineLimits
}

func (value outlineResourceContext) check(stats outlineScanStats, path string) error {
	return outlineCheckResourceLowerBound(value.upstream, value.glyfBytes, value.glyphCount, stats, value.limits, path)
}

func outlineResourceCharges(upstream Receipt, graphHash string, glyfBytes, glyphCount uint64, stats outlineScanStats, canonicalBytes uint64, limits OutlineLimits) (uint64, uint64, uint64, error) {
	workBase, workingBase, retained, ok := outlineResourceBase(upstream, graphHash, glyfBytes, glyphCount, stats)
	if !ok {
		return 0, 0, 0, fontError(CodeOutlineResourceLimit, "outline/resources/arithmetic", ErrResourceLimit)
	}
	work, ok := checkedAdd(workBase, canonicalBytes)
	if !ok || work > limits.MaxWorkUnits {
		return 0, 0, 0, fontError(CodeOutlineResourceLimit, "outline/resources/work_units", ErrResourceLimit)
	}
	working, ok := checkedAdd(workingBase, canonicalBytes)
	if !ok || working > limits.MaxWorkingBytes {
		return 0, 0, 0, fontError(CodeOutlineResourceLimit, "outline/resources/working_bytes", ErrResourceLimit)
	}
	if retained > limits.MaxRetainedBytes {
		return 0, 0, 0, fontError(CodeOutlineResourceLimit, "outline/resources/retained_bytes", ErrResourceLimit)
	}
	return work, working, retained, nil
}

func outlineResourceBase(upstream Receipt, graphHash string, glyfBytes, glyphCount uint64, stats outlineScanStats) (uint64, uint64, uint64, bool) {
	workTerms := [][2]uint64{
		{glyfBytes, 1}, {glyphCount, 32}, {stats.rawPoints, 12}, {stats.rawContours, 4},
		{stats.componentRecords, 32}, {stats.instructionBytes, 2}, {stats.paddingBytes, 1},
		{stats.expandedEdges, 8}, {stats.expandedPoints, 4}, {stats.expandedContours, 2},
	}
	work, ok := outlineWeightedSum(0, workTerms...)
	if !ok {
		return 0, 0, 0, false
	}
	strings := outlineReceiptStringBytes(OutlineReceipt{
		Profile: OutlineReceiptProfileV1, UpstreamProfile: upstream.Profile,
		UpstreamContentProfile: upstream.ContentHashProfile, UpstreamContentHash: upstream.ContentHash,
		UpstreamRetainedBytes: upstream.RetainedBytes,
		DescriptorHashProfile: OutlineDescriptorHashProfileV1, DescriptorHash: OutlineDescriptorHash(),
		GraphHashProfile: OutlineGraphHashProfileV1, GraphHash: graphHash,
		GeometryStatus: OutlineGeometryStatusV1, HintingStatus: OutlineHintingStatusV1,
		LayoutStatus: OutlineLayoutStatusV1, MaxpStatus: OutlineMaxpStatusV1,
		MetricsStatus: OutlineMetricsStatusV1, RoundXYStatus: OutlineRoundXYStatusV1,
		PointMatchStatus: OutlinePointMatchStatusV1, OffsetStatus: OutlineOffsetStatusV1,
	})
	retainedBase, ok := checkedAdd(upstream.RetainedBytes, 32_768)
	if !ok {
		return 0, 0, 0, false
	}
	retained, ok := outlineWeightedSum(retainedBase,
		[2]uint64{glyphCount, 128}, [2]uint64{stats.rawPoints, 24},
		[2]uint64{stats.rawContours, 8}, [2]uint64{stats.componentRecords, 64},
		[2]uint64{strings, 1})
	if !ok {
		return 0, 0, 0, false
	}
	maxPoints := uint64(stats.maxPoints)
	if uint64(stats.maxCompositePoints) > maxPoints {
		maxPoints = uint64(stats.maxCompositePoints)
	}
	maxContours := uint64(stats.maxContours)
	if uint64(stats.maxCompositeContours) > maxContours {
		maxContours = uint64(stats.maxCompositeContours)
	}
	working, ok := outlineWeightedSum(65_536,
		[2]uint64{glyphCount, 32}, [2]uint64{stats.componentRecords, 16},
		[2]uint64{maxPoints, 16}, [2]uint64{maxContours, 8},
		[2]uint64{stats.maxExpandedEdges, 8})
	if !ok {
		return 0, 0, 0, false
	}
	return work, working, retained, true
}

func outlineCheckResourceLowerBound(upstream Receipt, glyfBytes, glyphCount uint64, stats outlineScanStats, limits OutlineLimits, path string) error {
	const placeholderGraphHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	work, working, retained, ok := outlineResourceBase(upstream, placeholderGraphHash, glyfBytes, glyphCount, stats)
	if !ok || work > limits.MaxWorkUnits || working > limits.MaxWorkingBytes || retained > limits.MaxRetainedBytes {
		return fontError(CodeOutlineResourceLimit, path, ErrResourceLimit)
	}
	return nil
}

func outlineCanonicalLimit(upstream Receipt, glyfBytes, glyphCount uint64, stats outlineScanStats, limits OutlineLimits) (uint64, error) {
	const placeholderGraphHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	work, working, retained, ok := outlineResourceBase(upstream, placeholderGraphHash, glyfBytes, glyphCount, stats)
	if !ok || work > limits.MaxWorkUnits || working > limits.MaxWorkingBytes || retained > limits.MaxRetainedBytes {
		return 0, fontError(CodeOutlineResourceLimit, "outline/hash/preflight", ErrResourceLimit)
	}
	allowed := limits.MaxCanonicalBinaryBytes
	if remaining := limits.MaxWorkUnits - work; remaining < allowed {
		allowed = remaining
	}
	if remaining := limits.MaxWorkingBytes - working; remaining < allowed {
		allowed = remaining
	}
	return allowed, nil
}

func outlineWeightedSum(base uint64, terms ...[2]uint64) (uint64, bool) {
	result := base
	for _, term := range terms {
		value, ok := checkedMultiply(term[0], term[1])
		if !ok {
			return 0, false
		}
		result, ok = checkedAdd(result, value)
		if !ok {
			return 0, false
		}
	}
	return result, true
}

func outlineReceiptStringBytes(value OutlineReceipt) uint64 {
	return uint64(len(value.Profile) + len(value.UpstreamProfile) + len(value.UpstreamContentProfile) +
		len(value.UpstreamContentHash) + len(value.DescriptorHashProfile) + len(value.DescriptorHash) +
		len(value.GraphHashProfile) + len(value.GraphHash) + len(value.GeometryStatus) +
		len(value.HintingStatus) + len(value.LayoutStatus) + len(value.MaxpStatus) +
		len(value.MetricsStatus) + len(value.RoundXYStatus) + len(value.PointMatchStatus) + len(value.OffsetStatus))
}

func outlineReceiptOwnedStringBytes(value OutlineReceipt) uint64 {
	return outlineReceiptStringBytes(value)
}
