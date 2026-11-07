package sfntglyf

import (
	"context"
	"encoding/binary"
	"math/rand"
	"sort"
	"testing"
)

// TestCmapIndependentOracle differentially checks the production lookup against deliberately
// slow test-only decoders. The oracles spell out the OpenType address arithmetic independently;
// they do not call production measurement, decode, search-field, or lookup helpers.
func TestCmapIndependentOracle(t *testing.T) {
	t.Run("format4 random direct segments", func(t *testing.T) {
		random := rand.New(rand.NewSource(0x4355544c59))
		for testCase := 0; testCase < 16; testCase++ {
			unique := make(map[uint16]uint16)
			for len(unique) < 1+random.Intn(24) {
				codepoint := uint16(random.Intn(0xfffe))
				if codepoint >= 0xd800 && codepoint <= 0xdfff {
					continue
				}
				unique[codepoint] = uint16(1 + random.Intn(63))
			}
			pairs := make([]cmapMapping, 0, len(unique))
			for codepoint, glyphID := range unique {
				pairs = append(pairs, cmapMapping{codepoint: uint32(codepoint), glyphID: uint32(glyphID)})
			}
			sort.Slice(pairs, func(left, right int) bool { return pairs[left].codepoint < pairs[right].codepoint })
			subtable := oracleBuildFormat4Direct(pairs)
			font := oracleParseFont(t, 64, makeTestCmap([][]byte{subtable}, []testCmapRecord{{3, 1, 0}}))
			want := oracleDecodeFormat4(subtable)
			for codepoint := uint32(0); codepoint <= 0xffff; codepoint++ {
				glyphID, found := font.NominalGlyph(rune(codepoint))
				wantGlyphID, wantFound := want[codepoint]
				if found != wantFound || glyphID != wantGlyphID {
					t.Fatalf("case %d codepoint U+%04X = %d,%t; want %d,%t", testCase, codepoint, glyphID, found, wantGlyphID, wantFound)
				}
			}
		}
	})

	t.Run("format4 glyph array boundaries", func(t *testing.T) {
		for _, subtable := range [][]byte{
			makeFormat4Range(0x0041, 7, 0),
			makeFormat4Range(0x0041, 1, 0xffff),
			makeFormat4Range(0x0041, 0, 17),
		} {
			font := oracleParseFont(t, 16, makeTestCmap([][]byte{subtable}, []testCmapRecord{{3, 1, 0}}))
			want := oracleDecodeFormat4(subtable)
			for codepoint := uint32(0); codepoint <= 0xffff; codepoint++ {
				glyphID, found := font.NominalGlyph(rune(codepoint))
				wantGlyphID, wantFound := want[codepoint]
				if found != wantFound || glyphID != wantGlyphID {
					t.Fatalf("codepoint U+%04X = %d,%t; want %d,%t", codepoint, glyphID, found, wantGlyphID, wantFound)
				}
			}
		}
	})

	t.Run("format12 random singleton groups", func(t *testing.T) {
		random := rand.New(rand.NewSource(0x53464e543132))
		unique := make(map[uint32]uint32)
		for len(unique) < 64 {
			codepoint := uint32(random.Intn(0x110000))
			if codepoint >= 0xd800 && codepoint <= 0xdfff {
				continue
			}
			unique[codepoint] = uint32(1 + random.Intn(126))
		}
		groups := make([]testFormat12Group, 0, len(unique))
		for codepoint, glyphID := range unique {
			groups = append(groups, testFormat12Group{start: codepoint, end: codepoint, startGlyphID: glyphID})
		}
		sort.Slice(groups, func(left, right int) bool { return groups[left].start < groups[right].start })
		subtable := makeFormat12(groups)
		font := oracleParseFont(t, 128, makeTestCmap([][]byte{subtable}, []testCmapRecord{{3, 10, 0}}))
		want := oracleDecodeFormat12(subtable)
		for codepoint, wantGlyphID := range want {
			glyphID, found := font.NominalGlyph(rune(codepoint))
			if !found || glyphID != wantGlyphID {
				t.Fatalf("mapped U+%06X = %d,%t; want %d", codepoint, glyphID, found, wantGlyphID)
			}
		}
		for sample := 0; sample < 4096; sample++ {
			codepoint := uint32(random.Intn(0x110000))
			glyphID, found := font.NominalGlyph(rune(codepoint))
			wantGlyphID, wantFound := want[codepoint]
			if found != wantFound || glyphID != wantGlyphID {
				t.Fatalf("sample U+%06X = %d,%t; want %d,%t", codepoint, glyphID, found, wantGlyphID, wantFound)
			}
		}
	})

	t.Run("format12 continuous zero start and unicode endpoint", func(t *testing.T) {
		subtable := makeFormat12([]testFormat12Group{
			{start: 0x0041, end: 0x0043, startGlyphID: 0},
			{start: 0x10ffff, end: 0x10ffff, startGlyphID: 3},
		})
		font := oracleParseFont(t, 4, makeTestCmap([][]byte{subtable}, []testCmapRecord{{3, 10, 0}}))
		want := oracleDecodeFormat12(subtable)
		for _, codepoint := range []uint32{0x0041, 0x0042, 0x0043, 0x0044, 0x10fffe, 0x10ffff} {
			glyphID, found := font.NominalGlyph(rune(codepoint))
			wantGlyphID, wantFound := want[codepoint]
			if found != wantFound || glyphID != wantGlyphID {
				t.Fatalf("U+%06X = %d,%t; want %d,%t", codepoint, glyphID, found, wantGlyphID, wantFound)
			}
		}
	})
}

