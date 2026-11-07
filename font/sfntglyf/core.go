package sfntglyf

import (
	"context"
	"encoding/binary"
	"fmt"
)

const headMagicNumber uint32 = 0x5f0f3cf5

type coreInfo struct {
	numGlyphs        uint32
	unitsPerEm       uint16
	hheaAscender     int16
	numberOfHMetrics uint16
	indexToLocFormat int16
	hmtxOffset       uint32
	locaOffset       uint32
	glyfOffset       uint32
	workUnits        uint64
}

func validateCore(ctx context.Context, encoded []byte, records []tableRecord, limits Limits, baseWork uint64) (coreInfo, error) {
	headRecord, _ := findRecord(records, "head")
	maxpRecord, _ := findRecord(records, "maxp")
	hheaRecord, _ := findRecord(records, "hhea")
	hmtxRecord, _ := findRecord(records, "hmtx")
	locaRecord, _ := findRecord(records, "loca")
	glyfRecord, _ := findRecord(records, "glyf")

	head := tableBytes(encoded, headRecord)
	if len(head) != 54 || binary.BigEndian.Uint32(head[0:4]) != 0x00010000 ||
		binary.BigEndian.Uint32(head[12:16]) != headMagicNumber {
		return coreInfo{}, fontError(CodeTableInvalid, "head/header", ErrTableInvalid)
	}
	unitsPerEm := binary.BigEndian.Uint16(head[18:20])
	if unitsPerEm < 16 || unitsPerEm > 16384 || binary.BigEndian.Uint16(head[16:18])&0x87e0 != 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "head/flags_or_units_per_em", ErrTableInvalid)
	}
	xMin := int16(binary.BigEndian.Uint16(head[36:38]))
	yMin := int16(binary.BigEndian.Uint16(head[38:40]))
	xMax := int16(binary.BigEndian.Uint16(head[40:42]))
	yMax := int16(binary.BigEndian.Uint16(head[42:44]))
	if xMin > xMax || yMin > yMax || binary.BigEndian.Uint16(head[44:46])&0xff80 != 0 ||
		binary.BigEndian.Uint16(head[46:48]) == 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "head/bounds_or_reserved", ErrTableInvalid)
	}
	fontDirectionHint := int16(binary.BigEndian.Uint16(head[48:50]))
	indexToLocFormat := int16(binary.BigEndian.Uint16(head[50:52]))
	glyphDataFormat := int16(binary.BigEndian.Uint16(head[52:54]))
	if fontDirectionHint < -2 || fontDirectionHint > 2 ||
		(indexToLocFormat != 0 && indexToLocFormat != 1) || glyphDataFormat != 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "head/format", ErrTableInvalid)
	}

	maxp := tableBytes(encoded, maxpRecord)
	if len(maxp) != 32 || binary.BigEndian.Uint32(maxp[0:4]) != 0x00010000 {
		return coreInfo{}, fontError(CodeTableInvalid, "maxp/header", ErrTableInvalid)
	}
	numGlyphs := uint32(binary.BigEndian.Uint16(maxp[4:6]))
	if numGlyphs == 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "maxp/num_glyphs", ErrTableInvalid)
	}
	if numGlyphs > limits.MaxGlyphs {
		return coreInfo{}, fontError(CodeResourceLimit, "maxp/num_glyphs", ErrResourceLimit)
	}
	glyphWork, ok := checkedMultiply(uint64(numGlyphs), 4)
	if !ok {
		return coreInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	workUnits, ok := checkedAdd(baseWork, glyphWork)
	if !ok || workUnits > limits.MaxWorkUnits {
		return coreInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	maxZones := binary.BigEndian.Uint16(maxp[14:16])
	if maxZones != 1 && maxZones != 2 {
		return coreInfo{}, fontError(CodeTableInvalid, "maxp/max_zones", ErrTableInvalid)
	}

	hhea := tableBytes(encoded, hheaRecord)
	if len(hhea) != 36 || binary.BigEndian.Uint32(hhea[0:4]) != 0x00010000 {
		return coreInfo{}, fontError(CodeTableInvalid, "hhea/header", ErrTableInvalid)
	}
	for offset := 24; offset < 32; offset += 2 {
		if binary.BigEndian.Uint16(hhea[offset:offset+2]) != 0 {
			return coreInfo{}, fontError(CodeTableInvalid, "hhea/reserved", ErrTableInvalid)
		}
	}
	if int16(binary.BigEndian.Uint16(hhea[18:20])) == 0 && int16(binary.BigEndian.Uint16(hhea[20:22])) == 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "hhea/caret_slope", ErrTableInvalid)
	}
	if int16(binary.BigEndian.Uint16(hhea[32:34])) != 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "hhea/metric_data_format", ErrTableInvalid)
	}
	numberOfHMetrics := binary.BigEndian.Uint16(hhea[34:36])
	if numberOfHMetrics == 0 || uint32(numberOfHMetrics) > numGlyphs {
		return coreInfo{}, fontError(CodeTableInvalid, "hhea/number_of_h_metrics", ErrTableInvalid)
	}
	hmtxLength, ok := checkedAdd(uint64(numberOfHMetrics)*4, uint64(numGlyphs-uint32(numberOfHMetrics))*2)
	if !ok || hmtxLength != uint64(hmtxRecord.length) {
		return coreInfo{}, fontError(CodeTableInvalid, "hmtx/length", ErrTableInvalid)
	}

	locaEntryBytes := uint64(2)
	if indexToLocFormat == 1 {
		locaEntryBytes = 4
	}
	locaEntries, ok := checkedAdd(uint64(numGlyphs), 1)
	if !ok {
		return coreInfo{}, fontError(CodeResourceLimit, "loca/length", ErrResourceLimit)
	}
	locaLength, ok := checkedMultiply(locaEntries, locaEntryBytes)
	if !ok || locaLength != uint64(locaRecord.length) {
		return coreInfo{}, fontError(CodeTableInvalid, "loca/length", ErrTableInvalid)
	}
	loca := tableBytes(encoded, locaRecord)
	glyf := tableBytes(encoded, glyfRecord)
	previous := readLoca(loca, indexToLocFormat, 0)
	if previous != 0 {
		return coreInfo{}, fontError(CodeTableInvalid, "loca/first", ErrTableInvalid)
	}
	for glyphID := uint32(0); glyphID < numGlyphs; glyphID++ {
		if glyphID%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("loca/%d", glyphID)); err != nil {
				return coreInfo{}, err
			}
		}
		next := readLoca(loca, indexToLocFormat, glyphID+1)
		if next < previous || uint64(next) > uint64(len(glyf)) {
			return coreInfo{}, fontError(CodeTableInvalid, fmt.Sprintf("loca/%d", glyphID), ErrTableInvalid)
		}
		previous = next
	}
	if previous != uint32(len(glyf)) {
		return coreInfo{}, fontError(CodeTableInvalid, "loca/final", ErrTableInvalid)
	}

	if err := validateAdvanceWidthMax(ctx, hhea, tableBytes(encoded, hmtxRecord), numGlyphs, numberOfHMetrics); err != nil {
		return coreInfo{}, err
	}
	return coreInfo{
		numGlyphs: numGlyphs, unitsPerEm: unitsPerEm,
		hheaAscender: int16(binary.BigEndian.Uint16(hhea[4:6])), numberOfHMetrics: numberOfHMetrics,
		indexToLocFormat: indexToLocFormat, hmtxOffset: hmtxRecord.offset,
		locaOffset: locaRecord.offset, glyfOffset: glyfRecord.offset, workUnits: workUnits,
	}, nil
}

