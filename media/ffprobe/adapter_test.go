package ffprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

func TestParseCapturedOutputIsExactAndConservative(t *testing.T) {
	manifest, err := parseOutput(readFixture(t, "ffprobe-output.json"), fixtureIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := probe.Validate(manifest); probe.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	if len(manifest.Streams) != 2 {
		t.Fatalf("streams = %+v", manifest.Streams)
	}
	video := manifest.Streams[0]
	if video.StartPTS != -1001 || video.DurationPTS == nil || *video.DurationPTS != 60060 {
		t.Fatalf("video timestamps = %+v", video)
	}
	if video.Video == nil || video.Video.RotationDegrees != -90 || video.Video.FrameRateMode != probe.FrameRateUnknown {
		t.Fatalf("video = %+v", video.Video)
	}
	if video.Video.AverageFrameRate == nil || *video.Video.AverageFrameRate != (videoedit.RationalTime{Value: 30000, Rate: 1001}) {
		t.Fatalf("average rate = %+v", video.Video.AverageFrameRate)
	}
	if video.Video.Keyframes.Complete || video.Video.Keyframes.Count != 0 {
		t.Fatalf("adapter guessed keyframe evidence: %+v", video.Video.Keyframes)
	}
	if video.Video.Color.Range != probe.ColorRangeLimited || video.Video.HDR == nil || video.Video.HDR.MaxContentLightLevel != 1000 {
		t.Fatalf("color/HDR = %+v %+v", video.Video.Color, video.Video.HDR)
	}
	audio := manifest.Streams[1]
	if audio.Audio == nil || audio.Audio.SampleRate != 48000 || audio.Audio.EncoderDelaySamples != 1024 || audio.Audio.PaddingSamples != 128 {
		t.Fatalf("audio = %+v", audio.Audio)
	}
	if manifest.Duration != (videoedit.RationalTime{Value: 1001, Rate: 500}) {
		t.Fatalf("duration = %+v", manifest.Duration)
	}
}

func TestUnknownRateStaysNilInsteadOfBeingGuessed(t *testing.T) {
	raw := bytes.ReplaceAll(readFixture(t, "ffprobe-output.json"), []byte(`30000/1001`), []byte(`0/0`))
	manifest, err := parseOutput(raw, fixtureIdentity())
	if err != nil {
		t.Fatal(err)
	}
	video := manifest.Streams[0].Video
	if video.FrameRateMode != probe.FrameRateUnknown || video.AverageFrameRate != nil {
		t.Fatalf("unknown rate was guessed: %+v", video)
	}
	if diagnostics := probe.Validate(manifest); probe.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("unknown rate must remain valid: %+v", diagnostics)
	}
}

func TestMissingStreamClockIsRejectedInsteadOfDefaultingToZero(t *testing.T) {
	var output map[string]any
	if err := json.Unmarshal(readFixture(t, "ffprobe-output.json"), &output); err != nil {
		t.Fatal(err)
	}
	streams := output["streams"].([]any)
	video := streams[0].(map[string]any)
	delete(video, "start_pts")
	delete(video, "start_time")
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseOutput(raw, fixtureIdentity()); !errors.Is(err, ErrParse) {
		t.Fatalf("missing clock error = %v", err)
	}
}

func TestExactParsersAvoidFloatRounding(t *testing.T) {
	decimal, present, err := parseOptionalDecimal("-0.033367")
	if err != nil || !present || decimal != (videoedit.RationalTime{Value: -33367, Rate: 1000000}) {
		t.Fatalf("decimal = %+v present=%v err=%v", decimal, present, err)
	}
	fraction, err := parseFraction("30000/1001", '/')
	if err != nil || fraction != (videoedit.RationalTime{Value: 30000, Rate: 1001}) {
		t.Fatalf("fraction = %+v err=%v", fraction, err)
	}
	if ticks, exact := timeToTicks(videoedit.RationalTime{Value: 1, Rate: 2}, videoedit.RationalTime{Value: 1, Rate: 48000}); !exact || ticks != 24000 {
		t.Fatalf("ticks = %d exact=%v", ticks, exact)
	}
}

