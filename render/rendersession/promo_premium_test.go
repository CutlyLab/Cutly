package rendersession

import (
	"bytes"
	"context"
	cryptoSha256 "crypto/sha256"
	"encoding/binary"
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

// TestPremiumPromo renders a 20-second premium promotional video entirely through the Cutly
// SDK pipeline: cinematic dark scenes, dissolve transitions, a graded demo video segment,
// vector accent overlays, and a resident streaming encoder at CRF 16.
//
//	CUTLY_PROMO=1 CUTLY_PROMO_ASSETS=/tmp/promo-premium \
//	  CUTLY_TEST_FFMPEG=/path/to/ffmpeg \
//	  go test ./render/rendersession -run TestPremiumPromo -v -timeout 30m
func TestPremiumPromo(t *testing.T) {
	if os.Getenv("CUTLY_PROMO") != "1" {
		t.Skip("set CUTLY_PROMO=1")
	}
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Fatal("CUTLY_TEST_FFMPEG required")
	}
	assetDir := os.Getenv("CUTLY_PROMO_ASSETS")
	if assetDir == "" {
		assetDir = "/tmp/promo-premium"
	}

	const (
		width, height = 1920, 1080
		fps           = int64(24)
		totalFrames   = 480 // 20s
		// Hero video: 1280x720 at 24fps, 144 frames (6s)
		heroW, heroH = 1280, 720
		heroFrames   = int64(144)
	)

	// --- Load assets --------------------------------------------------------
	assetStart := time.Now()
	scenes := []struct {
		name   string
		frames int64
		isHero bool
	}{
		// Cinematic pacing: unhurried opens, quick feature cuts, confident close
		{"s1", 72, false},   // 3.0s — hook: 视频合成的正确方式
		{"s2", 60, false},   // 2.5s — three principles
		{"s3", 60, false},   // 2.5s — logo reveal: Cutly
		{"s4", 48, false},   // 2.0s — performance stat
		{"s5", 48, false},   // 2.0s — SHA-256 verified
		{"hero", 120, true}, // 5.0s — demo video (120 body + 12 head + 12 tail = 144)
		{"s6", 36, false},   // 1.5s — demo callout frame
		{"s7", 36, false},   // 1.5s — closing
	}

	// Load all PNG assets
	type asset struct {
		id   documentv2.ID
		png  []byte
		bind referencevisual.Binding
	}
	var assets []asset
	var clipRefs []struct {
		id     documentv2.ID
		asset  documentv2.ID
		frames int64
	}
	var offset int64
	const dissolve = int64(12) // 0.5s — smooth, not jarring

	for _, sc := range scenes {
		if sc.isHero {
			clipRefs = append(clipRefs, struct {
				id     documentv2.ID
				asset  documentv2.ID
				frames int64
			}{"clip-hero", "hero", sc.frames})
		} else {
			png := loadStrictPng(t, filepath.Join(assetDir, sc.name+"-raw.png"))
			id := documentv2.ID("clip-" + sc.name)
			assetID := documentv2.ID("asset-" + sc.name)
			assets = append(assets, asset{id: assetID, png: png,
				bind: referencevisual.Binding{AssetID: assetID, EncodedPNG: png}})
			clipRefs = append(clipRefs, struct {
				id     documentv2.ID
				asset  documentv2.ID
				frames int64
			}{id, assetID, sc.frames})
		}
		offset += sc.frames
	}

	// Hero y4m
	heroPath, err := filepath.EvalSymlinks(filepath.Join(assetDir, "hero.y4m"))
	if err != nil {
		t.Fatalf("hero: %v (generate assets first)", err)
	}
	heroRaw, err := os.ReadFile(heroPath)
	if err != nil {
		t.Fatal(err)
	}
	heroHash := promoHashBytes(heroRaw)
	assetDuration := time.Since(assetStart)

	// --- Decoder setup ---------------------------------------------------------
	decoderBin := stageBin(t, executable, "decoder")
	manifest := buildHeroManifestPremium(heroHash, int64(len(heroRaw)), heroW, heroH, heroFrames, fps)
	records := make([]videodecode.FrameRecord, heroFrames)
	keyframes := make([]int64, heroFrames)
	for i := range records {
		records[i] = videodecode.FrameRecord{PTS: int64(i), DurationPTS: 1, Keyframe: true}
		keyframes[i] = int64(i)
	}
	manifest.Streams[0].Video.Keyframes = keyframeDigest(keyframes)
	probeFP, _ := probe.Fingerprint(manifest)
	index := videodecode.FrameIndex{SchemaVersion: videodecode.FrameIndexSchemaVersion,
		Profile: videodecode.FrameIndexProfileV1, ProbeFingerprint: probeFP,
		ResourceHash: heroHash, StreamIndex: 0,
		TimeBase: videoedit.RationalTime{Value: 1, Rate: fps}, Frames: records}
	indexFP, _ := videodecode.FrameIndexFingerprint(index, int(heroFrames)+16)
	evidence, evidenceFP, _ := videodecode.NewVideoSemanticsEvidence(context.Background(), manifest, 0,
		"premium", "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64),
		videodecode.VideoSemanticsObservations{ScanComplete: true, ProgressiveOnly: true,
			StaticColorOnly: true, RotationOnlyMatrix: true, RotationDegrees: 0})
	adapter, err := videodecode.New(context.Background(), decoderBin, videodecode.DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), videodecode.Options{MaxInputBytes: 1 << 30, MaxFrameBytes: 1 << 28, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: int(heroFrames) + 16, MaxWidth: 4096, MaxHeight: 4096, MaxDecodeMillis: 120_000})
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}

	// --- Vector accent overlays (SDK's own rendering) ---------------------------
	// Thin cyan border frame — appears on all scenes, gives visual continuity
	rat := func(n int64) documentv2.Rational { return documentv2.Rational{Numerator: n, Denominator: 1} }
	accentFill := documentv2.ColorRGBA16{R: 0, G: 52_000, B: 62_000, A: 16_000} // subtle cyan
	accentVector := documentv2.Asset{
		ID: "accent-frame", Kind: documentv2.AssetKindVector,
		Vector: &documentv2.VectorSource{
			Profile:  documentv2.VectorProfilePathViewportPXV2,
			ViewBox:  documentv2.Rect{X: rat(0), Y: rat(0), Width: rat(width), Height: rat(height)},
			Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
			Paths: []documentv2.VectorPath{{
				ID: "accent", FillRule: documentv2.VectorFillNonZero, Fill: &accentFill,
				Commands: []documentv2.VectorCommand{
					{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: rat(48), Y: rat(48)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(width - 48), Y: rat(48)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(width - 48), Y: rat(height - 48)}}},
					{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(48), Y: rat(height - 48)}}},
					{Op: documentv2.VectorClose},
				},
			}},
		},
	}

	// --- Document -----------------------------------------------------------------
	span := func(start, duration int64) documentv2.TimeRange {
		return documentv2.TimeRange{Start: videoedit.RationalTime{Value: start, Rate: fps},
			Duration: videoedit.RationalTime{Value: duration, Rate: fps}}
	}
	var clips []documentv2.Clip
	var transitions []documentv2.Transition
	offset = 0
	for i, cr := range clipRefs {
		srcStart := int64(0)
		if i > 0 {
			srcStart = dissolve // head handle for incoming transition
		}
		srcDur := cr.frames
		clips = append(clips, documentv2.Clip{
			ID: cr.id, AssetID: cr.asset,
			TimelineRange: span(offset, cr.frames),
			SourceRange:   span(srcStart, srcDur),
		})
		if i < len(clipRefs)-1 {
			transitions = append(transitions, documentv2.Transition{
				ID:             documentv2.ID(fmt.Sprintf("t%d", i)),
				Profile:        documentv2.TransitionDissolveV1,
				OutgoingClipID: cr.id, IncomingClipID: clipRefs[i+1].id,
				PreDuration:  videoedit.RationalTime{Value: dissolve, Rate: fps},
				PostDuration: videoedit.RationalTime{Value: dissolve, Rate: fps},
			})
		}
		offset += cr.frames
	}

	docAssets := []documentv2.Asset{accentVector}
	for _, a := range assets {
		docAssets = append(docAssets, documentv2.Asset{
			ID: a.id, Kind: documentv2.AssetKindImage, URI: "unused.png",
			ContentHash: promoHashBytes(a.png),
		})
	}
	docAssets = append(docAssets, documentv2.Asset{
		ID: "hero", Kind: documentv2.AssetKindVideo, URI: "hero.y4m", ContentHash: heroHash,
		Duration: &videoedit.RationalTime{Value: heroFrames, Rate: fps},
	})

	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "premium-promo", Revision: 1,
		Assets: docAssets,
		Timelines: []documentv2.Timeline{{
			ID: "timeline",
			Tracks: []documentv2.Track{
				{ID: "scene", Kind: documentv2.TrackKindVideo, Clips: clips},
				{ID: "accent-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{
					ID: "accent-clip", AssetID: "accent-frame",
					TimelineRange: span(0, offset), SourceRange: span(0, 30),
				}}},
			},
			Transitions: transitions,
		}},
	}
	docHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}

	// --- Render through SDK --------------------------------------------------------
	renderStart := time.Now()
	source, err := adapter.Open(context.Background(), videodecode.OpenRequest{
		Path: heroPath, Manifest: manifest, Index: index, IndexFingerprint: indexFP,
		Semantics: evidence, SemanticsFingerprint: evidenceFP,
	})
	if err != nil {
		t.Fatalf("open hero: %v", err)
	}
	defer source.Close()
	decodeSession, err := videodecode.NewSession(source, videodecode.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer decodeSession.Close()

	pngBindings := make([]referencevisual.Binding, len(assets))
	for i, a := range assets {
		pngBindings[i] = a.bind
	}
	videoBindings := []nativevideo.Binding{
		{DocumentHash: docHash, AssetID: "hero", Source: decodeSession},
	}

	session, err := OpenVideo(context.Background(), document, pngBindings, videoBindings,
		referencevisual.DefaultConfigV2(width, height),
		Options{DeepValidationInterval: 48})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close()
	openDuration := time.Since(renderStart)

	requests := make([]FrameRequest, totalFrames)
	for i := range requests {
		requests[i] = FrameRequest{TimelineID: "timeline",
			At: videoedit.RationalTime{Value: int64(i), Rate: fps}}
	}

	output := "/tmp/cutly-premium-promo.mp4"
	os.Remove(output)
	encoder, err := streamencode.Start(context.Background(), streamencode.Options{
		Executable: stageBin(t, executable, "encoder"), OutputPath: output,
		Width: width, Height: height,
		FrameRateNumerator:   fps,
		FrameRateDenominator: 1,
		CRF:                  16,
	})
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}
	encodeStart := time.Now()
	result, err := session.EncodeFrames(context.Background(), requests, ParallelOptions{Workers: 4}, encoder)
	encodeDuration := time.Since(encodeStart)
	if err != nil {
		encoder.Abort()
		t.Fatalf("encode: %v", err)
	}

	t.Logf(`
=== Premium Promo (1080p, %d frames, %d scenes + vector accent, SDK pipeline) ===
assets:        %s
session open:  %s
RENDER+ENCODE: %s (%.1f ms/frame, %.1fx realtime)
output:        %s (%.2f MB, CRF %d)
hero decode:   %d hits / %d misses
deep valid:    %d frames verified through full reference path`,
		totalFrames, len(scenes),
		assetDuration.Round(time.Millisecond),
		openDuration.Round(time.Millisecond),
		encodeDuration, float64(encodeDuration.Microseconds())/float64(totalFrames)/1000,
		float64(20*time.Second)/float64(encodeDuration),
		output, float64(result.OutputBytes)/(1<<20), result.QualityCRF,
		func() int { h, _, _ := decodeSession.Statistics(); return int(h) }(),
		func() int { _, m, _ := decodeSession.Statistics(); return int(m) }(),
		session.DeepValidations())

	if result.FramesWritten != int64(totalFrames) {
		t.Fatalf("frames = %d, want %d", result.FramesWritten, totalFrames)
	}
}

