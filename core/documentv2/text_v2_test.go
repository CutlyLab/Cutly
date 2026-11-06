package documentv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestTextPinnedFontViewportV2ValidationAndCapabilities(t *testing.T) {
	for _, test := range []struct {
		name      string
		script    string
		direction TextDirection
		text      string
	}{
		{name: "latin", script: TextScriptLatin, direction: TextDirectionLTR, text: "Cutly fi e\u0301"},
		{name: "han", script: TextScriptHan, direction: TextDirectionLTR, text: "剪辑，文本"},
		{name: "arabic", script: TextScriptArabic, direction: TextDirectionRTL, text: "العَرَبِيَّة"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := textV2Document(test.text, test.script, test.direction)
			assertNoDiagnostics(t, Validate(document))
			required, diagnostics := RequiredCapabilities(document)
			if HasErrorDiagnostics(diagnostics) ||
				!slices.Equal(required.TrackKinds, []TrackKind{TrackKindText}) ||
				!slices.Equal(required.AssetKinds, []AssetKind{AssetKindFont, AssetKindText}) ||
				!slices.Equal(required.Features, []videoedit.CapabilityID{CapabilityTextPinnedFontViewportPXV2}) {
				t.Fatalf("requirements=%+v diagnostics=%+v", required, diagnostics)
			}
			offered := videoedit.BackendCapabilities{
				BackendID:  "text-v2-test",
				TrackKinds: []videoedit.TrackKind{videoedit.TrackKind(TrackKindText)},
				AssetKinds: []videoedit.AssetKind{videoedit.AssetKind(AssetKindFont), videoedit.AssetKind(AssetKindText)},
				Features:   []videoedit.CapabilityID{CapabilityTextPinnedFontViewportPXV2},
			}
			if diagnostics := PreflightCapabilities(document, offered); HasErrorDiagnostics(diagnostics) {
				t.Fatalf("matching backend rejected: %+v", diagnostics)
			}
			offered.Features = []videoedit.CapabilityID{CapabilityTextV1}
			if diagnostics := PreflightCapabilities(document, offered); !HasErrorDiagnostics(diagnostics) {
				t.Fatalf("legacy text capability accepted Text v2: %+v", diagnostics)
			}
		})
	}

	document := textV2Document("Cutly", TextScriptLatin, TextDirectionLTR)
	fill := ColorRGBA16{R: 1, G: 2, B: 3, A: 65_535}
	document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes = []KeyframeLane{{
		ID: "fill-lane", Target: KeyframeTargetTextFill, ValueType: KeyframeValueColor,
		Keyframes: []Keyframe{{Offset: RationalTime{Value: 0, Rate: 1}, Value: KeyframeValue{Color: &fill}}},
	}}
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) || !slices.Equal(required.Features, []videoedit.CapabilityID{
		CapabilityKeyframesV1, CapabilityTextPinnedFontViewportPXV2,
	}) {
		t.Fatalf("text-fill requirements=%+v diagnostics=%+v", required, diagnostics)
	}
}

