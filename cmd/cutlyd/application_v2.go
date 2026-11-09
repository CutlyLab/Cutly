package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/renderqueue"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/backendstage"
	checkpointlocal "github.com/CutlyLab/Cutly/render/checkpoint/local"
	"github.com/CutlyLab/Cutly/render/chunkcachev2"
	"github.com/CutlyLab/Cutly/render/ffmpegbackend"
	"github.com/CutlyLab/Cutly/render/prepared"
	renderruntime "github.com/CutlyLab/Cutly/render/runtime"
	"github.com/CutlyLab/Cutly/render/scheduler"
	schedulerlocal "github.com/CutlyLab/Cutly/render/scheduler/local"
	"github.com/CutlyLab/Cutly/render/stagedag"
	"github.com/CutlyLab/Cutly/render/worker"
)

type nativeConfigV2 struct {
	enabled                                                        bool
	executable, hostConfig                                         string
	supervisorExecutable, supervisorDigest, supervisorDependencies string
}

func registerNativeFlagsV2(f *flag.FlagSet, c *nativeConfigV2) {
	f.BoolVar(&c.enabled, "render-native-v2", false, "opt into original DocumentV2 native worker/CAS/assembler runtime")
	f.StringVar(&c.executable, "render-native-v2-worker", "", "host-owned cutly-worker-v2 executable")
	f.StringVar(&c.hostConfig, "render-native-v2-host-config", "", "private host-owned native worker resource/runtime configuration")
	f.StringVar(&c.supervisorExecutable, "render-native-v2-supervisor", "", "optional host-owned native FFmpeg liveness supervisor")
	f.StringVar(&c.supervisorDigest, "render-native-v2-supervisor-digest", "", "measured SHA-256 supervisor executable identity")
	f.StringVar(&c.supervisorDependencies, "render-native-v2-supervisor-dependency-fingerprint", "", "independent pinned supervisor dependency closure")
}
func validateNativeFlagsV2(c config) error {
	n := c.nativeV2
	if !n.enabled {
		if n.executable != "" || n.hostConfig != "" || n.supervisorExecutable != "" || n.supervisorDigest != "" || n.supervisorDependencies != "" {
			return errors.New("native V2 worker/config requires render-native-v2")
		}
		return nil
	}
	if !c.render.enabled {
		return errors.New("render-native-v2 requires explicit durable render configuration")
	}
	for _, p := range []string{n.executable, n.hostConfig} {
		if err := validateCleanAbsolutePath(p); err != nil {
			return fmt.Errorf("native V2 host path: %w", err)
		}
	}
	if c.render.mediaRuntimeBundleRoot == "" || c.render.unsafeAllowUnverifiedMediaRuntime {
		return errors.New("native V2 requires a verified signed media-runtime-bundle-root")
	}
	if n.supervisorExecutable != "" || n.supervisorDigest != "" || n.supervisorDependencies != "" {
		if err := validateCleanAbsolutePath(n.supervisorExecutable); err != nil {
			return err
		}
		if _, err := ffmpegbackend.ChunkAssemblerSupervisedCapabilitiesV2(c.render.sdkBuildDigest, n.supervisorDigest, n.supervisorDependencies); err != nil {
			return err
		}
	}
	if c.render.stageDiskBytes > math.MaxInt64 {
		return errors.New("native V2 disk budget exceeds int64")
	}
	return nil
}

func nativeCapabilitiesV2(c config) (api.BackendCapabilitiesV2, error) {
	n := c.nativeV2
	if n.supervisorExecutable != "" || n.supervisorDigest != "" || n.supervisorDependencies != "" {
		return ffmpegbackend.ChunkAssemblerSupervisedCapabilitiesV2(c.render.sdkBuildDigest, n.supervisorDigest, n.supervisorDependencies)
	}
	return ffmpegbackend.ChunkAssemblerCapabilitiesV2(c.render.sdkBuildDigest)
}

type nativeApplicationV2 struct {
	launch     *os.File
	mu         sync.Mutex
	assemblers map[prepared.KeyV2]*backendstage.FFmpegChunkAssemblerV2
	closed     bool
}

func (n *nativeApplicationV2) Close() error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	var err error
	for _, a := range n.assemblers {
		err = errors.Join(err, a.Close())
	}
	if n.launch != nil {
		err = errors.Join(err, n.launch.Close())
	}
	return err
}

