package ffprobe

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/probe"
)

// These fixtures are actually encoded and muxed, then independently inspected
// with FFprobe before ScanVideo runs. No synthetic JSON or altered manifest is
// supplied to the scanner. MP4 proves contiguous VFR intervals; MPEG-TS preserves
// negative PTS with copyts/avoid_negative_ts disabled (MP4 edit lists can instead
// discard the negative presentation interval). Both use explicit progressive SDR.
func TestRealFFmpegVideoScanTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adapter, err := New(ctx, ffprobePath, DefaultBuildIdentity(testDigest("real timing runtime test only")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, extension, encoderTimeBase string
		negative                         bool
		timeBase                         videoedit.RationalTime
		pts, durations                   []int64
	}{
		{"contiguous-vfr-mp4", "mp4", "1/1000", false, videoedit.RationalTime{Value: 1, Rate: 16000}, []int64{0, 640, 1280, 2560, 3840}, []int64{640, 640, 1280, 1280, 640}},
		{"negative-pts-vfr-mpegts", "ts", "1/25", true, videoedit.RationalTime{Value: 1, Rate: 90000}, []int64{-7200, -3600, 0, 7200, 14400}, []int64{3600, 3600, 3600, 3600, 3600}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(canonicalTempDir(t), "timing."+test.extension)
			expression := "settb=1/1000,setpts='if(lt(N,2),N*40,80+(N-2)*80)"
			if test.negative {
				expression += "-80"
			}
			expression += "',setparams=field_mode=prog:color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=limited"
			arguments := []string{"-v", "error", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=25", "-frames:v", "5", "-vf", expression,
				"-c:v", "libx264", "-bf", "0", "-pix_fmt", "yuv420p", "-enc_time_base", test.encoderTimeBase, "-fps_mode", "passthrough", "-avoid_negative_ts", "disabled",
				"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv", "-chroma_sample_location", "left"}
			if test.extension == "ts" {
				arguments = append(arguments, "-mpegts_copyts", "1", "-muxdelay", "0")
			}
			arguments = append(arguments, "-y", path)
			if raw, err := exec.CommandContext(ctx, ffmpeg, arguments...).CombinedOutput(); err != nil {
				t.Fatalf("encode timing fixture: %v: %s", err, raw)
			}
			raw, err := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-select_streams", "0", "-show_frames", "-print_format", "json", path).Output()
			if err != nil {
				t.Fatalf("independent frame inspection: %v", err)
			}
			var observed struct {
				Frames []struct {
					PTS            json.RawMessage `json:"pts"`
					Duration       json.RawMessage `json:"duration"`
					PacketDuration json.RawMessage `json:"pkt_duration"`
					Keyframe       int             `json:"key_frame"`
				} `json:"frames"`
			}
			if err := json.Unmarshal(raw, &observed); err != nil {
				t.Fatal(err)
			}
			if len(observed.Frames) != len(test.pts) {
				t.Fatalf("encoded frame count=%d, want %d", len(observed.Frames), len(test.pts))
			}
			records := make([]videodecode.FrameRecord, len(observed.Frames))
			for i, frame := range observed.Frames {
				pts, err := scanInteger(frame.PTS, "independent pts")
				if err != nil {
					t.Fatal(err)
				}
				durationRaw := frame.Duration
				if len(durationRaw) == 0 {
					durationRaw = frame.PacketDuration
				}
				duration, err := scanInteger(durationRaw, "independent duration")
				if err != nil {
					t.Fatal(err)
				}
				if pts != test.pts[i] || duration != test.durations[i] || duration <= 0 {
					t.Fatalf("encoded frame %d=(%d,%d), want (%d,%d)", i, pts, duration, test.pts[i], test.durations[i])
				}
				records[i] = videodecode.FrameRecord{PTS: pts, DurationPTS: duration, Keyframe: frame.Keyframe == 1}
			}
			request := Request{AssetID: "real-timing", Path: path}
			manifest, diagnostics, err := adapter.Probe(ctx, request)
			if err != nil || probe.HasErrorDiagnostics(diagnostics) {
				t.Fatalf("probe: %v, diagnostics=%+v", err, diagnostics)
			}
			stream := manifest.Streams[0]
			if stream.TimeBase != test.timeBase || stream.StartPTS != test.pts[0] || stream.Video == nil {
				t.Fatalf("unexpected exact stream clock: %+v", stream)
			}
			// Legacy Probe may retain unknown rate when this FFprobe version emits
			// duration rather than pkt_duration. Prove VFR from actual PTS instead
			// of changing that legacy manifest or relying on its average rate.
			if records[1].PTS-records[0].PTS == records[3].PTS-records[2].PTS {
				t.Fatal("encoded fixture did not retain variable presentation steps")
			}
			result, err := adapter.ScanVideo(ctx, request, manifest, stream.Index, VideoScanOptions{MaxFrames: len(records)})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Index.Frames, records) {
				t.Fatalf("scanner=%+v independent=%+v", result.Index.Frames, records)
			}
			if err := videodecode.BindFrameIndex(ctx, manifest, result.Index, len(records)); err != nil {
				t.Fatal(err)
			}
			last := records[len(records)-1]
			if stream.DurationPTS == nil || stream.StartPTS+*stream.DurationPTS != last.PTS+last.DurationPTS {
				t.Fatal("index does not close actual stream interval")
			}
			if test.negative && result.Index.Frames[0].PTS >= 0 {
				t.Fatal("container erased negative PTS")
			}
			if err := videodecode.ValidateVideoSemanticsEvidence(ctx, manifest, result.Semantics); err != nil {
				t.Fatal(err)
			}
			if fingerprint, err := videodecode.FrameIndexFingerprint(result.Index, len(records)); err != nil || fingerprint != result.IndexFingerprint {
				t.Fatal("index fingerprint", fingerprint, err)
			}
			if fingerprint, err := videodecode.VideoSemanticsEvidenceFingerprint(ctx, manifest, result.Semantics); err != nil || fingerprint != result.SemanticsFingerprint {
				t.Fatal("semantics fingerprint", fingerprint, err)
			}
			t.Logf("encoded exact time_base=%+v pts=%v durations=%v", stream.TimeBase, test.pts, test.durations)
		})
	}
}
