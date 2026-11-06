package cutly

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestClientCallCorrelatesAndPreservesBusinessRejection(t *testing.T) {
	request, err := NewRequest("request-1", ToolEdit, "apply", "project-1", map[string]any{"transaction_id": "tx-1"})
	if err != nil {
		t.Fatal(err)
	}
	transport := TransportFunc(func(_ context.Context, raw []byte) ([]byte, error) {
		decoded, err := agentprotocol.DecodeRequest(raw)
		if err != nil {
			return nil, err
		}
		return agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion,
			OK:              false,
			RequestID:       decoded.RequestID,
			Tool:            decoded.Tool,
			Action:          decoded.Action,
			State:           "rejected",
			ProjectID:       decoded.ProjectID,
			Diagnostics: []videoedit.Diagnostic{{
				Code: "TX.REJECTED", Severity: videoedit.SeverityError, Message: "rejected",
			}},
			NextActions: []NextAction{},
		})
	})
	client, err := NewClient(transport)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), request)
	if err != nil || result.OK || len(result.Diagnostics) != 1 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	mismatch, _ := NewClient(TransportFunc(func(context.Context, []byte) ([]byte, error) {
		return agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion, OK: true, RequestID: "other", Tool: ToolEdit,
			Action: "apply", State: "current", ProjectID: "project-1",
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
		})
	}))
	if _, err := mismatch.Call(context.Background(), request); !errors.Is(err, ErrCorrelation) {
		t.Fatalf("correlation error = %v", err)
	}
}

func TestNewRequestRequiresCanonicalIntegerPayload(t *testing.T) {
	if _, err := NewRequest("r", ToolEdit, "plan", "p", map[string]any{"unsafe": 1.5}); err == nil {
		t.Fatal("fractional payload accepted")
	}
	request, err := NewRequest("r", ToolProject, "inspect", "p", json.RawMessage(`{"nested":{"value":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Payload[0] = '['
	request2, err := NewRequest("r", ToolProject, "inspect", "p", json.RawMessage(`{"nested":{"value":1}}`))
	if err != nil || string(request2.Payload) != `{"nested":{"value":1}}` {
		t.Fatalf("owned payload = %s, %v", request2.Payload, err)
	}
}

func TestClientNilAndTransportFailure(t *testing.T) {
	if _, err := NewClient(nil); !errors.Is(err, ErrTransport) {
		t.Fatalf("NewClient(nil) = %v", err)
	}
	client, _ := NewClient(TransportFunc(func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("offline")
	}))
	request, _ := NewRequest("r", ToolProject, "capabilities", "", struct{}{})
	if _, err := client.Call(context.Background(), request); !errors.Is(err, ErrTransport) {
		t.Fatalf("transport error = %v", err)
	}
}
