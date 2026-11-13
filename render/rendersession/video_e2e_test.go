package rendersession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
)

// The video-layer end-to-end gate: one document with a temporal video layer plus a vector
// overlay, decoded by a real pinned FFmpeg, rendered through a production session. Two binding
// modes must agree byte for byte — the raw per-frame decoder Source and the bounded decode
// Session — and the session mode must reuse decoded frames instead of re-spawning the decoder.
//
// CUTLY_TEST_FFMPEG=/path/to/zscale-ffmpeg go test ./render/rendersession -run TestVideoLayer -v
func TestVideoLayerSessionEndToEnd(t *testing.T) {
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a pinned zscale-enabled FFmpeg")
	}
	staged := filepath.Join(t.TempDir(), "ffmpeg-pinned")
	if raw, err := os.ReadFile(executable); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}

	const (
		frames = 24
		width  = 64
		height = 48
	)
	document, pngPath := videoE2EDocument(t, frames, width, height)
	adapter, err := videodecode.New(context.Background(), staged, videodecode.DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), videodecode.Options{MaxInputBytes: 8 << 20, MaxFrameBytes: 8 << 20, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: 1000, MaxWidth: 256, MaxHeight: 256, MaxDecodeMillis: 60_000})
	if err != nil {
		t.Fatalf("pinned FFmpeg incompatible with the decode profile: %v", err)
	}

	render := func(mode string, useSession bool) ([]string, time.Duration) {
		t.Helper()
		source, err := adapter.Open(context.Background(), videodecode.OpenRequest{Path: pngPath,
			Manifest: videoE2EManifest, Index: videoE2EIndex, IndexFingerprint: videoE2EIndexFingerprint,
			Semantics: videoE2ESemantics, SemanticsFingerprint: videoE2ESemanticsFingerprint})
		if err != nil {
			t.Fatalf("%s open: %v", mode, err)
		}
		defer source.Close()
		var frameSource nativevideo.FrameSource = source
		if useSession {
			session, err := videodecode.NewSession(source, videodecode.SessionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			frameSource = session
		}
		session, err := OpenVideo(context.Background(), document, nil,
			[]nativevideo.Binding{{DocumentHash: videoE2EDocumentHash, AssetID: "clip-video", Source: frameSource}},
			referencevisual.DefaultConfigV2(width, height), Options{DeepValidationInterval: 8})
		if err != nil {
			t.Fatalf("%s session: %v", mode, err)
		}
		defer session.Close()
		requests := make([]FrameRequest, frames)
		for index := range requests {
			requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index), Rate: 30}}
		}
		start := time.Now()
		results, err := session.RenderFrames(context.Background(), requests, ParallelOptions{Workers: 2})
		if err != nil {
			t.Fatalf("%s render: %v", mode, err)
		}
		elapsed := time.Since(start)
		hashes := make([]string, len(results))
		for index, result := range results {
			hashes[index] = result.PNG.ArtifactHash
			if result.Visual.PixelHash == "" {
				t.Fatalf("%s frame %d has no visual hash", mode, index)
			}
		}
		return hashes, elapsed
	}

	rawHashes, rawElapsed := render("raw-source", false)
	sessionHashes, sessionElapsed := render("decode-session", true)
	for index := range rawHashes {
		if rawHashes[index] != sessionHashes[index] {
			t.Fatalf("frame %d diverged between raw source and decode session", index)
		}
	}
	t.Logf("video e2e %dx%d %d frames (2 workers, decode+render): raw decoder source %s (%.1f ms/frame), decode session %s (%.1f ms/frame)",
		width, height, frames,
		rawElapsed, float64(rawElapsed.Milliseconds())/frames,
		sessionElapsed, float64(sessionElapsed.Milliseconds())/frames)
}

// videoE2EDocument builds a two-layer document: a full-frame video layer plus a vector overlay.
// The shared fixtures (manifest/index/semantics) are populated by videoE2EPrepare once the
// pinned decoder has validated them.
var (
	videoE2EDocumentHash         string
	videoE2EManifest             probe.ProbeManifest
	videoE2EIndex                videodecode.FrameIndex
	videoE2EIndexFingerprint     string
	videoE2ESemantics            videodecode.VideoSemanticsEvidence
	videoE2ESemanticsFingerprint string
)

