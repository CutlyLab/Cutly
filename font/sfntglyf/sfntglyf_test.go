package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"
)

type testCmapRecord struct {
	platformID uint16
	encodingID uint16
	subtable   int
}

type testFormat12Group struct {
	start        uint32
	end          uint32
	startGlyphID uint32
}

func TestParseMinimalStaticTrueTypeCore(t *testing.T) {
	tables := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)},
		[]testCmapRecord{{platformID: 3, encodingID: 1, subtable: 0}},
	))
	encoded := buildTestSFNT(t, tables)
	font, receipt, err := Parse(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if font == nil || receipt.SchemaVersion != ReceiptSchemaVersion || receipt.Profile != ReceiptProfileV1 {
		t.Fatalf("Parse() = %#v, %#v", font, receipt)
	}
	if receipt.TableCount != 7 || receipt.NumGlyphs != 2 || receipt.UnitsPerEm != 1000 ||
		receipt.CmapRecordCount != 1 || receipt.CmapFormat4Segments != 2 ||
		receipt.CmapFormat12Groups != 0 || receipt.CmapMappingSlots != 2 || receipt.CmapMappingCount != 1 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if receipt.GlyfTableStatus != GlyfTableStatusV1 || receipt.MetricExtremaStatus != DeclaredStatusV1 ||
		receipt.LayoutTableStatus != LayoutTableStatusV1 {
		t.Fatalf("receipt statuses = %#v", receipt)
	}
	if glyphID, ok := font.NominalGlyph('A'); !ok || glyphID != 1 {
		t.Fatalf("NominalGlyph(A) = %d, %t", glyphID, ok)
	}
	if _, ok := font.NominalGlyph('B'); ok {
		t.Fatal("NominalGlyph(B) unexpectedly found")
	}
	if _, ok := font.NominalGlyph(rune(0xd800)); ok {
		t.Fatal("NominalGlyph(surrogate) unexpectedly found")
	}
	metric, ok := font.HorizontalMetric(1)
	if !ok || metric.AdvanceWidth != 500 || metric.LeftSideBearing != 0 {
		t.Fatalf("HorizontalMetric(1) = %#v, %t", metric, ok)
	}
	if _, ok := font.HorizontalMetric(2); ok {
		t.Fatal("HorizontalMetric(out of range) unexpectedly found")
	}
	start, end, ok := font.GlyphRange(1)
	if !ok || start != 0 || end != 0 {
		t.Fatalf("GlyphRange(1) = %d, %d, %t", start, end, ok)
	}
	if receipt.WorkUnits != uint64(len(encoded))*8+7*64+2*4+1*32+2*16+2*2 {
		t.Fatalf("work units = %d", receipt.WorkUnits)
	}
	if receipt.WorkingBytes != logicalFixedWorkingBytes+2*16 {
		t.Fatalf("working bytes = %d", receipt.WorkingBytes)
	}
	if receipt.RetainedBytes != logicalFixedRetainedBytes+uint64(len(encoded))+7*logicalTableBytes+logicalMappingBytes {
		t.Fatalf("retained bytes = %d", receipt.RetainedBytes)
	}
	glyfOffset := testTableOffset(encoded, "glyf")
	headOffset := testTableOffset(encoded, "head")
	if glyfOffset != headOffset {
		t.Fatalf("zero glyf offset = %d; head offset = %d", glyfOffset, headOffset)
	}
	headDirectoryChecksum := testTableDirectoryChecksum(encoded, "head")
	headLength := testTableLength(encoded, "head")
	headBytes := encoded[headOffset : headOffset+headLength]
	zeroedHead := append([]byte(nil), headBytes...)
	clear(zeroedHead[8:12])
	if headDirectoryChecksum != testChecksum(zeroedHead) || headDirectoryChecksum == testChecksum(headBytes) {
		t.Fatalf("head checksum normalization = %#x, actual=%#x, zeroed=%#x", headDirectoryChecksum, testChecksum(headBytes), testChecksum(zeroedHead))
	}

	stable := font.Receipt()
	for index := range encoded {
		encoded[index] ^= 0xff
	}
	if glyphID, ok := font.NominalGlyph('A'); !ok || glyphID != 1 {
		t.Fatalf("caller mutation changed Font: %d, %t", glyphID, ok)
	}
	if got := font.Receipt(); got.ContentHash != stable.ContentHash {
		t.Fatalf("caller mutation changed receipt hash: %q != %q", got.ContentHash, stable.ContentHash)
	}
	stable.Tables[0].Tag = "FAIL"
	stable.OpaqueLayoutTables = append(stable.OpaqueLayoutTables, "FAIL")
	if got := font.Receipt(); got.Tables[0].Tag == "FAIL" || len(got.OpaqueLayoutTables) != 0 {
		t.Fatalf("receipt getters alias internal state: %#v", got)
	}
}