func TestFrameScanProvesExactCFRAndKeyframes(t *testing.T) {
	manifest, err := parseOutput(readFixture(t, "ffprobe-output.json"), fixtureIdentity())
	if err != nil {
		t.Fatal(err)
	}
	type frame struct {
		StreamIndex int   `json:"stream_index"`
		PTS         int64 `json:"best_effort_timestamp"`
		Duration    int64 `json:"pkt_duration"`
		Key         int   `json:"key_frame"`
	}
	frames := make([]frame, 60)
	for index := range frames {
		frames[index] = frame{StreamIndex: 0, PTS: -1001 + int64(index)*1001, Duration: 1001}
		if index == 0 || index == 30 {
			frames[index].Key = 1
		}
	}
	raw, err := json.Marshal(struct {
		Frames []frame `json:"frames"`
	}{Frames: frames})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyFrameScan(&manifest, raw); err != nil {
		t.Fatal(err)
	}
	video := manifest.Streams[0].Video
	if video.FrameRateMode != probe.FrameRateCFR {
		t.Fatalf("frame rate mode = %q", video.FrameRateMode)
	}
	if !video.Keyframes.Complete || video.Keyframes.Count != 2 || video.Keyframes.FirstPTS == nil || *video.Keyframes.FirstPTS != -1001 ||
		video.Keyframes.LastPTS == nil || *video.Keyframes.LastPTS != 29029 || !strings.HasPrefix(video.Keyframes.IndexHash, "sha256:") {
		t.Fatalf("keyframes = %+v", video.Keyframes)
	}
	if diagnostics := probe.Validate(manifest); probe.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
}

func TestFrameScanDistinguishesVFRFromUnknown(t *testing.T) {
	base := probe.ProbeManifest{Streams: []probe.Stream{{
		Index: 0, Kind: probe.StreamKindVideo, TimeBase: videoedit.RationalTime{Value: 1, Rate: 30},
		StartPTS: -2, DurationPTS: ffInt64Pointer(7),
		Video: &probe.VideoStream{AverageFrameRate: ffRationalPointer(videoedit.RationalTime{Value: 30, Rate: 1})},
	}}}
	vfr := []byte(`{"frames":[{"stream_index":0,"best_effort_timestamp":-2,"pkt_duration":1,"key_frame":1},{"stream_index":0,"best_effort_timestamp":-1,"pkt_duration":2,"key_frame":0},{"stream_index":0,"best_effort_timestamp":1,"pkt_duration":4,"key_frame":0}]}`)
	if err := applyFrameScan(&base, vfr); err != nil {
		t.Fatal(err)
	}
	if got := base.Streams[0].Video.FrameRateMode; got != probe.FrameRateVFR {
		t.Fatalf("mode = %q, want vfr", got)
	}

	base.Streams[0].Video.FrameRateMode = probe.FrameRateUnknown
	base.Streams[0].Video.Keyframes = probe.KeyframeSummary{}
	missingPTS := []byte(`{"frames":[{"stream_index":0,"key_frame":1}]}`)
	if err := applyFrameScan(&base, missingPTS); err != nil {
		t.Fatal(err)
	}
	if got := base.Streams[0].Video.FrameRateMode; got != probe.FrameRateUnknown || base.Streams[0].Video.Keyframes.Complete {
		t.Fatalf("incomplete scan was promoted: mode=%q keyframes=%+v", got, base.Streams[0].Video.Keyframes)
	}
}

func TestFrameScanUsesGlobalStreamIndicesForMultipleVideoStreams(t *testing.T) {
	duration := int64(2)
	manifest := probe.ProbeManifest{Streams: []probe.Stream{
		{Index: 0, Kind: probe.StreamKindVideo, TimeBase: videoedit.RationalTime{Value: 1, Rate: 1}, DurationPTS: &duration,
			Video: &probe.VideoStream{AverageFrameRate: ffRationalPointer(videoedit.RationalTime{Value: 1, Rate: 1})}},
		{Index: 2, Kind: probe.StreamKindVideo, TimeBase: videoedit.RationalTime{Value: 1, Rate: 1}, DurationPTS: &duration,
			Video: &probe.VideoStream{AverageFrameRate: ffRationalPointer(videoedit.RationalTime{Value: 1, Rate: 1})}},
	}}
	raw := []byte(`{"frames":[{"stream_index":2,"best_effort_timestamp":0,"pkt_duration":1,"key_frame":1},{"stream_index":0,"best_effort_timestamp":0,"pkt_duration":1,"key_frame":1},{"stream_index":2,"best_effort_timestamp":1,"pkt_duration":1,"key_frame":0},{"stream_index":0,"best_effort_timestamp":1,"pkt_duration":1,"key_frame":0}]}`)
	if err := applyFrameScan(&manifest, raw); err != nil {
		t.Fatal(err)
	}
	for _, stream := range manifest.Streams {
		if stream.Video.FrameRateMode != probe.FrameRateCFR || stream.Video.Keyframes.Count != 1 {
			t.Fatalf("stream %d = %+v", stream.Index, stream.Video)
		}
	}
}

