package sfntglyf

import (
	"encoding/binary"
	"sort"
)

type TableReceipt struct {
	Tag         string `json:"tag"`
	Offset      uint32 `json:"offset"`
	Length      uint32 `json:"length"`
	Checksum    uint32 `json:"checksum"`
	ContentHash string `json:"content_hash"`
}

type Receipt struct {
	SchemaVersion       int            `json:"schema_version"`
	Profile             string         `json:"profile"`
	ContentHashProfile  string         `json:"content_hash_profile"`
	ContentHash         string         `json:"content_hash"`
	TableHashProfile    string         `json:"table_hash_profile"`
	CmapProfile         string         `json:"cmap_profile"`
	LayoutTableStatus   string         `json:"layout_table_status"`
	GlyfTableStatus     string         `json:"glyf_table_status"`
	MetricExtremaStatus string         `json:"metric_extrema_status"`
	EncodedBytes        uint64         `json:"encoded_bytes"`
	TableCount          uint32         `json:"table_count"`
	NumGlyphs           uint32         `json:"num_glyphs"`
	UnitsPerEm          uint16         `json:"units_per_em"`
	HheaAscender        int16          `json:"hhea_ascender"`
	NumberOfHMetrics    uint16         `json:"number_of_h_metrics"`
	IndexToLocFormat    int16          `json:"index_to_loc_format"`
	CmapRecordCount     uint32         `json:"cmap_record_count"`
	CmapFormat4Segments uint64         `json:"cmap_format4_segments"`
	CmapFormat12Groups  uint64         `json:"cmap_format12_groups"`
	CmapMappingSlots    uint64         `json:"cmap_mapping_slots"`
	CmapMappingCount    uint64         `json:"cmap_mapping_count"`
	OpaqueLayoutTables  []string       `json:"opaque_layout_tables"`
	Tables              []TableReceipt `json:"tables"`
	WorkUnits           uint64         `json:"work_units"`
	WorkingBytes        uint64         `json:"working_bytes"`
	RetainedBytes       uint64         `json:"retained_bytes"`
}

type HorizontalMetric struct {
	AdvanceWidth    uint16 `json:"advance_width"`
	LeftSideBearing int16  `json:"left_side_bearing"`
}

type tableRecord struct {
	tag      [4]byte
	checksum uint32
	offset   uint32
	length   uint32
}

type cmapMapping struct {
	codepoint uint32
	glyphID   uint32
}

type Font struct {
	encoded          []byte
	tables           []tableRecord
	mappings         []cmapMapping
	numGlyphs        uint32
	unitsPerEm       uint16
	hheaAscender     int16
	numberOfHMetrics uint16
	indexToLocFormat int16
	hmtxOffset       uint32
	locaOffset       uint32
	glyfOffset       uint32
	receipt          Receipt
}

func (value *Font) Receipt() Receipt {
	if value == nil {
		return Receipt{}
	}
	result := value.receipt
	result.OpaqueLayoutTables = append([]string(nil), value.receipt.OpaqueLayoutTables...)
	result.Tables = append([]TableReceipt(nil), value.receipt.Tables...)
	return result
}

func (value *Font) NumGlyphs() uint32 {
	if value == nil {
		return 0
	}
	return value.numGlyphs
}

func (value *Font) UnitsPerEm() uint16 {
	if value == nil {
		return 0
	}
	return value.unitsPerEm
}

func (value *Font) HheaAscender() int16 {
	if value == nil {
		return 0
	}
	return value.hheaAscender
}

func (value *Font) NominalGlyph(character rune) (uint32, bool) {
	if value == nil || character < 0 || character > 0x10ffff || character >= 0xd800 && character <= 0xdfff {
		return 0, false
	}
	codepoint := uint32(character)
	index := sort.Search(len(value.mappings), func(index int) bool {
		return value.mappings[index].codepoint >= codepoint
	})
	if index >= len(value.mappings) || value.mappings[index].codepoint != codepoint || value.mappings[index].glyphID == 0 {
		return 0, false
	}
	return value.mappings[index].glyphID, true
}

func (value *Font) HorizontalMetric(glyphID uint32) (HorizontalMetric, bool) {
	if value == nil || glyphID >= value.numGlyphs {
		return HorizontalMetric{}, false
	}
	metrics := uint32(value.numberOfHMetrics)
	if glyphID < metrics {
		offset := uint64(value.hmtxOffset) + uint64(glyphID)*4
		return HorizontalMetric{
			AdvanceWidth:    binary.BigEndian.Uint16(value.encoded[offset : offset+2]),
			LeftSideBearing: int16(binary.BigEndian.Uint16(value.encoded[offset+2 : offset+4])),
		}, true
	}
	last := uint64(value.hmtxOffset) + uint64(metrics-1)*4
	bearing := uint64(value.hmtxOffset) + uint64(metrics)*4 + uint64(glyphID-metrics)*2
	return HorizontalMetric{
		AdvanceWidth:    binary.BigEndian.Uint16(value.encoded[last : last+2]),
		LeftSideBearing: int16(binary.BigEndian.Uint16(value.encoded[bearing : bearing+2])),
	}, true
}

// GlyphRange returns the validated byte range relative to the glyf table. It intentionally does
// not expose the encoded bytes; body decoding belongs to a later profile.
func (value *Font) GlyphRange(glyphID uint32) (uint32, uint32, bool) {
	if value == nil || glyphID >= value.numGlyphs {
		return 0, 0, false
	}
	read := func(index uint32) uint32 {
		if value.indexToLocFormat == 0 {
			offset := uint64(value.locaOffset) + uint64(index)*2
			return uint32(binary.BigEndian.Uint16(value.encoded[offset:offset+2])) * 2
		}
		offset := uint64(value.locaOffset) + uint64(index)*4
		return binary.BigEndian.Uint32(value.encoded[offset : offset+4])
	}
	return read(glyphID), read(glyphID + 1), true
}
