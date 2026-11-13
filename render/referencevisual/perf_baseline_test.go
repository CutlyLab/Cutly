package referencevisual

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

// The perf baseline reproduces, at realistic sizes inside this package, the cost structure of a
// host reference-visual export: one mixed Document v2 (two native PNG assets plus three vector
// assets), one canonical decode and compile, then a per-frame render loop. It measures the
// segments the optimization plan calls for (decode, compile, per-frame evaluation/composite,
// validation, color+PNG output), the no-session host pattern that re-decodes and re-compiles for
// every frame, and per-frame P50/P95/max plus allocation and peak-RSS observations.
//
// The path measured here intentionally contains no audio and no FFmpeg stage: the reference-visual
// export boundary is visual-only, and encoder cost belongs to later production profiles.
//
// Opt-in because a full run takes minutes:
//
//	CUTLY_PERF_BASELINE=1 go test ./render/referencevisual -run TestPerfBaselineReport -v
//
// Optional overrides: CUTLY_PERF_FRAMES (default 150), CUTLY_PERF_WIDTH/CUTLY_PERF_HEIGHT
// (default 1920x1080), CUTLY_PERF_REPORT (write a JSON report to this path).
func TestPerfBaselineReport(t *testing.T) {
	if os.Getenv("CUTLY_PERF_BASELINE") != "1" {
		t.Skip("set CUTLY_PERF_BASELINE=1 to run the perf baseline")
	}
	ctx := context.Background()
	width := perfEnvInt("CUTLY_PERF_WIDTH", 1920)
	height := perfEnvInt("CUTLY_PERF_HEIGHT", 1080)
	frames := perfEnvInt("CUTLY_PERF_FRAMES", 150)
	frameRate := int64(30)
	report := perfReport{
		Profile: "cutly.reference-visual.perf-baseline.v1",
		Width:   width, Height: height, Frames: frames, FrameRate: frameRate,
		GoVersion: runtime.Version(),
		Segments:  map[string]*perfSegment{},
	}

	backdrop := perfNoisePNG(t, width, height, 0x2545F4914F6CDD1D)
	overlay := perfNoisePNG(t, width/2, height/2, 0x9E3779B97F4A7C15)
	document := perfDocument(t, width, height, frameRate, int64(frames), contentHash(backdrop), contentHash(overlay))
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	bindings := []Binding{
		{AssetID: "perf-image-backdrop", EncodedPNG: backdrop},
		{AssetID: "perf-image-overlay", EncodedPNG: overlay},
	}
	config := DefaultConfig(width, height)

	// Cold segments: three independent iterations so cold-cache effects are visible.
	for iteration := 0; iteration < 3; iteration++ {
		perfSample(t, report.Segments, "cold/document_decode", func() {
			if _, err := documentv2.Decode(canonical); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "cold/document_canonical_json", func() {
			if _, err := documentv2.CanonicalJSON(document); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "cold/visualcompositor_compile", func() {
			if program, diagnostics := visualcompositor.Compile(ctx, document, visualcompositor.DefaultConfig(width, height)); program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("visual compile diagnostics=%+v", diagnostics)
			}
		})
		frozen, err := documentv2.Decode(canonical)
		if err != nil {
			t.Fatal(err)
		}
		nativeBindings := []nativepngsource.Binding{
			{DocumentHash: contentHash(canonical), AssetID: "perf-image-backdrop", EncodedPNG: backdrop},
			{DocumentHash: contentHash(canonical), AssetID: "perf-image-overlay", EncodedPNG: overlay},
		}
		perfSample(t, report.Segments, "cold/nativepngsource_compile", func() {
			if _, err := nativepngsource.Compile(ctx, frozen, nativeBindings, nativepngsource.Config{Limits: nativepngsource.DefaultLimits()}); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "cold/expressionlayer_compile", func() {
			if _, err := expressionlayer.Compile(ctx, frozen, expressionlayer.Config{Limits: expressionlayer.DefaultLimits()}); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "cold/referencevisual_compile", func() {
			program, diagnostics, err := Compile(ctx, document, bindings, config)
			if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("compile program=%v diagnostics=%+v err=%v", program, diagnostics, err)
			}
		})
	}

	// Warm session: compile once, render every frame through the three nested boundaries.
	program, diagnostics, err := Compile(ctx, document, bindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("warm compile diagnostics=%+v err=%v", diagnostics, err)
	}
	var reference Result
	for index := int64(0); index < int64(frames); index++ {
		at := documentv2.RationalTime{Value: index, Rate: frameRate}
		frameIndex := index
		perfSample(t, report.Segments, "warm/frame_render_at", func() {
			result, renderErr := program.RenderAt(ctx, "timeline", at)
			if renderErr != nil || result.PNG.ArtifactHash == "" {
				t.Fatalf("RenderAt(%d) err=%v", frameIndex, renderErr)
			}
			reference = result
		})
		perfSample(t, report.Segments, "warm/frame_render_surface_at", func() {
			if _, renderErr := program.RenderSurfaceAt(ctx, "timeline", at); renderErr != nil {
				t.Fatalf("RenderSurfaceAt(%d) err=%v", frameIndex, renderErr)
			}
		})
		perfSample(t, report.Segments, "warm/frame_visual_raw", func() {
			if _, renderErr := program.visual.RenderAt(ctx, "timeline", at, program.providers()); renderErr != nil {
				t.Fatalf("raw visual RenderAt(%d) err=%v", frameIndex, renderErr)
			}
		})
	}

	// No-session host pattern: a host that re-runs decode plus the complete compile for every
	// frame. This is the per-frame cost the RenderSession work removes on the host side.
	noSessionFrames := frames
	if noSessionFrames > 20 {
		noSessionFrames = 20
	}
	for index := int64(0); index < int64(noSessionFrames); index++ {
		at := documentv2.RationalTime{Value: index, Rate: frameRate}
		perfSample(t, report.Segments, "nosession/frame_decode_compile_render", func() {
			decoded, decodeErr := documentv2.Decode(canonical)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			session, compileDiagnostics, compileErr := Compile(ctx, decoded, bindings, config)
			if compileErr != nil || session == nil || documentv2.HasErrorDiagnostics(compileDiagnostics) {
				t.Fatalf("nosession compile err=%v", compileErr)
			}
			if _, renderErr := session.RenderAt(ctx, "timeline", at); renderErr != nil {
				t.Fatalf("nosession render err=%v", renderErr)
			}
		})
	}

	// Micro segments isolate the repeated hashing and output conversion costs.
	surface := reference.Visual.Surface
	output := reference.Output
	for sample := 0; sample < 5; sample++ {
		perfSample(t, report.Segments, "micro/surface_canonical_hash", func() {
			if _, err := surface.CanonicalHash(); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "micro/convert_surface_rgba8", func() {
			if _, err := color.ConvertSurfaceToRGBA8(ctx, surface, color.DefaultOutputLimits()); err != nil {
				t.Fatal(err)
			}
		})
		perfSample(t, report.Segments, "micro/png_encode", func() {
			if _, err := pngencode.Encode(ctx, output, pngencode.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)
	for index := int64(0); index < int64(frames); index++ {
		at := documentv2.RationalTime{Value: index, Rate: frameRate}
		if _, err := program.RenderAt(ctx, "timeline", at); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&memAfter)
	report.AllocatedBytesPerFrame = int64(memAfter.TotalAlloc-memBefore.TotalAlloc) / int64(frames)

	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil {
		report.PeakRSSBytes = usage.Maxrss // darwin reports bytes; linux reports KiB, so only compare within one host OS
	}
	perfSummarize(t, &report)

	if path := os.Getenv("CUTLY_PERF_REPORT"); path != "" {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("report written to %s", path)
	}
}

type perfSegment struct {
	Count           int     `json:"count"`
	TotalMillis     float64 `json:"total_millis"`
	MeanMillis      float64 `json:"mean_millis"`
	P50Millis       float64 `json:"p50_millis"`
	P95Millis       float64 `json:"p95_millis"`
	MaxMillis       float64 `json:"max_millis"`
	TotalAllocBytes int64   `json:"total_alloc_bytes"`
	MeanAllocBytes  int64   `json:"mean_alloc_bytes"`
	samples         []time.Duration
	allocSamples    []int64
}

type perfReport struct {
	Profile                string                  `json:"profile"`
	GoVersion              string                  `json:"go_version"`
	Width                  int                     `json:"width"`
	Height                 int                     `json:"height"`
	Frames                 int                     `json:"frames"`
	FrameRate              int64                   `json:"frame_rate"`
	AllocatedBytesPerFrame int64                   `json:"allocated_bytes_per_frame"`
	PeakRSSBytes           int64                   `json:"peak_rss_bytes"`
	Segments               map[string]*perfSegment `json:"segments"`
}

func perfSample(t *testing.T, segments map[string]*perfSegment, name string, body func()) {
	t.Helper()
	if segments == nil {
		// The map is created by the caller before the first sample; guard misuse early.
		t.Fatal("perf segments map is not initialized")
	}
	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	start := time.Now()
	body()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)
	segment := segments[name]
	if segment == nil {
		segment = &perfSegment{}
		segments[name] = segment
	}
	segment.samples = append(segment.samples, elapsed)
	segment.allocSamples = append(segment.allocSamples, int64(memAfter.TotalAlloc-memBefore.TotalAlloc))
}

func perfSummarize(t *testing.T, report *perfReport) {
	t.Helper()
	names := make([]string, 0, len(report.Segments))
	for name := range report.Segments {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("perf baseline %dx%d %d frames @ %dfps (alloc/frame ≈ %d bytes, peak RSS %d bytes)",
		report.Width, report.Height, report.Frames, report.FrameRate, report.AllocatedBytesPerFrame, report.PeakRSSBytes)
	t.Logf("%-38s %5s %9s %9s %9s %9s %11s", "segment", "count", "mean", "p50", "p95", "max", "alloc/sample")
	for _, name := range names {
		segment := report.Segments[name]
		count := len(segment.samples)
		ordered := append([]time.Duration(nil), segment.samples...)
		sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
		var total time.Duration
		for _, sample := range ordered {
			total += sample
		}
		pick := func(fraction float64) time.Duration {
			if count == 0 {
				return 0
			}
			index := int(fraction * float64(count-1))
			return ordered[index]
		}
		var allocTotal int64
		for _, alloc := range segment.allocSamples {
			allocTotal += alloc
		}
		segment.Count = count
		segment.TotalMillis = millis(total)
		segment.MeanMillis = millis(total) / float64(count)
		segment.P50Millis = millis(pick(0.50))
		segment.P95Millis = millis(pick(0.95))
		segment.MaxMillis = millis(ordered[count-1])
		segment.TotalAllocBytes = allocTotal
		if count > 0 {
			segment.MeanAllocBytes = allocTotal / int64(count)
		}
		t.Logf("%-38s %5d %8.1fms %8.1fms %8.1fms %8.1fms %11d",
			name, count, segment.MeanMillis, segment.P50Millis, segment.P95Millis, segment.MaxMillis, segment.MeanAllocBytes)
	}
}

func millis(value time.Duration) float64 {
	return float64(value.Nanoseconds()) / 1e6
}

func perfEnvInt(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		var value int
		if _, err := fmt.Sscanf(raw, "%d", &value); err == nil && value > 0 {
			return value
		}
	}
	return fallback
}

// perfNoisePNG encodes a deterministic full-alpha-variance noise image. Solid-color fixtures would
// understate decode, hash and encode costs, so the generator keeps every channel noisy while
// staying reproducible from one seed. The encoded bytes carry the sRGB chunk the strict PNG
// profile requires, mirroring strictPNG.
func perfNoisePNG(t *testing.T, width, height int, seed uint64) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	state := seed
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := perfNextByte(&state)
			g := perfNextByte(&state)
			b := perfNextByte(&state)
			a := perfNextByte(&state)
			value.SetNRGBA(x, y, stdcolor.NRGBA{R: r, G: g, B: b, A: a})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, forceAlphaImage{NRGBA: value}); err != nil {
		t.Fatal(err)
	}
	encodedBytes := encoded.Bytes()
	length := int(binary.BigEndian.Uint32(encodedBytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = 0
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(encodedBytes)+len(chunk))
	result = append(result, encodedBytes[:end]...)
	result = append(result, chunk...)
	return append(result, encodedBytes[end:]...)
}

func perfNextByte(state *uint64) uint8 {
	*state = *state*6364136223846793005 + 1442695040888963407
	return uint8(*state >> 56)
}

// perfDocument builds the baseline composition: a full-frame backdrop image, a half-size overlay
// image, and three overlapping vector layers whose viewports match the target raster.
func perfDocument(t *testing.T, width, height int, frameRate, frames int64, backdropHash, overlayHash string) documentv2.Document {
	t.Helper()
	second := func(seconds int64) documentv2.TimeRange {
		return documentv2.TimeRange{
			Start:    documentv2.RationalTime{Value: seconds * frameRate, Rate: frameRate},
			Duration: documentv2.RationalTime{Value: frameRate, Rate: frameRate},
		}
	}
	rangeFrames := func(start, duration int64) documentv2.TimeRange {
		if duration < 1 {
			duration = 1
		}
		if start < 0 {
			start = 0
		}
		return documentv2.TimeRange{
			Start:    documentv2.RationalTime{Value: start, Rate: frameRate},
			Duration: documentv2.RationalTime{Value: duration, Rate: frameRate},
		}
	}
	vector := func(id documentv2.ID, seed uint64, fill documentv2.ColorRGBA16) documentv2.Asset {
		commands := []documentv2.VectorCommand{{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{perfPoint(seed, 0, int64(width), int64(height))}}}
		for step := 1; step <= 48; step++ {
			commands = append(commands, documentv2.VectorCommand{Op: documentv2.VectorLineTo, Points: []documentv2.Point{perfPoint(seed+uint64(step)*0x9E3779B9, int64(step), int64(width), int64(height))}})
		}
		commands = append(commands, documentv2.VectorCommand{Op: documentv2.VectorClose})
		return documentv2.Asset{ID: id, Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
			Profile:  documentv2.VectorProfilePathViewportPXV2,
			ViewBox:  documentv2.Rect{X: rat(0, 1), Y: rat(0, 1), Width: rat(int64(width), 1), Height: rat(int64(height), 1)},
			Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
			Paths:    []documentv2.VectorPath{{ID: id + "-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: commands}},
		}}
	}
	solid := func(r, g, b uint16, alpha uint16) documentv2.ColorRGBA16 {
		return documentv2.ColorRGBA16{R: r, G: g, B: b, A: alpha}
	}
	return documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "perf-baseline-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "perf-image-backdrop", Kind: documentv2.AssetKindImage, URI: "backdrop.png", ContentHash: backdropHash},
			{ID: "perf-image-overlay", Kind: documentv2.AssetKindImage, URI: "overlay.png", ContentHash: overlayHash},
			vector("perf-vector-a", 0x1234567, solid(60_000, 20_000, 20_000, 32_000)),
			vector("perf-vector-b", 0x7654321, solid(20_000, 60_000, 20_000, 24_000)),
			vector("perf-vector-c", 0xAAA5555, solid(20_000, 20_000, 60_000, 16_000)),
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			// Clips on one track must not overlap, so each layered asset occupies its own track.
			{ID: "image-track-backdrop", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "backdrop-clip", AssetID: "perf-image-backdrop", TimelineRange: rangeFrames(0, frames), SourceRange: rangeFrames(0, frames)},
			}},
			{ID: "image-track-overlay", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "overlay-clip", AssetID: "perf-image-overlay", TimelineRange: rangeFrames(frameRate, frames-2*frameRate), SourceRange: rangeFrames(0, frames-2*frameRate)},
			}},
			{ID: "vector-track-a", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{
				{ID: "vector-clip-a", AssetID: "perf-vector-a", TimelineRange: rangeFrames(0, frames), SourceRange: second(0)},
			}},
			{ID: "vector-track-b", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{
				{ID: "vector-clip-b", AssetID: "perf-vector-b", TimelineRange: rangeFrames(frameRate/2, frames-frameRate), SourceRange: second(0)},
			}},
			{ID: "vector-track-c", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{
				{ID: "vector-clip-c", AssetID: "perf-vector-c", TimelineRange: rangeFrames(frameRate, frames-2*frameRate), SourceRange: second(0)},
			}},
		}}},
	}
}

func perfPoint(seed uint64, step, width, height int64) documentv2.Point {
	state := seed + uint64(step)*0x9E3779B97F4A7C15
	x := perfNextByte(&state)
	y := perfNextByte(&state)
	xNumerator, xDenominator := perfReduced(int64(x)*width, int64(width)*256)
	yNumerator, yDenominator := perfReduced(int64(y)*height, int64(height)*256)
	return point(xNumerator, xDenominator, yNumerator, yDenominator)
}

// perfReduced returns numerator/denominator in the canonical reduced form Document v2 requires.
func perfReduced(numerator, denominator int64) (int64, int64) {
	if numerator == 0 {
		return 0, 1
	}
	if denominator < 0 {
		numerator, denominator = -numerator, -denominator
	}
	divisor := numerator
	if divisor < 0 {
		divisor = -divisor
	}
	for remainder := denominator; remainder != 0; {
		divisor, remainder = remainder, divisor%remainder
	}
	return numerator / divisor, denominator / divisor
}
