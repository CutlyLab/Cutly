package visualcompositor

import (
	"bytes"
	"context"
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
