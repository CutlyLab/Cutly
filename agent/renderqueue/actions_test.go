package renderqueue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

type memoryStore struct {
	events []scheduler.JobEvent
}

func (store *memoryStore) Append(_ context.Context, event scheduler.JobEvent) error {
	store.events = append(store.events, event)
	return nil
}

func (store *memoryStore) Replay(context.Context) ([]scheduler.JobEvent, error) {
	return append([]scheduler.JobEvent(nil), store.events...), nil
}

func TestQueueRoutesSubmitStatusCancelAndReplay(t *testing.T) {
	coordinator, err := scheduler.Open(context.Background(), &memoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	actionRouter, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	job := validJob()
	payload, _ := json.Marshal(map[string]any{"job": job})
	result, err := actionRouter.HandleAgent(context.Background(), request("render", payload))
	if err != nil || !result.OK || !result.Changed || result.JobID != job.ID || len(result.NextActions) != 2 {
		t.Fatalf("render = %+v, err = %v", result, err)
	}
	replayed, err := actionRouter.HandleAgent(context.Background(), request("render", payload))
	if err != nil || !replayed.OK || !replayed.Replayed || replayed.Changed {
		t.Fatalf("replay = %+v, err = %v", replayed, err)
	}
	status, err := actionRouter.HandleAgent(context.Background(), request("status", json.RawMessage(`{"job_id":"job-1"}`)))
	if err != nil || !status.OK || status.State != string(renderapi.JobQueued) {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	history, err := actionRouter.HandleAgent(context.Background(), request("history", json.RawMessage(`{"job_id":"job-1","after_sequence":0,"limit":1}`)))
	if err != nil || !history.OK || history.State != string(renderapi.JobQueued) {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
	page, err := scheduler.DecodeJobEventPage(history.Receipt)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != scheduler.EventSubmitted || !page.CaughtUp {
		t.Fatalf("event receipt = %+v, err = %v", page, err)
	}
	invalidCursor, err := actionRouter.HandleAgent(context.Background(), request("history", json.RawMessage(`{"job_id":"job-1","after_sequence":2}`)))
	if err != nil || invalidCursor.OK || len(invalidCursor.Diagnostics) != 1 ||
		invalidCursor.Diagnostics[0].Code != CodePayloadInvalid || invalidCursor.Diagnostics[0].Path != "/payload/after_sequence" {
		t.Fatalf("invalid history cursor = %+v, err = %v", invalidCursor, err)
	}
	canceled, err := actionRouter.HandleAgent(context.Background(), request("cancel", json.RawMessage(`{"job_id":"job-1"}`)))
	if err != nil || !canceled.OK || !canceled.Changed || canceled.State != string(renderapi.JobCanceling) {
		t.Fatalf("cancel = %+v, err = %v", canceled, err)
	}
}

func TestQueueRejectsPayloadAndSchedulerBusinessErrors(t *testing.T) {
	coordinator, _ := scheduler.Open(context.Background(), &memoryStore{})
	service, _ := New(coordinator)
	actionRouter, _ := router.New(service.Routes()...)
	for _, test := range []struct {
		action  string
		payload json.RawMessage
		code    string
	}{
		{action: "render", payload: json.RawMessage(`{}`), code: CodePayloadInvalid},
		{action: "status", payload: json.RawMessage(`{"job_id":"missing","extra":1}`), code: CodePayloadInvalid},
		{action: "status", payload: json.RawMessage(`{"job_id":"missing"}`), code: CodeJobNotFound},
		{action: "history", payload: json.RawMessage(`{"job_id":"missing"}`), code: CodeJobNotFound},
		{action: "history", payload: json.RawMessage(`{"job_id":"job-1","limit":65}`), code: CodePayloadInvalid},
	} {
		result, err := actionRouter.HandleAgent(context.Background(), request(test.action, test.payload))
		if err != nil || result.OK || result.Diagnostics[0].Code != test.code {
			t.Fatalf("%s result = %+v, err = %v", test.action, result, err)
		}
	}
}

func TestQueuePropagatesStoreAndContextFailures(t *testing.T) {
	service, _ := New(failingCoordinator{})
	routes := service.Routes()
	_, err := routes[0].Handler.Execute(context.Background(), request("render", mustJobPayload(t)))
	if !errors.Is(err, scheduler.ErrStore) {
		t.Fatalf("store error = %v", err)
	}
	if _, err := New(nil); err == nil {
		t.Fatal("nil coordinator accepted")
	}
}

func TestScopedQueueRejectsAnotherProjectBeforeCallbacks(t *testing.T) {
	coordinator := &countingCoordinator{}
	var admissionCalls atomic.Int32
	service, err := NewScoped("p", coordinator, AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
		admissionCalls.Add(1)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string]json.RawMessage{
		"render":  mustJobPayload(t),
		"status":  json.RawMessage(`{"job_id":"job-1"}`),
		"history": json.RawMessage(`{"job_id":"job-1"}`),
		"cancel":  json.RawMessage(`{"job_id":"job-1"}`),
	}
	for _, route := range service.Routes() {
		foreign := request(route.Action, payloads[route.Action])
		foreign.ProjectID = "another-project"
		outcome, err := route.Handler.Execute(context.Background(), foreign)
		if err != nil || outcome.OK || len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Code != CodeProjectMismatch {
			t.Fatalf("%s outcome=%+v err=%v", route.Action, outcome, err)
		}
	}
	if admissionCalls.Load() != 0 || coordinator.submitCalls.Load() != 0 ||
		coordinator.snapshotCalls.Load() != 0 || coordinator.historyCalls.Load() != 0 || coordinator.cancelCalls.Load() != 0 {
		t.Fatalf("callbacks admission=%d submit=%d snapshot=%d cancel=%d",
			admissionCalls.Load(), coordinator.submitCalls.Load(), coordinator.snapshotCalls.Load(), coordinator.cancelCalls.Load())
	}
}

func TestScopedQueueRejectsUnpreparedAndContainsAdmissionFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		admission Admission
	}{
		{
			name: "error",
			admission: AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
				return errors.New("secret admission detail")
			}),
		},
		{
			name: "panic",
			admission: AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
				panic("secret panic detail")
			}),
		},
		{
			name: "panicking error classifier",
			admission: AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
				return panicAdmissionError{}
			}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &countingCoordinator{}
			service, err := NewScoped("p", coordinator, test.admission)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := service.Routes()[0].Handler.Execute(context.Background(), request("render", mustJobPayload(t)))
			if err != nil || outcome.OK || len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Code != CodeJobNotPrepared {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if strings.Contains(outcome.Diagnostics[0].Message, "secret") {
				t.Fatalf("callback detail leaked: %q", outcome.Diagnostics[0].Message)
			}
			if coordinator.submitCalls.Load() != 0 {
				t.Fatalf("unprepared job submitted %d times", coordinator.submitCalls.Load())
			}
		})
	}

	t.Run("cancellation", func(t *testing.T) {
		coordinator := &countingCoordinator{}
		service, err := NewScoped("p", coordinator, AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
			return context.Canceled
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Routes()[0].Handler.Execute(context.Background(), request("render", mustJobPayload(t)))
		if !errors.Is(err, context.Canceled) || coordinator.submitCalls.Load() != 0 {
			t.Fatalf("cancel err=%v submits=%d", err, coordinator.submitCalls.Load())
		}
	})

	t.Run("already canceled", func(t *testing.T) {
		coordinator := &countingCoordinator{}
		var admissionCalls atomic.Int32
		service, err := NewScoped("p", coordinator, AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error {
			admissionCalls.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = service.Routes()[0].Handler.Execute(ctx, request("render", mustJobPayload(t)))
		if !errors.Is(err, context.Canceled) || admissionCalls.Load() != 0 || coordinator.submitCalls.Load() != 0 {
			t.Fatalf("cancel err=%v admissions=%d submits=%d", err, admissionCalls.Load(), coordinator.submitCalls.Load())
		}
	})
}

func TestScopedQueueFreezesAdmissionCopyAndRunsNormalFlow(t *testing.T) {
	coordinator, err := scheduler.Open(context.Background(), &memoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	var retained *renderapi.RenderJob
	service, err := NewScoped("p", coordinator, AdmissionFunc(func(ctx context.Context, projectID string, job renderapi.RenderJob) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if projectID != "p" || job.ID != "job-1" {
			t.Fatalf("admission project/job = %q/%q", projectID, job.ID)
		}
		job.ID = "mutated-by-admission"
		job.IdempotencyKey = "mutated-by-admission"
		retained = &job
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	actionRouter, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := actionRouter.HandleAgent(context.Background(), request("render", mustJobPayload(t)))
	if err != nil || !result.OK || result.JobID != "job-1" || len(result.NextActions) != 2 {
		t.Fatalf("render=%+v err=%v", result, err)
	}
	if retained == nil {
		t.Fatal("admission did not retain its owned copy")
	}
	retained.ID = "mutated-after-return"
	snapshot, err := coordinator.Snapshot("job-1")
	if err != nil || snapshot.Job.ID != "job-1" || snapshot.Job.IdempotencyKey != "idem-1" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	for _, action := range result.NextActions {
		if action.ProjectID != "p" {
			t.Fatalf("next action escaped project scope: %+v", action)
		}
	}
	status, err := actionRouter.HandleAgent(context.Background(), request("status", json.RawMessage(`{"job_id":"job-1"}`)))
	if err != nil || !status.OK || status.State != string(renderapi.JobQueued) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	canceled, err := actionRouter.HandleAgent(context.Background(), request("cancel", json.RawMessage(`{"job_id":"job-1"}`)))
	if err != nil || !canceled.OK || !canceled.Changed || canceled.State != string(renderapi.JobCanceling) {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
}

func TestScopedQueueRejectsTypedNilAndInvalidScope(t *testing.T) {
	var coordinator *countingCoordinator
	validAdmission := AdmissionFunc(func(context.Context, string, renderapi.RenderJob) error { return nil })
	if _, err := New(coordinator); err == nil {
		t.Fatal("typed nil coordinator accepted by New")
	}
	if _, err := NewScoped("p", coordinator, validAdmission); err == nil {
		t.Fatal("typed nil coordinator accepted by NewScoped")
	}
	var admission *nilAdmission
	if _, err := NewScoped("p", &countingCoordinator{}, admission); err == nil {
		t.Fatal("typed nil admission accepted")
	}
	for _, projectID := range []string{"", " \t\r\n", string([]byte{0xff})} {
		if _, err := NewScoped(projectID, &countingCoordinator{}, validAdmission); err == nil {
			t.Fatalf("invalid project ID %q accepted", projectID)
		}
	}
	for _, projectID := range []string{" p", "p "} {
		service, err := NewScoped(projectID, &countingCoordinator{}, validAdmission)
		if err != nil {
			t.Fatalf("protocol-legal project ID %q rejected: %v", projectID, err)
		}
		if _, rejected := service.requireProject(projectID); rejected {
			t.Fatalf("exact project ID %q rejected", projectID)
		}
		outcome, rejected := service.requireProject(strings.TrimSpace(projectID))
		if !rejected || len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Code != CodeProjectMismatch {
			t.Fatalf("project ID %q was not matched exactly: %+v", projectID, outcome)
		}
	}
}

type failingCoordinator struct{}

func (failingCoordinator) Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, scheduler.ErrStore
}
func (failingCoordinator) Snapshot(string) (scheduler.JobSnapshot, error) {
	return scheduler.JobSnapshot{}, scheduler.ErrStore
}
func (failingCoordinator) History(string, uint64, int) (scheduler.JobEventPage, error) {
	return scheduler.JobEventPage{}, scheduler.ErrStore
}
func (failingCoordinator) RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, scheduler.ErrStore
}

type countingCoordinator struct {
	submitCalls   atomic.Int32
	snapshotCalls atomic.Int32
	historyCalls  atomic.Int32
	cancelCalls   atomic.Int32
}

func (coordinator *countingCoordinator) Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	coordinator.submitCalls.Add(1)
	return scheduler.JobSnapshot{}, false, errors.New("unexpected submit")
}

func (coordinator *countingCoordinator) Snapshot(string) (scheduler.JobSnapshot, error) {
	coordinator.snapshotCalls.Add(1)
	return scheduler.JobSnapshot{}, errors.New("unexpected snapshot")
}

func (coordinator *countingCoordinator) History(string, uint64, int) (scheduler.JobEventPage, error) {
	coordinator.historyCalls.Add(1)
	return scheduler.JobEventPage{}, errors.New("unexpected history")
}

func (coordinator *countingCoordinator) RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error) {
	coordinator.cancelCalls.Add(1)
	return scheduler.JobSnapshot{}, false, errors.New("unexpected cancel")
}

type nilAdmission struct{}

func (*nilAdmission) AdmitRender(context.Context, string, renderapi.RenderJob) error {
	panic("typed nil admission must never be called")
}

type panicAdmissionError struct{}

func (panicAdmissionError) Error() string { panic("error detail must not be formatted") }
func (panicAdmissionError) Is(error) bool { panic("error classifier must be contained") }

func request(action string, payload json.RawMessage) agentprotocol.Request {
	return agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "r",
		Tool: agentprotocol.ToolRender, Action: action, ProjectID: "p", Payload: payload,
	}
}

func mustJobPayload(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"job": validJob()})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validJob() renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion,
		ID:            "job-1", IdempotencyKey: "idem-1",
		PlanFingerprint: schedulerDigest('a'), ExecutionManifestFingerprint: schedulerDigest('b'),
		TimelineRange: videoRange(0, 1), FrameRange: tickRange(0, 30), SampleRange: tickRange(0, 48_000),
		ArtifactURI: "file:///controlled/output.mp4",
	}
}

func schedulerDigest(fill byte) string {
	return "sha256:" + string(makeFilled(64, fill))
}

func makeFilled(count int, value byte) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func videoRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: videoedit.RationalTime{Value: start, Rate: 1}, Duration: videoedit.RationalTime{Value: duration, Rate: 1}}
}

func tickRange(first, count int64) videoedit.TickRange {
	return videoedit.TickRange{First: first, Count: count}
}
