package sfntglyf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
)

const (
	sfntVersionTrueType uint32 = 0x00010000
	sfntChecksumMagic   uint32 = 0xb1b0afba
)

var requiredTags = [7]string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp"}

type containerInfo struct {
	records []tableRecord
	head    tableRecord
}

func validateContainer(ctx context.Context, encoded []byte, limits Limits) (containerInfo, error) {
	if uint64(len(encoded)) > limits.MaxEncodedBytes {
		return containerInfo{}, fontError(CodeResourceLimit, "encoded_bytes", ErrResourceLimit)
	}
	if len(encoded) < 12 {
		return containerInfo{}, fontError(CodeContainerInvalid, "offset_table", ErrContainerInvalid)
	}
	if binary.BigEndian.Uint32(encoded[:4]) != sfntVersionTrueType {
		return containerInfo{}, fontError(CodeProfileMismatch, "sfnt_version", ErrProfileMismatch)
	}
	if len(encoded)&3 != 0 {
		return containerInfo{}, fontError(CodeContainerInvalid, "file_alignment", ErrContainerInvalid)
	}
	numTables := uint32(binary.BigEndian.Uint16(encoded[4:6]))
	if numTables == 0 {
		return containerInfo{}, fontError(CodeContainerInvalid, "num_tables", ErrContainerInvalid)
	}
	if numTables > limits.MaxTables {
		return containerInfo{}, fontError(CodeResourceLimit, "num_tables", ErrResourceLimit)
	}
	directoryBytes, ok := checkedMultiply(uint64(numTables), 16)
	if !ok {
		return containerInfo{}, fontError(CodeResourceLimit, "table_directory", ErrResourceLimit)
	}
	directoryEnd, ok := checkedAdd(12, directoryBytes)
	if !ok || directoryEnd > uint64(len(encoded)) || directoryEnd > uint64(maxInt()) {
		return containerInfo{}, fontError(CodeContainerInvalid, "table_directory", ErrContainerInvalid)
	}
	searchRange, entrySelector, rangeShift := directorySearchFields(numTables)
	if binary.BigEndian.Uint16(encoded[6:8]) != searchRange ||
		binary.BigEndian.Uint16(encoded[8:10]) != entrySelector ||
		binary.BigEndian.Uint16(encoded[10:12]) != rangeShift {
		return containerInfo{}, fontError(CodeContainerInvalid, "offset_table/search_fields", ErrContainerInvalid)
	}

	records := make([]tableRecord, 0, int(numTables))
	var previous [4]byte
	for index := uint32(0); index < numTables; index++ {
		if index%256 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("tables/%d", index)); err != nil {
				return containerInfo{}, err
			}
		}
		offset := 12 + int(index)*16
		var tag [4]byte
		copy(tag[:], encoded[offset:offset+4])
		if !validTag(tag) {
			return containerInfo{}, fontError(CodeContainerInvalid, fmt.Sprintf("tables/%d/tag", index), ErrContainerInvalid)
		}
		if index != 0 && bytes.Compare(previous[:], tag[:]) >= 0 {
			return containerInfo{}, fontError(CodeContainerInvalid, fmt.Sprintf("tables/%d/order", index), ErrContainerInvalid)
		}
		if !allowedTag(string(tag[:])) {
			return containerInfo{}, fontError(CodeProfileMismatch, fmt.Sprintf("tables/%d/%s", index, string(tag[:])), ErrProfileMismatch)
		}
		record := tableRecord{
			tag:      tag,
			checksum: binary.BigEndian.Uint32(encoded[offset+4 : offset+8]),
			offset:   binary.BigEndian.Uint32(encoded[offset+8 : offset+12]),
			length:   binary.BigEndian.Uint32(encoded[offset+12 : offset+16]),
		}
		if record.offset&3 != 0 || uint64(record.offset) < directoryEnd {
			return containerInfo{}, fontError(CodeContainerInvalid, fmt.Sprintf("tables/%d/range", index), ErrContainerInvalid)
		}
		end, ok := checkedAdd(uint64(record.offset), uint64(record.length))
		paddedEnd, aligned := align4(end)
		if !ok || !aligned || end > uint64(len(encoded)) || paddedEnd > uint64(len(encoded)) {
			return containerInfo{}, fontError(CodeContainerInvalid, fmt.Sprintf("tables/%d/range", index), ErrContainerInvalid)
		}
		records = append(records, record)
		previous = tag
	}
	for _, tag := range requiredTags {
		if _, ok := findRecord(records, tag); !ok {
			return containerInfo{}, fontError(CodeProfileMismatch, "required_table/"+tag, ErrProfileMismatch)
		}
	}

	physical := append([]tableRecord(nil), records...)
	sort.Slice(physical, func(left, right int) bool {
		if physical[left].offset != physical[right].offset {
			return physical[left].offset < physical[right].offset
		}
		return physical[left].length < physical[right].length
	})
	expectedStart, ok := align4(directoryEnd)
	if !ok {
		return containerInfo{}, fontError(CodeContainerInvalid, "table_layout", ErrContainerInvalid)
	}
	if err := requireZero(ctx, encoded[directoryEnd:expectedStart], "table_directory/padding"); err != nil {
		return containerInfo{}, err
	}
	for index, record := range physical {
		if uint64(record.offset) != expectedStart {
			return containerInfo{}, fontError(CodeContainerInvalid, fmt.Sprintf("physical_tables/%d/gap_or_overlap", index), ErrContainerInvalid)
		}
		if record.length == 0 {
			continue
		}
		end := uint64(record.offset) + uint64(record.length)
		paddedEnd, _ := align4(end)
		if err := requireZero(ctx, encoded[end:paddedEnd], fmt.Sprintf("physical_tables/%d/padding", index)); err != nil {
			return containerInfo{}, err
		}
		expectedStart = paddedEnd
	}
	if expectedStart != uint64(len(encoded)) {
		return containerInfo{}, fontError(CodeContainerInvalid, "trailing_bytes", ErrContainerInvalid)
	}

	head, _ := findRecord(records, "head")
	if head.length < 12 {
		return containerInfo{}, fontError(CodeTableInvalid, "head/length", ErrTableInvalid)
	}
	for index, record := range records {
		start := int(record.offset)
		end := start + int(record.length)
		zeroStart, zeroLength := -1, 0
		if string(record.tag[:]) == "head" {
			zeroStart, zeroLength = 8, 4
		}
		actual, err := sfntChecksum(ctx, encoded[start:end], zeroStart, zeroLength, fmt.Sprintf("tables/%d/checksum", index))
		if err != nil {
			return containerInfo{}, err
		}
		if actual != record.checksum {
			return containerInfo{}, fontError(CodeChecksumInvalid, fmt.Sprintf("tables/%d/%s", index, string(record.tag[:])), ErrChecksumInvalid)
		}
	}
	headAdjustmentAt := int(head.offset) + 8
	whole, err := sfntChecksum(ctx, encoded, -1, 0, "font_checksum")
	if err != nil {
		return containerInfo{}, err
	}
	zeroed, err := sfntChecksum(ctx, encoded, headAdjustmentAt, 4, "font_checksum/adjustment")
	if err != nil {
		return containerInfo{}, err
	}
	adjustment := binary.BigEndian.Uint32(encoded[headAdjustmentAt : headAdjustmentAt+4])
	if whole != sfntChecksumMagic || adjustment != sfntChecksumMagic-zeroed {
		return containerInfo{}, fontError(CodeChecksumInvalid, "head/check_sum_adjustment", ErrChecksumInvalid)
	}
	return containerInfo{records: records, head: head}, nil
}

