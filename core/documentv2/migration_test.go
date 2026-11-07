package documentv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestMigrateV1FixtureIsTypedDeterministicAndCanonical(t *testing.T) {
	raw := readFixture(t, "document-v1.valid.json")
	first, diagnostics, err := MigrateV1JSON(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	opacity := first.Timelines[0].Tracks[0].Clips[0].PropertySet.Opacity
	if opacity == nil || opacity.Value != (Rational{Numerator: 1, Denominator: 1}) {
		t.Fatalf("de-facto bare opacity did not migrate: %+v", opacity)
	}
	if len(first.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions) != 0 {
		t.Fatal("known opacity was also retained as an extension")
	}
	second, secondDiagnostics, secondErr := MigrateV1JSON(raw)
	if secondErr != nil || HasErrorDiagnostics(secondDiagnostics) {
		t.Fatalf("second migration: diagnostics=%+v err=%v", secondDiagnostics, secondErr)
	}
	firstCanonical, _ := CanonicalJSON(first)
	secondCanonical, _ := CanonicalJSON(second)
	if !bytes.Equal(firstCanonical, secondCanonical) {
		t.Fatal("migration is not deterministic")
	}
	want := bytes.TrimSpace(readFixture(t, "document-v1-to-v2.canonical.json"))
	if !bytes.Equal(firstCanonical, want) {
		t.Fatalf("migration canonical drift\nwant %s\n got %s", want, firstCanonical)
	}
	if bytes.Contains(firstCanonical, []byte(`"viewport"`)) {
		t.Fatalf("v1 migration guessed a raster viewport: %s", firstCanonical)
	}
}

func TestMigrateV1RecognizesOnlyStrictReviewedPropertyShapes(t *testing.T) {
	source := fixtureV1Document(t)
	videoProperties := source.Timelines[0].Tracks[0].Clips[0].Properties
	videoProperties[PropertyOpacityV1] = json.RawMessage(`{"value":0.25}`)
	videoProperties[PropertyTransformV1] = json.RawMessage(`{"position":{"x":12.5,"y":-3},"scale":{"x":1,"y":0},"rotation_degrees":90,"anchor":{"x":0.5,"y":0.5}}`)
	videoProperties[PropertyCropV1] = json.RawMessage(`{"rect":{"x":0,"y":0,"width":0.5,"height":1}}`)
	audioProperties := source.Timelines[0].Tracks[1].Clips[0].Properties
	if audioProperties == nil {
		audioProperties = make(map[string]json.RawMessage)
		source.Timelines[0].Tracks[1].Clips[0].Properties = audioProperties
	}
	audioProperties[PropertyAudioGainV1] = json.RawMessage(`{"value":0.5}`)
	audioProperties[PropertyAudioPanV1] = json.RawMessage(`-0.25`)

	migrated, diagnostics, err := MigrateV1(source)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	visual := migrated.Timelines[0].Tracks[0].Clips[0].PropertySet
	if visual.Opacity.Value != (Rational{Numerator: 1, Denominator: 4}) ||
		visual.Transform.Position.X != (Rational{Numerator: 25, Denominator: 2}) ||
		visual.Transform.Scale.Y != (Rational{Numerator: 0, Denominator: 1}) ||
		visual.Crop.Rect.Width != (Rational{Numerator: 1, Denominator: 2}) {
		t.Fatalf("visual typed migration = %+v", visual)
	}
	audio := migrated.Timelines[0].Tracks[1].Clips[0].PropertySet
	if audio.AudioGain.Linear != (Rational{Numerator: 1, Denominator: 2}) || audio.AudioPan.Value != (Rational{Numerator: -1, Denominator: 4}) {
		t.Fatalf("audio typed migration = %+v", audio)
	}
}

func TestMigrateV1UnknownPropertyRoundTripsFailClosedWithFullDigest(t *testing.T) {
	source := fixtureV1Document(t)
	const key = "vendor.sparkle.v7"
	raw := json.RawMessage(`{"strength":0.5,"mode":"screen"}`)
	source.Timelines[0].Tracks[0].Clips[0].Properties[key] = raw
	migrated, diagnostics, err := MigrateV1(source)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	extensions := migrated.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions
	if len(extensions) != 1 {
		t.Fatalf("extensions = %+v", extensions)
	}
	digest := sha256.Sum256([]byte(key))
	wantNamespace := "legacy.property." + hex.EncodeToString(digest[:]) + ".v1"
	if extensions[0].Namespace != wantNamespace || !extensions[0].AffectsRendering || extensions[0].RequiredCapability != "legacy.property.v1" {
		t.Fatalf("extension fail-closed envelope = %+v", extensions[0])
	}
	var envelope legacyPropertyEnvelope
	if err := json.Unmarshal(extensions[0].Value, &envelope); err != nil {
		t.Fatal(err)
	}
	rawCanonical, canonicalErr := videoedit.CanonicalJSON(raw)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if envelope.LegacyKey != key || !bytes.Equal(envelope.Value, rawCanonical) {
		t.Fatalf("legacy envelope = %+v", envelope)
	}
	requirements, requirementDiagnostics := RequiredCapabilities(migrated)
	if HasErrorDiagnostics(requirementDiagnostics) || !containsFeature(requirements, "legacy.property.v1") {
		t.Fatalf("unknown render property was not capability gated: requirements=%+v diagnostics=%+v", requirements, requirementDiagnostics)
	}
}

func TestMigrateV1DoesNotGuessNewAudioProcessingSemantics(t *testing.T) {
	source := fixtureV1Document(t)
	audioProperties := source.Timelines[0].Tracks[1].Clips[0].Properties
	if audioProperties == nil {
		audioProperties = make(map[string]json.RawMessage)
		source.Timelines[0].Tracks[1].Clips[0].Properties = audioProperties
	}
	audioProperties[PropertyAudioChannelMatrixV1] = json.RawMessage(`{
		"source_layout":"stereo",
		"source_channels":["FL","FR"],
		"left_coefficients":[{"numerator":1,"denominator":1},{"numerator":0,"denominator":1}],
		"right_coefficients":[{"numerator":0,"denominator":1},{"numerator":1,"denominator":1}]
	}`)
	audioProperties[PropertyAudioSamplePeakLimiterV1] = json.RawMessage(`{"ceiling_linear":{"numerator":19,"denominator":20}}`)

	migrated, diagnostics, err := MigrateV1(source)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	properties := migrated.Timelines[0].Tracks[1].Clips[0].PropertySet
	if properties.AudioChannelMatrix != nil {
		t.Fatalf("v1 raw property was silently promoted to typed matrix: %+v", properties.AudioChannelMatrix)
	}
	if len(properties.Extensions) != 2 {
		t.Fatalf("new audio-processing legacy envelopes = %+v", properties.Extensions)
	}
	wantKeys := map[string]bool{
		PropertyAudioChannelMatrixV1:     false,
		PropertyAudioSamplePeakLimiterV1: false,
	}
	for _, extension := range properties.Extensions {
		if extension.RequiredCapability != LegacyPropertyCapabilityV1 || !extension.AffectsRendering {
			t.Fatalf("new audio-processing property did not fail closed: %+v", extension)
		}
		var envelope legacyPropertyEnvelope
		if err := wirejson.Decode(extension.Value, &envelope); err != nil {
			t.Fatal(err)
		}
		if _, exists := wantKeys[envelope.LegacyKey]; !exists {
			t.Fatalf("unexpected migrated legacy key %q", envelope.LegacyKey)
		}
		wantKeys[envelope.LegacyKey] = true
	}
	for key, found := range wantKeys {
		if !found {
			t.Errorf("legacy property %q was not preserved", key)
		}
	}
}

func TestMigrateV1MalformedKnownPropertiesFailInsteadOfBecomingExtensions(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"value":"opaque"}`),
		json.RawMessage(`1e999999999999999999999999`),
		json.RawMessage(`{"value":0.5,"extra":true}`),
	} {
		source := fixtureV1Document(t)
		source.Timelines[0].Tracks[0].Clips[0].Properties[PropertyOpacityV1] = raw
		migrated, diagnostics, err := MigrateV1(source)
		if err != nil {
			t.Fatal(err)
		}
		if migrated.SchemaVersion != 0 || !HasErrorDiagnostics(diagnostics) {
			t.Fatalf("malformed known property migrated: document=%+v diagnostics=%+v", migrated, diagnostics)
		}
		assertDiagnostic(t, diagnostics, CodeMigrationInvalid, "/timelines/0/tracks/0/clips/0/properties/cutly.opacity.v1")
	}
}

func TestMigrateV1PreservesEmptyLegacyKeyAndNullValue(t *testing.T) {
	source := fixtureV1Document(t)
	source.Timelines[0].Tracks[0].Clips[0].Properties[""] = json.RawMessage(`null`)
	document, diagnostics, err := MigrateV1(source)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	extensions := document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions
	if len(extensions) != 1 || extensions[0].Namespace != legacyPropertyNamespace("") || extensions[0].RequiredCapability != LegacyPropertyCapabilityV1 {
		t.Fatalf("empty-key extension = %+v", extensions)
	}
	var envelope legacyPropertyEnvelope
	if err := wirejson.Decode(extensions[0].Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.LegacyKey != "" || !bytes.Equal(envelope.Value, []byte("null")) {
		t.Fatalf("empty-key/null envelope = %+v", envelope)
	}
}

func TestMigrateV1IsDeepCopyAndRejectsV2Input(t *testing.T) {
	source := fixtureV1Document(t)
	source.Timelines[0].Tracks[0].Clips[0].Properties["vendor.safe.v1"] = json.RawMessage(`{"x":1}`)
	migrated, diagnostics, err := MigrateV1(source)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("migration: diagnostics=%+v err=%v", diagnostics, err)
	}
	source.Metadata["fixture"][0] = 'x'
	source.Timelines[0].Tracks[0].Clips[0].Properties["vendor.safe.v1"][0] = 'x'
	if string(migrated.Metadata["fixture"]) != "true" || !json.Valid(migrated.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0].Value) {
		t.Fatal("migration result shares raw storage with v1 input")
	}

	if document, v2Diagnostics, v2Err := MigrateV1JSON([]byte(`{"schema_version":2,"id":"v2","revision":0}`)); v2Err != nil || document.SchemaVersion != 0 || !HasErrorDiagnostics(v2Diagnostics) {
		t.Fatalf("v2 accepted by v1 migration entry: document=%+v diagnostics=%+v err=%v", document, v2Diagnostics, v2Err)
	}
}

func TestMigrateV1ProgrammaticDuplicateRawJSONFailsClosed(t *testing.T) {
	source := fixtureV1Document(t)
	source.Timelines[0].Tracks[0].Clips[0].Properties["vendor.duplicate.v1"] = json.RawMessage(`{"x":1,"x":2}`)
	_, diagnostics, err := MigrateV1(source)
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnostic(t, diagnostics, CodeMigrationInvalid, "/timelines/0/tracks/0/clips/0/properties/vendor.duplicate.v1")
}

func TestMigrateV1StablyRejectsValidV1ThatViolatesV2Invariants(t *testing.T) {
	t.Run("URI-only font is not content addressed", func(t *testing.T) {
		source := fixtureV1Document(t)
		source.Assets = append(source.Assets, videoedit.Asset{
			ID: "legacy-font", Kind: videoedit.AssetKindFont, URI: "fonts/legacy.ttf",
		})
		document, diagnostics, err := MigrateV1(source)
		if err != nil || document.SchemaVersion != 0 {
			t.Fatalf("migration: document=%+v diagnostics=%+v err=%v", document, diagnostics, err)
		}
		assertDiagnostic(t, diagnostics, CodeAssetInvalid, "/assets/2/content_hash")
	})

	t.Run("track and asset kind mismatch", func(t *testing.T) {
		source := fixtureV1Document(t)
		source.Timelines[0].Tracks[0].Clips[0].AssetID = source.Assets[1].ID
		document, diagnostics, err := MigrateV1(source)
		if err != nil || document.SchemaVersion != 0 {
			t.Fatalf("migration: document=%+v diagnostics=%+v err=%v", document, diagnostics, err)
		}
		assertDiagnostic(t, diagnostics, CodeClipInvalid, "/timelines/0/tracks/0/clips/0/asset_id")
	})
}

func fixtureV1Document(t *testing.T) videoedit.Document {
	t.Helper()
	var document videoedit.Document
	if err := wirejson.Decode(readFixture(t, "document-v1.valid.json"), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func containsFeature(requirements CapabilityRequirements, want videoedit.CapabilityID) bool {
	for _, feature := range requirements.Features {
		if feature == want {
			return true
		}
	}
	return false
}
