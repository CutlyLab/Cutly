package renderapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const RenderJobSchemaVersion = 1

var (
	ErrRenderJobInvalid     = errors.New("render: job is invalid")
	ErrRenderRequestInvalid = errors.New("render: request is invalid")
)

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobCanceling JobState = "canceling"
	JobCanceled  JobState = "canceled"
	JobFailed    JobState = "failed"
	JobComplete  JobState = "complete"
)

type Stage string

const (
	StageResolve     Stage = "resolve"
	StageMaterialize Stage = "materialize"
	StageProbe       Stage = "probe"
	StageDecode      Stage = "decode"
	StageCompose     Stage = "compose"
	StageAudio       Stage = "audio"
	StageEncode      Stage = "encode"
	StageMux         Stage = "mux"
	StageVerify      Stage = "verify"
	StagePublish     Stage = "publish"
)

type ProgressUnit string

const (
	ProgressFrames  ProgressUnit = "frames"
	ProgressSamples ProgressUnit = "samples"
	ProgressBytes   ProgressUnit = "bytes"
	ProgressItems   ProgressUnit = "items"
)

type FailureClass string

const (
	FailureNonRetryable FailureClass = "non_retryable"
	FailureInputRepair  FailureClass = "input_repairable"
	FailureTransient    FailureClass = "transient"
	FailureResource     FailureClass = "resource"
	FailureIntegrity    FailureClass = "integrity"
	FailureCancellation FailureClass = "cancellation"
)

type RenderJob struct {
	SchemaVersion                int                 `json:"schema_version"`
	ID                           string              `json:"id"`
	IdempotencyKey               string              `json:"idempotency_key"`
	PlanFingerprint              string              `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string              `json:"execution_manifest_fingerprint"`
	TimelineRange                videoedit.TimeRange `json:"timeline_range"`
	FrameRange                   videoedit.TickRange `json:"frame_range"`
	SampleRange                  videoedit.TickRange `json:"sample_range"`
	ArtifactURI                  string              `json:"artifact_uri"`
}

type ProgressEvent struct {
	JobID       string                 `json:"job_id"`
	Sequence    uint64                 `json:"sequence"`
	Stage       Stage                  `json:"stage"`
	State       JobState               `json:"state"`
	Unit        ProgressUnit           `json:"unit,omitempty"`
	Completed   int64                  `json:"completed,omitempty"`
	Total       int64                  `json:"total,omitempty"`
	FrameRange  *videoedit.TickRange   `json:"frame_range,omitempty"`
	SampleRange *videoedit.TickRange   `json:"sample_range,omitempty"`
	WorkerID    string                 `json:"worker_id,omitempty"`
	CacheHit    bool                   `json:"cache_hit,omitempty"`
	Diagnostics []videoedit.Diagnostic `json:"diagnostics,omitempty"`
}

type RenderArtifact struct {
	URI                          string                 `json:"uri"`
	ContentHash                  string                 `json:"content_hash"`
	SizeBytes                    int64                  `json:"size_bytes"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	EvidenceURI                  string                 `json:"evidence_uri"`
	EvidenceFingerprint          string                 `json:"evidence_fingerprint"`
	Duration                     videoedit.RationalTime `json:"duration"`
	FrameCount                   int64                  `json:"frame_count"`
	SampleCount                  int64                  `json:"sample_count"`
}

type RenderRequest struct {
	Job      RenderJob            `json:"job"`
	Plan     videoedit.RenderPlan `json:"plan"`
	Manifest ExecutionManifest    `json:"execution_manifest"`
}

// EventSink receives structured progress. Backends must stop if Emit returns an error or ctx is
// canceled, so a disconnected scheduler cannot leave unbounded work running.
type EventSink interface {
	Emit(context.Context, ProgressEvent) error
}

type EventSinkFunc func(context.Context, ProgressEvent) error

func (function EventSinkFunc) Emit(ctx context.Context, event ProgressEvent) error {
	return function(ctx, event)
}

