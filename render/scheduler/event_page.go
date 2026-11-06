package scheduler

import (
	"bytes"
	"fmt"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	JobEventPageSchemaVersion = 1
	JobEventPageProfileV1     = "cutly.render-job-history.v1"
	DefaultJobEventPageLimit  = 16
	MaxJobEventPageLimit      = 64
)

// JobEventPage is a cursor-bound point-in-time history view of one durable render job.
// Snapshot binds the immutable job and the page's upper sequence boundary.
// NextAfterSequence is the cursor callers should supply on the next request;
// CaughtUp means that cursor reached the same atomic snapshot.
type JobEventPage struct {
	SchemaVersion     int         `json:"schema_version"`
	Profile           string      `json:"profile"`
	AfterSequence     uint64      `json:"after_sequence"`
	NextAfterSequence uint64      `json:"next_after_sequence"`
	CaughtUp          bool        `json:"caught_up"`
	Events            []JobEvent  `json:"events"`
	Snapshot          JobSnapshot `json:"snapshot"`
}

// DecodeJobEventPage strictly decodes and validates one canonical scheduler
// event page. It rejects alternate JSON spellings so receipts can be compared
// and fingerprinted without a second representation.
func DecodeJobEventPage(raw []byte) (JobEventPage, error) {
	if len(raw) == 0 || len(raw) > MaxProtocolBytes {
		return JobEventPage{}, fmt.Errorf("%w: event page length %d", ErrProtocolInvalid, len(raw))
	}
	var page JobEventPage
	if err := wirejson.Decode(raw, &page); err != nil {
		return JobEventPage{}, fmt.Errorf("%w: event page decode: %v", ErrProtocolInvalid, err)
	}
	if err := ValidateJobEventPage(page); err != nil {
		return JobEventPage{}, err
	}
	canonical, err := canonicalProtocol(page)
	if err != nil || !bytes.Equal(canonical, raw) {
		return JobEventPage{}, fmt.Errorf("%w: event page must use canonical JSON", ErrProtocolInvalid)
	}
	return cloneEventPage(page), nil
}

