package visualcompositor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
)

func TestDescriptorCanonicalFixture(t *testing.T) {
	descriptor := Descriptor()
	if descriptor.SchemaVersion != ProfileSchemaVersion || descriptor.Profile != ProfileV1 ||
		descriptor.ExpressionProviderProfile != expressionlayer.ProfileV1 ||
		descriptor.ExpressionHandleProfile != expressionlayer.TransitionHandleProfileV2 ||
		descriptor.SurfaceProfile != cpucompositor.SurfaceProfileV1 {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	encoded, err := videoedit.CanonicalJSON(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	want := visualFixture(t, "profile-v1.canonical.json")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("profile drift\nwant %s\n got %s", want, encoded)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(want, &decoded); err != nil || decoded != descriptor {
		t.Fatalf("strict profile decode: decoded=%+v err=%v", decoded, err)
	}
}

func TestResultCanonicalFixtureStrictUnionAndRoundTrip(t *testing.T) {
	document := mixedDocument()
	native := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 30_000, A: 30_000}, {R: 30_000, A: 30_000},
		{R: 30_000, A: 30_000}, {R: 30_000, A: 30_000},
	})
	expression, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(2, 2)
	config.BuildFingerprint = "visual-fixture-build-v1"
	program, diagnostics := Compile(context.Background(), document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{
		Native: nativeProvider(native, nil), Expression: expression,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := videoedit.CanonicalJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	want := visualFixture(t, "result-v1.canonical.json")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("result drift\nwant %s\n got %s", want, encoded)
	}
	if !bytes.Contains(encoded, []byte(`"native":null`)) || !bytes.Contains(encoded, []byte(`"expression":null`)) ||
		!bytes.Contains(encoded, []byte(`"transition_request":null`)) {
		t.Fatalf("strict null branches missing: %s", encoded)
	}
	var decoded Result
	if err := wirejson.Decode(want, &decoded); err != nil {
		t.Fatalf("strict result decode: %v", err)
	}
	for index, receipt := range decoded.Sources {
		if err := receipt.Validate(); err != nil {
			t.Fatalf("source %d: %v", index, err)
		}
	}
	roundTrip, err := videoedit.CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(roundTrip, want) {
		t.Fatalf("roundtrip drift: %v\n%s", err, roundTrip)
	}
}

func TestV2CanonicalProfileResultAndPixelGoldenClosure(t *testing.T) {
	document := textV2VisualDocument(false)
	config := DefaultConfigV2(2, 2)
	config.BuildFingerprint = "visual-fixture-build-v2"
	program, diagnostics := Compile(context.Background(), document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 50_000, A: 50_000}, {R: 40_000, A: 40_000},
		{R: 30_000, A: 30_000}, {R: 20_000, A: 20_000},
	})
	result, err := program.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{
		Text: fakeTextProvider(surface, document.Assets[1].Text.Fill, nil),
	})
	if err != nil {
		t.Fatal(err)
	}

	profileFixture := visualFixture(t, "profile-v2.canonical.json")
	encodedProfile, err := videoedit.CanonicalJSON(DescriptorV2())
	if err != nil || !bytes.Equal(encodedProfile, profileFixture) {
		t.Fatalf("v2 profile drift: %v\nwant %s\n got %s", err, profileFixture, encodedProfile)
	}
	var decodedProfile ProfileDescriptor
	if err := wirejson.Decode(profileFixture, &decodedProfile); err != nil || decodedProfile != DescriptorV2() {
		t.Fatalf("strict v2 profile decode: decoded=%+v err=%v", decodedProfile, err)
	}
	if err := wirejson.Decode(withUnknownFixtureMember(profileFixture), &decodedProfile); err == nil {
		t.Fatal("strict v2 profile accepted unknown field")
	}

	resultFixture := visualFixture(t, "result-v2.canonical.json")
	encodedResult, err := videoedit.CanonicalJSON(result)
	if err != nil || !bytes.Equal(encodedResult, resultFixture) {
		t.Fatalf("v2 result drift: %v\nwant %s\n got %s", err, resultFixture, encodedResult)
	}
	var decodedResult Result
	if err := wirejson.Decode(resultFixture, &decodedResult); err != nil {
		t.Fatalf("strict v2 result decode: %v", err)
	}
	if len(decodedResult.Sources) != 1 || decodedResult.Sources[0].Kind != SourceKindText || decodedResult.Sources[0].Text == nil {
		t.Fatalf("decoded v2 result=%+v", decodedResult)
	}
	if err := decodedResult.Sources[0].Validate(); err != nil {
		t.Fatalf("decoded text source: %v", err)
	}
	if err := program.ValidateSerializedReceipt(context.Background(), decodedResult); err != nil {
		t.Fatalf("decoded v2 serialized receipt: %v", err)
	}
	roundTrip, err := videoedit.CanonicalJSON(decodedResult)
	if err != nil || !bytes.Equal(roundTrip, resultFixture) {
		t.Fatalf("v2 result roundtrip drift: %v\n%s", err, roundTrip)
	}
	if err := wirejson.Decode(withUnknownFixtureMember(resultFixture), &decodedResult); err == nil {
		t.Fatal("strict v2 result accepted unknown field")
	}

	golden := goldenVectors{SchemaVersion: 1, Profile: ProfileV2, Cases: []goldenCase{{
		Name: "static-text", PixelHash: result.PixelHash, SourceKinds: sourceKinds(result.Sources),
	}}}
	encodedGolden, err := videoedit.CanonicalJSON(golden)
	goldenFixture := visualFixture(t, "golden-v2.canonical.json")
	if err != nil || !bytes.Equal(encodedGolden, goldenFixture) {
		t.Fatalf("v2 golden drift: %v\nwant %s\n got %s", err, goldenFixture, encodedGolden)
	}

	assertVisualFixtureSHA256(t, "profile-v2.canonical.json", "a1f91bdb49fac2ab40c9015df31c52f69ee477bf8edabac4ad9c869bc3ab743d")
	assertVisualFixtureSHA256(t, "result-v2.canonical.json", "9e62e5ffa15ea63603221e926e3912fb806d41cd9dadc15fe7eded3883102de5")
	assertVisualFixtureSHA256(t, "golden-v2.canonical.json", "35bfb1e757631a52fe8c5595e91cd524f449c0efe3dbc61d5c86538f7b1a3410")
}