// RenderBackend consumes a verified semantic plan and frozen execution manifest. It does not read
// the current project or resolve mutable assets during Render.
type RenderBackend interface {
	ID() string
	Capabilities(context.Context) (videoedit.BackendCapabilities, error)
	Preflight(context.Context, videoedit.RenderPlan, ExecutionManifest) []videoedit.Diagnostic
	Render(context.Context, RenderRequest, EventSink) (RenderArtifact, error)
}

// DecodeRenderJob strictly decodes the standalone scheduler contract. Parent-plan containment is
// checked later by ValidateRenderRequest, after the referenced immutable plan has been loaded.
func DecodeRenderJob(raw []byte) (RenderJob, error) {
	var job RenderJob
	if err := wirejson.Decode(raw, &job); err != nil {
		return RenderJob{}, fmt.Errorf("%w: decode: %v", ErrRenderJobInvalid, err)
	}
	if err := validateRenderJob(job); err != nil {
		return RenderJob{}, err
	}
	return job, nil
}

// ValidateRenderRequest verifies all immutable identities and chunk ranges before work starts.
func ValidateRenderRequest(request RenderRequest) error {
	planRaw, err := json.Marshal(request.Plan)
	if err != nil {
		return fmt.Errorf("%w: encode plan: %v", ErrRenderRequestInvalid, err)
	}
	verifiedPlan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		return fmt.Errorf("%w: plan: %v", ErrRenderRequestInvalid, err)
	}
	manifestRaw, err := json.Marshal(request.Manifest)
	if err != nil {
		return fmt.Errorf("%w: encode execution manifest: %v", ErrRenderRequestInvalid, err)
	}
	verifiedManifest, err := DecodeExecutionManifest(manifestRaw)
	if err != nil {
		return fmt.Errorf("%w: execution manifest: %v", ErrRenderRequestInvalid, err)
	}
	if err := validateManifestAgainstPlan(verifiedPlan, verifiedManifest); err != nil {
		return fmt.Errorf("%w: execution manifest binding: %v", ErrRenderRequestInvalid, err)
	}
	if diagnostics := videoedit.CheckCapabilities(verifiedPlan, verifiedManifest.Backend); videoedit.HasErrorDiagnostics(diagnostics) {
		return fmt.Errorf("%w: backend preflight: %s at %s", ErrRenderRequestInvalid, diagnostics[0].Code, diagnostics[0].Path)
	}
	job := request.Job
	if err := validateRenderJob(job); err != nil {
		return fmt.Errorf("%w: %v", ErrRenderRequestInvalid, err)
	}
	if job.PlanFingerprint != verifiedPlan.Fingerprint || job.PlanFingerprint != verifiedManifest.PlanFingerprint {
		return fmt.Errorf("%w: plan fingerprint mismatch", ErrRenderRequestInvalid)
	}
	if job.ExecutionManifestFingerprint != verifiedManifest.Fingerprint {
		return fmt.Errorf("%w: execution manifest fingerprint mismatch", ErrRenderRequestInvalid)
	}
	if err := timeRangeSubset(job.TimelineRange, verifiedPlan.RequestedRange); err != nil {
		return fmt.Errorf("%w: timeline range: %v", ErrRenderRequestInvalid, err)
	}
	wantFrames, err := videoedit.CompileFrameTickRange(job.TimelineRange, verifiedPlan.Target.FrameClock)
	if err != nil || wantFrames != job.FrameRange {
		return fmt.Errorf("%w: frame range is not derived from the job timeline range", ErrRenderRequestInvalid)
	}
	wantSamples, err := videoedit.CompileSampleTickRange(job.TimelineRange, verifiedPlan.Target.SampleClock)
	if err != nil || wantSamples != job.SampleRange {
		return fmt.Errorf("%w: sample range is not derived from the job timeline range", ErrRenderRequestInvalid)
	}
	if err := tickRangeSubset(job.FrameRange, verifiedPlan.FrameRange); err != nil {
		return fmt.Errorf("%w: frame range: %v", ErrRenderRequestInvalid, err)
	}
	if err := tickRangeSubset(job.SampleRange, verifiedPlan.SampleRange); err != nil {
		return fmt.Errorf("%w: sample range: %v", ErrRenderRequestInvalid, err)
	}
	return nil
}

