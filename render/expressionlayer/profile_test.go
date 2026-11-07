package expressionlayer

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

func TestDescriptorCanonicalFixtureAndProfileChain(t *testing.T) {
	descriptor := Descriptor()
	if descriptor.SchemaVersion != ProfileSchemaVersion || descriptor.Profile != ProfileV1 ||
		descriptor.ContractVersion != ContractVersion || descriptor.SemanticHashProfile != vectorraster.SourceHashProfileV1 ||
		descriptor.SourceProfile != SourceProfileV1 || descriptor.SelectionProfile != SelectionProfileV1 ||
		descriptor.ExpressionProfile != documentv2.VectorProfilePathViewportPXV2 ||
		descriptor.EvaluatorProfile != documentv2.EvaluatorVectorPathViewportPXV2 ||
		descriptor.RasterProfile != vectorraster.ProfileV1 || descriptor.PaintProfile != color.ProfileRGBA16SRGBPaintV1 ||
		descriptor.KernelProfile != cpucompositor.KernelProfileV1 ||
		descriptor.SurfaceProfile != cpucompositor.SurfaceProfileV1 || descriptor.SurfaceHashProfile != SurfaceHashProfileV1 {
		t.Fatalf("descriptor identity = %+v", descriptor)
	}
	canonical, err := videoedit.CanonicalJSON(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	want := fixture(t, "profile-v1.canonical.json")
	if !bytes.Equal(canonical, want) {
		t.Fatalf("profile fixture drift\nwant %s\n got %s", want, canonical)
	}
	var decoded ProfileDescriptor
	if err := wirejson.Decode(want, &decoded); err != nil || decoded != descriptor {
		t.Fatalf("strict profile decode: decoded=%+v err=%v", decoded, err)
	}
}

func TestManifestCanonicalFixtureStrictDecodeAndFrameSerialization(t *testing.T) {
	provider, err := Compile(context.Background(), fixtureDocument(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	manifest := provider.Manifest()
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := fixture(t, "manifest-v1.canonical.json")
	if !bytes.Equal(canonical, want) {
		t.Fatalf("manifest fixture drift\nwant %s\n got %s", want, canonical)
	}
	var decoded Manifest
	if err := wirejson.Decode(want, &decoded); err != nil {
		t.Fatalf("strict manifest decode: %v", err)
	}
	decodedCanonical, err := videoedit.CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(decodedCanonical, want) {
		t.Fatalf("manifest roundtrip drift: %v\n%s", err, decodedCanonical)
	}
	hostile := bytes.Replace(want, []byte(`"schema_version":1`), []byte(`"unknown":true,"schema_version":1`), 1)
	if bytes.Equal(hostile, want) {
		t.Fatal("hostile replacement did not match")
	}
	if err := wirejson.Decode(hostile, &decoded); err == nil {
		t.Fatal("strict manifest decode accepted an unknown field")
	}

	frame, err := provider.Frame(context.Background(), fixtureRequest(provider, "clip-b", documentv2.RationalTime{Value: 1, Rate: 2}))
	if err != nil {
		t.Fatal(err)
	}
	serializable, err := videoedit.CanonicalJSON(frame)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serializable, []byte(`"surface":`)) || !bytes.Contains(serializable, []byte(`"surface_identity":`)) {
		t.Fatalf("Frame serialization leaked/omitted surface state: %s", serializable)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(raw)
}
