package evaluator

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// BindTextSource live-validates and replays one Text v2 source retained by CompileSnapshot. The
// asset ID and index must identify the same aligned record. Every failure returns a nil source and
// the strictly zero receipt.
func (snapshot *FrozenSnapshot) BindTextSource(
	ctx context.Context,
	assetID documentv2.ID,
	index int,
	limits FrozenTextSourceLimits,
) (result *FrozenTextSource, receipt FrozenTextSourceReceipt, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			receipt = FrozenTextSourceReceipt{}
			err = frozenTextSourceError(
				CodeFrozenTextSourceInvariant,
				"frozen_text_source/bind/panic",
				fmt.Errorf("%w: unexpected panic", ErrFrozenTextSourceInvariant),
			)
		}
		if err != nil {
			result = nil
			receipt = FrozenTextSourceReceipt{}
		}
	}()
	if ctx == nil {
		return nil, FrozenTextSourceReceipt{}, frozenTextSourceError(
			CodeFrozenTextSourceInputInvalid, "frozen_text_source/bind/context", ErrFrozenTextSourceInputInvalid,
		)
	}
	if err := frozenTextContextFailure(ctx, "frozen_text_source/bind/context"); err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	normalized, err := normalizeFrozenTextSourceLimits(limits)
	if err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	record, asset, err := inspectFrozenTextSource(snapshot, assetID, index)
	if err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	identityPlan, err := admitFrozenTextSource(ctx, record, normalized)
	if err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	rehashed, bindingBytes, err := hashFrozenTextSourceRecord(ctx, record)
	if err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	if rehashed != record.bindingHash || bindingBytes != record.bindingCanonicalBytes {
		return nil, FrozenTextSourceReceipt{}, frozenTextBindingError("frozen_text_source/bind/private_hash")
	}

	identity, err := replayFrozenTextIdentity(ctx, identityPlan)
	if err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	if !frozenTextIdentityMatches(record, identity) ||
		asset.CanonicalSourceBytes != identity.canonicalSourceBytes ||
		asset.TextSemanticHash != identity.textSemanticHash ||
		asset.TextLayoutInputHash != identity.layoutInputHash {
		return nil, FrozenTextSourceReceipt{}, frozenTextBindingError("frozen_text_source/bind/identity_replay")
	}
	if err := frozenTextContextFailure(ctx, "frozen_text_source/bind/publish"); err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	receipt = makeFrozenTextSourceReceipt(record)
	result = &FrozenTextSource{
		assetID: frozenTextCloneID(record.assetID), assetIndex: record.assetIndex,
		profile: strings.Clone(record.textProfile), text: strings.Clone(record.text),
		fontAssetID: frozenTextCloneID(record.fontAssetID), fontAssetIndex: record.fontAssetIndex,
		fontContentHash: strings.Clone(record.fontContentHash),
		fontSize:        record.fontSize, lineHeight: record.lineHeight, viewport: record.viewport, fill: record.fill,
		language: strings.Clone(record.language), script: strings.Clone(record.script),
		direction: documentv2.TextDirection(strings.Clone(string(record.direction))),
		alignment: documentv2.TextAlignment(strings.Clone(string(record.alignment))), receipt: receipt,
	}
	if err := frozenTextContextFailure(ctx, "frozen_text_source/bind/result"); err != nil {
		return nil, FrozenTextSourceReceipt{}, err
	}
	return result, receipt, nil
}

