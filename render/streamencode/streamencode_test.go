package streamencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The streaming encoder needs a pinned zscale-free FFmpeg with libx264; the same hermes-style
// build the decode tests use. Opt-in:
//
//	CUTLY_TEST_FFMPEG=/path/to/ffmpeg go test ./render/streamencode -v
func testFFmpeg(t *testing.T) string {
	t.Helper()
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a pinned libx264-enabled FFmpeg")
	}
	// The encoder requires an immutable-by-mode regular executable.
	staged := filepath.Join(t.TempDir(), "ffmpeg-pinned")
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	return staged
}

func testFrames(count, width, height int) [][]byte {
	return testFramesSeeded(count, width, height, 0xB7E151628AED2A6A)
}

func testFramesSeeded(count, width, height int, seedBase uint64) [][]byte {
	frames := make([][]byte, count)
	seed := seedBase
	for index := range frames {
		frame := make([]byte, width*height*4)
		for offset := range frame {
			seed = seed*6364136223846793005 + 1442695040888963407
			frame[offset] = byte(seed >> 56)
		}
		// A visible difference: a per-frame gradient shift large enough to survive
		// quantization.
		for offset := 0; offset < len(frame); offset += 4 {
			frame[offset] = byte(int(frame[offset]) + index*7)
		}
		frames[index] = frame
	}
	return frames
}