func TestSecureLocalPathRejectsProtocolsDirectoriesAndSymlinks(t *testing.T) {
	if _, err := secureLocalPath("https://example.com/video.mp4"); !errors.Is(err, ErrInput) {
		t.Fatalf("URL error = %v", err)
	}
	directory := canonicalTempDir(t)
	if _, err := secureLocalPath(directory); !errors.Is(err, ErrInput) {
		t.Fatalf("directory error = %v", err)
	}
	file := filepath.Join(directory, "asset.mp4")
	if err := os.WriteFile(file, []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := secureLocalPath(file)
	if err != nil || resolved == "" {
		t.Fatalf("regular file = %q, %v", resolved, err)
	}
	symlink := filepath.Join(directory, "asset-link.mp4")
	if err := os.Symlink(file, symlink); err == nil {
		if _, err := secureLocalPath(symlink); !errors.Is(err, ErrInput) {
			t.Fatalf("symlink error = %v", err)
		}
	}
	realParent := filepath.Join(directory, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	parentFile := filepath.Join(realParent, "nested.mp4")
	if err := os.WriteFile(parentFile, []byte("nested"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(directory, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err == nil {
		if _, err := secureLocalPath(filepath.Join(linkedParent, "nested.mp4")); !errors.Is(err, ErrInput) {
			t.Fatalf("parent symlink error = %v", err)
		}
	}
}

func TestBoundedBufferCapsMemoryAndSignalsLimit(t *testing.T) {
	buffer := newBoundedBuffer(4)
	if count, err := buffer.Write([]byte("abcdef")); err != nil || count != 6 {
		t.Fatalf("write = %d, %v", count, err)
	}
	if !buffer.exceeded || buffer.String() != "abcd" {
		t.Fatalf("buffer = %q exceeded=%v", buffer.String(), buffer.exceeded)
	}
}

func TestAdapterProbesGeneratedLocalMedia(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output := filepath.Join(canonicalTempDir(t), "fixture.mp4")
	generate := exec.CommandContext(ctx, ffmpeg,
		"-v", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x48:r=30000/1001",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "0.25", "-shortest",
		"-vf", "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=limited",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-c:a", "aac", "-y", output,
	)
	if generated, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate local fixture: %v: %s", err, generated)
	}
	adapter, err := New(ctx, ffprobePath, DefaultBuildIdentity(testDigest("installed-ffprobe-runtime-test-only")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, diagnostics, err := adapter.Probe(ctx, Request{AssetID: "generated", Path: output})
	if err != nil {
		t.Fatal(err)
	}
	if probe.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	if len(manifest.Streams) != 2 || !strings.HasPrefix(manifest.Resource.ContentHash, "sha256:") {
		t.Fatalf("manifest = %+v", manifest)
	}
	video := manifest.Streams[0]
	if video.Video == nil || video.Video.FrameRateMode != probe.FrameRateCFR || video.StartPTS != 0 ||
		video.Video.RotationDegrees != 0 || video.Video.HasAlpha || video.Video.Color.Primaries != "bt709" ||
		video.Video.Color.Transfer != "bt709" || video.Video.Color.Matrix != "bt709" || video.Video.Color.Range != probe.ColorRangeLimited {
		t.Fatalf("generated video does not satisfy hard-cut source facts: stream=%+v video=%+v", video, video.Video)
	}
	if _, _, err := adapter.Probe(ctx, Request{AssetID: "generated", Path: output, ExpectedContentHash: "sha256:deadbeef"}); !errors.Is(err, ErrContentHash) {
		t.Fatalf("hash mismatch error = %v", err)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, _, err := adapter.Probe(canceled, Request{AssetID: "generated", Path: output}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe error = %v", err)
	}
}

func fixtureIdentity() parseIdentity {
	return parseIdentity{
		assetID:            "asset-main",
		contentHash:        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		sizeBytes:          1048576,
		buildFingerprint:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		optionsFingerprint: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func ffInt64Pointer(value int64) *int64 { return &value }

func ffRationalPointer(value videoedit.RationalTime) *videoedit.RationalTime { return &value }
