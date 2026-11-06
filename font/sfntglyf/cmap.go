package sfntglyf

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

type cmapInfo struct {
	mappings        []cmapMapping
	recordCount     uint32
	format4Segments uint64
	format12Groups  uint64
	mappingSlots    uint64
	workUnits       uint64
	workingBytes    uint64
	retainedBytes   uint64
}

type cmapEncodingRecord struct {
	platformID uint16
	encodingID uint16
	subtable   int
}

type cmapSubtable struct {
	offset       uint32
	length       uint32
	format       uint16
	itemCount    uint64
	mappingSlots uint64
}

func validateCmap(ctx context.Context, encoded []byte, record tableRecord, numGlyphs uint32, limits Limits, baseWork, baseRetained uint64) (cmapInfo, error) {
	cmap := tableBytes(encoded, record)
	if len(cmap) < 4 || binary.BigEndian.Uint16(cmap[0:2]) != 0 {
		return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/header", ErrCmapInvalid)
	}
	recordCount := uint32(binary.BigEndian.Uint16(cmap[2:4]))
	if recordCount == 0 {
		return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/num_tables", ErrCmapInvalid)
	}
	if recordCount > limits.MaxCmapRecords {
		return cmapInfo{}, fontError(CodeResourceLimit, "cmap/num_tables", ErrResourceLimit)
	}
	recordBytes, ok := checkedMultiply(uint64(recordCount), 8)
	if !ok {
		return cmapInfo{}, fontError(CodeResourceLimit, "cmap/records", ErrResourceLimit)
	}
	recordEnd, ok := checkedAdd(4, recordBytes)
	if !ok || recordEnd > uint64(len(cmap)) {
		return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/records", ErrCmapInvalid)
	}

	encodingRecords := make([]cmapEncodingRecord, 0, int(recordCount))
	subtables := make([]cmapSubtable, 0, 4)
	var previousPlatform, previousEncoding uint16
	for index := uint32(0); index < recordCount; index++ {
		if err := contextFailure(ctx, fmt.Sprintf("cmap/records/%d", index)); err != nil {
			return cmapInfo{}, err
		}
		at := 4 + int(index)*8
		platformID := binary.BigEndian.Uint16(cmap[at : at+2])
		encodingID := binary.BigEndian.Uint16(cmap[at+2 : at+4])
		if index != 0 && (platformID < previousPlatform || platformID == previousPlatform && encodingID <= previousEncoding) {
			return cmapInfo{}, fontError(CodeCmapInvalid, fmt.Sprintf("cmap/records/%d/order", index), ErrCmapInvalid)
		}
		expectedFormat, allowed := expectedCmapFormat(platformID, encodingID)
		if !allowed {
			return cmapInfo{}, fontError(CodeProfileMismatch, fmt.Sprintf("cmap/records/%d/encoding", index), ErrProfileMismatch)
		}
		offset := binary.BigEndian.Uint32(cmap[at+4 : at+8])
		if uint64(offset) < recordEnd || uint64(offset)+2 > uint64(len(cmap)) {
			return cmapInfo{}, fontError(CodeCmapInvalid, fmt.Sprintf("cmap/records/%d/offset", index), ErrCmapInvalid)
		}
		format := binary.BigEndian.Uint16(cmap[offset : offset+2])
		if format != expectedFormat {
			return cmapInfo{}, fontError(CodeProfileMismatch, fmt.Sprintf("cmap/records/%d/format", index), ErrProfileMismatch)
		}
		subtableIndex := -1
		for candidate := range subtables {
			if subtables[candidate].offset == offset {
				subtableIndex = candidate
				break
			}
		}
		if subtableIndex < 0 {
			length, lengthErr := cmapSubtableLength(cmap, offset, format)
			if lengthErr != nil {
				return cmapInfo{}, fontError(CodeCmapInvalid, fmt.Sprintf("cmap/records/%d/length", index), lengthErr)
			}
			subtableIndex = len(subtables)
			subtables = append(subtables, cmapSubtable{offset: offset, length: length, format: format})
		}
		encodingRecords = append(encodingRecords, cmapEncodingRecord{platformID: platformID, encodingID: encodingID, subtable: subtableIndex})
		previousPlatform, previousEncoding = platformID, encodingID
	}

	physical := append([]cmapSubtable(nil), subtables...)
	sort.Slice(physical, func(left, right int) bool { return physical[left].offset < physical[right].offset })
	cursor := recordEnd
	for index := range physical {
		if uint64(physical[index].offset) < cursor {
			return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/subtable_overlap", ErrCmapInvalid)
		}
		if err := requireCmapZero(ctx, cmap[cursor:uint64(physical[index].offset)], "cmap/unreferenced_bytes"); err != nil {
			return cmapInfo{}, err
		}
		cursor = uint64(physical[index].offset) + uint64(physical[index].length)
	}
	if err := requireCmapZero(ctx, cmap[cursor:], "cmap/unreferenced_bytes"); err != nil {
		return cmapInfo{}, err
	}

	workUnits := baseWork
	recordWork, ok := checkedMultiply(uint64(recordCount), 32)
	if !ok {
		return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	workUnits, ok = checkedAdd(workUnits, recordWork)
	if !ok || workUnits > limits.MaxWorkUnits {
		return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	var format4Segments, format12Groups, mappingSlots uint64
	for index := range subtables {
		subtable := cmap[int(subtables[index].offset):int(subtables[index].offset+subtables[index].length)]
		var itemCount uint64
		if subtables[index].format == 4 {
			itemCount = uint64(binary.BigEndian.Uint16(subtable[6:8]) / 2)
			format4Segments, ok = checkedAdd(format4Segments, itemCount)
		} else {
			itemCount = uint64(binary.BigEndian.Uint32(subtable[12:16]))
			format12Groups, ok = checkedAdd(format12Groups, itemCount)
		}
		if !ok || format12Groups > limits.MaxCmapGroups {
			return cmapInfo{}, fontError(CodeResourceLimit, "cmap/items", ErrResourceLimit)
		}
		itemWork, multiplied := checkedMultiply(itemCount, 16)
		if !multiplied {
			return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
		}
		workUnits, ok = checkedAdd(workUnits, itemWork)
		if !ok || workUnits > limits.MaxWorkUnits {
			return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
		}
		var slots uint64
		var err error
		if subtables[index].format == 4 {
			slots, err = measureFormat4(ctx, subtable)
		} else {
			slots, err = measureFormat12(ctx, subtable, numGlyphs)
		}
		if err != nil {
			return cmapInfo{}, err
		}
		subtables[index].itemCount = itemCount
		subtables[index].mappingSlots = slots
		mappingSlots, ok = checkedAdd(mappingSlots, slots)
		if !ok || mappingSlots > limits.MaxCmapMappings {
			return cmapInfo{}, fontError(CodeResourceLimit, "cmap/mapping_slots", ErrResourceLimit)
		}
	}
	mappingWork, ok := checkedMultiply(mappingSlots, 2)
	if !ok {
		return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	workUnits, ok = checkedAdd(workUnits, mappingWork)
	if !ok || workUnits > limits.MaxWorkUnits {
		return cmapInfo{}, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	mappingWorking, ok := checkedMultiply(mappingSlots, 16)
	if !ok {
		return cmapInfo{}, fontError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	workingBytes, ok := checkedAdd(logicalFixedWorkingBytes, mappingWorking)
	if !ok || workingBytes > limits.MaxWorkingBytes {
		return cmapInfo{}, fontError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	decoded := make([][]cmapMapping, len(subtables))
	for index, info := range subtables {
		if err := contextFailure(ctx, fmt.Sprintf("cmap/subtables/%d/decode", index)); err != nil {
			return cmapInfo{}, err
		}
		subtable := cmap[int(info.offset):int(info.offset+info.length)]
		var mappings []cmapMapping
		var err error
		if info.format == 4 {
			mappings, err = decodeFormat4(ctx, subtable, numGlyphs, info.mappingSlots)
		} else {
			mappings, err = decodeFormat12(ctx, subtable, numGlyphs, info.mappingSlots)
		}
		if err != nil {
			return cmapInfo{}, err
		}
		decoded[index] = mappings
	}

	var format4, format12 []cmapMapping
	for _, record := range encodingRecords {
		candidate := decoded[record.subtable]
		if subtables[record.subtable].format == 4 {
			if format4 == nil {
				format4 = candidate
			} else if !equalMappings(format4, candidate) {
				return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/format4_semantics", ErrCmapInvalid)
			}
		} else {
			if format12 == nil {
				format12 = candidate
			} else if !equalMappings(format12, candidate) {
				return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/format12_semantics", ErrCmapInvalid)
			}
		}
	}
	canonical := format4
	if format12 != nil {
		for _, mapping := range format4 {
			glyphID, found := findMapping(format12, mapping.codepoint)
			if !found || glyphID != mapping.glyphID {
				return cmapInfo{}, fontError(CodeCmapInvalid, "cmap/format12_superset", ErrCmapInvalid)
			}
		}
		canonical = format12
	}
	retainedMappingBytes, ok := checkedMultiply(uint64(len(canonical)), logicalMappingBytes)
	if !ok {
		return cmapInfo{}, fontError(CodeResourceLimit, "retained_bytes", ErrResourceLimit)
	}
	retainedBytes, ok := checkedAdd(baseRetained, retainedMappingBytes)
	if !ok || retainedBytes > limits.MaxRetainedBytes {
		return cmapInfo{}, fontError(CodeResourceLimit, "retained_bytes", ErrResourceLimit)
	}
	ownedMappings, err := allocateMappings(uint64(len(canonical)), "cmap/canonical")
	if err != nil {
		return cmapInfo{}, err
	}
	ownedMappings = append(ownedMappings, canonical...)
	return cmapInfo{
		mappings: ownedMappings, recordCount: recordCount,
		format4Segments: format4Segments, format12Groups: format12Groups,
		mappingSlots: mappingSlots, workUnits: workUnits, workingBytes: workingBytes,
		retainedBytes: retainedBytes,
	}, nil
}

func expectedCmapFormat(platformID, encodingID uint16) (uint16, bool) {
	switch {
	case platformID == 0 && encodingID == 3:
		return 4, true
	case platformID == 0 && encodingID == 4:
		return 12, true
	case platformID == 3 && encodingID == 1:
		return 4, true
	case platformID == 3 && encodingID == 10:
		return 12, true
	default:
		return 0, false
	}
}

func cmapSubtableLength(cmap []byte, offset uint32, format uint16) (uint32, error) {
	start := uint64(offset)
	var length uint64
	if format == 4 {
		if start+4 > uint64(len(cmap)) {
			return 0, ErrCmapInvalid
		}
		length = uint64(binary.BigEndian.Uint16(cmap[offset+2 : offset+4]))
		if length < 16 || length&1 != 0 {
			return 0, ErrCmapInvalid
		}
	} else {
		if start+16 > uint64(len(cmap)) {
			return 0, ErrCmapInvalid
		}
		length = uint64(binary.BigEndian.Uint32(cmap[offset+4 : offset+8]))
		if length < 16 {
			return 0, ErrCmapInvalid
		}
	}
	end, ok := checkedAdd(start, length)
	if !ok || end > uint64(len(cmap)) || length > math.MaxUint32 {
		return 0, ErrCmapInvalid
	}
	return uint32(length), nil
}

func measureFormat4(ctx context.Context, table []byte) (uint64, error) {
	if len(table) < 24 || binary.BigEndian.Uint16(table[0:2]) != 4 ||
		binary.BigEndian.Uint16(table[2:4]) != uint16(len(table)) || binary.BigEndian.Uint16(table[4:6]) != 0 {
		return 0, fontError(CodeCmapInvalid, "cmap/format4/header", ErrCmapInvalid)
	}
	segCountX2 := binary.BigEndian.Uint16(table[6:8])
	if segCountX2 == 0 || segCountX2&1 != 0 {
		return 0, fontError(CodeCmapInvalid, "cmap/format4/seg_count", ErrCmapInvalid)
	}
	segCount := uint32(segCountX2 / 2)
	searchRange, entrySelector, rangeShift := cmap4SearchFields(segCount)
	if binary.BigEndian.Uint16(table[8:10]) != searchRange || binary.BigEndian.Uint16(table[10:12]) != entrySelector ||
		binary.BigEndian.Uint16(table[12:14]) != rangeShift {
		return 0, fontError(CodeCmapInvalid, "cmap/format4/search_fields", ErrCmapInvalid)
	}
	minimum := uint64(16) + uint64(segCount)*8
	if minimum > uint64(len(table)) || binary.BigEndian.Uint16(table[14+segCount*2:16+segCount*2]) != 0 {
		return 0, fontError(CodeCmapInvalid, "cmap/format4/arrays", ErrCmapInvalid)
	}
	endBase := uint32(14)
	startBase := uint32(16) + segCount*2
	var previousEnd uint16
	var slots uint64
	for index := uint32(0); index < segCount; index++ {
		if index%2048 == 0 {
			if err := contextFailure(ctx, "cmap/format4/segments"); err != nil {
				return 0, err
			}
		}
		end := binary.BigEndian.Uint16(table[endBase+index*2 : endBase+index*2+2])
		start := binary.BigEndian.Uint16(table[startBase+index*2 : startBase+index*2+2])
		if start > end || index != 0 && start <= previousEnd || index != 0 && end <= previousEnd {
			return 0, fontError(CodeCmapInvalid, "cmap/format4/segment_order", ErrCmapInvalid)
		}
		count := uint64(end) - uint64(start) + 1
		var ok bool
		slots, ok = checkedAdd(slots, count)
		if !ok {
			return 0, fontError(CodeResourceLimit, "cmap/format4/mapping_slots", ErrResourceLimit)
		}
		previousEnd = end
	}
	last := segCount - 1
	if binary.BigEndian.Uint16(table[endBase+last*2:endBase+last*2+2]) != 0xffff ||
		binary.BigEndian.Uint16(table[startBase+last*2:startBase+last*2+2]) != 0xffff {
		return 0, fontError(CodeCmapInvalid, "cmap/format4/sentinel", ErrCmapInvalid)
	}
	return slots, nil
}

func decodeFormat4(ctx context.Context, table []byte, numGlyphs uint32, slots uint64) ([]cmapMapping, error) {
	result, err := allocateMappings(slots, "cmap/format4/mappings")
	if err != nil {
		return nil, err
	}
	segCount := uint32(binary.BigEndian.Uint16(table[6:8]) / 2)
	endBase := uint32(14)
	startBase := uint32(16) + segCount*2
	deltaBase := uint32(16) + segCount*4
	rangeBase := uint32(16) + segCount*6
	glyphBase := uint32(16) + segCount*8
	var visited uint64
	for index := uint32(0); index < segCount; index++ {
		end := binary.BigEndian.Uint16(table[endBase+index*2 : endBase+index*2+2])
		start := binary.BigEndian.Uint16(table[startBase+index*2 : startBase+index*2+2])
		delta := binary.BigEndian.Uint16(table[deltaBase+index*2 : deltaBase+index*2+2])
		rangeOffset := binary.BigEndian.Uint16(table[rangeBase+index*2 : rangeBase+index*2+2])
		if rangeOffset&1 != 0 {
			return nil, fontError(CodeCmapInvalid, "cmap/format4/id_range_offset", ErrCmapInvalid)
		}
		for codepoint := uint32(start); codepoint <= uint32(end); codepoint++ {
			if visited%4096 == 0 {
				if err := contextFailure(ctx, "cmap/format4/mappings"); err != nil {
					return nil, err
				}
			}
			visited++
			var glyphID uint16
			if rangeOffset == 0 {
				glyphID = uint16(codepoint + uint32(delta))
			} else {
				wordAt := uint64(rangeBase+index*2) + uint64(rangeOffset) + uint64(codepoint-uint32(start))*2
				if wordAt < uint64(glyphBase) || wordAt+2 > uint64(len(table)) {
					return nil, fontError(CodeCmapInvalid, "cmap/format4/id_range_offset", ErrCmapInvalid)
				}
				raw := binary.BigEndian.Uint16(table[wordAt : wordAt+2])
				if raw != 0 {
					glyphID = uint16(uint32(raw) + uint32(delta))
				}
			}
			if codepoint == 0xffff && glyphID != 0 {
				return nil, fontError(CodeCmapInvalid, "cmap/format4/sentinel_gid", ErrCmapInvalid)
			}
			if codepoint >= 0xd800 && codepoint <= 0xdfff && glyphID != 0 {
				return nil, fontError(CodeCmapInvalid, "cmap/format4/surrogate", ErrCmapInvalid)
			}
			if glyphID != 0 {
				if uint32(glyphID) >= numGlyphs {
					return nil, fontError(CodeCmapInvalid, "cmap/format4/glyph_id", ErrCmapInvalid)
				}
				result = append(result, cmapMapping{codepoint: codepoint, glyphID: uint32(glyphID)})
			}
			if codepoint == math.MaxUint16 {
				break
			}
		}
	}
	return result, nil
}

func measureFormat12(ctx context.Context, table []byte, numGlyphs uint32) (uint64, error) {
	if len(table) < 16 || binary.BigEndian.Uint16(table[0:2]) != 12 || binary.BigEndian.Uint16(table[2:4]) != 0 ||
		binary.BigEndian.Uint32(table[4:8]) != uint32(len(table)) || binary.BigEndian.Uint32(table[8:12]) != 0 {
		return 0, fontError(CodeCmapInvalid, "cmap/format12/header", ErrCmapInvalid)
	}
	groupCount := uint64(binary.BigEndian.Uint32(table[12:16]))
	expected, ok := checkedMultiply(groupCount, 12)
	if !ok {
		return 0, fontError(CodeResourceLimit, "cmap/format12/length", ErrResourceLimit)
	}
	expected, ok = checkedAdd(16, expected)
	if !ok || expected != uint64(len(table)) {
		return 0, fontError(CodeCmapInvalid, "cmap/format12/length", ErrCmapInvalid)
	}
	var previousEnd uint32
	var slots uint64
	for index := uint64(0); index < groupCount; index++ {
		if index%2048 == 0 {
			if err := contextFailure(ctx, "cmap/format12/groups"); err != nil {
				return 0, err
			}
		}
		at := 16 + index*12
		start := binary.BigEndian.Uint32(table[at : at+4])
		end := binary.BigEndian.Uint32(table[at+4 : at+8])
		startGlyphID := binary.BigEndian.Uint32(table[at+8 : at+12])
		if start > end || end > 0x10ffff || index != 0 && start <= previousEnd || start <= 0xdfff && end >= 0xd800 {
			return 0, fontError(CodeCmapInvalid, "cmap/format12/group", ErrCmapInvalid)
		}
		delta := uint64(end) - uint64(start)
		lastGlyphID, ok := checkedAdd(uint64(startGlyphID), delta)
		if !ok || lastGlyphID > math.MaxUint32 || lastGlyphID != 0 && lastGlyphID >= uint64(numGlyphs) {
			return 0, fontError(CodeCmapInvalid, "cmap/format12/glyph_id", ErrCmapInvalid)
		}
		count := delta + 1
		slots, ok = checkedAdd(slots, count)
		if !ok {
			return 0, fontError(CodeResourceLimit, "cmap/format12/mapping_slots", ErrResourceLimit)
		}
		previousEnd = end
	}
	return slots, nil
}

func decodeFormat12(ctx context.Context, table []byte, numGlyphs uint32, slots uint64) ([]cmapMapping, error) {
	result, err := allocateMappings(slots, "cmap/format12/mappings")
	if err != nil {
		return nil, err
	}
	groupCount := uint64(binary.BigEndian.Uint32(table[12:16]))
	var visited uint64
	for index := uint64(0); index < groupCount; index++ {
		at := 16 + index*12
		start := binary.BigEndian.Uint32(table[at : at+4])
		end := binary.BigEndian.Uint32(table[at+4 : at+8])
		startGlyphID := binary.BigEndian.Uint32(table[at+8 : at+12])
		for codepoint := start; codepoint <= end; codepoint++ {
			if visited%4096 == 0 {
				if err := contextFailure(ctx, "cmap/format12/mappings"); err != nil {
					return nil, err
				}
			}
			visited++
			glyphID := startGlyphID + (codepoint - start)
			if glyphID != 0 {
				if glyphID >= numGlyphs {
					return nil, fontError(CodeInvariantFailed, "cmap/format12/glyph_id", ErrInvariantFailed)
				}
				result = append(result, cmapMapping{codepoint: codepoint, glyphID: glyphID})
			}
			if codepoint == math.MaxUint32 {
				break
			}
		}
	}
	return result, nil
}

func cmap4SearchFields(segCount uint32) (uint16, uint16, uint16) {
	power, selector := uint32(1), uint16(0)
	for power<<1 <= segCount {
		power <<= 1
		selector++
	}
	searchRange := power * 2
	return uint16(searchRange), selector, uint16(segCount*2 - searchRange)
}

func equalMappings(left, right []cmapMapping) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func findMapping(mappings []cmapMapping, codepoint uint32) (uint32, bool) {
	index := sort.Search(len(mappings), func(index int) bool { return mappings[index].codepoint >= codepoint })
	if index == len(mappings) || mappings[index].codepoint != codepoint {
		return 0, false
	}
	return mappings[index].glyphID, true
}

func requireCmapZero(ctx context.Context, value []byte, path string) error {
	for index, character := range value {
		if index%contextStrideBytes == 0 {
			if err := contextFailure(ctx, path); err != nil {
				return err
			}
		}
		if character != 0 {
			return fontError(CodeCmapInvalid, path, ErrCmapInvalid)
		}
	}
	return contextFailure(ctx, path)
}
