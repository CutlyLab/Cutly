package evaluator

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

type frozenTextIdentity struct {
	canonicalSourceHash        string
	canonicalSourceBytes       uint64
	canonicalSourceUpperBytes  uint64
	textSemanticHash           string
	textSemanticCanonicalBytes uint64
	textSemanticUpperBytes     uint64
	layoutInputHash            string
	layoutInputCanonicalBytes  uint64
	layoutInputUpperBytes      uint64
}

type frozenTextIdentityValues struct {
	source   documentv2.TextSource
	semantic snapshotTextSemanticEnvelope
	layout   snapshotTextLayoutInputEnvelope
}

type frozenTextIdentityPlan struct {
	values                    frozenTextIdentityValues
	canonicalSourceUpperBytes uint64
	textSemanticUpperBytes    uint64
	layoutInputUpperBytes     uint64
}

func makeFrozenTextIdentityValues(source documentv2.TextSource, fontContentHash string) frozenTextIdentityValues {
	return frozenTextIdentityValues{
		source: source,
		semantic: snapshotTextSemanticEnvelope{
			SchemaVersion: 1, Profile: documentv2.TextSemanticHashProfileV1,
			TextProfile: source.Profile, EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
			FontProfile: documentv2.TextFontProfileStaticGlyfV1, RunInputProfile: documentv2.TextRunInputProfileV1,
			LayoutProfile: documentv2.TextLayoutProfileUnwrappedV1, OutlineProfile: documentv2.TextOutlineProfileQ26_6V1,
			RasterProfile:                documentv2.TextRasterProfileSupersample8x8V1,
			PaintProfile:                 documentv2.TextPaintProfileRGBA16SRGBV1,
			CoverageHashProfile:          documentv2.TextCoverageHashProfileV1,
			LayoutInputHashProfile:       documentv2.TextLayoutInputHashProfileV1,
			UnicodeVersion:               documentv2.TextUnicodeDataVersionV1,
			UnicodeValidationProfile:     documentv2.TextUnicodeValidationTableProfileV1,
			UnicodeValidationHashProfile: documentv2.TextUnicodeValidationTableHashProfileV1,
			UnicodeValidationTableHash:   documentv2.TextUnicodeValidationTableHashV1,
			FontContentHash:              fontContentHash, Source: source,
		},
		layout: snapshotTextLayoutInputEnvelope{
			SchemaVersion: 1, Profile: documentv2.TextLayoutInputHashProfileV1,
			TextProfile: source.Profile, EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
			FontProfile: documentv2.TextFontProfileStaticGlyfV1, RunInputProfile: documentv2.TextRunInputProfileV1,
			LayoutProfile: documentv2.TextLayoutProfileUnwrappedV1, OutlineProfile: documentv2.TextOutlineProfileQ26_6V1,
			RasterProfile:                documentv2.TextRasterProfileSupersample8x8V1,
			CoverageHashProfile:          documentv2.TextCoverageHashProfileV1,
			UnicodeVersion:               documentv2.TextUnicodeDataVersionV1,
			UnicodeValidationProfile:     documentv2.TextUnicodeValidationTableProfileV1,
			UnicodeValidationHashProfile: documentv2.TextUnicodeValidationTableHashProfileV1,
			UnicodeValidationTableHash:   documentv2.TextUnicodeValidationTableHashV1,
			FontContentHash:              fontContentHash, Text: source.Text, FontSize: source.FontSize,
			Language: source.Language, Script: source.Script, Direction: source.Direction, Alignment: source.Alignment,
		},
	}
}

func buildFrozenTextIdentity(ctx context.Context, source documentv2.TextSource, fontContentHash string) (frozenTextIdentity, error) {
	plan, err := measureFrozenTextIdentity(ctx, source, fontContentHash)
	if err != nil {
		return frozenTextIdentity{}, err
	}
	return replayFrozenTextIdentity(ctx, plan)
}

