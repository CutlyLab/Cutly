package backendstage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/chunkcachev2"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
	"github.com/CutlyLab/Cutly/render/worker"
)

func (s *ServiceV2) buildPlanV2(ctx context.Context, j api.RenderJob, v prepared.VerifiedEnvironmentV2, e prepared.EnvironmentV2) (stagedag.Plan, error) {
	p := stagedag.Plan{Profile: stagedag.PlanProfileV1, JobID: j.ID, PlanFingerprint: j.PlanFingerprint, ExecutionManifestFingerprint: j.ExecutionManifestFingerprint, Tasks: []stagedag.Task{}}
	// Admit total descriptor count and disk before allocating a task list or starting workers.
	count := func(n, unit int64) int64 {
		if n == 0 {
			return 0
		}
		return 1 + (n-1)/unit
	}
	n := count(j.FrameRange.Count, worker.FrameGroupCountV2) + count(j.SampleRange.Count, worker.SampleBlockCountV2)
	if n <= 0 || n > stagedag.MaxDependencies-2 {
		return p, fmt.Errorf("%w: native chunk count", ErrPlan)
	}
	if s.config.MemoryBytes < 256<<20+uint64(n)*(16<<10) {
		return p, fmt.Errorf("%w: native validation/worker memory admission", ErrPlan)
	}
	var disk uint64
	for _, domain := range []string{"audio", "video"} {
		span, partition, profile, stage := j.SampleRange, worker.SampleBlockCountV2, api.AudioBlockProfileV2, api.StageAudio
		if domain == "video" {
			span, partition, profile, stage = j.FrameRange, worker.FrameGroupCountV2, api.VideoSurfaceProfileV2, api.StageCompose
		}
		for ordinal, first, left := 0, span.First, span.Count; left > 0; ordinal++ {
			r := videoedit.TickRange{First: first, Count: min(partition, left)}
			identity, err := chunkcachev2.NewIdentity(ctx, v, profile, r)
			if err != nil {
				return p, err
			}
			if identity.UnitBytes <= 0 || r.Count > worker.MaxStreamPayloadBytesV2/identity.UnitBytes {
				return p, fmt.Errorf("%w: native partition exceeds worker payload bound", ErrPlan)
			}
			size := uint64(identity.UnitBytes * r.Count)
			if disk > ^uint64(0)-size {
				return p, ErrPlan
			}
			disk += size
			input, err := chunkcachev2.Input(identity)
			if err != nil {
				return p, err
			}
			key, err := cachekey.New(input)
			if err != nil {
				return p, err
			}
			t := stagedag.Task{ID: fmt.Sprintf("%s-%05d", domain, ordinal), Stage: stage, Profile: input.StageProfile, DependsOn: []string{}, CacheInput: input, CacheKey: key, Resources: stagedag.ResourceEstimate{MemoryBytes: s.config.MemoryBytes, DiskBytes: s.config.DiskBytes}, Retry: stagedag.RetryPolicy{MaxAttempts: 3, RetryTransient: true, RetryResource: true}}
			if domain == "video" {
				t.FrameRange = &r
			} else {
				t.SampleRange = &r
			}
			_ = profile
			p.Tasks = append(p.Tasks, t)
			first += r.Count
			left -= r.Count
		}
	}
	// All intermediate chunks remain durable. Leave bounded publication/CAS scratch
	// headroom in addition to them; encoder is independently capped by the host.
	if disk > s.config.DiskBytes/3 || s.config.DiskBytes-disk < 1<<20 {
		return p, fmt.Errorf("%w: native durable chunks and publication disk admission", ErrPlan)
	}
	inputs := make([]cachekey.NormalizedInput, 0, len(p.Tasks)+2)
	deps := make([]string, len(p.Tasks))
	for i, t := range p.Tasks {
		deps[i] = t.ID
		b, err := stagedag.DependencyBindingInput(i, t.CacheKey)
		if err != nil {
			return p, err
		}
		inputs = append(inputs, b)
	}
	stage, _ := stagedag.StageBindingInput(api.StagePublish)
	timeline, err := timelineRangeBindingInput(j.TimelineRange)
	if err != nil {
		return p, err
	}
	inputs = append(inputs, stage, timeline)
	input := cachekey.Input{SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1, Namespace: "cutly.cache.chunk-publish.document-v2.v2", ArtifactKind: "cutly.artifact.encoded-media.document-v2.v2", PlanFingerprint: j.PlanFingerprint, ExecutionManifestFingerprint: j.ExecutionManifestFingerprint, StageProfile: PublishStageProfileV2, OutputProfile: api.OutputReferenceProfileV2, Inputs: inputs}
	t := stagedag.Task{ID: PublishTaskIDV2, Stage: api.StagePublish, Profile: PublishStageProfileV2, DependsOn: deps, Resources: stagedag.ResourceEstimate{MemoryBytes: s.config.MemoryBytes, DiskBytes: s.config.DiskBytes}, Retry: stagedag.RetryPolicy{MaxAttempts: 1}}
	if j.FrameRange.Count > 0 {
		r := j.FrameRange
		t.FrameRange = &r
		input.FrameRange = &cachekey.IndexRange{Start: r.First, Count: r.Count}
	}
	if j.SampleRange.Count > 0 {
		r := j.SampleRange
		t.SampleRange = &r
		input.SampleRange = &cachekey.IndexRange{Start: r.First, Count: r.Count}
	}
	t.CacheInput = input
	t.CacheKey, err = cachekey.New(input)
	if err != nil {
		return p, err
	}
	p.Tasks = append(p.Tasks, t)
	return p, stagedag.ValidatePlan(p)
}
func (x *executorV2) identityV2(ctx context.Context, t stagedag.Task) (chunkcachev2.Identity, error) {
	domain, r := api.AudioBlockProfileV2, t.SampleRange
	if t.FrameRange != nil {
		domain, r = api.VideoSurfaceProfileV2, t.FrameRange
	}
	if r == nil {
		return chunkcachev2.Identity{}, ErrExecution
	}
	return chunkcachev2.NewIdentity(ctx, x.verified, domain, *r)
}
func chunkOutputV2(h *chunkcachev2.Handle) (stagedag.StageOutput, error) {
	r := h.Receipt()
	// Lookup pins a stream, not merely metadata. Fully consume it to prove bytes
	// before accepting a warm hit or closing its actual CAS reader lease.
	if n, err := io.Copy(io.Discard, h); err != nil || n != r.SizeBytes {
		return stagedag.StageOutput{}, errors.Join(ErrArtifact, err)
	}
	// Current-job checkpoint identity is deterministic and job-neutral. Historical
	// worker invocation/attempt provenance remains in the independently verified
	// durable cache receipt, never borrowed as the current invocation identity.
	raw, err := canonicaljson.Marshal(struct {
		Profile     string                `json:"profile"`
		Identity    chunkcachev2.Identity `json:"identity"`
		ContentHash string                `json:"content_hash"`
		SizeBytes   int64                 `json:"size_bytes"`
	}{ServiceProfileV2, r.Identity, r.ContentHash, r.SizeBytes})
	return stagedag.StageOutput{ContentHash: r.ContentHash, SizeBytes: r.SizeBytes, Receipt: raw}, err
}
func (x *executorV2) chunkV2(ctx context.Context, t stagedag.Task, attempt uint8) (output stagedag.StageOutput, returnErr error) {
	identity, err := x.identityV2(ctx, t)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	h, err := x.service.config.Chunks.Lookup(ctx, identity)
	if err == nil {
		out, e := chunkOutputV2(h)
		return out, errors.Join(e, h.Close())
	}
	if !errors.Is(err, chunkcachev2.ErrMiss) {
		return stagedag.StageOutput{}, err
	}
	invocation, err := newInvocationID()
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	domain := "audio"
	if identity.Domain == api.VideoSurfaceProfileV2 {
		domain = "video"
	}
	request := worker.RequestV2{SchemaVersion: 2, Profile: worker.RequestProfileV2, InvocationID: invocation, Attempt: uint32(attempt), ProviderExecutableDigest: x.environment.Manifest.ProviderBuild, Environment: x.environment, Chunk: worker.ChunkV2{Domain: domain, Range: identity.Range, IntermediateProfile: identity.Domain}}
	result, _, err := x.service.config.Worker.RunStream(ctx, request)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	if result == nil {
		return stagedag.StageOutput{}, ErrExecution
	}
	defer func() {
		returnErr = errors.Join(returnErr, result.Close())
		if returnErr != nil {
			output = stagedag.StageOutput{}
		}
	}()
	if !result.OK {
		return stagedag.StageOutput{}, &stagedag.StageError{Class: result.FailureClass, Err: errors.New(result.Message)}
	}
	if result.Receipt == nil || result.Payload == nil {
		return stagedag.StageOutput{}, ErrExecution
	}
	receipt, err := chunkcachev2.NewReceipt(identity, result.Receipt.ContentHash, result.Receipt.SizeBytes, invocation, attempt)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	if err = x.service.config.Chunks.Publish(ctx, identity, receipt, result.Payload); err != nil {
		return stagedag.StageOutput{}, err
	}
	h, err = x.service.config.Chunks.Lookup(ctx, identity)
	if err != nil {
		return stagedag.StageOutput{}, err
	}
	out, err := chunkOutputV2(h)
	return out, errors.Join(err, h.Close())
}
func descriptorV2(task stagedag.Task, identity chunkcachev2.Identity, receipt chunkcachev2.Receipt) ChunkDescriptorV2 {
	return ChunkDescriptorV2{TaskID: task.ID, Domain: func() string {
		if identity.Domain == api.VideoSurfaceProfileV2 {
			return api.VideoSurfaceProfileV2
		}
		return api.AudioBlockProfileV2
	}(), CacheKey: task.CacheKey, ContentHash: receipt.ContentHash, SizeBytes: receipt.SizeBytes, Range: identity.Range, UnitBytes: identity.UnitBytes}
}
func (x *executorV2) assemblyV2(ctx context.Context, cps []stagedag.Checkpoint) (VerifiedChunkAssemblyRequestV2, error) {
	if len(cps) != len(x.plan.Tasks)-1 {
		return VerifiedChunkAssemblyRequestV2{}, ErrExecution
	}
	audio, video := []ChunkDescriptorV2{}, []ChunkDescriptorV2{}
	for i, cp := range cps {
		t := x.plan.Tasks[i]
		if err := stagedag.ValidateCheckpoint(x.plan, t, cp); err != nil {
			return VerifiedChunkAssemblyRequestV2{}, err
		}
		identity, err := x.identityV2(ctx, t)
		if err != nil {
			return VerifiedChunkAssemblyRequestV2{}, err
		}
		h, err := x.service.config.Chunks.Lookup(ctx, identity)
		if err != nil {
			return VerifiedChunkAssemblyRequestV2{}, err
		}
		receipt := h.Receipt()
		out, outErr := chunkOutputV2(h)
		closeErr := h.Close()
		if outErr != nil || closeErr != nil || !reflect.DeepEqual(out, cp.Output) {
			return VerifiedChunkAssemblyRequestV2{}, errors.Join(ErrArtifact, outErr, closeErr)
		}
		d := descriptorV2(t, identity, receipt)
		if identity.Domain == api.VideoSurfaceProfileV2 {
			video = append(video, d)
		} else {
			audio = append(audio, d)
		}
	}
	root := func(ds []ChunkDescriptorV2, span videoedit.TickRange, domain string, unit evidence.EvidenceUnit) (*ChunkAggregateRootV2, error) {
		if len(ds) == 0 {
			return nil, nil
		}
		seq := evidence.DecodedSequence{Domain: domain, Unit: unit, UnitBytes: ds[0].UnitBytes, Range: span, Chunks: make([]evidence.DecodedChunk, len(ds))}
		var size int64
		for i, d := range ds {
			seq.Chunks[i] = evidence.DecodedChunk{First: d.Range.First, Count: d.Range.Count, ContentHash: d.ContentHash}
			size += d.SizeBytes
		}
		hash, err := evidence.ComputeDecodedRoot(seq)
		if err != nil {
			return nil, err
		}
		return &ChunkAggregateRootV2{Domain: domain, Range: span, UnitBytes: ds[0].UnitBytes, ChunkCount: len(ds), RootHash: hash}, nil
	}
	vr, err := root(video, x.job.FrameRange, api.VideoSurfaceProfileV2, evidence.EvidenceFrames)
	if err != nil {
		return VerifiedChunkAssemblyRequestV2{}, err
	}
	ar, err := root(audio, x.job.SampleRange, api.AudioBlockProfileV2, evidence.EvidenceSamples)
	if err != nil {
		return VerifiedChunkAssemblyRequestV2{}, err
	}
	draft := ChunkAssemblyRequestV2{DocumentHash: x.environment.Plan.DocumentHash, PlanFingerprint: x.environment.Plan.Fingerprint, ExecutionManifestFingerprint: x.environment.Manifest.Fingerprint, Target: x.environment.Plan.Target, MediaRuntime: x.environment.Manifest.MediaRuntime, Output: x.environment.Manifest.Output, DeterministicSeed: x.environment.Manifest.DeterministicSeed, ProviderExecutableDigest: x.environment.Manifest.ProviderBuild, JobID: x.job.ID, ArtifactURI: x.job.ArtifactURI, TimelineRange: x.job.TimelineRange, FrameRange: x.job.FrameRange, SampleRange: x.job.SampleRange, Aggregate: ChunkAggregateBindingV2{Profile: ChunkAggregateBindingProfileV2, ResumeMode: ChunkAssemblyResumeModeV2, Video: vr, Audio: ar}, Audio: audio, Video: video}
	request, err := FreezeChunkAssemblyRequestV2(ctx, x.environment.Plan, x.environment.Manifest, draft)
	if err != nil {
		return VerifiedChunkAssemblyRequestV2{}, err
	}
	return VerifyChunkAssemblyRequestV2AgainstPlan(ctx, request, x.environment.Manifest, x.environment.Plan)
}
func (x *executorV2) publishV2(ctx context.Context, t stagedag.Task, cps []stagedag.Checkpoint) (output stagedag.StageOutput, returnErr error) {
	verified, err := x.assemblyV2(ctx, cps)
	if err != nil {
		return output, err
	}
	request, err := verified.Request()
	if err != nil {
		return output, err
	}
	// Reuse only the job-neutral CAS streaming verifier, never the legacy provider.
	descriptors := append(append([]ChunkDescriptor{}, request.Audio...), request.Video...)
	source := newReferenceChunkSource(ctx, &Service{cas: x.service.config.CAS}, descriptors)
	defer func() {
		returnErr = errors.Join(returnErr, source.VerifyComplete())
		if returnErr != nil {
			output = stagedag.StageOutput{}
		}
	}()
	artifact, err := x.assembler.AssembleChunks(ctx, verified, source, x.service.config.Progress)
	if err != nil {
		return output, err
	}
	if err = source.VerifyComplete(); err != nil {
		return output, err
	}
	if err = ctx.Err(); err != nil {
		return output, err
	}
	actual, found, err := x.assembler.InspectChunkAssembly(ctx, verified)
	if err != nil || !found || !reflect.DeepEqual(artifact, actual) {
		return output, errors.Join(ErrArtifact, err)
	}
	if artifact.URI != x.job.ArtifactURI || artifact.ExecutionManifestFingerprint != x.job.ExecutionManifestFingerprint || artifact.FrameCount != x.job.FrameRange.Count || artifact.SampleCount != x.job.SampleRange.Count || artifact.Duration != x.job.TimelineRange.Duration {
		return output, ErrArtifact
	}
	disk, ok := minimumPublishDisk(artifact.SizeBytes)
	if !ok || disk > t.Resources.DiskBytes {
		return output, ErrPlan
	}
	published, found, err := x.assembler.OpenChunkAssembly(ctx, verified)
	if err != nil || !found || nilLike(published) {
		return output, errors.Join(ErrArtifact, err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, published.Close())
		if returnErr != nil {
			output = stagedag.StageOutput{}
		}
	}()
	if !reflect.DeepEqual(published.Artifact(), artifact) {
		return output, ErrArtifact
	}
	if pos, err := published.Seek(0, io.SeekStart); err != nil || pos != 0 {
		return output, errors.Join(ErrArtifact, err)
	}
	result, err := x.service.config.CAS.Publish(ctx, cas.PublishRequest{Key: t.CacheKey, Payload: published, ExpectedHash: artifact.ContentHash, ExpectedSize: artifact.SizeBytes})
	if err != nil {
		return output, err
	}
	if result.Entry.PayloadHash != artifact.ContentHash || result.Entry.PayloadSize != artifact.SizeBytes {
		return output, ErrArtifact
	}
	raw, err := canonicaljson.Marshal(publishReceiptV2{Profile: PublishStageProfileV2, RequestFingerprint: verified.Fingerprint(), Artifact: artifact})
	return stagedag.StageOutput{ContentHash: artifact.ContentHash, SizeBytes: artifact.SizeBytes, Receipt: raw}, err
}
