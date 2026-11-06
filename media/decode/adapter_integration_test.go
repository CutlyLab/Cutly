package videodecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

// This test is opt-in because ordinary FFmpeg distributions frequently omit
// libzimg/zscale. Point CUTLY_TEST_FFMPEG at a controlled zscale-enabled build
// to exercise the real inherited-fd demux/decode/filter/rawvideo boundary.
func TestRealFFmpegDescriptorDecode(t *testing.T) {
	executable := os.Getenv("CUTLY_TEST_FFMPEG")
	if executable == "" {
		t.Skip("set CUTLY_TEST_FFMPEG to a pinned zscale-enabled FFmpeg")
	}
	// One 2x2 limited-range black YUV420 frame in a deterministic y4m stream.
	input := append([]byte("YUV4MPEG2 W2 H2 F25:1 Ip A1:1 C420jpeg XCOLORRANGE=LIMITED\nFRAME\n"),
		[]byte{16, 16, 16, 16, 128, 128}...)
	path := filepath.Join(realTempDir(t), "one-frame.y4m")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, index := fixtureManifestAndIndex(t, input, []FrameRecord{{PTS: 0, DurationPTS: 1, Keyframe: true}})
	duration := int64(1)
	manifest.Container.FormatName = "yuv4mpegpipe"
	manifest.Duration = videoedit.RationalTime{Value: 1, Rate: 25}
	manifest.Streams[0].TimeBase = videoedit.RationalTime{Value: 1, Rate: 25}
	manifest.Streams[0].DurationPTS = &duration
	manifest.Streams[0].Video.Width = 2
	manifest.Streams[0].Video.Height = 2
	manifest.Streams[0].Video.DisplayAspectRatio = &probe.Ratio{Numerator: 1, Denominator: 1}
	manifest.Streams[0].Video.AverageFrameRate = &videoedit.RationalTime{Value: 25, Rate: 1}
	index.TimeBase = manifest.Streams[0].TimeBase
	index.ProbeFingerprint, _ = probe.Fingerprint(manifest)
	indexFingerprint, err := FrameIndexFingerprint(index, 10)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(context.Background(), executable, DefaultBuildIdentity(
		"sha256:"+strings.Repeat("d", 64), "sha256:"+strings.Repeat("e", 64),
	), Options{
		MaxInputBytes: 1 << 20, MaxFrameBytes: 1 << 20, MaxStderrBytes: 1 << 20,
		MaxIndexedFrames: 10, MaxWidth: 16, MaxHeight: 16, MaxDecodeMillis: 10_000,
	})
	if err != nil {
		t.Fatalf("controlled FFmpeg is incompatible with the decode profile: %v", err)
	}
	source, err := adapter.Open(context.Background(), fixtureOpenRequest(t, path, manifest, index, indexFingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	surface, receipt, err := source.Frame(context.Background(), videoedit.RationalTime{Value: 0, Rate: 1})
	if err != nil {
		t.Fatal(err)
	}
	if surface.Width() != 2 || surface.Height() != 2 || receipt.DisplayPTS != 0 || receipt.SurfaceHash == "" {
		t.Fatalf("unexpected real decode: surface=%dx%d receipt=%+v", surface.Width(), surface.Height(), receipt)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			pixel, err := surface.Pixel(x, y)
			if err != nil || pixel.A != 0xffff {
				t.Fatalf("pixel %d,%d = %+v, %v", x, y, pixel, err)
			}
		}
	}
}
