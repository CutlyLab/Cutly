package videodecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

type fakeFFmpegConfig struct {
	InputHex    string `json:"input_hex,omitempty"`
	OutputHex   string `json:"output_hex,omitempty"`
	OutputBytes int    `json:"output_bytes,omitempty"`
	StderrBytes int    `json:"stderr_bytes,omitempty"`
	CapturePath string `json:"capture_path,omitempty"`
	StartedPath string `json:"started_path,omitempty"`
	Block       bool   `json:"block,omitempty"`
	OmitZscale  bool   `json:"omit_zscale,omitempty"`
}

func TestMain(m *testing.M) {
	if strings.HasPrefix(filepath.Base(os.Args[0]), "fake-ffmpeg-") {
		os.Exit(runFakeFFmpeg())
	}
	os.Exit(m.Run())
}

func runFakeFFmpeg() int {
	if len(os.Args) == 4 && os.Args[1] == "-cpuflags" && os.Args[2] == "0" && os.Args[3] == "-version" {
		_, _ = io.WriteString(os.Stdout, "ffmpeg version cutly-test\n")
		return 0
	}
	if len(os.Args) == 3 && os.Args[1] == "-hide_banner" {
		var inventoryConfig fakeFFmpegConfig
		if raw, err := os.ReadFile(os.Args[0] + ".json"); err == nil {
			_ = json.Unmarshal(raw, &inventoryConfig)
		}
		switch os.Args[2] {
		case "-filters":
			_, _ = io.WriteString(os.Stdout, "T.. select V->N\n... format V->V\n... transpose V->V\n... hflip V->V\n... vflip V->V\n... setsar V->V\n")
			if !inventoryConfig.OmitZscale {
				_, _ = io.WriteString(os.Stdout, "... zscale V->V\n")
			}
			return 0
		case "-pix_fmts":
			_, _ = io.WriteString(os.Stdout, "IO... gbrp16le 3 48\nIO... rgba64le 4 64\n")
			return 0
		case "-muxers":
			_, _ = io.WriteString(os.Stdout, " E rawvideo raw video\n")
			return 0
		}
	}
	raw, err := os.ReadFile(os.Args[0] + ".json")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var config fakeFFmpegConfig
	if json.Unmarshal(raw, &config) != nil {
		return 2
	}
	if config.StartedPath != "" {
		_ = os.WriteFile(config.StartedPath, []byte("started"), 0o600)
	}
	if config.Block {
		for {
			time.Sleep(time.Hour)
		}
	}
	input := os.NewFile(3, "descriptor-3")
	if input == nil {
		return 2
	}
	inputBytes, err := io.ReadAll(input)
	if err != nil || hex.EncodeToString(inputBytes) != config.InputHex {
		_, _ = fmt.Fprintln(os.Stderr, "descriptor input mismatch")
		return 3
	}
	if config.CapturePath != "" {
		_ = os.WriteFile(config.CapturePath, []byte(strings.Join(os.Args[1:], "\x00")), 0o600)
	}
	if config.StderrBytes > 0 {
		_, _ = os.Stderr.Write(bytesOf('e', config.StderrBytes))
	}
	if config.OutputBytes > 0 {
		_, _ = os.Stdout.Write(bytesOf('x', config.OutputBytes))
	} else if config.OutputHex != "" {
		output, _ := hex.DecodeString(config.OutputHex)
		_, _ = os.Stdout.Write(output)
	}
	return 0
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for position := range result {
		result[position] = value
	}
	return result
}

