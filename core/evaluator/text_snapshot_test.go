package evaluator

import (
	"context"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestTextV2SnapshotBindsFontProfilesAndSeparateLayoutInputIdentity(t *testing.T) {
	baseDocument := textV2SnapshotDocument(t)
	base := compileTextV2Snapshot(t, baseDocument)
	const wantSemanticHash = "sha256:e2de6b4c1ed27b01a6053ac5bdd4b1d948888619980b4c985a427e456cc5efcf"
	const wantLayoutInputHash = "sha256:7499cb2d591a4a6c4c8f0386f7ebf121c425d822322057263a8dbb5f30426ab9"
	const wantCanonicalSourceBytes = 355
	if base.TextSemanticHash != wantSemanticHash || base.TextLayoutInputHash != wantLayoutInputHash || base.CanonicalSourceBytes != wantCanonicalSourceBytes {
		t.Fatalf("frozen Text v2 identities drifted: %+v", base)
	}
	textSource := baseDocument.Assets[4].Text
	encoded, err := videoedit.CanonicalJSON(*textSource)
	if err != nil {
		t.Fatal(err)
	}
	if base.TextProfile != documentv2.TextProfilePinnedFontViewportPXV2 ||
		base.TextViewport != *textSource.Viewport || base.TextFontAssetID != textSource.FontAssetID || base.TextFontAssetIndex != 3 ||
		base.TextFontContentHash != baseDocument.Assets[3].ContentHash || base.CanonicalSourceBytes != uint64(len(encoded)) ||
		base.TextFontProfile != documentv2.TextFontProfileStaticGlyfV1 || base.TextRunInputProfile != documentv2.TextRunInputProfileV1 ||
		base.TextLayoutProfile != documentv2.TextLayoutProfileUnwrappedV1 || base.TextOutlineProfile != documentv2.TextOutlineProfileQ26_6V1 ||
		base.TextRasterProfile != documentv2.TextRasterProfileSupersample8x8V1 || base.TextPaintProfile != documentv2.TextPaintProfileRGBA16SRGBV1 ||
		base.TextCoverageHashProfile != documentv2.TextCoverageHashProfileV1 ||
		base.TextUnicodeVersion != documentv2.TextUnicodeDataVersionV1 || base.TextSemanticHashProfile != documentv2.TextSemanticHashProfileV1 ||
		base.TextUnicodeValidationProfile != documentv2.TextUnicodeValidationTableProfileV1 ||
		base.TextUnicodeValidationHashProfile != documentv2.TextUnicodeValidationTableHashProfileV1 ||
		base.TextUnicodeValidationTableHash != documentv2.TextUnicodeValidationTableHashV1 ||
		base.TextLayoutInputHashProfile != documentv2.TextLayoutInputHashProfileV1 || !snapshotDigest(base.TextSemanticHash) ||
		!snapshotDigest(base.TextLayoutInputHash) {
		t.Fatalf("text snapshot metadata=%+v", base)
	}

	fillDocument := textV2SnapshotDocument(t)
	fillDocument.Assets[4].Text.Fill = documentv2.ColorRGBA16{B: 65_535, A: 32_768}
	fill := compileTextV2Snapshot(t, fillDocument)
	if fill.TextSemanticHash == base.TextSemanticHash || fill.TextLayoutInputHash != base.TextLayoutInputHash {
		t.Fatalf("fill must change full semantic identity only: base=%+v fill=%+v", base, fill)
	}

	fontDocument := textV2SnapshotDocument(t)
	fontDocument.Assets[3].ContentHash = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	font := compileTextV2Snapshot(t, fontDocument)
	if font.TextSemanticHash == base.TextSemanticHash || font.TextLayoutInputHash == base.TextLayoutInputHash {
		t.Fatalf("font content hash did not bind both identities: base=%+v font=%+v", base, font)
	}

	textDocument := textV2SnapshotDocument(t)
	textDocument.Assets[4].Text.Text = "Agent"
	text := compileTextV2Snapshot(t, textDocument)
	if text.TextSemanticHash == base.TextSemanticHash || text.TextLayoutInputHash == base.TextLayoutInputHash {
		t.Fatalf("text bytes did not bind both identities: base=%+v text=%+v", base, text)
	}

	nfcDocument := textV2SnapshotDocument(t)
	nfcDocument.Assets[4].Text.Text = "é"
	nfc := compileTextV2Snapshot(t, nfcDocument)
	nfdDocument := textV2SnapshotDocument(t)
	nfdDocument.Assets[4].Text.Text = "e\u0301"
	nfd := compileTextV2Snapshot(t, nfdDocument)
	if nfc.TextSemanticHash == nfd.TextSemanticHash || nfc.TextLayoutInputHash == nfd.TextLayoutInputHash {
		t.Fatalf("normalization-distinct UTF-8 collapsed: NFC=%+v NFD=%+v", nfc, nfd)
	}

	fontIDDocument := textV2SnapshotDocument(t)
	fontIDDocument.Assets[3].ID = "renamed-font"
	fontIDDocument.Assets[4].Text.FontAssetID = "renamed-font"
	fontID := compileTextV2Snapshot(t, fontIDDocument)
	if fontID.TextSemanticHash == base.TextSemanticHash || fontID.TextLayoutInputHash != base.TextLayoutInputHash {
		t.Fatalf("font identity must affect full source identity but not content-bound layout input: base=%+v renamed=%+v", base, fontID)
	}

	fontURIDocument := textV2SnapshotDocument(t)
	fontURIDocument.Assets[3].URI = "mirror/pinned-font.ttf"
	fontURI := compileTextV2Snapshot(t, fontURIDocument)
	if fontURI.TextSemanticHash != base.TextSemanticHash || fontURI.TextLayoutInputHash != base.TextLayoutInputHash {
		t.Fatalf("locator leaked into content-bound text identity: base=%+v URI=%+v", base, fontURI)
	}

	for _, test := range []struct {
		name   string
		mutate func(*documentv2.Document)
	}{
		{name: "font size", mutate: func(document *documentv2.Document) { document.Assets[4].Text.FontSize.Numerator++ }},
		{name: "line height", mutate: func(document *documentv2.Document) { document.Assets[4].Text.LineHeight.Numerator++ }},
		{name: "viewport", mutate: func(document *documentv2.Document) { document.Assets[4].Text.Viewport.WidthPixels++ }},
		{name: "language", mutate: func(document *documentv2.Document) { document.Assets[4].Text.Language = "fr" }},
		{name: "direction", mutate: func(document *documentv2.Document) { document.Assets[4].Text.Direction = documentv2.TextDirectionRTL }},
		{name: "alignment", mutate: func(document *documentv2.Document) { document.Assets[4].Text.Alignment = documentv2.TextAlignmentEnd }},
	} {
		t.Run("layout field "+test.name, func(t *testing.T) {
			document := textV2SnapshotDocument(t)
			test.mutate(&document)
			changed := compileTextV2Snapshot(t, document)
			if changed.TextSemanticHash == base.TextSemanticHash || changed.TextLayoutInputHash == base.TextLayoutInputHash {
				t.Fatalf("%s did not bind both identities: base=%+v changed=%+v", test.name, base, changed)
			}
		})
	}

	latinNeutralDocument := textV2SnapshotDocument(t)
	latinNeutralDocument.Assets[4].Text.Text = "123"
	latinNeutral := compileTextV2Snapshot(t, latinNeutralDocument)
	hanNeutralDocument := textV2SnapshotDocument(t)
	hanNeutralDocument.Assets[4].Text.Text = "123"
	hanNeutralDocument.Assets[4].Text.Script = documentv2.TextScriptHan
	hanNeutral := compileTextV2Snapshot(t, hanNeutralDocument)
	if latinNeutral.TextSemanticHash == hanNeutral.TextSemanticHash || latinNeutral.TextLayoutInputHash == hanNeutral.TextLayoutInputHash {
		t.Fatalf("explicit script did not bind both identities: Latin=%+v Han=%+v", latinNeutral, hanNeutral)
	}

	legacyDocument := validDocument(t)
	legacy := compileTextV2Snapshot(t, legacyDocument)
	if legacy.TextProfile != "" || legacy.CanonicalSourceBytes != 0 || legacy.TextFontAssetIndex != -1 ||
		legacy.TextFontAssetID != "" || legacy.TextFontContentHash != "" || legacy.TextSemanticHash != "" || legacy.TextLayoutInputHash != "" {
		t.Fatalf("legacy snapshot claimed or lost text identity: %+v", legacy)
	}
	program, snapshot, diagnostics := CompileSnapshot(context.Background(), legacyDocument, snapshotOffered(t, legacyDocument), SnapshotLimits{
		MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20,
	})
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) || snapshot.Assets()[0].TextFontAssetIndex != -1 {
		t.Fatalf("non-text sentinel program=%v snapshot=%+v diagnostics=%+v", program, snapshot, diagnostics)
	}
}

