package backendstage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	checkpointlocal "github.com/CutlyLab/Cutly/render/checkpoint/local"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func TestServiceBuildsExactSinglePublishPlan(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	plan, err := fixture.service.ResolveStagePlan(context.Background(), fixture.job("job-a", "a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.ResolveStagePlan(context.Background(), fixture.job("job-b", "b.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tasks) != 1 || plan.Tasks[0].Stage != renderapi.StagePublish || plan.Tasks[0].ID != PublishTaskID ||
		plan.Tasks[0].Retry.MaxAttempts != 1 || plan.Tasks[0].Retry.RetryResource || plan.Tasks[0].Retry.RetryTransient {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Tasks[0].Resources != (stagedag.ResourceEstimate{MemoryBytes: 64 << 10, DiskBytes: 1 << 20}) {
		t.Fatalf("resources = %+v", plan.Tasks[0].Resources)
	}
	if plan.Tasks[0].CacheKey != second.Tasks[0].CacheKey {
		t.Fatal("job identity or artifact URI perturbed the semantic media cache key")
	}
	if plan.JobID == second.JobID {
		t.Fatal("job-scoped stage plans collapsed")
	}
}

func TestServiceCacheKeyBindsTheNormalizedExactTimelineRange(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	first := fixture.job("job-range-a", "range-a.mp4")
	setJobTimelineRange(t, &first, videoedit.TimeRange{
		Start: rational(1, 1000), Duration: rational(1, 1),
	}, fixture.environment.Plan)

	equivalent := fixture.job("job-range-equivalent", "range-equivalent.mp4")
	setJobTimelineRange(t, &equivalent, videoedit.TimeRange{
		Start: rational(2, 2000), Duration: rational(2, 2),
	}, fixture.environment.Plan)

	shifted := fixture.job("job-range-shifted", "range-shifted.mp4")
	setJobTimelineRange(t, &shifted, videoedit.TimeRange{
		Start: rational(2, 1000), Duration: rational(1, 1),
	}, fixture.environment.Plan)
	if first.FrameRange != shifted.FrameRange || first.SampleRange != shifted.SampleRange {
		t.Fatalf("fixture requires equal tick ranges: first=%+v/%+v shifted=%+v/%+v",
			first.FrameRange, first.SampleRange, shifted.FrameRange, shifted.SampleRange)
	}

	firstPlan := mustPlan(t, fixture.service, first)
	equivalentPlan := mustPlan(t, fixture.service, equivalent)
	shiftedPlan := mustPlan(t, fixture.service, shifted)
	if firstPlan.Tasks[0].CacheKey != equivalentPlan.Tasks[0].CacheKey {
		t.Fatal("equivalent rational timeline ranges produced different cache keys")
	}
	if firstPlan.Tasks[0].CacheKey == shiftedPlan.Tasks[0].CacheKey {
		t.Fatal("different exact timeline ranges with equal ticks shared a cache key")
	}
	inputs := firstPlan.Tasks[0].CacheInput.Inputs
	if len(inputs) != 2 || inputs[1].Role != TimelineBindingRole {
		t.Fatalf("normalized inputs = %+v", inputs)
	}
}

func TestServiceRejectsInvalidConfigurationAndLiveBackend(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	base := Config{
		Environments: fixture.service.environments, Backends: fixture.service.backends,
		CAS: fixture.store, MemoryBytes: 1, DiskBytes: 1,
	}
	missingMemory := base
	missingMemory.MemoryBytes = 0
	if _, err := New(missingMemory); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing memory error = %v", err)
	}
	missingDisk := base
	missingDisk.DiskBytes = 0
	if _, err := New(missingDisk); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing disk error = %v", err)
	}
	missingCAS := base
	missingCAS.CAS = nil
	if _, err := New(missingCAS); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing CAS error = %v", err)
	}

	mismatch := newFakeBackend(testCapabilities())
	mismatch.capabilities.BuildFingerprint = digest('f')
	mismatchFixture := newServiceFixture(t, mismatch)
	job := mismatchFixture.job("job-identity", "identity.mp4")
	plan := mustPlan(t, mismatchFixture.service, job)
	if _, err := mismatchFixture.service.ResolveExecutor(context.Background(), job, plan); !errors.Is(err, ErrBackend) {
		t.Fatalf("identity mismatch error = %v", err)
	}

	rejected := newFakeBackend(testCapabilities())
	rejected.preflightDiagnostics = []videoedit.Diagnostic{
		{Code: "TEST.WARNING", Severity: videoedit.SeverityWarning, Path: "/warning", Message: "warning"},
		{Code: "TEST.REJECTED", Severity: videoedit.SeverityError, Path: "/plan", Message: "rejected"},
	}
	rejectedFixture := newServiceFixture(t, rejected)
	job = rejectedFixture.job("job-preflight", "preflight.mp4")
	plan = mustPlan(t, rejectedFixture.service, job)
	executor := mustExecutor(t, rejectedFixture.service, job, plan)
	if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); !errors.Is(err, ErrBackend) ||
		!strings.Contains(err.Error(), "TEST.REJECTED") || strings.Contains(err.Error(), "TEST.WARNING") {
		t.Fatalf("preflight rejection error = %v", err)
	}
}