func TestAdapterDecodesExactVFRFrameThroughDescriptor(t *testing.T) {
	input := []byte("not-a-real-video;$(touch should-not-run)")
	path := filepath.Join(realTempDir(t), "clip;$(touch injected).mp4")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	frames := []FrameRecord{{PTS: 0, DurationPTS: 40, Keyframe: true}, {PTS: 40, DurationPTS: 20}, {PTS: 70, DurationPTS: 10, Keyframe: true}, {PTS: 80, DurationPTS: 20}}
	manifest, index := fixtureManifestAndIndex(t, input, frames)
	indexFingerprint, err := FrameIndexFingerprint(index, 100)
	if err != nil {
		t.Fatal(err)
	}
	executable, configPath := installFakeFFmpeg(t)
	capture := filepath.Join(t.TempDir(), "args")
	output := []byte{
		0x02, 0x01, 0x04, 0x03, 0x06, 0x05, 0xff, 0xff,
		0x08, 0x07, 0x0a, 0x09, 0x0c, 0x0b, 0xff, 0xff,
	}
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), OutputHex: hex.EncodeToString(output), CapturePath: capture})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxIndexedFrames: 100, MaxWidth: 64, MaxHeight: 64})
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, indexFingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	surface, receipt, err := source.Frame(context.Background(), videoedit.RationalTime{Value: 85, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if surface.Width() != 2 || surface.Height() != 1 || receipt.FrameOrdinal != 3 || receipt.DisplayPTS != 80 || receipt.SeekPTS != 70 {
		t.Fatalf("surface/receipt = %dx%d %+v", surface.Width(), surface.Height(), receipt)
	}
	pixel, err := surface.Pixel(1, 0)
	if err != nil || pixel.R != 0x0708 || pixel.G != 0x090a || pixel.B != 0x0b0c || pixel.A != 0xffff {
		t.Fatalf("pixel = %+v, err=%v", pixel, err)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	argumentList := strings.Split(string(arguments), "\x00")
	joined := strings.Join(argumentList, " ")
	for _, required := range []string{"-cpuflags 0", "-ss 0.070000", "-protocol_whitelist fd", "-fd 3", "-i fd:", "-map 0:0", `select=eq(pts\,80)`, "transfer=linear", "pipe:1"} {
		if !strings.Contains(joined, required) {
			t.Errorf("arguments missing %q: %q", required, joined)
		}
	}
	if strings.Contains(joined, path) || strings.Contains(joined, "touch injected") {
		t.Fatalf("caller path reached process arguments: %q", joined)
	}
}

func TestAdapterFailsClosedForHDRImplicitColorAndAlpha(t *testing.T) {
	input := []byte("fixture")
	path := filepath.Join(realTempDir(t), "input")
	_ = os.WriteFile(path, input, 0o600)
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	for _, mutate := range []func(*probe.VideoStream){
		func(video *probe.VideoStream) { video.HDR = &probe.HDRMetadata{Format: "pq"} },
		func(video *probe.VideoStream) { video.Color.Matrix = "" },
		func(video *probe.VideoStream) { video.HasAlpha = true },
	} {
		manifest, index := fixtureManifestAndIndex(t, input, []FrameRecord{{PTS: 0, DurationPTS: 100, Keyframe: true}})
		mutate(manifest.Streams[0].Video)
		// The index must bind to the mutated manifest before semantic rejection.
		index.ProbeFingerprint, _ = probe.Fingerprint(manifest)
		fingerprint, _ := FrameIndexFingerprint(index, 100)
		_, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("error = %v, want unsupported", err)
		}
	}
}

func TestNewRejectsRuntimeWithoutRequiredColorFilter(t *testing.T) {
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{OmitZscale: true})
	_, err := New(context.Background(), executable, DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), Options{})
	if !errors.Is(err, ErrExecutable) {
		t.Fatalf("error = %v, want executable rejection", err)
	}
}

func TestAdapterFingerprintBindsRuntimeCPUFeatureProfile(t *testing.T) {
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{})
	dependency := "sha256:" + strings.Repeat("c", 64)
	first, err := New(context.Background(), executable, DefaultBuildIdentity(
		dependency, "sha256:"+strings.Repeat("d", 64),
	), Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(context.Background(), executable, DefaultBuildIdentity(
		dependency, "sha256:"+strings.Repeat("e", 64),
	), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("CPU feature drift did not change adapter fingerprint")
	}
	invalid := DefaultBuildIdentity(dependency, "host-cpu")
	if _, err := New(context.Background(), executable, invalid, Options{}); !errors.Is(err, ErrExecutable) {
		t.Fatalf("untrusted CPU identity = %v", err)
	}
}