func TestTextV2SnapshotOwnsSourceAndReturnedMetadata(t *testing.T) {
	document := textV2SnapshotDocument(t)
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}
	program, snapshot, diagnostics := CompileSnapshot(context.Background(), document, snapshotOffered(t, document), limits)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
	before := snapshot.Assets()[4]
	document.Assets[3].ContentHash = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	document.Assets[4].Text.Text = "mutated"
	document.Assets[4].Text.Fill = documentv2.ColorRGBA16{R: 1, A: 1}
	document.Assets[4].Text.LineHeight.Numerator++
	document.Assets[4].Text.Viewport.WidthPixels++
	if after := snapshot.Assets()[4]; !reflect.DeepEqual(after, before) {
		t.Fatalf("published snapshot aliases source document: before=%+v after=%+v", before, after)
	}

	returned := snapshot.Assets()
	returned[4].TextFontContentHash = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	returned[4].TextViewport.WidthPixels++
	returned[4].TextSemanticHash = "mutated"
	returned[4].TextLayoutInputHash = "mutated"
	if after := snapshot.Assets()[4]; !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot Assets result aliases internal metadata: before=%+v after=%+v", before, after)
	}
}

func TestSnapshotMeasureOmitsAdditiveZeroTextFieldsLikeCanonicalJSON(t *testing.T) {
	type legacyTextSourceWire struct {
		Text        string                   `json:"text"`
		FontAssetID documentv2.ID            `json:"font_asset_id"`
		FontSize    documentv2.Rational      `json:"font_size"`
		Fill        documentv2.ColorRGBA16   `json:"fill"`
		Language    string                   `json:"language"`
		Direction   documentv2.TextDirection `json:"direction"`
		Alignment   documentv2.TextAlignment `json:"alignment"`
	}
	legacy := legacyTextSourceWire{
		Text: "Cutly", FontAssetID: "font", FontSize: documentv2.Rational{Numerator: 48, Denominator: 1},
		Fill: documentv2.ColorRGBA16{A: 65_535}, Language: "en",
		Direction: documentv2.TextDirectionLTR, Alignment: documentv2.TextAlignmentStart,
	}
	additive := documentv2.TextSource{
		Text: legacy.Text, FontAssetID: legacy.FontAssetID, FontSize: legacy.FontSize, Fill: legacy.Fill,
		Language: legacy.Language, Direction: legacy.Direction, Alignment: legacy.Alignment,
	}
	legacyRaw, err := videoedit.CanonicalJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	additiveRaw, err := videoedit.CanonicalJSON(additive)
	if err != nil || string(additiveRaw) != string(legacyRaw) {
		t.Fatalf("legacy bytes changed: legacy=%s additive=%s err=%v", legacyRaw, additiveRaw, err)
	}
	measure := func(value any) snapshotMeasure {
		result := snapshotMeasure{ctx: context.Background(), nodeLimit: 1 << 20, byteLimit: 1 << 20}
		if err := result.walk(reflect.ValueOf(value)); err != nil {
			t.Fatal(err)
		}
		return result
	}
	legacyMeasure := measure(legacy)
	additiveMeasure := measure(additive)
	if legacyMeasure.encodedUpper != additiveMeasure.encodedUpper || legacyMeasure.nodes != additiveMeasure.nodes {
		t.Fatalf("omitempty fields changed legacy admission: legacy=%+v additive=%+v", legacyMeasure, additiveMeasure)
	}

	legacyDocument := validDocument(t)
	documentMeasure := snapshotMeasure{ctx: context.Background(), nodeLimit: ^uint64(0), byteLimit: ^uint64(0)}
	if err := documentMeasure.walk(reflect.ValueOf(legacyDocument)); err != nil {
		t.Fatal(err)
	}
	exact := SnapshotLimits{MaxCanonicalDocumentBytes: documentMeasure.encodedUpper, MaxDocumentNodes: documentMeasure.nodes + 1}
	program, snapshot, diagnostics := CompileSnapshot(context.Background(), legacyDocument, snapshotOffered(t, legacyDocument), exact)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("legacy document rejected at exact preflight boundary: program=%v snapshot=%v diagnostics=%+v measure=%+v", program, snapshot, diagnostics, documentMeasure)
	}
	exact.MaxCanonicalDocumentBytes--
	program, snapshot, diagnostics = CompileSnapshot(context.Background(), legacyDocument, snapshotOffered(t, legacyDocument), exact)
	if program != nil || snapshot != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("legacy document accepted below exact preflight boundary: program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
}

