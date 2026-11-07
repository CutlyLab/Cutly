package backendstage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/chunkplan"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	ReferenceChunkPublishStageProfileV1 = "cutly.stage.backend-publish-after-reference-chunks.v1"
	ReferenceChunkPublishCacheV1        = "cutly.cache.backend-publish-after-reference-chunks.v1"
	ReferenceChunkPublishReceiptV1      = "cutly.render.backend-publish-after-reference-chunks-receipt.v1"
	ReferenceChunkAggregateBindingV1    = "cutly.render.reference-chunk-aggregate-binding.v1"
	ReferenceChunkPublishTaskID         = "zz-publish"
	ReferenceChunkResumeModeV1          = "pre-codec-chunks-resume-final-codec-restarts.v1"

	// A publish task needs one stage input and one exact-timeline input in
	// addition to one dependency input per chunk.
	ReferenceChunkMaxTasks = cachekey.MaxNormalizedInputs - 2

	// Final validation can retain engine dependency clones, SelectMissing
	// candidates/ready values, decoded receipts and aggregate descriptors at the
	// same time. Budget four maximum-size receipt copies plus fixed Go/JSON
	// metadata per dependency; the fixed allowance covers the request and roots.
	referenceReceiptWorkingCopies       uint64 = 4
	referenceDependencyFixedMemoryBytes uint64 = 8 << 10
	referenceFinalFixedMemoryBytes      uint64 = 64 << 10
)

// SurfaceProvider renders one exact composition frame in the canonical
// cpucompositor surface domain. Implementations must be safe for concurrent
// calls and return an immutable caller-owned surface.
type SurfaceProvider interface {
	RenderSurface(context.Context, ReferenceChunkRequest, int64) (*cpucompositor.Surface, error)
}

type SurfaceProviderFunc func(context.Context, ReferenceChunkRequest, int64) (*cpucompositor.Surface, error)

func (function SurfaceProviderFunc) RenderSurface(ctx context.Context, request ReferenceChunkRequest, frame int64) (*cpucompositor.Surface, error) {
	return function(ctx, request, frame)
}

// PCMProvider renders one exact composition sample range as canonical stereo
// PCM16LE. Implementations must be safe for concurrent calls and return an
// immutable caller-owned block.
type PCMProvider interface {
	RenderPCM(context.Context, ReferenceChunkRequest, videoedit.TickRange) (*pcm16le.Block, error)
}

type PCMProviderFunc func(context.Context, ReferenceChunkRequest, videoedit.TickRange) (*pcm16le.Block, error)

func (function PCMProviderFunc) RenderPCM(ctx context.Context, request ReferenceChunkRequest, sampleRange videoedit.TickRange) (*pcm16le.Block, error) {
	return function(ctx, request, sampleRange)
}

// ReferenceChunkRequest intentionally omits JobID, IdempotencyKey and the
// publication URI. Those values are excluded from chunk cache keys and must
// therefore be impossible for a provider to observe while producing bytes.
type ReferenceChunkRequest struct {
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	TimelineID                   videoedit.ID           `json:"timeline_id"`
	Target                       videoedit.RenderTarget `json:"target"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
}

const ChunkAssemblyProfileV1 = "cutly.render.reference-chunk-assembly.v1"

// ChunkDescriptor is one verified, immutable input to a final assembler. Audio
// and video descriptors are presented in fixed composition order.
type ChunkDescriptor struct {
	TaskID      string              `json:"task_id"`
	Domain      string              `json:"domain"`
	CacheKey    cachekey.Key        `json:"cache_key"`
	Range       videoedit.TickRange `json:"range"`
	ContentHash string              `json:"content_hash"`
	SizeBytes   int64               `json:"size_bytes"`
	UnitBytes   int64               `json:"unit_bytes"`
}

// ChunkAssemblyRequest contains no original media locators. A conforming
// backend can obtain decoded media only through the supplied ChunkSource.
type ChunkAssemblyRequest struct {
	Profile                      string                 `json:"profile"`
	JobID                        string                 `json:"job_id"`
	ArtifactURI                  string                 `json:"artifact_uri"`
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	TimelineRange                videoedit.TimeRange    `json:"timeline_range"`
	FrameRange                   videoedit.TickRange    `json:"frame_range"`
	SampleRange                  videoedit.TickRange    `json:"sample_range"`
	Target                       videoedit.RenderTarget `json:"target"`
	MediaRuntime                 api.MediaRuntime       `json:"media_runtime"`
	Output                       api.OutputSpec         `json:"output"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
	ProviderExecutableDigest     string                 `json:"provider_executable_digest"`
	Aggregate                    ChunkAggregateBinding  `json:"aggregate"`
	Audio                        []ChunkDescriptor      `json:"audio"`
	Video                        []ChunkDescriptor      `json:"video"`
}

// ChunkSource opens only descriptors present in its assembly request. Readers
// are forward-only and Close fails unless the complete verified payload was
// consumed.
type ChunkSource interface {
	Open(context.Context, ChunkDescriptor) (io.ReadCloser, error)
}