func TestParseAcceptsOpaqueOddLongLocaRange(t *testing.T) {
	tables := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)},
		[]testCmapRecord{{platformID: 3, encodingID: 1, subtable: 0}},
	))
	binary.BigEndian.PutUint16(tables["head"][50:52], 1)
	tables["glyf"] = []byte{0xa5}
	tables["loca"] = make([]byte, 12)
	binary.BigEndian.PutUint32(tables["loca"][4:8], 1)
	binary.BigEndian.PutUint32(tables["loca"][8:12], 1)
	font, receipt, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
	if err != nil {
		t.Fatalf("Parse() odd long loca error = %v", err)
	}
	start, end, ok := font.GlyphRange(0)
	if !ok || start != 0 || end != 1 || receipt.GlyfTableStatus != GlyfTableStatusV1 {
		t.Fatalf("odd opaque range = %d, %d, %t; receipt=%#v", start, end, ok, receipt)
	}
}

func TestParseOpaqueLayoutReceipt(t *testing.T) {
	tables := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)},
		[]testCmapRecord{{platformID: 3, encodingID: 1, subtable: 0}},
	))
	tables["GDEF"] = []byte{0xde, 0xad, 0xbe, 0xef}
	tables["GPOS"] = []byte{1, 2, 3, 4}
	tables["GSUB"] = []byte{5, 6, 7, 8}
	font, receipt, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
	if err != nil || font == nil {
		t.Fatalf("Parse() = %v, %v", font, err)
	}
	want := []string{"GDEF", "GPOS", "GSUB"}
	if len(receipt.OpaqueLayoutTables) != len(want) {
		t.Fatalf("opaque tables = %#v", receipt.OpaqueLayoutTables)
	}
	for index := range want {
		if receipt.OpaqueLayoutTables[index] != want[index] {
			t.Fatalf("opaque tables = %#v", receipt.OpaqueLayoutTables)
		}
	}
}

func TestParseContainerHostileMutations(t *testing.T) {
	baseTables := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)},
		[]testCmapRecord{{platformID: 3, encodingID: 1, subtable: 0}},
	))
	valid := buildTestSFNT(t, baseTables)
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
		is     error
	}{
		{name: "collection", is: ErrProfileMismatch, mutate: func(value []byte) []byte {
			copy(value[:4], []byte("ttcf"))
			return value
		}},
		{name: "otto", is: ErrProfileMismatch, mutate: func(value []byte) []byte {
			copy(value[:4], []byte("OTTO"))
			return value
		}},
		{name: "woff", is: ErrProfileMismatch, mutate: func(value []byte) []byte {
			copy(value[:4], []byte("wOFF"))
			return value
		}},
		{name: "search range", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			value[7] ^= 1
			return value
		}},
		{name: "directory order", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			first := append([]byte(nil), value[12:28]...)
			copy(value[12:28], value[28:44])
			copy(value[28:44], first)
			return value
		}},
		{name: "duplicate tag", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			copy(value[28:32], value[12:16])
			return value
		}},
		{name: "internal tag space", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			copy(value[12:16], []byte("c ap"))
			return value
		}},
		{name: "all space tag", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			copy(value[12:16], []byte("    "))
			return value
		}},
		{name: "misaligned offset", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			offset := binary.BigEndian.Uint32(value[20:24])
			binary.BigEndian.PutUint32(value[20:24], offset+1)
			return value
		}},
		{name: "table out of bounds", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			binary.BigEndian.PutUint32(value[24:28], math.MaxUint32)
			return value
		}},
		{name: "physical overlap", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			cmapOffset := binary.BigEndian.Uint32(value[20:24])
			headDirectory := testTableDirectoryOffset(value, "head")
			binary.BigEndian.PutUint32(value[headDirectory+8:headDirectory+12], cmapOffset)
			return value
		}},
		{name: "physical gap", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			headDirectory := testTableDirectoryOffset(value, "head")
			headOffset := binary.BigEndian.Uint32(value[headDirectory+8 : headDirectory+12])
			binary.BigEndian.PutUint32(value[headDirectory+8:headDirectory+12], headOffset+4)
			return value
		}},
		{name: "table checksum", is: ErrChecksumInvalid, mutate: func(value []byte) []byte {
			value[19] ^= 1
			return value
		}},
		{name: "whole checksum", is: ErrChecksumInvalid, mutate: func(value []byte) []byte {
			headAt := testTableOffset(value, "head")
			value[headAt+11] ^= 1
			return value
		}},
		{name: "nonzero padding", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			headAt := testTableOffset(value, "head")
			value[headAt+54] = 1
			return value
		}},
		{name: "trailing bytes", is: ErrContainerInvalid, mutate: func(value []byte) []byte {
			return append(value, 0, 0, 0, 0)
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			value := test.mutate(append([]byte(nil), valid...))
			font, receipt, err := Parse(context.Background(), value, Limits{})
			if font != nil || !isZeroReceipt(receipt) || !errors.Is(err, test.is) {
				t.Fatalf("Parse() = %v, %#v, %v; want nil, zero, %v", font, receipt, err, test.is)
			}
		})
	}

	t.Run("missing required", func(t *testing.T) {
		tables := cloneTestTables(baseTables)
		delete(tables, "glyf")
		_, receipt, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
		if !isZeroReceipt(receipt) || !errors.Is(err, ErrProfileMismatch) {
			t.Fatalf("Parse() receipt=%#v error=%v", receipt, err)
		}
	})
	t.Run("rejected table", func(t *testing.T) {
		tables := cloneTestTables(baseTables)
		tables["kern"] = []byte{0, 0, 0, 0}
		_, _, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
		if !errors.Is(err, ErrProfileMismatch) {
			t.Fatalf("Parse() error=%v", err)
		}
	})
}

