package streamencode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

type recordingWriter struct {
	frames [][]byte
	want   int
}

func (writer *recordingWriter) FrameBytes() int { return writer.want }

func (writer *recordingWriter) WriteFrame(_ context.Context, frame []byte) error {
	if len(frame) != writer.want {
		return errors.New("frame size mismatch")
	}
	writer.frames = append(writer.frames, append([]byte(nil), frame...))
	return nil
}

func chunkPayload(t *testing.T, frames, width, height int) []byte {
	t.Helper()
	var payload bytes.Buffer
	for frame := 0; frame < frames; frame++ {
		pixels := make([]cpucompositor.Pixel, width*height)
		for index := range pixels {
			alpha := uint16(0xC000 + (frame+index)%0x3FFF)
			pixels[index] = cpucompositor.Pixel{R: uint16(frame*100) & alpha, G: uint16(index) & alpha, B: uint16(frame*7+index) & alpha, A: alpha}
		}
		surface, err := cpucompositor.NewSurfaceFromPixels(width, height, pixels)
		if err != nil {
			t.Fatal(err)
		}
		if err := surface.WriteCanonical(&payload); err != nil {
			t.Fatal(err)
		}
	}
	return payload.Bytes()
}

// The feeder must decode chunk units through the exact reference color stage and reject
// truncated or trailing payloads.
func TestFeedChunkVideoConvertsAndValidates(t *testing.T) {
	const width, height, frames = 6, 4, 3
	payload := chunkPayload(t, frames, width, height)
	writer := &recordingWriter{want: width * height * 4}
	if err := FeedChunkVideo(context.Background(), writer, bytes.NewReader(payload), frames, width, height); err != nil {
		t.Fatal(err)
	}
	if len(writer.frames) != frames {
		t.Fatalf("fed %d frames, want %d", len(writer.frames), frames)
	}
	// Truncated payload fails closed.
	if err := FeedChunkVideo(context.Background(), &recordingWriter{want: width * height * 4}, bytes.NewReader(payload[:len(payload)-8]), frames, width, height); err == nil {
		t.Fatal("truncated payload accepted")
	}
	// Trailing bytes fail closed.
	if err := FeedChunkVideo(context.Background(), &recordingWriter{want: width * height * 4}, bytes.NewReader(append(append([]byte(nil), payload...), 0)), frames, width, height); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	// Geometry mismatch fails closed.
	if err := FeedChunkVideo(context.Background(), &recordingWriter{want: 999}, bytes.NewReader(payload), frames, width, height); err == nil {
		t.Fatal("geometry mismatch accepted")
	}
}

// Differential gate: frames fed through the chunk bridge must be byte-identical to the frames
// a session's direct output-color stage produces, and the resulting MP4s identical.
//
//	CUTLY_TEST_FFMPEG=/path/to/ffmpeg go test ./render/streamencode -run TestChunkFeedMatchesDirectEncode -v
func TestChunkFeedMatchesDirectEncode(t *testing.T) {
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
	const width, height, frames = 12, 10, 6
	payload := chunkPayload(t, frames, width, height)

	encode := func(output string, feed func(*Encoder) error) Result {
		t.Helper()
		encoder, err := Start(context.Background(), Options{Executable: staged, OutputPath: output,
			Width: width, Height: height, FrameRateNumerator: 24, FrameRateDenominator: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := feed(encoder); err != nil {
			encoder.Abort()
			t.Fatal(err)
		}
		result, err := encoder.Finish(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	bridged := encode(filepath.Join(t.TempDir(), "bridged.mp4"), func(encoder *Encoder) error {
		return FeedChunkVideo(context.Background(), encoder, bytes.NewReader(payload), frames, width, height)
	})
	direct := encode(filepath.Join(t.TempDir(), "direct.mp4"), func(encoder *Encoder) error {
		// Decode the same canonical units through the identical helpers the bridge uses,
		// feeding them frame by frame without the chunk-reader shell.
		reader := bytes.NewReader(payload)
		unit := width * height * 8
		for frame := 0; frame < frames; frame++ {
			raw := make([]byte, unit)
			if _, err := reader.Read(raw); err != nil {
				return err
			}
			surface, err := surfaceFromCanonical(raw, width, height)
			if err != nil {
				return err
			}
			framesBytes, err := directFrameBytes(context.Background(), surface)
			if err != nil {
				return err
			}
			if err := encoder.WriteFrame(context.Background(), framesBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if bridged.OutputHash != direct.OutputHash {
		t.Fatalf("bridged %s != direct %s", bridged.OutputHash, direct.OutputHash)
	}
}