// ChunkAssemblerBackend is mandatory for the reference chunk profile. Its
// assembly, recovery inspection, and publication-open methods must bind the
// external media/evidence pair to the complete ChunkAssemblyRequest. The
// original Backend.Render path is never used for this profile.
type ChunkAssemblerBackend interface {
	ID() string
	Capabilities(context.Context) (videoedit.BackendCapabilities, error)
	AssembleChunks(context.Context, ChunkAssemblyRequest, ChunkSource, api.EventSink) (api.RenderArtifact, error)
	InspectChunkAssembly(context.Context, ChunkAssemblyRequest) (api.RenderArtifact, bool, error)
	OpenChunkAssembly(context.Context, ChunkAssemblyRequest) (PublishedMedia, bool, error)
}

// ReferenceChunkConfig explicitly opts a Service into the executable
// reference chunk profile. There is deliberately no implicit adapter from the
// whole-job backend: callers must provide real surface and PCM providers.
//
// ProviderExecutableDigest is the caller-declared provider implementation
// identity carried by chunk receipts. It is provenance/admission metadata, not
// a trusted-execution signature or proof of the bytes actually loaded.
// ScratchDirectory holds one bounded pre-publication spool per active chunk.
// It must be host-owned and on storage covered by the declared DiskBytes.
type ReferenceChunkConfig struct {
	Surfaces                 SurfaceProvider
	PCM                      PCMProvider
	ProviderExecutableDigest string
	ScratchDirectory         string
	VideoResources           stagedag.ResourceEstimate
	AudioResources           stagedag.ResourceEstimate
	MaxTasks                 int
}

type referenceChunks struct {
	surfaces         SurfaceProvider
	pcm              PCMProvider
	executableDigest string
	scratchDirectory string
	videoResources   stagedag.ResourceEstimate
	audioResources   stagedag.ResourceEstimate
	maxTasks         int
}

func normalizeReferenceChunks(value *ReferenceChunkConfig) (*referenceChunks, error) {
	if value == nil {
		return nil, nil
	}
	maximum := value.MaxTasks
	if maximum == 0 {
		maximum = ReferenceChunkMaxTasks
	}
	if nilLike(value.Surfaces) || nilLike(value.PCM) ||
		!cachekey.CanonicalSHA256(value.ProviderExecutableDigest) ||
		value.VideoResources.MemoryBytes == 0 || value.VideoResources.DiskBytes == 0 ||
		value.AudioResources.MemoryBytes == 0 || value.AudioResources.DiskBytes == 0 ||
		maximum <= 0 || maximum > ReferenceChunkMaxTasks {
		return nil, fmt.Errorf("%w: invalid reference chunk providers, identity, resources, or task limit", ErrConfiguration)
	}
	path := value.ScratchDirectory
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return nil, fmt.Errorf("%w: reference chunk scratch must be a clean absolute non-root directory", ErrConfiguration)
	}
	if err := validateReferenceScratchDirectory(path); err != nil {
		return nil, err
	}
	return &referenceChunks{
		surfaces: value.Surfaces, pcm: value.PCM,
		executableDigest: value.ProviderExecutableDigest,
		scratchDirectory: path, videoResources: value.VideoResources,
		audioResources: value.AudioResources, maxTasks: maximum,
	}, nil
}

// validateReferenceScratchDirectory rejects substitution-prone paths before a
// provider can write canonical decoded media. On Unix the leaf must be private
// 0700; every ancestor must be a real directory, with only safe sticky shared
// roots permitted to be group/other writable.
// This is deliberately stricter than filepath.EvalSymlinks: a caller must pass
// the resolved host-owned path rather than relying on a symlinked prefix.
func validateReferenceScratchDirectory(path string) error {
	current := path
	leaf := true
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return errors.Join(fmt.Errorf("%w: inspect reference chunk scratch component %q", ErrConfiguration, current), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: reference chunk scratch component %q must be a real directory", ErrConfiguration, current)
		}
		if err := validateReferenceScratchMode(current, info, leaf); err != nil {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current, leaf = parent, false
	}
}

func (service *Service) buildReferenceChunkPlan(ctx context.Context, job api.RenderJob) (chunkplan.Plan, error) {
	if service == nil || service.reference == nil {
		return chunkplan.Plan{}, fmt.Errorf("%w: reference chunk profile is not configured", ErrConfiguration)
	}
	plan, err := chunkplan.Build(ctx, job, chunkplan.Config{
		VideoResources: service.reference.videoResources,
		AudioResources: service.reference.audioResources,
		Retry:          stagedag.RetryPolicy{MaxAttempts: 1},
		MaxTasks:       service.reference.maxTasks,
	})
	if err != nil {
		return chunkplan.Plan{}, errors.Join(ErrPlan, err)
	}
	return plan, nil
}

func (service *Service) buildReferencePlan(ctx context.Context, job api.RenderJob) (stagedag.Plan, error) {
	chunks, err := service.buildReferenceChunkPlan(ctx, job)
	if err != nil {
		return stagedag.Plan{}, err
	}
	publish, err := service.buildReferencePublishTask(job, chunks.Tasks)
	if err != nil {
		return stagedag.Plan{}, err
	}
	tasks := make([]stagedag.Task, 0, len(chunks.Tasks)+1)
	tasks = append(tasks, chunks.Tasks...)
	tasks = append(tasks, publish)
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: job.ID,
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		Tasks: tasks,
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		return stagedag.Plan{}, errors.Join(ErrPlan, err)
	}
	return plan, nil
}