func inspectFrozenTextSource(snapshot *FrozenSnapshot, assetID documentv2.ID, index int) (frozenTextSourceRecord, SnapshotAsset, error) {
	if snapshot == nil || assetID == "" || index < 0 || index >= len(snapshot.assets) {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextSourceError(
			CodeFrozenTextSourceInputInvalid, "frozen_text_source/bind/selection", ErrFrozenTextSourceInputInvalid,
		)
	}
	if !frozenTextDigest(snapshot.documentHash) || snapshot.canonicalSize == 0 {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/snapshot")
	}
	if len(snapshot.textSourceSlots) != len(snapshot.assets) {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/slot_table")
	}
	slot := snapshot.textSourceSlots[index]
	if slot <= 0 || slot > len(snapshot.textSources) {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/slot")
	}
	asset := snapshot.assets[index]
	record := snapshot.textSources[slot-1]
	if asset.ID != assetID || asset.Index != index || asset.Kind != documentv2.AssetKindText ||
		!record.present || record.assetID != assetID || record.assetIndex != uint64(index) ||
		record.assetKind != documentv2.AssetKindText || record.assetContentHash != asset.ContentHash ||
		record.documentHash != snapshot.documentHash || record.documentCanonicalBytes != snapshot.canonicalSize {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/asset")
	}
	if record.fontAssetIndex >= uint64(len(snapshot.assets)) || record.fontAssetIndex > uint64(^uint(0)>>1) {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/font_index")
	}
	fontIndex := int(record.fontAssetIndex)
	font := snapshot.assets[fontIndex]
	if font.Index != fontIndex || font.ID != record.fontAssetID || font.Kind != documentv2.AssetKindFont ||
		record.fontAssetKind != documentv2.AssetKindFont || font.ContentHash != record.fontContentHash ||
		asset.TextFontAssetID != record.fontAssetID || asset.TextFontAssetIndex != fontIndex ||
		asset.TextFontContentHash != record.fontContentHash {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/font")
	}
	if !validFrozenTextProfiles(record, asset) || !validFrozenTextScalars(record) {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/profiles")
	}
	if asset.TextViewport != record.viewport || asset.CanonicalSourceBytes != record.canonicalSourceBytes ||
		asset.TextSemanticHash != record.textSemanticHash || asset.TextLayoutInputHash != record.layoutInputHash {
		return frozenTextSourceRecord{}, SnapshotAsset{}, frozenTextBindingError("frozen_text_source/bind/metadata")
	}
	return record, asset, nil
}

func validFrozenTextProfiles(record frozenTextSourceRecord, asset SnapshotAsset) bool {
	return record.textProfile == documentv2.TextProfilePinnedFontViewportPXV2 &&
		record.evaluatorProfile == documentv2.EvaluatorTextPinnedFontViewportPXV2 &&
		record.fontProfile == documentv2.TextFontProfileStaticGlyfV1 &&
		record.runInputProfile == documentv2.TextRunInputProfileV1 &&
		record.layoutProfile == documentv2.TextLayoutProfileUnwrappedV1 &&
		record.outlineProfile == documentv2.TextOutlineProfileQ26_6V1 &&
		record.rasterProfile == documentv2.TextRasterProfileSupersample8x8V1 &&
		record.paintProfile == documentv2.TextPaintProfileRGBA16SRGBV1 &&
		record.coverageHashProfile == documentv2.TextCoverageHashProfileV1 &&
		record.unicodeVersion == documentv2.TextUnicodeDataVersionV1 &&
		record.unicodeValidationProfile == documentv2.TextUnicodeValidationTableProfileV1 &&
		record.unicodeValidationHashProfile == documentv2.TextUnicodeValidationTableHashProfileV1 &&
		record.unicodeValidationTableHash == documentv2.TextUnicodeValidationTableHashV1 &&
		record.canonicalSourceHashProfile == FrozenTextSourceCanonicalSourceHashProfileV1 &&
		record.textSemanticHashProfile == documentv2.TextSemanticHashProfileV1 &&
		record.layoutInputHashProfile == documentv2.TextLayoutInputHashProfileV1 &&
		record.bindingHashProfile == FrozenTextSourceBindingHashProfileV1 &&
		asset.TextProfile == record.textProfile && asset.TextFontProfile == record.fontProfile &&
		asset.TextRunInputProfile == record.runInputProfile && asset.TextLayoutProfile == record.layoutProfile &&
		asset.TextOutlineProfile == record.outlineProfile && asset.TextRasterProfile == record.rasterProfile &&
		asset.TextPaintProfile == record.paintProfile && asset.TextCoverageHashProfile == record.coverageHashProfile &&
		asset.TextUnicodeVersion == record.unicodeVersion &&
		asset.TextUnicodeValidationProfile == record.unicodeValidationProfile &&
		asset.TextUnicodeValidationHashProfile == record.unicodeValidationHashProfile &&
		asset.TextUnicodeValidationTableHash == record.unicodeValidationTableHash &&
		asset.TextSemanticHashProfile == record.textSemanticHashProfile &&
		asset.TextLayoutInputHashProfile == record.layoutInputHashProfile
}

