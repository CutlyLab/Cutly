package scheduler

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	JobEventSchemaVersion    = 1
	JobSnapshotSchemaVersion = 1

	// MaxProtocolBytes is the largest encoded job event or snapshot accepted
	// by the v1 scheduler boundary. Individual variable-length fields have
	// tighter limits so programmatic callers cannot force an oversized marshal.
	MaxProtocolBytes       = 4 << 20
	MaxJobIDBytes          = 1 << 10
	MaxIdempotencyKeyBytes = 4 << 10
	MaxArtifactURIBytes    = 16 << 10
	MaxEvidenceURIBytes    = MaxArtifactURIBytes + len(".evidence.json")
	MaxFailureMessageBytes = 64 << 10

	// MaxReplayEvents is the v1 Coordinator admission ceiling. Stores may
	// enforce a tighter limit but Open never materializes more scheduler state.
	MaxReplayEvents       = 1 << 20
	MaxSnapshotExclusions = 1 << 12
)

var (
	ErrProtocolInvalid     = errors.New("scheduler: protocol is invalid")
	ErrJobNotFound         = errors.New("scheduler: job not found")
	ErrJobConflict         = errors.New("scheduler: job identity conflicts with stored job")
	ErrIdempotencyConflict = errors.New("scheduler: idempotency key conflicts with stored job")
	ErrInvalidTransition   = errors.New("scheduler: invalid job state transition")
	ErrStore               = errors.New("scheduler: durable store failed")
	ErrReplayLimit         = errors.New("scheduler: replay admission limit exceeded")
	// ErrCommitUnknown marks a Store.Append failure for which the event may
	// already be replay-visible. A Coordinator that observes this error must be
	// discarded and reopened before its state can be trusted again.
	ErrCommitUnknown = errors.New("scheduler: durable append commit outcome is unknown")
	// ErrRecoveryRequired means a Coordinator stopped serving after an unknown
	// append outcome. Open a new Coordinator from the recovered Store.
	ErrRecoveryRequired = errors.New("scheduler: coordinator recovery is required")
)

type JobEventKind string

const (
	EventSubmitted       JobEventKind = "submitted"
	EventStarted         JobEventKind = "started"
	EventCancelRequested JobEventKind = "cancel_requested"
	EventCanceled        JobEventKind = "canceled"
	EventFailed          JobEventKind = "failed"
	EventCompleted       JobEventKind = "completed"
)

// JobEvent is the append-only scheduler protocol. The submitted event owns the
// complete immutable RenderJob. Later events bind only its ID and next
// sequence. Terminal evidence is carried by the failed/completed event.
type JobEvent struct {
	SchemaVersion  int                       `json:"schema_version"`
	JobID          string                    `json:"job_id"`
	Sequence       uint64                    `json:"sequence"`
	Kind           JobEventKind              `json:"kind"`
	State          renderapi.JobState        `json:"state"`
	Job            *renderapi.RenderJob      `json:"job,omitempty"`
	FailureClass   renderapi.FailureClass    `json:"failure_class,omitempty"`
	FailureMessage string                    `json:"failure_message,omitempty"`
	Artifact       *renderapi.RenderArtifact `json:"artifact,omitempty"`
}

// JobSnapshot is the replayable materialized view returned by the coordinator.
// It is never the durable source of truth; the event stream is.
type JobSnapshot struct {
	SchemaVersion  int                       `json:"schema_version"`
	Job            renderapi.RenderJob       `json:"job"`
	State          renderapi.JobState        `json:"state"`
	LastSequence   uint64                    `json:"last_sequence"`
	FailureClass   renderapi.FailureClass    `json:"failure_class,omitempty"`
	FailureMessage string                    `json:"failure_message,omitempty"`
	Artifact       *renderapi.RenderArtifact `json:"artifact,omitempty"`
}

