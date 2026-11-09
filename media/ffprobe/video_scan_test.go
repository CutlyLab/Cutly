package ffprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/probe"
)

func scanFixture(t *testing.T, start int64, durations ...int64) (probe.ProbeManifest, map[string]any) {
	t.Helper()
	total := int64(0)
	for _, duration := range durations {
		total += duration
	}
	header := map[string]any{"index": 2, "codec_name": "h264", "codec_type": "video", "time_base": "1/1000", "start_pts": start, "duration_ts": total,
		"width": 64, "height": 48, "pix_fmt": "yuv420p", "sample_aspect_ratio": "1:1", "display_aspect_ratio": "4:3", "avg_frame_rate": "25/1", "r_frame_rate": "25/1",
		"color_primaries": "bt709", "color_transfer": "bt709", "color_space": "bt709", "color_range": "tv", "chroma_location": "left", "field_order": "progressive"}
	frames := []any{}
	pts := start
	keys := []int64{}
	for i, duration := range durations {
		key := 0
		if i == 0 {
			key = 1
			keys = append(keys, pts)
		}
		frames = append(frames, map[string]any{"media_type": "video", "stream_index": 2, "pts": pts, "best_effort_timestamp": pts, "duration": duration, "key_frame": key,
			"interlaced_frame": 0, "top_field_first": 0, "repeat_pict": 0, "width": 64, "height": 48, "pix_fmt": "yuv420p", "sample_aspect_ratio": "1:1",
			"color_primaries": "bt709", "color_transfer": "bt709", "color_space": "bt709", "color_range": "tv", "chroma_location": "left"})
		pts += duration
	}
	base := map[string]any{"streams": []any{header}, "format": map[string]any{"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "start_time": "0", "duration": "1"}}
	manifest, err := parseOutput(scanJSON(t, base), fixtureIdentity())
	if err != nil {
		t.Fatal(err)
	}
	manifest.Streams[0].Video.Keyframes = completeKeyframeSummary(2, keys)
	return manifest, map[string]any{"streams": []any{header}, "frames": frames}
}

func scanJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func scanFirstFrame(output map[string]any) map[string]any {
	return output["frames"].([]any)[0].(map[string]any)
}
func scanHeader(output map[string]any) map[string]any {
	return output["streams"].([]any)[0].(map[string]any)
}

func TestVideoScanExactVFRAndNegativePTS(t *testing.T) {
	for _, start := range []int64{0, -80} {
		t.Run(fmt.Sprint(start), func(t *testing.T) {
			manifest, output := scanFixture(t, start, 40, 70, 20)
			options, _ := normalizeVideoScanOptions(VideoScanOptions{MaxFrames: 3})
			raw := scanJSON(t, output)
			frames, observations, err := parseVideoScan(context.Background(), raw, manifest.Streams[0], options)
			if err != nil {
				t.Fatal(err)
			}
			index, fingerprint, err := videodecode.NewFrameIndex(context.Background(), manifest, 2, frames, 3)
			if err != nil {
				t.Fatal(err)
			}
			if index.Frames[1].PTS != start+40 || index.Frames[1].DurationPTS != 70 || fingerprint == "" || !observations.ProgressiveOnly || !observations.StaticColorOnly || !observations.RotationOnlyMatrix || !observations.ScanComplete {
				t.Fatalf("index=%+v observations=%+v", index, observations)
			}
			frames[0].PTS++
			if index.Frames[0].PTS != start {
				t.Fatal("index aliases scanner records")
			}
		})
	}
}

func TestVideoScanRejectsIncompleteUnknownAndUnsupportedFacts(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing progressive": func(o map[string]any) { delete(scanFirstFrame(o), "interlaced_frame") },
		"interlace":           func(o map[string]any) { scanFirstFrame(o)["interlaced_frame"] = 1 },
		"repeat":              func(o map[string]any) { scanFirstFrame(o)["repeat_pict"] = 1 },
		"missing duration":    func(o map[string]any) { delete(scanFirstFrame(o), "duration") },
		"zero duration":       func(o map[string]any) { scanFirstFrame(o)["duration"] = 0 },
		"no pts":              func(o map[string]any) { delete(scanFirstFrame(o), "pts") },
		"ambiguous pts":       func(o map[string]any) { scanFirstFrame(o)["best_effort_timestamp"] = 1 },
		"dynamic color":       func(o map[string]any) { scanFirstFrame(o)["color_transfer"] = "smpte2084" },
		"missing color":       func(o map[string]any) { delete(scanFirstFrame(o), "color_space") },
		"geometry":            func(o map[string]any) { scanFirstFrame(o)["width"] = 100 },
		"crop":                func(o map[string]any) { scanFirstFrame(o)["crop_left"] = 2 },
		"decode error":        func(o map[string]any) { scanFirstFrame(o)["decode_error_flags"] = 1 },
		"wrong stream":        func(o map[string]any) { scanFirstFrame(o)["stream_index"] = 0 },
		"hdr": func(o map[string]any) {
			scanFirstFrame(o)["side_data_list"] = []any{map[string]any{"side_data_type": "Mastering display metadata"}}
		},
		"dynamic hdr": func(o map[string]any) {
			scanFirstFrame(o)["side_data_list"] = []any{map[string]any{"side_data_type": "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}}
		},
		"icc": func(o map[string]any) {
			scanFirstFrame(o)["side_data_list"] = []any{map[string]any{"side_data_type": "ICC profile"}}
		},
		"unknown side": func(o map[string]any) {
			scanFirstFrame(o)["side_data_list"] = []any{map[string]any{"side_data_type": "future color"}}
		},
		"header clock":     func(o map[string]any) { scanHeader(o)["time_base"] = "1/2000" },
		"header color":     func(o map[string]any) { scanHeader(o)["color_space"] = "bt2020nc" },
		"header interlace": func(o map[string]any) { scanHeader(o)["field_order"] = "tt" },
		"header hdr": func(o map[string]any) {
			scanHeader(o)["side_data_list"] = []any{map[string]any{"side_data_type": "DOVI configuration record"}}
		},
		"no header":  func(o map[string]any) { delete(o, "streams") },
		"no frames":  func(o map[string]any) { o["frames"] = []any{} },
		"extra root": func(o map[string]any) { o["error"] = map[string]any{"code": -1} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, output := scanFixture(t, 0, 40, 40)
			change(output)
			options, _ := normalizeVideoScanOptions(VideoScanOptions{})
			frames, observations, err := parseVideoScan(context.Background(), scanJSON(t, output), manifest.Streams[0], options)
			if err == nil || frames != nil || observations != (videodecode.VideoSemanticsObservations{}) {
				t.Fatalf("frames=%+v observations=%+v err=%v", frames, observations, err)
			}
		})
	}
}

func TestVideoScanBindingRejectsIncompleteIntervalsAndKeyframes(t *testing.T) {
	cases := map[string]func(map[string]any){
		"negative duration": func(o map[string]any) { scanFirstFrame(o)["duration"] = -1 },
		"wrong start":       func(o map[string]any) { scanFirstFrame(o)["pts"] = 1; scanFirstFrame(o)["best_effort_timestamp"] = 1 },
		"missing key":       func(o map[string]any) { scanFirstFrame(o)["key_frame"] = 0 },
		"overlap":           func(o map[string]any) { scanFirstFrame(o)["duration"] = 50 },
		"wrong end":         func(o map[string]any) { o["frames"].([]any)[1].(map[string]any)["duration"] = 39 },
		"extra key":         func(o map[string]any) { o["frames"].([]any)[1].(map[string]any)["key_frame"] = 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, output := scanFixture(t, 0, 40, 40)
			change(output)
			options, _ := normalizeVideoScanOptions(VideoScanOptions{})
			frames, _, err := parseVideoScan(context.Background(), scanJSON(t, output), manifest.Streams[0], options)
			if err == nil {
				_, _, err = videodecode.NewFrameIndex(context.Background(), manifest, 2, frames, 10)
			}
			if err == nil {
				t.Fatal("invalid scan accepted")
			}
		})
	}
}

func TestVideoScanBudgetsAndStrictJSON(t *testing.T) {
	manifest, output := scanFixture(t, 0, 40, 40)
	options, _ := normalizeVideoScanOptions(VideoScanOptions{MaxFrames: 1})
	if _, _, err := parseVideoScan(context.Background(), scanJSON(t, output), manifest.Streams[0], options); !errors.Is(err, ErrVideoScanLimit) {
		t.Fatal(err)
	}
	options.MaxFrames = 2
	options.MaxOutputBytes = 1
	if _, _, err := parseVideoScan(context.Background(), scanJSON(t, output), manifest.Streams[0], options); !errors.Is(err, ErrVideoScanLimit) {
		t.Fatal(err)
	}
	options, _ = normalizeVideoScanOptions(VideoScanOptions{})
	raw := scanJSON(t, output)
	for _, bad := range [][]byte{append(append([]byte{}, raw...), []byte(` {}`)...), []byte(strings.Replace(string(raw), `"interlaced_frame":0`, `"interlaced_frame":0,"interlaced_frame":1`, 1)), []byte(strings.Replace(string(raw), `"frames":`, `"frames":[],"frames":`, 1)), raw[:len(raw)-1]} {
		if _, _, err := parseVideoScan(context.Background(), bad, manifest.Streams[0], options); err == nil {
			t.Fatal("malformed/ambiguous JSON accepted")
		}
	}
	for _, bad := range []VideoScanOptions{{MaxFrames: -1}, {MaxFrames: videodecode.HardMaxIndexedFrames + 1}, {MaxOutputBytes: 1023}, {MaxOutputBytes: HardMaxVideoScanBytes + 1}} {
		if _, err := normalizeVideoScanOptions(bad); !errors.Is(err, ErrVideoScanLimit) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := parseVideoScan(ctx, raw, manifest.Streams[0], options); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func matrixSide(rotation int, values [9]int64) map[string]json.RawMessage {
	text := fmt.Sprintf("\n00000000: %d %d %d\n00000001: %d %d %d\n00000002: %d %d %d\n", values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8])
	encoded, _ := json.Marshal(text)
	return map[string]json.RawMessage{"side_data_type": json.RawMessage(`"Display Matrix"`), "rotation": json.RawMessage(fmt.Sprint(rotation)), "displaymatrix": encoded}
}

func TestVideoScanProvesMatrixNotJustRotationScalar(t *testing.T) {
	matrices := map[int][9]int64{0: {65536, 0, 0, 0, 65536, 0, 0, 0, 1073741824}, 90: {0, -65536, 0, 65536, 0, 0, 0, 0, 1073741824}, -90: {0, 65536, 0, -65536, 0, 0, 0, 0, 1073741824}, 180: {-65536, 0, 0, 0, -65536, 0, 0, 0, 1073741824}}
	for rotation, matrix := range matrices {
		side := matrixSide(rotation, matrix)
		if err := observeDisplayMatrix(side, rotation); err != nil {
			t.Fatal(err)
		}
		// Rotation scalars from reflected matrices can look identical. Compare all nine coefficients.
		for _, position := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
			bad := matrix
			bad[position]++
			if err := observeDisplayMatrix(matrixSide(rotation, bad), rotation); !errors.Is(err, ErrVideoScanUnsupported) {
				t.Fatalf("accepted matrix change %d: %v", position, err)
			}
		}
	}
	if err := observeScanSideData(nil, 90, true); !errors.Is(err, ErrVideoScanUnsupported) {
		t.Fatal(err)
	}
}

func setupScanHelper(t *testing.T) (*Adapter, Request, probe.ProbeManifest, map[string]any, string) {
	t.Helper()
	helper := installFFprobeHelper(t)
	dir := canonicalTempDir(t)
	source := filepath.Join(dir, "source.mp4")
	content := []byte("complete scanner immutable input")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, output := scanFixture(t, 0, 40, 70, 20)
	manifest.Resource.ContentHash = testDigestBytes(content)
	manifest.Resource.SizeBytes = int64(len(content))
	fixture := filepath.Join(dir, "scan.json")
	if err := os.WriteFile(fixture, scanJSON(t, output), 0600); err != nil {
		t.Fatal(err)
	}
	writeFFprobeHelperConfig(t, helper, ffprobeHelperConfig{Fixture: fixture, FrameFixture: fixture})
	adapter, err := New(context.Background(), helper, DefaultBuildIdentity(testDigest("scan helper runtime")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Probe.BuildFingerprint = adapter.buildFingerprint
	manifest.Probe.OptionsFingerprint = adapter.optionsFingerprint
	return adapter, Request{AssetID: manifest.Resource.AssetID, Path: source}, manifest, output, fixture
}

func TestScanVideoPublishesOwnedBoundEvidence(t *testing.T) {
	adapter, request, manifest, _, _ := setupScanHelper(t)
	before := scanJSON(t, manifest)
	result, err := adapter.ScanVideo(context.Background(), request, manifest, 2, VideoScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := videodecode.BindFrameIndex(context.Background(), manifest, result.Index, 0); err != nil {
		t.Fatal(err)
	}
	if err := videodecode.ValidateVideoSemanticsEvidence(context.Background(), manifest, result.Semantics); err != nil {
		t.Fatal(err)
	}
	if fingerprint, _ := videodecode.FrameIndexFingerprint(result.Index, 0); fingerprint != result.IndexFingerprint {
		t.Fatal("index fingerprint mismatch")
	}
	if fingerprint, _ := videodecode.VideoSemanticsEvidenceFingerprint(context.Background(), manifest, result.Semantics); fingerprint != result.SemanticsFingerprint {
		t.Fatal("semantic fingerprint mismatch")
	}
	other, err := adapter.ScanVideo(context.Background(), request, manifest, 2, VideoScanOptions{MaxFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexFingerprint != other.IndexFingerprint || result.SemanticsFingerprint == other.SemanticsFingerprint || result.ScannerOptionsFingerprint == other.ScannerOptionsFingerprint {
		t.Fatal("options identity not causal")
	}
	result.Index.Frames[0].PTS = 999
	if other.Index.Frames[0].PTS != 0 || string(before) != string(scanJSON(t, manifest)) {
		t.Fatal("caller/result alias")
	}
}

func TestScanVideoFailuresPublishZero(t *testing.T) {
	adapter, request, manifest, output, fixture := setupScanHelper(t)
	check := func(errWant error, request Request, manifest probe.ProbeManifest, options VideoScanOptions) {
		t.Helper()
		result, err := adapter.ScanVideo(context.Background(), request, manifest, 2, options)
		if err == nil || (errWant != nil && !errors.Is(err, errWant)) || !reflect.DeepEqual(result, VideoScanResult{}) {
			t.Fatalf("result=%+v err=%v want=%v", result, err, errWant)
		}
	}
	wrong := request
	wrong.AssetID = "wrong"
	check(ErrInput, wrong, manifest, VideoScanOptions{})
	wrong = request
	wrong.ExpectedContentHash = testDigest("wrong")
	check(ErrInput, wrong, manifest, VideoScanOptions{})
	bad := manifest
	bad.Resource.SizeBytes++
	check(ErrInput, request, bad, VideoScanOptions{})
	bad = manifest
	bad.Probe.BuildFingerprint = testDigest("other build")
	check(ErrInput, request, bad, VideoScanOptions{})
	check(ErrVideoScanLimit, request, manifest, VideoScanOptions{MaxFrames: 2})
	check(ErrOutputLimit, request, manifest, VideoScanOptions{MaxOutputBytes: 1024})
	scanFirstFrame(output)["interlaced_frame"] = 1
	if err := os.WriteFile(fixture, scanJSON(t, output), 0600); err != nil {
		t.Fatal(err)
	}
	check(ErrVideoScanUnsupported, request, manifest, VideoScanOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := adapter.ScanVideo(ctx, request, manifest, 2, VideoScanOptions{}); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, VideoScanResult{}) {
		t.Fatal(result, err)
	}
	// Replacing executable bytes after New is rejected before executing them.
	if err := os.WriteFile(adapter.executable, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	check(ErrExecutable, request, manifest, VideoScanOptions{})
}

// ScanVideo first requests ctx.Done when constructing exec.CommandContext,
// after freezing the manifest. The helper's ready file proves the child really
// started; this channel additionally publishes that prior Go memory access to
// the test goroutine, which a cross-process filesystem signal alone cannot do.
// Err deliberately delegates without consulting Done, matching context.Context.
type scanCommandContext struct {
	context.Context
	commandStarted chan struct{}
	once           sync.Once
}

func (ctx *scanCommandContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.commandStarted) })
	return ctx.Context.Done()
}

func TestScanVideoUsesFrozenManifestAndDescriptorAfterPathReplacement(t *testing.T) {
	adapter, request, manifest, _, fixture := setupScanHelper(t)
	dir := canonicalTempDir(t)
	ready, release, observed := filepath.Join(dir, "ready"), filepath.Join(dir, "release"), filepath.Join(dir, "observed")
	writeFFprobeHelperConfig(t, adapter.executable, ffprobeHelperConfig{Fixture: fixture, FrameFixture: fixture, Ready: ready, Release: release, Observed: observed})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scanCtx := &scanCommandContext{Context: ctx, commandStarted: make(chan struct{})}
	type outcome struct {
		result VideoScanResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := adapter.ScanVideo(scanCtx, request, manifest, 2, VideoScanOptions{})
		done <- outcome{result, err}
	}()
	if err := waitForFile(ctx, ready); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scanCtx.commandStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The scanner froze nested manifest pointers and unlinked its private copy
	// before launching the child. The channel receive establishes happens-before
	// for the deep copy; the ready/release handshake keeps the child blocked.
	// Neither caller mutation can change the scan.
	manifest.Streams[0].Video.Color.Primaries = "bt2020"
	if err := os.Remove(request.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Path, []byte("replacement input"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	raw, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if testDigestBytes(raw) != out.result.Index.ResourceHash || len(out.result.Index.Frames) != 3 {
		t.Fatal("scan used mutable caller input")
	}
}

func TestScanVideoPanicAndCleanupFailurePublishZero(t *testing.T) {
	for _, panicNow := range []bool{false, true} {
		func() {
			result := VideoScanResult{IndexFingerprint: "must be suppressed"}
			var err error
			file, openErr := os.CreateTemp(canonicalTempDir(t), "closed")
			if openErr != nil {
				t.Fatal(openErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			input := &materializedInput{file: file}
			func() {
				defer finishVideoScan(&result, &err, &input)
				if panicNow {
					panic("injected scanner panic")
				}
			}()
			if !errors.Is(err, ErrCleanup) || !reflect.DeepEqual(result, VideoScanResult{}) {
				t.Fatal(result, err)
			}
			if panicNow && !errors.Is(err, ErrParse) {
				t.Fatal(err)
			}
		}()
	}
}

func TestScanVideoRejectsZeroExitDecoderErrors(t *testing.T) {
	adapter, request, manifest, _, fixture := setupScanHelper(t)
	writeFFprobeHelperConfig(t, adapter.executable, ffprobeHelperConfig{Fixture: fixture, FrameFixture: fixture, Stderr: "decode error on omitted frame"})
	result, err := adapter.ScanVideo(context.Background(), request, manifest, 2, VideoScanOptions{})
	if !errors.Is(err, ErrCommand) || !reflect.DeepEqual(result, VideoScanResult{}) {
		t.Fatal(result, err)
	}
}

func TestScanVideoCancellationCleansPrivateInput(t *testing.T) {
	adapter, request, manifest, _, fixture := setupScanHelper(t)
	dir := canonicalTempDir(t)
	ready := filepath.Join(dir, "ready")
	writeFFprobeHelperConfig(t, adapter.executable, ffprobeHelperConfig{Fixture: fixture, FrameFixture: fixture, Ready: ready, Release: filepath.Join(dir, "never")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := adapter.ScanVideo(ctx, request, manifest, 2, VideoScanOptions{})
		if !reflect.DeepEqual(result, VideoScanResult{}) {
			done <- fmt.Errorf("nonzero canceled result")
			return
		}
		done <- err
	}()
	if err := waitForFile(ctx, ready); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Materialization removes all pathnames before command execution; cancellation
	// closes the sole retained descriptor. A repeat call must remain usable.
	writeFFprobeHelperConfig(t, adapter.executable, ffprobeHelperConfig{Fixture: fixture, FrameFixture: fixture})
	if _, err := adapter.ScanVideo(context.Background(), request, manifest, 2, VideoScanOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRealFFmpegCompleteVideoScan(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := canonicalTempDir(t)
	source := filepath.Join(dir, "source.mp4")
	generate := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=25", "-frames:v", "5", "-vf", "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=limited", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv", "-chroma_sample_location", "left", "-y", source)
	if raw, err := generate.CombinedOutput(); err != nil {
		t.Skipf("fixture encoder unavailable: %v: %s", err, raw)
	}
	adapter, err := New(ctx, ffprobe, DefaultBuildIdentity(testDigest("real scanner test runtime")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{AssetID: "scan-real", Path: source}
	manifest, diagnostics, err := adapter.Probe(ctx, request)
	if err != nil || probe.HasErrorDiagnostics(diagnostics) {
		t.Fatal(err, diagnostics)
	}
	result, err := adapter.ScanVideo(ctx, request, manifest, 0, VideoScanOptions{MaxFrames: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Index.Frames) != 5 || result.IndexFingerprint == "" || result.SemanticsFingerprint == "" {
		t.Fatalf("result=%+v", result)
	}
	rotated := filepath.Join(dir, "rotated.mp4")
	rotate := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-display_rotation:v:0", "90", "-i", source, "-c", "copy", "-y", rotated)
	if raw, err := rotate.CombinedOutput(); err != nil {
		t.Fatal(err, string(raw))
	}
	request.Path = rotated
	manifest, _, err = adapter.Probe(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err = adapter.ScanVideo(ctx, request, manifest, 0, VideoScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Semantics.Observations.RotationDegrees != 90 {
		t.Fatalf("rotation=%+v", result.Semantics)
	}
	flipped := filepath.Join(dir, "flipped.mp4")
	flip := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-display_hflip:v:0", "-i", source, "-c", "copy", "-y", flipped)
	if raw, err := flip.CombinedOutput(); err != nil {
		t.Fatal(err, string(raw))
	}
	request.Path = flipped
	manifest, _, err = adapter.Probe(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err = adapter.ScanVideo(ctx, request, manifest, 0, VideoScanOptions{})
	if !errors.Is(err, ErrVideoScanUnsupported) || !reflect.DeepEqual(result, VideoScanResult{}) {
		t.Fatal("display flip accepted", result, err)
	}
}