func TestServiceRecoversPublicationBeforeCheckpointAndThenCacheHits(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	job := fixture.job("job-recover", "recover.mp4")
	plan := mustPlan(t, fixture.service, job)
	executor := mustExecutor(t, fixture.service, job, plan)

	// Execute once without committing the returned checkpoint: this is the
	// publish/CAS-visible crash window the next owner must reconcile.
	firstOutput, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.backend.renderCalls.Load() != 1 {
		t.Fatalf("render calls = %d", fixture.backend.renderCalls.Load())
	}
	firstArtifact, err := decodeReceipt(firstOutput.Receipt)
	if err != nil {
		t.Fatal(err)
	}

	checkpoints, err := checkpointlocal.New(filepath.Join(fixture.root, "checkpoints"), fixture.store, checkpointlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	engine := stagedag.Engine{
		Store: checkpoints, Executor: executor,
		Limits: stagedag.Limits{MaxConcurrent: 1, MaxMemoryBytes: 64 << 10, MaxDiskBytes: 1 << 20},
	}
	result, err := engine.Run(context.Background(), plan)
	if err != nil || result.Executed != 1 || result.CacheHits != 0 {
		t.Fatalf("recovery result=%+v err=%v", result, err)
	}
	if fixture.backend.renderCalls.Load() != 1 {
		t.Fatalf("recovery rerendered: calls=%d", fixture.backend.renderCalls.Load())
	}
	resolved, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result)
	if err != nil || !reflect.DeepEqual(resolved, firstArtifact) {
		t.Fatalf("resolved=%+v err=%v want=%+v", resolved, err, firstArtifact)
	}

	second, err := engine.Run(context.Background(), plan)
	if err != nil || second.Executed != 0 || second.CacheHits != 1 {
		t.Fatalf("cache result=%+v err=%v", second, err)
	}
	if fixture.backend.openCalls.Load() != 2 {
		// First manual execution and recovery engine each open one snapshot after
		// an inspection. A checkpoint hit must not invoke the executor again.
		t.Fatalf("open calls after checkpoint hit = %d", fixture.backend.openCalls.Load())
	}
}

func TestServiceRecoveryDoesNotRequireSourceTouchingPreflight(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	job := fixture.job("job-offline-recovery", "offline-recovery.mp4")
	plan := mustPlan(t, fixture.service, job)
	first := mustExecutor(t, fixture.service, job, plan)
	firstOutput, err := first.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.backend.preflightCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 1 {
		t.Fatalf("first execution preflight/render = %d/%d",
			fixture.backend.preflightCalls.Load(), fixture.backend.renderCalls.Load())
	}
	fixture.backend.preflightDiagnostics = []videoedit.Diagnostic{{
		Code: "TEST.SOURCE_OFFLINE", Severity: videoedit.SeverityError,
		Path: "/resources/0/locator", Message: "source is offline",
	}}
	recovered := mustExecutor(t, fixture.service, job, plan)
	secondOutput, err := recovered.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatalf("published recovery touched offline source preflight: %v", err)
	}
	if !reflect.DeepEqual(firstOutput, secondOutput) {
		t.Fatalf("recovered output=%+v want=%+v", secondOutput, firstOutput)
	}
	if fixture.backend.preflightCalls.Load() != 1 || fixture.backend.renderCalls.Load() != 1 {
		t.Fatalf("recovery preflight/render = %d/%d",
			fixture.backend.preflightCalls.Load(), fixture.backend.renderCalls.Load())
	}
}

