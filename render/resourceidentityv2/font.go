package resourceidentityv2

import (
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"sort"
)

// validateFont checks the strict core receipt projection's internal invariants.
// It cannot establish checksums, cmap facts or content claims without actual
// bytes. Matches/Check require an opaque successful fresh inspection for that.
func validateFont(f sfntglyf.Receipt, b Binding) error {
	if f.SchemaVersion != sfntglyf.ReceiptSchemaVersion || f.Profile != sfntglyf.ReceiptProfileV1 || f.ContentHashProfile != sfntglyf.ContentHashProfileV1 || f.TableHashProfile != sfntglyf.TableHashProfileV1 || f.CmapProfile != sfntglyf.CmapProfileV1 || f.LayoutTableStatus != sfntglyf.LayoutTableStatusV1 || f.GlyfTableStatus != sfntglyf.GlyfTableStatusV1 || f.MetricExtremaStatus != sfntglyf.DeclaredStatusV1 || f.ContentHash != b.ContentHash || f.EncodedBytes != uint64(b.SizeBytes) || f.EncodedBytes > sfntglyf.HardMaxEncodedBytes || f.EncodedBytes%4 != 0 {
		return ErrInvalid
	}
	if f.Tables == nil || f.OpaqueLayoutTables == nil || len(f.Tables) < 7 || len(f.Tables) > int(sfntglyf.HardMaxTables) || f.TableCount != uint32(len(f.Tables)) || len(f.OpaqueLayoutTables) > 3 {
		return ErrInvalid
	}
	if f.NumGlyphs == 0 || f.NumGlyphs > sfntglyf.HardMaxGlyphs || f.UnitsPerEm < 16 || f.UnitsPerEm > 16384 || f.NumberOfHMetrics == 0 || uint32(f.NumberOfHMetrics) > f.NumGlyphs || (f.IndexToLocFormat != 0 && f.IndexToLocFormat != 1) || f.CmapRecordCount == 0 || f.CmapRecordCount > sfntglyf.HardMaxCmapRecords || f.CmapFormat4Segments > uint64(f.CmapRecordCount)*32767 || f.CmapFormat12Groups > sfntglyf.HardMaxCmapGroups || f.CmapMappingSlots > sfntglyf.HardMaxCmapMappings || f.CmapMappingCount > f.CmapMappingSlots || f.CmapMappingCount > 0x110000-0x800 {
		return ErrInvalid
	}
	// At least one format4/12 subtable is required; an empty format12 is valid.
	if f.CmapFormat4Segments != 0 && f.CmapFormat4Segments < 2 {
		return ErrInvalid
	}
	tables := make(map[string]sfntglyf.TableReceipt, len(f.Tables))
	opaque := make([]string, 0, 3)
	directoryEnd := uint64(12) + uint64(f.TableCount)*16
	for i, t := range f.Tables {
		if i > 0 && f.Tables[i-1].Tag >= t.Tag || !validDigest(t.ContentHash) || t.Offset%4 != 0 || uint64(t.Offset) < directoryEnd || uint64(t.Offset)+uint64(t.Length) > f.EncodedBytes {
			return ErrInvalid
		}
		switch t.Tag {
		case "cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "DSIG", "OS/2", "cvt ", "fpgm", "gasp", "name", "post", "prep":
		case "GDEF", "GPOS", "GSUB":
			opaque = append(opaque, t.Tag)
		default:
			return ErrInvalid
		}
		tables[t.Tag] = t
	}
	if len(opaque) != len(f.OpaqueLayoutTables) {
		return ErrInvalid
	}
	for i, t := range opaque {
		if f.OpaqueLayoutTables[i] != t {
			return ErrInvalid
		}
	}
	for _, tag := range []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp"} {
		if _, ok := tables[tag]; !ok {
			return ErrInvalid
		}
	}
	if tables["head"].Length != 54 || tables["hhea"].Length != 36 || tables["maxp"].Length != 32 || uint64(tables["hmtx"].Length) != uint64(f.NumberOfHMetrics)*4+uint64(f.NumGlyphs-uint32(f.NumberOfHMetrics))*2 {
		return ErrInvalid
	}
	locaWidth := uint64(2)
	if f.IndexToLocFormat == 1 {
		locaWidth = 4
	}
	if uint64(tables["loca"].Length) != (uint64(f.NumGlyphs)+1)*locaWidth || tables["cmap"].Length < 4+8*f.CmapRecordCount {
		return ErrInvalid
	}
	physical := append([]sfntglyf.TableReceipt(nil), f.Tables...)
	sort.Slice(physical, func(i, j int) bool {
		if physical[i].Offset == physical[j].Offset {
			return physical[i].Length < physical[j].Length
		}
		return physical[i].Offset < physical[j].Offset
	})
	end := directoryEnd
	for _, t := range physical {
		if uint64(t.Offset) != end {
			return ErrInvalid
		}
		end = (uint64(t.Offset) + uint64(t.Length) + 3) &^ uint64(3)
		if end > f.EncodedBytes {
			return ErrInvalid
		}
	}
	if end != f.EncodedBytes {
		return ErrInvalid
	}
	// All terms are bounded above before this arithmetic. These are exactly the
	// parser's portable logical charges (no unsafe.Sizeof, int width, RSS or limits).
	work := 8*f.EncodedBytes + 64*uint64(f.TableCount) + 4*uint64(f.NumGlyphs) + 32*uint64(f.CmapRecordCount) + 16*(f.CmapFormat4Segments+f.CmapFormat12Groups) + 2*f.CmapMappingSlots
	working := uint64(64<<10) + 16*f.CmapMappingSlots
	retained := uint64(16<<10) + f.EncodedBytes + 256*uint64(f.TableCount) + 8*f.CmapMappingCount
	if f.WorkUnits != work || f.WorkingBytes != working || f.RetainedBytes != retained || work > sfntglyf.HardMaxWorkUnits || working > sfntglyf.HardMaxWorkingBytes || retained > sfntglyf.HardMaxRetainedBytes {
		return ErrInvalid
	}
	return nil
}