func TestParseCoreTableHostileMutations(t *testing.T) {
	base := minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)},
		[]testCmapRecord{{platformID: 3, encodingID: 1, subtable: 0}},
	))
	tests := []struct {
		name   string
		mutate func(map[string][]byte)
	}{
		{name: "head reserved flag", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["head"][16:18], 1<<5)
		}},
		{name: "head units", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["head"][18:20], 15)
		}},
		{name: "head loca format", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["head"][50:52], 2)
		}},
		{name: "maxp zero glyphs", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["maxp"][4:6], 0)
		}},
		{name: "maxp zones", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["maxp"][14:16], 3)
		}},
		{name: "hhea reserved", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["hhea"][24:26], 1)
		}},
		{name: "hhea zero metrics", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["hhea"][34:36], 0)
		}},
		{name: "hhea advance max mismatch", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["hhea"][10:12], 499)
		}},
		{name: "hmtx trailing bytes", mutate: func(tables map[string][]byte) {
			tables["hmtx"] = append(tables["hmtx"], 0, 0)
		}},
		{name: "loca beyond glyf", mutate: func(tables map[string][]byte) {
			binary.BigEndian.PutUint16(tables["loca"][4:6], 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tables := cloneTestTables(base)
			test.mutate(tables)
			font, receipt, err := Parse(context.Background(), buildTestSFNT(t, tables), Limits{})
			if font != nil || !isZeroReceipt(receipt) || !errors.Is(err, ErrTableInvalid) {
				t.Fatalf("Parse() = %v, %#v, %v", font, receipt, err)
			}
		})
	}
}