func TestServiceSharesMediaCASAcrossJobsButKeepsReceiptsScoped(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	firstJob := fixture.job("job-one", "one.mp4")
	secondJob := fixture.job("job-two", "two.mp4")
	firstPlan := mustPlan(t, fixture.service, firstJob)
	secondPlan := mustPlan(t, fixture.service, secondJob)
	if firstPlan.Tasks[0].CacheKey != secondPlan.Tasks[0].CacheKey {
		t.Fatal("equivalent media work did not share a cache key")
	}
	firstOutput, err := mustExecutor(t, fixture.service, firstJob, firstPlan).Execute(context.Background(), firstPlan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	secondOutput, err := mustExecutor(t, fixture.service, secondJob, secondPlan).Execute(context.Background(), secondPlan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstOutput.ContentHash != secondOutput.ContentHash || firstOutput.SizeBytes != secondOutput.SizeBytes {
		t.Fatalf("media identities differ: %+v %+v", firstOutput, secondOutput)
	}
	firstArtifact, _ := decodeReceipt(firstOutput.Receipt)
	secondArtifact, _ := decodeReceipt(secondOutput.Receipt)
	if firstArtifact.URI == secondArtifact.URI || bytes.Equal(firstOutput.Receipt, secondOutput.Receipt) {
		t.Fatal("job-scoped publication receipts collapsed")
	}
	if fixture.backend.renderCalls.Load() != 2 {
		t.Fatalf("each publication URI must be rendered/published once, calls=%d", fixture.backend.renderCalls.Load())
	}
}

func TestServiceRejectsCASCollisionForDifferentMediaAtSameKey(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	firstJob := fixture.job("job-collision-one", "collision-one.mp4")
	firstPlan := mustPlan(t, fixture.service, firstJob)
	if _, err := mustExecutor(t, fixture.service, firstJob, firstPlan).Execute(context.Background(), firstPlan.Tasks[0], nil); err != nil {
		t.Fatal(err)
	}
	fixture.backend.media = []byte("different-encoded-media")
	secondJob := fixture.job("job-collision-two", "collision-two.mp4")
	secondPlan := mustPlan(t, fixture.service, secondJob)
	if _, err := mustExecutor(t, fixture.service, secondJob, secondPlan).Execute(context.Background(), secondPlan.Tasks[0], nil); !errors.Is(err, cas.ErrCollision) {
		t.Fatalf("CAS collision error = %v", err)
	}
}

func TestServiceAcceptsVerifiedPublicationAfterRenderReturnError(t *testing.T) {
	backend := newFakeBackend(testCapabilities())
	backend.renderErr = errors.New("response lost after publication")
	fixture := newServiceFixture(t, backend)
	job := fixture.job("job-commit-boundary", "commit-boundary.mp4")
	plan := mustPlan(t, fixture.service, job)
	output, err := mustExecutor(t, fixture.service, job, plan).Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil || output.ContentHash != hashBytes(backend.media) {
		t.Fatalf("post-publication recovery output=%+v err=%v", output, err)
	}
}

func TestServiceFailsClosedForMissingCASAndPublicationTamper(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	job := fixture.job("job-integrity", "integrity.mp4")
	plan := mustPlan(t, fixture.service, job)
	output, err := mustExecutor(t, fixture.service, job, plan).Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan, plan.Tasks[0], output, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := stagedag.RunResult{Checkpoints: []stagedag.Checkpoint{checkpoint}, Executed: 1}

	emptyRoot := t.TempDir()
	emptyCAS, err := cas.New(filepath.Join(emptyRoot, "cas"), cas.Options{})
	if err != nil {
		t.Fatal(err)
	}
	originalCAS := fixture.service.cas
	fixture.service.cas = emptyCAS
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); !errors.Is(err, ErrArtifact) {
		t.Fatalf("missing CAS error = %v", err)
	}
	fixture.service.cas = originalCAS

	fixture.backend.mu.Lock()
	publication := fixture.backend.publications[job.ArtifactURI]
	publication.artifact.ContentHash = digest('9')
	fixture.backend.publications[job.ArtifactURI] = publication
	fixture.backend.mu.Unlock()
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, result); !errors.Is(err, ErrArtifact) {
		t.Fatalf("tampered publication error = %v", err)
	}
}

