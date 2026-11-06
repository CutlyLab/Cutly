package referenceimage

import (
	"context"
	stdcolor "image/color"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func TestOrderedSourceReceiptReplayRejectsPositiveIndexAndTimeForgery(t *testing.T) {
	encoded := strictPNG(t, 1, 1, stdcolor.NRGBA{R: 255, A: 255}, 0)
	document := singleImageDocument(contentHash(encoded))
	program, _, err := Compile(context.Background(), document, []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := program.RenderAt(context.Background(), "timeline", rt(0, 1))
	if err != nil || len(baseline.Raster.Sources) != 1 {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	mutations := []struct {
		name string
		edit func(*Result)
	}{
		{name: "timeline index", edit: func(value *Result) { value.Raster.Sources[0].Request.TimelineIndex++ }},
		{name: "track index", edit: func(value *Result) { value.Raster.Sources[0].Request.TrackIndex++ }},
		{name: "clip index", edit: func(value *Result) { value.Raster.Sources[0].Request.ClipIndex++ }},
		{name: "local time", edit: func(value *Result) { value.Raster.Sources[0].Request.LocalTime = rt(1, 2) }},
		{name: "source time", edit: func(value *Result) { value.Raster.Sources[0].Request.SourceTime = rt(1, 2) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			forged := baseline
			forged.Raster.Sources = append([]rastercompositor.SourceReceipt(nil), baseline.Raster.Sources...)
			test.edit(&forged)
			result, err := program.finish(context.Background(), forged.Raster)
			assertZeroResult(t, result, err, ErrStageMismatch, CodeStageMismatch)
		})
	}
}

func TestProgramAndManifestDocumentInvariantsRejectForgery(t *testing.T) {
	encoded := strictPNG(t, 1, 1, stdcolor.NRGBA{R: 255, A: 255}, 0)
	document := singleImageDocument(contentHash(encoded))
	program, _, err := Compile(context.Background(), document, []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	manifest := program.SourceManifest()
	if !validSourceManifestAgainstDocument(context.Background(), manifest, document, program.config.SourceLimits) {
		t.Fatal("compiled manifest does not bind frozen document")
	}
	wrongDocument := document
	wrongDocument.Assets = append([]documentv2.Asset(nil), document.Assets...)
	wrongDocument.Assets[0].ContentHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if validSourceManifestAgainstDocument(context.Background(), manifest, wrongDocument, program.config.SourceLimits) {
		t.Fatal("manifest accepted a different document asset hash")
	}

	forged := *program
	forged.manifest = cloneManifest(program.manifest)
	forged.manifest.Assets[0].PixelHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, renderErr := forged.RenderAt(context.Background(), "timeline", rt(0, 1))
	assertZeroResult(t, result, renderErr, ErrProgramUnavailable, CodeProgramUnavailable)

	forged = *program
	forged.config.SourceLimits.MaxBindings++
	result, renderErr = forged.RenderAt(context.Background(), "timeline", rt(0, 1))
	assertZeroResult(t, result, renderErr, ErrProgramUnavailable, CodeProgramUnavailable)
}

func TestRenderFrameNonIntegralClockParity(t *testing.T) {
	encoded := strictPNG(t, 1, 1, stdcolor.NRGBA{G: 255, A: 255}, 0)
	document := singleImageDocument(contentHash(encoded))
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = tr(0, 10)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = tr(0, 10)
	program, _, err := Compile(context.Background(), document, []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	clock := videoedit.FrameClock{Origin: rt(2, 1), FramesPerSecond: rt(2, 1)}
	frame, err := program.RenderFrame(context.Background(), "timeline", clock, 6)
	if err != nil {
		t.Fatal(err)
	}
	at, err := program.RenderAt(context.Background(), "timeline", rt(5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Result.PNG.ArtifactHash != at.PNG.ArtifactHash || serialHash(t, frame.Result) != serialHash(t, at) {
		t.Fatalf("frame=%+v at=%+v", frame, at)
	}
}

func BenchmarkRenderStaticPNGReference720p(b *testing.B) {
	encoded := strictPNG(b, 1, 1, stdcolor.NRGBA{R: 255, G: 128, B: 64, A: 255}, 0)
	document := singleImageDocument(contentHash(encoded))
	program, _, err := Compile(context.Background(), document, []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1280, 720))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(1280 * 720 * 4)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		result, renderErr := program.RenderAt(context.Background(), "timeline", rt(0, 1))
		if renderErr != nil || result.PNG.Artifact == nil {
			b.Fatalf("result=%+v err=%v", result, renderErr)
		}
	}
}
