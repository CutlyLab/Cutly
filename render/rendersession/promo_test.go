package rendersession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/render/nativevideo"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/streamencode"
)

// The 20-second high-quality promo acceptance run: 1920x1080 at 30 fps (600 frames), five
// concurrent layers at peak (three image scenes joined by two dissolve transitions, an
// animated 960x540 video hero layer, two vector overlays), rendered through one production
// session with four workers streaming into a resident CRF 16 encoder. The run also proves
// end-to-end determinism on the full artifact and reports comparison slices.
//
// CUTLY_PROMO=1 CUTLY_TEST_FFMPEG=/path/to/zscale+libx264-ffmpeg \
// go test ./render/rendersession -run TestPromo20Seconds -v -timeout 30m
func TestPromo20Seconds(t *testing.T) {
	if os.Getenv("CUTLY_PROMO") != "1" {
		t.Skip("set CUTLY_PROMO=1 to run the 20-second promo acceptance render")
	}
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Fatal("CUTLY_TEST_FFMPEG is required for the promo render (zscale + libx264 build)")
	}
	staged := filepath.Join(t.TempDir(), "ffmpeg-pinned")
	if raw, err := os.ReadFile(executable); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}

	const (
		width, height = 1920, 1080
		frameRate     = int64(30)
		seconds       = int64(20)
		frames        = int(seconds * frameRate)
		framesTotal   = int64(seconds * frameRate)
		heroW, heroH  = 960, 540
	)

	// --- Assets ---------------------------------------------------------------
	assetStart := time.Now()
	sceneA := promoGradientPNG(t, width, height, 0.00, 0.55, 0.95, 0.20) // deep blue -> cyan wash
	sceneB := promoGradientPNG(t, width, height, 0.95, 0.45, 0.10, 0.65) // amber -> violet dusk
	sceneC := promoGradientPNG(t, width, height, 0.10, 0.80, 0.45, 0.35) // emerald -> teal dawn
	heroPath := promoHeroY4M(t, heroW, heroH, frames, int(frameRate))
	assetDuration := time.Since(assetStart)

	// --- Document -------------------------------------------------------------
	document, documentHash, adapter, openRequest := promoDocument(t, width, height, framesTotal, frameRate,
		[][]byte{sceneA, sceneB, sceneC}, heroPath, heroW, heroH)

	pngBindings := []referencevisual.Binding{
		{AssetID: "scene-a", EncodedPNG: sceneA},
		{AssetID: "scene-b", EncodedPNG: sceneB},
		{AssetID: "scene-c", EncodedPNG: sceneC},
	}

	// --- Production render ------------------------------------------------------
	openStart := time.Now()
	source, err := adapter.Open(context.Background(), openRequest)
	if err != nil {
		t.Fatalf("open hero decoder: %v", err)
	}
	defer source.Close()
	decodeSession, err := videodecode.NewSession(source, videodecode.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer decodeSession.Close()
	session, err := OpenVideo(context.Background(), document, pngBindings,
		[]nativevideo.Binding{{DocumentHash: documentHash, AssetID: "hero", Source: decodeSession}},
		referencevisual.DefaultConfigV2(width, height), Options{DeepValidationInterval: 64})
	if err != nil {
		t.Fatalf("open promo session: %v", err)
	}
	defer session.Close()
	openDuration := time.Since(openStart)

	requests := make([]FrameRequest, frames)
	for index := range requests {
		requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index), Rate: frameRate}}
	}

	output := "/tmp/cutly-promo-1080p-20s.mp4"
	os.Remove(output)
	encode := func(path string) (streamencode.Result, time.Duration) {
		t.Helper()
		encoder, err := streamencode.Start(context.Background(), streamencode.Options{
			Executable: staged, OutputPath: path,
			Width: width, Height: height,
			FrameRateNumerator:   frameRate,
			FrameRateDenominator: 1,
			CRF:                  16,
		})
		if err != nil {
			t.Fatalf("start encoder: %v", err)
		}
		start := time.Now()
		result, err := session.EncodeFrames(context.Background(), requests, ParallelOptions{Workers: 4}, encoder)
		elapsed := time.Since(start)
		if err != nil {
			encoder.Abort()
			t.Fatalf("encode promo: %v", err)
		}
		return result, elapsed
	}
	result, renderDuration := encode(output)

	// --- Determinism on the full artifact ---------------------------------------
	verifyStart := time.Now()
	secondPath := filepath.Join(t.TempDir(), "promo-second.mp4")
	second, secondDuration := encode(secondPath)
	_ = time.Since(verifyStart)
	if second.OutputHash != result.OutputHash {
		t.Fatalf("promo determinism drift: %s vs %s", result.OutputHash, second.OutputHash)
	}

	// --- Comparison slices (clearly labeled estimates) ---------------------------
	sliceStart := time.Now()
	for index := 0; index < 8; index++ {
		if _, err := session.RenderFrame(context.Background(), "timeline", videoedit.RationalTime{Value: int64(200 + index), Rate: frameRate}); err != nil {
			t.Fatalf("serial slice frame: %v", err)
		}
	}
	serialSlice := time.Since(sliceStart) / 8

	referenceSliceStart := time.Now()
	for index := 0; index < 3; index++ {
		if _, err := session.reference.RenderAt(context.Background(), "timeline", videoedit.RationalTime{Value: int64(210 + index), Rate: frameRate}); err != nil {
			t.Fatalf("reference slice frame: %v", err)
		}
	}
	referenceSlice := time.Since(referenceSliceStart) / 3

	hits, misses, _ := decodeSession.Statistics()
	t.Logf(`
=== 20s promo acceptance render (1080p, %d frames, 5 peak layers) ===
assets generated (3 gradient PNG + %d-frame hero y4m): %s
session open (compile + decoder open):                  %s
PRODUCTION render+encode (4 workers, CRF 16):           %s (%.1f ms/frame, %.2fx realtime)
determinism re-run (full 600 frames):                   %s (%.1f ms/frame) -> identical sha256
output: %s (%.2f MB, sha256 %s)
hero decode session: %d hits / %d misses, deep validations: %d
--- comparison slices (estimates by extrapolation, same machine) ---
serial production session:   ~%d ms/frame -> ~%.0f s for %d frames
full reference path:         ~%d ms/frame -> ~%.0f s for %d frames
historical host per-frame:   ~471 ms/frame -> ~283 s (measured earlier at this size)`,
		frames, frames,
		assetDuration.Round(time.Millisecond),
		openDuration.Round(time.Millisecond),
		renderDuration, float64(renderDuration.Microseconds())/float64(frames)/1000, float64(seconds)*float64(time.Second)/float64(renderDuration),
		secondDuration, float64(secondDuration.Microseconds())/float64(frames)/1000,
		output, float64(result.OutputBytes)/(1<<20), result.OutputHash,
		hits, misses, session.DeepValidations(),
		serialSlice.Milliseconds(), float64(serialSlice.Milliseconds())*float64(frames)/1000, frames,
		referenceSlice.Milliseconds(), float64(referenceSlice.Milliseconds())*float64(frames)/1000, frames)
	if result.FramesWritten != int64(frames) {
		t.Fatalf("frames written = %d, want %d", result.FramesWritten, frames)
	}
	if info, err := os.Stat(output); err != nil || info.Size() != result.OutputBytes {
		t.Fatalf("output artifact mismatch: %v", err)
	}
}