// --- helpers ---

func loadStrictPng(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
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
			rgba.SetNRGBA(x, y, stdcolor.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255})
		}
	}
	rgba.SetNRGBA(0, 0, stdcolor.NRGBA{A: 254})
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		t.Fatal(err)
	}
	// inject sRGB chunk
	pngBytes := buf.Bytes()
	length := int(binary.BigEndian.Uint32(pngBytes[8:12]))
	end := 8 + 12 + length
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[:4], 1)
	copy(chunk[4:8], "sRGB")
	chunk[8] = 0
	binary.BigEndian.PutUint32(chunk[9:], crc32.ChecksumIEEE(chunk[4:9]))
	result := make([]byte, 0, len(pngBytes)+13)
	result = append(result, pngBytes[:end]...)
	result = append(result, chunk...)
	return append(result, pngBytes[end:]...)
}

func stageBin(t *testing.T, executable, name string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "ffmpeg-"+name)
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	return staged
}

func buildHeroManifestPremium(hash string, size int64, w, h int, frames, fps int64) probe.ProbeManifest {
	d := frames
	return probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "hero", ContentHash: hash, SizeBytes: size},
		Probe: probe.ProbeIdentity{Implementation: "premium",
			BuildFingerprint:   "sha256:" + strings.Repeat("a", 64),
			OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container: probe.ContainerInfo{FormatName: "yuv4mpegpipe",
			StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration: videoedit.RationalTime{Value: frames, Rate: fps},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "rawvideo",
			TimeBase: videoedit.RationalTime{Value: 1, Rate: fps},
			StartPTS: 0, DurationPTS: &d,
			Video: &probe.VideoStream{
				Width: w, Height: h, RotationDegrees: 0,
				SampleAspectRatio:  &probe.Ratio{Numerator: 1, Denominator: 1},
				DisplayAspectRatio: &probe.Ratio{Numerator: int64(w), Denominator: int64(h)},
				PixelFormat:        "yuv420p", FrameRateMode: probe.FrameRateVFR,
				AverageFrameRate: &videoedit.RationalTime{Value: fps, Rate: 1},
				Color: probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709",
					Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
}

func keyframeDigest(values []int64) probe.KeyframeSummary {
	canonical, _ := videoedit.CanonicalJSON(struct {
		Profile     string  `json:"profile"`
		StreamIndex int     `json:"stream_index"`
		PTS         []int64 `json:"pts"`
	}{"cutly.keyframe-index.v1", 0, values})
	digest := promoSha256(canonical)
	summary := probe.KeyframeSummary{Complete: true, Count: int64(len(values)),
		IndexHash: "sha256:" + digest}
	if len(values) > 0 {
		f, l := values[0], values[len(values)-1]
		summary.FirstPTS, summary.LastPTS = &f, &l
	}
	return summary
}

func promoHashBytes(value []byte) string {
	return "sha256:" + promoSha256(value)
}

func promoSha256(value []byte) string {
	digest := sha256Sum(value)
	return fmt.Sprintf("%x", digest)
}

func sha256Sum(value []byte) []byte {
	digest := cryptoSha256.Sum256(value)
	return digest[:]
}