func validFrozenTextScalars(record frozenTextSourceRecord) bool {
	if !utf8.ValidString(record.text) || uint64(len(record.text)) > documentv2.TextMaxUTF8Bytes ||
		record.fontAssetID == "" || record.fontSize.Validate() != nil || record.fontSize.Numerator <= 0 ||
		record.lineHeight.Validate() != nil || record.lineHeight.Numerator <= 0 ||
		record.viewport.WidthPixels == 0 || record.viewport.WidthPixels > documentv2.RasterViewportMaxDimension ||
		record.viewport.HeightPixels == 0 || record.viewport.HeightPixels > documentv2.RasterViewportMaxDimension ||
		record.language == "" ||
		(record.script != documentv2.TextScriptLatin && record.script != documentv2.TextScriptHan && record.script != documentv2.TextScriptArabic) ||
		(record.direction != documentv2.TextDirectionLTR && record.direction != documentv2.TextDirectionRTL) ||
		(record.alignment != documentv2.TextAlignmentStart && record.alignment != documentv2.TextAlignmentCenter && record.alignment != documentv2.TextAlignmentEnd) ||
		!frozenTextDigest(record.documentHash) || !frozenTextDigest(record.fontContentHash) ||
		!frozenTextDigest(record.canonicalSourceHash) || !frozenTextDigest(record.textSemanticHash) ||
		!frozenTextDigest(record.layoutInputHash) || !frozenTextDigest(record.bindingHash) {
		return false
	}
	return record.canonicalSourceBytes != 0 && record.canonicalSourceUpperBytes >= record.canonicalSourceBytes &&
		record.textSemanticCanonicalBytes != 0 && record.textSemanticUpperBytes >= record.textSemanticCanonicalBytes &&
		record.layoutInputCanonicalBytes != 0 && record.layoutInputUpperBytes >= record.layoutInputCanonicalBytes &&
		record.bindingCanonicalBytes != 0 && record.canonicalBinaryBytes != 0 &&
		record.workUnits != 0 && record.workingBytes >= frozenTextFixedWorkingBytes &&
		record.retainedBytes >= frozenTextFixedRetainedBytes
}

func admitFrozenTextSource(ctx context.Context, record frozenTextSourceRecord, limits FrozenTextSourceLimits) (frozenTextIdentityPlan, error) {
	if uint64(len(record.text)) > limits.MaxTextBytes ||
		record.canonicalSourceBytes > limits.MaxCanonicalSourceBytes ||
		record.textSemanticCanonicalBytes > limits.MaxTextSemanticCanonicalBytes ||
		record.layoutInputCanonicalBytes > limits.MaxLayoutInputCanonicalBytes ||
		record.canonicalBinaryBytes > limits.MaxCanonicalBinaryBytes ||
		record.workUnits > limits.MaxWorkUnits || record.workingBytes > limits.MaxWorkingBytes ||
		record.retainedBytes > limits.MaxRetainedBytes {
		return frozenTextIdentityPlan{}, frozenTextResourceError("frozen_text_source/bind/pre_admission")
	}
	expectedCanonicalBytes, ok := frozenTextSum(
		record.canonicalSourceBytes,
		record.textSemanticCanonicalBytes,
		record.layoutInputCanonicalBytes,
		record.bindingCanonicalBytes,
	)
	if !ok || expectedCanonicalBytes != record.canonicalBinaryBytes {
		return frozenTextIdentityPlan{}, frozenTextBindingError("frozen_text_source/bind/canonical_total")
	}
	expectedWorkUnits, ok := frozenTextSum(record.canonicalBinaryBytes, uint64(len(record.text)))
	if !ok || expectedWorkUnits != record.workUnits {
		return frozenTextIdentityPlan{}, frozenTextBindingError("frozen_text_source/bind/work_total")
	}
	source := sourceFromFrozenTextRecord(record)
	plan, err := measureFrozenTextIdentity(ctx, source, record.fontContentHash)
	if err != nil {
		return frozenTextIdentityPlan{}, err
	}
	if plan.canonicalSourceUpperBytes != record.canonicalSourceUpperBytes ||
		plan.textSemanticUpperBytes != record.textSemanticUpperBytes ||
		plan.layoutInputUpperBytes != record.layoutInputUpperBytes {
		return frozenTextIdentityPlan{}, frozenTextBindingError("frozen_text_source/bind/replay_upper")
	}
	expected := record
	expected.workUnits = 0
	expected.workingBytes = 0
	expected.retainedBytes = 0
	if err := finalizeFrozenTextSourceResources(&expected); err != nil {
		return frozenTextIdentityPlan{}, err
	}
	if expected.workUnits != record.workUnits || expected.workingBytes != record.workingBytes ||
		expected.retainedBytes != record.retainedBytes {
		return frozenTextIdentityPlan{}, frozenTextBindingError("frozen_text_source/bind/resources")
	}
	if err := frozenTextContextFailure(ctx, "frozen_text_source/bind/pre_admission"); err != nil {
		return frozenTextIdentityPlan{}, err
	}
	return plan, nil
}