// promoGradientPNG renders one smooth diagonal gradient with soft vignette — deterministic,
// visually intentional backdrop content (not noise) for quality inspection.
func promoGradientPNG(t *testing.T, width, height int, r0, g0, b0, phase float64) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			u := float64(x) / float64(width-1)
			v := float64(y) / float64(height-1)
			diag := (u + v) / 2
			wave := 0.5 + 0.5*math.Sin(diag*math.Pi*2+phase*math.Pi)
			vignette := 1.0 - 0.35*math.Sqrt((u-0.5)*(u-0.5)+(v-0.5)*(v-0.5))
			channel := func(base float64, offset float64) uint8 {
				return uint8(255 * clamp01(base*vignette+offset*wave*0.35))
			}
			value.SetNRGBA(x, y, stdcolor.NRGBA{R: channel(r0, 0.15), G: channel(g0, 0.10), B: channel(b0, 0.20), A: 254})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, value); err != nil {
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

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// promoHeroY4M generates the animated hero footage: drifting sine-band gradient in Y, gentle
// chroma drift — smooth motion that survives compression visibly.
func promoHeroY4M(t *testing.T, width, height, frames int, frameRate int) string {
	t.Helper()
	path := filepath.Join(realTempDir(t), "hero.y4m")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "YUV4MPEG2 W%d H%d F%d:1 Ip A1:1 C420jpeg\n", width, height, frameRate); err != nil {
		t.Fatal(err)
	}
	yPlane := make([]byte, width*height)
	uPlane := make([]byte, width*height/4)
	vPlane := make([]byte, width*height/4)
	for frame := 0; frame < frames; frame++ {
		if _, err := file.Write([]byte("FRAME\n")); err != nil {
			t.Fatal(err)
		}
		phase := float64(frame) * 2 * math.Pi / 150 // one full drift every five seconds
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				u := float64(x) / float64(width)
				v := float64(y) / float64(height)
				band := 0.5 + 0.5*math.Sin((u*4+math.Sin(phase)*0.8)*math.Pi+v*phase*0.5)
				yPlane[y*width+x] = byte(40 + 160*band)
			}
		}
		for y := 0; y < height/2; y++ {
			for x := 0; x < width/2; x++ {
				uPlane[y*(width/2)+x] = byte(110 + 40*math.Sin(phase+float64(x)/40))
				vPlane[y*(width/2)+x] = byte(140 + 40*math.Cos(phase*0.7+float64(y)/30))
			}
		}
		if _, err := file.Write(yPlane); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(uPlane); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(vPlane); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// promoDocument assembles the promo composition and the hero decoder's frozen artifacts.