func (service *Service) buildReferencePublishTask(job api.RenderJob, chunks []stagedag.Task) (stagedag.Task, error) {
	if len(chunks) == 0 || len(chunks) > ReferenceChunkMaxTasks {
		return stagedag.Task{}, fmt.Errorf("%w: reference chunk dependency count %d", ErrPlan, len(chunks))
	}
	validationMemory, ok := referenceFinalValidationMemory(len(chunks))
	if !ok || service.memoryBytes < validationMemory {
		return stagedag.Task{}, fmt.Errorf("%w: reference final validation requires memory=%d, declared=%d", ErrPlan, validationMemory, service.memoryBytes)
	}
	stageBinding, err := stagedag.StageBindingInput(api.StagePublish)
	if err != nil {
		return stagedag.Task{}, errors.Join(ErrPlan, err)
	}
	timelineBinding, err := timelineRangeBindingInput(job.TimelineRange)
	if err != nil {
		return stagedag.Task{}, errors.Join(ErrPlan, err)
	}
	dependencies := make([]string, len(chunks))
	inputs := make([]cachekey.NormalizedInput, 0, len(chunks)+2)
	for index, task := range chunks {
		dependencies[index] = task.ID
		input, inputErr := stagedag.DependencyBindingInput(index, task.CacheKey)
		if inputErr != nil {
			return stagedag.Task{}, errors.Join(ErrPlan, inputErr)
		}
		inputs = append(inputs, input)
	}
	// cache/key requires role order: dependency, stage, timeline_range.
	inputs = append(inputs, stageBinding, timelineBinding)
	frameRange := job.FrameRange
	sampleRange := job.SampleRange
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: ReferenceChunkPublishCacheV1, ArtifactKind: ArtifactKindV1,
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		StageProfile: ReferenceChunkPublishStageProfileV1, Inputs: inputs,
		OutputProfile: OutputProfileV1,
	}
	task := stagedag.Task{
		ID: ReferenceChunkPublishTaskID, Stage: api.StagePublish, Profile: ReferenceChunkPublishStageProfileV1,
		DependsOn: dependencies, Resources: stagedag.ResourceEstimate{MemoryBytes: service.memoryBytes, DiskBytes: service.diskBytes},
		Retry: stagedag.RetryPolicy{MaxAttempts: 1},
	}
	if frameRange.Count > 0 {
		task.FrameRange = &frameRange
		input.FrameRange = &cachekey.IndexRange{Start: frameRange.First, Count: frameRange.Count}
	}
	if sampleRange.Count > 0 {
		task.SampleRange = &sampleRange
		input.SampleRange = &cachekey.IndexRange{Start: sampleRange.First, Count: sampleRange.Count}
	}
	key, err := cachekey.New(input)
	if err != nil {
		return stagedag.Task{}, errors.Join(ErrPlan, err)
	}
	task.CacheInput, task.CacheKey = input, key
	return task, nil
}

func referenceFinalValidationMemory(dependencyCount int) (uint64, bool) {
	if dependencyCount <= 0 || dependencyCount > ReferenceChunkMaxTasks {
		return 0, false
	}
	perDependency := referenceReceiptWorkingCopies*uint64(chunkplan.MaxReceiptBytes) + referenceDependencyFixedMemoryBytes
	count := uint64(dependencyCount)
	maximum := ^uint64(0)
	if count > (maximum-referenceFinalFixedMemoryBytes)/perDependency {
		return 0, false
	}
	return referenceFinalFixedMemoryBytes + count*perDependency, true
}

type chunkMetrics struct {
	unitBytes    int64
	payloadBytes int64
	memoryBytes  uint64
	diskBytes    uint64
}

func metricsForChunk(request api.RenderRequest, task stagedag.Task) (chunkMetrics, error) {
	var unit, count uint64
	switch task.Profile {
	case chunkplan.VideoStageProfileV1:
		if task.FrameRange == nil || request.Plan.Target.Width <= 0 || request.Plan.Target.Height <= 0 {
			return chunkMetrics{}, fmt.Errorf("%w: invalid video chunk dimensions or range", ErrExecution)
		}
		width, height := uint64(request.Plan.Target.Width), uint64(request.Plan.Target.Height)
		if width > math.MaxUint64/height || width*height > math.MaxUint64/cpucompositor.BytesPerPixel {
			return chunkMetrics{}, fmt.Errorf("%w: video unit byte overflow", ErrExecution)
		}
		unit, count = width*height*cpucompositor.BytesPerPixel, uint64(task.FrameRange.Count)
	case chunkplan.AudioStageProfileV1:
		if task.SampleRange == nil {
			return chunkMetrics{}, fmt.Errorf("%w: invalid audio chunk range", ErrExecution)
		}
		unit, count = pcm16le.BytesPerFrame, uint64(task.SampleRange.Count)
	default:
		return chunkMetrics{}, fmt.Errorf("%w: unsupported chunk profile %q", ErrExecution, task.Profile)
	}
	if unit == 0 || unit > math.MaxInt64 || count > math.MaxInt64/unit {
		return chunkMetrics{}, fmt.Errorf("%w: chunk payload byte overflow", ErrExecution)
	}
	payload := int64(unit * count)
	memory := unit
	if task.Profile == chunkplan.AudioStageProfileV1 {
		memory = uint64(payload)
	}
	disk, ok := minimumPublishDisk(payload)
	if !ok {
		return chunkMetrics{}, fmt.Errorf("%w: chunk disk byte overflow", ErrExecution)
	}
	return chunkMetrics{unitBytes: int64(unit), payloadBytes: payload, memoryBytes: memory, diskBytes: disk}, nil
}

