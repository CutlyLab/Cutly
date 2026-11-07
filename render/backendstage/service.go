package backendstage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/chunkplan"
	"github.com/CutlyLab/Cutly/render/prepared"
	renderuntime "github.com/CutlyLab/Cutly/render/runtime"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	// StageProfileV1 is the executor-dispatch profile for a complete backend
	// publication. The remaining identifiers are its stable CAS protocol names.
	StageProfileV1           = "cutly.stage.backend-publish.v1"
	CacheNamespaceV1         = "cutly.cache.backend-publish.v1"
	ArtifactKindV1           = "cutly.cache-artifact.encoded-media.v1"
	OutputProfileV1          = "cutly.output.encoded-media.v1"
	TimelineBindingProfileV1 = "cutly.render.timeline-range-binding.v1"
	TimelineBindingRole      = "timeline_range"
	PublishTaskID            = "publish"
)

var (
	ErrConfiguration = errors.New("backendstage: invalid configuration")
	ErrEnvironment   = errors.New("backendstage: prepared environment is invalid")
	ErrPlan          = errors.New("backendstage: stage plan is invalid")
	ErrBackend       = errors.New("backendstage: backend is invalid")
	ErrExecution     = errors.New("backendstage: publish execution failed")
	ErrArtifact      = errors.New("backendstage: artifact integrity failed")
)

type contentStore interface {
	Publish(context.Context, cas.PublishRequest) (cas.PublishResult, error)
	Open(context.Context, cachekey.Key, string) (*cas.Reader, error)
}

// Config makes the complete backend render's peak admission estimate explicit.
// DiskBytes must cover the backend's own encoding/publication scratch plus the
// recovery snapshot, the CAS payload scratch, and CAS publication metadata.
// In reference mode MemoryBytes must additionally cover the dependency receipt,
// descriptor, aggregate and request validation allowance computed from the
// exact chunk count. StageLimits in the enclosing runtime must admit both
// values.
type Config struct {
	Environments EnvironmentSource
	// Backends is the legacy whole-job resolver. It is required when
	// ReferenceChunks is nil and is never called in reference mode.
	Backends BackendResolver
	// ReferenceAssembler is a host-owned, locator-free assembler boundary.
	// It is required exactly when ReferenceChunks is configured. Reference
	// execution never passes an ExecutionManifest to Backends and never calls
	// the legacy resolver.
	ReferenceAssembler ChunkAssemblerBackend
	CAS                *cas.Store
	MemoryBytes        uint64
	DiskBytes          uint64
	Progress           renderapi.EventSink
	// ReferenceChunks opts into the pre-codec reference surface/PCM profile.
	// Nil preserves the restricted hard-cut whole-job backend path exactly.
	ReferenceChunks *ReferenceChunkConfig
}

// Service implements the three runtime seams as one immutable unit so plan,
// execution and completion cannot silently use different cache profiles.
type Service struct {
	environments EnvironmentSource
	backends     BackendResolver
	assembler    ChunkAssemblerBackend
	cas          contentStore
	memoryBytes  uint64
	diskBytes    uint64
	progress     renderapi.EventSink
	reference    *referenceChunks
}

func New(config Config) (*Service, error) {
	if nilLike(config.Environments) || config.CAS == nil ||
		config.MemoryBytes == 0 || config.DiskBytes == 0 {
		return nil, ErrConfiguration
	}
	reference, err := normalizeReferenceChunks(config.ReferenceChunks)
	if err != nil {
		return nil, err
	}
	if reference == nil {
		if nilLike(config.Backends) {
			return nil, ErrConfiguration
		}
	} else if nilLike(config.ReferenceAssembler) {
		return nil, fmt.Errorf("%w: reference chunk profile requires a host-owned chunk assembler", ErrConfiguration)
	}
	return &Service{
		environments: config.Environments, backends: config.Backends, assembler: config.ReferenceAssembler, cas: config.CAS,
		memoryBytes: config.MemoryBytes, diskBytes: config.DiskBytes, progress: config.Progress,
		reference: reference,
	}, nil
}

func (service *Service) ResolveStagePlan(ctx context.Context, job renderapi.RenderJob) (stagedag.Plan, error) {
	_, request, err := service.resolveRequest(ctx, job)
	if err != nil {
		return stagedag.Plan{}, err
	}
	return service.buildPlan(ctx, request.Job)
}

