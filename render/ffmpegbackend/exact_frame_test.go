package ffmpegbackend

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestCompileExactFrameUsesEvaluatorAndSharedFrameWindow(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	request := exactFrameRequest(fixture.request, 30, fileURI(filepath.Join(fixture.outputRoot, "frame-30.png")))
	spec, evaluation, err := compileExactFrameCommand(request, ".cutly-frame.png", fixture.profiles)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Arguments, " ")
	for _, want := range []string{
		"trim=start_frame=30:end_frame=31",
		"format=pix_fmts=rgba[vout]",
		"-frames:v 1",
		"-c:v png",
		"-f image2pipe",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("exact-frame argv does not contain %q: %s", want, joined)
		}
	}
	if evaluation.CompositionFrameIndex != 30 || evaluation.TimelineTime != rational(1, 1) ||
		len(evaluation.Video) != 1 || evaluation.Video[0].ClipID != "video-clip-b" || evaluation.Video[0].SourceTime != rational(0, 1) {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	if len(spec.InputPaths) != len(fixture.request.Manifest.Resources) {
		t.Fatalf("input paths = %q", spec.InputPaths)
	}
	var _ renderapi.ExactFrameBackend = fixture.backend
}

func TestEvaluateVideoFrameWindowRejectsOutsideAndEmpty(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	for _, test := range []struct {
		first int64
		count int64
	}{
		{first: fixture.request.Plan.FrameRange.First, count: 0},
		{first: fixture.request.Plan.FrameRange.First - 1, count: 1},
		{first: fixture.request.Plan.FrameRange.First + fixture.request.Plan.FrameRange.Count, count: 1},
	} {
		if _, err := evaluateVideoFrameWindow(fixture.request.Plan, test.first, test.count); !errors.Is(err, ErrPreflight) {
			t.Fatalf("window [%d,+%d) error = %v", test.first, test.count, err)
		}
	}
}

func TestCompileExactFrameUsesCompositionIndexOffsetForNegativeRange(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	plan := clonePlan(t, fixture.request.Plan)
	// The requested timeline remains [0,2), but this composition clock names its frames
	// [-30,30). Preview must trim by the plan-relative offset, never by the signed absolute
	// composition index.
	plan.Target.FrameClock.Origin = rational(1, 1)
	plan.FrameRange = videoedit.TickRange{First: -30, Count: 60}
	plan.Fingerprint = refingerprintPlan(t, plan)

	for _, test := range []struct {
		index      int64
		wantTrim   string
		wantClip   videoedit.ID
		wantSource videoedit.RationalTime
	}{
		{index: -1, wantTrim: "trim=start_frame=29:end_frame=30", wantClip: "video-clip-a", wantSource: rational(29, 30)},
		{index: 0, wantTrim: "trim=start_frame=30:end_frame=31", wantClip: "video-clip-b", wantSource: rational(0, 1)},
	} {
		request := exactFrameRequest(fixture.request, test.index, fileURI(filepath.Join(fixture.outputRoot, "negative-frame.png")))
		request.Plan = plan
		request.Frame.PlanFingerprint = plan.Fingerprint
		spec, evaluation, err := compileExactFrameCommand(request, ".cutly-frame.png", fixture.profiles)
		if err != nil {
			t.Fatalf("frame %d: %v", test.index, err)
		}
		if joined := strings.Join(spec.Arguments, " "); !strings.Contains(joined, test.wantTrim) {
			t.Fatalf("frame %d argv does not contain %q: %s", test.index, test.wantTrim, joined)
		}
		if evaluation.CompositionFrameIndex != test.index || len(evaluation.Video) != 1 ||
			evaluation.Video[0].ClipID != test.wantClip || evaluation.Video[0].SourceTime != test.wantSource {
			t.Fatalf("frame %d evaluation = %+v", test.index, evaluation)
		}
	}
}