func (executor *executor) executeChunk(ctx context.Context, task stagedag.Task) (output stagedag.StageOutput, returnErr error) {
	if executor.reference == nil {
		return stagedag.StageOutput{}, fmt.Errorf("%w: reference chunk executor is not configured", ErrExecution)
	}
	metrics, err := metricsForChunk(executor.request, task)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	if metrics.memoryBytes > task.Resources.MemoryBytes || metrics.diskBytes > task.Resources.DiskBytes {
		return stagedag.StageOutput{}, fmt.Errorf("%w: chunk minimum memory=%d disk=%d exceeds declared memory=%d disk=%d",
			ErrExecution, metrics.memoryBytes, metrics.diskBytes, task.Resources.MemoryBytes, task.Resources.DiskBytes)
	}
	invocation, err := newInvocationID()
	if err != nil {
		return stagedag.StageOutput{}, wrapError(ErrExecution, "create unique provider invocation ID", err)
	}
	// Revalidate immediately before opening the spool. Configuration-time
	// validation cannot protect a long-lived Service from later path, owner, or
	// permission drift. This narrows but cannot eliminate the remaining
	// validate/CreateTemp TOCTOU without a dirfd-relative API.
	if err := validateReferenceScratchDirectory(executor.reference.scratchDirectory); err != nil {
		return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: reference chunk scratch changed after configuration", ErrExecution), err)
	}
	spool, err := os.CreateTemp(executor.reference.scratchDirectory, ".cutly-reference-chunk-"+task.ID+"-")
	if err != nil {
		return stagedag.StageOutput{}, wrapError(ErrExecution, "create chunk spool", err)
	}
	spoolPath := spool.Name()
	defer func() {
		returnErr = errors.Join(returnErr, closeFile(spool), removeSpool(spoolPath))
		if returnErr != nil {
			output = stagedag.StageOutput{}
		}
	}()
	hash := sha256.New()
	written := &countingWriter{writer: io.MultiWriter(spool, hash)}
	switch task.Profile {
	case chunkplan.VideoStageProfileV1:
		for offset := int64(0); offset < task.FrameRange.Count; offset++ {
			if err := ctx.Err(); err != nil {
				return stagedag.StageOutput{}, err
			}
			surface, providerErr := safeRenderSurface(ctx, executor.reference.surfaces, executor.request, task.FrameRange.First+offset)
			if providerErr != nil {
				return stagedag.StageOutput{}, wrapError(ErrExecution, "render reference surface", providerErr)
			}
			if surface == nil || surface.Width() != executor.request.Plan.Target.Width || surface.Height() != executor.request.Plan.Target.Height {
				return stagedag.StageOutput{}, fmt.Errorf("%w: surface provider returned another frame geometry", ErrExecution)
			}
			if err := safeWriteSurface(surface, written); err != nil {
				return stagedag.StageOutput{}, wrapError(ErrExecution, "serialize reference surface", err)
			}
		}
	case chunkplan.AudioStageProfileV1:
		block, providerErr := safeRenderPCM(ctx, executor.reference.pcm, executor.request, *task.SampleRange)
		if providerErr != nil {
			return stagedag.StageOutput{}, wrapError(ErrExecution, "render reference PCM", providerErr)
		}
		if block == nil || block.FrameCount() != uint64(task.SampleRange.Count) {
			return stagedag.StageOutput{}, fmt.Errorf("%w: PCM provider returned another sample count", ErrExecution)
		}
		if err := safeWritePCM(block, written); err != nil {
			return stagedag.StageOutput{}, wrapError(ErrExecution, "serialize reference PCM", err)
		}
	default:
		return stagedag.StageOutput{}, fmt.Errorf("%w: unsupported chunk task", ErrExecution)
	}
	if err := ctx.Err(); err != nil {
		return stagedag.StageOutput{}, err
	}
	if written.count != metrics.payloadBytes {
		return stagedag.StageOutput{}, fmt.Errorf("%w: canonical chunk bytes %d, want %d", ErrExecution, written.count, metrics.payloadBytes)
	}
	contentHash := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if position, err := spool.Seek(0, io.SeekStart); err != nil || position != 0 {
		return stagedag.StageOutput{}, errors.Join(fmt.Errorf("%w: rewind chunk spool", ErrExecution), err)
	}
	published, err := safeCASPublish(ctx, executor.service.cas, cas.PublishRequest{
		Key: task.CacheKey, Payload: spool, ExpectedHash: contentHash, ExpectedSize: metrics.payloadBytes,
	})
	if err != nil {
		return stagedag.StageOutput{}, wrapError(ErrExecution, "publish reference chunk to CAS", err)
	}
	if published.Entry.Key != task.CacheKey || published.Entry.PayloadHash != contentHash ||
		published.Entry.PayloadSize != metrics.payloadBytes ||
		(published.Disposition != cas.Published && published.Disposition != cas.AlreadyPresent) {
		return stagedag.StageOutput{}, fmt.Errorf("%w: CAS returned another chunk identity", ErrExecution)
	}
	receipt, err := chunkplan.NewReceipt(ctx, task, chunkplan.OutputIdentity{
		ContentHash: contentHash, SizeBytes: metrics.payloadBytes, UnitBytes: metrics.unitBytes,
	}, 1, chunkplan.WorkerBinding{
		InvocationID:     invocation,
		ExecutableDigest: executor.reference.executableDigest,
	})
	if err != nil {
		return stagedag.StageOutput{}, errors.Join(ErrExecution, err)
	}
	return chunkplan.StageOutput(receipt)
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	if written > 0 {
		writer.count += int64(written)
	}
	return written, err
}

func closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func removeSpool(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func newInvocationID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "reference-" + hex.EncodeToString(nonce[:]), nil
}

func safeRenderSurface(ctx context.Context, provider SurfaceProvider, request api.RenderRequest, frame int64) (_ *cpucompositor.Surface, returnErr error) {
	defer recoverError("reference surface provider", &returnErr)
	return provider.RenderSurface(ctx, referenceProviderRequest(request), frame)
}

func safeRenderPCM(ctx context.Context, provider PCMProvider, request api.RenderRequest, sampleRange videoedit.TickRange) (_ *pcm16le.Block, returnErr error) {
	defer recoverError("reference PCM provider", &returnErr)
	return provider.RenderPCM(ctx, referenceProviderRequest(request), sampleRange)
}

func referenceProviderRequest(request api.RenderRequest) ReferenceChunkRequest {
	return ReferenceChunkRequest{
		PlanFingerprint: request.Job.PlanFingerprint, ExecutionManifestFingerprint: request.Job.ExecutionManifestFingerprint,
		TimelineID: request.Plan.TimelineID, Target: request.Plan.Target,
		DeterministicSeed: request.Manifest.DeterministicSeed,
	}
}

func safeWriteSurface(surface *cpucompositor.Surface, writer io.Writer) (returnErr error) {
	defer recoverError("reference surface serialization", &returnErr)
	return surface.WriteCanonical(writer)
}

func safeWritePCM(block *pcm16le.Block, writer io.Writer) (returnErr error) {
	defer recoverError("reference PCM serialization", &returnErr)
	return block.WriteCanonical(writer)
}

type aggregateRoot struct {
	Domain     string              `json:"domain"`
	UnitBytes  int64               `json:"unit_bytes"`
	Range      videoedit.TickRange `json:"range"`
	ChunkCount int                 `json:"chunk_count"`
	RootHash   string              `json:"root_hash"`
}

type aggregateBinding struct {
	Profile    string         `json:"profile"`
	ResumeMode string         `json:"resume_mode"`
	Video      *aggregateRoot `json:"video,omitempty"`
	Audio      *aggregateRoot `json:"audio,omitempty"`
}

// ChunkAggregateRoot and ChunkAggregateBinding are the public, immutable
// aggregate identities a chunk assembler must bind into its publication
// evidence.
type ChunkAggregateRoot = aggregateRoot
type ChunkAggregateBinding = aggregateBinding

func bindingFromSequences(video, audio *evidence.DecodedSequence) aggregateBinding {
	value := aggregateBinding{Profile: ReferenceChunkAggregateBindingV1, ResumeMode: ReferenceChunkResumeModeV1}
	if video != nil {
		value.Video = &aggregateRoot{Domain: video.Domain, UnitBytes: video.UnitBytes, Range: video.Range, ChunkCount: len(video.Chunks), RootHash: video.RootHash}
	}
	if audio != nil {
		value.Audio = &aggregateRoot{Domain: audio.Domain, UnitBytes: audio.UnitBytes, Range: audio.Range, ChunkCount: len(audio.Chunks), RootHash: audio.RootHash}
	}
	return value
}

type validatedChunkAssembly struct {
	request ChunkAssemblyRequest
	source  *referenceChunkSource
}

