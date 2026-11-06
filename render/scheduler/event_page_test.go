package scheduler

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestJobHistoryPageCanonicalFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/job-history-page-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.TrimSpace(raw)
	page, err := DecodeJobEventPage(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalProtocol(page)
	if err != nil || !bytes.Equal(canonical, raw) {
		t.Fatalf("history fixture drift: %v\nwant %s\n got %s", err, raw, canonical)
	}
}

func TestCoordinatorEventPagesAreAtomicCursorBoundAndReplayable(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-events", "request-events")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Complete(context.Background(), job.ID, schedulerTestArtifact(job)); err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.History(job.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.AfterSequence != 0 || first.NextAfterSequence != 2 || first.CaughtUp || len(first.Events) != 2 ||
		first.Events[0].Kind != EventSubmitted || first.Events[1].Kind != EventStarted ||
		first.Snapshot.State != renderapi.JobComplete || first.Snapshot.LastSequence != 3 {
		t.Fatalf("first page = %+v", first)
	}
	second, err := coordinator.History(job.ID, first.NextAfterSequence, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.AfterSequence != 2 || second.NextAfterSequence != 3 || !second.CaughtUp ||
		len(second.Events) != 1 || second.Events[0].Kind != EventCompleted {
		t.Fatalf("second page = %+v", second)
	}
	empty, err := coordinator.History(job.ID, second.NextAfterSequence, 0)
	if err != nil || !empty.CaughtUp || empty.Events == nil || len(empty.Events) != 0 ||
		empty.AfterSequence != 3 || empty.NextAfterSequence != 3 {
		t.Fatalf("empty page = %+v err=%v", empty, err)
	}

	// Returned pages own every pointer and slice. Mutating a caller copy cannot
	// change a later cursor read or the durable coordinator state.
	first.Events[0].Job.ID = "mutated"
	first.Snapshot.Job.ID = "mutated"
	second.Events[0].Artifact.URI = "file:///mutated.mp4"
	again, err := coordinator.History(job.ID, 0, 0)
	if err != nil || again.Snapshot.Job.ID != job.ID || again.Events[0].Job.ID != job.ID ||
		again.Events[2].Artifact.URI != job.ArtifactURI {
		t.Fatalf("owned page = %+v err=%v", again, err)
	}

	reopened, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.History(job.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := canonicalProtocol(again)
	right, _ := canonicalProtocol(replayed)
	if !bytes.Equal(left, right) {
		t.Fatalf("replayed page drift\nfirst %s\nagain %s", left, right)
	}
}

func TestCoordinatorEventPageRejectsInvalidQueriesAndRecoveryState(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-event-errors", "request-event-errors")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		jobID string
		after uint64
		limit int
		want  error
	}{
		{name: "not found", jobID: "missing", want: ErrJobNotFound},
		{name: "cursor", jobID: job.ID, after: 2, want: ErrProtocolInvalid},
		{name: "negative limit", jobID: job.ID, limit: -1, want: ErrProtocolInvalid},
		{name: "large limit", jobID: job.ID, limit: MaxJobEventPageLimit + 1, want: ErrProtocolInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := coordinator.History(test.jobID, test.after, test.limit); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	store.appendCommittedErr = ErrCommitUnknown
	if _, err := coordinator.Start(context.Background(), job.ID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("unknown commit = %v", err)
	}
	if _, err := coordinator.History(job.ID, 0, 0); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("events after unknown commit = %v", err)
	}
}

func TestJobEventPageStrictCanonicalCodecAndTamperRejection(t *testing.T) {
	store := &memoryStore{}
	coordinator, err := Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	job := schedulerTestJob("job-page-codec", "request-page-codec")
	if _, _, err := coordinator.Submit(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	page, err := coordinator.History(job.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalProtocol(page)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJobEventPage(raw)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, _ := canonicalProtocol(decoded)
	if !bytes.Equal(raw, roundTrip) {
		t.Fatalf("round trip drift\nwant %s\n got %s", raw, roundTrip)
	}
	if _, err := DecodeJobEventPage(append([]byte(" \n"), raw...)); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("noncanonical page = %v", err)
	}

	mutations := []func(*JobEventPage){
		func(value *JobEventPage) { value.Profile = "other.v1" },
		func(value *JobEventPage) { value.NextAfterSequence = 0 },
		func(value *JobEventPage) { value.CaughtUp = false },
		func(value *JobEventPage) { value.Events[0].Sequence = 2 },
		func(value *JobEventPage) { value.Events[0].Job.ID = "other" },
		func(value *JobEventPage) { value.Snapshot.Job.ID = "other" },
	}
	for index, mutate := range mutations {
		changed := cloneEventPage(page)
		mutate(&changed)
		if err := ValidateJobEventPage(changed); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("mutation %d = %v", index, err)
		}
	}

	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Complete(context.Background(), job.ID, schedulerTestArtifact(job)); err != nil {
		t.Fatal(err)
	}
	complete, err := coordinator.History(job.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	impossibleAdjacent := cloneEventPage(complete)
	impossibleAdjacent.Events[1] = JobEvent{
		SchemaVersion:  JobEventSchemaVersion,
		JobID:          job.ID,
		Sequence:       2,
		Kind:           EventFailed,
		State:          renderapi.JobFailed,
		FailureClass:   renderapi.FailureNonRetryable,
		FailureMessage: "failed before an impossible completion",
	}
	if err := ValidateJobEventPage(impossibleAdjacent); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("impossible adjacent transition = %v", err)
	}
	impossibleTail := cloneEventPage(complete)
	impossibleTail.Events = impossibleTail.Events[:2]
	impossibleTail.Events[1] = JobEvent{
		SchemaVersion: JobEventSchemaVersion,
		JobID:         job.ID,
		Sequence:      2,
		Kind:          EventCancelRequested,
		State:         renderapi.JobCanceling,
	}
	impossibleTail.NextAfterSequence = 2
	impossibleTail.CaughtUp = false
	if err := ValidateJobEventPage(impossibleTail); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("impossible page tail = %v", err)
	}
}