func TestServiceCancellationAndResourceLimit(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		backend := newFakeBackend(testCapabilities())
		backend.blockRender = true
		fixture := newServiceFixture(t, backend)
		job := fixture.job("job-cancel", "cancel.mp4")
		plan := mustPlan(t, fixture.service, job)
		executor := mustExecutor(t, fixture.service, job, plan)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			_, err := executor.Execute(ctx, plan.Tasks[0], nil)
			finished <- err
		}()
		select {
		case <-backend.renderStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("render did not start")
		}
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled execution did not stop")
		}
	})

	t.Run("disk estimate", func(t *testing.T) {
		fixture := newServiceFixture(t, nil)
		fixture.service.diskBytes = 1
		job := fixture.job("job-budget", "budget.mp4")
		plan := mustPlan(t, fixture.service, job)
		executor := mustExecutor(t, fixture.service, job, plan)
		if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); !errors.Is(err, ErrExecution) {
			t.Fatalf("disk estimate error = %v", err)
		}
	})
}

func TestServiceRejectsOversizePublicationBeforeOpeningSnapshot(t *testing.T) {
	backend := newFakeBackend(testCapabilities())
	backend.media = bytes.Repeat([]byte{0x5a}, 512<<10)
	fixture := newServiceFixture(t, backend)
	job := fixture.job("job-oversize", "oversize.mp4")
	request := renderapi.RenderRequest{Job: job, Plan: fixture.environment.Plan, Manifest: fixture.environment.Manifest}
	if _, err := backend.Render(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, fixture.service, job)
	if _, err := mustExecutor(t, fixture.service, job, plan).Execute(context.Background(), plan.Tasks[0], nil); !errors.Is(err, ErrExecution) {
		t.Fatalf("oversize error = %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("oversize publication opened %d snapshots before admission", backend.openCalls.Load())
	}
	if backend.renderCalls.Load() != 1 {
		t.Fatalf("existing oversize publication was rerendered: %d", backend.renderCalls.Load())
	}
}

func TestServicePreservesCancellationAndDiskArithmetic(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.ResolveStagePlan(ctx, fixture.job("job-canceled-source", "canceled.mp4")); !errors.Is(err, context.Canceled) {
		t.Fatalf("source cancellation error = %v", err)
	}
	want := uint64(2*13) + cas.PublicationOverheadBytes
	if got, ok := minimumPublishDisk(13); !ok || got != want {
		t.Fatalf("minimum disk = %d,%v want %d", got, ok, want)
	}
	if _, ok := minimumPublishDisk(0); ok {
		t.Fatal("zero-size media produced a disk estimate")
	}
	if _, ok := minimumPublishDisk(1<<63 - 1); ok {
		t.Fatal("overflowing media produced a disk estimate")
	}
}

func TestServiceContainsPanicsNilAndCallbackMutation(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	var nilSource *panicEnvironmentSource
	if _, err := New(Config{Environments: nilSource, Backends: fixture.service.backends, CAS: fixture.store, MemoryBytes: 1, DiskBytes: 1}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("typed nil source error = %v", err)
	}
	var nilResolver *panicBackendResolver
	if _, err := New(Config{Environments: fixture.service.environments, Backends: nilResolver, CAS: fixture.store, MemoryBytes: 1, DiskBytes: 1}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("typed nil resolver error = %v", err)
	}
	panicSource := EnvironmentSourceFunc(func(context.Context, string, string) (prepared.Environment, error) {
		panic("environment boom")
	})
	panicService, err := New(Config{Environments: panicSource, Backends: fixture.service.backends, CAS: fixture.store, MemoryBytes: 1, DiskBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panicService.ResolveStagePlan(context.Background(), fixture.job("job-env-panic", "panic.mp4")); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("environment panic error = %v", err)
	}

	panicResolver := BackendResolverFunc(func(context.Context, renderapi.ExecutionManifest) (Backend, error) {
		panic("resolver boom")
	})
	panicService, err = New(Config{Environments: fixture.service.environments, Backends: panicResolver, CAS: fixture.store, MemoryBytes: 1, DiskBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	job := fixture.job("job-backend-panic", "backend-panic.mp4")
	plan := mustPlan(t, panicService, job)
	if _, err := panicService.ResolveExecutor(context.Background(), job, plan); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("resolver panic error = %v", err)
	}

	mutating := newFakeBackend(testCapabilities())
	mutating.mutatePreflight = true
	mutatingFixture := newServiceFixture(t, mutating)
	job = mutatingFixture.job("job-callback-ownership", "ownership.mp4")
	plan = mustPlan(t, mutatingFixture.service, job)
	executor := mustExecutor(t, mutatingFixture.service, job, plan)
	if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); err != nil {
		t.Fatalf("callback-owned request execution: %v", err)
	}
	if mutating.lastRenderValidation.Load() != nil {
		t.Fatalf("preflight mutation escaped into render request: %v", *mutating.lastRenderValidation.Load())
	}

	mutating.panicOpen = true
	job = mutatingFixture.job("job-open-panic", "open-panic.mp4")
	plan = mustPlan(t, mutatingFixture.service, job)
	executor = mustExecutor(t, mutatingFixture.service, job, plan)
	if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("open panic error = %v", err)
	}

	panicCASFixture := newServiceFixture(t, nil)
	job = panicCASFixture.job("job-cas-panic", "cas-panic.mp4")
	plan = mustPlan(t, panicCASFixture.service, job)
	executor = mustExecutor(t, panicCASFixture.service, job, plan)
	panicCASFixture.service.cas = panicContentStore{}
	if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("CAS panic error = %v", err)
	}

	closePanic := newFakeBackend(testCapabilities())
	closePanic.closePanic = true
	closeFixture := newServiceFixture(t, closePanic)
	job = closeFixture.job("job-close-panic", "close-panic.mp4")
	plan = mustPlan(t, closeFixture.service, job)
	executor = mustExecutor(t, closeFixture.service, job, plan)
	if _, err := executor.Execute(context.Background(), plan.Tasks[0], nil); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("published close panic error = %v", err)
	}
}