func validateRenderJob(job RenderJob) error {
	if job.SchemaVersion != RenderJobSchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrRenderJobInvalid, job.SchemaVersion)
	}
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.IdempotencyKey) == "" || strings.TrimSpace(job.ArtifactURI) == "" {
		return fmt.Errorf("%w: identity, idempotency key and artifact URI are required", ErrRenderJobInvalid)
	}
	if err := validateArtifactURI(job.ArtifactURI); err != nil {
		return fmt.Errorf("%w: artifact URI: %v", ErrRenderJobInvalid, err)
	}
	if !validDigest(job.PlanFingerprint) || !validDigest(job.ExecutionManifestFingerprint) {
		return fmt.Errorf("%w: plan and execution manifest fingerprints must be canonical sha256 digests", ErrRenderJobInvalid)
	}
	if err := job.TimelineRange.Validate(); err != nil || job.TimelineRange.Duration.Value <= 0 {
		return fmt.Errorf("%w: timeline range must be valid and positive", ErrRenderJobInvalid)
	}
	if err := job.FrameRange.Validate(); err != nil {
		return fmt.Errorf("%w: frame range: %v", ErrRenderJobInvalid, err)
	}
	if err := job.SampleRange.Validate(); err != nil {
		return fmt.Errorf("%w: sample range: %v", ErrRenderJobInvalid, err)
	}
	if job.FrameRange.Count == 0 && job.SampleRange.Count == 0 {
		return fmt.Errorf("%w: job has no frame or sample work", ErrRenderJobInvalid)
	}
	return nil
}

func validateArtifactURI(value string) error {
	if strings.ContainsRune(value, 0) {
		return errors.New("NUL bytes are forbidden")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("only hostless file:// publication targets are supported")
	}
	if !filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path || parsed.Path == string(filepath.Separator) {
		return errors.New("publication path must be an absolute clean non-root path")
	}
	return nil
}

// EvidenceSidecarURI returns the only EvidenceManifest locator authorized by RenderJob v1. The
// sidecar is a direct suffix of the already validated hostless file artifact, so consumers never
// dereference a renderer-supplied scheme or authority after validation.
func EvidenceSidecarURI(artifactURI string) (string, error) {
	if err := validateArtifactURI(artifactURI); err != nil {
		return "", fmt.Errorf("%w: artifact URI: %v", ErrRenderJobInvalid, err)
	}
	parsed, err := url.ParseRequestURI(artifactURI)
	if err != nil {
		return "", fmt.Errorf("%w: artifact URI: %v", ErrRenderJobInvalid, err)
	}
	parsed.Path += ".evidence.json"
	parsed.RawPath = ""
	result := parsed.String()
	if err := validateArtifactURI(result); err != nil {
		return "", fmt.Errorf("%w: evidence URI: %v", ErrRenderJobInvalid, err)
	}
	return result, nil
}

func tickRangeSubset(candidate, parent videoedit.TickRange) error {
	candidateEnd, err := candidate.EndExclusive()
	if err != nil {
		return err
	}
	parentEnd, err := parent.EndExclusive()
	if err != nil {
		return err
	}
	if candidate.First < parent.First || candidateEnd > parentEnd {
		return fmt.Errorf("[%d,%d) is outside [%d,%d)", candidate.First, candidateEnd, parent.First, parentEnd)
	}
	return nil
}

func timeRangeSubset(candidate, parent videoedit.TimeRange) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := parent.Validate(); err != nil {
		return err
	}
	candidateEnd, err := candidate.End()
	if err != nil {
		return err
	}
	parentEnd, err := parent.End()
	if err != nil {
		return err
	}
	startComparison, err := candidate.Start.Compare(parent.Start)
	if err != nil {
		return err
	}
	endComparison, err := candidateEnd.Compare(parentEnd)
	if err != nil {
		return err
	}
	if startComparison < 0 || endComparison > 0 {
		return errors.New("job timeline range is outside plan requested range")
	}
	return nil
}