func (executor *executor) validateChunkDependencies(ctx context.Context, dependencies []stagedag.Dependency) (*validatedChunkAssembly, error) {
	if executor.chunkPlan == nil || len(dependencies) != len(executor.chunkPlan.Tasks) {
		return nil, fmt.Errorf("%w: incomplete reference chunk dependencies", ErrExecution)
	}
	videoReceipts := make([]chunkplan.Receipt, 0)
	audioReceipts := make([]chunkplan.Receipt, 0)
	videoDescriptors := make([]ChunkDescriptor, 0)
	audioDescriptors := make([]ChunkDescriptor, 0)
	for index, task := range executor.chunkPlan.Tasks {
		dependency := dependencies[index]
		checkpoint := dependency.Checkpoint
		if _, err := stagedag.CanonicalCheckpoint(checkpoint); err != nil {
			return nil, errors.Join(fmt.Errorf("%w: chunk dependency %d checkpoint", ErrExecution, index), err)
		}
		if dependency.TaskID != task.ID || checkpoint.TaskID != task.ID || checkpoint.JobID != executor.plan.JobID ||
			checkpoint.PlanFingerprint != executor.plan.PlanFingerprint || checkpoint.ExecutionManifestFingerprint != executor.plan.ExecutionManifestFingerprint ||
			checkpoint.Stage != task.Stage || checkpoint.TaskProfile != task.Profile || checkpoint.CacheKey != task.CacheKey ||
			!rangesEqual(checkpoint.FrameRange, task.FrameRange) || !rangesEqual(checkpoint.SampleRange, task.SampleRange) || checkpoint.AttemptCount != 1 {
			return nil, fmt.Errorf("%w: chunk dependency %d binding", ErrExecution, index)
		}
		receipt, err := chunkplan.DecodeReceipt(checkpoint.Output.Receipt)
		if err != nil || chunkplan.ValidateReceiptForTask(task, receipt) != nil ||
			receipt.Output.ContentHash != checkpoint.Output.ContentHash || receipt.Output.SizeBytes != checkpoint.Output.SizeBytes ||
			receipt.AttemptCount != checkpoint.AttemptCount ||
			receipt.Worker.ExecutableDigest != executor.reference.executableDigest {
			return nil, errors.Join(fmt.Errorf("%w: chunk dependency %d receipt", ErrExecution, index), err)
		}
		if err := executor.service.verifyCAS(ctx, task, checkpoint); err != nil {
			return nil, err
		}
		descriptor := descriptorFor(task, receipt)
		if task.FrameRange != nil {
			videoReceipts = append(videoReceipts, receipt)
			videoDescriptors = append(videoDescriptors, descriptor)
		} else {
			audioReceipts = append(audioReceipts, receipt)
			audioDescriptors = append(audioDescriptors, descriptor)
		}
	}
	binding, err := aggregateReceipts(ctx, executor.request.Job, videoReceipts, audioReceipts)
	if err != nil {
		return nil, err
	}
	request, err := newChunkAssemblyRequest(executor.request, executor.reference.executableDigest, binding, audioDescriptors, videoDescriptors)
	if err != nil {
		return nil, err
	}
	return &validatedChunkAssembly{
		request: request,
		source:  newReferenceChunkSource(ctx, executor.service, appendDescriptors(audioDescriptors, videoDescriptors)),
	}, nil
}

func descriptorFor(task stagedag.Task, receipt chunkplan.Receipt) ChunkDescriptor {
	rangeValue := receipt.FrameRange
	if rangeValue == nil {
		rangeValue = receipt.SampleRange
	}
	return ChunkDescriptor{
		TaskID: task.ID, Domain: receipt.OutputProfile, CacheKey: task.CacheKey, Range: *rangeValue,
		ContentHash: receipt.Output.ContentHash, SizeBytes: receipt.Output.SizeBytes, UnitBytes: receipt.Output.UnitBytes,
	}
}

func appendDescriptors(audio, video []ChunkDescriptor) []ChunkDescriptor {
	result := make([]ChunkDescriptor, 0, len(audio)+len(video))
	result = append(result, audio...)
	result = append(result, video...)
	return result
}

func newChunkAssemblyRequest(request api.RenderRequest, providerDigest string, binding aggregateBinding,
	audio, video []ChunkDescriptor) (ChunkAssemblyRequest, error) {
	value := ChunkAssemblyRequest{
		Profile: ChunkAssemblyProfileV1, JobID: request.Job.ID, ArtifactURI: request.Job.ArtifactURI,
		PlanFingerprint: request.Job.PlanFingerprint, ExecutionManifestFingerprint: request.Job.ExecutionManifestFingerprint,
		TimelineRange: request.Job.TimelineRange, FrameRange: request.Job.FrameRange, SampleRange: request.Job.SampleRange,
		Target: request.Plan.Target, MediaRuntime: request.Manifest.MediaRuntime, Output: request.Manifest.Output,
		DeterministicSeed: request.Manifest.DeterministicSeed, ProviderExecutableDigest: providerDigest,
		Aggregate: cloneAggregateBinding(binding), Audio: append([]ChunkDescriptor{}, audio...), Video: append([]ChunkDescriptor{}, video...),
	}
	if err := validateChunkAssemblyRequest(value); err != nil {
		return ChunkAssemblyRequest{}, err
	}
	return value, nil
}

func validateChunkAssemblyRequest(value ChunkAssemblyRequest) error {
	if value.Profile != ChunkAssemblyProfileV1 || value.JobID == "" || value.ArtifactURI == "" ||
		!cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) ||
		!cachekey.CanonicalSHA256(value.ProviderExecutableDigest) || value.ProviderExecutableDigest == "" ||
		value.TimelineRange.Duration.Value <= 0 || value.TimelineRange.Validate() != nil ||
		value.FrameRange.Validate() != nil || value.SampleRange.Validate() != nil ||
		(value.FrameRange.Count == 0 && value.SampleRange.Count == 0) || value.Audio == nil || value.Video == nil {
		return fmt.Errorf("%w: invalid chunk assembly request header", ErrExecution)
	}
	if err := validateAggregateBinding(value.Aggregate); err != nil {
		return errors.Join(ErrExecution, err)
	}
	if err := validateAssemblyDomain(value.Audio, value.Aggregate.Audio, chunkplan.AudioOutputProfileV1, value.SampleRange); err != nil {
		return err
	}
	if err := validateAssemblyDomain(value.Video, value.Aggregate.Video, chunkplan.VideoOutputProfileV1, value.FrameRange); err != nil {
		return err
	}
	return nil
}