func TestTextPinnedFontViewportV2RejectsAmbiguousOrUnboundedSources(t *testing.T) {
	tests := []struct {
		name string
		path string
		edit func(*TextSource)
	}{
		{name: "explicit legacy alias", path: "/assets/1/text/profile", edit: func(value *TextSource) { value.Profile = string(CapabilityTextV1) }},
		{name: "unknown profile", path: "/assets/1/text/profile", edit: func(value *TextSource) { value.Profile = "vendor.text.v9" }},
		{name: "missing line height", path: "/assets/1/text/line_height", edit: func(value *TextSource) { value.LineHeight = nil }},
		{name: "zero line height", path: "/assets/1/text/line_height", edit: func(value *TextSource) { value.LineHeight = &Rational{Numerator: 0, Denominator: 1} }},
		{name: "missing viewport", path: "/assets/1/text/viewport", edit: func(value *TextSource) { value.Viewport = nil }},
		{name: "zero viewport", path: "/assets/1/text/viewport/width_pixels", edit: func(value *TextSource) { value.Viewport.WidthPixels = 0 }},
		{name: "oversize viewport", path: "/assets/1/text/viewport/height_pixels", edit: func(value *TextSource) { value.Viewport.HeightPixels = RasterViewportMaxDimension + 1 }},
		{name: "missing script", path: "/assets/1/text/script", edit: func(value *TextSource) { value.Script = "" }},
		{name: "unknown script", path: "/assets/1/text/script", edit: func(value *TextSource) { value.Script = "Cyrl" }},
		{name: "mixed script", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cutly文本" }},
		{name: "unicode format", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cut\u200dly" }},
		{name: "default ignorable", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cut\u034fly" }},
		{name: "BMP variation selector", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cutly\ufe0f" }},
		{name: "supplement variation selector", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cutly\U000e0100" }},
		{name: "C0 control", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cutly\tAgent" }},
		{name: "host line separator", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = "Cutly\u2028next" }},
		{name: "too many bytes", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = strings.Repeat("a", TextMaxUTF8Bytes+1) }},
		{name: "too many lines", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = strings.Repeat("\n", TextMaxLines) }},
		{name: "too many scalars", path: "/assets/1/text/text", edit: func(value *TextSource) { value.Text = strings.Repeat("a", TextMaxScalarsPerLine+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := textV2Document("Cutly", TextScriptLatin, TextDirectionLTR)
			test.edit(document.Assets[1].Text)
			assertDiagnostic(t, Validate(document), CodeAssetInvalid, test.path)
		})
	}

	t.Run("invalid utf8", func(t *testing.T) {
		document := textV2Document("Cutly", TextScriptLatin, TextDirectionLTR)
		document.Assets[1].Text.Text = string([]byte{'b', 0xff, 'd'})
		assertDiagnostic(t, Validate(document), CodeJSONInvalid, "/assets/1/text/text")
		if _, err := CanonicalJSON(document); !errors.Is(err, ErrDocumentInvalid) {
			t.Fatalf("canonical invalid UTF-8 error=%v", err)
		}
	})

	t.Run("legacy resource admission is unchanged", func(t *testing.T) {
		for name, text := range map[string]string{
			"bytes":        strings.Repeat("a", TextMaxUTF8Bytes+1),
			"lines":        strings.Repeat("\n", TextMaxLines),
			"line scalars": strings.Repeat("a", TextMaxScalarsPerLine+1),
		} {
			t.Run(name, func(t *testing.T) {
				legacy := fixtureDocument(t)
				legacy.Assets[4].Text.Text = text
				assertNoDiagnostics(t, Validate(legacy))
			})
		}
	})

	t.Run("exact resource boundaries", func(t *testing.T) {
		for name, text := range map[string]string{
			"line scalars": strings.Repeat("a", TextMaxScalarsPerLine),
			"line count":   strings.Repeat("\n", TextMaxLines-1),
			"UTF-8 bytes":  strings.Repeat(strings.Repeat("a", 65_535)+"\n", 15) + strings.Repeat("a", 65_536),
		} {
			t.Run(name, func(t *testing.T) {
				if len(text) > TextMaxUTF8Bytes {
					t.Fatalf("bad boundary fixture length=%d", len(text))
				}
				document := textV2Document(text, TextScriptLatin, TextDirectionLTR)
				assertNoDiagnostics(t, Validate(document))
			})
		}
	})

	t.Run("legacy spelling rejects v2 members and keeps old bytes", func(t *testing.T) {
		legacy := fixtureDocument(t)
		canonical, err := CanonicalJSON(legacy)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range [][]byte{[]byte(`"profile":"cutly.text`), []byte(`"line_height"`), []byte(`"viewport"`), []byte(`"script"`)} {
			if bytes.Contains(canonical, forbidden) {
				t.Fatalf("legacy canonical bytes acquired Text v2 member %q", forbidden)
			}
		}
		legacy.Assets[4].Text.Script = TextScriptLatin
		assertDiagnostic(t, Validate(legacy), CodeAssetInvalid, "/assets/4/text/script")
	})
}

func TestTextPinnedFontViewportV2CanonicalFixturesAndDescriptor(t *testing.T) {
	descriptor := TextPinnedFontViewportDescriptor()
	if descriptor.Profile != TextProfilePinnedFontViewportPXV2 || descriptor.RequiredCapability != CapabilityTextPinnedFontViewportPXV2 ||
		descriptor.EvaluatorProfile != EvaluatorTextPinnedFontViewportPXV2 || descriptor.FontProfile != TextFontProfileStaticGlyfV1 ||
		descriptor.RunInputProfile != TextRunInputProfileV1 || descriptor.ShapingProfile != "" || descriptor.LayoutProfile != TextLayoutProfileUnwrappedV1 ||
		descriptor.OutlineProfile != TextOutlineProfileQ26_6V1 || descriptor.RasterProfile != TextRasterProfileSupersample8x8V1 ||
		descriptor.PaintProfile != TextPaintProfileRGBA16SRGBV1 ||
		descriptor.CoverageHashProfile != TextCoverageHashProfileV1 || descriptor.SemanticHashProfile != TextSemanticHashProfileV1 ||
		descriptor.SemanticHashSemantics == "" ||
		descriptor.LayoutInputHashProfile != TextLayoutInputHashProfileV1 || descriptor.UnicodeVersion != TextUnicodeDataVersionV1 ||
		descriptor.UnicodeValidationProfile != TextUnicodeValidationTableProfileV1 ||
		descriptor.UnicodeValidationHashProfile != TextUnicodeValidationTableHashProfileV1 ||
		descriptor.UnicodeValidationTableHash != TextUnicodeValidationTableHashV1 || descriptor.UnicodeValidationProvenance == "" ||
		descriptor.MaxUTF8Bytes != TextMaxUTF8Bytes || descriptor.MaxLines != TextMaxLines ||
		descriptor.MaxScalarsPerLine != TextMaxScalarsPerLine || descriptor.MaxViewportDimension != RasterViewportMaxDimension {
		t.Fatalf("incomplete descriptor: %+v", descriptor)
	}
	contractText := descriptor.ValueSemantics + " " + descriptor.RunInputSemantics + " " + descriptor.ExcludedResponsibilities
	for _, phrase := range []string{
		"without Unicode normalization", "byte offsets into the original complete UTF-8", "BOT|EOT", "MONOTONE_CHARACTERS",
		"nearest-even", "hhea.ascender", "never automatically wrapped", "alignment advance box", "clip all coverage",
		"YAdvance=0", "viewportY=baselineY-scaledYOffset-scaledOutlineY", "GSUB/GPOS algorithm", "hinting",
	} {
		if !strings.Contains(contractText, phrase) {
			t.Errorf("descriptor does not freeze %q", phrase)
		}
	}
	hashContract := descriptor.SemanticHashSemantics + " " + descriptor.LayoutInputHashSemantics
	for _, phrase := range []string{"complete TextSource", "FontAssetID and fill", "pinned font content hash", "Exclude fill, paint, FontAssetID", "not by itself a final shaped-glyph"} {
		if !strings.Contains(hashContract, phrase) {
			t.Errorf("descriptor hash contract does not freeze %q", phrase)
		}
	}
	for _, vector := range []struct {
		name         string
		value        any
		canonicalSHA string
	}{
		{name: "text-pinned-font-viewport-v2.descriptor.canonical.json", value: descriptor, canonicalSHA: "sha256:8148d0e2d895de39bb0696f1f58869f85f71e034650e12617c9b72fcbbf81c16"},
		{name: "text-pinned-font-viewport-v2.document.canonical.json", value: textV2Document("Cutly\nAgent", TextScriptLatin, TextDirectionLTR), canonicalSHA: "sha256:12c272cee65e2f0a4d2d6a4104ee17b84fba1568960c9655a0502d0d28ebbb6f"},
	} {
		got, err := videoedit.CanonicalJSON(vector.value)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("testdata/" + vector.name)
		if err != nil {
			t.Fatal(err)
		}
		want = bytes.TrimSpace(want)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s drift\nwant=%s\ngot=%s", vector.name, want, got)
		}
		digest := sha256.Sum256(got)
		if gotHash := "sha256:" + hex.EncodeToString(digest[:]); gotHash != vector.canonicalSHA {
			t.Fatalf("%s canonical hash=%q, want %q", vector.name, gotHash, vector.canonicalSHA)
		}
	}

	documentRaw, err := os.ReadFile("testdata/text-pinned-font-viewport-v2.document.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	documentRaw = bytes.TrimSpace(documentRaw)
	hostileUTF8 := bytes.Replace(documentRaw, []byte("Cutly"), []byte{'C', 0xff, 't'}, 1)
	if bytes.Equal(hostileUTF8, documentRaw) {
		t.Fatal("invalid UTF-8 fixture replacement did not match")
	}
	if _, err := Decode(hostileUTF8); !errors.Is(err, ErrDocumentInvalid) || !strings.Contains(strings.ToLower(err.Error()), "invalid unicode encoding") {
		t.Fatalf("raw invalid UTF-8 decode error=%v", err)
	}
	decoded, err := Decode(documentRaw)
	if err != nil {
		t.Fatalf("strict fixture decode: %v", err)
	}
	roundTrip, err := CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(roundTrip, documentRaw) {
		t.Fatalf("fixture roundtrip err=%v", err)
	}

	descriptorRaw, err := os.ReadFile("testdata/text-pinned-font-viewport-v2.descriptor.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var decodedDescriptor SourceDescriptor
	if err := wirejson.Decode(bytes.TrimSpace(descriptorRaw), &decodedDescriptor); err != nil || decodedDescriptor.Profile != descriptor.Profile {
		t.Fatalf("strict descriptor decode=%+v err=%v", decodedDescriptor, err)
	}

	combined := SourceDescriptorsWithTextV2()
	if len(combined) != len(SourceDescriptors())+1 {
		t.Fatalf("combined source descriptors=%+v", combined)
	}
	found := false
	for index, item := range combined {
		if index > 0 && combined[index-1].Profile >= item.Profile {
			t.Fatalf("combined descriptors not sorted: %+v", combined)
		}
		found = found || item.Profile == TextProfilePinnedFontViewportPXV2
	}
	if !found {
		t.Fatal("Text v2 descriptor missing from additive discovery")
	}
	combined[0].RequiredAssetKinds[0] = AssetKindData
	if TextPinnedFontViewportDescriptor().RequiredAssetKinds[0] != AssetKindFont {
		t.Fatal("additive descriptor aliases caller storage")
	}
	lookup, ok := LookupSourceDescriptor(TextProfilePinnedFontViewportPXV2)
	if !ok || lookup.Profile != descriptor.Profile || lookup.UnicodeVersion != TextUnicodeDataVersionV1 {
		t.Fatalf("additive lookup=%+v ok=%v", lookup, ok)
	}
	lookup.RequiredAssetKinds[0] = AssetKindData
	lookupAgain, ok := LookupSourceDescriptor(TextProfilePinnedFontViewportPXV2)
	if !ok || lookupAgain.RequiredAssetKinds[0] != AssetKindFont {
		t.Fatal("additive lookup returned registry-owned slices")
	}
	for _, token := range []string{
		descriptor.Profile, descriptor.EvaluatorProfile, descriptor.FontProfile, descriptor.RunInputProfile,
		descriptor.LayoutProfile, descriptor.OutlineProfile, descriptor.RasterProfile, descriptor.PaintProfile,
		descriptor.CoverageHashProfile, descriptor.SemanticHashProfile, descriptor.LayoutInputHashProfile,
		descriptor.UnicodeValidationProfile, descriptor.UnicodeValidationHashProfile,
	} {
		if !reservedSemanticToken(token) {
			t.Errorf("Text v2 semantic token is not reserved: %q", token)
		}
	}
}

func TestSemanticDescriptorCatalogV3IncludesTextV2AndIsFrozen(t *testing.T) {
	catalog := SemanticDescriptorsV3()
	if catalog.SchemaVersion != DescriptorCatalogVersionV3 || len(catalog.Sources) != len(SourceDescriptors())+1 || len(catalog.TransitionScopes) != 1 {
		t.Fatalf("Text v2 catalog is incomplete: %+v", catalog)
	}
	textIndex := -1
	for index, descriptor := range catalog.Sources {
		if descriptor.Profile == TextProfilePinnedFontViewportPXV2 {
			textIndex = index
		}
	}
	if textIndex < 0 {
		t.Fatal("Text v2 descriptor missing from catalog v3")
	}
	encoded, err := videoedit.CanonicalJSON(catalog)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/semantic-descriptors-v3.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("semantic descriptor catalog v3 canonical fixture drift\nwant=%s\ngot=%s", want, encoded)
	}
	digest := sha256.Sum256(encoded)
	gotHash := "sha256:" + hex.EncodeToString(digest[:])
	const wantHash = "sha256:86ef8d55059de4c2ff39d38f915d35176c3f8b4ada73de65f1352fbcb7f141f0"
	if gotHash != wantHash {
		t.Fatalf("semantic descriptor catalog v3 canonical hash=%q, want %q", gotHash, wantHash)
	}
	catalog.Sources[textIndex].RequiredAssetKinds[0] = AssetKindData
	catalog.TransitionScopes[0].Profile = "mutated"
	again := SemanticDescriptorsV3()
	if again.Sources[textIndex].RequiredAssetKinds[0] != AssetKindFont || again.TransitionScopes[0].Profile != VectorTransitionScopeProfileV1 {
		t.Fatal("catalog v3 aliases caller-owned storage")
	}
}

func textV2Document(text, script string, direction TextDirection) Document {
	lineHeight := Rational{Numerator: 60, Denominator: 1}
	return Document{
		SchemaVersion: SchemaVersion, ID: "text-v2-document", Revision: 1,
		Assets: []Asset{
			{ID: "font", Kind: AssetKindFont, URI: "fonts/pinned.ttf", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{ID: "text", Kind: AssetKindText, Text: &TextSource{
				Profile: TextProfilePinnedFontViewportPXV2, Text: text, FontAssetID: "font",
				FontSize: Rational{Numerator: 48, Denominator: 1}, LineHeight: &lineHeight,
				Viewport: &RasterViewport{WidthPixels: 640, HeightPixels: 180},
				Fill:     ColorRGBA16{R: 65_535, G: 60_000, B: 4_096, A: 65_535},
				Language: "en", Script: script, Direction: direction, Alignment: TextAlignmentCenter,
			}},
		},
		Timelines: []Timeline{{ID: "timeline", Tracks: []Track{{
			ID: "text-track", Kind: TrackKindText, Clips: []Clip{{
				ID: "text-clip", AssetID: "text", TimelineRange: testRange(0, 5), SourceRange: testRange(0, 5),
			}},
		}}}},
	}
}