func (service *Service) ResolveExecutor(ctx context.Context, job renderapi.RenderJob, supplied stagedag.Plan) (stagedag.Executor, error) {
	if service == nil || ctx == nil {
		return nil, ErrConfiguration
	}
	environment, request, err := service.resolveRequest(ctx, job)
	if err != nil {
		return nil, err
	}
	want, err := service.buildPlan(ctx, job)
	if err != nil {
		return nil, err
	}
	frozen, err := exactPlan(supplied, want)
	if err != nil {
		return nil, err
	}
	resolved := &executor{
		service: service, request: request, plan: frozen, reference: service.reference,
		taskIndex: make(map[string]int, len(frozen.Tasks)), newInvocation: newInvocationID,
	}
	for index, task := range frozen.Tasks {
		resolved.taskIndex[task.ID] = index
	}
	if service.reference == nil {
		backend, resolveErr := service.resolveBackend(ctx, environment)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolved.backend = backend
		resolved.publishTask = frozen.Tasks[0]
		return resolved, nil
	}
	assembler, err := service.resolveReferenceAssembler(ctx, environment.Manifest.Backend)
	if err != nil {
		return nil, err
	}
	resolved.assembler = assembler
	chunks, err := service.buildReferenceChunkPlan(ctx, request.Job)
	if err != nil {
		return nil, err
	}
	resolved.chunkPlan = &chunks
	resolved.publishTask = frozen.Tasks[len(frozen.Tasks)-1]
	return resolved, nil
}

func (service *Service) ResolveArtifact(ctx context.Context, job renderapi.RenderJob, supplied stagedag.Plan, result stagedag.RunResult) (renderapi.RenderArtifact, error) {
	if service == nil || ctx == nil {
		return renderapi.RenderArtifact{}, ErrConfiguration
	}
	environment, request, err := service.resolveRequest(ctx, job)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	want, err := service.buildPlan(ctx, job)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	plan, err := exactPlan(supplied, want)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if service.reference != nil {
		required, ok := referenceFinalValidationMemory(len(plan.Tasks) - 1)
		if !ok || plan.Tasks[len(plan.Tasks)-1].Resources.MemoryBytes < required {
			return renderapi.RenderArtifact{}, fmt.Errorf("%w: reference artifact validation memory budget", ErrArtifact)
		}
	}
	checkpoint, err := validateResult(plan, result)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	var receiptArtifact renderapi.RenderArtifact
	var assemblyRequest *ChunkAssemblyRequest
	if service.reference == nil {
		receiptArtifact, err = decodeReceipt(checkpoint.Output.Receipt)
	} else {
		var binding aggregateBinding
		receiptArtifact, binding, err = decodeReferencePublishReceipt(checkpoint.Output.Receipt)
		if err == nil {
			chunks, buildErr := service.buildReferenceChunkPlan(ctx, request.Job)
			if buildErr != nil {
				return renderapi.RenderArtifact{}, buildErr
			}
			selection, selectionErr := service.selectReferenceChunks(ctx, chunks, result.Checkpoints[:len(chunks.Tasks)])
			if selectionErr != nil || len(selection.Missing) != 0 || len(selection.Rejected) != 0 || len(selection.Ready) != len(chunks.Tasks) {
				return renderapi.RenderArtifact{}, errors.Join(fmt.Errorf("%w: reference chunk set is incomplete", ErrArtifact), selectionErr)
			}
			videoReceipts, audioReceipts, videoDescriptors, audioDescriptors, receiptErr := receiptsFromCheckpoints(chunks, selection.Ready, service.reference.executableDigest)
			if receiptErr != nil {
				return renderapi.RenderArtifact{}, receiptErr
			}
			actualBinding, aggregateErr := aggregateReceipts(ctx, request.Job, videoReceipts, audioReceipts)
			if aggregateErr != nil || !exactReferenceBinding(actualBinding, binding) {
				return renderapi.RenderArtifact{}, errors.Join(fmt.Errorf("%w: final receipt aggregate root differs from chunks", ErrArtifact), aggregateErr)
			}
			assembled, assemblyErr := newChunkAssemblyRequest(request, service.reference.executableDigest, binding, audioDescriptors, videoDescriptors)
			if assemblyErr != nil {
				return renderapi.RenderArtifact{}, assemblyErr
			}
			assemblyRequest = &assembled
		}
	}
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if err := validateArtifactBinding(receiptArtifact, request); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if receiptArtifact.ContentHash != checkpoint.Output.ContentHash || receiptArtifact.SizeBytes != checkpoint.Output.SizeBytes {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: receipt and checkpoint media identities differ", ErrArtifact)
	}
	publishTask := plan.Tasks[len(plan.Tasks)-1]
	if err := service.verifyCAS(ctx, publishTask, checkpoint); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	var actual renderapi.RenderArtifact
	var found bool
	if assemblyRequest == nil {
		backend, resolveErr := service.resolveBackend(ctx, environment)
		if resolveErr != nil {
			return renderapi.RenderArtifact{}, resolveErr
		}
		actual, found, err = safeInspectPublished(ctx, backend, request)
	} else {
		assembler, resolveErr := service.resolveReferenceAssembler(ctx, environment.Manifest.Backend)
		if resolveErr != nil {
			return renderapi.RenderArtifact{}, resolveErr
		}
		actual, found, err = safeInspectChunkAssembly(ctx, assembler, *assemblyRequest)
	}
	if err != nil {
		return renderapi.RenderArtifact{}, wrapError(ErrArtifact, "inspect published artifact", err)
	}
	if !found || !reflect.DeepEqual(actual, receiptArtifact) {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: published artifact does not match the checkpoint receipt", ErrArtifact)
	}
	if err := validateArtifactBinding(actual, request); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	return actual, nil
}

