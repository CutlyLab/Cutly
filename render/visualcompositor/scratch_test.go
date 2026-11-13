package visualcompositor

import (
	"context"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

// The scratch path must be byte-identical to the allocating path for every frame it serves,
// including frames whose active layer count changes and frames inside a dissolve transition.
// The differential drives one mixed program through both renderers and compares the canonical
// surface hash, width, height and receipt count.
func TestRenderAtWithScratchMatchesAllocatingRender(t *testing.T) {
	document, providers, config := scratchFixture(t)
	program, diagnostics := Compile(context.Background(), document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile diagnostics=%+v", diagnostics)
	}
	scratch, err := cpucompositor.NewScratch(config.Limits.Target, 4)
	if err != nil {
		t.Fatal(err)
	}
	for frame := int64(0); frame < 60; frame++ {
		at := videoedit.RationalTime{Value: frame, Rate: 30}
		allocating, allocErr := program.RenderAt(context.Background(), "timeline", at, providers)
		if allocErr != nil {
			t.Fatalf("allocating frame %d: %v", frame, allocErr)
		}
		pooled, poolErr := program.RenderAtWithScratch(context.Background(), "timeline", at, providers, scratch)
		if poolErr != nil {
			t.Fatalf("scratch frame %d: %v", frame, poolErr)
		}
		if allocating.PixelHash != pooled.PixelHash || allocating.Width != pooled.Width || allocating.Height != pooled.Height ||
			len(allocating.Sources) != len(pooled.Sources) || allocating.TimelineTime != pooled.TimelineTime {
			t.Fatalf("frame %d diverged: hash %s vs %s", frame, allocating.PixelHash, pooled.PixelHash)
		}
	}
	if scratch.Retained() == 0 {
		t.Fatal("scratch retained no surfaces; recycling is not engaged")
	}
}

// One pool shared by concurrent frames must never hand the same storage to two writers; the
// per-frame results must still match the allocating path.
func TestRenderAtWithScratchConcurrentFramesMatch(t *testing.T) {
	document, providers, config := scratchFixture(t)
	program, diagnostics := Compile(context.Background(), document, config)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("compile diagnostics=%+v", diagnostics)
	}
	scratch, err := cpucompositor.NewScratch(config.Limits.Target, 4)
	if err != nil {
		t.Fatal(err)
	}
	frames := []int64{0, 1, 15, 16, 28, 29, 30, 31, 45, 59}
	type outcome struct {
		frame int64
		hash  string
		err   error
	}
	results := make(chan outcome, len(frames))
	for _, frame := range frames {
		go func(frame int64) {
			at := videoedit.RationalTime{Value: frame, Rate: 30}
			result, renderErr := program.RenderAtWithScratch(context.Background(), "timeline", at, providers, scratch)
			results <- outcome{frame: frame, hash: result.PixelHash, err: renderErr}
		}(frame)
	}
	for range frames {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent frame %d: %v", got.frame, got.err)
		}
		at := videoedit.RationalTime{Value: got.frame, Rate: 30}
		reference, refErr := program.RenderAt(context.Background(), "timeline", at, providers)
		if refErr != nil {
			t.Fatal(refErr)
		}
		if got.hash != reference.PixelHash {
			t.Fatalf("concurrent frame %d diverged: %s vs %s", got.frame, got.hash, reference.PixelHash)
		}
	}
}