func buildRenderApplicationV2(ctx context.Context, c config) (_ *renderApplication, returnErr error) {
	if err := validateNativeFlagsV2(c); err != nil {
		return nil, err
	}
	r := c.render
	for _, p := range append([]string{r.stateDir, r.outputDir}, []string(r.inputRoots)...) {
		if err := requireRealDirectory(p); err != nil {
			return nil, err
		}
	}
	if err := validateRenderRootSeparation(r.stateDir, r.outputDir, r.inputRoots); err != nil {
		return nil, err
	}
	runtime, err := resolveMediaRuntime(ctx, r)
	if err != nil {
		return nil, err
	}
	owner, err := acquireRenderStateOwner(r.stateDir)
	if err != nil {
		return nil, err
	}
	native := &nativeApplicationV2{assemblers: make(map[prepared.KeyV2]*backendstage.FFmpegChunkAssemblerV2)}
	result := &renderApplication{owner: owner, nativeV2: native}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, result.Close())
		}
	}()
	// Native durable namespaces are separate. Binding still prevents cross-project reuse.
	if err = bindRenderStateProject(ctx, r.stateDir, c.projectID, c.projectDir, r.outputDir); err != nil {
		return nil, err
	}
	native.launch, err = worker.OpenLaunchConfigV2(c.nativeV2.hostConfig)
	if err != nil {
		return nil, err
	}
	host, err := worker.ReadLaunchConfigV2(native.launch)
	if err != nil {
		return nil, err
	}
	offered, err := nativeCapabilitiesV2(c)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(host.Backend, offered) || host.CompilerBuild != r.compilerBuild || host.EvaluatorBuild != r.evaluatorBuild {
		return nil, errors.New("native V2 host authority differs from daemon builds/fixed backend offer")
	}
	measured, err := ffmpegbackend.ChunkAssemblerRuntimeFingerprintV2(ctx, runtime.ffmpegExecutable, runtime.ffmpegRuntimeDependencyFingerprint)
	if err != nil {
		return nil, err
	}
	if host.MediaRuntime.FFmpegBuild != measured {
		return nil, errors.New("native V2 host authority differs from measured media runtime")
	}
	spoolRoot := filepath.Join(r.stateDir, "worker-spool-v2")
	if err = os.Mkdir(spoolRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	maxChunkBytes := int64(r.stageDiskBytes / 3)
	if maxChunkBytes > worker.MaxStreamPayloadBytesV2 {
		maxChunkBytes = worker.MaxStreamPayloadBytesV2
	}
	runner, err := worker.NewRunnerV2(worker.ConfigV2{Process: worker.Config{Executable: c.nativeV2.executable, WorkingDirectory: filepath.Dir(c.nativeV2.executable), Timeout: 10 * time.Minute, TerminationGrace: worker.MaxTerminationGraceV2}, LaunchConfig: native.launch, SpoolDirectory: spoolRoot, MaxPayloadBytes: maxChunkBytes})
	if err != nil {
		return nil, err
	}
	environments, err := prepared.New(filepath.Join(r.stateDir, "prepared-v2"), prepared.Options{})
	if err != nil {
		return nil, err
	}
	artifacts, err := cas.New(filepath.Join(r.stateDir, "cas-v2"), cas.Options{})
	if err != nil {
		return nil, err
	}
	chunkRoot := filepath.Join(r.stateDir, "chunks-v2")
	if err = os.Mkdir(chunkRoot, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	chunks, err := chunkcachev2.New(chunkRoot, artifacts, chunkcachev2.Options{MaxPayloadBytes: maxChunkBytes, MaxEntries: stagedag.MaxTasks})
	if err != nil {
		return nil, err
	}
	checkpoints, err := checkpointlocal.New(filepath.Join(r.stateDir, "checkpoints-v2"), artifacts, checkpointlocal.Options{})
	if err != nil {
		return nil, err
	}
	journal, err := schedulerlocal.OpenJournal(filepath.Join(r.stateDir, "scheduler-v2.journal"))
	if err != nil {
		return nil, err
	}
	result.journal = journal
	coordinator, err := scheduler.Open(ctx, journal)
	if err != nil {
		return nil, err
	}
	service, err := backendstage.NewV2(backendstage.ConfigV2{Environments: environments, Worker: runner, Chunks: chunks, CAS: artifacts, MemoryBytes: r.stageMemoryBytes, DiskBytes: r.stageDiskBytes, Assemblers: backendstage.AssemblerResolverFuncV2(func(ctx context.Context, v prepared.VerifiedEnvironmentV2) (backendstage.ChunkAssemblerBackendV2, error) {
		e, err := v.Environment(ctx)
		if err != nil {
			return nil, err
		}
		if err := worker.AuthorizeLaunchEnvironmentV2(ctx, host, c.nativeV2.executable, e); err != nil {
			return nil, err
		}
		if e.Manifest.MediaRuntime != host.MediaRuntime {
			return nil, errors.New("native manifest runtime not authorized by host")
		}
		native.mu.Lock()
		defer native.mu.Unlock()
		if native.closed {
			return nil, os.ErrClosed
		}
		if a := native.assemblers[v.Key()]; a != nil {
			return a, nil
		}
		if len(native.assemblers) >= 64 {
			return nil, errors.New("native assembler admission: maximum64 prepared environments per daemon")
		}
		a, err := backendstage.NewFFmpegChunkAssemblerV2(ctx, ffmpegbackend.ChunkAssemblerOptionsV2{Executable: runtime.ffmpegExecutable, OutputRoot: r.outputDir, SDKBuildDigest: r.sdkBuildDigest, WorkerExecutableDigest: runner.ExecutableDigest(), RuntimeDependencyFingerprint: runtime.ffmpegRuntimeDependencyFingerprint, MaxStagingBytes: int64(r.stageDiskBytes / 3), SupervisorExecutable: c.nativeV2.supervisorExecutable, SupervisorExecutableDigest: c.nativeV2.supervisorDigest, SupervisorDependencyFingerprint: c.nativeV2.supervisorDependencies, Plan: e.Plan, Manifest: e.Manifest})
		if err != nil {
			return nil, err
		}
		native.assemblers[v.Key()] = a
		return a, nil
	})})
	if err != nil {
		return nil, err
	}
	dispatcher, err := renderruntime.New(renderruntime.Config{Coordinator: coordinator, Plans: service, Executors: service, Artifacts: service, Checkpoints: backendstage.CheckpointsV2{Service: service, Store: checkpoints}, MaxConcurrentJobs: r.maxConcurrentJobs, StageLimits: stagedag.Limits{MaxConcurrent: 1, MaxMemoryBytes: r.stageMemoryBytes, MaxDiskBytes: r.stageDiskBytes}})
	if err != nil {
		return nil, err
	}
	workers, err := startWorkerPool(ctx, dispatcher, r.maxConcurrentJobs, r.workerPollInterval)
	if err != nil {
		return nil, err
	}
	result.workers = workers
	queue, err := renderqueue.NewScoped(c.projectID, &notifyingCoordinator{dispatcher: dispatcher, workers: workers}, renderqueue.AdmissionFunc(func(ctx context.Context, _ string, j api.RenderJob) error {
		_, err := service.ResolveStagePlan(ctx, j)
		return err
	}))
	if err != nil {
		return nil, err
	}
	result.routes = queue.Routes()
	// Prepared native identities are ingested verbatim, not converted to legacy projects.
	result.routes = append(result.routes, router.Route{Tool: agentprotocol.ToolRender, Action: "prepare", Handler: router.ActionFunc(func(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
		if request.ProjectID != c.projectID {
			return router.Outcome{Diagnostics: []videoedit.Diagnostic{}}, nil
		}
		var payload struct {
			Environment json.RawMessage `json:"environment"`
		}
		if err := wirejson.Decode(request.Payload, &payload); err != nil {
			return router.Outcome{}, err
		}
		e, err := prepared.DecodeEnvironmentV2(ctx, payload.Environment)
		if err != nil {
			return router.Outcome{}, err
		}
		if e.Manifest.ProviderBuild != runner.ExecutableDigest() || e.Manifest.MediaRuntime != host.MediaRuntime {
			return router.Outcome{}, errors.New("native prepared environment differs from measured host authority")
		}
		if err := worker.AuthorizeLaunchEnvironmentV2(ctx, host, c.nativeV2.executable, e); err != nil {
			return router.Outcome{}, err
		}
		// Admission constructs and validates the fixed native assembler before durable storage.
		v, err := prepared.VerifyEnvironmentV2(ctx, e)
		if err != nil {
			return router.Outcome{}, err
		}
		if _, err = serviceAssemblerAdmissionV2(ctx, native, v, e, r, c.nativeV2, runtime.ffmpegExecutable, runtime.ffmpegRuntimeDependencyFingerprint, runner.ExecutableDigest(), host.MediaRuntime); err != nil {
			return router.Outcome{}, err
		}
		_, created, err := environments.PutV2(ctx, e.Document, e.Plan, e.Manifest)
		if err != nil {
			return router.Outcome{}, err
		}
		receipt, err := canonicaljson.Marshal(v.Key())
		return router.Outcome{OK: true, State: "prepared-native-v2", Changed: created, Replayed: !created, Receipt: receipt, Diagnostics: []videoedit.Diagnostic{}}, err
	})})
	result.routes = append(result.routes, router.Route{Tool: agentprotocol.ToolRender, Action: "preflight", Handler: router.ActionFunc(func(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
		if request.ProjectID != c.projectID {
			return router.Outcome{State: "rejected", Diagnostics: []videoedit.Diagnostic{{Code: "RENDER.V2.PROJECT_MISMATCH", Severity: documentv2.SeverityError, Path: "/project_id", Message: "project is outside the configured native runtime"}}, Receipt: json.RawMessage(`{}`)}, nil
		}
		var payload struct {
			PlanFingerprint              string `json:"plan_fingerprint"`
			ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
		}
		if err := wirejson.Decode(request.Payload, &payload); err != nil || payload.PlanFingerprint == "" || payload.ExecutionManifestFingerprint == "" {
			return router.Outcome{State: "rejected", Diagnostics: []videoedit.Diagnostic{{Code: "RENDER.V2.PAYLOAD_INVALID", Severity: documentv2.SeverityError, Path: "/payload", Message: "plan_fingerprint and execution_manifest_fingerprint are required"}}, Receipt: json.RawMessage(`{}`)}, nil
		}
		environment, err := environments.ResolveV2(ctx, payload.PlanFingerprint, payload.ExecutionManifestFingerprint)
		if err != nil {
			return router.Outcome{}, err
		}
		verified, err := prepared.VerifyEnvironmentV2(ctx, environment)
		if err != nil {
			return router.Outcome{}, err
		}
		environment, err = verified.Environment(ctx)
		if err != nil {
			return router.Outcome{}, err
		}
		if err := worker.AuthorizeLaunchEnvironmentV2(ctx, host, c.nativeV2.executable, environment); err != nil {
			return router.Outcome{}, err
		}
		if _, err := serviceAssemblerAdmissionV2(ctx, native, verified, environment, r, c.nativeV2, runtime.ffmpegExecutable, runtime.ffmpegRuntimeDependencyFingerprint, runner.ExecutableDigest(), host.MediaRuntime); err != nil {
			return router.Outcome{}, err
		}
		receipt, err := canonicaljson.Marshal(struct {
			Profile                      string                    `json:"profile"`
			PlanFingerprint              string                    `json:"plan_fingerprint"`
			ExecutionManifestFingerprint string                    `json:"execution_manifest_fingerprint"`
			ProviderBuild                string                    `json:"provider_build"`
			Backend                      api.BackendCapabilitiesV2 `json:"backend"`
			MediaRuntime                 api.MediaRuntime          `json:"media_runtime"`
		}{"cutly.render-preflight.document-v2.v2", environment.Plan.Fingerprint, environment.Manifest.Fingerprint, environment.Manifest.ProviderBuild, environment.Manifest.Backend, environment.Manifest.MediaRuntime})
		return router.Outcome{OK: true, State: "ready-native-v2", Receipt: receipt, Diagnostics: []videoedit.Diagnostic{}}, err
	})})
	result.routes = append(result.routes, (&nativeFramePreviewV2{
		projectID:          c.projectID,
		outputRoot:         r.outputDir,
		providerExecutable: c.nativeV2.executable,
		host:               host,
		environments:       environments,
		runner:             runner,
	}).Route())
	return result, nil
}

func serviceAssemblerAdmissionV2(ctx context.Context, n *nativeApplicationV2, v prepared.VerifiedEnvironmentV2, e prepared.EnvironmentV2, r renderConfig, native nativeConfigV2, executable, deps, provider string, runtime api.MediaRuntime) (backendstage.ChunkAssemblerBackendV2, error) {
	if e.Manifest.MediaRuntime != runtime {
		return nil, errors.New("native runtime authority mismatch")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if a := n.assemblers[v.Key()]; a != nil {
		return a, nil
	}
	if len(n.assemblers) >= 64 {
		return nil, errors.New("native prepared environment admission limit")
	}
	a, err := backendstage.NewFFmpegChunkAssemblerV2(ctx, ffmpegbackend.ChunkAssemblerOptionsV2{Executable: executable, OutputRoot: r.outputDir, SDKBuildDigest: r.sdkBuildDigest, WorkerExecutableDigest: provider, RuntimeDependencyFingerprint: deps, MaxStagingBytes: int64(r.stageDiskBytes / 3), SupervisorExecutable: native.supervisorExecutable, SupervisorExecutableDigest: native.supervisorDigest, SupervisorDependencyFingerprint: native.supervisorDependencies, Plan: e.Plan, Manifest: e.Manifest})
	if err == nil {
		n.assemblers[v.Key()] = a
	}
	return a, err
}
