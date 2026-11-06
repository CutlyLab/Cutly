package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/renderprepare"
	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	renderevidence "github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/scheduler"
	schedulerlocal "github.com/CutlyLab/Cutly/render/scheduler/local"
)

func TestRenderApplicationEndToEndAndOfflinePublicationRecovery(t *testing.T) {
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobePath, ffprobeErr := exec.LookPath("ffprobe")
	if ffmpegErr != nil || ffprobeErr != nil {
		t.Skip("FFmpeg and FFprobe are required for the production render acceptance test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration := renderAcceptanceConfig(t, root, ffmpeg, ffprobePath)
	videoPath := filepath.Join(configuration.render.inputRoots[0], "video.mp4")
	audioPath := filepath.Join(configuration.render.inputRoots[0], "audio.mov")
	generateAcceptanceMedia(t, ctx, ffmpeg, videoPath, audioPath)

	prober, err := cutlyffprobe.New(ctx, ffprobePath,
		cutlyffprobe.DefaultBuildIdentity(configuration.render.ffprobeRuntimeDependencyFingerprint), cutlyffprobe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	probes := make(map[videoedit.ID]probe.ProbeManifest, 2)
	for _, input := range []struct {
		id   videoedit.ID
		path string
	}{{"video", videoPath}, {"audio", audioPath}} {
		manifest, diagnostics, probeErr := prober.Probe(ctx, cutlyffprobe.Request{AssetID: input.id, Path: input.path})
		if probeErr != nil || probe.HasErrorDiagnostics(diagnostics) {
			t.Fatalf("probe %s: %v diagnostics=%+v", input.id, probeErr, diagnostics)
		}
		probes[input.id] = manifest
	}
	plan := acceptancePlan(t, probes)
	paths := map[videoedit.ID]string{"video": videoPath, "audio": audioPath}

	application, err := buildApplication(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	openApplication := application
	t.Cleanup(func() { _ = openApplication.Close() })
	bindings := make([]map[string]any, 0, len(plan.Resources))
	for _, resource := range plan.Resources {
		bindings = append(bindings, map[string]any{"asset_id": resource.AssetID, "locator": paths[resource.AssetID]})
	}
	prepared := callRenderAction(t, ctx, application, "prepare", "e2e-prepare", map[string]any{
		"plan": plan, "resources": bindings, "deterministic_seed": int64(1),
	})
	if !prepared.OK || prepared.State != "prepared" || len(prepared.NextActions) != 1 ||
		prepared.NextActions[0].Tool != agentprotocol.ToolRender || prepared.NextActions[0].Action != "preflight" {
		t.Fatalf("prepare = %+v", prepared)
	}
	var preparedReceipt renderprepare.Receipt
	if err := json.Unmarshal(prepared.Receipt, &preparedReceipt); err != nil {
		t.Fatalf("decode prepare receipt: %v", err)
	}
	manifest := preparedReceipt.ExecutionManifest
	if preparedReceipt.Profile != renderprepare.ReceiptProfile || manifest.PlanFingerprint != plan.Fingerprint ||
		manifest.DeterministicSeed != 1 || len(manifest.Resources) != len(plan.Resources) {
		t.Fatalf("prepare receipt = %+v", preparedReceipt)
	}
	preflight := callRenderAction(t, ctx, application, "preflight", "e2e-preflight", map[string]any{
		"plan": plan, "execution_manifest": manifest,
	})
	if !preflight.OK || preflight.State != "ready" {
		t.Fatalf("preflight = %+v", preflight)
	}

	firstJob := acceptanceJob(plan, manifest, "e2e-first", "first.mp4", configuration.render.outputDir)
	queued := callRenderAction(t, ctx, application, "render", "e2e-submit-first", map[string]any{"job": firstJob})
	if !queued.OK {
		t.Fatalf("submit = %+v", queued)
	}
	waitForRenderState(t, ctx, application, firstJob.ID, renderapi.JobComplete)
	assertPublishedEvidence(t, firstJob, manifest.Fingerprint)
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	openApplication = nil

	// Place a prepared job directly into the durable journal while no worker is
	// running. Reopening cutlyd must replay and drain it without a new Agent
	// submission or Notify signal.
	replayJob := acceptanceJob(plan, manifest, "e2e-replay", "replay.mp4", configuration.render.outputDir)
	journal, err := schedulerlocal.OpenJournal(filepath.Join(configuration.render.stateDir, "scheduler.journal"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := scheduler.Open(ctx, journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if _, created, err := coordinator.Submit(ctx, replayJob); err != nil || !created {
		_ = journal.Close()
		t.Fatalf("seed replay job: created=%v error=%v", created, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	replayApplication, err := buildApplication(ctx, configuration)
	if err != nil {
		t.Fatalf("restart for journal replay: %v", err)
	}
	application = replayApplication
	openApplication = replayApplication
	waitForRenderState(t, ctx, replayApplication, replayJob.ID, renderapi.JobComplete)
	assertPublishedEvidence(t, replayJob, manifest.Fingerprint)

	// Publish a second output without enqueueing it. This reproduces the crash
	// window after the backend's durable publication boundary and before the
	// scheduler checkpoint/complete events are committed.
	recoveryJob := acceptanceJob(plan, manifest, "e2e-recovery", "recovery.mp4", configuration.render.outputDir)
	if _, err := application.render.backend.Render(ctx, renderapi.RenderRequest{
		Job: recoveryJob, Plan: plan, Manifest: manifest,
	}, nil); err != nil {
		t.Fatalf("seed published recovery artifact: %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	openApplication = nil
	for _, path := range []string{videoPath, audioPath} {
		if err := os.Rename(path, path+".offline"); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := buildApplication(ctx, configuration)
	if err != nil {
		t.Fatalf("restart with offline sources: %v", err)
	}
	openApplication = reopened
	checkpointRoot := filepath.Join(configuration.render.stateDir, "checkpoints", "checkpoints")
	checkpointsBeforeRecovery := countRegularFiles(t, checkpointRoot)
	recoverySubmit := callRenderAction(t, ctx, reopened, "render", "e2e-submit-recovery", map[string]any{"job": recoveryJob})
	if !recoverySubmit.OK {
		t.Fatalf("recovery submit = %+v", recoverySubmit)
	}
	waitForRenderState(t, ctx, reopened, recoveryJob.ID, renderapi.JobComplete)
	assertPublishedEvidence(t, recoveryJob, manifest.Fingerprint)
	if countRegularFiles(t, filepath.Join(configuration.render.stateDir, "cas", "entries")) == 0 {
		t.Fatal("render completed without a CAS artifact")
	}
	if checkpointsAfterRecovery := countRegularFiles(t, checkpointRoot); checkpointsAfterRecovery <= checkpointsBeforeRecovery {
		t.Fatalf("offline recovery did not add its durable checkpoint: before=%d after=%d", checkpointsBeforeRecovery, checkpointsAfterRecovery)
	}
}

func renderAcceptanceConfig(t *testing.T, root, ffmpeg, ffprobe string) config {
	t.Helper()
	configuration := config{
		projectID: "project", projectDir: filepath.Join(root, "project"),
		render: renderConfig{
			enabled: true, stateDir: filepath.Join(root, "state"), outputDir: filepath.Join(root, "output"),
			inputRoots: stringList{filepath.Join(root, "input")}, ffmpegExecutable: ffmpeg, ffprobeExecutable: ffprobe,
			sdkBuildDigest: testSHA256('a'), compilerBuild: testSHA256('b'), evaluatorBuild: testSHA256('c'),
			ffmpegRuntimeDependencyFingerprint: testSHA256('d'), ffprobeRuntimeDependencyFingerprint: testSHA256('e'),
			stageMemoryBytes: 64 << 20, stageDiskBytes: 256 << 20, maxConcurrentJobs: 1,
			workerPollInterval: 10 * time.Millisecond,
		},
	}
	for _, directory := range []string{
		configuration.projectDir, configuration.render.stateDir, configuration.render.outputDir, configuration.render.inputRoots[0],
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return configuration
}

func generateAcceptanceMedia(t *testing.T, ctx context.Context, ffmpeg, videoPath, audioPath string) {
	t.Helper()
	runAcceptanceCommand(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:r=30:d=1",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-y", videoPath,
	)
	runAcceptanceCommand(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-vn", "-ac", "2", "-c:a", "pcm_s16le", "-y", audioPath,
	)
}

func runAcceptanceCommand(t *testing.T, ctx context.Context, executable string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, executable, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v: %s", filepath.Base(executable), err, output)
	}
}

func acceptancePlan(t *testing.T, probes map[videoedit.ID]probe.ProbeManifest) videoedit.RenderPlan {
	t.Helper()
	duration := videoedit.RationalTime{Value: 1, Rate: 1}
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{
			{ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.mov", ContentHash: probes["audio"].Resource.ContentHash, Duration: &duration},
			{ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4", ContentHash: probes["video"].Resource.ContentHash, Duration: &duration},
		},
		Timelines: []videoedit.Timeline{{
			ID: "timeline", Tracks: []videoedit.Track{
				{ID: "audio-track", Kind: videoedit.TrackKindAudio, Clips: []videoedit.Clip{{
					ID: "audio-clip", AssetID: "audio", TimelineRange: acceptanceRange(0, 1), SourceRange: acceptanceRange(0, 1),
				}}},
				{ID: "video-track", Kind: videoedit.TrackKindVideo, Clips: []videoedit.Clip{{
					ID: "video-clip", AssetID: "video", TimelineRange: acceptanceRange(0, 1), SourceRange: acceptanceRange(0, 1),
				}}},
			},
		}},
	}
	plan, diagnostics := videoedit.Resolve(document, acceptanceRange(0, 1), videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock: videoedit.FrameClock{
			Origin: videoedit.RationalTime{Value: 0, Rate: 1}, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1},
		},
		SampleClock: videoedit.SampleClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	return plan
}

func acceptanceRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{
		Start:    videoedit.RationalTime{Value: start, Rate: 1},
		Duration: videoedit.RationalTime{Value: duration, Rate: 1},
	}
}

func acceptanceJob(plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest, id, name, outputRoot string) renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion, ID: id, IdempotencyKey: id + "-once",
		PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
		TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
		ArtifactURI: (&url.URL{Scheme: "file", Path: filepath.Join(outputRoot, name)}).String(),
	}
}

func callRenderAction(t *testing.T, ctx context.Context, application *application, action, requestID string, payload any) agentprotocol.Result {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.handler.HandleAgent(ctx, agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: requestID,
		Tool: agentprotocol.ToolRender, Action: action, ProjectID: "project", Payload: raw,
	})
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return result
}

func waitForRenderState(t *testing.T, ctx context.Context, application *application, jobID string, wanted renderapi.JobState) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for attempt := 0; ; attempt++ {
		result := callRenderAction(t, ctx, application, "status", fmt.Sprintf("status-%s-%d", jobID, attempt), map[string]any{"job_id": jobID})
		if result.State == string(wanted) {
			if !result.OK {
				t.Fatalf("terminal status = %+v", result)
			}
			return
		}
		if result.State == string(renderapi.JobFailed) || result.State == string(renderapi.JobCanceled) {
			t.Fatalf("render reached %s: %+v", result.State, result)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v", jobID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertPublishedEvidence(t *testing.T, job renderapi.RenderJob, manifestFingerprint string) {
	t.Helper()
	mediaPath := fileURIPath(t, job.ArtifactURI)
	media, err := os.Stat(mediaPath)
	if err != nil || media.Size() <= 0 {
		t.Fatalf("published media: info=%+v error=%v", media, err)
	}
	evidenceURI, err := renderapi.EvidenceSidecarURI(job.ArtifactURI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fileURIPath(t, evidenceURI))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := renderevidence.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Job.ID != job.ID || evidence.ExecutionManifestFingerprint != manifestFingerprint ||
		evidence.Artifact.URI != job.ArtifactURI || evidence.FrameCount != job.FrameRange.Count ||
		evidence.SampleCount != job.SampleRange.Count {
		t.Fatalf("evidence binding = %+v", evidence)
	}
}

func fileURIPath(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" {
		t.Fatalf("file URI %q: %v", raw, err)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func countRegularFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