func (service *Service) buildPlan(ctx context.Context, job renderapi.RenderJob) (stagedag.Plan, error) {
	if service.reference != nil {
		return service.buildReferencePlan(ctx, job)
	}
	stageBinding, err := stagedag.StageBindingInput(renderapi.StagePublish)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	timelineBinding, err := timelineRangeBindingInput(job.TimelineRange)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	frameRange := job.FrameRange
	sampleRange := job.SampleRange
	var taskFrameRange *videoedit.TickRange
	var cacheFrameRange *cachekey.IndexRange
	if frameRange.Count > 0 {
		taskFrameRange = &frameRange
		cacheFrameRange = &cachekey.IndexRange{Start: frameRange.First, Count: frameRange.Count}
	}
	var taskSampleRange *videoedit.TickRange
	var cacheSampleRange *cachekey.IndexRange
	if sampleRange.Count > 0 {
		taskSampleRange = &sampleRange
		cacheSampleRange = &cachekey.IndexRange{Start: sampleRange.First, Count: sampleRange.Count}
	}
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: CacheNamespaceV1, ArtifactKind: ArtifactKindV1,
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		StageProfile: StageProfileV1, Inputs: []cachekey.NormalizedInput{stageBinding, timelineBinding},
		FrameRange:    cacheFrameRange,
		SampleRange:   cacheSampleRange,
		OutputProfile: OutputProfileV1,
	}
	key, err := cachekey.New(input)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: job.ID,
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		Tasks: []stagedag.Task{{
			ID: PublishTaskID, Stage: renderapi.StagePublish, Profile: StageProfileV1,
			DependsOn: []string{}, CacheInput: input, CacheKey: key,
			FrameRange: taskFrameRange, SampleRange: taskSampleRange,
			Resources: stagedag.ResourceEstimate{MemoryBytes: service.memoryBytes, DiskBytes: service.diskBytes},
			Retry:     stagedag.RetryPolicy{MaxAttempts: 1},
		}},
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	return plan, nil
}

