package videodecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

func sessionFixtureFrames() []FrameRecord {
	return []FrameRecord{
		{PTS: 0, DurationPTS: 100, Keyframe: true},
		{PTS: 100, DurationPTS: 100, Keyframe: true},
		{PTS: 200, DurationPTS: 100, Keyframe: true},
	}
}

func openSessionSource(t *testing.T, frames []FrameRecord) (*Adapter, *Source) {
	t.Helper()
	input, path, manifest, index, fingerprint := sessionMultiFixture(t, frames)
	executable, configPath := installFakeFFmpeg(t)
	// Two valid premultiplied RGBA16 pixels (opaque black) for the 2x1 fixture output.
	writeFakeConfig(t, configPath, fakeFFmpegConfig{InputHex: hex.EncodeToString(input), OutputHex: "000000000000ffff000000000000ffff"})
	adapter, err := New(context.Background(), executable, DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), Options{MaxInputBytes: 1 << 20, MaxFrameBytes: 1 << 20, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: 100, MaxWidth: 64, MaxHeight: 64, MaxDecodeMillis: 30_000})
	if err != nil {
		t.Fatal(err)
	}
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	return adapter, source
}

func sessionMultiFixture(t *testing.T, frames []FrameRecord) ([]byte, string, probe.ProbeManifest, FrameIndex, string) {
	t.Helper()
	input := []byte("fixture-input")
	path := filepath.Join(realTempDir(t), "input.mp4")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, index := fixtureManifestAndIndex(t, input, frames)
	// The multi-frame index must close the stream interval, so the fixture duration covers
	// every frame; the index rebinds to the adjusted manifest fingerprint.
	perFrame := int64(100)
	total := perFrame * int64(len(frames))
	manifest.Duration = videoedit.RationalTime{Value: total, Rate: 1000}
	manifest.Streams[0].DurationPTS = &total
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	index.ProbeFingerprint = probeFingerprint
	fingerprint, err := FrameIndexFingerprint(index, 100)
	if err != nil {
		t.Fatal(err)
	}
	return input, path, manifest, index, fingerprint
}

