package pngdecode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type vectorFixture struct {
	SchemaVersion int          `json:"schema_version"`
	Profile       string       `json:"profile"`
	Vectors       []testVector `json:"vectors"`
}

type testVector struct {
	Name                string                `json:"name"`
	EncodedPNGBase64    string                `json:"encoded_png_base64"`
	Width               int                   `json:"width"`
	Height              int                   `json:"height"`
	SRGBRenderingIntent uint8                 `json:"srgb_rendering_intent"`
	EncodedContentHash  string                `json:"encoded_content_hash"`
	PixelHash           string                `json:"pixel_hash"`
	NormalizedRGBA16    []cpucompositor.Pixel `json:"normalized_rgba16"`
}

func TestCanonicalDecodeVectors(t *testing.T) {
	encodedFixture, err := os.ReadFile("testdata/decode-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture vectorFixture
	if err := json.Unmarshal(encodedFixture, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(encodedFixture)) {
		t.Fatalf("decode vector fixture is not canonical\nactual %s\nwant   %s", encodedFixture, canonical)
	}
	if fixture.SchemaVersion != 1 || fixture.Profile != ProfileV1 || len(fixture.Vectors) == 0 {
		t.Fatalf("invalid fixture header: %+v", fixture)
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			encoded, decodeErr := base64.StdEncoding.Strict().DecodeString(vector.EncodedPNGBase64)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			result, decodeErr := Decode(context.Background(), encoded, Limits{})
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if result.Width != vector.Width || result.Height != vector.Height ||
				result.SRGBRenderingIntent != vector.SRGBRenderingIntent ||
				result.EncodedContentHash != vector.EncodedContentHash || result.PixelHash != vector.PixelHash ||
				result.DecodeProfile != ProfileV1 || result.ContentHashProfile != ContentHashProfileV1 {
				t.Fatalf("result = %+v, vector = %+v", result, vector)
			}
			if len(vector.NormalizedRGBA16) != vector.Width*vector.Height {
				t.Fatalf("pixel vector length = %d", len(vector.NormalizedRGBA16))
			}
			for index, want := range vector.NormalizedRGBA16 {
				got, pixelErr := result.Surface.Pixel(index%vector.Width, index/vector.Width)
				if pixelErr != nil || got != want {
					t.Fatalf("pixel %d = %+v, %v; want %+v", index, got, pixelErr, want)
				}
			}
		})
	}
}