func timelineRangeBindingInput(value videoedit.TimeRange) (cachekey.NormalizedInput, error) {
	if err := value.Validate(); err != nil || value.Duration.Value <= 0 {
		return cachekey.NormalizedInput{}, errors.Join(errors.New("timeline range must be valid and positive"), err)
	}
	start, err := value.Start.Normalize()
	if err != nil {
		return cachekey.NormalizedInput{}, err
	}
	duration, err := value.Duration.Normalize()
	if err != nil {
		return cachekey.NormalizedInput{}, err
	}
	canonical, err := videoedit.CanonicalJSON(videoedit.TimeRange{Start: start, Duration: duration})
	if err != nil {
		return cachekey.NormalizedInput{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(TimelineBindingProfileV1))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return cachekey.NormalizedInput{
		Role: TimelineBindingRole, Ordinal: 0,
		ContentHash: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func (service *Service) resolveRequest(ctx context.Context, job renderapi.RenderJob) (prepared.Environment, renderapi.RenderRequest, error) {
	if service == nil || nilLike(service.environments) || ctx == nil {
		return prepared.Environment{}, renderapi.RenderRequest{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return prepared.Environment{}, renderapi.RenderRequest{}, err
	}
	environment, err := safeResolveEnvironment(ctx, service.environments, job.PlanFingerprint, job.ExecutionManifestFingerprint)
	if err != nil {
		return prepared.Environment{}, renderapi.RenderRequest{}, wrapError(ErrEnvironment, "resolve", err)
	}
	environment, err = freezeEnvironment(environment)
	if err != nil {
		return prepared.Environment{}, renderapi.RenderRequest{}, err
	}
	request := renderapi.RenderRequest{Job: job, Plan: environment.Plan, Manifest: environment.Manifest}
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		return prepared.Environment{}, renderapi.RenderRequest{}, wrapError(ErrEnvironment, "request binding", err)
	}
	if service.reference != nil && service.reference.executableDigest != request.Manifest.EvaluatorBuild {
		return prepared.Environment{}, renderapi.RenderRequest{}, fmt.Errorf("%w: reference provider digest must equal the frozen evaluator build", ErrEnvironment)
	}
	return environment, request, nil
}

func freezeEnvironment(environment prepared.Environment) (prepared.Environment, error) {
	planRaw, err := json.Marshal(environment.Plan)
	if err != nil {
		return prepared.Environment{}, wrapError(ErrEnvironment, "encode plan", err)
	}
	plan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		return prepared.Environment{}, wrapError(ErrEnvironment, "plan", err)
	}
	manifestRaw, err := json.Marshal(environment.Manifest)
	if err != nil {
		return prepared.Environment{}, wrapError(ErrEnvironment, "encode manifest", err)
	}
	manifest, err := renderapi.DecodeExecutionManifest(manifestRaw)
	if err != nil {
		return prepared.Environment{}, wrapError(ErrEnvironment, "manifest", err)
	}
	refrozen, err := renderapi.FreezeExecutionManifest(plan, manifest)
	if err != nil {
		return prepared.Environment{}, wrapError(ErrEnvironment, "refreeze manifest", err)
	}
	suppliedCanonical, suppliedErr := videoedit.CanonicalJSON(manifest)
	refrozenCanonical, refrozenErr := videoedit.CanonicalJSON(refrozen)
	if err := errors.Join(suppliedErr, refrozenErr); err != nil || !bytes.Equal(suppliedCanonical, refrozenCanonical) {
		return prepared.Environment{}, errors.Join(fmt.Errorf("%w: manifest is not the exact frozen environment", ErrEnvironment), err)
	}
	return prepared.Environment{Plan: plan, Manifest: manifest}, nil
}

func (service *Service) resolveBackend(ctx context.Context, environment prepared.Environment) (Backend, error) {
	if nilLike(service.backends) {
		return nil, ErrConfiguration
	}
	resolverEnvironment, err := freezeEnvironment(environment)
	if err != nil {
		return nil, err
	}
	backend, err := safeResolveBackend(ctx, service.backends, resolverEnvironment.Manifest)
	if err != nil {
		return nil, wrapError(ErrBackend, "resolve", err)
	}
	if nilLike(backend) {
		return nil, fmt.Errorf("%w: resolver returned nil", ErrBackend)
	}
	identifier, err := safeBackendID(backend)
	if err != nil {
		return nil, wrapError(ErrBackend, "ID", err)
	}
	capabilities, err := safeCapabilities(ctx, backend)
	if err != nil {
		return nil, wrapError(ErrBackend, "capabilities", err)
	}
	if identifier != environment.Manifest.Backend.BackendID || !reflect.DeepEqual(
		videoedit.CanonicalBackendCapabilities(capabilities),
		videoedit.CanonicalBackendCapabilities(environment.Manifest.Backend),
	) {
		return nil, fmt.Errorf("%w: live identity does not match the frozen manifest", ErrBackend)
	}
	return backend, nil
}

// resolveReferenceAssembler validates a directly configured host-owned
// assembler against the locator-free backend identity frozen in the manifest.
// Unlike resolveBackend, it deliberately has no resolver call and never hands
// Resources, Fonts, or their locators to backend-selection code.
func (service *Service) resolveReferenceAssembler(ctx context.Context, expected videoedit.BackendCapabilities) (ChunkAssemblerBackend, error) {
	if service == nil || ctx == nil || nilLike(service.assembler) {
		return nil, fmt.Errorf("%w: host-owned reference assembler is required", ErrBackend)
	}
	identifier, err := safeBackendID(service.assembler)
	if err != nil {
		return nil, wrapError(ErrBackend, "reference assembler ID", err)
	}
	capabilities, err := safeCapabilities(ctx, service.assembler)
	if err != nil {
		return nil, wrapError(ErrBackend, "reference assembler capabilities", err)
	}
	if identifier != expected.BackendID || !reflect.DeepEqual(
		videoedit.CanonicalBackendCapabilities(capabilities),
		videoedit.CanonicalBackendCapabilities(expected),
	) {
		return nil, fmt.Errorf("%w: live reference assembler identity does not match the frozen locator-free backend identity", ErrBackend)
	}
	return service.assembler, nil
}

func exactPlan(supplied, wanted stagedag.Plan) (stagedag.Plan, error) {
	suppliedRaw, err := stagedag.CanonicalPlan(supplied)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	wantedRaw, err := stagedag.CanonicalPlan(wanted)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	if !bytes.Equal(suppliedRaw, wantedRaw) {
		return stagedag.Plan{}, fmt.Errorf("%w: supplied plan differs from the derived plan", ErrPlan)
	}
	frozen, err := stagedag.DecodePlan(suppliedRaw)
	if err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	return frozen, nil
}

func validateResult(plan stagedag.Plan, result stagedag.RunResult) (stagedag.Checkpoint, error) {
	if len(plan.Tasks) == 0 || len(result.Checkpoints) != len(plan.Tasks) ||
		len(result.TaskOutcomes) != len(plan.Tasks) || result.Retries < 0 ||
		result.Executed < 0 || result.CacheHits < 0 {
		return stagedag.Checkpoint{}, fmt.Errorf("%w: run result shape", ErrArtifact)
	}
	executed, cacheHits, retries := 0, 0, 0
	for index, task := range plan.Tasks {
		checkpoint, outcome := result.Checkpoints[index], result.TaskOutcomes[index]
		if _, err := stagedag.CanonicalCheckpoint(checkpoint); err != nil {
			return stagedag.Checkpoint{}, errors.Join(ErrArtifact, err)
		}
		if checkpoint.JobID != plan.JobID || checkpoint.PlanFingerprint != plan.PlanFingerprint ||
			checkpoint.ExecutionManifestFingerprint != plan.ExecutionManifestFingerprint ||
			checkpoint.TaskID != task.ID || checkpoint.Stage != task.Stage || checkpoint.TaskProfile != task.Profile ||
			checkpoint.CacheKey != task.CacheKey || !rangesEqual(checkpoint.FrameRange, task.FrameRange) ||
			!rangesEqual(checkpoint.SampleRange, task.SampleRange) || outcome.TaskID != task.ID {
			return stagedag.Checkpoint{}, fmt.Errorf("%w: checkpoint/task binding", ErrArtifact)
		}
		if isReferenceChunkTask(task) {
			if _, err := boundChunkReceipt(task, checkpoint, ""); err != nil {
				return stagedag.Checkpoint{}, errors.Join(ErrArtifact, err)
			}
		} else if checkpoint.AttemptCount != 1 {
			// Legacy and reference-final receipts do not carry an attempt field;
			// those task policies remain deliberately single-attempt.
			return stagedag.Checkpoint{}, fmt.Errorf("%w: publish checkpoint attempt", ErrArtifact)
		}
		if outcome.CacheHit {
			if outcome.AttemptsThisRun != 0 {
				return stagedag.Checkpoint{}, fmt.Errorf("%w: cache-hit task has execution attempts", ErrArtifact)
			}
			cacheHits++
			continue
		}
		if !attemptWithinPolicy(task, outcome.AttemptsThisRun) || outcome.AttemptsThisRun != checkpoint.AttemptCount {
			return stagedag.Checkpoint{}, fmt.Errorf("%w: executed task attempt telemetry", ErrArtifact)
		}
		executed++
		retries += int(outcome.AttemptsThisRun) - 1
	}
	if result.Executed != executed || result.CacheHits != cacheHits || result.Retries != retries ||
		executed+cacheHits != len(plan.Tasks) {
		return stagedag.Checkpoint{}, fmt.Errorf("%w: run retry telemetry", ErrArtifact)
	}
	return result.Checkpoints[len(result.Checkpoints)-1], nil
}

func validateArtifactBinding(artifact renderapi.RenderArtifact, request renderapi.RenderRequest) error {
	evidenceURI, err := renderapi.EvidenceSidecarURI(request.Job.ArtifactURI)
	if err != nil {
		return errors.Join(ErrArtifact, err)
	}
	durationComparison, durationErr := artifact.Duration.Compare(request.Job.TimelineRange.Duration)
	if artifact.URI != request.Job.ArtifactURI || artifact.EvidenceURI != evidenceURI ||
		!cachekey.CanonicalSHA256(artifact.ContentHash) || artifact.SizeBytes <= 0 ||
		artifact.ExecutionManifestFingerprint != request.Manifest.Fingerprint ||
		!cachekey.CanonicalSHA256(artifact.EvidenceFingerprint) || durationErr != nil || durationComparison != 0 ||
		artifact.FrameCount != request.Job.FrameRange.Count || artifact.SampleCount != request.Job.SampleRange.Count {
		return errors.Join(fmt.Errorf("%w: artifact is not exactly bound to the render request", ErrArtifact), durationErr)
	}
	return nil
}

func minimumPublishDisk(mediaBytes int64) (uint64, bool) {
	if mediaBytes <= 0 {
		return 0, false
	}
	payload := uint64(mediaBytes)
	maximum := ^uint64(0)
	if payload > (maximum-cas.PublicationOverheadBytes)/2 {
		return maximum, false
	}
	return payload*2 + cas.PublicationOverheadBytes, true
}

func (service *Service) verifyCAS(ctx context.Context, task stagedag.Task, checkpoint stagedag.Checkpoint) (returnErr error) {
	reader, err := safeCASOpen(ctx, service.cas, task.CacheKey, "backendstage-artifact-"+strings.TrimPrefix(task.CacheKey.Digest, "sha256:")[:24])
	if err != nil {
		return wrapError(ErrArtifact, "open CAS media", err)
	}
	if reader == nil {
		return fmt.Errorf("%w: CAS returned nil reader", ErrArtifact)
	}
	defer func() { returnErr = errors.Join(returnErr, reader.Close()) }()
	if reader.Entry.Key != task.CacheKey || reader.Entry.PayloadHash != checkpoint.Output.ContentHash ||
		reader.Entry.PayloadSize != checkpoint.Output.SizeBytes {
		return fmt.Errorf("%w: CAS media identity differs from checkpoint", ErrArtifact)
	}
	return nil
}

type executor struct {
	service       *Service
	request       renderapi.RenderRequest
	plan          stagedag.Plan
	publishTask   stagedag.Task
	backend       Backend
	assembler     ChunkAssemblerBackend
	reference     *referenceChunks
	chunkPlan     *chunkplan.Plan
	taskIndex     map[string]int
	newInvocation func() (string, error)
}

func (executor *executor) Execute(ctx context.Context, task stagedag.Task, dependencies []stagedag.Dependency) (stagedag.StageOutput, error) {
	return executor.ExecuteAttempt(ctx, task, dependencies, 1)
}

// ExecuteAttempt binds the exact 1-based stagedag attempt into reference
// chunk receipts. Execute remains the compatibility entry point and is exactly
// equivalent to attempt 1.
func (executor *executor) ExecuteAttempt(ctx context.Context, task stagedag.Task, dependencies []stagedag.Dependency, attempt uint8) (stagedag.StageOutput, error) {
	if executor == nil || executor.service == nil || ctx == nil {
		return stagedag.StageOutput{}, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return stagedag.StageOutput{}, err
	}
	index, found := executor.taskIndex[task.ID]
	if !found || index < 0 || index >= len(executor.plan.Tasks) || !reflect.DeepEqual(task, executor.plan.Tasks[index]) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: executor received another task", ErrExecution)
	}
	if !attemptWithinPolicy(task, attempt) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: attempt %d exceeds task policy", ErrExecution, attempt)
	}
	if task.Profile == chunkplan.VideoStageProfileV1 || task.Profile == chunkplan.AudioStageProfileV1 {
		if len(dependencies) != 0 {
			return stagedag.StageOutput{}, fmt.Errorf("%w: chunk task received dependencies", ErrExecution)
		}
		return executor.executeChunk(ctx, task, attempt)
	}
	if !reflect.DeepEqual(task, executor.publishTask) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: unsupported task profile", ErrExecution)
	}
	var assembly *validatedChunkAssembly
	if executor.reference == nil {
		if len(dependencies) != 0 {
			return stagedag.StageOutput{}, fmt.Errorf("%w: legacy publish received dependencies", ErrExecution)
		}
	} else {
		value, err := executor.validateChunkDependencies(ctx, dependencies)
		if err != nil {
			return stagedag.StageOutput{}, err
		}
		assembly = value
	}
	return executor.execute(ctx, task, assembly)
}