func TestSessionServesRepeatsWithIdenticalSurfaceAndReceipt(t *testing.T) {
	_, source := openSessionSource(t, sessionFixtureFrames())
	session, err := NewSession(source, SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	firstAt := videoedit.RationalTime{Value: 0, Rate: 1000}
	first, firstReceipt, err := session.Frame(context.Background(), firstAt)
	if err != nil {
		t.Fatal(err)
	}
	repeated, repeatedReceipt, err := session.Frame(context.Background(), firstAt)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != first || repeatedReceipt != firstReceipt {
		t.Fatal("repeated frame is not the identical cached surface and receipt")
	}
	second, secondReceipt, err := session.Frame(context.Background(), videoedit.RationalTime{Value: 100, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if second == first || secondReceipt.FrameOrdinal == firstReceipt.FrameOrdinal {
		t.Fatal("different source times returned the same cached frame")
	}
	hits, misses, retained := session.Statistics()
	if hits != 1 || misses != 2 || retained != 2 {
		t.Fatalf("statistics hits=%d misses=%d retained=%d", hits, misses, retained)
	}
}

func TestSessionBoundsEvictLeastRecentlyUsed(t *testing.T) {
	_, source := openSessionSource(t, sessionFixtureFrames())
	session, err := NewSession(source, SessionOptions{MaxCachedFrames: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	time0 := videoedit.RationalTime{Value: 0, Rate: 1000}
	time1 := videoedit.RationalTime{Value: 100, Rate: 1000}
	time2 := videoedit.RationalTime{Value: 200, Rate: 1000}
	if _, _, err := session.Frame(context.Background(), time0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Frame(context.Background(), time1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Frame(context.Background(), time2); err != nil {
		t.Fatal(err)
	}
	if _, _, retained := session.Statistics(); retained != 2 {
		t.Fatalf("retained=%d want 2", retained)
	}
	// time0 was least recently used and evicted: it must decode again; time2 stays cached.
	if _, _, err := session.Frame(context.Background(), time0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Frame(context.Background(), time2); err != nil {
		t.Fatal(err)
	}
	hits, _, _ := session.Statistics()
	if hits != 1 {
		t.Fatalf("hits=%d want exactly the time2 replay", hits)
	}
}

func TestSessionCloseRejectsFurtherUse(t *testing.T) {
	_, source := openSessionSource(t, sessionFixtureFrames())
	session, err := NewSession(source, SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Frame(context.Background(), videoedit.RationalTime{Value: 0, Rate: 1000}); err == nil {
		t.Fatal("closed session decoded")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("double close = %v", err)
	}
}

func TestSessionConcurrentRepeatsAreSafe(t *testing.T) {
	_, source := openSessionSource(t, sessionFixtureFrames())
	session, err := NewSession(source, SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	at := videoedit.RationalTime{Value: 100, Rate: 1000}
	var wait sync.WaitGroup
	hashes := make([]string, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			surface, receipt, err := session.Frame(context.Background(), at)
			if err != nil {
				t.Errorf("concurrent frame: %v", err)
				return
			}
			hashes[index] = receipt.SurfaceHash
			if surface == nil {
				t.Error("concurrent frame returned no surface")
			}
		}(index)
	}
	wait.Wait()
	for index := 1; index < len(hashes); index++ {
		if hashes[index] != hashes[0] {
			t.Fatalf("concurrent repeats diverged: %q vs %q", hashes[index], hashes[0])
		}
	}
}

// TestRealFFmpegSessionBenchmark measures what the decode session removes on a real pinned
// decoder: a sequential 240-frame pass plus the complete replay pass (what live re-render
// verification does per frame), first through the raw source and then through the session.
// Opt-in because it needs a zscale-enabled FFmpeg and runs hundreds of decodes:
//
//	CUTLY_TEST_FFMPEG=/path/to/ffmpeg go test ./media/decode -run TestRealFFmpegSessionBenchmark -v
func TestRealFFmpegSessionBenchmark(t *testing.T) {
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a pinned zscale-enabled FFmpeg")
	}
	// The adapter requires an immutable-by-mode regular executable, so the benchmark stages a
	// private read-only copy of the pinned build.
	staged := filepath.Join(realTempDir(t), "ffmpeg-pinned")
	if raw, err := os.ReadFile(executable); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	executable = staged
	const (
		frames   = 240
		width    = 320
		height   = 180
		framePTS = 1
	)
	// Deterministic y4m: all-intra C420jpeg frames the decoder reads sequentially.
	frameBytes := width*height*3/2 + len("FRAME\n")
	input := make([]byte, 0, frameBytes*frames)
	input = append(input, []byte(fmt.Sprintf("YUV4MPEG2 W%d H%d F30:1 Ip A1:1 C420jpeg\n", width, height))...)
	for frame := 0; frame < frames; frame++ {
		input = append(input, []byte("FRAME\n")...)
		plane := make([]byte, width*height*3/2)
		for index := range plane {
			plane[index] = byte(frame + index)
		}
		input = append(input, plane...)
	}
	path := filepath.Join(realTempDir(t), "bench.y4m")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	records := make([]FrameRecord, frames)
	for frame := range records {
		records[frame] = FrameRecord{PTS: int64(frame), DurationPTS: framePTS, Keyframe: true}
	}
	duration := int64(frames)
	keyframePTS := make([]int64, frames)
	for frame := range keyframePTS {
		keyframePTS[frame] = int64(frame)
	}
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: "bench", ContentHash: "sha256:" + hex.EncodeToString(sumSha(input)), SizeBytes: int64(len(input))},
		Probe:         probe.ProbeIdentity{Implementation: "bench", BuildFingerprint: "sha256:" + strings.Repeat("a", 64), OptionsFingerprint: "sha256:" + strings.Repeat("b", 64)},
		Container:     probe.ContainerInfo{FormatName: "yuv4mpegpipe", StartTime: videoedit.RationalTime{Value: 0, Rate: 1}, Seekable: true},
		Duration:      videoedit.RationalTime{Value: duration, Rate: 30},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "rawvideo", TimeBase: videoedit.RationalTime{Value: 1, Rate: 30}, StartPTS: 0, DurationPTS: &duration,
			Video: &probe.VideoStream{
				Width: width, Height: height, RotationDegrees: 0,
				SampleAspectRatio: &probe.Ratio{Numerator: 1, Denominator: 1}, DisplayAspectRatio: &probe.Ratio{Numerator: width, Denominator: height},
				PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateVFR, AverageFrameRate: &videoedit.RationalTime{Value: 30, Rate: 1},
				Keyframes: keyframeSummary(0, keyframePTS),
				Color:     probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited, ChromaLocation: "left"},
			},
		}},
	}
	probeFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	index := FrameIndex{SchemaVersion: FrameIndexSchemaVersion, Profile: FrameIndexProfileV1, ProbeFingerprint: probeFingerprint, ResourceHash: manifest.Resource.ContentHash, StreamIndex: 0, TimeBase: videoedit.RationalTime{Value: 1, Rate: 30}, Frames: records}
	indexFingerprint, err := FrameIndexFingerprint(index, 1000)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(context.Background(), executable, DefaultBuildIdentity(
		"sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64),
	), Options{MaxInputBytes: 64 << 20, MaxFrameBytes: 64 << 20, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: 1000, MaxWidth: 4096, MaxHeight: 4096, MaxDecodeMillis: 60_000})
	if err != nil {
		t.Fatalf("pinned FFmpeg is incompatible with the decode profile: %v", err)
	}
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, indexFingerprint))
	if err != nil {
		t.Fatal(err)
	}

	decodePass := func(label string, frame func(int) error) time.Duration {
		start := time.Now()
		for frameIndex := 0; frameIndex < frames; frameIndex++ {
			if err := frame(frameIndex); err != nil {
				t.Fatalf("%s frame %d: %v", label, frameIndex, err)
			}
		}
		return time.Since(start)
	}
	at := func(frame int) videoedit.RationalTime { return videoedit.RationalTime{Value: int64(frame), Rate: 30} }

	// The realistic per-frame pattern: decode once for pixels, immediately re-request the
	// same frame for live replay verification — twice through the raw source (two decoder
	// spawns) versus twice through the session (one decode plus one cache hit).
	rawWithReplay := decodePass("raw", func(frame int) error {
		if _, _, err := source.Frame(context.Background(), at(frame)); err != nil {
			return err
		}
		_, _, err := source.Frame(context.Background(), at(frame))
		return err
	})

	session, err := NewSession(source, SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	sessionWithReplay := decodePass("session", func(frame int) error {
		if _, _, err := session.Frame(context.Background(), at(frame)); err != nil {
			return err
		}
		_, _, err := session.Frame(context.Background(), at(frame))
		return err
	})
	hits, _, retained := session.Statistics()
	t.Logf("decode+replay %d frames %dx%d: raw source %s (%.1f ms/frame, 2 spawns), session %s (%.1f ms/frame, 1 spawn + 1 hit), hits %d retained %d",
		frames, width, height,
		rawWithReplay, float64(rawWithReplay.Milliseconds())/frames,
		sessionWithReplay, float64(sessionWithReplay.Milliseconds())/frames,
		hits, retained)
	if hits != frames {
		t.Fatalf("expected every replay to hit the cache, hits=%d", hits)
	}
}

func sumSha(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}