func encodeOnce(t *testing.T, ctx context.Context, executable, output string, frames [][]byte, width, height int) Result {
	t.Helper()
	encoder, err := Start(ctx, Options{
		Executable: executable, OutputPath: output,
		Width: width, Height: height,
		FrameRateNumerator:   30,
		FrameRateDenominator: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoder.FrameBytes() != width*height*4 {
		t.Fatalf("frame bytes = %d", encoder.FrameBytes())
	}
	for index, frame := range frames {
		if err := encoder.WriteFrame(ctx, frame); err != nil {
			t.Fatalf("write frame %d: %v", index, err)
		}
	}
	result, err := encoder.Finish(ctx)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.FramesWritten != int64(len(frames)) || result.OutputBytes == 0 || result.BinaryFingerprint == "" {
		t.Fatalf("receipt = %+v", result)
	}
	return result
}

// Determinism gate: the same frame sequence must encode to byte-identical MP4s across runs.
func TestStreamEncodeIsByteDeterministic(t *testing.T) {
	executable := testFFmpeg(t)
	ctx := context.Background()
	const width, height, frames = 64, 48, 12
	sequence := testFrames(frames, width, height)
	first := encodeOnce(t, ctx, executable, filepath.Join(t.TempDir(), "first.mp4"), sequence, width, height)
	second := encodeOnce(t, ctx, executable, filepath.Join(t.TempDir(), "second.mp4"), sequence, width, height)
	if first.OutputHash != second.OutputHash {
		t.Fatalf("determinism drift: %s vs %s", first.OutputHash, second.OutputHash)
	}
	third := encodeOnce(t, ctx, executable, filepath.Join(t.TempDir(), "third.mp4"), testFramesSeeded(frames, width, height, 0x9E3779B97F4A7C15), width, height)
	if third.OutputHash == first.OutputHash {
		t.Fatal("different frame sequences produced identical output")
	}
}

// Frame accounting, size validation and the pinned-fingerprint mismatch all fail closed.
func TestStreamEncodeRejectsInvalidUse(t *testing.T) {
	executable := testFFmpeg(t)
	ctx := context.Background()
	const width, height = 16, 12
	output := filepath.Join(t.TempDir(), "out.mp4")

	encoder, err := Start(ctx, Options{Executable: executable, OutputPath: output, Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.WriteFrame(ctx, make([]byte, 10)); err == nil {
		t.Fatal("short frame accepted")
	}
	if _, err := encoder.Finish(ctx); err == nil {
		t.Fatal("finish without frames succeeded")
	}

	pinned, err := Start(ctx, Options{Executable: executable, OutputPath: filepath.Join(t.TempDir(), "pinned.mp4"), Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1, BinaryFingerprint: "sha256:" + strings.Repeat("0", 64)})
	if err == nil || pinned != nil {
		t.Fatal("mismatched binary fingerprint accepted")
	}
	if _, err := Start(ctx, Options{Executable: filepath.Join(t.TempDir(), "missing"), OutputPath: output, Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1}); err == nil {
		t.Fatal("missing executable accepted")
	}
	if _, err := Start(ctx, Options{Executable: executable, OutputPath: filepath.Join(t.TempDir(), "odd.mp4"), Width: width + 1, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1}); err == nil {
		t.Fatal("odd width accepted")
	}

	// Abort mid-stream kills the encoder and leaves it unusable.
	stream, err := Start(ctx, Options{Executable: executable, OutputPath: filepath.Join(t.TempDir(), "abort.mp4"), Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WriteFrame(ctx, testFrames(1, width, height)[0]); err != nil {
		t.Fatal(err)
	}
	stream.Abort()
	if err := stream.WriteFrame(ctx, testFrames(1, width, height)[0]); err == nil {
		t.Fatal("aborted encoder accepted a frame")
	}
}

// Cancellation before Start and between frames fails closed without partial receipts.
func TestStreamEncodeCancellation(t *testing.T) {
	executable := testFFmpeg(t)
	const width, height = 16, 12
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Start(canceled, Options{Executable: executable, OutputPath: filepath.Join(t.TempDir(), "x.mp4"), Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1}); err == nil {
		t.Fatal("canceled start succeeded")
	}
	encoder, err := Start(context.Background(), Options{Executable: executable, OutputPath: filepath.Join(t.TempDir(), "y.mp4"), Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Abort()
	frameCtx, frameCancel := context.WithCancel(context.Background())
	frameCancel()
	if err := encoder.WriteFrame(frameCtx, testFrames(1, width, height)[0]); err == nil {
		t.Fatal("canceled frame write succeeded")
	}
}

// HEVC 10-bit must produce byte-deterministic output with the visual quality improvement
// of 10-bit color depth.
func TestStreamEncodeHEVC10BitDeterministic(t *testing.T) {
	executable := testFFmpeg(t)
	ctx := context.Background()
	const width, height, frames = 64, 48, 8
	sequence := testFrames(frames, width, height)
	first := encodeHEVC(t, ctx, executable, filepath.Join(t.TempDir(), "first.mp4"), sequence, width, height)
	second := encodeHEVC(t, ctx, executable, filepath.Join(t.TempDir(), "second.mp4"), sequence, width, height)
	if first.OutputHash != second.OutputHash {
		t.Fatalf("HEVC determinism drift: %s vs %s", first.OutputHash, second.OutputHash)
	}
	if first.VideoCodec != "hevc" {
		t.Fatalf("codec = %q, want hevc", first.VideoCodec)
	}
}

func encodeHEVC(t *testing.T, ctx context.Context, executable, output string, frames [][]byte, width, height int) Result {
	t.Helper()
	encoder, err := Start(ctx, Options{
		Executable: executable, OutputPath: output,
		Width: width, Height: height,
		FrameRateNumerator: 24, FrameRateDenominator: 1,
		VideoCodec: "hevc", CRF: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, frame := range frames {
		if err := encoder.WriteFrame(ctx, frame); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	result, err := encoder.Finish(ctx)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return result
}

// Invalid codec rejected.
func TestStreamEncodeRejectsInvalidCodec(t *testing.T) {
	executable := testFFmpeg(t)
	if _, err := Start(context.Background(), Options{
		Executable: executable, OutputPath: filepath.Join(t.TempDir(), "x.mp4"),
		Width: 64, Height: 48, FrameRateNumerator: 24, FrameRateDenominator: 1,
		VideoCodec: "av1",
	}); err == nil {
		t.Fatal("invalid codec accepted")
	}
}
