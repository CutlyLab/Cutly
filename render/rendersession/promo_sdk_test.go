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

// TestSDKPromo renders a 20-second promotional video entirely through the Cutly SDK pipeline:
// multi-layer composition (image + video), dissolve transitions, bounded parallel frame
// rendering, and a resident streaming encoder. The assets are pre-rendered (title cards,
// feature callouts, closing card) plus the real demo video as a video layer.
//
//	CUTLY_PROMO=1 CUTLY_TEST_FFMPEG=/path/to/zscale+libx264-ffmpeg \
//	  go test ./render/rendersession -run TestSDKPromo -v -timeout 30m
func TestSDKPromo(t *testing.T) {
	if os.Getenv("CUTLY_PROMO") != "1" {
		t.Skip("set CUTLY_PROMO=1 to run the SDK promo render")
	}
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Fatal("CUTLY_TEST_FFMPEG is required")
	}
	assetDir := os.Getenv("CUTLY_PROMO_ASSETS")
	if assetDir == "" {
		assetDir = "/tmp/promo-sdk"
	}

	const (
		width, height = 1920, 1080
		frameRate     = int64(24)
		totalFrames   = int(frameRate * 20) // 480 frames = 20s
	)

	// --- Load and normalize assets ---------------------------------------
	assetStart := time.Now()

	titlePNG := loadStrictPNG(t, filepath.Join(assetDir, "title-raw.png"))
	feat1PNG := loadStrictPNG(t, filepath.Join(assetDir, "feat1-raw.png"))
	feat2PNG := loadStrictPNG(t, filepath.Join(assetDir, "feat2-raw.png"))
	feat3PNG := loadStrictPNG(t, filepath.Join(assetDir, "feat3-raw.png"))
	closingPNG := loadStrictPNG(t, filepath.Join(assetDir, "closing-raw.png"))

	heroPath, err := filepath.EvalSymlinks(filepath.Join(assetDir, "hero.y4m"))
	if err != nil {
		t.Fatalf("resolve hero path: %v (generate assets first)", err)
	}
	heroRaw, err := os.ReadFile(heroPath)
	if err != nil {
		t.Fatalf("hero y4m: %v (generate assets first with the ffmpeg pre-step)", err)
	}
	heroHash := sdkPromoHash(heroRaw)

	// Hero video is 960x540 at 24fps
	const heroW, heroH = 960, 540
	heroInfo, err := probeY4M(heroRaw)
	if err != nil {
		t.Fatal(err)
	}
	_ = heroInfo

	// --- Build the decoder artifacts ---------------------------------------
	decoderStaged := stageDecoder(t, executable)

	heroFrames := int64(144) // 6s at 24fps from the demo
	manifest := buildHeroManifest(heroHash, int64(len(heroRaw)), heroW, heroH, heroFrames, frameRate)
	records := make([]videodecode.FrameRecord, heroFrames)
	keyframes := make([]int64, heroFrames)
	for i := range records {
		records[i] = videodecode.FrameRecord{PTS: int64(i), DurationPTS: 1, Keyframe: true}
		keyframes[i] = int64(i)
	}
	manifest.Streams[0].Video.Keyframes = promoKeyframeSummary(keyframes)
	probeFP, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	index := videodecode.FrameIndex{
		SchemaVersion: videodecode.FrameIndexSchemaVersion, Profile: videodecode.FrameIndexProfileV1,
		ProbeFingerprint: probeFP, ResourceHash: heroHash, StreamIndex: 0,
		TimeBase: videoedit.RationalTime{Value: 1, Rate: frameRate}, Frames: records,
	}
	indexFP, err := videodecode.FrameIndexFingerprint(index, int(heroFrames)+16)
	if err != nil {
		t.Fatal(err)
	}
	evidence, evidenceFP, err := videodecode.NewVideoSemanticsEvidence(context.Background(), manifest, 0,
		"promo", "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64),
		videodecode.VideoSemanticsObservations{ScanComplete: true, ProgressiveOnly: true, StaticColorOnly: true, RotationOnlyMatrix: true, RotationDegrees: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := videodecode.New(context.Background(), decoderStaged, videodecode.DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), videodecode.Options{MaxInputBytes: 1 << 30, MaxFrameBytes: 1 << 28, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: int(heroFrames) + 16, MaxWidth: 4096, MaxHeight: 4096, MaxDecodeMillis: 120_000})
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	openRequest := videodecode.OpenRequest{Path: heroPath, Manifest: manifest, Index: index,
		IndexFingerprint: indexFP, Semantics: evidence, SemanticsFingerprint: evidenceFP}
	assetDuration := time.Since(assetStart)

	// --- Document: 6 scenes, dissolve transitions -------------------------
	// Timeline at 24fps: title(96) -> video(144) -> feat1(80) -> feat2(80) -> feat3(40) -> closing(40)
	// With 12-frame dissolves between each pair.
	// Scene durations chosen so the hero video (144 source frames) has enough head and
	// tail handles for 12-frame dissolve transitions on both sides: 12 head + 120 body +
	// 12 tail = 144 exactly. Images have unlimited source.
	scenes := []struct {
		id      documentv2.ID
		assetID documentv2.ID
		frames  int64
	}{
		{"title", "card-title", 96},     // 4s
		{"hero-video", "hero", 120},     // 5s (source: frames 12-131 + handles)
		{"feat1", "card-feat1", 96},     // 4s
		{"feat2", "card-feat2", 72},     // 3s
		{"feat3", "card-feat3", 48},     // 2s
		{"closing", "card-closing", 48}, // 2s
	}

	span := func(start, duration int64) documentv2.TimeRange {
		return documentv2.TimeRange{Start: videoedit.RationalTime{Value: start, Rate: frameRate}, Duration: videoedit.RationalTime{Value: duration, Rate: frameRate}}
	}

	const dissolveFrames = int64(12) // 0.5s at 24fps
	var clips []documentv2.Clip
	var transitions []documentv2.Transition
	var offset int64
	for i, scene := range scenes {
		// Source ranges must provide dissolve handles:
		// - incoming clips need PreDuration frames of source before their body
		// - outgoing clips need PostDuration frames of source after their body
		// Images have unlimited source; the hero video has exactly 144 frames.
		head := int64(0)
		if i > 0 {
			head = dissolveFrames // incoming transition pre-blend
		}
		// Source range covers the body; transition handles come from source media
		// around it (the hero's 144 frames accommodate 12 head + 120 body + 12 tail).
		sourceStart := head
		sourceDuration := scene.frames
		clips = append(clips, documentv2.Clip{
			ID: scene.id, AssetID: scene.assetID,
			TimelineRange: span(offset, scene.frames),
			SourceRange:   span(sourceStart, sourceDuration),
		})
		if i < len(scenes)-1 {
			nextID := scenes[i+1].id
			transitions = append(transitions, documentv2.Transition{
				ID: documentv2.ID(fmt.Sprintf("dissolve-%d", i)), Profile: documentv2.TransitionDissolveV1,
				OutgoingClipID: scene.id, IncomingClipID: nextID,
				PreDuration:  videoedit.RationalTime{Value: dissolveFrames, Rate: frameRate},
				PostDuration: videoedit.RationalTime{Value: dissolveFrames, Rate: frameRate},
			})
		}
		offset += scene.frames
	}

	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "sdk-promo", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "card-title", Kind: documentv2.AssetKindImage, URI: "title.png", ContentHash: sdkPromoHash(titlePNG)},
			{ID: "card-feat1", Kind: documentv2.AssetKindImage, URI: "feat1.png", ContentHash: sdkPromoHash(feat1PNG)},
			{ID: "card-feat2", Kind: documentv2.AssetKindImage, URI: "feat2.png", ContentHash: sdkPromoHash(feat2PNG)},
			{ID: "card-feat3", Kind: documentv2.AssetKindImage, URI: "feat3.png", ContentHash: sdkPromoHash(feat3PNG)},
			{ID: "card-closing", Kind: documentv2.AssetKindImage, URI: "closing.png", ContentHash: sdkPromoHash(closingPNG)},
			{ID: "hero", Kind: documentv2.AssetKindVideo, URI: "hero.y4m", ContentHash: heroHash,
				Duration: &videoedit.RationalTime{Value: heroFrames, Rate: frameRate}},
		},
		Timelines: []documentv2.Timeline{{
			ID: "timeline",
			Tracks: []documentv2.Track{{
				ID: "scene-track", Kind: documentv2.TrackKindVideo, Clips: clips,
			}},
			Transitions: transitions,
		}},
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}

	// --- Render through the SDK ------------------------------------------
	renderStart := time.Now()

	source, err := adapter.Open(context.Background(), openRequest)
	if err != nil {
		t.Fatalf("open hero: %v", err)
	}
	defer source.Close()
	decodeSession, err := videodecode.NewSession(source, videodecode.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer decodeSession.Close()

	pngBindings := []referencevisual.Binding{
		{AssetID: "card-title", EncodedPNG: titlePNG},
		{AssetID: "card-feat1", EncodedPNG: feat1PNG},
		{AssetID: "card-feat2", EncodedPNG: feat2PNG},
		{AssetID: "card-feat3", EncodedPNG: feat3PNG},
		{AssetID: "card-closing", EncodedPNG: closingPNG},
	}
	videoBindings := []nativevideo.Binding{
		{DocumentHash: documentHash, AssetID: "hero", Source: decodeSession},
	}

	session, err := OpenVideo(context.Background(), document, pngBindings, videoBindings,
		referencevisual.DefaultConfigV2(width, height), Options{DeepValidationInterval: 48})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()
	openDuration := time.Since(renderStart)

	requests := make([]FrameRequest, totalFrames)
	for i := range requests {
		requests[i] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(i), Rate: frameRate}}
	}

	output := "/tmp/cutly-sdk-promo.mp4"
	os.Remove(output)
	encoder, err := streamencode.Start(context.Background(), streamencode.Options{
		Executable: stageEncoder(t, executable), OutputPath: output,
		Width: width, Height: height,
		FrameRateNumerator:   frameRate,
		FrameRateDenominator: 1,
		CRF:                  16,
	})
	if err != nil {
		t.Fatalf("start encoder: %v", err)
	}
	encodeStart := time.Now()
	result, err := session.EncodeFrames(context.Background(), requests, ParallelOptions{Workers: 4}, encoder)
	encodeDuration := time.Since(encodeStart)
	if err != nil {
		encoder.Abort()
		t.Fatalf("encode: %v", err)
	}

	hits, misses, _ := decodeSession.Statistics()
	t.Logf(`
=== SDK Promo (1080p, %d frames, %d scenes with dissolves) ===
assets loaded:       %s
session open:        %s
RENDER+ENCODE:       %s (%.1f ms/frame, %.1fx realtime)
output:              %s (%.2f MB, CRF %d)
frames written:      %d
hero decode session: %d hits / %d misses
deep validations:    %d`,
		totalFrames, len(scenes),
		assetDuration.Round(time.Millisecond),
		openDuration.Round(time.Millisecond),
		encodeDuration, float64(encodeDuration.Microseconds())/float64(totalFrames)/1000,
		float64(20*time.Second)/float64(encodeDuration),
		output, float64(result.OutputBytes)/(1<<20), result.QualityCRF,
		result.FramesWritten,
		hits, misses,
		session.DeepValidations())

	if result.FramesWritten != int64(totalFrames) {
		t.Fatalf("frames = %d, want %d", result.FramesWritten, totalFrames)
	}
}