func oracleParseFont(t *testing.T, numGlyphs uint16, cmap []byte) *Font {
	t.Helper()
	font, _, err := Parse(context.Background(), buildTestSFNT(t, minimalTestTables(numGlyphs, cmap)), Limits{})
	if err != nil {
		t.Fatalf("Parse() oracle fixture error = %v", err)
	}
	return font
}

func oracleBuildFormat4Direct(pairs []cmapMapping) []byte {
	segmentCount := len(pairs) + 1
	table := make([]byte, 16+segmentCount*8)
	binary.BigEndian.PutUint16(table[0:2], 4)
	binary.BigEndian.PutUint16(table[2:4], uint16(len(table)))
	binary.BigEndian.PutUint16(table[6:8], uint16(segmentCount*2))
	power, selector := 1, uint16(0)
	for power*2 <= segmentCount {
		power *= 2
		selector++
	}
	binary.BigEndian.PutUint16(table[8:10], uint16(power*2))
	binary.BigEndian.PutUint16(table[10:12], selector)
	binary.BigEndian.PutUint16(table[12:14], uint16(segmentCount*2-power*2))
	endBase := 14
	startBase := 16 + segmentCount*2
	deltaBase := 16 + segmentCount*4
	for index, pair := range pairs {
		codepoint := uint16(pair.codepoint)
		binary.BigEndian.PutUint16(table[endBase+index*2:endBase+index*2+2], codepoint)
		binary.BigEndian.PutUint16(table[startBase+index*2:startBase+index*2+2], codepoint)
		binary.BigEndian.PutUint16(table[deltaBase+index*2:deltaBase+index*2+2], uint16(pair.glyphID)-codepoint)
	}
	last := segmentCount - 1
	binary.BigEndian.PutUint16(table[endBase+last*2:endBase+last*2+2], 0xffff)
	binary.BigEndian.PutUint16(table[startBase+last*2:startBase+last*2+2], 0xffff)
	binary.BigEndian.PutUint16(table[deltaBase+last*2:deltaBase+last*2+2], 1)
	return table
}

func oracleDecodeFormat4(table []byte) map[uint32]uint32 {
	result := make(map[uint32]uint32)
	segmentCount := int(binary.BigEndian.Uint16(table[6:8]) / 2)
	endCodes := table[14 : 14+segmentCount*2]
	startCodes := table[16+segmentCount*2 : 16+segmentCount*4]
	deltas := table[16+segmentCount*4 : 16+segmentCount*6]
	rangeOffsetsAt := 16 + segmentCount*6
	for segment := 0; segment < segmentCount; segment++ {
		start := binary.BigEndian.Uint16(startCodes[segment*2 : segment*2+2])
		end := binary.BigEndian.Uint16(endCodes[segment*2 : segment*2+2])
		delta := binary.BigEndian.Uint16(deltas[segment*2 : segment*2+2])
		rangeOffsetWord := rangeOffsetsAt + segment*2
		rangeOffset := binary.BigEndian.Uint16(table[rangeOffsetWord : rangeOffsetWord+2])
		for scalar := uint32(start); scalar <= uint32(end); scalar++ {
			var glyph uint16
			if rangeOffset == 0 {
				glyph = uint16(scalar) + delta
			} else {
				glyphWord := rangeOffsetWord + int(rangeOffset) + int(scalar-uint32(start))*2
				raw := binary.BigEndian.Uint16(table[glyphWord : glyphWord+2])
				if raw != 0 {
					glyph = raw + delta
				}
			}
			if glyph != 0 && (scalar < 0xd800 || scalar > 0xdfff && scalar != 0xffff) {
				result[scalar] = uint32(glyph)
			}
			if scalar == 0xffff {
				break
			}
		}
	}
	return result
}

func oracleDecodeFormat12(table []byte) map[uint32]uint32 {
	result := make(map[uint32]uint32)
	groupCount := int(binary.BigEndian.Uint32(table[12:16]))
	for group := 0; group < groupCount; group++ {
		at := 16 + group*12
		start := binary.BigEndian.Uint32(table[at : at+4])
		end := binary.BigEndian.Uint32(table[at+4 : at+8])
		startGlyph := binary.BigEndian.Uint32(table[at+8 : at+12])
		for scalar := start; scalar <= end; scalar++ {
			glyph := startGlyph + scalar - start
			if glyph != 0 {
				result[scalar] = glyph
			}
		}
	}
	return result
}
