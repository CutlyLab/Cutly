package sfntglyf

const layoutDFLTTag uint32 = 0x44464c54

func (value *layoutReader) parseScriptList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, featureCount uint16, path string) ([]layoutScriptRecord, error) {
	count, err := value.u16(start, path+"/count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 6, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanScriptList, layoutDecodeParams{first: uint64(featureCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[[]layoutScriptRecord](node, path)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.scriptRecords, uint64(count), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*24, path+"/records"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[layoutScriptRecord](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	var previous uint32
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/records"); err != nil {
			return nil, err
		}
		recordStart := start + 2 + 6*index
		tag, _ := value.u32(recordStart, path+"/records/tag")
		rawOffset, _ := value.u16(recordStart+4, path+"/records/offset")
		if index != 0 && tag <= previous {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/records", index)+"/tag_order", errLayoutInvalid)
		}
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/records", index)+"/offset")
		if err != nil {
			return nil, err
		}
		script, err := value.parseScript(node, layoutEdgeRef{role: layoutEdgeScript, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, featureCount, layoutPath(path+"/scripts", index))
		if err != nil {
			return nil, err
		}
		if tag == layoutDFLTTag && script.defaultLanguage == nil {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/scripts", index)+"/dflt_default", errLayoutInvalid)
		}
		records[index] = layoutScriptRecord{tag: tag, script: script}
		previous = tag
	}
	if err := value.graph.complete(node, records, path); err != nil {
		return nil, err
	}
	return records, nil
}