// --- Helpers --------------------------------------------------------------

// loadStrictPNG reads a PNG, converts it to RGBA with alpha, and injects the sRGB chunk
// the strict decoder requires.
func loadStrictPNG(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (generate assets first)", path, err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	bounds := img.Bounds()
	rgba := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			rgba.SetNRGBA(x, y, stdcolor.NRGBA{
				R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255,
			})
		}
	}
	// Force one pixel to alpha 254 so Go's PNG encoder keeps the RGBA color type (it
	// downconverts to RGB when every pixel is opaque, which the strict decoder rejects).
	rgba.SetNRGBA(0, 0, stdcolor.NRGBA{R: 0, G: 0, B: 0, A: 254})
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		t.Fatal(err)
	}
	return addSRGBChunk(t, buf.Bytes())
}

func addSRGBChunk(t *testing.T, pngBytes []byte) []byte {
	t.Helper()
	length := int(binary.BigEndian.Uint32(pngBytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = 0
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(pngBytes)+len(chunk))
	result = append(result, pngBytes[:end]...)
	result = append(result, chunk...)
	return append(result, pngBytes[end:]...)
}

func sdkPromoHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func stageDecoder(t *testing.T, executable string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "ffmpeg-decoder")
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	return staged
}

func stageEncoder(t *testing.T, executable string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "ffmpeg-encoder")
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	return staged
}

