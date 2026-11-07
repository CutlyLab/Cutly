package scheduler

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestSchedulerProtocolFixtures(t *testing.T) {
	job := schedulerTestJob("job-fixture", "request-fixture")
	event := JobEvent{
		SchemaVersion: JobEventSchemaVersion, JobID: job.ID, Sequence: 1,
		Kind: EventSubmitted, State: "queued", Job: &job,
	}
	eventRaw, err := canonicalProtocol(event)
	if err != nil {
		t.Fatal(err)
	}
	assertSchedulerFixture(t, "job-event-v1.canonical.json", eventRaw)
	decodedEvent, err := DecodeJobEvent(eventRaw)
	if err != nil || decodedEvent.Job == event.Job {
		t.Fatalf("decoded event = %+v err=%v", decodedEvent, err)
	}

	snapshot := snapshotFromSubmission(job)
	snapshotRaw, err := canonicalProtocol(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	assertSchedulerFixture(t, "job-snapshot-v1.canonical.json", snapshotRaw)
	decodedSnapshot, err := DecodeJobSnapshot(snapshotRaw)
	if err != nil || decodedSnapshot != snapshot {
		t.Fatalf("decoded snapshot = %+v err=%v", decodedSnapshot, err)
	}
}

func TestSchedulerProtocolStrictDecodeAndSemanticValidation(t *testing.T) {
	job := schedulerTestJob("job-strict", "request-strict")
	event := JobEvent{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: "queued", Job: &job}
	raw := marshalProtocol(t, event)
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeJobEvent(withUnknown); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("unknown field = %v", err)
	}
	duplicate := append([]byte(`{"schema_version":99,`), raw[1:]...)
	if _, err := DecodeJobEvent(duplicate); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("duplicate field = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := DecodeJobEvent(caseAlias); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("case alias = %v", err)
	}

	invalid := event
	invalid.Sequence = 2
	if err := ValidateJobEvent(invalid); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("submitted sequence = %v", err)
	}
	invalid = JobEvent{SchemaVersion: 1, JobID: job.ID, Sequence: 2, Kind: EventCompleted, State: "complete"}
	if err := ValidateJobEvent(invalid); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("complete without artifact = %v", err)
	}
	snapshot := snapshotFromSubmission(job)
	snapshot.LastSequence = 0
	raw = marshalProtocol(t, snapshot)
	if _, err := DecodeJobSnapshot(raw); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("zero snapshot sequence = %v", err)
	}
}

func TestSchedulerProtocolResourceAndReachabilityLimits(t *testing.T) {
	if _, err := DecodeJobEvent(bytes.Repeat([]byte{' '}, MaxProtocolBytes+1)); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized event = %v", err)
	}
	if _, err := DecodeJobSnapshot(bytes.Repeat([]byte{' '}, MaxProtocolBytes+1)); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized snapshot = %v", err)
	}

	job := schedulerTestJob("job-limits", "request-limits")
	event := JobEvent{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: renderapi.JobQueued, Job: &job}
	event.JobID = strings.Repeat("j", MaxJobIDBytes+1)
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized job ID = %v", err)
	}
	event = JobEvent{
		SchemaVersion: 1, JobID: job.ID, Sequence: 2, Kind: EventFailed, State: renderapi.JobFailed,
		FailureClass: renderapi.FailureIntegrity, FailureMessage: strings.Repeat("f", MaxFailureMessageBytes+1),
	}
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized failure = %v", err)
	}
	job.IdempotencyKey = string([]byte{0xff})
	event = JobEvent{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: renderapi.JobQueued, Job: &job}
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("invalid UTF-8 identity = %v", err)
	}

	job = schedulerTestJob("job-fingerprint-limit", "request-fingerprint-limit")
	job.PlanFingerprint = strings.Repeat("x", MaxProtocolBytes+1)
	event = JobEvent{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: EventSubmitted, State: renderapi.JobQueued, Job: &job}
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized programmatic fingerprint = %v", err)
	}

	job = schedulerTestJob("job-artifact-shape", "request-artifact-shape")
	artifact := schedulerTestArtifact(job)
	event = JobEvent{
		SchemaVersion: 1, JobID: job.ID, Sequence: 3, Kind: EventCompleted,
		State: renderapi.JobComplete, Artifact: pointerArtifact(artifact),
	}
	if err := ValidateJobEvent(event); err != nil {
		t.Fatalf("valid standalone completed event = %v", err)
	}
	artifact.ExecutionManifestFingerprint = strings.Repeat("x", MaxProtocolBytes+1)
	event.Artifact = pointerArtifact(artifact)
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("oversized artifact execution fingerprint = %v", err)
	}
	artifact = schedulerTestArtifact(job)
	artifact.URI = ""
	event.Artifact = pointerArtifact(artifact)
	if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("empty standalone artifact URI = %v", err)
	}

	job = schedulerTestJob("job-sequence", "request-sequence")
	for _, event := range []JobEvent{
		{SchemaVersion: 1, JobID: job.ID, Sequence: 3, Kind: EventStarted, State: renderapi.JobRunning},
		{SchemaVersion: 1, JobID: job.ID, Sequence: 2, Kind: EventCompleted, State: renderapi.JobComplete, Artifact: pointerArtifact(schedulerTestArtifact(job))},
		{SchemaVersion: 1, JobID: job.ID, Sequence: 5, Kind: EventFailed, State: renderapi.JobFailed, FailureClass: renderapi.FailureIntegrity, FailureMessage: "failure"},
	} {
		if err := ValidateJobEvent(event); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("unreachable event %+v = %v", event, err)
		}
	}
	snapshot := JobSnapshot{SchemaVersion: 1, Job: job, State: renderapi.JobComplete, LastSequence: 4, Artifact: pointerArtifact(schedulerTestArtifact(job))}
	if _, err := DecodeJobSnapshot(marshalProtocol(t, snapshot)); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("unreachable snapshot = %v", err)
	}
}

func assertSchedulerFixture(t *testing.T, name string, actual []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), actual) {
		t.Fatalf("fixture %s drift\nwant %s\n got %s", name, bytes.TrimSpace(want), actual)
	}
}