func measureFrozenTextIdentity(ctx context.Context, source documentv2.TextSource, fontContentHash string) (frozenTextIdentityPlan, error) {
	if ctx == nil {
		return frozenTextIdentityPlan{}, frozenTextSourceError(CodeFrozenTextSourceInputInvalid, "frozen_text_source/identity/context", ErrFrozenTextSourceInputInvalid)
	}
	values := makeFrozenTextIdentityValues(source, fontContentHash)
	if source.LineHeight == nil || source.Viewport == nil {
		return frozenTextIdentityPlan{}, frozenTextSourceError(CodeFrozenTextSourceInvariant, "frozen_text_source/identity/source", ErrFrozenTextSourceInvariant)
	}
	values.layout.LineHeight = *source.LineHeight
	values.layout.Viewport = *source.Viewport

	sourceUpper, err := frozenTextCanonicalUpper(ctx, values.source)
	if err != nil {
		return frozenTextIdentityPlan{}, err
	}
	semanticUpper, err := frozenTextCanonicalUpper(ctx, values.semantic)
	if err != nil {
		return frozenTextIdentityPlan{}, err
	}
	layoutUpper, err := frozenTextCanonicalUpper(ctx, values.layout)
	if err != nil {
		return frozenTextIdentityPlan{}, err
	}
	return frozenTextIdentityPlan{
		values:                    values,
		canonicalSourceUpperBytes: sourceUpper,
		textSemanticUpperBytes:    semanticUpper,
		layoutInputUpperBytes:     layoutUpper,
	}, nil
}

func replayFrozenTextIdentity(ctx context.Context, plan frozenTextIdentityPlan) (frozenTextIdentity, error) {
	result := frozenTextIdentity{
		canonicalSourceUpperBytes: plan.canonicalSourceUpperBytes,
		textSemanticUpperBytes:    plan.textSemanticUpperBytes,
		layoutInputUpperBytes:     plan.layoutInputUpperBytes,
	}

	sourceRaw, err := frozenTextCanonicalJSON(ctx, "frozen_text_source/identity/source", plan.values.source)
	if err != nil {
		return frozenTextIdentity{}, err
	}
	result.canonicalSourceBytes = uint64(len(sourceRaw))
	result.canonicalSourceHash = canonicalDocumentHash(sourceRaw)
	sourceRaw = nil

	semanticRaw, err := frozenTextCanonicalJSON(ctx, "frozen_text_source/identity/semantic", plan.values.semantic)
	if err != nil {
		return frozenTextIdentity{}, err
	}
	result.textSemanticCanonicalBytes = uint64(len(semanticRaw))
	result.textSemanticHash = canonicalDocumentHash(semanticRaw)
	semanticRaw = nil

	layoutRaw, err := frozenTextCanonicalJSON(ctx, "frozen_text_source/identity/layout", plan.values.layout)
	if err != nil {
		return frozenTextIdentity{}, err
	}
	result.layoutInputCanonicalBytes = uint64(len(layoutRaw))
	result.layoutInputHash = canonicalDocumentHash(layoutRaw)
	if err := frozenTextContextFailure(ctx, "frozen_text_source/identity/result"); err != nil {
		return frozenTextIdentity{}, err
	}
	return result, nil
}

func frozenTextCanonicalJSON(ctx context.Context, path string, value any) ([]byte, error) {
	if err := frozenTextContextFailure(ctx, path+"/before"); err != nil {
		return nil, err
	}
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return nil, frozenTextSourceError(CodeFrozenTextSourceInvariant, path, errors.Join(ErrFrozenTextSourceInvariant, err))
	}
	if err := frozenTextContextFailure(ctx, path+"/after"); err != nil {
		return nil, err
	}
	return encoded, nil
}

func frozenTextCanonicalUpper(ctx context.Context, value any) (uint64, error) {
	measure := snapshotMeasure{ctx: ctx, nodeLimit: math.MaxUint64, byteLimit: math.MaxUint64}
	if err := measure.walk(reflect.ValueOf(value)); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, frozenTextCanceled("frozen_text_source/identity/upper", ctxErr)
		}
		return 0, frozenTextSourceError(CodeFrozenTextSourceInvariant, "frozen_text_source/identity/upper", errors.Join(ErrFrozenTextSourceInvariant, err))
	}
	return measure.encodedUpper, nil
}