func validateAssemblyDomain(values []ChunkDescriptor, root *aggregateRoot, domain string, expected videoedit.TickRange) error {
	if expected.Count == 0 {
		if root != nil || len(values) != 0 {
			return fmt.Errorf("%w: inactive assembly domain %q is non-empty", ErrExecution, domain)
		}
		return nil
	}
	if root == nil || root.Domain != domain || root.Range != expected || root.ChunkCount != len(values) || len(values) == 0 {
		return fmt.Errorf("%w: assembly domain %q root/count/range", ErrExecution, domain)
	}
	next := expected.First
	prefix, chunkSize := "audio", chunkplan.SampleBlockCount
	if domain == chunkplan.VideoOutputProfileV1 {
		prefix, chunkSize = "video", chunkplan.FrameGroupCount
	}
	for index, value := range values {
		remaining := expected.Count - int64(index)*chunkSize
		wantCount := chunkSize
		if remaining < wantCount {
			wantCount = remaining
		}
		if value.Domain != domain || value.TaskID == "" || cachekey.ValidateKey(value.CacheKey) != nil ||
			!cachekey.CanonicalSHA256(value.ContentHash) || value.SizeBytes <= 0 || value.UnitBytes != root.UnitBytes ||
			value.TaskID != fmt.Sprintf("%s-%05d", prefix, index) || value.Range.First != next ||
			value.Range.Count != wantCount || value.Range.Validate() != nil ||
			value.Range.Count > math.MaxInt64/value.UnitBytes || value.Range.Count*value.UnitBytes != value.SizeBytes {
			return fmt.Errorf("%w: assembly domain %q descriptor %d", ErrExecution, domain, index)
		}
		end, _ := value.Range.EndExclusive()
		next = end
	}
	end, err := expected.EndExclusive()
	if err != nil || next != end {
		return errors.Join(fmt.Errorf("%w: assembly domain %q does not cover expected range", ErrExecution, domain), err)
	}
	return nil
}

func cloneAggregateBinding(value aggregateBinding) aggregateBinding {
	result := value
	if value.Video != nil {
		copy := *value.Video
		result.Video = &copy
	}
	if value.Audio != nil {
		copy := *value.Audio
		result.Audio = &copy
	}
	return result
}

func cloneChunkAssemblyRequest(value ChunkAssemblyRequest) ChunkAssemblyRequest {
	value.Aggregate = cloneAggregateBinding(value.Aggregate)
	value.Audio = append([]ChunkDescriptor{}, value.Audio...)
	value.Video = append([]ChunkDescriptor{}, value.Video...)
	return value
}

func aggregateReceipts(ctx context.Context, job api.RenderJob, videoReceipts, audioReceipts []chunkplan.Receipt) (aggregateBinding, error) {
	var videoSequence, audioSequence *evidence.DecodedSequence
	if job.FrameRange.Count > 0 {
		sequence, err := chunkplan.AggregateVideo(ctx, job.FrameRange, videoReceipts)
		if err != nil {
			return aggregateBinding{}, errors.Join(ErrExecution, err)
		}
		videoSequence = &sequence
	}
	if job.SampleRange.Count > 0 {
		sequence, err := chunkplan.AggregateAudio(ctx, job.SampleRange, audioReceipts)
		if err != nil {
			return aggregateBinding{}, errors.Join(ErrExecution, err)
		}
		audioSequence = &sequence
	}
	return bindingFromSequences(videoSequence, audioSequence), nil
}

func receiptsFromCheckpoints(plan chunkplan.Plan, checkpoints []stagedag.Checkpoint, executableDigest string) (
	videoReceipts, audioReceipts []chunkplan.Receipt,
	videoDescriptors, audioDescriptors []ChunkDescriptor,
	returnErr error,
) {
	if len(checkpoints) != len(plan.Tasks) {
		return nil, nil, nil, nil, fmt.Errorf("%w: checkpoint count does not match chunk plan", ErrArtifact)
	}
	videoReceipts = make([]chunkplan.Receipt, 0)
	audioReceipts = make([]chunkplan.Receipt, 0)
	videoDescriptors = make([]ChunkDescriptor, 0)
	audioDescriptors = make([]ChunkDescriptor, 0)
	for index, task := range plan.Tasks {
		checkpoint := checkpoints[index]
		receipt, err := chunkplan.DecodeReceipt(checkpoint.Output.Receipt)
		if err != nil || chunkplan.ValidateReceiptForTask(task, receipt) != nil ||
			receipt.Output.ContentHash != checkpoint.Output.ContentHash ||
			receipt.Output.SizeBytes != checkpoint.Output.SizeBytes ||
			receipt.AttemptCount != checkpoint.AttemptCount ||
			receipt.Worker.ExecutableDigest != executableDigest {
			return nil, nil, nil, nil, errors.Join(fmt.Errorf("%w: invalid chunk checkpoint %d", ErrArtifact, index), err)
		}
		descriptor := descriptorFor(task, receipt)
		if task.FrameRange != nil {
			videoReceipts = append(videoReceipts, receipt)
			videoDescriptors = append(videoDescriptors, descriptor)
		} else {
			audioReceipts = append(audioReceipts, receipt)
			audioDescriptors = append(audioDescriptors, descriptor)
		}
	}
	return videoReceipts, audioReceipts, videoDescriptors, audioDescriptors, nil
}