// scratchFixture builds one mixed composition: two overlapping image-backed clips plus a
// vector dissolve transition around frames 28-36.
func scratchFixture(t *testing.T) (documentv2.Document, Providers, Config) {
	t.Helper()
	width, height := 24, 18
	backdrop := cpucompositorSurface(t, width, height, 0x11)
	overlay := cpucompositorSurface(t, width, height, 0x33)
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "scratch-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "a", Kind: documentv2.AssetKindImage, URI: "a.png", ContentHash: "sha256:" + repeat('a')},
			{ID: "b", Kind: documentv2.AssetKindImage, URI: "b.png", ContentHash: "sha256:" + repeat('b')},
			{ID: "red", Kind: documentv2.AssetKindVector, Vector: scratchVector("red", 65_535, 20_000, 20_000, width, height)},
			{ID: "blue", Kind: documentv2.AssetKindVector, Vector: scratchVector("blue", 20_000, 20_000, 65_535, width, height)},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "track-a", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "clip-a", AssetID: "a", TimelineRange: scratchRange(0, 60), SourceRange: scratchRange(0, 60)},
			}},
			{ID: "track-b", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "clip-b", AssetID: "b", TimelineRange: scratchRange(20, 40), SourceRange: scratchRange(0, 40)},
			}},
			{ID: "track-vector", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{
				{ID: "outgoing", AssetID: "red", TimelineRange: scratchRange(0, 34), SourceRange: scratchRange(0, 34)},
				{ID: "incoming", AssetID: "blue", TimelineRange: scratchRange(34, 26), SourceRange: scratchRange(0, 26)},
			}},
		}, Transitions: []documentv2.Transition{{
			ID: "dissolve", Profile: documentv2.TransitionDissolveV1,
			OutgoingClipID: "outgoing", IncomingClipID: "incoming",
			PreDuration: videoedit.RationalTime{Value: 4, Rate: 30}, PostDuration: videoedit.RationalTime{Value: 4, Rate: 30},
		}}}},
	}
	// One native provider serving both image assets by request asset id, mirroring the
	// provider-per-request shape the compiled pipeline uses.
	provider := rastercompositor.NativeSourceProviderFunc(func(_ context.Context, request rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
		surface := backdrop
		if request.AssetID == "b" {
			surface = overlay
		}
		hash, err := surface.CanonicalHash()
		if err != nil {
			return rastercompositor.NativeSourceFrame{}, err
		}
		return rastercompositor.NativeSourceFrame{
			ContractVersion: request.ContractVersion, Request: request,
			Width: surface.Width(), Height: surface.Height(), AssetContentHash: request.AssetContentHash,
			PixelHash: hash, DecodeProfile: request.DecodeProfile, SelectionProfile: request.SelectionProfile,
			ColorProfile: request.ColorProfile, NormalizationProfile: request.NormalizationProfile,
			SourceProfile: request.SourceProfile, SurfaceProfile: request.SurfaceProfile, Surface: surface,
		}, nil
	})
	expression, err := expressionlayer.Compile(context.Background(), document, expressionlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	providers := Providers{Native: provider, Expression: expression}
	config := DefaultConfig(width, height)
	return document, providers, config
}

func scratchRange(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{
		Start:    videoedit.RationalTime{Value: start, Rate: 30},
		Duration: videoedit.RationalTime{Value: duration, Rate: 30},
	}
}

func scratchVector(id documentv2.ID, r, g, b uint16, width, height int) *documentv2.VectorSource {
	fill := documentv2.ColorRGBA16{R: r, G: g, B: b, A: 40_000}
	rat := func(n int64) documentv2.Rational { return documentv2.Rational{Numerator: n, Denominator: 1} }
	commands := []documentv2.VectorCommand{
		{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: rat(0), Y: rat(0)}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width)), Y: rat(0)}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width)), Y: rat(int64(height))}}},
		{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(0), Y: rat(int64(height))}}},
		{Op: documentv2.VectorClose},
	}
	return &documentv2.VectorSource{
		Profile:  documentv2.VectorProfilePathViewportPXV2,
		ViewBox:  documentv2.Rect{X: rat(0), Y: rat(0), Width: rat(int64(width)), Height: rat(int64(height))},
		Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
		Paths:    []documentv2.VectorPath{{ID: id + "-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: commands}},
	}
}

func cpucompositorSurface(t *testing.T, width, height int, seed uint16) *cpucompositor.Surface {
	t.Helper()
	surface, err := cpucompositor.NewSurface(width, height)
	if err != nil {
		t.Fatal(err)
	}
	return surface
}

func repeat(char rune) string {
	out := make([]rune, 64)
	for index := range out {
		out[index] = char
	}
	return string(out)
}