// ValidateJobEventPage checks the cursor, contiguous event sequence, current
// snapshot binding and terminal evidence. It intentionally accepts a partial
// page: the snapshot records the atomic upper boundary while CaughtUp reports
// whether this page reached it.
func ValidateJobEventPage(page JobEventPage) error {
	if page.SchemaVersion != JobEventPageSchemaVersion || page.Profile != JobEventPageProfileV1 {
		return fmt.Errorf("%w: event page schema/profile", ErrProtocolInvalid)
	}
	if err := validateSnapshot(page.Snapshot); err != nil {
		return fmt.Errorf("%w: event page snapshot: %v", ErrProtocolInvalid, err)
	}
	if page.Events == nil || len(page.Events) > MaxJobEventPageLimit ||
		page.AfterSequence > page.Snapshot.LastSequence ||
		page.NextAfterSequence < page.AfterSequence ||
		page.NextAfterSequence > page.Snapshot.LastSequence ||
		page.CaughtUp != (page.NextAfterSequence == page.Snapshot.LastSequence) {
		return fmt.Errorf("%w: event page cursor/size", ErrProtocolInvalid)
	}
	if page.AfterSequence < page.Snapshot.LastSequence && len(page.Events) == 0 {
		return fmt.Errorf("%w: event page omitted an available event", ErrProtocolInvalid)
	}
	if page.AfterSequence == page.Snapshot.LastSequence && len(page.Events) != 0 {
		return fmt.Errorf("%w: caught-up event page contains events", ErrProtocolInvalid)
	}

	wantSequence := page.AfterSequence + 1
	for index, event := range page.Events {
		if err := ValidateJobEvent(event); err != nil {
			return fmt.Errorf("%w: event page event %d: %v", ErrProtocolInvalid, index, err)
		}
		if event.JobID != page.Snapshot.Job.ID || event.Sequence != wantSequence ||
			event.Sequence > page.Snapshot.LastSequence {
			return fmt.Errorf("%w: event page event %d binding", ErrProtocolInvalid, index)
		}
		if event.Kind == EventSubmitted {
			if event.Job == nil {
				return fmt.Errorf("%w: event page submission is missing its job", ErrProtocolInvalid)
			}
			left, leftErr := canonicalProtocol(*event.Job)
			right, rightErr := canonicalProtocol(page.Snapshot.Job)
			if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
				return fmt.Errorf("%w: event page submitted job differs from snapshot", ErrProtocolInvalid)
			}
		}
		if event.Artifact != nil {
			if err := validateArtifact(page.Snapshot.Job, *event.Artifact); err != nil {
				return fmt.Errorf("%w: event page artifact: %v", ErrProtocolInvalid, err)
			}
		}
		if index != 0 && !legalTransition(page.Events[index-1].State, event.State) {
			return fmt.Errorf("%w: event page event %d transition", ErrProtocolInvalid, index)
		}
		wantSequence++
	}
	wantNext := page.AfterSequence
	if len(page.Events) != 0 {
		wantNext = page.Events[len(page.Events)-1].Sequence
	}
	if page.NextAfterSequence != wantNext {
		return fmt.Errorf("%w: event page next cursor", ErrProtocolInvalid)
	}
	if page.NextAfterSequence == page.Snapshot.LastSequence && len(page.Events) != 0 {
		last := page.Events[len(page.Events)-1]
		if last.State != page.Snapshot.State ||
			last.FailureClass != page.Snapshot.FailureClass || last.FailureMessage != page.Snapshot.FailureMessage ||
			!equalOptionalArtifact(last.Artifact, page.Snapshot.Artifact) {
			return fmt.Errorf("%w: final event differs from snapshot", ErrProtocolInvalid)
		}
	} else if len(page.Events) != 0 && !eventCanReachSnapshot(page.Events[len(page.Events)-1], page.Snapshot) {
		return fmt.Errorf("%w: event page cannot reach snapshot", ErrProtocolInvalid)
	}
	return nil
}

func eventCanReachSnapshot(event JobEvent, snapshot JobSnapshot) bool {
	if event.Sequence >= snapshot.LastSequence {
		return event.Sequence == snapshot.LastSequence && event.State == snapshot.State
	}
	states := []renderapi.JobState{event.State}
	allStates := [...]renderapi.JobState{
		renderapi.JobQueued,
		renderapi.JobRunning,
		renderapi.JobCanceling,
		renderapi.JobCanceled,
		renderapi.JobFailed,
		renderapi.JobComplete,
	}
	for sequence := event.Sequence + 1; sequence <= snapshot.LastSequence; sequence++ {
		next := make([]renderapi.JobState, 0, len(allStates))
		for _, from := range states {
			for _, to := range allStates {
				if legalTransition(from, to) && validSnapshotSequence(to, sequence) && !containsJobState(next, to) {
					next = append(next, to)
				}
			}
		}
		if len(next) == 0 {
			return false
		}
		states = next
	}
	return containsJobState(states, snapshot.State)
}

func containsJobState(states []renderapi.JobState, wanted renderapi.JobState) bool {
	for _, state := range states {
		if state == wanted {
			return true
		}
	}
	return false
}

func cloneEventPage(value JobEventPage) JobEventPage {
	value.Events = cloneEvents(value.Events)
	value.Snapshot = cloneSnapshot(value.Snapshot)
	return value
}

func cloneEvents(events []JobEvent) []JobEvent {
	result := make([]JobEvent, len(events))
	for index := range events {
		result[index] = cloneEvent(events[index])
	}
	return result
}

func equalOptionalArtifact(left, right *renderapi.RenderArtifact) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftRaw, leftErr := canonicalProtocol(*left)
	rightRaw, rightErr := canonicalProtocol(*right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}