func makeFrozenTextSourceRecord(
	ctx context.Context,
	documentHash string,
	documentCanonicalBytes uint64,
	asset documentv2.Asset,
	assetIndex int,
	font documentv2.Asset,
	fontIndex int,
	source documentv2.TextSource,
	identity frozenTextIdentity,
) (frozenTextSourceRecord, error) {
	if assetIndex < 0 || fontIndex < 0 || source.LineHeight == nil || source.Viewport == nil {
		return frozenTextSourceRecord{}, frozenTextSourceError(CodeFrozenTextSourceInvariant, "frozen_text_source/record/input", ErrFrozenTextSourceInvariant)
	}
	record := frozenTextSourceRecord{
		present:      true,
		documentHash: strings.Clone(documentHash), documentCanonicalBytes: documentCanonicalBytes,
		assetID: frozenTextCloneID(asset.ID), assetIndex: uint64(assetIndex),
		assetKind:        documentv2.AssetKind(strings.Clone(string(asset.Kind))),
		assetContentHash: strings.Clone(asset.ContentHash),
		textProfile:      strings.Clone(source.Profile), evaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
		text: strings.Clone(source.Text), fontAssetID: frozenTextCloneID(source.FontAssetID), fontAssetIndex: uint64(fontIndex),
		fontAssetKind: documentv2.AssetKind(strings.Clone(string(font.Kind))), fontContentHash: strings.Clone(font.ContentHash),
		fontProfile: documentv2.TextFontProfileStaticGlyfV1, runInputProfile: documentv2.TextRunInputProfileV1,
		layoutProfile: documentv2.TextLayoutProfileUnwrappedV1, outlineProfile: documentv2.TextOutlineProfileQ26_6V1,
		rasterProfile: documentv2.TextRasterProfileSupersample8x8V1, paintProfile: documentv2.TextPaintProfileRGBA16SRGBV1,
		coverageHashProfile: documentv2.TextCoverageHashProfileV1,
		fontSize:            source.FontSize, lineHeight: *source.LineHeight, viewport: *source.Viewport, fill: source.Fill,
		language: strings.Clone(source.Language), script: strings.Clone(source.Script),
		direction:                    documentv2.TextDirection(strings.Clone(string(source.Direction))),
		alignment:                    documentv2.TextAlignment(strings.Clone(string(source.Alignment))),
		unicodeVersion:               documentv2.TextUnicodeDataVersionV1,
		unicodeValidationProfile:     documentv2.TextUnicodeValidationTableProfileV1,
		unicodeValidationHashProfile: documentv2.TextUnicodeValidationTableHashProfileV1,
		unicodeValidationTableHash:   documentv2.TextUnicodeValidationTableHashV1,
		canonicalSourceHashProfile:   FrozenTextSourceCanonicalSourceHashProfileV1,
		canonicalSourceHash:          strings.Clone(identity.canonicalSourceHash), canonicalSourceBytes: identity.canonicalSourceBytes,
		canonicalSourceUpperBytes: identity.canonicalSourceUpperBytes,
		textSemanticHashProfile:   documentv2.TextSemanticHashProfileV1,
		textSemanticHash:          strings.Clone(identity.textSemanticHash), textSemanticCanonicalBytes: identity.textSemanticCanonicalBytes,
		textSemanticUpperBytes: identity.textSemanticUpperBytes,
		layoutInputHashProfile: documentv2.TextLayoutInputHashProfileV1,
		layoutInputHash:        strings.Clone(identity.layoutInputHash), layoutInputCanonicalBytes: identity.layoutInputCanonicalBytes,
		layoutInputUpperBytes: identity.layoutInputUpperBytes,
		bindingHashProfile:    FrozenTextSourceBindingHashProfileV1,
	}
	_, bindingBytes, err := hashFrozenTextSourceRecord(ctx, record)
	if err != nil {
		return frozenTextSourceRecord{}, err
	}
	record.bindingCanonicalBytes = bindingBytes
	var canonicalOK bool
	record.canonicalBinaryBytes, canonicalOK = frozenTextSum(
		record.canonicalSourceBytes,
		record.textSemanticCanonicalBytes,
		record.layoutInputCanonicalBytes,
		record.bindingCanonicalBytes,
	)
	if !canonicalOK || record.canonicalBinaryBytes == 0 {
		return frozenTextSourceRecord{}, frozenTextResourceError("frozen_text_source/record/canonical")
	}
	if err := finalizeFrozenTextSourceResources(&record); err != nil {
		return frozenTextSourceRecord{}, err
	}
	record.bindingHash, bindingBytes, err = hashFrozenTextSourceRecord(ctx, record)
	if err != nil {
		return frozenTextSourceRecord{}, err
	}
	if bindingBytes != record.bindingCanonicalBytes {
		return frozenTextSourceRecord{}, frozenTextSourceError(CodeFrozenTextSourceInvariant, "frozen_text_source/record/binding_bytes", ErrFrozenTextSourceInvariant)
	}
	return record, nil
}