func directorySearchFields(numTables uint32) (uint16, uint16, uint16) {
	power, selector := uint32(1), uint16(0)
	for power<<1 <= numTables {
		power <<= 1
		selector++
	}
	searchRange := power * 16
	return uint16(searchRange), selector, uint16(numTables*16 - searchRange)
}

func validTag(tag [4]byte) bool {
	seenSpace := false
	seenNonSpace := false
	for _, character := range tag {
		if character < 0x20 || character > 0x7e {
			return false
		}
		if character == ' ' {
			seenSpace = true
		} else if seenSpace {
			return false
		} else {
			seenNonSpace = true
		}
	}
	return seenNonSpace
}

func allowedTag(tag string) bool {
	switch tag {
	case "cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp",
		"GDEF", "GPOS", "GSUB", "DSIG", "OS/2", "cvt ", "fpgm", "gasp", "name", "post", "prep":
		return true
	default:
		return false
	}
}

func findRecord(records []tableRecord, tag string) (tableRecord, bool) {
	index := sort.Search(len(records), func(index int) bool { return string(records[index].tag[:]) >= tag })
	if index == len(records) || string(records[index].tag[:]) != tag {
		return tableRecord{}, false
	}
	return records[index], true
}

func requireZero(ctx context.Context, value []byte, path string) error {
	for index, character := range value {
		if index%contextStrideBytes == 0 {
			if err := contextFailure(ctx, path); err != nil {
				return err
			}
		}
		if character != 0 {
			return fontError(CodeContainerInvalid, path, ErrContainerInvalid)
		}
	}
	return contextFailure(ctx, path)
}
