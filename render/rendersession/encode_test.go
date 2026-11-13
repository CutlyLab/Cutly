package rendersession

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/streamencode"
)

// The session-to-encoder path needs a pinned libx264 FFmpeg:
//
//	CUTLY_TEST_FFMPEG=/path/to/ffmpeg go test ./render/rendersession -run TestEncodeFrames -v
func TestEncodeFramesProducesDeterministicMP4(t *testing.T) {
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a pinned libx264-enabled FFmpeg")
	}
	staged := filepath.Join(t.TempDir(), "ffmpeg-pinned")
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, raw, 0o500); err != nil {
		t.Fatal(err)
	}

	document, bindings, config := sessionFixture(t)
	requests := make([]FrameRequest, 12)
	for index := range requests {
		requests[index] = FrameRequest{TimelineID: "timeline", At: videoedit.RationalTime{Value: int64(index) + 20, Rate: 30}}
	}
	encode := func(output string) streamencode.Result {
		t.Helper()
		session, err := Open(context.Background(), document, bindings, config, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		encoder, err := streamencode.Start(context.Background(), streamencode.Options{
			Executable: staged, OutputPath: output,
			Width: config.Width, Height: config.Height,
			FrameRateNumerator: 30, FrameRateDenominator: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := session.EncodeFrames(context.Background(), requests, ParallelOptions{Workers: 4}, encoder)
		if err != nil {
			t.Fatalf("encode %s: %v", output, err)
		}
		if result.FramesWritten != int64(len(requests)) {
			t.Fatalf("frames written = %d", result.FramesWritten)
		}
		return result
	}
	first := encode(filepath.Join(t.TempDir(), "first.mp4"))
	second := encode(filepath.Join(t.TempDir(), "second.mp4"))
	if first.OutputHash != second.OutputHash {
		t.Fatalf("encode determinism drift: %s vs %s", first.OutputHash, second.OutputHash)
	}
	if _, err := os.Stat(first.OutputPath); err != nil {
		t.Fatalf("output missing: %v", err)
	}
}
