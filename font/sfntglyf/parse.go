package sfntglyf

import (
	"context"
	"encoding/binary"
	"fmt"
)

// Parse validates one complete static TrueType core font and returns an immutable owned Font.
// Encoded is borrowed until Parse returns and must not be mutated concurrently. The returned
// evidence does not authorize outline decoding, shaping, text layout, or rendering.
func Parse(ctx context.Context, encoded []byte, limits Limits) (*Font, Receipt, error) {
	if ctx == nil {
		return nil, Receipt{}, fontError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, Receipt{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, Receipt{}, err
	}
	if uint64(len(encoded)) > normalized.MaxEncodedBytes {
		return nil, Receipt{}, fontError(CodeResourceLimit, "encoded_bytes", ErrResourceLimit)
	}
	if logicalFixedWorkingBytes > normalized.MaxWorkingBytes {
		return nil, Receipt{}, fontError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	baseWork, err := preflightBaseWork(encoded, normalized)
	if err != nil {
		return nil, Receipt{}, err
	}
	var declaredTableCount uint64
	if len(encoded) >= 6 {
		declaredTableCount = uint64(binary.BigEndian.Uint16(encoded[4:6]))
	}
	baseRetained, err := preflightBaseRetained(encoded, declaredTableCount, normalized)
	if err != nil {
		return nil, Receipt{}, err
	}
	container, err := validateContainer(ctx, encoded, normalized)
	if err != nil {
		return nil, Receipt{}, err
	}
	core, err := validateCore(ctx, encoded, container.records, normalized, baseWork)
	if err != nil {
		return nil, Receipt{}, err
	}
	if declaredTableCount != uint64(len(container.records)) {
		return nil, Receipt{}, fontError(CodeInvariantFailed, "table_count", ErrInvariantFailed)
	}
	cmapRecord, _ := findRecord(container.records, "cmap")
	cmap, err := validateCmap(ctx, encoded, cmapRecord, core.numGlyphs, normalized, core.workUnits, baseRetained)
	if err != nil {
		return nil, Receipt{}, err
	}
	tableReceipts := make([]TableReceipt, 0, len(container.records))
	opaqueLayoutTables := make([]string, 0, 3)
	for index, record := range container.records {
		if err := contextFailure(ctx, fmt.Sprintf("table_hashes/%d", index)); err != nil {
			return nil, Receipt{}, err
		}
		hash, err := hashBytes(ctx, tableBytes(encoded, record), fmt.Sprintf("table_hashes/%d", index))
		if err != nil {
			return nil, Receipt{}, err
		}
		tag := string(record.tag[:])
		tableReceipts = append(tableReceipts, TableReceipt{
			Tag: tag, Offset: record.offset, Length: record.length,
			Checksum: record.checksum, ContentHash: hash,
		})
		if tag == "GDEF" || tag == "GPOS" || tag == "GSUB" {
			opaqueLayoutTables = append(opaqueLayoutTables, tag)
		}
	}
	contentHash, err := hashBytes(ctx, encoded, "font_hash")
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := contextFailure(ctx, "owned_copy"); err != nil {
		return nil, Receipt{}, err
	}
	owned, err := allocateBytes(len(encoded), "owned_copy")
	if err != nil {
		return nil, Receipt{}, err
	}
	for offset := 0; offset < len(encoded); offset += contextStrideBytes {
		if err := contextFailure(ctx, "owned_copy"); err != nil {
			return nil, Receipt{}, err
		}
		end := offset + contextStrideBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		copy(owned[offset:end], encoded[offset:end])
	}

	receipt := Receipt{
		SchemaVersion: ReceiptSchemaVersion, Profile: ReceiptProfileV1,
		ContentHashProfile: ContentHashProfileV1, ContentHash: contentHash,
		TableHashProfile: TableHashProfileV1, CmapProfile: CmapProfileV1,
		LayoutTableStatus: LayoutTableStatusV1, GlyfTableStatus: GlyfTableStatusV1,
		MetricExtremaStatus: DeclaredStatusV1,
		EncodedBytes:        uint64(len(encoded)), TableCount: uint32(len(container.records)),
		NumGlyphs: core.numGlyphs, UnitsPerEm: core.unitsPerEm,
		HheaAscender: core.hheaAscender, NumberOfHMetrics: core.numberOfHMetrics,
		IndexToLocFormat: core.indexToLocFormat,
		CmapRecordCount:  cmap.recordCount, CmapFormat4Segments: cmap.format4Segments,
		CmapFormat12Groups: cmap.format12Groups, CmapMappingSlots: cmap.mappingSlots,
		CmapMappingCount:   uint64(len(cmap.mappings)),
		OpaqueLayoutTables: opaqueLayoutTables, Tables: tableReceipts,
		WorkUnits: cmap.workUnits, WorkingBytes: cmap.workingBytes, RetainedBytes: cmap.retainedBytes,
	}
	font := &Font{
		encoded: owned, tables: append([]tableRecord(nil), container.records...),
		mappings: cmap.mappings, numGlyphs: core.numGlyphs, unitsPerEm: core.unitsPerEm,
		hheaAscender: core.hheaAscender, numberOfHMetrics: core.numberOfHMetrics,
		indexToLocFormat: core.indexToLocFormat, hmtxOffset: core.hmtxOffset,
		locaOffset: core.locaOffset, glyfOffset: core.glyfOffset, receipt: receipt,
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return nil, Receipt{}, err
	}
	return font, font.Receipt(), nil
}

func preflightBaseWork(encoded []byte, limits Limits) (uint64, error) {
	encodedWork, ok := checkedMultiply(uint64(len(encoded)), 8)
	if !ok {
		return 0, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	var tableCount uint64
	if len(encoded) >= 6 {
		tableCount = uint64(binary.BigEndian.Uint16(encoded[4:6]))
	}
	tableWork, ok := checkedMultiply(tableCount, 64)
	if !ok {
		return 0, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	workUnits, ok := checkedAdd(encodedWork, tableWork)
	if !ok || workUnits > limits.MaxWorkUnits {
		return 0, fontError(CodeResourceLimit, "work_units", ErrResourceLimit)
	}
	return workUnits, nil
}

func preflightBaseRetained(encoded []byte, tableCount uint64, limits Limits) (uint64, error) {
	tableBytes, ok := checkedMultiply(tableCount, logicalTableBytes)
	if !ok {
		return 0, fontError(CodeResourceLimit, "retained_bytes", ErrResourceLimit)
	}
	retained, ok := checkedAdd(logicalFixedRetainedBytes, uint64(len(encoded)))
	if ok {
		retained, ok = checkedAdd(retained, tableBytes)
	}
	if !ok || retained > limits.MaxRetainedBytes {
		return 0, fontError(CodeResourceLimit, "retained_bytes", ErrResourceLimit)
	}
	return retained, nil
}
