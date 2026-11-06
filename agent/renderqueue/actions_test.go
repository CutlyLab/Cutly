package renderqueue

import (
	"context"
	"encoding/json"
	"errors"
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

type failingCoordinator struct{}

func (failingCoordinator) Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, scheduler.ErrStore
}
func (failingCoordinator) Snapshot(string) (scheduler.JobSnapshot, error) {
	return scheduler.JobSnapshot{}, scheduler.ErrStore
}
func (failingCoordinator) RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, scheduler.ErrStore
}

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