func withUnknownFixtureMember(raw []byte) []byte {
	result := append([]byte(nil), raw[:len(raw)-1]...)
	return append(result, []byte(`,"unknown_v2_member":true}`)...)
}

func assertVisualFixtureSHA256(t *testing.T, name, want string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
		t.Fatalf("%s SHA-256=%s want=%s", name, got, want)
	}
}

type goldenCase struct {
	Name        string       `json:"name"`
	PixelHash   string       `json:"pixel_hash"`
	SourceKinds []SourceKind `json:"source_kinds"`
}

type goldenVectors struct {
	SchemaVersion int          `json:"schema_version"`
	Profile       string       `json:"profile"`
	Cases         []goldenCase `json:"cases"`
}

func TestCanonicalPixelGoldens(t *testing.T) {
	image := imageDocument(false)
	native := testSurface(t, 2, 2, []cpucompositor.Pixel{
		{R: 65_535, A: 65_535}, {G: 65_535, A: 65_535},
		{B: 65_535, A: 65_535}, {R: 1, G: 2, B: 3, A: 4},
	})
	imageProgram, diagnostics := Compile(context.Background(), image, DefaultConfig(2, 2))
	if imageProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("image diagnostics=%+v", diagnostics)
	}
	imageResult, err := imageProgram.RenderAt(context.Background(), "timeline", rt(1, 2), Providers{Native: nativeProvider(native, nil)})
	if err != nil {
		t.Fatal(err)
	}
	gapResult, err := imageProgram.RenderAt(context.Background(), "timeline", rt(10, 1), Providers{Native: nativeProvider(native, nil)})
	if err != nil {
		t.Fatal(err)
	}
	transitionDocument := vectorTransitionDocument()
	expression, err := expressionlayer.Compile(context.Background(), transitionDocument, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	transitionProgram, diagnostics := Compile(context.Background(), transitionDocument, DefaultConfig(2, 2))
	if transitionProgram == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("transition diagnostics=%+v", diagnostics)
	}
	transitionResult, err := transitionProgram.RenderAt(context.Background(), "timeline", rt(3, 2), Providers{Expression: expression})
	if err != nil {
		t.Fatal(err)
	}
	values := goldenVectors{SchemaVersion: 1, Profile: ProfileV1, Cases: []goldenCase{
		{Name: "native-image", PixelHash: imageResult.PixelHash, SourceKinds: sourceKinds(imageResult.Sources)},
		{Name: "transparent-gap", PixelHash: gapResult.PixelHash, SourceKinds: sourceKinds(gapResult.Sources)},
		{Name: "vector-dissolve-quarter", PixelHash: transitionResult.PixelHash, SourceKinds: sourceKinds(transitionResult.Sources)},
	}}
	encoded, err := videoedit.CanonicalJSON(values)
	if err != nil {
		t.Fatal(err)
	}
	want := visualFixture(t, "golden-v1.canonical.json")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("golden drift\nwant %s\n got %s", want, encoded)
	}
}

func sourceKinds(values []SourceReceipt) []SourceKind {
	result := make([]SourceKind, len(values))
	for index := range values {
		result[index] = values[index].Kind
	}
	return result
}

func visualFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(raw)
}