func validateAdvanceWidthMax(ctx context.Context, hhea, hmtx []byte, numGlyphs uint32, numberOfHMetrics uint16) error {
	var advanceWidthMax uint16
	for glyphID := uint32(0); glyphID < numGlyphs; glyphID++ {
		if glyphID%2048 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("hmtx/%d", glyphID)); err != nil {
				return err
			}
		}
		advance, _ := metricAt(hmtx, glyphID, numberOfHMetrics)
		if advance > advanceWidthMax {
			advanceWidthMax = advance
		}
	}
	if binary.BigEndian.Uint16(hhea[10:12]) != advanceWidthMax {
		return fontError(CodeTableInvalid, "hhea/advance_width_max", ErrTableInvalid)
	}
	return nil
}

func metricAt(hmtx []byte, glyphID uint32, numberOfHMetrics uint16) (uint16, int16) {
	metricCount := uint32(numberOfHMetrics)
	if glyphID < metricCount {
		offset := glyphID * 4
		return binary.BigEndian.Uint16(hmtx[offset : offset+2]), int16(binary.BigEndian.Uint16(hmtx[offset+2 : offset+4]))
	}
	last := (metricCount - 1) * 4
	bearing := metricCount*4 + (glyphID-metricCount)*2
	return binary.BigEndian.Uint16(hmtx[last : last+2]), int16(binary.BigEndian.Uint16(hmtx[bearing : bearing+2]))
}

func readLoca(loca []byte, format int16, index uint32) uint32 {
	if format == 0 {
		offset := index * 2
		return uint32(binary.BigEndian.Uint16(loca[offset:offset+2])) * 2
	}
	offset := index * 4
	return binary.BigEndian.Uint32(loca[offset : offset+4])
}

func tableBytes(encoded []byte, record tableRecord) []byte {
	start := int(record.offset)
	return encoded[start : start+int(record.length)]
}