func TestAdapterBoundsOutputAndStderr(t *testing.T) {
	input, path, manifest, index, fingerprint := openFixture(t)
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), OutputBytes: 17})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, _, err := source.Frame(context.Background(), videoedit.RationalTime{Value: 0, Rate: 1}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized stdout error = %v", err)
	}
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), OutputBytes: 16, StderrBytes: 257})
	if _, _, err := source.Frame(context.Background(), videoedit.RationalTime{Value: 0, Rate: 1}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized stderr error = %v", err)
	}
}

func TestAdapterCancellationKillsBlockedProcess(t *testing.T) {
	input, path, manifest, index, fingerprint := openFixture(t)
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input)})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	writeFakeConfig(t, configPath, fakeFFmpegConfig{Block: true})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = source.Frame(ctx, videoedit.RationalTime{Value: 0, Rate: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("blocked FFmpeg process was not killed promptly")
	}
}

func TestFrameCancellationWhileWaitingForSerializedDescriptor(t *testing.T) {
	input, path, manifest, index, fingerprint := openFixture(t)
	executable, configPath := installFakeFFmpeg(t)
	started := filepath.Join(t.TempDir(), "started")
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), Block: true, StartedPath: started})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := source.Frame(firstCtx, videoedit.RationalTime{Value: 0, Rate: 1})
		firstDone <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancelFirst()
			t.Fatal("first decoder did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelWait()
	if _, _, err := source.Frame(waitCtx, videoedit.RationalTime{Value: 0, Rate: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting frame cancellation error = %v", err)
	}
	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first frame cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first decoder was not terminated")
	}
}

func TestSourceConcurrentFramesAreSerializedAndDeterministic(t *testing.T) {
	input, path, manifest, index, fingerprint := openFixture(t)
	executable, configPath := installFakeFFmpeg(t)
	output := []byte{
		1, 0, 2, 0, 3, 0, 0xff, 0xff,
		4, 0, 5, 0, 6, 0, 0xff, 0xff,
	}
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), OutputHex: hex.EncodeToString(output)})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	const workers = 8
	hashes := make(chan string, workers)
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, receipt, err := source.Frame(context.Background(), videoedit.RationalTime{Value: 0, Rate: 1})
			errorsChannel <- err
			hashes <- receipt.SurfaceHash
		}()
	}
	wait.Wait()
	close(hashes)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for hash := range hashes {
		if first == "" {
			first = hash
		}
		if hash == "" || hash != first {
			t.Fatalf("non-deterministic hashes: first=%q got=%q", first, hash)
		}
	}
}

func TestAdapterRejectsSymlinkInputAndExecutableMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor profile is Unix-only")
	}
	input, path, manifest, index, fingerprint := openFixture(t)
	_ = input
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Open(context.Background(), fixtureOpenRequest(t, link, manifest, index, fingerprint)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink error = %v", err)
	}
	file, err := os.OpenFile(executable, os.O_APPEND|os.O_WRONLY, 0)
	if errors.Is(err, os.ErrPermission) {
		if chmodErr := os.Chmod(executable, 0o700); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		file, err = os.OpenFile(executable, os.O_APPEND|os.O_WRONLY, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("mutation"))
	_ = file.Close()
	if _, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint)); !errors.Is(err, ErrExecutable) {
		t.Fatalf("mutated executable error = %v", err)
	}
}

func TestDecodeRejectsNonOpaqueOutput(t *testing.T) {
	options, _ := normalizeOptions(Options{MaxFrameBytes: 1024, MaxWidth: 10, MaxHeight: 10})
	_, err := decodeOpaqueRGBA64LE([]byte{0, 0, 0, 0, 0, 0, 1, 0}, 1, 1, options)
	if !errors.Is(err, ErrOutput) {
		t.Fatalf("alpha error = %v", err)
	}
}

func TestDecodedFrameBudgetCoversScratchAndImmutableSurface(t *testing.T) {
	tooSmall, _ := normalizeOptions(Options{MaxFrameBytes: 15, MaxWidth: 1, MaxHeight: 1})
	if _, err := newRGBA64PixelWriter(1, 1, tooSmall); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("undersized working-set budget error = %v", err)
	}
	exact, _ := normalizeOptions(Options{MaxFrameBytes: 16, MaxWidth: 1, MaxHeight: 1})
	writer, err := newRGBA64PixelWriter(1, 1, exact)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range [][]byte{{1, 0, 2}, {0, 3, 0, 0xff, 0xff}} {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Surface(1, 1, exact); err != nil {
		t.Fatalf("exact two-frame-buffer budget failed: %v", err)
	}
}

