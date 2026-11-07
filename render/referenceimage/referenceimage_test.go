package referenceimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"os"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	mediacolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
)

func TestStaticPNGReferenceExportEndToEnd(t *testing.T) {
	green := strictPNG(t, 2, 2, stdcolor.NRGBA{G: 255, A: 255}, 0)
	red := strictPNG(t, 1, 1, stdcolor.NRGBA{R: 255, A: 255}, 1)
	blue := strictPNG(t, 3, 1, stdcolor.NRGBA{B: 255, A: 192}, 2)
	document := referenceDocument(contentHash(green), contentHash(red), contentHash(blue))
	bindings := []Binding{{AssetID: "asset-green", EncodedPNG: green}, {AssetID: "asset-red", EncodedPNG: red}, {AssetID: "asset-blue", EncodedPNG: blue}}
	config := DefaultConfig(3, 2)
	config.BuildFingerprint = "referenceimage-test-build"
	program, diagnostics, err := Compile(context.Background(), document, bindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile program=%v diagnostics=%+v err=%v", program, diagnostics, err)
	}
	wantDocumentHash, err := documentv2.DocumentHash(document)
	if err != nil || program.DocumentHash() != wantDocumentHash || program.SourceManifest().DocumentHash != wantDocumentHash {
		t.Fatalf("document hash program=%q want=%q err=%v", program.DocumentHash(), wantDocumentHash, err)
	}
	if got := program.Config(); got.Width != 3 || got.Height != 2 || got.BuildFingerprint != config.BuildFingerprint {
		t.Fatalf("normalized config=%+v", got)
	}

	// A successful Program owns the frozen document, decoded source pixels and manifest.
	document.ID = "mutated"
	document.Assets[0].ID = "mutated-asset"
	document.Timelines[0].Tracks[0].Clips[0].ID = "mutated-clip"
	for bindingIndex := range bindings {
		for byteIndex := range bindings[bindingIndex].EncodedPNG {
			bindings[bindingIndex].EncodedPNG[byteIndex] = 0
		}
	}
	manifest := program.SourceManifest()
	manifest.Assets[0].PixelHash = "mutated"
	if program.SourceManifest().Assets[0].PixelHash == "mutated" {
		t.Fatal("source manifest aliases caller storage")
	}

	// Seek out of order. Repeating the dissolve time must reproduce every serializable receipt.
	times := []documentv2.RationalTime{rt(5, 1), rt(0, 1), rt(12, 1), rt(4, 1), rt(5, 1)}
	results := make([]Result, len(times))
	for index, at := range times {
		results[index], err = program.RenderAt(context.Background(), "timeline", at)
		if err != nil {
			t.Fatalf("render[%d] at=%+v: %v", index, at, err)
		}
	}
	dissolve := results[0]
	if len(dissolve.SourceManifest.Assets) != 3 || len(dissolve.Raster.Sources) != 3 ||
		dissolve.Raster.Sources[0].Request.TrackID != "track-bottom" ||
		dissolve.Raster.Sources[1].Request.ClipID != "clip-red" || dissolve.Raster.Sources[2].Request.ClipID != "clip-blue" {
		t.Fatalf("dissolve source receipts=%+v manifest=%+v", dissolve.Raster.Sources, dissolve.SourceManifest)
	}
	if len(results[2].Raster.Sources) != 0 || results[2].Raster.PixelHash == dissolve.Raster.PixelHash {
		t.Fatalf("transparent gap=%+v", results[2].Raster)
	}
	if serialHash(t, results[0]) != serialHash(t, results[4]) || results[0].PNG.ArtifactHash != results[4].PNG.ArtifactHash {
		t.Fatalf("repeat seek mismatch first=%+v second=%+v", results[0], results[4])
	}

	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(1, 1)}
	frame, err := program.RenderFrame(context.Background(), "timeline", clock, 5)
	if err != nil || frame.FrameClock != clock || frame.CompositionFrameIndex != 5 ||
		serialHash(t, frame.Result) != serialHash(t, dissolve) || frame.Result.PNG.ArtifactHash != dissolve.PNG.ArtifactHash {
		t.Fatalf("frame parity=%+v err=%v", frame, err)
	}

	artifact, err := dissolve.PNG.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := pngdecode.Decode(context.Background(), artifact, pngdecode.DefaultLimits())
	if err != nil {
		t.Fatalf("strict roundtrip decode: %v", err)
	}
	outputBytes, err := dissolve.Output.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := mediacolor.NormalizeRGBA8(context.Background(), mediacolor.RGBA8Input{
		Width: 3, Height: 2, Stride: 12, Metadata: mediacolor.MetadataRGBA8SRGBV1(), Pixels: outputBytes,
	}, mediacolor.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	normalizedHash, err := normalized.CanonicalHash()
	if err != nil || decoded.Width != 3 || decoded.Height != 2 || decoded.EncodedContentHash != dissolve.PNG.ArtifactHash ||
		decoded.PixelHash != normalizedHash {
		t.Fatalf("roundtrip decoded=%+v normalized_hash=%q err=%v", decoded, normalizedHash, err)
	}

	fixture := loadResultFixture(t)
	if fixture.Profile != dissolve.Profile || fixture.DocumentHash != dissolve.DocumentHash || fixture.TimelineID != dissolve.TimelineID ||
		fixture.TimelineTime != dissolve.TimelineTime || fixture.SourceAssetCount != len(dissolve.SourceManifest.Assets) ||
		fixture.OrderedSourceCount != len(dissolve.Raster.Sources) || fixture.RasterPixelHash != dissolve.Raster.PixelHash ||
		fixture.OutputPixelHash != dissolve.Output.PixelHash || fixture.SerializedResultHash != serialHash(t, dissolve) ||
		fixture.PNGArtifactHash != dissolve.PNG.ArtifactHash {
		t.Fatalf("result fixture=%+v result=%+v serialized=%s", fixture, dissolve, serialHash(t, dissolve))
	}
	results[0].SourceManifest.Assets[0].PixelHash = "mutated"
	results[0].Raster.Sources[0].PixelHash = "mutated"
	again, err := program.RenderAt(context.Background(), "timeline", rt(5, 1))
	if err != nil || again.PNG.ArtifactHash != fixture.PNGArtifactHash || again.Raster.PixelHash != fixture.RasterPixelHash ||
		again.SourceManifest.Assets[0].PixelHash == "mutated" || again.Raster.Sources[0].PixelHash == "mutated" {
		t.Fatalf("render result aliases program storage: result=%+v err=%v", again, err)
	}
}