func makeFrozenTextSourceReceipt(record frozenTextSourceRecord) FrozenTextSourceReceipt {
	return FrozenTextSourceReceipt{
		SchemaVersion: FrozenTextSourceSchemaVersion,
		Profile:       FrozenTextSourceReceiptProfileV1, SourceProfile: FrozenTextSourceProfileV1,
		DescriptorHashProfile: FrozenTextSourceDescriptorHashProfileV1,
		DescriptorHash:        strings.Clone(FrozenTextSourceDescriptorHash()),
		BindingHashProfile:    strings.Clone(record.bindingHashProfile), BindingHash: strings.Clone(record.bindingHash),
		DocumentHashProfile: FrozenTextSourceDocumentHashProfileV1,
		DocumentHash:        strings.Clone(record.documentHash), DocumentCanonicalBytes: record.documentCanonicalBytes,
		AssetID: frozenTextCloneID(record.assetID), AssetIndex: record.assetIndex,
		AssetKind:        documentv2.AssetKind(strings.Clone(string(record.assetKind))),
		AssetContentHash: strings.Clone(record.assetContentHash),
		TextProfile:      strings.Clone(record.textProfile), EvaluatorProfile: strings.Clone(record.evaluatorProfile),
		FontAssetID: frozenTextCloneID(record.fontAssetID), FontAssetIndex: record.fontAssetIndex,
		FontAssetKind:   documentv2.AssetKind(strings.Clone(string(record.fontAssetKind))),
		FontContentHash: strings.Clone(record.fontContentHash), FontProfile: strings.Clone(record.fontProfile),
		RunInputProfile: strings.Clone(record.runInputProfile), LayoutProfile: strings.Clone(record.layoutProfile),
		OutlineProfile: strings.Clone(record.outlineProfile), RasterProfile: strings.Clone(record.rasterProfile),
		PaintProfile: strings.Clone(record.paintProfile), CoverageHashProfile: strings.Clone(record.coverageHashProfile),
		FontSizeNumerator: record.fontSize.Numerator, FontSizeDenominator: record.fontSize.Denominator,
		LineHeightNumerator: record.lineHeight.Numerator, LineHeightDenominator: record.lineHeight.Denominator,
		ViewportWidthPixels: record.viewport.WidthPixels, ViewportHeightPixels: record.viewport.HeightPixels,
		FillR: record.fill.R, FillG: record.fill.G, FillB: record.fill.B, FillA: record.fill.A,
		Language: strings.Clone(record.language), Script: strings.Clone(record.script),
		Direction:                    documentv2.TextDirection(strings.Clone(string(record.direction))),
		Alignment:                    documentv2.TextAlignment(strings.Clone(string(record.alignment))),
		UnicodeVersion:               strings.Clone(record.unicodeVersion),
		UnicodeValidationProfile:     strings.Clone(record.unicodeValidationProfile),
		UnicodeValidationHashProfile: strings.Clone(record.unicodeValidationHashProfile),
		UnicodeValidationTableHash:   strings.Clone(record.unicodeValidationTableHash),
		CanonicalSourceHashProfile:   strings.Clone(record.canonicalSourceHashProfile),
		CanonicalSourceHash:          strings.Clone(record.canonicalSourceHash), CanonicalSourceBytes: record.canonicalSourceBytes,
		CanonicalSourceUpperBytes: record.canonicalSourceUpperBytes,
		TextSemanticHashProfile:   strings.Clone(record.textSemanticHashProfile), TextSemanticHash: strings.Clone(record.textSemanticHash),
		TextSemanticCanonicalBytes: record.textSemanticCanonicalBytes, TextSemanticUpperBytes: record.textSemanticUpperBytes,
		LayoutInputHashProfile: strings.Clone(record.layoutInputHashProfile), LayoutInputHash: strings.Clone(record.layoutInputHash),
		LayoutInputCanonicalBytes: record.layoutInputCanonicalBytes, LayoutInputUpperBytes: record.layoutInputUpperBytes,
		BindingStatus: FrozenTextSourceBindingStatusV1, CapabilityStatus: FrozenTextSourceCapabilityStatusV1,
		TextBytes: uint64(len(record.text)), BindingCanonicalBytes: record.bindingCanonicalBytes,
		CanonicalBinaryBytes: record.canonicalBinaryBytes, WorkUnits: record.workUnits,
		WorkingBytes: record.workingBytes, RetainedBytes: record.retainedBytes,
	}
}

func frozenTextBindingError(path string) error {
	return frozenTextSourceError(CodeFrozenTextSourceBinding, path, ErrFrozenTextSourceBinding)
}

func frozenTextDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[7:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