func finalizeFrozenTextSourceResources(record *frozenTextSourceRecord) error {
	actual, ok := frozenTextSum(record.canonicalBinaryBytes, uint64(len(record.text)))
	if !ok {
		return frozenTextResourceError("frozen_text_source/record/work")
	}
	record.workUnits = actual
	upper, ok := frozenTextSum(record.canonicalSourceUpperBytes, record.textSemanticUpperBytes, record.layoutInputUpperBytes)
	if !ok || upper > math.MaxUint64/3 {
		return frozenTextResourceError("frozen_text_source/record/working")
	}
	working, ok := frozenTextSum(frozenTextFixedWorkingBytes, upper*3, record.bindingCanonicalBytes)
	if !ok {
		return frozenTextResourceError("frozen_text_source/record/working")
	}
	record.workingBytes = working
	stringsBytes, ok := frozenTextRecordStringBytes(*record)
	if !ok || stringsBytes > (math.MaxUint64-frozenTextFixedRetainedBytes)/2 {
		return frozenTextResourceError("frozen_text_source/record/retained")
	}
	record.retainedBytes = frozenTextFixedRetainedBytes + stringsBytes*2
	return nil
}

func frozenTextRecordStringBytes(record frozenTextSourceRecord) (uint64, bool) {
	values := []string{
		record.documentHash, string(record.assetID), string(record.assetKind), record.assetContentHash, record.textProfile, record.evaluatorProfile,
		record.text, string(record.fontAssetID), string(record.fontAssetKind), record.fontContentHash, record.fontProfile, record.runInputProfile,
		record.layoutProfile, record.outlineProfile, record.rasterProfile, record.paintProfile, record.coverageHashProfile,
		record.language, record.script, string(record.direction), string(record.alignment), record.unicodeVersion,
		record.unicodeValidationProfile, record.unicodeValidationHashProfile, record.unicodeValidationTableHash,
		record.canonicalSourceHashProfile, record.canonicalSourceHash, record.textSemanticHashProfile, record.textSemanticHash,
		record.layoutInputHashProfile, record.layoutInputHash, record.bindingHashProfile,
		FrozenTextSourceReceiptProfileV1, FrozenTextSourceProfileV1, FrozenTextSourceDescriptorHashProfileV1,
		FrozenTextSourceDescriptorHash(), FrozenTextSourceDocumentHashProfileV1,
		FrozenTextSourceBindingStatusV1, FrozenTextSourceCapabilityStatusV1,
	}
	var total uint64
	for _, value := range values {
		length := uint64(len(value))
		if length > math.MaxUint64-total {
			return 0, false
		}
		total += length
	}
	if uint64(len("sha256:"))+64 > math.MaxUint64-total {
		return 0, false
	}
	total += uint64(len("sha256:")) + 64
	return total, true
}

func sourceFromFrozenTextRecord(record frozenTextSourceRecord) documentv2.TextSource {
	lineHeight := record.lineHeight
	viewport := record.viewport
	return documentv2.TextSource{
		Profile: record.textProfile, Text: record.text, FontAssetID: record.fontAssetID,
		FontSize: record.fontSize, LineHeight: &lineHeight, Viewport: &viewport, Fill: record.fill,
		Language: record.language, Script: record.script, Direction: record.direction, Alignment: record.alignment,
	}
}

func frozenTextIdentityMatches(record frozenTextSourceRecord, identity frozenTextIdentity) bool {
	return record.canonicalSourceHash == identity.canonicalSourceHash &&
		record.canonicalSourceBytes == identity.canonicalSourceBytes &&
		record.canonicalSourceUpperBytes == identity.canonicalSourceUpperBytes &&
		record.textSemanticHash == identity.textSemanticHash &&
		record.textSemanticCanonicalBytes == identity.textSemanticCanonicalBytes &&
		record.textSemanticUpperBytes == identity.textSemanticUpperBytes &&
		record.layoutInputHash == identity.layoutInputHash &&
		record.layoutInputCanonicalBytes == identity.layoutInputCanonicalBytes &&
		record.layoutInputUpperBytes == identity.layoutInputUpperBytes
}

