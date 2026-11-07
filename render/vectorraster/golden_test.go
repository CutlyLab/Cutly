package vectorraster

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

type goldenVectors struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       string         `json:"profile"`
	Vectors       []goldenVector `json:"vectors"`
}

type goldenVector struct {
	Name               string `json:"name"`
	SourceSemanticHash string `json:"source_semantic_hash"`
	PixelHash          string `json:"pixel_hash"`
	WidthPixels        uint32 `json:"width_pixels"`
	HeightPixels       uint32 `json:"height_pixels"`
	PathCount          uint64 `json:"path_count"`
	CommandCount       uint64 `json:"command_count"`
	ConceptualEdges    uint64 `json:"conceptual_edges"`
	EffectiveEdges     uint64 `json:"effective_edges"`
}

func TestRasterGoldenHashes(t *testing.T) {
	outer := rectangleCommands(q(0, 1), q(0, 1), q(3, 1), q(3, 1), true)
	inner := rectangleCommands(q(1, 1), q(1, 1), q(2, 1), q(2, 1), true)
	cases := []struct {
		name   string
		source documentv2.VectorSource
	}{
		{
			"full-2x2",
			testSource(2, 2, testRectPath("full", q(0, 1), q(0, 1), q(2, 1), q(2, 1), testColor(65_535, 65_535, 65_535, 65_535), true)),
		},
		{
			"meet-letterbox-4x2",
			func() documentv2.VectorSource {
				value := testSource(4, 2, testRectPath("square", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(65_535, 0, 0, 65_535), true))
				value.ViewBox.Width, value.ViewBox.Height = q(1, 1), q(1, 1)
				return value
			}(),
		},
		{
			"nearest-even-half-1x1",
			testSource(1, 1, testRectPath("half", q(0, 1), q(0, 1), q(1, 2), q(1, 1), testColor(65_535, 65_535, 65_535, 65_535), true)),
		},
		{
			"evenodd-hole-3x3",
			testSource(3, 3, documentv2.VectorPath{
				ID: "hole", FillRule: documentv2.VectorFillEvenOdd, Fill: testColor(0, 65_535, 0, 65_535),
				Commands: append(outer, inner...),
			}),
		},
		{
			"ordered-alpha-1x1",
			testSource(1, 1,
				testRectPath("red", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(65_535, 0, 0, 32_768), true),
				testRectPath("blue", q(0, 1), q(0, 1), q(1, 1), q(1, 1), testColor(0, 0, 65_535, 32_768), true),
			),
		},
	}
	vectors := goldenVectors{SchemaVersion: 1, Profile: ProfileV1, Vectors: make([]goldenVector, 0, len(cases))}
	for _, test := range cases {
		_, receipt, err := Rasterize(context.Background(), test.source, DefaultLimits())
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		vectors.Vectors = append(vectors.Vectors, goldenVector{
			Name: test.name, SourceSemanticHash: receipt.SourceSemanticHash, PixelHash: receipt.PixelHash,
			WidthPixels: receipt.WidthPixels, HeightPixels: receipt.HeightPixels,
			PathCount: receipt.PathCount, CommandCount: receipt.CommandCount,
			ConceptualEdges: receipt.ConceptualEdgeCount, EffectiveEdges: receipt.EffectiveEdgeCount,
		})
	}
	canonical, err := videoedit.CanonicalJSON(vectors)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "raster-v1.vectors.canonical.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v\ncanonical: %s", err, canonical)
	}
	fixture = bytes.TrimSpace(fixture)
	if !bytes.Equal(canonical, fixture) {
		t.Fatalf("golden fixture drift\nwant %s\n got %s", fixture, canonical)
	}
}