func TestCmapFormatsAndSelection(t *testing.T) {
	tests := []struct {
		name      string
		numGlyphs uint16
		cmap      []byte
		want      map[rune]uint32
		wantError error
	}{
		{
			name: "format4 range offset", numGlyphs: 2,
			cmap: makeTestCmap([][]byte{makeFormat4Range(0x0041, 1, 0)}, []testCmapRecord{{3, 1, 0}}),
			want: map[rune]uint32{'A': 1},
		},
		{
			name: "format4 range delta wraps to missing", numGlyphs: 2,
			cmap: makeTestCmap([][]byte{makeFormat4Range(0x0041, 1, 0xffff)}, []testCmapRecord{{3, 1, 0}}),
			want: map[rune]uint32{},
		},
		{
			name: "format4 raw zero ignores nonzero delta", numGlyphs: 2,
			cmap: makeTestCmap([][]byte{makeFormat4Range(0x0041, 0, 1)}, []testCmapRecord{{3, 1, 0}}),
			want: map[rune]uint32{},
		},
		{
			name: "format12", numGlyphs: 2,
			cmap: makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x10000, 0x10000, 1}})}, []testCmapRecord{{3, 10, 0}}),
			want: map[rune]uint32{0x10000: 1},
		},
		{
			name: "exact alias", numGlyphs: 2,
			cmap: makeTestCmap([][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{0, 3, 0}, {3, 1, 0}}),
			want: map[rune]uint32{'A': 1},
		},
		{
			name: "format12 replaces format4 and may add BMP", numGlyphs: 3,
			cmap: makeTestCmap(
				[][]byte{makeFormat4Direct(0x0041, 1), makeFormat12([]testFormat12Group{{0x0041, 0x0042, 1}})},
				[]testCmapRecord{{0, 3, 0}, {0, 4, 1}},
			),
			want: map[rune]uint32{'A': 1, 'B': 2},
		},
		{
			name: "format12 missing format4 mapping", numGlyphs: 2,
			cmap: makeTestCmap(
				[][]byte{makeFormat4Direct(0x0041, 1), makeFormat12([]testFormat12Group{{0x0042, 0x0042, 1}})},
				[]testCmapRecord{{0, 3, 0}, {0, 4, 1}},
			),
			wantError: ErrCmapInvalid,
		},
		{
			name: "format12 changes format4 mapping", numGlyphs: 3,
			cmap: makeTestCmap(
				[][]byte{makeFormat4Direct(0x0041, 1), makeFormat12([]testFormat12Group{{0x0041, 0x0041, 2}})},
				[]testCmapRecord{{0, 3, 0}, {0, 4, 1}},
			),
			wantError: ErrCmapInvalid,
		},
		{
			name: "same format disagreement", numGlyphs: 3,
			cmap: makeTestCmap(
				[][]byte{makeFormat4Direct(0x0041, 1), makeFormat4Direct(0x0041, 2)},
				[]testCmapRecord{{0, 3, 0}, {3, 1, 1}},
			),
			wantError: ErrCmapInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font, receipt, err := Parse(context.Background(), buildTestSFNT(t, minimalTestTables(test.numGlyphs, test.cmap)), Limits{})
			if test.wantError != nil {
				if font != nil || !isZeroReceipt(receipt) || !errors.Is(err, test.wantError) {
					t.Fatalf("Parse() = %v, %#v, %v", font, receipt, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			for character, glyphID := range test.want {
				got, ok := font.NominalGlyph(character)
				if !ok || got != glyphID {
					t.Fatalf("NominalGlyph(%U) = %d, %t; want %d", character, got, ok, glyphID)
				}
			}
			if len(test.want) == 0 {
				if _, ok := font.NominalGlyph('A'); ok {
					t.Fatal("wrapped missing glyph unexpectedly found")
				}
			}
		})
	}
}

func TestCmapHostileMutations(t *testing.T) {
	baseFormat4 := makeTestCmap([][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}})
	tests := []struct {
		name      string
		numGlyphs uint16
		cmap      func() []byte
		want      error
	}{
		{name: "odd idRangeOffset", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+28:12+30], 1)
			return value
		}},
		{name: "inside arrays idRangeOffset", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+28:12+30], 2)
			return value
		}},
		{name: "out of bounds idRangeOffset", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+28:12+30], 100)
			return value
		}},
		{name: "format4 search fields", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+8:12+10], 2)
			return value
		}},
		{name: "format4 reserved pad", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+18:12+20], 1)
			return value
		}},
		{name: "format4 segment order", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+20:12+22], 0x42)
			return value
		}},
		{name: "sentinel nonzero", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint16(value[12+26:12+28], 0)
			return value
		}},
		{name: "surrogate nonzero", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := makeTestCmap([][]byte{makeFormat4Direct(0xd800, 1)}, []testCmapRecord{{3, 1, 0}})
			return value
		}},
		{name: "gid at numGlyphs", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat4Direct(0x0041, 2)}, []testCmapRecord{{3, 1, 0}})
		}},
		{name: "format12 final gid at numGlyphs", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x0041, 0x0042, 1}})}, []testCmapRecord{{3, 10, 0}})
		}},
		{name: "format12 glyph overflow", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x0041, 0x0042, math.MaxUint32}})}, []testCmapRecord{{3, 10, 0}})
		}},
		{name: "format12 surrogate range", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0xd7ff, 0xe000, 0}})}, []testCmapRecord{{3, 10, 0}})
		}},
		{name: "format12 reserved", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x41, 0x41, 1}})}, []testCmapRecord{{3, 10, 0}})
			binary.BigEndian.PutUint16(value[12+2:12+4], 1)
			return value
		}},
		{name: "format12 language", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x41, 0x41, 1}})}, []testCmapRecord{{3, 10, 0}})
			binary.BigEndian.PutUint32(value[12+8:12+12], 1)
			return value
		}},
		{name: "format12 length mismatch", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x41, 0x41, 1}})}, []testCmapRecord{{3, 10, 0}})
			binary.BigEndian.PutUint32(value[12+4:12+8], 27)
			return value
		}},
		{name: "format12 group overlap", numGlyphs: 4, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x41, 0x42, 1}, {0x42, 0x43, 1}})}, []testCmapRecord{{3, 10, 0}})
		}},
		{name: "format12 outside unicode", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat12([]testFormat12Group{{0x110000, 0x110000, 1}})}, []testCmapRecord{{3, 10, 0}})
		}},
		{name: "record order", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat4Direct(0x41, 1)}, []testCmapRecord{{3, 1, 0}, {0, 3, 0}})
		}},
		{name: "unsupported encoding", numGlyphs: 2, want: ErrProfileMismatch, cmap: func() []byte {
			return makeTestCmap([][]byte{makeFormat4Direct(0x41, 1)}, []testCmapRecord{{3, 0, 0}})
		}},
		{name: "record offset in array", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint32(value[8:12], 4)
			return value
		}},
		{name: "record offset out of bounds", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			value := append([]byte(nil), baseFormat4...)
			binary.BigEndian.PutUint32(value[8:12], uint32(len(value)))
			return value
		}},
		{name: "nonzero trailing bytes", numGlyphs: 2, want: ErrCmapInvalid, cmap: func() []byte {
			return append(append([]byte(nil), baseFormat4...), 1)
		}},
		{name: "partial overlap", numGlyphs: 2, want: ErrCmapInvalid, cmap: makePartialOverlapCmap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font, receipt, err := Parse(context.Background(), buildTestSFNT(t, minimalTestTables(test.numGlyphs, test.cmap())), Limits{})
			if font != nil || !isZeroReceipt(receipt) || !errors.Is(err, test.want) {
				t.Fatalf("Parse() = %v, %#v, %v", font, receipt, err)
			}
		})
	}
}

