package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/project/localv2"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/scheduler"
	"github.com/CutlyLab/Cutly/render/worker"
)

// This opt-in runner exercises a host-supplied release-stamped cutlyd and native
// worker, using a signed runtime bundle and canonical prepared fixture. It never
// manufactures runtime trust. Daemon kills do not stand in for worker/encoder
// fault coverage or prove that the supplied fixture meets mixed-stage coverage.
func TestNativeV2ProcessAcceptance(t *testing.T) {
	mode := os.Getenv("CUTLY_TEST_NATIVE_ACCEPTANCE")
	if mode == "" {
		t.Skip("set CUTLY_TEST_NATIVE_ACCEPTANCE=smoke, fault100 or soak")
	}
	if mode != "smoke" && mode != "fault100" && mode != "soak" {
		t.Fatal("invalid CUTLY_TEST_NATIVE_ACCEPTANCE mode")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("RSS process-tree measurement requires Darwin or Linux ps")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Minute)
	defer cancel()
	c, environment, executable := nativeAcceptanceFixture(t, ctx)
	var process *nativeAcceptanceProcess
	t.Cleanup(func() {
		if process != nil {
			process.stop(t)
		}
	})
	start := func() {
		process = startNativeAcceptanceProcess(t, ctx, executable, c)
	}
	start()
	canonical, err := prepared.CanonicalEnvironmentV2(ctx, environment)
	if err != nil {
		t.Fatal(err)
	}
	planned := process.actionV2(t, ctx, "plan", map[string]any{"range": environment.Plan.RequestedRange, "target": environment.Plan.Target})
	var planReceipt struct {
		Profile string            `json:"profile"`
		Plan    renderplanv2.Plan `json:"plan"`
	}
	if err := json.Unmarshal(planned.Receipt, &planReceipt); err != nil || planReceipt.Profile != "cutly.render-plan.document-v2.v2" || planReceipt.Plan.Fingerprint != environment.Plan.Fingerprint {
		t.Fatalf("native v2 plan differs from prepared fixture: %v", err)
	}
	process.actionV2(t, ctx, "prepare", map[string]any{"environment": json.RawMessage(canonical)})
	process.actionV2(t, ctx, "preflight", map[string]any{"plan_fingerprint": environment.Plan.Fingerprint, "execution_manifest_fingerprint": environment.Manifest.Fingerprint})
	preview := process.actionV2(t, ctx, "frame", map[string]any{
		"profile": nativeFramePreviewProfileV2, "plan_fingerprint": environment.Plan.Fingerprint,
		"execution_manifest_fingerprint": environment.Manifest.Fingerprint,
		"composition_frame_index":        environment.Plan.FrameRange.First,
	})
	var frameReceipt struct {
		Profile                      string `json:"profile"`
		PlanFingerprint              string `json:"plan_fingerprint"`
		ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
		CompositionFrameIndex        int64  `json:"composition_frame_index"`
		ContentHash                  string `json:"content_hash"`
		URI                          string `json:"uri"`
	}
	if err := json.Unmarshal(preview.Receipt, &frameReceipt); err != nil || frameReceipt.Profile != nativeFramePreviewProfileV2 || frameReceipt.PlanFingerprint != environment.Plan.Fingerprint || frameReceipt.ExecutionManifestFingerprint != environment.Manifest.Fingerprint || frameReceipt.CompositionFrameIndex != environment.Plan.FrameRange.First {
		t.Fatalf("native v2 frame receipt drift: %+v err=%v", frameReceipt, err)
	}
	framePath := fileURIPath(t, frameReceipt.URI)
	if fileSHA256(t, framePath) != frameReceipt.ContentHash {
		t.Fatal("native v2 frame artifact hash drift")
	}
	if err := os.Remove(framePath); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	deadline := started
	if mode == "soak" {
		deadline = started.Add(time.Hour)
	}
	iterations, faults := 1, 0
	if mode == "fault100" {
		iterations = 100
	}
	var videoRoot, audioRoot string
	var firstRSS, lastRSS, maxRSS int64
	completed := 0
	for iteration := 0; iteration < iterations || (mode == "soak" && time.Now().Before(deadline)); iteration++ {
		id := fmt.Sprintf("process-%s-%06d", mode, iteration)
		job := api.RenderJob{SchemaVersion: api.RenderJobSchemaVersion, ID: id, IdempotencyKey: id + "-once", PlanFingerprint: environment.Plan.Fingerprint, ExecutionManifestFingerprint: environment.Manifest.Fingerprint, ArtifactURI: (&url.URL{Scheme: "file", Path: filepath.Join(c.render.outputDir, id+".mp4")}).String(), TimelineRange: environment.Plan.RequestedRange, FrameRange: environment.Plan.FrameRange, SampleRange: environment.Plan.SampleRange}
		process.actionV2(t, ctx, "submit", map[string]any{"job": job})
		if mode == "fault100" {
			// Kill only after a real durable running state is observed. A completed
			// job before this boundary is a missed fault, never counted as a kill.
			process.wait(t, ctx, job.ID, api.JobRunning)
			process.stop(t)
			process = nil
			faults++
			start()
		}
		snapshot := process.wait(t, ctx, job.ID, api.JobComplete)
		if snapshot.Artifact == nil {
			t.Fatal("completed job has no artifact")
		}
		artifact := *snapshot.Artifact
		expectedEvidence, err := api.EvidenceSidecarURI(job.ArtifactURI)
		if err != nil || artifact.URI != job.ArtifactURI || artifact.EvidenceURI != expectedEvidence {
			t.Fatal("artifact publication location drift", err)
		}
		if artifact.FrameCount != job.FrameRange.Count || artifact.SampleCount != job.SampleRange.Count || artifact.ExecutionManifestFingerprint != environment.Manifest.Fingerprint {
			t.Fatalf("artifact coverage/identity drift: %+v", artifact)
		}
		mediaPath := fileURIPath(t, artifact.URI)
		info, err := os.Stat(mediaPath)
		if err != nil || info.Size() != artifact.SizeBytes || fileSHA256(t, mediaPath) != artifact.ContentHash {
			t.Fatalf("artifact bytes drift: %v", err)
		}
		file, err := os.Open(fileURIPath(t, artifact.EvidenceURI))
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (16<<20)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(raw) > 16<<20 {
			t.Fatalf("bounded evidence read: read=%v close=%v", readErr, closeErr)
		}
		manifest, err := evidence.DecodeManifestV2(raw)
		if err != nil || manifest.Fingerprint != artifact.EvidenceFingerprint || manifest.Assembly.JobID != job.ID || manifest.Assembly.ExecutionManifestFingerprint != environment.Manifest.Fingerprint || manifest.Artifact.ContentHash != artifact.ContentHash || manifest.Artifact.SizeBytes != artifact.SizeBytes {
			t.Fatalf("evidence identity drift: %v", err)
		}
		if manifest.DecodedVideo == nil || manifest.DecodedAudio == nil || manifest.DecodedVideo.Range != job.FrameRange || manifest.DecodedAudio.Range != job.SampleRange {
			t.Fatal("decoded evidence coverage drift")
		}
		if completed == 0 {
			videoRoot, audioRoot = manifest.DecodedVideo.RootHash, manifest.DecodedAudio.RootHash
		} else if videoRoot != manifest.DecodedVideo.RootHash || audioRoot != manifest.DecodedAudio.RootHash {
			t.Fatal("repeated decoded roots changed")
		}
		completed++
		rss := nativeAcceptanceRSS(t, ctx, process.command.Process.Pid)
		lastRSS = rss
		if completed == 1 {
			firstRSS = rss
		}
		maxRSS = max(maxRSS, rss)
		t.Logf("native_acceptance timestamp=%s mode=%s completed=%d daemon_kills=%d elapsed=%s tree_rss_kib=%d peak_sample_kib=%d video_root=%s audio_root=%s", time.Now().UTC().Format(time.RFC3339Nano), mode, completed, faults, time.Since(started), rss, maxRSS, videoRoot, audioRoot)
		// These are this iteration's verified outputs in a private test root;
		// removing them keeps the one-hour runner's disk use independent of jobs.
		for _, path := range []string{mediaPath, fileURIPath(t, artifact.EvidenceURI)} {
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if mode == "fault100" && faults != 100 {
		t.Fatal("did not execute 100 actual daemon kills")
	}
	if mode == "soak" && time.Since(started) < time.Hour {
		t.Fatal("soak did not run a full wall-clock hour")
	}
	if mode == "soak" && lastRSS > firstRSS+128*1024 {
		t.Fatalf("retained process-tree RSS grew over 128MiB: first=%d last=%d KiB", firstRSS, lastRSS)
	}
	t.Logf("native_acceptance COMPLETE started=%s ended=%s completed=%d daemon_kills=%d first_rss_kib=%d last_rss_kib=%d peak_sample_kib=%d fixture=host-prepared runtime=signed-release-bundle", started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), completed, faults, firstRSS, lastRSS, maxRSS)
}

// nativeAcceptanceFixture consumes host inputs without rebuilding trust roots or
// stamping a locally discovered executable into a purported release runtime.
func nativeAcceptanceFixture(t *testing.T, ctx context.Context) (config, prepared.EnvironmentV2, string) {
	t.Helper()
	names := []string{"CUTLY_TEST_NATIVE_DAEMON", "CUTLY_TEST_NATIVE_WORKER", "CUTLY_TEST_NATIVE_RUNTIME_BUNDLE", "CUTLY_TEST_NATIVE_ENVIRONMENT", "CUTLY_TEST_NATIVE_HOST_CONFIG", "CUTLY_TEST_NATIVE_SDK_DIGEST"}
	values := make(map[string]string, len(names))
	for _, name := range names {
		value := os.Getenv(name)
		if value == "" {
			t.Skipf("host-supplied verified acceptance input required: %s", name)
		}
		values[name] = value
	}
	for _, name := range names[:5] {
		if !filepath.IsAbs(values[name]) || filepath.Clean(values[name]) != values[name] {
			t.Fatalf("%s must be an absolute clean host-selected path", name)
		}
	}
	file, err := os.Open(values["CUTLY_TEST_NATIVE_ENVIRONMENT"])
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 16<<20 {
		t.Fatalf("bounded prepared input: read=%v close=%v", readErr, closeErr)
	}
	environment, err := prepared.DecodeEnvironmentV2(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	hostFile, err := worker.OpenLaunchConfigV2(values["CUTLY_TEST_NATIVE_HOST_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	host, hostErr := worker.ReadLaunchConfigV2(hostFile)
	closeErr = hostFile.Close()
	if hostErr != nil || closeErr != nil {
		t.Fatalf("host configuration: read=%v close=%v", hostErr, closeErr)
	}
	if host.MediaRuntime != environment.Manifest.MediaRuntime || host.CompilerBuild != environment.Manifest.CompilerBuild || host.EvaluatorBuild != environment.Manifest.EvaluatorBuild {
		t.Fatal("host policy and prepared fixture differ")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := config{projectID: "project", projectDir: filepath.Join(root, "project"), render: renderConfig{enabled: true, stateDir: filepath.Join(root, "state"), outputDir: filepath.Join(root, "output"), inputRoots: stringList{filepath.Join(root, "input")}, mediaRuntimeBundleRoot: values["CUTLY_TEST_NATIVE_RUNTIME_BUNDLE"], sdkBuildDigest: values["CUTLY_TEST_NATIVE_SDK_DIGEST"], compilerBuild: host.CompilerBuild, evaluatorBuild: host.EvaluatorBuild, stageMemoryBytes: 512 << 20, stageDiskBytes: 4 << 30}, nativeV2: nativeConfigV2{enabled: true, executable: values["CUTLY_TEST_NATIVE_WORKER"], hostConfig: values["CUTLY_TEST_NATIVE_HOST_CONFIG"]}}
	for _, directory := range []string{c.projectDir, c.render.stateDir, c.render.outputDir, c.render.inputRoots[0]} {
		if err = os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = localv2.Create(ctx, c.projectDir, environment.Document, localv2.Options{}); err != nil {
		t.Fatal(err)
	}
	return c, environment, values["CUTLY_TEST_NATIVE_DAEMON"]
}

type nativeAcceptanceProcess struct {
	command *exec.Cmd
	log     *os.File
	baseURL string
	serial  int
}

func startNativeAcceptanceProcess(t *testing.T, ctx context.Context, executable string, c config) *nativeAcceptanceProcess {
	t.Helper()
	// A supplied release executable may live in an immutable deployment root.
	// All runner writes stay in the private test root, never beside that binary.
	log, err := os.CreateTemp(filepath.Dir(c.projectDir), "daemon-log-*")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--listen=127.0.0.1:0", "--project-id=" + c.projectID, "--project-dir=" + c.projectDir,
		"--render-state-dir=" + c.render.stateDir, "--render-output-dir=" + c.render.outputDir,
		"--render-input-root=" + c.render.inputRoots[0],
		"--media-runtime-bundle-root=" + c.render.mediaRuntimeBundleRoot,
		"--sdk-build-digest=" + c.render.sdkBuildDigest, "--compiler-build=" + c.render.compilerBuild,
		"--evaluator-build=" + c.render.evaluatorBuild,
		fmt.Sprintf("--render-stage-memory-bytes=%d", c.render.stageMemoryBytes),
		fmt.Sprintf("--render-stage-disk-bytes=%d", c.render.stageDiskBytes),
		"--render-worker-poll-interval=10ms", "--render-native-v2",
		"--render-native-v2-worker=" + c.nativeV2.executable, "--render-native-v2-host-config=" + c.nativeV2.hostConfig,
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	process := &nativeAcceptanceProcess{command: command, log: log}
	ready := false
	defer func() {
		if !ready {
			process.stop(t)
		}
	}()
	pattern := regexp.MustCompile(`listening on (http://127\.0\.0\.1:[0-9]+)/v1/agent`)
	until := time.Now().Add(20 * time.Second)
	for time.Now().Before(until) {
		raw, readErr := os.ReadFile(log.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if match := pattern.FindSubmatch(raw); match != nil {
			process.baseURL = string(match[1])
			ready = true
			return process
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	raw, _ := os.ReadFile(log.Name())
	t.Fatalf("daemon not ready: %s", raw)
	return nil
}

func (p *nativeAcceptanceProcess) stop(t *testing.T) {
	t.Helper()
	_ = p.command.Process.Kill()
	_ = p.command.Wait()
	_ = p.log.Close()
}

func (p *nativeAcceptanceProcess) actionV2(t *testing.T, ctx context.Context, action string, payload any) protocolv2.Result {
	t.Helper()
	p.serial++
	payloadRaw, err := videoedit.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolv2.Request{
		SchemaVersion: protocolv2.SchemaVersion, Profile: protocolv2.RequestProfile,
		RequestID: fmt.Sprintf("process-v2-%d-%d", p.command.Process.Pid, p.serial),
		Tool:      protocolv2.ToolRender, Action: action, ProjectID: "project",
		PayloadProfile: protocolv2.PayloadProfile(protocolv2.ToolRender, action), Payload: payloadRaw,
	}
	raw, err := protocolv2.CanonicalRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.baseURL+"/v2/agent", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Content-Type", protocolv2.MediaType)
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, protocolv2.MaxResultBytes+1))
	if err != nil || len(raw) > protocolv2.MaxResultBytes || response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %v: %s", response.StatusCode, err, raw)
	}
	result, err := protocolv2.DecodeResult(callCtx, raw)
	if err != nil || !result.OK {
		t.Fatalf("v2 action %s: %v: %s", action, err, raw)
	}
	return result
}

func (p *nativeAcceptanceProcess) wait(t *testing.T, ctx context.Context, jobID string, wanted api.JobState) scheduler.JobSnapshot {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		result := p.actionV2(t, waitCtx, "status", map[string]any{"job_id": jobID})
		var snapshot scheduler.JobSnapshot
		if err := json.Unmarshal(result.Receipt, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.State == wanted {
			return snapshot
		}
		if snapshot.State == api.JobComplete || snapshot.State == api.JobFailed || snapshot.State == api.JobCanceled {
			t.Fatalf("wanted %s, reached %s: %+v", wanted, snapshot.State, snapshot)
		}
		select {
		case <-waitCtx.Done():
			t.Fatal(waitCtx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func nativeAcceptanceRSS(t *testing.T, ctx context.Context, pid int) int64 {
	t.Helper()
	command := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=")
	raw, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	type process struct {
		pid, parent int
		rss         int64
	}
	var processes []process
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		id, a := strconv.Atoi(fields[0])
		parent, b := strconv.Atoi(fields[1])
		rss, c := strconv.ParseInt(fields[2], 10, 64)
		if a != nil || b != nil || c != nil || rss < 0 {
			t.Fatal("invalid ps row", line)
		}
		processes = append(processes, process{id, parent, rss})
	}
	selected := map[int]bool{pid: true}
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			if selected[p.parent] && !selected[p.pid] {
				selected[p.pid], changed = true, true
			}
		}
	}
	var total int64
	found := false
	for _, p := range processes {
		if selected[p.pid] {
			total += p.rss
			found = found || p.pid == pid
		}
	}
	if !found {
		t.Fatal("daemon absent from RSS process table")
	}
	return total
}
