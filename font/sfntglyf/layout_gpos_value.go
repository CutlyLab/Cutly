package sfntglyf

const gposValueRecordRetainedBytes uint64 = 64

func gposValueRecordSize(format uint16, path string) (uint64, error) {
	if format&0xff00 != 0 {
		return 0, fontError(codeLayoutInvalid, path+"/value_format", errLayoutInvalid)
	}
	var fields uint64
	for bits := format; bits != 0; bits >>= 1 {
		fields += uint64(bits & 1)
	}
	size, ok := checkedMultiply(fields, 2)
	if !ok {
		return 0, fontError(codeLayoutInvalid, path+"/value_size", errLayoutInvalid)
	}
	return size, nil
}

func (value *layoutReader) admitGPOSValueRecords(count uint64, path string) error {
	if err := value.meter.chargeOrdered(&value.meter.stats.valueRecords, count, path); err != nil {
		return err
	}
	bytes, err := layoutRetainedArrayBytes(count, gposValueRecordRetainedBytes, path)
	if err != nil {
		return err
	}
	return value.meter.reserveRetained(bytes, path)
}

func (value *layoutReader) parseGPOSValueRecord(parent *layoutSpanNode, start, deviceBase uint64, format uint16, recordOrdinal uint64, path string) (gposValueRecord, error) {
	var result gposValueRecord
	size, err := gposValueRecordSize(format, path)
	if err != nil {
		return result, err
	}
	if _, err := value.bytes(start, size, path); err != nil {
		return result, err
	}
	if recordOrdinal > uint64((^uint32(0)-3)/4) {
		return result, fontError(codeLayoutResource, path+"/ordinal", ErrResourceLimit)
	}
	cursor := start
	readScalar := func(bit uint16, target *int16, name string) error {
		if format&bit == 0 {
			return nil
		}
		raw, err := value.u16(cursor, path+"/"+name)
		if err != nil {
			return err
		}
		*target = int16(raw)
		cursor += 2
		return nil
	}
	if err := readScalar(0x0001, &result.xPlacement, "x_placement"); err != nil {
		return result, err
	}
	if err := readScalar(0x0002, &result.yPlacement, "y_placement"); err != nil {
		return result, err
	}
	if err := readScalar(0x0004, &result.xAdvance, "x_advance"); err != nil {
		return result, err
	}
	if err := readScalar(0x0008, &result.yAdvance, "y_advance"); err != nil {
		return result, err
	}
	parseDevice := func(bit uint16, slot uint32, target **gdefDevice, name string) error {
		if format&bit == 0 {
			return nil
		}
		raw, err := value.u16(cursor, path+"/"+name+"_offset")
		if err != nil {
			return err
		}
		cursor += 2
		if raw == 0 {
			return nil
		}
		childStart, err := value.childOffset(deviceBase, uint64(raw), path+"/"+name+"_offset")
		if err != nil {
			return err
		}
		device, err := value.parseDevice(parent, layoutEdgeRef{
			role: layoutEdgeGPOSValueDevice, ordinal: uint32(recordOrdinal*4) + slot, rawOffset: uint32(raw),
		}, childStart, path+"/"+name)
		if err != nil {
			return err
		}
		*target = device
		return nil
	}
	if err := parseDevice(0x0010, 0, &result.xPlaDevice, "x_placement_device"); err != nil {
		return result, err
	}
	if err := parseDevice(0x0020, 1, &result.yPlaDevice, "y_placement_device"); err != nil {
		return result, err
	}
	if err := parseDevice(0x0040, 2, &result.xAdvDevice, "x_advance_device"); err != nil {
		return result, err
	}
	if err := parseDevice(0x0080, 3, &result.yAdvDevice, "y_advance_device"); err != nil {
		return result, err
	}
	if cursor != start+size {
		return result, fontError(codeLayoutInvariant, path+"/cursor", ErrInvariantFailed)
	}
	return result, nil
}

func (value *layoutReader) parseGPOSSingle(parent *layoutSpanNode, edge layoutEdgeRef, start uint64, numGlyphs uint32, path string) (*gposSingle, error) {
	format, err := value.u16(start, path+"/format")
	if err != nil {
		return nil, err
	}
	coverageOffset, err := value.u16(start+2, path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	valueFormat, err := value.u16(start+4, path+"/value_format")
	if err != nil {
		return nil, err
	}
	recordSize, err := gposValueRecordSize(valueFormat, path)
	if err != nil {
		return nil, err
	}
	var count uint16 = 1
	headerSize := uint64(6)
	if format == 2 {
		count, err = value.u16(start+6, path+"/value_count")
		if err != nil {
			return nil, err
		}
		headerSize = 8
	} else if format != 1 {
		return nil, fontError(codeLayoutInvalid, path+"/format", errLayoutInvalid)
	}
	end, err := layoutSpanEnd(start, headerSize, uint64(count), recordSize, path)
	if err != nil {
		return nil, err
	}
	if _, err := value.bytes(start, end-start, path); err != nil {
		return nil, err
	}
	params := layoutDecodeParams{first: uint64(format)<<48 | uint64(valueFormat)<<32 | uint64(numGlyphs), second: uint64(count)}
	node, alias, err := value.graph.begin(parent, edge, start, end, layoutSpanGPOSSingle, params, path)
	if err != nil {
		return nil, err
	}
	if alias {
		return layoutValueAs[*gposSingle](node, path)
	}
	if err := value.chargeLookupSubtable(path); err != nil {
		return nil, err
	}
	if err := value.admitGPOSValueRecords(uint64(count), path+"/values"); err != nil {
		return nil, err
	}
	coverageStart, err := value.childOffset(start, uint64(coverageOffset), path+"/coverage_offset")
	if err != nil {
		return nil, err
	}
	coverage, err := value.parseCoverage(node, layoutEdgeRef{role: layoutEdgeGPOSCoverage, rawOffset: uint32(coverageOffset)}, coverageStart, numGlyphs, path+"/coverage")
	if err != nil {
		return nil, err
	}
	if format == 2 && coverage.glyphCount != uint32(count) {
		return nil, fontError(codeLayoutInvalid, path+"/coverage_cardinality", errLayoutInvalid)
	}
	if err := contextFailure(value.ctx, path+"/values/allocate"); err != nil {
		return nil, err
	}
	values, err := layoutMakeSlice[gposValueRecord](uint64(count), path+"/values")
	if err != nil {
		return nil, err
	}
	for index := uint64(0); index < uint64(count); index++ {
		if err := value.checkContext(index, path+"/values"); err != nil {
			return nil, err
		}
		record, err := value.parseGPOSValueRecord(node, start+headerSize+recordSize*index, start, valueFormat, index, layoutPath(path+"/values", index))
		if err != nil {
			return nil, err
		}
		values[index] = record
	}
	result := &gposSingle{format: format, coverage: coverage, valueFormat: valueFormat, values: values}
	if err := value.graph.complete(node, result, path); err != nil {
		return nil, err
	}
	return result, nil
}