// SelectMissingChunks exposes the exact chunkplan recovery selector used by
// artifact verification. It does not include the final publish checkpoint:
// codec failure recovery deliberately restarts the complete encode.
func (service *Service) SelectMissingChunks(ctx context.Context, job api.RenderJob, candidates []stagedag.Checkpoint) (chunkplan.ResumeSelection, error) {
	if service == nil || service.reference == nil || ctx == nil {
		return chunkplan.ResumeSelection{}, ErrConfiguration
	}
	_, request, err := service.resolveRequest(ctx, job)
	if err != nil {
		return chunkplan.ResumeSelection{}, err
	}
	plan, err := service.buildReferenceChunkPlan(ctx, request.Job)
	if err != nil {
		return chunkplan.ResumeSelection{}, err
	}
	return service.selectReferenceChunks(ctx, plan, candidates)
}

// selectReferenceChunks makes provider provenance an admission condition, not
// merely metadata. chunkplan intentionally accepts any canonical worker digest;
// this profile rewrites a well-formed but foreign-digest receipt to a canonical
// invalid receipt before selection. It therefore becomes Missing/Rejected and
// is never queried against CAS or returned Ready. Caller checkpoints are not
// mutated.
func (service *Service) selectReferenceChunks(ctx context.Context, plan chunkplan.Plan, candidates []stagedag.Checkpoint) (chunkplan.ResumeSelection, error) {
	if service == nil || service.reference == nil || ctx == nil {
		return chunkplan.ResumeSelection{}, ErrConfiguration
	}
	if len(plan.Tasks) == 0 || len(plan.Tasks) > ReferenceChunkMaxTasks || len(candidates) > len(plan.Tasks) {
		return chunkplan.ResumeSelection{}, fmt.Errorf("%w: reference candidates %d exceed exact chunk plan size %d", ErrArtifact, len(candidates), len(plan.Tasks))
	}
	requiredMemory, ok := referenceFinalValidationMemory(len(plan.Tasks))
	if !ok || service.memoryBytes < requiredMemory {
		return chunkplan.ResumeSelection{}, fmt.Errorf("%w: reference selection requires memory=%d, declared=%d", ErrArtifact, requiredMemory, service.memoryBytes)
	}
	var receiptBytes uint64
	for index := range candidates {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return chunkplan.ResumeSelection{}, err
			}
		}
		size := uint64(len(candidates[index].Output.Receipt))
		if receiptBytes > ^uint64(0)-size {
			return chunkplan.ResumeSelection{}, fmt.Errorf("%w: reference candidate receipt bytes overflow", ErrArtifact)
		}
		receiptBytes += size
	}
	if receiptBytes > requiredMemory || receiptBytes > service.memoryBytes {
		return chunkplan.ResumeSelection{}, fmt.Errorf("%w: reference candidate receipts require %d bytes beyond admitted memory", ErrArtifact, receiptBytes)
	}
	owned := make([]stagedag.Checkpoint, len(candidates))
	copy(owned, candidates)
	for index := range owned {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return chunkplan.ResumeSelection{}, err
			}
		}
		if len(candidates[index].Output.Receipt) > chunkplan.MaxReceiptBytes {
			owned[index].Output.Receipt = []byte("{}")
			continue
		}
		owned[index].Output.Receipt = append([]byte(nil), candidates[index].Output.Receipt...)
		receipt, err := chunkplan.DecodeReceipt(owned[index].Output.Receipt)
		if err == nil && receipt.Worker.ExecutableDigest != service.reference.executableDigest {
			owned[index].Output.Receipt = []byte("{}")
		}
	}
	return chunkplan.SelectMissing(ctx, plan, owned, chunkAvailability{service: service})
}

type chunkAvailability struct{ service *Service }

func (availability chunkAvailability) Available(ctx context.Context, key cachekey.Key, contentHash string, sizeBytes int64) (bool, error) {
	reader, err := safeCASOpen(ctx, availability.service.cas, key, "backendstage-chunk-select-"+strings.TrimPrefix(key.Digest, "sha256:")[:24])
	if err != nil {
		if errors.Is(err, cas.ErrMiss) || errors.Is(err, cas.ErrCorrupt) || errors.Is(err, cas.ErrRecomputeRequired) {
			return false, nil
		}
		return false, err
	}
	if reader == nil {
		return false, fmt.Errorf("%w: CAS returned nil chunk reader", ErrArtifact)
	}
	valid := reader.Entry.Key == key && reader.Entry.PayloadHash == contentHash && reader.Entry.PayloadSize == sizeBytes
	if err := reader.Close(); err != nil {
		return false, err
	}
	return valid, nil
}

func rangesEqual(left, right *videoedit.TickRange) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func exactReferenceBinding(left, right aggregateBinding) bool { return reflect.DeepEqual(left, right) }