func (executor *executor) execute(ctx context.Context, task stagedag.Task, assembly *validatedChunkAssembly) (output stagedag.StageOutput, returnErr error) {
	var artifact renderapi.RenderArtifact
	var found bool
	var err error
	if assembly == nil {
		artifact, found, err = safeInspectPublished(ctx, executor.backend, executor.request)
		if err != nil {
			return stagedag.StageOutput{}, wrapError(ErrExecution, "inspect recovery state", err)
		}
		if !found {
			if err := preflightForRender(ctx, executor.backend, executor.request); err != nil {
				return stagedag.StageOutput{}, err
			}
			rendered, renderErr := safeRender(ctx, executor.backend, executor.request, executor.service.progress)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return stagedag.StageOutput{}, ctxErr
			}
			var inspectErr error
			artifact, found, inspectErr = safeInspectPublished(ctx, executor.backend, executor.request)
			if inspectErr != nil || !found {
				return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: render did not leave a valid publication", ErrExecution), renderErr, inspectErr)
			}
			if renderErr == nil && !reflect.DeepEqual(rendered, artifact) {
				return stagedag.StageOutput{}, fmt.Errorf("%w: render result differs from inspected publication", ErrExecution)
			}
			// If Render crossed its publication boundary and then returned an
			// error, the independently verified publication is authoritative.
		}
	} else {
		// A reference final checkpoint is the only recovery authority. Before
		// that checkpoint exists, even a matching residual publication must not
		// bypass consumption of the complete verified chunk source. The codec
		// assembly therefore restarts in full on every final-task invocation.
		rendered, assembleErr := safeAssembleChunks(ctx, executor.assembler, assembly.request, assembly.source, executor.service.progress)
		sourceErr := assembly.source.VerifyComplete()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stagedag.StageOutput{}, errors.Join(ctxErr, sourceErr)
		}
		if sourceErr != nil {
			return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: assembler did not consume the complete verified chunk set", ErrExecution), assembleErr, sourceErr)
		}
		if assembleErr != nil {
			return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: chunk assembler returned an error after consuming the verified source", ErrExecution), assembleErr)
		}
		var inspectErr error
		artifact, found, inspectErr = safeInspectChunkAssembly(ctx, executor.assembler, assembly.request)
		if inspectErr != nil || !found {
			return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: assembly did not leave a valid publication", ErrExecution), assembleErr, inspectErr)
		}
		if !reflect.DeepEqual(rendered, artifact) {
			return stagedag.StageOutput{}, fmt.Errorf("%w: assembly result differs from inspected publication", ErrExecution)
		}
	}
	if err := validateArtifactBinding(artifact, executor.request); err != nil {
		return stagedag.StageOutput{}, err
	}
	requiredDisk, ok := minimumPublishDisk(artifact.SizeBytes)
	if !ok || requiredDisk > task.Resources.DiskBytes {
		return stagedag.StageOutput{}, fmt.Errorf("%w: snapshot+CAS minimum disk %d for media size %d exceeds declared estimate %d", ErrExecution, requiredDisk, artifact.SizeBytes, task.Resources.DiskBytes)
	}
	var published PublishedMedia
	var opened bool
	if assembly == nil {
		published, opened, err = safeOpenPublished(ctx, executor.backend, executor.request)
	} else {
		published, opened, err = safeOpenChunkAssembly(ctx, executor.assembler, assembly.request)
	}
	if err != nil || !opened {
		return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: inspected publication could not be opened", ErrExecution), err)
	}
	if nilLike(published) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: backend returned nil published media", ErrExecution)
	}
	defer func() {
		if closeErr := safePublishedClose(published); closeErr != nil {
			output = stagedag.StageOutput{}
			returnErr = errors.Join(returnErr, wrapError(ErrExecution, "close published media", closeErr))
		}
	}()
	openedArtifact, err := safePublishedArtifact(published)
	if err != nil || !reflect.DeepEqual(openedArtifact, artifact) {
		return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: opened publication differs from inspection", ErrExecution), err)
	}
	if position, err := safePublishedSeek(published, 0, io.SeekStart); err != nil || position != 0 {
		return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: rewind published media", ErrExecution), err)
	}
	result, err := safeCASPublish(ctx, executor.service.cas, cas.PublishRequest{
		Key: task.CacheKey, Payload: published, ExpectedHash: artifact.ContentHash, ExpectedSize: artifact.SizeBytes,
	})
	if err != nil {
		return stagedag.StageOutput{}, wrapError(ErrExecution, "publish media to CAS", err)
	}
	if result.Entry.Key != task.CacheKey || result.Entry.PayloadHash != artifact.ContentHash || result.Entry.PayloadSize != artifact.SizeBytes ||
		(result.Disposition != cas.Published && result.Disposition != cas.AlreadyPresent) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: CAS returned another media identity", ErrExecution)
	}
	var receipt json.RawMessage
	if assembly == nil {
		receipt, err = encodeReceipt(artifact)
	} else {
		receipt, err = encodeReferencePublishReceipt(artifact, assembly.request.Aggregate)
	}
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	return stagedag.StageOutput{ContentHash: artifact.ContentHash, SizeBytes: artifact.SizeBytes, Receipt: receipt}, nil
}