func TestTextV2SnapshotCancellationPublishesNothing(t *testing.T) {
	document := textV2SnapshotDocument(t)
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshot, err := buildFrozenSnapshot(ctx, document, hash, uint64(len(canonical))); snapshot != nil || err == nil {
		t.Fatalf("canceled text snapshot=%v err=%v", snapshot, err)
	}
}

func compileTextV2Snapshot(t *testing.T, document documentv2.Document) SnapshotAsset {
	t.Helper()
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}
	program, snapshot, diagnostics := CompileSnapshot(context.Background(), document, snapshotOffered(t, document), limits)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
	for _, asset := range snapshot.Assets() {
		if asset.Kind == documentv2.AssetKindText {
			return asset
		}
	}
	t.Fatal("text snapshot missing")
	return SnapshotAsset{}
}

func textV2SnapshotDocument(t *testing.T) documentv2.Document {
	t.Helper()
	document := validDocument(t)
	lineHeight := documentv2.Rational{Numerator: 60, Denominator: 1}
	text := document.Assets[4].Text
	text.Profile = documentv2.TextProfilePinnedFontViewportPXV2
	text.Text = "Cutly"
	text.Language = "en"
	text.Script = documentv2.TextScriptLatin
	text.LineHeight = &lineHeight
	text.Viewport = &documentv2.RasterViewport{WidthPixels: 640, HeightPixels: 180}
	return document
}

func snapshotDigest(value string) bool {
	return len(value) == 71 && value[:7] == "sha256:"
}