func TestAdapterRequiresCompleteVideoSemanticsEvidence(t *testing.T) {
	input, path, manifest, index, fingerprint := openFixture(t)
	executable, configPath := installFakeFFmpeg(t)
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input)})
	adapter := newTestAdapter(t, executable, Options{MaxInputBytes: 1024, MaxFrameBytes: 1024, MaxStderrBytes: 256, MaxWidth: 64, MaxHeight: 64})
	if _, err := adapter.Open(context.Background(), OpenRequest{
		Path: path, Manifest: manifest, Index: index, IndexFingerprint: fingerprint,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing semantic evidence error = %v", err)
	}
	request := fixtureOpenRequest(t, path, manifest, index, fingerprint)
	request.Semantics.Observations.ProgressiveOnly = false
	request.SemanticsFingerprint, _ = fingerprintJSON(request.Semantics)
	if _, err := adapter.Open(context.Background(), request); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("incomplete semantic evidence error = %v", err)
	}
}

func TestVideoSemanticsEvidenceCanonicalRoundTripAndStrictDecode(t *testing.T) {
	_, _, manifest, index, fingerprint := openFixture(t)
	request := fixtureOpenRequest(t, "unused", manifest, index, fingerprint)
	canonical, err := CanonicalVideoSemanticsEvidence(context.Background(), manifest, request.Semantics)
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedFingerprint, err := DecodeVideoSemanticsEvidence(context.Background(), manifest, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != request.Semantics || decodedFingerprint != request.SemanticsFingerprint {
		t.Fatalf("semantic evidence round trip = %+v %q", decoded, decodedFingerprint)
	}
	unknown := []byte(strings.Replace(string(canonical), `"profile":`, `"unknown":1,"profile":`, 1))
	if _, _, err := DecodeVideoSemanticsEvidence(context.Background(), manifest, unknown); err == nil {
		t.Fatal("unknown semantic evidence member was accepted")
	}
}

func TestGeometryNormalizationUsesNearestEvenSARAndQuarterTurn(t *testing.T) {
	for _, test := range []struct {
		width int
		ratio probe.Ratio
		want  int
	}{
		{width: 3, ratio: probe.Ratio{Numerator: 4, Denominator: 3}, want: 4},
		{width: 1, ratio: probe.Ratio{Numerator: 5, Denominator: 2}, want: 2},
		{width: 1, ratio: probe.Ratio{Numerator: 7, Denominator: 2}, want: 4},
	} {
		got, err := roundedDisplayWidth(test.width, test.ratio)
		if err != nil || got != test.want {
			t.Fatalf("roundedDisplayWidth(%d,%+v) = %d,%v; want %d", test.width, test.ratio, got, err, test.want)
		}
	}
	manifest, _ := fixtureManifestAndIndex(t, []byte("fixture"), []FrameRecord{{PTS: 0, DurationPTS: 100, Keyframe: true}})
	video := manifest.Streams[0].Video
	video.Width, video.Height, video.RotationDegrees = 3, 2, 90
	video.SampleAspectRatio = &probe.Ratio{Numerator: 4, Denominator: 3}
	options, _ := normalizeOptions(Options{MaxFrameBytes: 1024, MaxWidth: 16, MaxHeight: 16})
	normalized, err := normalizeVideoStream(manifest, 0, options)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.squareWidth != 4 || normalized.outputWidth != 2 || normalized.outputHeight != 4 ||
		!strings.Contains(filterGraph(normalized, 0), "transpose=cclock") || !strings.HasSuffix(filterGraph(normalized, 0), "setsar=1") {
		t.Fatalf("normalized = %+v filter=%q", normalized, filterGraph(normalized, 0))
	}
	video.RotationDegrees = 45
	if _, err := normalizeVideoStream(manifest, 0, options); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("arbitrary rotation error = %v", err)
	}
}

