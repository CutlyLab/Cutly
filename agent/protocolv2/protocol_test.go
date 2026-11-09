package protocolv2

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

func TestEditPlanEnvelopeOwnsCanonicalDocumentV2Transaction(t *testing.T) {
	transaction, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "document-v2-transaction-numbers.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(`{"transaction":`), transaction...)
	payload = append(payload, '}')
	request := Request{SchemaVersion: 2, Profile: RequestProfile, RequestID: "request", Tool: ToolEdit, Action: "plan", ProjectID: "project", PayloadProfile: PayloadProfile(ToolEdit, "plan"), Payload: payload}
	canonical, err := CanonicalRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(context.Background(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PayloadProfile != request.PayloadProfile || !bytes.Contains(decoded.Payload, []byte(`"decimal":1.2500`)) {
		t.Fatalf("decoded request=%+v payload=%s", decoded, decoded.Payload)
	}
	canonical[0] = '!'
	if !bytes.Contains(decoded.Payload, []byte(`"negative_zero":-0`)) {
		t.Fatal("decoded payload aliased input")
	}
}

func TestResultAndProfileFailures(t *testing.T) {
	result := Result{SchemaVersion: 2, Profile: ResultProfile, OK: true, RequestID: "request", Tool: ToolProject, Action: "inspect", ProjectID: "project", State: "current", Diagnostics: []documentv2.Diagnostic{}, Receipt: json.RawMessage(`{"profile":"cutly.project-inspect.document-v2.v2"}`)}
	canonical, err := CanonicalResult(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResult(context.Background(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CanonicalResult(context.Background(), decoded); err != nil {
		t.Fatal("decoded empty diagnostics lost required array ownership", err)
	}
	bad := result
	bad.Profile = RequestProfile
	if _, err = CanonicalResult(context.Background(), bad); err == nil {
		t.Fatal("request profile accepted as result")
	}
}

func TestActionSpecificPayloadsFailClosed(t *testing.T) {
	ctx := context.Background()
	tests := []Request{
		{Tool: ToolProject, Action: "inspect", Payload: json.RawMessage(`{"unexpected":true}`)},
		{Tool: ToolEdit, Action: "apply", Payload: json.RawMessage(`{}`)},
		{Tool: ToolEdit, Action: "revert", Payload: json.RawMessage(`{"idempotency_key":"key","target_transaction_id":"target","transaction_id":""}`)},
		{Tool: ToolRender, Action: "plan", Payload: json.RawMessage(`{"range":{},"target":{},"unexpected":true}`)},
		{Tool: ToolRender, Action: "preflight", Payload: json.RawMessage(`{"execution_manifest_fingerprint":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","plan_fingerprint":"bad"}`)},
		{Tool: ToolRender, Action: "frame", Payload: json.RawMessage(`{"composition_frame_index":0,"execution_manifest_fingerprint":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","plan_fingerprint":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","profile":"wrong"}`)},
		{Tool: ToolRender, Action: "submit", Payload: json.RawMessage(`{"job":{}}`)},
		{Tool: ToolRender, Action: "history", Payload: json.RawMessage(`{"job_id":""}`)},
	}
	for index, request := range tests {
		request.SchemaVersion = SchemaVersion
		request.Profile = RequestProfile
		request.RequestID = "request"
		request.ProjectID = "project"
		request.PayloadProfile = PayloadProfile(request.Tool, request.Action)
		if _, err := CanonicalRequest(ctx, request); err == nil {
			t.Fatalf("case %d accepted: %s/%s", index, request.Tool, request.Action)
		}
	}
}