func TestParseLimitsCancellationAndTypedErrors(t *testing.T) {
	encoded := buildTestSFNT(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	tests := []struct {
		name   string
		ctx    context.Context
		limits Limits
		is     error
		code   ErrorCode
	}{
		{name: "nil context", ctx: nil, is: ErrContextInvalid, code: CodeContextInvalid},
		{name: "encoded limit", ctx: context.Background(), limits: func() Limits {
			value := DefaultLimits()
			value.MaxEncodedBytes = uint64(len(encoded) - 1)
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
		{name: "table limit", ctx: context.Background(), limits: func() Limits {
			value := DefaultLimits()
			value.MaxTables = 6
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
		{name: "mapping limit", ctx: context.Background(), limits: func() Limits {
			value := DefaultLimits()
			value.MaxCmapMappings = 1
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
		{name: "work N minus one", ctx: context.Background(), limits: func() Limits {
			_, receipt, err := Parse(context.Background(), encoded, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			value := DefaultLimits()
			value.MaxWorkUnits = receipt.WorkUnits - 1
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
		{name: "working N minus one", ctx: context.Background(), limits: func() Limits {
			_, receipt, err := Parse(context.Background(), encoded, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			value := DefaultLimits()
			value.MaxWorkingBytes = receipt.WorkingBytes - 1
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
		{name: "retained N minus one", ctx: context.Background(), limits: func() Limits {
			_, receipt, err := Parse(context.Background(), encoded, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			value := DefaultLimits()
			value.MaxRetainedBytes = receipt.RetainedBytes - 1
			return value
		}(), is: ErrResourceLimit, code: CodeResourceLimit},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests = append(tests, struct {
		name   string
		ctx    context.Context
		limits Limits
		is     error
		code   ErrorCode
	}{name: "canceled", ctx: canceled, is: ErrCanceled, code: CodeCanceled})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			font, receipt, err := Parse(test.ctx, encoded, test.limits)
			if font != nil || !isZeroReceipt(receipt) || !errors.Is(err, test.is) {
				t.Fatalf("Parse() = %v, %#v, %v", font, receipt, err)
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != test.code || typed.Path == "" {
				t.Fatalf("typed error = %#v", typed)
			}
		})
	}
	for _, field := range []func(*Limits){
		func(value *Limits) { value.MaxTables = HardMaxTables + 1 },
		func(value *Limits) { value.MaxWorkingBytes = HardMaxWorkingBytes + 1 },
	} {
		limits := DefaultLimits()
		field(&limits)
		_, _, err := Parse(context.Background(), encoded, limits)
		if !errors.Is(err, ErrLimitsInvalid) {
			t.Fatalf("invalid limits error = %v", err)
		}
	}
	_, exactReceipt, err := Parse(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	exact := DefaultLimits()
	exact.MaxWorkUnits = exactReceipt.WorkUnits
	exact.MaxWorkingBytes = exactReceipt.WorkingBytes
	exact.MaxRetainedBytes = exactReceipt.RetainedBytes
	if font, _, err := Parse(context.Background(), encoded, exact); err != nil || font == nil {
		t.Fatalf("exact resource limits = %v, %v", font, err)
	}
}

func TestDirectorySearchFieldRepresentability(t *testing.T) {
	searchRange, entrySelector, rangeShift := directorySearchFields(HardMaxTables)
	if searchRange != 32768 || entrySelector != 11 || rangeShift != 32752 {
		t.Fatalf("directorySearchFields(4095) = %d, %d, %d", searchRange, entrySelector, rangeShift)
	}
	limits := DefaultLimits()
	limits.MaxTables = HardMaxTables + 1
	if _, err := normalizeLimits(limits); !errors.Is(err, ErrLimitsInvalid) {
		t.Fatalf("normalizeLimits(4096) error = %v", err)
	}
}

func TestFontConcurrentReadSafety(t *testing.T) {
	font, _, err := Parse(context.Background(), buildTestSFNT(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	))), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 1000; iteration++ {
				font.NominalGlyph('A')
				font.HorizontalMetric(1)
				font.GlyphRange(1)
				font.Receipt()
			}
		}()
	}
	wait.Wait()
}

func TestCanonicalDescriptorAndReceiptHashes(t *testing.T) {
	descriptorJSON, err := json.Marshal(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	descriptorDigest := sha256.Sum256(descriptorJSON)
	const wantDescriptorSHA256 = "39c08736811403bbd64f7eefca5ee5558fb9cffcf9e8ded233648ae4d86b102c"
	if got := hex.EncodeToString(descriptorDigest[:]); got != wantDescriptorSHA256 {
		t.Fatalf("descriptor sha256 = %s; update fixture after review", got)
	}
	descriptorFixture, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(descriptorFixture, append(append([]byte(nil), descriptorJSON...), '\n')) {
		t.Fatalf("descriptor fixture drift\ngot:  %s\nwant: %s", descriptorJSON, bytes.TrimSpace(descriptorFixture))
	}
	_, receipt, err := Parse(context.Background(), buildTestSFNT(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	))), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	const wantReceiptSHA256 = "ae051d4a75f24710869ac1dbdb254d341298bb9c73ad3a9f69319897b4d2269f"
	if got := hex.EncodeToString(receiptDigest[:]); got != wantReceiptSHA256 {
		t.Fatalf("receipt sha256 = %s; update fixture after review\n%s", got, receiptJSON)
	}
	receiptFixture, err := os.ReadFile("testdata/minimal-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptFixture, append(append([]byte(nil), receiptJSON...), '\n')) {
		t.Fatalf("receipt fixture drift\ngot:  %s\nwant: %s", receiptJSON, bytes.TrimSpace(receiptFixture))
	}
}

func TestMinimalFontHexFixture(t *testing.T) {
	hexFixture, err := os.ReadFile("testdata/minimal-font-v1.hex")
	if err != nil {
		t.Fatal(err)
	}
	built := buildTestSFNT(t, minimalTestTables(2, makeTestCmap(
		[][]byte{makeFormat4Direct(0x0041, 1)}, []testCmapRecord{{3, 1, 0}},
	)))
	wantFixture := append([]byte(hex.EncodeToString(built)), '\n')
	if !bytes.Equal(hexFixture, wantFixture) {
		t.Fatal("minimal font fixture differs from canonical builder")
	}
	fixture, err := hex.DecodeString(string(hexFixture[:len(hexFixture)-1]))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fixture)
	if got := hex.EncodeToString(digest[:]); got != "dd93a040f53dce0abe8743b140642f60f4d08079b90f09cdabf5a8a4f05fe09d" {
		t.Fatalf("minimal font sha256 = %s", got)
	}
	if _, _, err := Parse(context.Background(), fixture, Limits{}); err != nil {
		t.Fatalf("Parse(minimal fixture) error = %v", err)
	}
}

func minimalTestTables(numGlyphs uint16, cmap []byte) map[string][]byte {
	head := make([]byte, 54)
	binary.BigEndian.PutUint32(head[0:4], 0x00010000)
	binary.BigEndian.PutUint32(head[4:8], 0x00010000)
	binary.BigEndian.PutUint32(head[12:16], headMagicNumber)
	binary.BigEndian.PutUint16(head[18:20], 1000)
	binary.BigEndian.PutUint16(head[46:48], 8)
	binary.BigEndian.PutUint16(head[48:50], 2)

	maxp := make([]byte, 32)
	binary.BigEndian.PutUint32(maxp[0:4], 0x00010000)
	binary.BigEndian.PutUint16(maxp[4:6], numGlyphs)
	binary.BigEndian.PutUint16(maxp[14:16], 1)

	hhea := make([]byte, 36)
	binary.BigEndian.PutUint32(hhea[0:4], 0x00010000)
	binary.BigEndian.PutUint16(hhea[4:6], uint16(800))
	descender := int16(-200)
	binary.BigEndian.PutUint16(hhea[6:8], uint16(descender))
	binary.BigEndian.PutUint16(hhea[10:12], 500)
	binary.BigEndian.PutUint16(hhea[18:20], 1)
	binary.BigEndian.PutUint16(hhea[34:36], 1)

	hmtx := make([]byte, 4+int(numGlyphs-1)*2)
	binary.BigEndian.PutUint16(hmtx[0:2], 500)
	loca := make([]byte, (int(numGlyphs)+1)*2)
	return map[string][]byte{
		"cmap": cmap,
		"glyf": {},
		"head": head,
		"hhea": hhea,
		"hmtx": hmtx,
		"loca": loca,
		"maxp": maxp,
	}
}

func makeFormat4Direct(codepoint, glyphID uint16) []byte {
	table := make([]byte, 32)
	binary.BigEndian.PutUint16(table[0:2], 4)
	binary.BigEndian.PutUint16(table[2:4], uint16(len(table)))
	binary.BigEndian.PutUint16(table[6:8], 4)
	binary.BigEndian.PutUint16(table[8:10], 4)
	binary.BigEndian.PutUint16(table[10:12], 1)
	binary.BigEndian.PutUint16(table[14:16], codepoint)
	binary.BigEndian.PutUint16(table[16:18], 0xffff)
	binary.BigEndian.PutUint16(table[20:22], codepoint)
	binary.BigEndian.PutUint16(table[22:24], 0xffff)
	binary.BigEndian.PutUint16(table[24:26], glyphID-codepoint)
	binary.BigEndian.PutUint16(table[26:28], 1)
	return table
}

func makeFormat4Range(codepoint, rawGlyphID, delta uint16) []byte {
	table := make([]byte, 34)
	binary.BigEndian.PutUint16(table[0:2], 4)
	binary.BigEndian.PutUint16(table[2:4], uint16(len(table)))
	binary.BigEndian.PutUint16(table[6:8], 4)
	binary.BigEndian.PutUint16(table[8:10], 4)
	binary.BigEndian.PutUint16(table[10:12], 1)
	binary.BigEndian.PutUint16(table[14:16], codepoint)
	binary.BigEndian.PutUint16(table[16:18], 0xffff)
	binary.BigEndian.PutUint16(table[20:22], codepoint)
	binary.BigEndian.PutUint16(table[22:24], 0xffff)
	binary.BigEndian.PutUint16(table[24:26], delta)
	binary.BigEndian.PutUint16(table[26:28], 1)
	binary.BigEndian.PutUint16(table[28:30], 4)
	binary.BigEndian.PutUint16(table[32:34], rawGlyphID)
	return table
}

func makeFormat12(groups []testFormat12Group) []byte {
	table := make([]byte, 16+len(groups)*12)
	binary.BigEndian.PutUint16(table[0:2], 12)
	binary.BigEndian.PutUint32(table[4:8], uint32(len(table)))
	binary.BigEndian.PutUint32(table[12:16], uint32(len(groups)))
	for index, group := range groups {
		at := 16 + index*12
		binary.BigEndian.PutUint32(table[at:at+4], group.start)
		binary.BigEndian.PutUint32(table[at+4:at+8], group.end)
		binary.BigEndian.PutUint32(table[at+8:at+12], group.startGlyphID)
	}
	return table
}

func makeTestCmap(subtables [][]byte, records []testCmapRecord) []byte {
	headerLength := 4 + len(records)*8
	offsets := make([]uint32, len(subtables))
	length := headerLength
	for index, subtable := range subtables {
		offsets[index] = uint32(length)
		length += len(subtable)
	}
	result := make([]byte, length)
	binary.BigEndian.PutUint16(result[2:4], uint16(len(records)))
	for index, record := range records {
		at := 4 + index*8
		binary.BigEndian.PutUint16(result[at:at+2], record.platformID)
		binary.BigEndian.PutUint16(result[at+2:at+4], record.encodingID)
		binary.BigEndian.PutUint32(result[at+4:at+8], offsets[record.subtable])
	}
	for index, subtable := range subtables {
		copy(result[offsets[index]:], subtable)
	}
	return result
}

func makePartialOverlapCmap() []byte {
	first := makeFormat4Direct(0x0041, 1)
	result := make([]byte, 74)
	binary.BigEndian.PutUint16(result[2:4], 2)
	binary.BigEndian.PutUint16(result[4:6], 0)
	binary.BigEndian.PutUint16(result[6:8], 3)
	binary.BigEndian.PutUint32(result[8:12], 20)
	binary.BigEndian.PutUint16(result[12:14], 3)
	binary.BigEndian.PutUint16(result[14:16], 1)
	binary.BigEndian.PutUint32(result[16:20], 50)
	copy(result[20:52], first)
	binary.BigEndian.PutUint16(result[50:52], 4)
	binary.BigEndian.PutUint16(result[52:54], 24)
	return result
}

type testFataler interface {
	Helper()
	Fatalf(string, ...any)
}

func buildTestSFNT(t testFataler, source map[string][]byte) []byte {
	t.Helper()
	tables := cloneTestTables(source)
	tags := make([]string, 0, len(tables))
	for tag := range tables {
		if len(tag) != 4 {
			t.Fatalf("invalid test tag %q", tag)
		}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	if head, ok := tables["head"]; ok && len(head) >= 12 {
		for index := 8; index < 12; index++ {
			head[index] = 0
		}
	}
	directoryEnd := 12 + len(tags)*16
	font := make([]byte, (directoryEnd+3)&^3)
	binary.BigEndian.PutUint32(font[0:4], sfntVersionTrueType)
	binary.BigEndian.PutUint16(font[4:6], uint16(len(tags)))
	searchRange, entrySelector, rangeShift := directorySearchFields(uint32(len(tags)))
	binary.BigEndian.PutUint16(font[6:8], searchRange)
	binary.BigEndian.PutUint16(font[8:10], entrySelector)
	binary.BigEndian.PutUint16(font[10:12], rangeShift)
	offsets := make(map[string]uint32, len(tags))
	for index, tag := range tags {
		data := tables[tag]
		offset := uint32(len(font))
		offsets[tag] = offset
		at := 12 + index*16
		copy(font[at:at+4], tag)
		binary.BigEndian.PutUint32(font[at+4:at+8], testChecksum(data))
		binary.BigEndian.PutUint32(font[at+8:at+12], offset)
		binary.BigEndian.PutUint32(font[at+12:at+16], uint32(len(data)))
		font = append(font, data...)
		for len(font)&3 != 0 {
			font = append(font, 0)
		}
	}
	headOffset, ok := offsets["head"]
	if ok && len(tables["head"]) >= 12 {
		adjustment := sfntChecksumMagic - testChecksum(font)
		binary.BigEndian.PutUint32(font[int(headOffset)+8:int(headOffset)+12], adjustment)
	}
	return font
}

func testChecksum(value []byte) uint32 {
	var sum uint32
	for offset := 0; offset < len(value); offset += 4 {
		var word [4]byte
		end := offset + 4
		if end > len(value) {
			end = len(value)
		}
		copy(word[:], value[offset:end])
		sum += binary.BigEndian.Uint32(word[:])
	}
	return sum
}

func cloneTestTables(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for tag, data := range source {
		result[tag] = append([]byte(nil), data...)
	}
	return result
}

func testTableOffset(font []byte, tag string) int {
	count := int(binary.BigEndian.Uint16(font[4:6]))
	for index := 0; index < count; index++ {
		at := 12 + index*16
		if string(font[at:at+4]) == tag {
			return int(binary.BigEndian.Uint32(font[at+8 : at+12]))
		}
	}
	return -1
}

func testTableDirectoryOffset(font []byte, tag string) int {
	count := int(binary.BigEndian.Uint16(font[4:6]))
	for index := 0; index < count; index++ {
		at := 12 + index*16
		if string(font[at:at+4]) == tag {
			return at
		}
	}
	return -1
}

func isZeroReceipt(value Receipt) bool {
	return reflect.DeepEqual(value, Receipt{})
}

func testTableDirectoryChecksum(font []byte, tag string) uint32 {
	count := int(binary.BigEndian.Uint16(font[4:6]))
	for index := 0; index < count; index++ {
		at := 12 + index*16
		if string(font[at:at+4]) == tag {
			return binary.BigEndian.Uint32(font[at+4 : at+8])
		}
	}
	return 0
}

func testTableLength(font []byte, tag string) int {
	count := int(binary.BigEndian.Uint16(font[4:6]))
	for index := 0; index < count; index++ {
		at := 12 + index*16
		if string(font[at:at+4]) == tag {
			return int(binary.BigEndian.Uint32(font[at+12 : at+16]))
		}
	}
	return -1
}
