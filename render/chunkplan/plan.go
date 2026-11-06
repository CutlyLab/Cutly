package chunkplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	// These limits intentionally mirror the scheduler v1 admission boundary.
	// They are repeated here to keep the lower-level planner independent of the
	// scheduler package while preventing programmatic callers from forcing an
	// unbounded json.Marshal allocation before RenderJob validation.
	maxJobIDBytes          = 1 << 10
	maxIdempotencyKeyBytes = 4 << 10
	maxArtifactURIBytes    = 16 << 10
)

// Plan is the chunkplan-owned recovery contract. The embedded stage plan is
// accompanied by the exact frozen RenderJob whose frame and sample ranges it
// must completely partition. SelectMissing accepts this type, rather than a
// generic stagedag.Plan, so a valid but truncated or re-partitioned stage plan
// cannot silently redefine the work that recovery considers complete.
type Plan struct {
	stagedag.Plan
	Job renderapi.RenderJob
}

// Build validates and freezes job, admits the complete task count before
// allocating proportional state, then builds one canonical chunk plan.
func Build(ctx context.Context, job renderapi.RenderJob, config Config) (Plan, error) {
	if ctx == nil {
		return Plan{}, fmt.Errorf("%w: nil context", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	frozenJob, err := freezeJob(job)
	if err != nil {
		return Plan{}, err
	}
	config, err = normalizeConfig(config, frozenJob)
	if err != nil {
		return Plan{}, err
	}
	videoCount, err := chunkCount(frozenJob.FrameRange.Count, FrameGroupCount)
	if err != nil {
		return Plan{}, err
	}
	audioCount, err := chunkCount(frozenJob.SampleRange.Count, SampleBlockCount)
	if err != nil {
		return Plan{}, err
	}
	total := videoCount + audioCount
	if total > uint64(config.MaxTasks) || total > uint64(stagedag.MaxTasks) {
		return Plan{}, fmt.Errorf("%w: %d tasks exceed limit %d", ErrLimit, total, config.MaxTasks)
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	tasks := make([]stagedag.Task, 0, int(total))
	// IDs are intentionally domain-prefixed and zero-padded, so audio tasks
	// precede video tasks in stagedag's required lexical order.
	audioTasks, err := buildDomainTasks(ctx, frozenJob, domainAudio, int(audioCount), config.AudioResources, config.Retry)
	if err != nil {
		return Plan{}, err
	}
	videoTasks, err := buildDomainTasks(ctx, frozenJob, domainVideo, int(videoCount), config.VideoResources, config.Retry)
	if err != nil {
		return Plan{}, err
	}
	tasks = append(tasks, audioTasks...)
	tasks = append(tasks, videoTasks...)
	plan := stagedag.Plan{
		Profile: stagedag.PlanProfileV1, JobID: frozenJob.ID,
		PlanFingerprint:              frozenJob.PlanFingerprint,
		ExecutionManifestFingerprint: frozenJob.ExecutionManifestFingerprint,
		Tasks:                        tasks,
	}
	if err := stagedag.ValidatePlan(plan); err != nil {
		return Plan{}, errors.Join(ErrJob, err)
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	raw, err := stagedag.CanonicalPlan(plan)
	if err != nil {
		return Plan{}, errors.Join(ErrJob, err)
	}
	frozen, err := stagedag.DecodePlan(raw)
	if err != nil {
		return Plan{}, errors.Join(ErrJob, err)
	}
	if err := validatePlanContract(frozenJob, frozen); err != nil {
		return Plan{}, err
	}
	return Plan{Plan: frozen, Job: frozenJob}, nil
}

type chunkDomain uint8

const (
	domainVideo chunkDomain = iota + 1
	domainAudio
)

type domainProfile struct {
	idPrefix, stageProfile, namespace, artifactKind, outputProfile string
	stage                                                          renderapi.Stage
	chunkSize                                                      int64
}

func profileFor(domain chunkDomain) domainProfile {
	if domain == domainVideo {
		return domainProfile{
			idPrefix: "video", stageProfile: VideoStageProfileV1, namespace: VideoCacheNamespaceV1,
			artifactKind: VideoArtifactKindV1, outputProfile: VideoOutputProfileV1,
			stage: renderapi.StageCompose, chunkSize: FrameGroupCount,
		}
	}
	return domainProfile{
		idPrefix: "audio", stageProfile: AudioStageProfileV1, namespace: AudioCacheNamespaceV1,
		artifactKind: AudioArtifactKindV1, outputProfile: AudioOutputProfileV1,
		stage: renderapi.StageAudio, chunkSize: SampleBlockCount,
	}
}

func buildDomainTasks(ctx context.Context, job renderapi.RenderJob, domain chunkDomain, count int,
	resources stagedag.ResourceEstimate, retry stagedag.RetryPolicy) ([]stagedag.Task, error) {
	if count == 0 {
		return []stagedag.Task{}, nil
	}
	profile := profileFor(domain)
	base := job.FrameRange
	if domain == domainAudio {
		base = job.SampleRange
	}
	result := make([]stagedag.Task, 0, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		if ordinal&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		offset := int64(ordinal) * profile.chunkSize
		remaining := base.Count - offset
		chunkSize := profile.chunkSize
		if remaining < chunkSize {
			chunkSize = remaining
		}
		rangeValue := videoedit.TickRange{First: base.First + offset, Count: chunkSize}
		task, err := makeTask(job, domain, ordinal, rangeValue, resources, retry)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, nil
}

func makeTask(job renderapi.RenderJob, domain chunkDomain, ordinal int, rangeValue videoedit.TickRange,
	resources stagedag.ResourceEstimate, retry stagedag.RetryPolicy) (stagedag.Task, error) {
	profile := profileFor(domain)
	stageBinding, err := stagedag.StageBindingInput(profile.stage)
	if err != nil {
		return stagedag.Task{}, err
	}
	cacheRange := &cachekey.IndexRange{Start: rangeValue.First, Count: rangeValue.Count}
	input := cachekey.Input{
		SchemaVersion: cachekey.SchemaVersion, Profile: cachekey.ProfileV1,
		Namespace: profile.namespace, ArtifactKind: profile.artifactKind,
		PlanFingerprint: job.PlanFingerprint, ExecutionManifestFingerprint: job.ExecutionManifestFingerprint,
		StageProfile: profile.stageProfile, Inputs: []cachekey.NormalizedInput{stageBinding},
		OutputProfile: profile.outputProfile,
	}
	task := stagedag.Task{
		ID: fmt.Sprintf("%s-%05d", profile.idPrefix, ordinal), Stage: profile.stage, Profile: profile.stageProfile,
		DependsOn: []string{}, Resources: resources, Retry: retry,
	}
	if domain == domainVideo {
		input.FrameRange = cacheRange
		task.FrameRange = &rangeValue
	} else {
		input.SampleRange = cacheRange
		task.SampleRange = &rangeValue
	}
	key, err := cachekey.New(input)
	if err != nil {
		return stagedag.Task{}, errors.Join(ErrJob, err)
	}
	task.CacheInput, task.CacheKey = input, key
	return task, nil
}

func freezeJob(job renderapi.RenderJob) (renderapi.RenderJob, error) {
	if err := validateJobMarshalAdmission(job); err != nil {
		return renderapi.RenderJob{}, err
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return renderapi.RenderJob{}, errors.Join(ErrJob, err)
	}
	frozen, err := renderapi.DecodeRenderJob(raw)
	if err != nil {
		return renderapi.RenderJob{}, errors.Join(ErrJob, err)
	}
	if frozen.FrameRange.Count > 0 && frozen.FrameRange.First < 0 ||
		frozen.SampleRange.Count > 0 && frozen.SampleRange.First < 0 {
		return renderapi.RenderJob{}, fmt.Errorf("%w: negative composition indices are not representable by cache key v1", ErrJob)
	}
	return frozen, nil
}

func validateJobMarshalAdmission(job renderapi.RenderJob) error {
	if job.SchemaVersion != renderapi.RenderJobSchemaVersion ||
		!boundedUTF8(job.ID, maxJobIDBytes) ||
		!boundedUTF8(job.IdempotencyKey, maxIdempotencyKeyBytes) ||
		!boundedUTF8(job.ArtifactURI, maxArtifactURIBytes) ||
		!cachekey.CanonicalSHA256(job.PlanFingerprint) ||
		!cachekey.CanonicalSHA256(job.ExecutionManifestFingerprint) {
		return fmt.Errorf("%w: job exceeds fixed marshal admission limits", ErrJob)
	}
	if err := job.TimelineRange.Validate(); err != nil || job.TimelineRange.Duration.Value <= 0 {
		return fmt.Errorf("%w: invalid timeline range", ErrJob)
	}
	if err := job.FrameRange.Validate(); err != nil {
		return errors.Join(ErrJob, err)
	}
	if err := job.SampleRange.Validate(); err != nil {
		return errors.Join(ErrJob, err)
	}
	if job.FrameRange.Count == 0 && job.SampleRange.Count == 0 {
		return fmt.Errorf("%w: job has no work", ErrJob)
	}
	if job.FrameRange.Count > 0 && job.FrameRange.First < 0 ||
		job.SampleRange.Count > 0 && job.SampleRange.First < 0 {
		return fmt.Errorf("%w: negative composition indices are not representable by cache key v1", ErrJob)
	}
	return nil
}

func boundedUTF8(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value)
}

// validatePlanContract proves the fixed partition directly from the frozen
// job. stagedag validation establishes cache/plan binding; this second layer
// establishes ordinal-to-range meaning and complete domain coverage.
func validatePlanContract(job renderapi.RenderJob, plan stagedag.Plan) error {
	if plan.JobID != job.ID || plan.PlanFingerprint != job.PlanFingerprint ||
		plan.ExecutionManifestFingerprint != job.ExecutionManifestFingerprint {
		return fmt.Errorf("%w: stage plan does not belong to its frozen job", ErrResume)
	}
	audioCount, err := chunkCount(job.SampleRange.Count, SampleBlockCount)
	if err != nil {
		return errors.Join(ErrResume, err)
	}
	videoCount, err := chunkCount(job.FrameRange.Count, FrameGroupCount)
	if err != nil {
		return errors.Join(ErrResume, err)
	}
	total := audioCount + videoCount
	if total > stagedag.MaxTasks || uint64(len(plan.Tasks)) != total {
		return fmt.Errorf("%w: stage plan does not completely cover the job", ErrResume)
	}
	index := 0
	for ordinal := uint64(0); ordinal < audioCount; ordinal++ {
		if err := validatePartitionTask(plan.Tasks[index], domainAudio, ordinal, job.SampleRange); err != nil {
			return err
		}
		index++
	}
	for ordinal := uint64(0); ordinal < videoCount; ordinal++ {
		if err := validatePartitionTask(plan.Tasks[index], domainVideo, ordinal, job.FrameRange); err != nil {
			return err
		}
		index++
	}
	return nil
}

func validatePartitionTask(task stagedag.Task, domain chunkDomain, ordinal uint64,
	complete videoedit.TickRange) error {
	profile := profileFor(domain)
	if ordinal > uint64(stagedag.MaxTasks-1) || task.ID != fmt.Sprintf("%s-%05d", profile.idPrefix, ordinal) {
		return fmt.Errorf("%w: chunk task ordinal identity", ErrResume)
	}
	actualDomain, err := validateChunkTask(task)
	if err != nil || actualDomain != domain {
		return errors.Join(ErrResume, err)
	}
	offset := int64(ordinal) * profile.chunkSize
	remaining := complete.Count - offset
	wantCount := profile.chunkSize
	if remaining < wantCount {
		wantCount = remaining
	}
	want := videoedit.TickRange{First: complete.First + offset, Count: wantCount}
	actual := task.FrameRange
	if domain == domainAudio {
		actual = task.SampleRange
	}
	if actual == nil || *actual != want {
		return fmt.Errorf("%w: task %q does not match fixed partition", ErrResume, task.ID)
	}
	return nil
}

func normalizeConfig(value Config, job renderapi.RenderJob) (Config, error) {
	if value.MaxTasks <= 0 || value.MaxTasks > stagedag.MaxTasks || value.Retry.MaxAttempts == 0 ||
		value.Retry.MaxAttempts > stagedag.MaxAttempts {
		return Config{}, ErrConfiguration
	}
	if job.FrameRange.Count > 0 && (value.VideoResources.MemoryBytes == 0 || value.VideoResources.DiskBytes == 0) {
		return Config{}, fmt.Errorf("%w: positive video memory and disk estimates are required", ErrConfiguration)
	}
	if job.SampleRange.Count > 0 && (value.AudioResources.MemoryBytes == 0 || value.AudioResources.DiskBytes == 0) {
		return Config{}, fmt.Errorf("%w: positive audio memory and disk estimates are required", ErrConfiguration)
	}
	return value, nil
}

func chunkCount(count, size int64) (uint64, error) {
	if count < 0 || size <= 0 {
		return 0, ErrJob
	}
	if count == 0 {
		return 0, nil
	}
	return uint64((count-1)/size + 1), nil
}
