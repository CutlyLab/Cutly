package cpuraster

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type rasterVectorFixture struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       string         `json:"profile"`
	Vectors       []rasterVector `json:"vectors"`
}

type rasterVector struct {
	Name       string                       `json:"name"`
	Source     rasterVectorSurface          `json:"source"`
	Target     rasterVectorTarget           `json:"target"`
	Transform  documentv2.TransformProperty `json:"transform"`
	Crop       documentv2.CropProperty      `json:"crop"`
	OutputHash string                       `json:"output_hash"`
	Output     []cpucompositor.Pixel        `json:"output"`
}

type rasterVectorSurface struct {
	Width  int                   `json:"width"`
	Height int                   `json:"height"`
	Pixels []cpucompositor.Pixel `json:"pixels"`
}

type rasterVectorTarget struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func TestCanonicalRasterVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/raster-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture rasterVectorFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(raw)) {
		t.Fatal("raster vector fixture is not in its canonical struct serialization")
	}
	if fixture.SchemaVersion != 1 || fixture.Profile != ProfileV1 || len(fixture.Vectors) == 0 {
		t.Fatalf("bad fixture header: %+v", fixture)
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			source := testSurface(t, vector.Source.Width, vector.Source.Height, vector.Source.Pixels)
			transform, crop := evaluatedGeometry(t, &vector.Transform, &vector.Crop)
			result, rasterErr := Rasterize(context.Background(), source, vector.Target.Width, vector.Target.Height, transform, crop, Limits{})
			if rasterErr != nil {
				t.Fatal(rasterErr)
			}
			assertPixels(t, result, vector.Output)
			hash, hashErr := result.CanonicalHash()
			if hashErr != nil || hash != vector.OutputHash {
				t.Fatalf("output hash = %q, %v; want %q", hash, hashErr, vector.OutputHash)
			}
		})
	}
}