func preflightForRender(ctx context.Context, backend Backend, request renderapi.RenderRequest) error {
	owned, err := cloneRequest(request)
	if err != nil {
		return err
	}
	diagnostics, err := safePreflight(ctx, backend, owned.Plan, owned.Manifest)
	if err != nil {
		return wrapError(ErrBackend, "preflight", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == videoedit.SeverityError {
			return fmt.Errorf("%w: preflight rejected code=%q path=%q", ErrBackend, diagnostic.Code, diagnostic.Path)
		}
	}
	return nil
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func safeResolveEnvironment(ctx context.Context, source EnvironmentSource, plan, manifest string) (_ prepared.Environment, returnErr error) {
	defer recoverError("environment source", &returnErr)
	return source.Resolve(ctx, plan, manifest)
}

func safeResolveBackend(ctx context.Context, resolver BackendResolver, manifest renderapi.ExecutionManifest) (_ Backend, returnErr error) {
	defer recoverError("backend resolver", &returnErr)
	return resolver.ResolveBackend(ctx, manifest)
}

func safeBackendID(backend backendIdentity) (_ string, returnErr error) {
	defer recoverError("backend ID", &returnErr)
	return backend.ID(), nil
}

func safeCapabilities(ctx context.Context, backend backendIdentity) (_ videoedit.BackendCapabilities, returnErr error) {
	defer recoverError("backend capabilities", &returnErr)
	return backend.Capabilities(ctx)
}

func safePreflight(ctx context.Context, backend Backend, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) (_ []videoedit.Diagnostic, returnErr error) {
	defer recoverError("backend preflight", &returnErr)
	return backend.Preflight(ctx, plan, manifest), nil
}

func safeOpenPublished(ctx context.Context, backend Backend, request renderapi.RenderRequest) (_ PublishedMedia, _ bool, returnErr error) {
	defer recoverError("backend open published", &returnErr)
	owned, err := cloneRequest(request)
	if err != nil {
		return nil, false, err
	}
	published, found, err := backend.OpenPublished(ctx, owned)
	if err != nil {
		if !nilLike(published) {
			err = errors.Join(err, safePublishedClose(published))
		}
		return nil, false, err
	}
	if found == nilLike(published) {
		if !nilLike(published) {
			_ = safePublishedClose(published)
		}
		return nil, false, fmt.Errorf("%w: backend returned inconsistent found/media values", ErrBackend)
	}
	return published, found, nil
}

func safeInspectPublished(ctx context.Context, backend Backend, request renderapi.RenderRequest) (_ renderapi.RenderArtifact, _ bool, returnErr error) {
	defer recoverError("backend inspect published", &returnErr)
	owned, err := cloneRequest(request)
	if err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	return backend.InspectPublished(ctx, owned)
}

func safeRender(ctx context.Context, backend Backend, request renderapi.RenderRequest, sink renderapi.EventSink) (_ renderapi.RenderArtifact, returnErr error) {
	defer recoverError("backend render", &returnErr)
	owned, err := cloneRequest(request)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	return backend.Render(ctx, owned, sink)
}

func safeAssembleChunks(ctx context.Context, backend ChunkAssemblerBackend, request ChunkAssemblyRequest,
	source ChunkSource, sink renderapi.EventSink) (_ renderapi.RenderArtifact, returnErr error) {
	defer recoverError("chunk assembler", &returnErr)
	if nilLike(backend) || nilLike(source) {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: assembler backend and chunk source are required", ErrBackend)
	}
	owned := cloneChunkAssemblyRequest(request)
	if err := validateChunkAssemblyRequest(owned); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	return backend.AssembleChunks(ctx, owned, source, sink)
}

func safeInspectChunkAssembly(ctx context.Context, backend ChunkAssemblerBackend, request ChunkAssemblyRequest) (_ renderapi.RenderArtifact, _ bool, returnErr error) {
	defer recoverError("chunk assembly inspection", &returnErr)
	if nilLike(backend) {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: assembler backend is required", ErrBackend)
	}
	owned := cloneChunkAssemblyRequest(request)
	if err := validateChunkAssemblyRequest(owned); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	return backend.InspectChunkAssembly(ctx, owned)
}

func safeOpenChunkAssembly(ctx context.Context, backend ChunkAssemblerBackend, request ChunkAssemblyRequest) (_ PublishedMedia, _ bool, returnErr error) {
	defer recoverError("chunk assembly open", &returnErr)
	if nilLike(backend) {
		return nil, false, fmt.Errorf("%w: assembler backend is required", ErrBackend)
	}
	owned := cloneChunkAssemblyRequest(request)
	if err := validateChunkAssemblyRequest(owned); err != nil {
		return nil, false, err
	}
	published, found, err := backend.OpenChunkAssembly(ctx, owned)
	if err != nil {
		if !nilLike(published) {
			err = errors.Join(err, safePublishedClose(published))
		}
		return nil, false, err
	}
	if found == nilLike(published) {
		if !nilLike(published) {
			_ = safePublishedClose(published)
		}
		return nil, false, fmt.Errorf("%w: assembler backend returned inconsistent found/media values", ErrBackend)
	}
	return published, found, nil
}

func safePublishedArtifact(published PublishedMedia) (_ renderapi.RenderArtifact, returnErr error) {
	defer recoverError("published artifact", &returnErr)
	if nilLike(published) {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: nil published media", ErrExecution)
	}
	return published.Artifact(), nil
}

func safePublishedClose(published PublishedMedia) (returnErr error) {
	defer recoverError("published media close", &returnErr)
	if nilLike(published) {
		return nil
	}
	return published.Close()
}

func safePublishedSeek(published PublishedMedia, offset int64, whence int) (_ int64, returnErr error) {
	defer recoverError("published media seek", &returnErr)
	if nilLike(published) {
		return 0, fmt.Errorf("%w: nil published media", ErrExecution)
	}
	return published.Seek(offset, whence)
}

func safeCASPublish(ctx context.Context, store contentStore, request cas.PublishRequest) (_ cas.PublishResult, returnErr error) {
	defer recoverError("CAS publish", &returnErr)
	return store.Publish(ctx, request)
}

func safeCASOpen(ctx context.Context, store contentStore, key cachekey.Key, owner string) (_ *cas.Reader, returnErr error) {
	defer recoverError("CAS open", &returnErr)
	return store.Open(ctx, key, owner)
}

func cloneRequest(request renderapi.RenderRequest) (renderapi.RenderRequest, error) {
	environment, err := freezeEnvironment(prepared.Environment{Plan: request.Plan, Manifest: request.Manifest})
	if err != nil {
		return renderapi.RenderRequest{}, err
	}
	clone := renderapi.RenderRequest{Job: request.Job, Plan: environment.Plan, Manifest: environment.Manifest}
	if err := renderapi.ValidateRenderRequest(clone); err != nil {
		return renderapi.RenderRequest{}, errors.Join(ErrEnvironment, err)
	}
	return clone, nil
}

func recoverError(component string, target *error) {
	if recovered := recover(); recovered != nil {
		*target = fmt.Errorf("backendstage: %s panic: %v", component, recovered)
	}
}

func wrapError(class error, message string, cause error) error {
	if cause == nil {
		return class
	}
	return errors.Join(class, fmt.Errorf("%s: %w", message, cause))
}

var (
	_ renderuntime.PlanSource       = (*Service)(nil)
	_ renderuntime.ExecutorSource   = (*Service)(nil)
	_ renderuntime.ArtifactResolver = (*Service)(nil)
	_ stagedag.AttemptExecutor      = (*executor)(nil)
)