func buildHeroManifest(hash string, size int64, w, h int, frames, fps int64) probe.ProbeManifest {
	durationPTS := frames
	return probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "hero", ContentHash: hash, SizeBytes: size},
		Probe:         probe.ProbeIdentity{Implementation: "promo", BuildFingerprint: "sha256:" + strings.Repeat("a", 64), OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container:     probe.ContainerInfo{FormatName: "yuv4mpegpipe", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:      videoedit.RationalTime{Value: frames, Rate: fps},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "rawvideo",
			TimeBase: videoedit.RationalTime{Value: 1, Rate: fps}, StartPTS: 0, DurationPTS: &durationPTS,
			Video: &probe.VideoStream{
				Width: w, Height: h, RotationDegrees: 0,
				SampleAspectRatio:  &probe.Ratio{Numerator: 1, Denominator: 1},
				DisplayAspectRatio: &probe.Ratio{Numerator: int64(w), Denominator: int64(h)},
				PixelFormat:        "yuv420p", FrameRateMode: probe.FrameRateVFR,
				AverageFrameRate: &videoedit.RationalTime{Value: fps, Rate: 1},
				Color:            probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
}

func probeY4M(raw []byte) (struct{ W, H, FPS int }, error) {
	var result struct{ W, H, FPS int }
	header := raw[:bytes.IndexByte(raw, '\n')+1]
	if _, err := fmt.Sscanf(string(header), "YUV4MPEG2 W%d H%d F%d", &result.W, &result.H, &result.FPS); err != nil {
		return result, fmt.Errorf("y4m header: %v", err)
	}
	return result, nil
}