func fixtureManifestAndIndex(t testing.TB, input []byte, frames []FrameRecord) (probe.ProbeManifest, FrameIndex) {
	t.Helper()
	digest := sha256.Sum256(input)
	resourceHash := "sha256:" + hex.EncodeToString(digest[:])
	duration := int64(100)
	keyframes := make([]int64, 0)
	for _, frame := range frames {
		if frame.Keyframe {
			keyframes = append(keyframes, frame.PTS)
		}
	}
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "asset-video", ContentHash: resourceHash, SizeBytes: int64(len(input))},
		Probe:         probe.ProbeIdentity{Implementation: "test", BuildFingerprint: "sha256:" + strings.Repeat("a", 64), OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container:     probe.ContainerInfo{FormatName: "test", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:      videoedit.RationalTime{Value: 100, Rate: 1000},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "h264", TimeBase: videoedit.RationalTime{Value: 1, Rate: 1000}, StartPTS: 0, DurationPTS: &duration,
			Video: &probe.VideoStream{
				Width: 2, Height: 1, RotationDegrees: 0, SampleAspectRatio: &probe.Ratio{Numerator: 1, Denominator: 1},
				DisplayAspectRatio: &probe.Ratio{Numerator: 2, Denominator: 1}, PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateVFR,
				AverageFrameRate: &videoedit.RationalTime{Value: 30, Rate: 1}, Keyframes: keyframeSummary(0, keyframes),
				Color: probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatalf("probe fixture: %v", err)
	}
	index := FrameIndex{SchemaVersion: FrameIndexSchemaVersion, Profile: FrameIndexProfileV1, ProbeFingerprint: probeFingerprint, ResourceHash: resourceHash, StreamIndex: 0, TimeBase: videoedit.RationalTime{Value: 1, Rate: 1000}, Frames: append([]FrameRecord(nil), frames...)}
	return manifest, index
}

func openFixture(t *testing.T) ([]byte, string, probe.ProbeManifest, FrameIndex, string) {
	t.Helper()
	input := []byte("fixture")
	path := filepath.Join(realTempDir(t), "input.mp4")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, index := fixtureManifestAndIndex(t, input, []FrameRecord{{PTS: 0, DurationPTS: 100, Keyframe: true}})
	fingerprint, err := FrameIndexFingerprint(index, 100)
	if err != nil {
		t.Fatal(err)
	}
	return input, path, manifest, index, fingerprint
}

func fixtureOpenRequest(t testing.TB, path string, manifest probe.ProbeManifest, index FrameIndex, indexFingerprint string) OpenRequest {
	t.Helper()
	rotation := 0
	for _, stream := range manifest.Streams {
		if stream.Index == index.StreamIndex && stream.Video != nil {
			rotation = stream.Video.RotationDegrees
			break
		}
	}
	evidence, evidenceFingerprint, err := NewVideoSemanticsEvidence(
		context.Background(), manifest, index.StreamIndex, "cutly-test-scanner",
		"sha256:"+strings.Repeat("d", 64), "sha256:"+strings.Repeat("e", 64),
		VideoSemanticsObservations{
			ScanComplete: true, ProgressiveOnly: true, StaticColorOnly: true,
			RotationOnlyMatrix: true, RotationDegrees: rotation,
		},
	)
	if err != nil {
		t.Fatalf("semantic evidence fixture: %v", err)
	}
	return OpenRequest{
		Path: path, Manifest: manifest, Index: index, IndexFingerprint: indexFingerprint,
		Semantics: evidence, SemanticsFingerprint: evidenceFingerprint,
	}
}

func installFakeFFmpeg(t *testing.T) (string, string) {
	t.Helper()
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "fake-ffmpeg-"+strings.ReplaceAll(t.Name(), "/", "-"))
	source, err := os.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	return path, path + ".json"
}

func realTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeFakeConfig(t *testing.T, path string, config fakeFFmpegConfig) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestAdapter(t *testing.T, executable string, options Options) *Adapter {
	t.Helper()
	identity := DefaultBuildIdentity("sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64))
	adapter, err := New(context.Background(), executable, identity, options)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}