func videoE2EDocument(t *testing.T, frames, width, height int) (document documentv2.Document, mediaPath string) {
	t.Helper()
	// Deterministic all-intra y4m the pinned decoder reads sequentially.
	frameBytes := width*height*3/2 + len("FRAME\n")
	input := make([]byte, 0, frameBytes*frames)
	input = append(input, []byte(fmt.Sprintf("YUV4MPEG2 W%d H%d F30:1 Ip A1:1 C420jpeg\n", width, height))...)
	for frame := 0; frame < frames; frame++ {
		input = append(input, []byte("FRAME\n")...)
		plane := make([]byte, width*height*3/2)
		for index := range plane {
			plane[index] = byte(frame*3 + index)
		}
		input = append(input, plane...)
	}
	mediaPath = filepath.Join(realTempDir(t), "video.y4m")
	if err := os.WriteFile(mediaPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	resourceHash := "sha256:" + hex.EncodeToString(digest[:])
	duration := int64(frames)
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "clip-video", ContentHash: resourceHash, SizeBytes: int64(len(input))},
		Probe:         probe.ProbeIdentity{Implementation: "e2e", BuildFingerprint: "sha256:" + strings.Repeat("a", 64), OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container:     probe.ContainerInfo{FormatName: "yuv4mpegpipe", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:      videoedit.RationalTime{Value: duration, Rate: 30},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "rawvideo", TimeBase: videoedit.RationalTime{Value: 1, Rate: 30}, StartPTS: 0, DurationPTS: &duration,
			Video: &probe.VideoStream{
				Width: width, Height: height, RotationDegrees: 0,
				SampleAspectRatio: &probe.Ratio{Numerator: 1, Denominator: 1}, DisplayAspectRatio: &probe.Ratio{Numerator: int64(width), Denominator: int64(height)},
				PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateVFR, AverageFrameRate: &videoedit.RationalTime{Value: 30, Rate: 1},
				Color: probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
	records := make([]videodecode.FrameRecord, frames)
	keyframes := make([]int64, frames)
	for frame := range records {
		records[frame] = videodecode.FrameRecord{PTS: int64(frame), DurationPTS: 1, Keyframe: true}
		keyframes[frame] = int64(frame)
	}
	manifest.Streams[0].Video.Keyframes = keyframeE2ESummary(keyframes)
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	index := videodecode.FrameIndex{SchemaVersion: videodecode.FrameIndexSchemaVersion, Profile: videodecode.FrameIndexProfileV1,
		ProbeFingerprint: probeFingerprint, ResourceHash: resourceHash, StreamIndex: 0,
		TimeBase: videoedit.RationalTime{Value: 1, Rate: 30}, Frames: records}
	indexFingerprint, err := videodecode.FrameIndexFingerprint(index, 1000)
	if err != nil {
		t.Fatal(err)
	}
	evidence, evidenceFingerprint, err := videodecode.NewVideoSemanticsEvidence(context.Background(), manifest, 0,
		"e2e-scanner", "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("e", 64),
		videodecode.VideoSemanticsObservations{ScanComplete: true, ProgressiveOnly: true, StaticColorOnly: true, RotationOnlyMatrix: true, RotationDegrees: 0},
	)
	if err != nil {
		t.Fatal(err)
	}

	rat := func(n int64) documentv2.Rational { return documentv2.Rational{Numerator: n, Denominator: 1} }
	fill := documentv2.ColorRGBA16{R: 20_000, G: 60_000, B: 20_000, A: 32_768}
	document = documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "video-e2e-document", Revision: 1,
		Assets: []documentv2.Asset{
			{ID: "clip-video", Kind: documentv2.AssetKindVideo, URI: "video.y4m", ContentHash: resourceHash,
				Duration: &videoedit.RationalTime{Value: int64(frames), Rate: 30}},
			{ID: "overlay", Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
				Profile:  documentv2.VectorProfilePathViewportPXV2,
				ViewBox:  documentv2.Rect{X: rat(0), Y: rat(0), Width: rat(int64(width)), Height: rat(int64(height))},
				Viewport: &documentv2.RasterViewport{WidthPixels: uint32(width), HeightPixels: uint32(height)},
				Paths: []documentv2.VectorPath{{ID: "overlay-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill,
					Commands: []documentv2.VectorCommand{
						{Op: documentv2.VectorMoveTo, Points: []documentv2.Point{{X: rat(0), Y: rat(0)}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width)), Y: rat(0)}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(int64(width)), Y: rat(int64(height))}}},
						{Op: documentv2.VectorLineTo, Points: []documentv2.Point{{X: rat(0), Y: rat(int64(height))}}},
						{Op: documentv2.VectorClose},
					}}},
			}},
		},
		Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
			{ID: "video-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{
				ID: "video-clip", AssetID: "clip-video",
				TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 30}, Duration: videoedit.RationalTime{Value: int64(frames), Rate: 30}},
				SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 30}, Duration: videoedit.RationalTime{Value: int64(frames), Rate: 30}},
			}}},
			{ID: "overlay-track", Kind: documentv2.TrackKindVector, Clips: []documentv2.Clip{{
				ID: "overlay-clip", AssetID: "overlay",
				TimelineRange: documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 30}, Duration: videoedit.RationalTime{Value: int64(frames), Rate: 30}},
				SourceRange:   documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 30}, Duration: videoedit.RationalTime{Value: 1, Rate: 30}},
			}}},
		}}},
	}
	documentHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	videoE2EDocumentHash, videoE2EManifest, videoE2EIndex = documentHash, manifest, index
	videoE2EIndexFingerprint, videoE2ESemantics, videoE2ESemanticsFingerprint = indexFingerprint, evidence, evidenceFingerprint
	return document, mediaPath
}

func keyframeE2ESummary(values []int64) probe.KeyframeSummary {
	// The canonical keyframe index digest the decoder validates the manifest against.
	canonical, err := videoedit.CanonicalJSON(struct {
		Profile     string  `json:"profile"`
		StreamIndex int     `json:"stream_index"`
		PTS         []int64 `json:"pts"`
	}{Profile: "cutly.keyframe-index.v1", StreamIndex: 0, PTS: append([]int64(nil), values...)})
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

func realTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