func TestProgramConcurrentRenders(t *testing.T) {
	green := strictPNG(t, 2, 2, stdcolor.NRGBA{G: 255, A: 255}, 0)
	red := strictPNG(t, 1, 1, stdcolor.NRGBA{R: 255, A: 255}, 1)
	blue := strictPNG(t, 3, 1, stdcolor.NRGBA{B: 255, A: 192}, 2)
	document := referenceDocument(contentHash(green), contentHash(red), contentHash(blue))
	program, _, err := Compile(context.Background(), document, []Binding{
		{AssetID: "asset-green", EncodedPNG: green}, {AssetID: "asset-red", EncodedPNG: red}, {AssetID: "asset-blue", EncodedPNG: blue},
	}, DefaultConfig(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	want, err := program.RenderAt(context.Background(), "timeline", rt(5, 1))
	if err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, renderErr := program.RenderAt(context.Background(), "timeline", rt(5, 1))
			if renderErr != nil {
				errorsByWorker <- renderErr
				return
			}
			if got.PNG.ArtifactHash != want.PNG.ArtifactHash || got.Raster.PixelHash != want.Raster.PixelHash || len(got.Raster.Sources) != 3 {
				errorsByWorker <- &concurrencyMismatch{}
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		t.Fatal(workerErr)
	}
}

type concurrencyMismatch struct{}

func (*concurrencyMismatch) Error() string { return "concurrent receipt mismatch" }

type resultFixture struct {
	Profile              string                  `json:"profile"`
	Scenario             string                  `json:"scenario"`
	DocumentHash         string                  `json:"document_hash"`
	TimelineID           documentv2.ID           `json:"timeline_id"`
	TimelineTime         documentv2.RationalTime `json:"timeline_time"`
	SourceAssetCount     int                     `json:"source_asset_count"`
	OrderedSourceCount   int                     `json:"ordered_source_count"`
	RasterPixelHash      string                  `json:"raster_pixel_hash"`
	OutputPixelHash      string                  `json:"output_pixel_hash"`
	SerializedResultHash string                  `json:"serialized_result_hash"`
	PNGArtifactHash      string                  `json:"png_artifact_hash"`
}

func loadResultFixture(t testing.TB) resultFixture {
	t.Helper()
	encoded, err := os.ReadFile("testdata/result-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var result resultFixture
	if err := wirejson.Decode(encoded, &result); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(encoded), canonical) {
		t.Fatalf("result fixture is not canonical\nwant=%s\ngot=%s", canonical, bytes.TrimSpace(encoded))
	}
	if result.Scenario == "" {
		t.Fatal("result fixture lacks scenario")
	}
	return result
}

func referenceDocument(greenHash, redHash, blueHash string) documentv2.Document {
	zero, quarter, half, threeQuarter, one := ratio(0, 1), ratio(1, 4), ratio(1, 2), ratio(3, 4), ratio(1, 1)
	position0, position1 := documentv2.Point{X: zero, Y: zero}, documentv2.Point{X: one, Y: zero}
	full := documentv2.Rect{X: zero, Y: zero, Width: one, Height: one}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "reference-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "asset-green", Kind: documentv2.AssetKindImage, URI: "green.png", ContentHash: greenHash},
			{ID: "asset-red", Kind: documentv2.AssetKindImage, URI: "red.png", ContentHash: redHash},
			{ID: "asset-blue", Kind: documentv2.AssetKindImage, URI: "blue.png", ContentHash: blueHash},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "track-bottom", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "clip-green", AssetID: "asset-green", TimelineRange: tr(0, 10), SourceRange: tr(0, 10),
				PropertySet: &documentv2.PropertySet{
					Transform: &documentv2.TransformProperty{Position: position0, Scale: documentv2.Point{X: one, Y: one}, RotationDegrees: zero, Anchor: documentv2.Point{X: half, Y: half}},
					Crop:      &documentv2.CropProperty{Rect: documentv2.Rect{X: half, Y: zero, Width: half, Height: one}},
					Opacity:   &documentv2.OpacityProperty{Value: quarter},
				},
				KeyframeLanes: []documentv2.KeyframeLane{
					{ID: "position", Target: documentv2.KeyframeTargetTransformPosition, ValueType: documentv2.KeyframeValuePoint, Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Point: &position0}, Interpolation: documentv2.InterpolationLinear}, {Offset: rt(10, 1), Value: documentv2.KeyframeValue{Point: &position1}}}},
					{ID: "crop", Target: documentv2.KeyframeTargetCrop, ValueType: documentv2.KeyframeValueRect, Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Rect: &full}, Interpolation: documentv2.InterpolationHold}, {Offset: rt(10, 1), Value: documentv2.KeyframeValue{Rect: &full}}}},
					{ID: "opacity", Target: documentv2.KeyframeTargetOpacity, ValueType: documentv2.KeyframeValueScalar, Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Scalar: &quarter}, Interpolation: documentv2.InterpolationLinear}, {Offset: rt(10, 1), Value: documentv2.KeyframeValue{Scalar: &threeQuarter}}}},
				},
			}}},
			{ID: "track-top", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "clip-red", AssetID: "asset-red", TimelineRange: tr(0, 5), SourceRange: tr(0, 5)},
				{ID: "clip-blue", AssetID: "asset-blue", TimelineRange: tr(5, 5), SourceRange: tr(0, 5)},
			}},
		}, Transitions: []documentv2.Transition{{ID: "dissolve", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "clip-red", IncomingClipID: "clip-blue", PreDuration: rt(1, 1), PostDuration: rt(1, 1)}}}},
	}
}

func serialHash(t testing.TB, value any) string {
	t.Helper()
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return contentHash(encoded)
}

func strictPNG(t testing.TB, width, height int, pixel stdcolor.NRGBA, intent byte) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, forceAlphaImage{NRGBA: value}); err != nil {
		t.Fatal(err)
	}
	return insertSRGB(t, encoded.Bytes(), intent)
}

type forceAlphaImage struct{ *image.NRGBA }

func (forceAlphaImage) Opaque() bool { return false }

func insertSRGB(t testing.TB, encoded []byte, intent byte) []byte {
	t.Helper()
	if len(encoded) < 33 || string(encoded[12:16]) != "IHDR" {
		t.Fatal("test PNG lacks IHDR")
	}
	length := int(binary.BigEndian.Uint32(encoded[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = intent
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(encoded)+len(chunk))
	result = append(result, encoded[:end]...)
	result = append(result, chunk...)
	return append(result, encoded[end:]...)
}

func contentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
func rt(value, rate int64) documentv2.RationalTime {
	return documentv2.RationalTime{Value: value, Rate: rate}
}
func tr(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}
func ratio(numerator, denominator int64) documentv2.Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}