func (value *layoutReader) parseScript(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, featureCount uint16, path string) (*layoutScript, error) {
	defaultOffset, err := value.u16(start, path+"/default_lang_sys_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+2, path+"/language_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 4, uint64(count), 6, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanScript, layoutDecodeParams{first: uint64(featureCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutScript](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.scriptTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.langSysRecords, uint64(count), path+"/languages"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32+uint64(count)*24, path); err != nil {
		return nil, err
	}
	result := &layoutScript{}
	if defaultOffset != 0 {
		childStart, err := value.childOffset(start, uint64(defaultOffset), path+"/default_lang_sys_offset")
		if err != nil {
			return nil, err
		}
		result.defaultLanguage, err = value.parseLangSys(node, layoutEdgeRef{role: layoutEdgeDefaultLangSys, rawOffset: uint32(defaultOffset)}, childStart, featureCount, path+"/default_lang_sys")
		if err != nil {
			return nil, err
		}
	}
	if err := contextFailure(value.ctx, path+"/languages/allocate"); err != nil {
		return nil, err
	}
	result.languages, err = layoutMakeSlice[layoutLangSysRecord](uint64(count), path+"/languages")
	if err != nil {
		return nil, err
	}
	var previous uint32
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/languages"); err != nil {
			return nil, err
		}
		recordStart := start + 4 + 6*index
		tag, _ := value.u32(recordStart, path+"/languages/tag")
		rawOffset, _ := value.u16(recordStart+4, path+"/languages/offset")
		if index != 0 && tag <= previous {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/languages", index)+"/tag_order", errLayoutInvalid)
		}
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/languages", index)+"/offset")
		if err != nil {
			return nil, err
		}
		language, err := value.parseLangSys(node, layoutEdgeRef{role: layoutEdgeLangSys, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, featureCount, layoutPath(path+"/lang_sys", index))
		if err != nil {
			return nil, err
		}
		result.languages[index] = layoutLangSysRecord{tag: tag, language: language}
		previous = tag
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseLangSys(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, featureCount uint16, path string) (*layoutLangSys, error) {
	lookupOrder, err := value.u16(start, path+"/lookup_order_offset")
	if err != nil {
		return nil, err
	}
	required, err := value.u16(start+2, path+"/required_feature_index")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+4, path+"/feature_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 6, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	if lookupOrder != 0 || (required != 0xffff && required >= featureCount) {
		return nil, fontError(codeLayoutInvalid, path+"/indices", errLayoutInvalid)
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanLangSys, layoutDecodeParams{first: uint64(featureCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutLangSys](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.langSysTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.langSysFeatureReferences, uint64(count), path+"/features"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*2, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/features/allocate"); err != nil {
		return nil, err
	}
	indices, err := layoutMakeSlice[uint16](uint64(count), path+"/features")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/features"); err != nil {
			return nil, err
		}
		featureIndex, _ := value.u16(start+6+2*index, path+"/features")
		if featureIndex >= featureCount {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/features", index), errLayoutInvalid)
		}
		indices[index] = featureIndex
	}
	result := &layoutLangSys{requiredFeatureIndex: required, featureIndices: indices}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseFeatureList(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, lookupCount uint16, path string) ([]layoutFeatureRecord, error) {
	count, err := value.u16(start, path+"/count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 2, uint64(count), 6, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanFeatureList, layoutDecodeParams{first: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[[]layoutFeatureRecord](node, path)
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.featureRecords, uint64(count), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(uint64(count)*32, path+"/records"); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/records/allocate"); err != nil {
		return nil, err
	}
	records, err := layoutMakeSlice[layoutFeatureRecord](uint64(count), path+"/records")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/records"); err != nil {
			return nil, err
		}
		recordStart := start + 2 + 6*index
		tag, _ := value.u32(recordStart, path+"/records/tag")
		rawOffset, _ := value.u16(recordStart+4, path+"/records/offset")
		childStart, err := value.childOffset(start, uint64(rawOffset), layoutPath(path+"/records", index)+"/offset")
		if err != nil {
			return nil, err
		}
		feature, err := value.parseFeature(node, layoutEdgeRef{role: layoutEdgeFeature, ordinal: uint32(index), rawOffset: uint32(rawOffset)}, childStart, lookupCount, layoutPath(path+"/features", index))
		if err != nil {
			return nil, err
		}
		if tag == layoutSizeTag {
			if value.tag != layoutGPOSTag || feature.paramsOffset == 0 || len(feature.lookupIndices) != 0 {
				return nil, fontError(codeLayoutInvalid, layoutPath(path+"/features", index)+"/size_contract", errLayoutInvalid)
			}
		}
		var params layoutFeatureParams
		if feature.paramsOffset != 0 {
			paramsStart, err := value.childOffset(childStart, uint64(feature.paramsOffset), layoutPath(path+"/features", index)+"/params_offset")
			if err != nil {
				return nil, err
			}
			params, err = value.parseFeatureParams(featureNodeForStart(value.graph, childStart), layoutEdgeRef{role: layoutEdgeFeatureParams, ordinal: uint32(index), rawOffset: uint32(feature.paramsOffset)}, paramsStart, tag, layoutPath(path+"/features", index)+"/params")
			if err != nil {
				return nil, err
			}
		}
		records[index] = layoutFeatureRecord{tag: tag, feature: feature, params: params}
	}
	if err := value.graph.complete(node, records, path); err != nil {
		return nil, err
	}
	return records, nil
}

func featureNodeForStart(graph *layoutSpanGraph, start uint64) *layoutSpanNode {
	return graph.nodes[start]
}

func (value *layoutReader) parseFeature(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, lookupCount uint16, path string) (*layoutFeature, error) {
	paramsOffset, err := value.u16(start, path+"/params_offset")
	if err != nil {
		return nil, err
	}
	count, err := value.u16(start+2, path+"/lookup_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 4, uint64(count), 2, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanFeature, layoutDecodeParams{first: uint64(lookupCount)}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutFeature](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.featureTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeOrdered(&value.meter.stats.featureLookupReferences, uint64(count), path+"/lookups"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24+uint64(count)*2, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/lookups/allocate"); err != nil {
		return nil, err
	}
	indices, err := layoutMakeSlice[uint16](uint64(count), path+"/lookups")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/lookups"); err != nil {
			return nil, err
		}
		lookupIndex, _ := value.u16(start+4+2*index, path+"/lookups")
		if lookupIndex >= lookupCount {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/lookups", index), errLayoutInvalid)
		}
		indices[index] = lookupIndex
	}
	result := &layoutFeature{paramsOffset: paramsOffset, lookupIndices: indices}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