func TestServiceRejectsPlanTaskAndReceiptDrift(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	job := fixture.job("job-drift", "drift.mp4")
	plan := mustPlan(t, fixture.service, job)
	changed := plan
	changed.Tasks = append([]stagedag.Task(nil), plan.Tasks...)
	changed.Tasks[0].Resources.DiskBytes++
	if _, err := fixture.service.ResolveExecutor(context.Background(), job, changed); !errors.Is(err, ErrPlan) {
		t.Fatalf("plan drift error = %v", err)
	}
	executor := mustExecutor(t, fixture.service, job, plan)
	changedTask := plan.Tasks[0]
	changedTask.ID = "another"
	if _, err := executor.Execute(context.Background(), changedTask, nil); !errors.Is(err, ErrExecution) {
		t.Fatalf("task drift error = %v", err)
	}
	output, err := executor.Execute(context.Background(), plan.Tasks[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stagedag.NewCheckpoint(plan, plan.Tasks[0], output, 1)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.AttemptCount = 2
	if _, err := fixture.service.ResolveArtifact(context.Background(), job, plan, stagedag.RunResult{
		Checkpoints: []stagedag.Checkpoint{checkpoint}, Executed: 1,
	}); !errors.Is(err, ErrArtifact) {
		t.Fatalf("attempt drift error = %v", err)
	}
	if _, err := decodeReceipt([]byte(`{"profile":"cutly.render.backend-publish-receipt.v1","profile":"cutly.render.backend-publish-receipt.v1"}`)); !errors.Is(err, ErrArtifact) {
		t.Fatalf("duplicate receipt error = %v", err)
	}
}

type serviceFixture struct {
	service     *Service
	backend     *fakeBackend
	environment prepared.Environment
	store       *cas.Store
	root        string
}

func newServiceFixture(t *testing.T, backend *fakeBackend) serviceFixture {
	t.Helper()
	plan, manifest := testEnvironment(t)
	if backend == nil {
		backend = newFakeBackend(manifest.Backend)
	}
	root := t.TempDir()
	store, err := cas.New(filepath.Join(root, "cas"), cas.Options{})
	if err != nil {
		t.Fatal(err)
	}
	environment := prepared.Environment{Plan: plan, Manifest: manifest}
	source := EnvironmentSourceFunc(func(_ context.Context, planFingerprint, manifestFingerprint string) (prepared.Environment, error) {
		if planFingerprint != plan.Fingerprint || manifestFingerprint != manifest.Fingerprint {
			return prepared.Environment{}, prepared.ErrNotFound
		}
		return environment, nil
	})
	resolver := BackendResolverFunc(func(context.Context, renderapi.ExecutionManifest) (Backend, error) {
		return backend, nil
	})
	service, err := New(Config{
		Environments: source, Backends: resolver, CAS: store,
		MemoryBytes: 64 << 10, DiskBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return serviceFixture{service: service, backend: backend, environment: environment, store: store, root: root}
}

func (fixture serviceFixture) job(id, name string) renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion, ID: id, IdempotencyKey: id + "-once",
		PlanFingerprint:              fixture.environment.Plan.Fingerprint,
		ExecutionManifestFingerprint: fixture.environment.Manifest.Fingerprint,
		TimelineRange:                fixture.environment.Plan.RequestedRange, FrameRange: fixture.environment.Plan.FrameRange,
		SampleRange: fixture.environment.Plan.SampleRange,
		ArtifactURI: (&url.URL{Scheme: "file", Path: filepath.Join(fixture.root, name)}).String(),
	}
}

func testEnvironment(t *testing.T) (videoedit.RenderPlan, renderapi.ExecutionManifest) {
	t.Helper()
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Timelines: []videoedit.Timeline{{ID: "timeline"}},
	}
	requested := videoedit.TimeRange{Start: rational(0, 1), Duration: rational(2, 1)}
	plan, diagnostics := videoedit.Resolve(document, requested, videoedit.RenderTarget{
		TimelineID: "timeline", Width: 320, Height: 180,
		FrameClock:  videoedit.FrameClock{Origin: rational(0, 1), FramesPerSecond: rational(2, 1)},
		SampleClock: videoedit.SampleClock{Origin: rational(0, 1), SamplesPerSecond: 96},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digest('a'), EvaluatorBuild: digest('b'), Backend: testCapabilities(),
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digest('c'), DecoderProfile: "test-decoder-v1",
			EncoderProfile: "test-encoder-v1", ColorProfile: "test-color-v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "test-reference-v1", Format: "mp4", Width: 320, Height: 180,
			PixelFormat: "yuv420p", ColorSpace: "bt709", VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: 96, Channels: 2, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan, manifest
}

func testCapabilities() videoedit.BackendCapabilities {
	return videoedit.BackendCapabilities{
		BackendID: "test-backend", BuildFingerprint: digest('d'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindVideo},
		MaxWidth:   1920, MaxHeight: 1080,
	}
}

type fakePublication struct {
	media    []byte
	artifact renderapi.RenderArtifact
	assembly *ChunkAssemblyRequest
}

type fakeBackend struct {
	capabilities videoedit.BackendCapabilities
	media        []byte

	mu                sync.Mutex
	publications      map[string]fakePublication
	renderCalls       atomic.Int32
	preflightCalls    atomic.Int32
	inspectCalls      atomic.Int32
	openCalls         atomic.Int32
	assembleCalls     atomic.Int32
	assembledBytes    atomic.Int64
	chunkInspectCalls atomic.Int32
	chunkOpenCalls    atomic.Int32

	blockRender           bool
	renderStarted         chan struct{}
	panicOpen             bool
	closePanic            bool
	mutatePreflight       bool
	preflightDiagnostics  []videoedit.Diagnostic
	renderErr             error
	failBeforePublication atomic.Int32
	publishWithoutSource  atomic.Int32
	failAfterSource       atomic.Int32
	pauseChunkRead        atomic.Bool
	chunkReadPaused       chan struct{}
	resumeChunkRead       chan struct{}
	chunkPauseOnce        sync.Once
	lastRenderValidation  atomic.Pointer[error]
}

func newFakeBackend(capabilities videoedit.BackendCapabilities) *fakeBackend {
	return &fakeBackend{
		capabilities: capabilities, media: []byte("encoded-media"),
		publications: make(map[string]fakePublication), renderStarted: make(chan struct{}),
		chunkReadPaused: make(chan struct{}), resumeChunkRead: make(chan struct{}),
	}
}

func (backend *fakeBackend) ID() string { return backend.capabilities.BackendID }

func (backend *fakeBackend) Capabilities(context.Context) (videoedit.BackendCapabilities, error) {
	value := backend.capabilities
	value.TrackKinds = append([]videoedit.TrackKind(nil), value.TrackKinds...)
	value.AssetKinds = append([]videoedit.AssetKind(nil), value.AssetKinds...)
	value.Features = append([]videoedit.CapabilityID(nil), value.Features...)
	return value, nil
}

func (backend *fakeBackend) Preflight(_ context.Context, _ videoedit.RenderPlan, manifest renderapi.ExecutionManifest) []videoedit.Diagnostic {
	backend.preflightCalls.Add(1)
	if backend.mutatePreflight && len(manifest.Backend.TrackKinds) > 0 {
		manifest.Backend.TrackKinds[0] = videoedit.TrackKindAudio
	}
	return append([]videoedit.Diagnostic(nil), backend.preflightDiagnostics...)
}

func (backend *fakeBackend) Render(ctx context.Context, request renderapi.RenderRequest, _ renderapi.EventSink) (renderapi.RenderArtifact, error) {
	backend.renderCalls.Add(1)
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		backend.lastRenderValidation.Store(&err)
	}
	if backend.blockRender {
		select {
		case <-backend.renderStarted:
		default:
			close(backend.renderStarted)
		}
		<-ctx.Done()
		return renderapi.RenderArtifact{}, ctx.Err()
	}
	if backend.failBeforePublication.CompareAndSwap(1, 0) {
		return renderapi.RenderArtifact{}, errors.New("encoder process crashed before publication")
	}
	artifact := renderapi.RenderArtifact{
		URI: request.Job.ArtifactURI, ContentHash: hashBytes(backend.media), SizeBytes: int64(len(backend.media)),
		ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		EvidenceURI:                  request.Job.ArtifactURI + ".evidence.json", EvidenceFingerprint: digest('e'),
		Duration: request.Job.TimelineRange.Duration, FrameCount: request.Job.FrameRange.Count,
		SampleCount: request.Job.SampleRange.Count,
	}
	backend.mu.Lock()
	backend.publications[request.Job.ArtifactURI] = fakePublication{media: append([]byte(nil), backend.media...), artifact: artifact}
	backend.mu.Unlock()
	return artifact, backend.renderErr
}

func (backend *fakeBackend) AssembleChunks(ctx context.Context, request ChunkAssemblyRequest, source ChunkSource, _ renderapi.EventSink) (renderapi.RenderArtifact, error) {
	backend.assembleCalls.Add(1)
	if backend.failBeforePublication.CompareAndSwap(1, 0) {
		return renderapi.RenderArtifact{}, errors.New("encoder process crashed before chunk publication")
	}
	if backend.publishWithoutSource.CompareAndSwap(1, 0) {
		media := []byte("invalid-residual-assembly-without-source")
		artifact := renderapi.RenderArtifact{
			URI: request.ArtifactURI, ContentHash: hashBytes(media), SizeBytes: int64(len(media)),
			ExecutionManifestFingerprint: request.ExecutionManifestFingerprint,
			EvidenceURI:                  request.ArtifactURI + ".evidence.json", EvidenceFingerprint: digest('e'),
			Duration: request.TimelineRange.Duration, FrameCount: request.FrameRange.Count, SampleCount: request.SampleRange.Count,
		}
		owned := cloneChunkAssemblyRequest(request)
		backend.mu.Lock()
		backend.publications[request.ArtifactURI] = fakePublication{media: media, artifact: artifact, assembly: &owned}
		backend.mu.Unlock()
		return artifact, nil
	}
	hash := sha256.New()
	var consumed int64
	for _, descriptor := range appendDescriptors(request.Audio, request.Video) {
		reader, err := source.Open(ctx, descriptor)
		if err != nil {
			return renderapi.RenderArtifact{}, err
		}
		if backend.pauseChunkRead.Load() && consumed == 0 {
			var first [1]byte
			count, readErr := reader.Read(first[:])
			if count > 0 {
				_, _ = hash.Write(first[:count])
				consumed += int64(count)
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				_ = reader.Close()
				return renderapi.RenderArtifact{}, readErr
			}
			backend.chunkPauseOnce.Do(func() { close(backend.chunkReadPaused) })
			select {
			case <-backend.resumeChunkRead:
			case <-ctx.Done():
				_ = reader.Close()
				return renderapi.RenderArtifact{}, ctx.Err()
			}
		}
		count, copyErr := io.Copy(hash, reader)
		consumed += count
		closeErr := reader.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return renderapi.RenderArtifact{}, err
		}
	}
	backend.assembledBytes.Add(consumed)
	if backend.failAfterSource.CompareAndSwap(1, 0) {
		return renderapi.RenderArtifact{}, errors.New("encoder crashed after consuming chunks without publishing")
	}
	media := []byte("assembled-chunks:" + hex.EncodeToString(hash.Sum(nil)))
	artifact := renderapi.RenderArtifact{
		URI: request.ArtifactURI, ContentHash: hashBytes(media), SizeBytes: int64(len(media)),
		ExecutionManifestFingerprint: request.ExecutionManifestFingerprint,
		EvidenceURI:                  request.ArtifactURI + ".evidence.json", EvidenceFingerprint: digest('e'),
		Duration: request.TimelineRange.Duration, FrameCount: request.FrameRange.Count, SampleCount: request.SampleRange.Count,
	}
	owned := cloneChunkAssemblyRequest(request)
	backend.mu.Lock()
	backend.publications[request.ArtifactURI] = fakePublication{media: media, artifact: artifact, assembly: &owned}
	backend.mu.Unlock()
	return artifact, backend.renderErr
}

func (backend *fakeBackend) InspectChunkAssembly(_ context.Context, request ChunkAssemblyRequest) (renderapi.RenderArtifact, bool, error) {
	backend.chunkInspectCalls.Add(1)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	publication, found := backend.publications[request.ArtifactURI]
	if !found {
		return renderapi.RenderArtifact{}, false, nil
	}
	if publication.assembly == nil || !reflect.DeepEqual(*publication.assembly, request) {
		return renderapi.RenderArtifact{}, false, errors.New("chunk publication aggregate mismatch")
	}
	return publication.artifact, true, nil
}

func (backend *fakeBackend) OpenChunkAssembly(_ context.Context, request ChunkAssemblyRequest) (PublishedMedia, bool, error) {
	backend.chunkOpenCalls.Add(1)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	publication, found := backend.publications[request.ArtifactURI]
	if !found {
		return nil, false, nil
	}
	if publication.assembly == nil || !reflect.DeepEqual(*publication.assembly, request) {
		return nil, false, errors.New("chunk publication aggregate mismatch")
	}
	return &fakePublished{Reader: *bytes.NewReader(append([]byte(nil), publication.media...)), artifact: publication.artifact}, true, nil
}

func (backend *fakeBackend) InspectPublished(_ context.Context, request renderapi.RenderRequest) (renderapi.RenderArtifact, bool, error) {
	backend.inspectCalls.Add(1)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	publication, found := backend.publications[request.Job.ArtifactURI]
	return publication.artifact, found, nil
}

func (backend *fakeBackend) OpenPublished(_ context.Context, request renderapi.RenderRequest) (PublishedMedia, bool, error) {
	backend.openCalls.Add(1)
	if backend.panicOpen {
		panic("open boom")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	publication, found := backend.publications[request.Job.ArtifactURI]
	if !found {
		return nil, false, nil
	}
	return &fakePublished{
		Reader: *bytes.NewReader(append([]byte(nil), publication.media...)), artifact: publication.artifact,
		panicClose: backend.closePanic,
	}, true, nil
}

type fakePublished struct {
	bytes.Reader
	artifact   renderapi.RenderArtifact
	panicClose bool
}

func (published *fakePublished) Artifact() renderapi.RenderArtifact { return published.artifact }
func (published *fakePublished) Close() error {
	if published.panicClose {
		panic("close boom")
	}
	return nil
}

type panicEnvironmentSource struct{}

func (*panicEnvironmentSource) Resolve(context.Context, string, string) (prepared.Environment, error) {
	panic("typed nil must never be called")
}

type panicBackendResolver struct{}

func (*panicBackendResolver) ResolveBackend(context.Context, renderapi.ExecutionManifest) (Backend, error) {
	panic("typed nil must never be called")
}

type panicContentStore struct{}

func (panicContentStore) Publish(context.Context, cas.PublishRequest) (cas.PublishResult, error) {
	panic("CAS publish boom")
}

func (panicContentStore) Open(context.Context, cachekey.Key, string) (*cas.Reader, error) {
	panic("CAS open boom")
}

func mustPlan(t *testing.T, service *Service, job renderapi.RenderJob) stagedag.Plan {
	t.Helper()
	plan, err := service.ResolveStagePlan(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func mustExecutor(t *testing.T, service *Service, job renderapi.RenderJob, plan stagedag.Plan) stagedag.Executor {
	t.Helper()
	executor, err := service.ResolveExecutor(context.Background(), job, plan)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func rational(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func setJobTimelineRange(t *testing.T, job *renderapi.RenderJob, value videoedit.TimeRange, plan videoedit.RenderPlan) {
	t.Helper()
	job.TimelineRange = value
	var err error
	job.FrameRange, err = videoedit.CompileFrameTickRange(value, plan.Target.FrameClock)
	if err != nil {
		t.Fatal(err)
	}
	job.SampleRange, err = videoedit.CompileSampleTickRange(value, plan.Target.SampleClock)
	if err != nil {
		t.Fatal(err)
	}
}

func digest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

var _ io.ReadSeekCloser = (*fakePublished)(nil)
