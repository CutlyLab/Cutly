package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestRouterCapabilitiesAndUnavailableAreTruthful(t *testing.T) {
	router, err := New(Route{
		Tool: agentprotocol.ToolProject, Action: "inspect",
		Handler: ActionFunc(func(context.Context, agentprotocol.Request) (Outcome, error) {
			return Outcome{OK: true, State: "current"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := router.HandleAgent(context.Background(), request(agentprotocol.ToolProject, "capabilities", ""))
	if err != nil || !capabilities.OK || len(capabilities.Receipt) == 0 {
		t.Fatalf("capabilities = %+v, err = %v", capabilities, err)
	}
	var receipt struct {
		Actions []capabilityAction `json:"actions"`
	}
	if err := json.Unmarshal(capabilities.Receipt, &receipt); err != nil || len(receipt.Actions) != 2 {
		t.Fatalf("receipt = %s, err = %v", capabilities.Receipt, err)
	}

	unavailable, err := router.HandleAgent(context.Background(), request(agentprotocol.ToolRender, "status", "p"))
	if err != nil || unavailable.OK || unavailable.Diagnostics[0].Code != CodeActionUnavailable {
		t.Fatalf("unavailable = %+v, err = %v", unavailable, err)
	}
}

func TestRouterWrapsOwnedValidatedOutcomes(t *testing.T) {
	revision := videoedit.Revision(7)
	diagnostics := []videoedit.Diagnostic{{Code: "OK", Severity: videoedit.SeverityInfo, Message: "ready"}}
	router, err := New(Route{
		Tool: agentprotocol.ToolEdit, Action: "apply",
		Handler: ActionFunc(func(context.Context, agentprotocol.Request) (Outcome, error) {
			return Outcome{
				OK: true, State: "applied", TransactionID: "tx", NewRevision: &revision,
				Changed: true, RenderRequired: true, Diagnostics: diagnostics,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.HandleAgent(context.Background(), request(agentprotocol.ToolEdit, "apply", "p"))
	if err != nil || !result.OK || result.RequestID != "r" || result.TransactionID != "tx" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	diagnostics[0].Message = "mutated"
	revision = 99
	if result.Diagnostics[0].Message != "ready" || *result.NewRevision != 7 {
		t.Fatalf("result aliases handler values: %+v", result)
	}
}

func TestRouterRejectsInvalidRoutesAndOutcomes(t *testing.T) {
	handler := ActionFunc(func(context.Context, agentprotocol.Request) (Outcome, error) {
		return Outcome{OK: true, State: "current"}, nil
	})
	if _, err := New(Route{Tool: agentprotocol.ToolEdit, Action: "inspect", Handler: handler}); !errors.Is(err, ErrRouteInvalid) {
		t.Fatalf("invalid route = %v", err)
	}
	if _, err := New(
		Route{Tool: agentprotocol.ToolEdit, Action: "apply", Handler: handler},
		Route{Tool: agentprotocol.ToolEdit, Action: "apply", Handler: handler},
	); !errors.Is(err, ErrRouteDuplicate) {
		t.Fatalf("duplicate route = %v", err)
	}
	router, _ := New(Route{
		Tool: agentprotocol.ToolEdit, Action: "apply",
		Handler: ActionFunc(func(context.Context, agentprotocol.Request) (Outcome, error) {
			return Outcome{OK: false, State: "rejected"}, nil
		}),
	})
	if _, err := router.HandleAgent(context.Background(), request(agentprotocol.ToolEdit, "apply", "p")); err == nil {
		t.Fatal("rejected outcome without an error diagnostic was accepted")
	}
}

func request(tool agentprotocol.Tool, action, projectID string) agentprotocol.Request {
	return agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		RequestID:       "r",
		Tool:            tool,
		Action:          action,
		ProjectID:       projectID,
		Payload:         json.RawMessage(`{}`),
	}
}