func DecodeJobEvent(raw []byte) (JobEvent, error) {
	if len(raw) == 0 || len(raw) > MaxProtocolBytes {
		return JobEvent{}, fmt.Errorf("%w: event length %d", ErrProtocolInvalid, len(raw))
	}
	var event JobEvent
	if err := wirejson.Decode(raw, &event); err != nil {
		return JobEvent{}, fmt.Errorf("%w: event decode: %v", ErrProtocolInvalid, err)
	}
	if err := ValidateJobEvent(event); err != nil {
		return JobEvent{}, err
	}
	return cloneEvent(event), nil
}

func DecodeJobSnapshot(raw []byte) (JobSnapshot, error) {
	if len(raw) == 0 || len(raw) > MaxProtocolBytes {
		return JobSnapshot{}, fmt.Errorf("%w: snapshot length %d", ErrProtocolInvalid, len(raw))
	}
	var snapshot JobSnapshot
	if err := wirejson.Decode(raw, &snapshot); err != nil {
		return JobSnapshot{}, fmt.Errorf("%w: snapshot decode: %v", ErrProtocolInvalid, err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return JobSnapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

func ValidateJobEvent(event JobEvent) error {
	if event.SchemaVersion != JobEventSchemaVersion {
		return fmt.Errorf("%w: event schema version %d", ErrProtocolInvalid, event.SchemaVersion)
	}
	if !validBoundedString(event.JobID, MaxJobIDBytes) || strings.TrimSpace(event.JobID) == "" || event.Sequence == 0 {
		return fmt.Errorf("%w: event job ID and positive sequence are required", ErrProtocolInvalid)
	}
	wantState, validKind := eventState(event.Kind)
	if !validKind || event.State != wantState {
		return fmt.Errorf("%w: event kind %q does not match state %q", ErrProtocolInvalid, event.Kind, event.State)
	}
	if event.Kind == EventSubmitted {
		if event.Sequence != 1 || event.Job == nil || event.Job.ID != event.JobID {
			return fmt.Errorf("%w: submitted event must be sequence one and own its matching job", ErrProtocolInvalid)
		}
		if _, err := validateAndCloneRenderJob(*event.Job); err != nil {
			return fmt.Errorf("%w: submitted job: %v", ErrProtocolInvalid, err)
		}
	} else {
		if event.Sequence < 2 {
			return fmt.Errorf("%w: non-submitted event sequence must be at least two", ErrProtocolInvalid)
		}
		if event.Job != nil {
			return fmt.Errorf("%w: only submitted events may contain a job", ErrProtocolInvalid)
		}
	}
	if !validEventSequence(event.Kind, event.Sequence) {
		return fmt.Errorf("%w: event kind %q cannot have sequence %d", ErrProtocolInvalid, event.Kind, event.Sequence)
	}
	if event.Kind == EventFailed {
		if !validFailureClass(event.FailureClass) || !validBoundedString(event.FailureMessage, MaxFailureMessageBytes) || strings.TrimSpace(event.FailureMessage) == "" {
			return fmt.Errorf("%w: failed event requires failure class and message", ErrProtocolInvalid)
		}
	} else if event.FailureClass != "" || event.FailureMessage != "" {
		return fmt.Errorf("%w: failure fields are only valid for failed events", ErrProtocolInvalid)
	}
	if event.Kind == EventCompleted {
		if event.Artifact == nil {
			return fmt.Errorf("%w: completed event requires an artifact", ErrProtocolInvalid)
		}
		if err := validateArtifactShape(*event.Artifact); err != nil {
			return err
		}
	} else if event.Artifact != nil {
		return fmt.Errorf("%w: artifact is only valid for completed events", ErrProtocolInvalid)
	}
	return nil
}

func validateSnapshot(snapshot JobSnapshot) error {
	if snapshot.SchemaVersion != JobSnapshotSchemaVersion {
		return fmt.Errorf("%w: snapshot schema version %d", ErrProtocolInvalid, snapshot.SchemaVersion)
	}
	if _, err := validateAndCloneRenderJob(snapshot.Job); err != nil {
		return fmt.Errorf("%w: snapshot job: %v", ErrProtocolInvalid, err)
	}
	if snapshot.LastSequence == 0 || !validJobState(snapshot.State) || !validSnapshotSequence(snapshot.State, snapshot.LastSequence) {
		return fmt.Errorf("%w: snapshot state and sequence are invalid", ErrProtocolInvalid)
	}
	if snapshot.State == renderapi.JobFailed {
		if !validFailureClass(snapshot.FailureClass) || !validBoundedString(snapshot.FailureMessage, MaxFailureMessageBytes) || strings.TrimSpace(snapshot.FailureMessage) == "" {
			return fmt.Errorf("%w: failed snapshot requires failure details", ErrProtocolInvalid)
		}
	} else if snapshot.FailureClass != "" || snapshot.FailureMessage != "" {
		return fmt.Errorf("%w: non-failed snapshot contains failure details", ErrProtocolInvalid)
	}
	if snapshot.State == renderapi.JobComplete {
		if snapshot.Artifact == nil {
			return fmt.Errorf("%w: complete snapshot requires an artifact", ErrProtocolInvalid)
		}
		if err := validateArtifact(snapshot.Job, *snapshot.Artifact); err != nil {
			return err
		}
	} else if snapshot.Artifact != nil {
		return fmt.Errorf("%w: non-complete snapshot contains an artifact", ErrProtocolInvalid)
	}
	return nil
}

func validateAndCloneRenderJob(job renderapi.RenderJob) (renderapi.RenderJob, error) {
	if !validBoundedString(job.ID, MaxJobIDBytes) ||
		!validBoundedString(job.IdempotencyKey, MaxIdempotencyKeyBytes) ||
		!validBoundedString(job.ArtifactURI, MaxArtifactURIBytes) {
		return renderapi.RenderJob{}, errors.New("scheduler: render job string exceeds its protocol limit or is not UTF-8")
	}
	// Check fixed-width identities before marshaling. Besides producing a clearer
	// protocol error, this preserves the programmatic-call resource boundary: a
	// caller cannot force an oversized allocation before MaxProtocolBytes applies.
	if !validDigest(job.PlanFingerprint) || !validDigest(job.ExecutionManifestFingerprint) {
		return renderapi.RenderJob{}, errors.New("scheduler: render job fingerprints must be canonical sha256 digests")
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return renderapi.RenderJob{}, err
	}
	verified, err := renderapi.DecodeRenderJob(raw)
	if err != nil {
		return renderapi.RenderJob{}, err
	}
	return verified, nil
}

func validateArtifact(job renderapi.RenderJob, artifact renderapi.RenderArtifact) error {
	if artifact.URI != job.ArtifactURI || artifact.ExecutionManifestFingerprint != job.ExecutionManifestFingerprint {
		return fmt.Errorf("%w: artifact identity does not match job", ErrProtocolInvalid)
	}
	wantEvidenceURI, err := renderapi.EvidenceSidecarURI(job.ArtifactURI)
	if err != nil || artifact.EvidenceURI != wantEvidenceURI {
		return fmt.Errorf("%w: artifact evidence locator does not match job", ErrProtocolInvalid)
	}
	durationComparison, err := artifact.Duration.Compare(job.TimelineRange.Duration)
	if err != nil || durationComparison != 0 || artifact.FrameCount != job.FrameRange.Count || artifact.SampleCount != job.SampleRange.Count {
		return fmt.Errorf("%w: artifact timing does not match job", ErrProtocolInvalid)
	}
	return validateArtifactShape(artifact)
}

func validateArtifactShape(artifact renderapi.RenderArtifact) error {
	if !validDigest(artifact.ContentHash) || !validDigest(artifact.EvidenceFingerprint) ||
		!validDigest(artifact.ExecutionManifestFingerprint) ||
		!validBoundedString(artifact.URI, MaxArtifactURIBytes) ||
		!validBoundedString(artifact.EvidenceURI, MaxEvidenceURIBytes) ||
		strings.TrimSpace(artifact.URI) == "" || strings.TrimSpace(artifact.EvidenceURI) == "" || artifact.SizeBytes <= 0 ||
		artifact.FrameCount < 0 || artifact.SampleCount < 0 {
		return fmt.Errorf("%w: artifact fields are incomplete", ErrProtocolInvalid)
	}
	wantEvidenceURI, err := renderapi.EvidenceSidecarURI(artifact.URI)
	if err != nil || artifact.EvidenceURI != wantEvidenceURI {
		return fmt.Errorf("%w: artifact evidence locator is invalid", ErrProtocolInvalid)
	}
	if err := artifact.Duration.Validate(); err != nil || artifact.Duration.Value <= 0 {
		return fmt.Errorf("%w: artifact duration is invalid", ErrProtocolInvalid)
	}
	return nil
}

func validEventSequence(kind JobEventKind, sequence uint64) bool {
	switch kind {
	case EventSubmitted:
		return sequence == 1
	case EventStarted:
		return sequence == 2
	case EventCancelRequested:
		return sequence == 2 || sequence == 3
	case EventCanceled:
		return sequence == 3 || sequence == 4
	case EventFailed:
		return sequence >= 2 && sequence <= 4
	case EventCompleted:
		return sequence == 3
	default:
		return false
	}
}

func validSnapshotSequence(state renderapi.JobState, sequence uint64) bool {
	switch state {
	case renderapi.JobQueued:
		return sequence == 1
	case renderapi.JobRunning:
		return sequence == 2
	case renderapi.JobCanceling:
		return sequence == 2 || sequence == 3
	case renderapi.JobCanceled:
		return sequence == 3 || sequence == 4
	case renderapi.JobFailed:
		return sequence >= 2 && sequence <= 4
	case renderapi.JobComplete:
		return sequence == 3
	default:
		return false
	}
}

func eventState(kind JobEventKind) (renderapi.JobState, bool) {
	switch kind {
	case EventSubmitted:
		return renderapi.JobQueued, true
	case EventStarted:
		return renderapi.JobRunning, true
	case EventCancelRequested:
		return renderapi.JobCanceling, true
	case EventCanceled:
		return renderapi.JobCanceled, true
	case EventFailed:
		return renderapi.JobFailed, true
	case EventCompleted:
		return renderapi.JobComplete, true
	default:
		return "", false
	}
}

func validJobState(state renderapi.JobState) bool {
	switch state {
	case renderapi.JobQueued, renderapi.JobRunning, renderapi.JobCanceling,
		renderapi.JobCanceled, renderapi.JobFailed, renderapi.JobComplete:
		return true
	default:
		return false
	}
}

func validFailureClass(value renderapi.FailureClass) bool {
	switch value {
	case renderapi.FailureNonRetryable, renderapi.FailureInputRepair, renderapi.FailureTransient,
		renderapi.FailureResource, renderapi.FailureIntegrity, renderapi.FailureCancellation:
		return true
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	payload := value[len("sha256:"):]
	if payload != strings.ToLower(payload) {
		return false
	}
	_, err := hex.DecodeString(payload)
	return err == nil
}

func validBoundedString(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value)
}

func cloneEvent(value JobEvent) JobEvent {
	if value.Job != nil {
		job := *value.Job
		value.Job = &job
	}
	if value.Artifact != nil {
		artifact := *value.Artifact
		value.Artifact = &artifact
	}
	return value
}

func cloneSnapshot(value JobSnapshot) JobSnapshot {
	if value.Artifact != nil {
		artifact := *value.Artifact
		value.Artifact = &artifact
	}
	return value
}

func canonicalProtocol(value any) ([]byte, error) {
	return videoedit.CanonicalJSON(value)
}
