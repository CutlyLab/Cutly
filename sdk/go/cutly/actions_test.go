package cutly

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestTypedAgentActionsBuildExactPayloads(t *testing.T) {
	var requests []agentprotocol.Request
	client, err := NewClient(TransportFunc(func(_ context.Context, raw []byte) ([]byte, error) {
		request, err := agentprotocol.DecodeRequest(raw)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
		return agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion, OK: true, RequestID: request.RequestID,
			Tool: request.Tool, Action: request.Action, State: "ok", ProjectID: request.ProjectID,
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	transaction := TransactionV1{
		ID: "tx", IdempotencyKey: "key", ExpectedRevision: 7,
		Operations: []OperationV1{{ID: "delete", Kind: OperationDeleteV1, ClipID: "clip"}},
	}
	calls := []func() (Result, error){
		func() (Result, error) { return client.InspectProject(context.Background(), "r1", "project") },
		func() (Result, error) { return client.ProjectStatus(context.Background(), "r2", "project") },
		func() (Result, error) { return client.ProjectHistory(context.Background(), "r3", "project", 4, 10) },
		func() (Result, error) { return client.PlanEditV1(context.Background(), "r4", "project", transaction) },
		func() (Result, error) { return client.ApplyEditV1(context.Background(), "r5", "project", "tx", 7) },
		func() (Result, error) { return client.DiscardEditV1(context.Background(), "r6", "project", "tx") },
		func() (Result, error) { return client.RevertEditV1(context.Background(), "r7", "project", "tx", 8) },
		func() (Result, error) {
			return client.SubmitRender(context.Background(), "r8", "project", RenderJobV1{})
		},
		func() (Result, error) { return client.RenderStatus(context.Background(), "r9", "project", "job") },
		func() (Result, error) { return client.CancelRender(context.Background(), "r10", "project", "job") },
	}
	for index, call := range calls {
		if _, err := call(); err != nil {
			t.Fatalf("call %d: %v", index, err)
		}
	}
	if len(requests) != len(calls) {
		t.Fatalf("requests = %d", len(requests))
	}
	if got := string(requests[2].Payload); got != `{"after_sequence":4,"limit":10}` {
		t.Fatalf("history payload = %s", got)
	}
	if got := string(requests[4].Payload); got != `{"expected_revision":7,"transaction_id":"tx"}` {
		t.Fatalf("apply payload = %s", got)
	}
	wantRender, err := canonicaljson.Marshal(renderPayload{Job: RenderJobV1{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[7].Payload); got != string(wantRender) {
		t.Fatalf("render payload = %s, want %s", got, wantRender)
	}
}

func TestProjectReceiptDecoders(t *testing.T) {
	document := DocumentV1{SchemaVersion: videoedit.SchemaVersion, ID: "doc", Revision: 2}
	inspectRaw, err := canonicaljson.Marshal(ProjectInspectReceiptV1{
		Profile: "cutly.project-inspect.v1",
		Snapshot: ProjectSnapshotV1{
			Document: document, DocumentHash: digestString('a'), PendingPlans: []ProjectPlanSummaryV1{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := DecodeProjectInspectReceipt(inspectRaw)
	if err != nil || inspect.Snapshot.Document.ID != "doc" {
		t.Fatalf("inspect = %+v, %v", inspect, err)
	}

	historyRaw, err := canonicaljson.Marshal(ProjectHistoryReceiptV1{
		Profile: "cutly.project-history.v1", Records: []ProjectHistoryRecordV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := DecodeProjectHistoryReceipt(historyRaw); err != nil || receipt.Records == nil {
		t.Fatalf("history = %+v, %v", receipt, err)
	}

	mutationRaw, err := canonicaljson.Marshal(ProjectMutationReceiptV1{
		Profile: "cutly.project-mutation.v1", Action: "apply", State: "applied", TransactionID: "tx",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := DecodeProjectMutationReceipt(mutationRaw); err != nil || receipt.TransactionID != "tx" {
		t.Fatalf("mutation = %+v, %v", receipt, err)
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(` {"profile":"cutly.project-mutation.v1","action":"apply","state":"applied"}`),
		json.RawMessage(`{"action":"apply","profile":"wrong.v1","state":"applied"}`),
		json.RawMessage(`{"action":"apply","extra":1,"profile":"cutly.project-mutation.v1","state":"applied"}`),
	} {
		if _, err := DecodeProjectMutationReceipt(raw); !errors.Is(err, ErrReceiptInvalid) {
			t.Fatalf("invalid receipt %s: %v", raw, err)
		}
	}
}

func digestString(fill byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = fill
	}
	return "sha256:" + string(value)
}
