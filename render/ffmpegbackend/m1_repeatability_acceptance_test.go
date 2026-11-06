package ffmpegbackend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	renderevidence "github.com/CutlyLab/Cutly/render/evidence"
)

func TestM1ReferenceProfileTenRunDecodedRepeatability(t *testing.T) {
	if !platformSupportsSecureDescriptors() {
		t.Skip("M1 secure render profile is unavailable on this platform")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	generateM1RepeatabilityMedia(t, ctx, ffmpeg, media)

	seed := newBackendFixture(t, fakeSuccessScript)
	plan := clonePlan(t, seed.request.Plan)
	resources := make([]renderapi.MaterializedResource, 0, len(plan.Resources))
	probeResolver := staticProbeResolver{}
	probeAdapter, err := cutlyffprobe.New(
		ctx,
		ffprobePath,
		cutlyffprobe.DefaultBuildIdentity(digestByte('8')),
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
			SampleRate: 48000, Channels: 2, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := renderapi.RenderRequest{
		Plan: plan, Manifest: manifest,
		Job: renderapi.RenderJob{
			SchemaVersion:   renderapi.RenderJobSchemaVersion,
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
		},
	}

	var wantVideoRoot, wantAudioRoot string
	for run := 0; run < 10; run++ {
		current := request
		current.Job.ID = fmt.Sprintf("repeatability-%02d", run)
		current.Job.IdempotencyKey = fmt.Sprintf("repeatability-%02d-once", run)
		current.Job.ArtifactURI = fileURI(filepath.Join(backend.outputRoot, fmt.Sprintf("repeatability-%02d.mp4", run)))
		artifact, renderErr := backend.Render(ctx, current, nil)
		if renderErr != nil {
			t.Fatalf("render %d: %v", run, renderErr)
		}
		manifest := readEvidenceArtifact(t, artifact)
		if err := renderevidence.ValidateAgainst(manifest, current, &artifact); err != nil {
			t.Fatalf("evidence %d: %v", run, err)
		}
		if manifest.DecodedVideo == nil || manifest.DecodedAudio == nil {
			t.Fatalf("render %d has incomplete decoded evidence: %+v", run, manifest)
		}
		if run == 0 {
			wantVideoRoot = manifest.DecodedVideo.RootHash
			wantAudioRoot = manifest.DecodedAudio.RootHash
			continue
		}
		if manifest.DecodedVideo.RootHash != wantVideoRoot || manifest.DecodedAudio.RootHash != wantAudioRoot {
			t.Fatalf(
				"run %d decoded roots drifted: video=%s/%s audio=%s/%s",
				run, manifest.DecodedVideo.RootHash, wantVideoRoot, manifest.DecodedAudio.RootHash, wantAudioRoot,
			)
		}
	}
	assertNoM1TemporaryNames(t, outputRoot)
}

func generateM1RepeatabilityMedia(t *testing.T, ctx context.Context, ffmpeg string, media map[videoedit.ID]string) {
	t.Helper()
	for _, value := range []struct {
		path  string
		color string
	}{
		{path: media["video-a"], color: "red"},
		{path: media["video-b"], color: "blue"},
	} {
		runExternal(t, ctx, ffmpeg,
			"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c="+value.color+":s=64x64:r=30:d=2",
			"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
			"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited",
			"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
			"-y", value.path,
		)
	}
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-vn", "-ac", "2", "-c:a", "pcm_s16le", "-y", media["audio"],
	)
}

func assertNoM1TemporaryNames(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".cutly-") && entry.Name()[:len(".cutly-")] == ".cutly-" {
			t.Fatalf("temporary render state remains after repeatability gate: %s", entry.Name())
		}
	}
}