func promoDocument(t *testing.T, width, height int, frames int64, frameRate int64, scenes [][]byte, heroPath string, heroW, heroH int) (documentv2.Document, string, *videodecode.Adapter, videodecode.OpenRequest) {
	t.Helper()
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	staged := filepath.Join(t.TempDir(), "ffmpeg-pinned-decoder")
	if raw, err := os.ReadFile(executable); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}

	sceneHashes := make([]string, len(scenes))
	for index, scene := range scenes {
		sceneHashes[index] = promoHash(scene)
	}
	heroRaw, err := os.ReadFile(heroPath)
	if err != nil {
		t.Fatal(err)
	}
	heroHash := promoHash(heroRaw)

	// Hero decoder artifacts: manifest, complete all-keyframe index, semantics evidence.
	durationPTS := frames
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "hero", ContentHash: heroHash, SizeBytes: int64(len(heroRaw))},
		Probe:         probe.ProbeIdentity{Implementation: "promo", BuildFingerprint: "sha256:" + strings.Repeat("a", 64), OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container:     probe.ContainerInfo{FormatName: "yuv4mpegpipe", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:      videoedit.RationalTime{Value: frames, Rate: frameRate},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "rawvideo", TimeBase: videoedit.RationalTime{Value: 1, Rate: frameRate}, StartPTS: 0, DurationPTS: &durationPTS,
			Video: &probe.VideoStream{
				Width: heroW, Height: heroH, RotationDegrees: 0,
				SampleAspectRatio: &probe.Ratio{Numerator: 1, Denominator: 1}, DisplayAspectRatio: &probe.Ratio{Numerator: int64(heroW), Denominator: int64(heroH)},
				PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateVFR, AverageFrameRate: &videoedit.RationalTime{Value: frameRate, Rate: 1},
				Color: probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
	keyframes := make([]int64, frames)
	records := make([]videodecode.FrameRecord, frames)
	for frame := range records {
		records[frame] = videodecode.FrameRecord{PTS: int64(frame), DurationPTS: 1, Keyframe: true}
		keyframes[frame] = int64(frame)
	}
	manifest.Streams[0].Video.Keyframes = promoKeyframeSummary(keyframes)
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	index := videodecode.FrameIndex{SchemaVersion: videodecode.FrameIndexSchemaVersion, Profile: videodecode.FrameIndexProfileV1,
		ProbeFingerprint: probeFingerprint, ResourceHash: heroHash, StreamIndex: 0,
		TimeBase: videoedit.RationalTime{Value: 1, Rate: frameRate}, Frames: records}
	indexFingerprint, err := videodecode.FrameIndexFingerprint(index, int(frames)+16)
	if err != nil {
		t.Fatal(err)
	}
	evidence, evidenceFingerprint, err := videodecode.NewVideoSemanticsEvidence(context.Background(), manifest, 0,
		"promo-scanner", "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64),
		videodecode.VideoSemanticsObservations{ScanComplete: true, ProgressiveOnly: true, StaticColorOnly: true, RotationOnlyMatrix: true, RotationDegrees: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := videodecode.New(context.Background(), staged, videodecode.DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), videodecode.Options{MaxInputBytes: 1 << 30, MaxFrameBytes: 1 << 28, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: int(frames) + 16, MaxWidth: 4096, MaxHeight: 4096, MaxDecodeMillis: 120_000})
	if err != nil {
		t.Fatalf("pinned decoder incompatible: %v", err)
	}
	openRequest := videodecode.OpenRequest{Path: heroPath,
		Manifest: manifest, Index: index, IndexFingerprint: indexFingerprint,
		Semantics: evidence, SemanticsFingerprint: evidenceFingerprint}

	// Scene cuts at 6.5s and 13.5s with 0.5s dissolves.
	rat := func(n int64) documentv2.Rational { return documentv2.Rational{Numerator: n, Denominator: 1} }
	span := func(start, duration int64) documentv2.TimeRange {
		return documentv2.TimeRange{Start: videoedit.RationalTime{Value: start, Rate: frameRate}, Duration: videoedit.RationalTime{Value: duration, Rate: frameRate}}
	}
	vector := func(id documentv2.ID, r, g, b, alpha uint16) documentv2.Asset {
		fill := documentv2.ColorRGBA16{R: r, G: g, B: b, A: alpha}
		return documentv2.Asset{ID: id, Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
			Profile:  documentv2.VectorProfilePathViewportPXV2,
			ViewBox:  documentv2.Rect{X: rat(0), Y: rat(0), Width: rat(int64(width)), Height: rat(int64(height))},
			Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
			Paths: []documentv2.VectorPath{{ID: id + "-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill,
				Commands: []documentv2.VectorCommand{
					{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: rat(120), Y: rat(120)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width) - 120), Y: rat(120)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width) - 120), Y: rat(int64(height) - 120)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(120), Y: rat(int64(height) - 120)}}},
					{Op: documentv2.VectorClose},
				}}}},
		}
	}
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "promo-20s", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "scene-a", Kind: documentv2.AssetKindImage, URI: "a.png", ContentHash: sceneHashes[0]},
			{ID: "scene-b", Kind: documentv2.AssetKindImage, URI: "b.png", ContentHash: sceneHashes[1]},
			{ID: "scene-c", Kind: documentv2.AssetKindImage, URI: "c.png", ContentHash: sceneHashes[2]},
			{ID: "hero", Kind: documentv2.AssetKindVideo, URI: "hero.y4m", ContentHash: heroHash,
				Duration: &videoedit.RationalTime{Value: frames, Rate: frameRate}},
			vector("overlay-warm", 65_535, 49_151, 13_107, 13_000),
			vector("overlay-cool", 13_107, 39_321, 65_535, 10_000),
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "scene-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
				{ID: "clip-a", AssetID: "scene-a", TimelineRange: span(0, 195), SourceRange: span(0, 195)},
				{ID: "clip-b", AssetID: "scene-b", TimelineRange: span(195, 210), SourceRange: span(0, 210)},
				{ID: "clip-c", AssetID: "scene-c", TimelineRange: span(405, 195), SourceRange: span(0, 195)},
			}},
			{ID: "hero-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "hero-clip", AssetID: "hero", TimelineRange: span(0, frames), SourceRange: span(0, frames),
			}}},
			{ID: "overlay-warm-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{
				ID: "overlay-warm-clip", AssetID: "overlay-warm", TimelineRange: span(0, frames), SourceRange: span(0, 30),
			}}},
			{ID: "overlay-cool-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{
				ID: "overlay-cool-clip", AssetID: "overlay-cool", TimelineRange: span(30, frames-60), SourceRange: span(0, 30),
			}}},
		}, Transitions: []documentv2.Transition{
			{ID: "dissolve-1", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "clip-a", IncomingClipID: "clip-b",
				PreDuration: videoedit.RationalTime{Value: 15, Rate: frameRate}, PostDuration: videoedit.RationalTime{Value: 15, Rate: frameRate}},
			{ID: "dissolve-2", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "clip-b", IncomingClipID: "clip-c",
				PreDuration: videoedit.RationalTime{Value: 15, Rate: frameRate}, PostDuration: videoedit.RationalTime{Value: 15, Rate: frameRate}},
		}}},
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	return document, documentHash, adapter, openRequest
}

func promoHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func promoKeyframeSummary(values []int64) probe.KeyframeSummary {
	canonical, err := videoedit.CanonicalJSON(struct {
		Profile     string  `json:"profile"`
		StreamIndex int     `json:"stream_index"`
		PTS         []int64 `json:"pts"`
	}{Profile: "cutly.keyframe-index.v1", StreamIndex: 0, PTS: values})
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(canonical)
	summary := probe.KeyframeSummary{Complete: true, Count: int64(len(values)), IndexHash: "sha256:" + hex.EncodeToString(digest[:])}
	if len(values) > 0 {
		first, last := values[0], values[len(values)-1]
		summary.FirstPTS, summary.LastPTS = &first, &last
	}
	return summary
}
