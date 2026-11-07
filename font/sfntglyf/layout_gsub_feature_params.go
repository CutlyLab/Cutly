package sfntglyf

func (value *layoutReader) parseFeatureParams(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, tag uint32, path string) (layoutFeatureParams, error) {
	if layoutIsCVTag(tag) && value.tag == layoutGSUBTag {
		return value.parseCVFeatureParams(parent, edge, start, path)
	}
	if layoutIsSSTag(tag) {
		return value.parseSSFeatureParams(parent, edge, start, path)
	}
	if tag == layoutSizeTag && value.tag == layoutGPOSTag {
		return value.parseSizeFeatureParams(parent, edge, start, path)
	}
	return nil, fontError(codeLayoutUnsupported, path+"/tag", errLayoutUnsupported)
}

const layoutSizeTag uint32 = 0x73697a65

func (value *layoutReader) parseSizeFeatureParams(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*layoutSizeFeatureParams, error) {
	if _, err := value.bytes(start, 10, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, start+10, layoutSpanFeatureParamsSize, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutSizeFeatureParams](node, path)
	}
	design := mustLayoutU16(value, start, path+"/design_size")
	subfamily := mustLayoutU16(value, start+2, path+"/subfamily_id")
	nameID := mustLayoutU16(value, start+4, path+"/subfamily_name_id")
	rangeStart := mustLayoutU16(value, start+6, path+"/range_start")
	rangeEnd := mustLayoutU16(value, start+8, path+"/range_end")
	if design == 0 {
		return nil, fontError(codeLayoutInvalid, path+"/design_size", errLayoutInvalid)
	}
	if subfamily == 0 {
		if nameID != 0 || rangeStart != 0 || rangeEnd != 0 {
			return nil, fontError(codeLayoutInvalid, path+"/no_range_fields", errLayoutInvalid)
		}
	} else if nameID < 256 || nameID > 32767 || rangeStart >= rangeEnd {
		return nil, fontError(codeLayoutInvalid, path+"/range_fields", errLayoutInvalid)
	}
	if err := value.meter.charge(&value.meter.stats.featureParamTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(24, path); err != nil {
		return nil, err
	}
	result := &layoutSizeFeatureParams{
		designSize: design, subfamilyID: subfamily, subfamilyNameID: nameID,
		rangeStart: rangeStart, rangeEnd: rangeEnd,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseCVFeatureParams(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*layoutCVFeatureParams, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	if format != 0 {
		return nil, fontError(codeLayoutUnsupported, path+"/format", errLayoutUnsupported)
	}
	characterCount, err := value.u16(start+12, path+"/character_count")
	if err != nil {
		return nil, err
	}
	end, err := layoutSpanEnd(start, 14, uint64(characterCount), 3, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanFeatureParamsCV, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutCVFeatureParams](node, path)
	}
	namedParameterCount, _ := value.u16(start+8, path+"/named_parameter_count")
	firstParameterNameID, _ := value.u16(start+10, path+"/first_parameter_ui_label_name_id")
	if namedParameterCount == 0 && firstParameterNameID != 0 {
		return nil, fontError(codeLayoutInvalid, path+"/first_parameter_ui_label_name_id", errLayoutInvalid)
	}
	if err := value.meter.charge(&value.meter.stats.featureParamTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.chargeFeatureCharacters(uint64(characterCount), path); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(32+uint64(characterCount)*4, path); err != nil {
		return nil, err
	}
	if err := contextFailure(value.ctx, path+"/characters/allocate"); err != nil {
		return nil, err
	}
	characters, err := layoutMakeSlice[uint32](uint64(characterCount), path+"/characters")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(characterCount); index++ {
		if err := value.checkContext(index, path+"/characters"); err != nil {
			return nil, err
		}
		character, _ := value.u24(start+14+3*index, path+"/characters")
		if character > 0x10ffff || (character >= 0xd800 && character <= 0xdfff) {
			return nil, fontError(codeLayoutInvalid, layoutPath(path+"/characters", index), errLayoutInvalid)
		}
		characters[index] = character
	}
	result := &layoutCVFeatureParams{
		featureUILabelNameID:        mustLayoutU16(value, start+2, path+"/feature_ui_label_name_id"),
		featureUITooltipTextNameID:  mustLayoutU16(value, start+4, path+"/feature_ui_tooltip_text_name_id"),
		sampleTextNameID:            mustLayoutU16(value, start+6, path+"/sample_text_name_id"),
		namedParameterCount:         namedParameterCount,
		firstParameterUILabelNameID: firstParameterNameID,
		characters:                  characters,
	}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func (value *layoutReader) parseSSFeatureParams(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, path string) (*layoutSSFeatureParams, error) {
	version, err := value.u16(start, path+"/version")
	if err != nil {
		return nil, err
	}
	if version != 0 {
		return nil, fontError(codeLayoutUnsupported, path+"/version", errLayoutUnsupported)
	}
	if _, err := value.bytes(start, 4, path); err != nil {
		return nil, err
	}
	node, alias, err := value.graph.begin(parent, edge, start, start+4, layoutSpanFeatureParamsSS, layoutDecodeParams{}, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*layoutSSFeatureParams](node, path)
	}
	if err := value.meter.charge(&value.meter.stats.featureParamTables, 1, value.meter.limits.maxOrderedRecords, path+"/tables"); err != nil {
		return nil, err
	}
	if err := value.meter.reserveRetained(16, path); err != nil {
		return nil, err
	}
	result := &layoutSSFeatureParams{uiLabelNameID: mustLayoutU16(value, start+2, path+"/ui_label_name_id")}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func layoutIsCVTag(tag uint32) bool {
	if byte(tag>>24) != 'c' || byte(tag>>16) != 'v' || byte(tag>>8) < '0' || byte(tag>>8) > '9' || byte(tag) < '0' || byte(tag) > '9' {
		return false
	}
	number := int(byte(tag>>8)-'0')*10 + int(byte(tag)-'0')
	return number >= 1 && number <= 99
}

func layoutIsSSTag(tag uint32) bool {
	if byte(tag>>24) != 's' || byte(tag>>16) != 's' || byte(tag>>8) < '0' || byte(tag>>8) > '9' || byte(tag) < '0' || byte(tag) > '9' {
		return false
	}
	value := int(byte(tag>>8)-'0')*10 + int(byte(tag)-'0')
	return value >= 1 && value <= 20
}

// The caller has already checked the containing direct span, so these reads
// cannot fail. Keeping the helper private avoids obscuring actual boundary
// checks at the start of each decoder.
func mustLayoutU16(value *layoutReader, offset uint64, path string) uint16 {
	result, err := value.u16(offset, path)
	if err != nil {
		panic(err)
	}
	return result
}