func hashFrozenTextSourceRecord(ctx context.Context, record frozenTextSourceRecord) (string, uint64, error) {
	if ctx == nil {
		return "", 0, frozenTextSourceError(CodeFrozenTextSourceInputInvalid, "frozen_text_source/hash/context", ErrFrozenTextSourceInputInvalid)
	}
	sink := frozenTextHashSink{ctx: ctx, hash: sha256.New(), path: "frozen_text_source/hash"}
	for _, value := range []string{
		FrozenTextSourceBindingHashProfileV1,
		FrozenTextSourceReceiptProfileV1,
		FrozenTextSourceProfileV1,
		FrozenTextSourceDescriptorHashProfileV1,
		FrozenTextSourceDescriptorHash(),
		FrozenTextSourceDocumentHashProfileV1,
		record.documentHash,
	} {
		if err := sink.string(value); err != nil {
			return "", 0, err
		}
	}
	sink.u32(uint32(FrozenTextSourceSchemaVersion))
	sink.u64(record.documentCanonicalBytes)
	for _, value := range []string{string(record.assetID), string(record.assetKind), record.assetContentHash} {
		if err := sink.string(value); err != nil {
			return "", 0, err
		}
	}
	sink.u64(record.assetIndex)
	for _, value := range []string{
		record.textProfile, record.evaluatorProfile, string(record.fontAssetID), string(record.fontAssetKind),
		record.fontContentHash, record.fontProfile, record.runInputProfile, record.layoutProfile,
		record.outlineProfile, record.rasterProfile, record.paintProfile, record.coverageHashProfile,
	} {
		if err := sink.string(value); err != nil {
			return "", 0, err
		}
	}
	sink.u64(record.fontAssetIndex)
	sink.i64(record.fontSize.Numerator)
	sink.i64(record.fontSize.Denominator)
	sink.i64(record.lineHeight.Numerator)
	sink.i64(record.lineHeight.Denominator)
	sink.u32(record.viewport.WidthPixels)
	sink.u32(record.viewport.HeightPixels)
	sink.u16(record.fill.R)
	sink.u16(record.fill.G)
	sink.u16(record.fill.B)
	sink.u16(record.fill.A)
	for _, value := range []string{
		record.language, record.script, string(record.direction), string(record.alignment), record.unicodeVersion,
		record.unicodeValidationProfile, record.unicodeValidationHashProfile, record.unicodeValidationTableHash,
		record.canonicalSourceHashProfile, record.canonicalSourceHash, record.textSemanticHashProfile,
		record.textSemanticHash, record.layoutInputHashProfile, record.layoutInputHash, record.bindingHashProfile,
		FrozenTextSourceBindingStatusV1, FrozenTextSourceCapabilityStatusV1,
	} {
		if err := sink.string(value); err != nil {
			return "", 0, err
		}
	}
	for _, value := range []uint64{
		record.canonicalSourceBytes, record.canonicalSourceUpperBytes,
		record.textSemanticCanonicalBytes, record.textSemanticUpperBytes,
		record.layoutInputCanonicalBytes, record.layoutInputUpperBytes,
		uint64(len(record.text)),
		record.bindingCanonicalBytes, record.canonicalBinaryBytes,
		record.workUnits, record.workingBytes, record.retainedBytes,
	} {
		sink.u64(value)
	}
	if sink.err != nil {
		return "", 0, sink.err
	}
	if err := frozenTextContextFailure(ctx, "frozen_text_source/hash/result"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.bytes, nil
}

type frozenTextHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	bytes uint64
	path  string
	err   error
}

func (sink *frozenTextHashSink) write(value []byte) error {
	if sink.err != nil {
		return sink.err
	}
	const chunk = 4096
	for len(value) > 0 {
		if err := frozenTextContextFailure(sink.ctx, sink.path); err != nil {
			return err
		}
		count := len(value)
		if count > chunk {
			count = chunk
		}
		_, _ = sink.hash.Write(value[:count])
		if !sink.addBytes(uint64(count)) {
			return sink.err
		}
		value = value[count:]
	}
	return nil
}

func (sink *frozenTextHashSink) string(value string) error {
	sink.u64(uint64(len(value)))
	if sink.err != nil {
		return sink.err
	}
	return sink.write([]byte(value))
}

func (sink *frozenTextHashSink) u16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
	sink.addBytes(uint64(len(encoded)))
}

func (sink *frozenTextHashSink) u32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
	sink.addBytes(uint64(len(encoded)))
}

func (sink *frozenTextHashSink) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = sink.hash.Write(encoded[:])
	sink.addBytes(uint64(len(encoded)))
}

func (sink *frozenTextHashSink) i64(value int64) { sink.u64(uint64(value)) }

func (sink *frozenTextHashSink) addBytes(count uint64) bool {
	if sink.err != nil {
		return false
	}
	if count > math.MaxUint64-sink.bytes {
		sink.err = frozenTextResourceError(sink.path + "/bytes")
		return false
	}
	sink.bytes += count
	return true
}

func frozenTextSum(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func frozenTextResourceError(path string) error {
	return frozenTextSourceError(CodeFrozenTextSourceResourceLimit, path, ErrFrozenTextSourceResourceLimit)
}

func frozenTextContextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return frozenTextSourceError(CodeFrozenTextSourceInputInvalid, path, ErrFrozenTextSourceInputInvalid)
	}
	if err := ctx.Err(); err != nil {
		return frozenTextCanceled(path, err)
	}
	return nil
}

func frozenTextCloneID(value documentv2.ID) documentv2.ID {
	return documentv2.ID(strings.Clone(string(value)))
}