func TestPreflightRejectsCompositionClockPhaseDrift(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	plan := clonePlan(t, fixture.request.Plan)
	// The first frame is at 1/60 even though the requested/media interval starts at zero. The
	// semantic plan is valid, but hard-cut v1 cannot rebase it to PTS zero without selecting the
	// wrong source time.
	plan.Target.FrameClock.Origin = rational(1, 60)
	frameRange, err := videoedit.CompileFrameTickRange(plan.RequestedRange, plan.Target.FrameClock)
	if err != nil {
		t.Fatal(err)
	}
	plan.FrameRange = frameRange
	plan.Fingerprint = refingerprintPlan(t, plan)
	manifest, err := renderapi.FreezeExecutionManifest(plan, fixture.request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := fixture.backend.Preflight(context.Background(), plan, manifest)
	if !hasDiagnostic(diagnostics, CodeTimelineUnsupported) || !diagnosticsContain(diagnostics, "first composition frame tick") {
		t.Fatalf("phase-shifted frame clock was not rejected: %+v", diagnostics)
	}

	plan = clonePlan(t, fixture.request.Plan)
	plan.Target.SampleClock.Origin = rational(1, 96000)
	sampleRange, err := videoedit.CompileSampleTickRange(plan.RequestedRange, plan.Target.SampleClock)
	if err != nil {
		t.Fatal(err)
	}
	plan.SampleRange = sampleRange
	plan.Fingerprint = refingerprintPlan(t, plan)
	manifest, err = renderapi.FreezeExecutionManifest(plan, fixture.request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics = fixture.backend.Preflight(context.Background(), plan, manifest)
	if !hasDiagnostic(diagnostics, CodeTimelineUnsupported) || !diagnosticsContain(diagnostics, "first composition sample tick") {
		t.Fatalf("phase-shifted sample clock was not rejected: %+v", diagnostics)
	}
}

func TestExactFrameCancellationDuringDecodedVerificationIsTerminalAndBounded(t *testing.T) {
	fixture := newBackendFixture(t, fakeSlowExactFrameDecodeScript)
	pngFile, err := os.Create(fixture.backend.executable + ".png")
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := png.Encode(pngFile, image.NewRGBA(image.Rect(0, 0, 64, 64)))
	closeErr := pngFile.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatalf("create fake exact-frame PNG: encode=%v close=%v", encodeErr, closeErr)
	}

	request := exactFrameRequest(fixture.request, 0, fileURI(filepath.Join(fixture.outputRoot, "canceled.png")))
	var events []renderapi.ProgressEvent
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = fixture.backend.RenderFrame(ctx, request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("exact-frame decoded verification cancellation took %s, want <= 2s", elapsed)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobCanceled {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, fixture.outputRoot)
}

func TestExactFrameCancellationDuringPreflightIsCanceledNotFailed(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	fixture.backend.probeResolver = ProbeResolverFunc(func(ctx context.Context, _ string) (probe.ProbeManifest, error) {
		<-ctx.Done()
		return probe.ProbeManifest{}, ctx.Err()
	})
	request := exactFrameRequest(fixture.request, 0, fileURI(filepath.Join(fixture.outputRoot, "preflight-canceled.png")))
	var events []renderapi.ProgressEvent
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := fixture.backend.RenderFrame(ctx, request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobCanceled {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, fixture.outputRoot)
}

func TestRealExactFrameMatchesExportFrameSelection(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "output")
	if err := os.Mkdir(outputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media := map[videoedit.ID]string{
		"video-a": filepath.Join(root, "video-a.mp4"),
		"video-b": filepath.Join(root, "video-b.mp4"),
		"audio":   filepath.Join(root, "audio.mov"),
	}
	makeExactFrameVideo(t, ctx, ffmpeg, "red", media["video-a"])
	makeExactFrameVideo(t, ctx, ffmpeg, "blue", media["video-b"])
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-vn", "-ac", "2", "-c:a", "pcm_s16le", "-y", media["audio"],
	)

	seed := newBackendFixture(t, fakeSuccessScript)
	plan := clonePlan(t, seed.request.Plan)
	resources := make([]renderapi.MaterializedResource, 0, len(plan.Resources))
	probeResolver := staticProbeResolver{}
	probeAdapter, err := cutlyffprobe.New(
		ctx,
		ffprobePath,
		cutlyffprobe.DefaultBuildIdentity(digestByte('7')),
		cutlyffprobe.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plan.Resources {
		planned := &plan.Resources[index]
		path := media[planned.AssetID]
		probeManifest, diagnostics, probeErr := probeAdapter.Probe(ctx, cutlyffprobe.Request{AssetID: planned.AssetID, Path: path})
		if probeErr != nil || probe.HasErrorDiagnostics(diagnostics) {
			t.Fatalf("probe %s: %v, diagnostics=%+v", planned.AssetID, probeErr, diagnostics)
		}
		planned.ContentHash = probeManifest.Resource.ContentHash
		probeFingerprint, fingerprintErr := probe.Fingerprint(probeManifest)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		probeResolver[probeFingerprint] = probeManifest
		resources = append(resources, renderapi.MaterializedResource{
			AssetID: planned.AssetID, Kind: planned.Kind,
			ContentHash: probeManifest.Resource.ContentHash, SizeBytes: probeManifest.Resource.SizeBytes,
			LocatorScheme: renderapi.LocatorFile, Locator: path, ProbeFingerprint: probeFingerprint,
		})
	}
	plan.Fingerprint = refingerprintPlan(t, plan)
	backend, err := New(ctx, Options{
		Executable: ffmpeg, OutputRoot: outputRoot, InputRoots: []string{root},
		ProbeResolver: probeResolver, SDKBuildDigest: digestByte('a'),
		RuntimeDependencyFingerprint: digestByte('f'),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	capabilities, err := backend.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digestByte('c'), EvaluatorBuild: digestByte('d'), Resources: resources,
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: backend.runtimeFingerprint, DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4", Width: 64, Height: 64,
			PixelFormat: "yuv420p", ColorSpace: "bt709", VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: 48000, Channels: 2,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(outputRoot, "export.mp4")
	exportRequest := renderapi.RenderRequest{
		Plan: plan, Manifest: manifest,
		Job: renderapi.RenderJob{
			SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "export", IdempotencyKey: "export-once",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
			ArtifactURI: fileURI(exportPath),
		},
	}
	if _, err := backend.Render(ctx, exportRequest, nil); err != nil {
		t.Fatal(err)
	}
	exportRGBA := decodeExactFrameRGBA(t, ctx, ffmpeg, exportPath)
	frameBytes := 64 * 64 * 4
	if len(exportRGBA) != int(plan.FrameRange.Count)*frameBytes {
		t.Fatalf("export RGBA bytes = %d", len(exportRGBA))
	}

	for _, test := range []struct {
		index      int64
		clip       videoedit.ID
		sourceTime videoedit.RationalTime
		red        bool
	}{
		{index: 29, clip: "video-clip-a", sourceTime: rational(29, 30), red: true},
		{index: 30, clip: "video-clip-b", sourceTime: rational(0, 1), red: false},
	} {
		t.Run(string(test.clip), func(t *testing.T) {
			pngPath := filepath.Join(outputRoot, "frame-"+string(test.clip)+".png")
			request := exactFrameRequest(exportRequest, test.index, fileURI(pngPath))
			artifact, err := backend.RenderFrame(ctx, request, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := renderapi.ValidateExactFrameArtifact(artifact, request); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(pngPath)
			if err != nil {
				t.Fatal(err)
			}
			config, decodeConfigErr := png.DecodeConfig(file)
			closeErr := file.Close()
			if decodeConfigErr != nil || closeErr != nil || config.Width != 64 || config.Height != 64 {
				t.Fatalf("PNG config=%+v decode=%v close=%v", config, decodeConfigErr, closeErr)
			}
			pngRGBA := decodeExactFrameRGBA(t, ctx, ffmpeg, pngPath)
			if len(pngRGBA) != frameBytes || artifact.DecodedContentHash != hashBytes(pngRGBA) {
				t.Fatalf("PNG decoded identity = %s bytes=%d", artifact.DecodedContentHash, len(pngRGBA))
			}
			exportedFrame := exportRGBA[int(test.index)*frameBytes : (int(test.index)+1)*frameBytes]
			if difference := maximumChannelDifference(pngRGBA, exportedFrame); difference > 8 {
				t.Fatalf("preview/export frame %d channel difference = %d", test.index, difference)
			}
			center := pngRGBA[(32*64+32)*4:]
			if test.red && center[0] <= center[2] || !test.red && center[2] <= center[0] {
				t.Fatalf("frame %d selected wrong source color: %v", test.index, center[:4])
			}
			if len(artifact.Evaluation.Video) != 1 || artifact.Evaluation.Video[0].ClipID != test.clip ||
				artifact.Evaluation.Video[0].SourceTime != test.sourceTime {
				t.Fatalf("frame %d evaluation = %+v", test.index, artifact.Evaluation)
			}

			before, err := os.ReadFile(pngPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backend.RenderFrame(ctx, request, nil); !errors.Is(err, ErrPublication) {
				t.Fatalf("no-replace error = %v", err)
			}
			after, err := os.ReadFile(pngPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("published PNG changed after rejected replacement: %v", err)
			}
		})
	}
}

func exactFrameRequest(request renderapi.RenderRequest, index int64, artifactURI string) renderapi.ExactFrameRenderRequest {
	return renderapi.ExactFrameRenderRequest{
		Plan: request.Plan, Manifest: request.Manifest,
		Frame: renderapi.ExactFrameRequest{
			SchemaVersion: renderapi.ExactFrameRequestSchemaVersion,
			ID:            "frame", IdempotencyKey: "frame-once",
			PlanFingerprint:              request.Plan.Fingerprint,
			ExecutionManifestFingerprint: request.Manifest.Fingerprint,
			CompositionFrameIndex:        index, ArtifactURI: artifactURI,
		},
	}
}

func makeExactFrameVideo(t *testing.T, ctx context.Context, ffmpeg, color, path string) {
	t.Helper()
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c="+color+":s=64x64:r=30:d=1",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-y", path,
	)
}

func decodeExactFrameRGBA(t *testing.T, ctx context.Context, ffmpeg, path string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-i", path,
		"-map", "0:v:0", "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-",
	)
	raw, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func maximumChannelDifference(left, right []byte) byte {
	var maximum byte
	for index := range left {
		difference := int(left[index]) - int(right[index])
		if difference < 0 {
			difference = -difference
		}
		if byte(difference) > maximum {
			maximum = byte(difference)
		}
	}
	return maximum
}

const fakeSlowExactFrameDecodeScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*) sleep 5; exit 0 ;;
esac
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
cat "$0.png" >&$descriptor
`
