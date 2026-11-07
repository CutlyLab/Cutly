package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

var (
	ErrRouteInvalid   = errors.New("agent router: route is invalid")
	ErrRouteDuplicate = errors.New("agent router: route is duplicated")
)

const CodeActionUnavailable = "AGENT.ACTION_UNAVAILABLE"

type Outcome struct {
	OK             bool
	State          string
	TransactionID  string
	JobID          string
	BaseRevision   *videoedit.Revision
	NewRevision    *videoedit.Revision
	Changed        bool
	Replayed       bool
	RenderRequired bool
	Diagnostics    []videoedit.Diagnostic
	Diff           json.RawMessage
	Receipt        json.RawMessage
	NextActions    []agentprotocol.NextAction
}

type Action interface {
	Execute(context.Context, agentprotocol.Request) (Outcome, error)
}

type ActionFunc func(context.Context, agentprotocol.Request) (Outcome, error)

func (fn ActionFunc) Execute(ctx context.Context, request agentprotocol.Request) (Outcome, error) {
	return fn(ctx, request)
}

type Route struct {
	Tool    agentprotocol.Tool
	Action  string
	Handler Action
}

type routeKey struct {
	tool   agentprotocol.Tool
	action string
}

// Router is immutable after construction and safe for concurrent dispatch.
type Router struct {
	routes       map[routeKey]Action
	capabilities json.RawMessage
}

func New(routes ...Route) (*Router, error) {
	handlers := make(map[routeKey]Action, len(routes))
	for index, route := range routes {
		if route.Handler == nil || route.Action == "capabilities" {
			return nil, fmt.Errorf("%w: route %d", ErrRouteInvalid, index)
		}
		if err := validatePair(route.Tool, route.Action); err != nil {
			return nil, fmt.Errorf("%w: route %d: %v", ErrRouteInvalid, index, err)
		}
		key := routeKey{tool: route.Tool, action: route.Action}
		if _, exists := handlers[key]; exists {
			return nil, fmt.Errorf("%w: %s/%s", ErrRouteDuplicate, route.Tool, route.Action)
		}
		handlers[key] = route.Handler
	}
	capabilities, err := encodeCapabilities(handlers)
	if err != nil {
		return nil, err
	}
	return &Router{routes: handlers, capabilities: capabilities}, nil
}

func (router *Router) HandleAgent(ctx context.Context, request agentprotocol.Request) (agentprotocol.Result, error) {
	if router == nil {
		return agentprotocol.Result{}, errors.New("agent router: uninitialized")
	}
	canonicalRequest, err := agentprotocol.CanonicalRequest(request)
	if err != nil {
		return agentprotocol.Result{}, fmt.Errorf("agent router: invalid request: %w", err)
	}
	request, err = agentprotocol.DecodeRequest(canonicalRequest)
	if err != nil {
		return agentprotocol.Result{}, fmt.Errorf("agent router: refreeze request: %w", err)
	}
	if request.Tool == agentprotocol.ToolProject && request.Action == "capabilities" {
		return router.wrap(request, Outcome{
			OK:          true,
			State:       "available",
			Diagnostics: []videoedit.Diagnostic{},
			Receipt:     append(json.RawMessage(nil), router.capabilities...),
			NextActions: []agentprotocol.NextAction{},
		})
	}
	handler := router.routes[routeKey{tool: request.Tool, action: request.Action}]
	if handler == nil {
		return router.wrap(request, Outcome{
			OK:    false,
			State: "unavailable",
			Diagnostics: []videoedit.Diagnostic{{
				Code:     CodeActionUnavailable,
				Severity: videoedit.SeverityError,
				Path:     "/action",
				Message:  fmt.Sprintf("%s/%s is not installed in this daemon", request.Tool, request.Action),
			}},
			NextActions: []agentprotocol.NextAction{{Tool: agentprotocol.ToolProject, Action: "capabilities"}},
		})
	}
	outcome, err := handler.Execute(ctx, request)
	if err != nil {
		return agentprotocol.Result{}, err
	}
	return router.wrap(request, outcome)
}

func (router *Router) wrap(request agentprotocol.Request, outcome Outcome) (agentprotocol.Result, error) {
	result := agentprotocol.Result{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		OK:              outcome.OK,
		RequestID:       request.RequestID,
		Tool:            request.Tool,
		Action:          request.Action,
		State:           outcome.State,
		ProjectID:       request.ProjectID,
		TransactionID:   outcome.TransactionID,
		JobID:           outcome.JobID,
		BaseRevision:    cloneRevision(outcome.BaseRevision),
		NewRevision:     cloneRevision(outcome.NewRevision),
		Changed:         outcome.Changed,
		Replayed:        outcome.Replayed,
		RenderRequired:  outcome.RenderRequired,
		Diagnostics:     cloneDiagnostics(outcome.Diagnostics),
		Diff:            append(json.RawMessage(nil), outcome.Diff...),
		Receipt:         append(json.RawMessage(nil), outcome.Receipt...),
		NextActions:     cloneNextActions(outcome.NextActions),
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []videoedit.Diagnostic{}
	}
	if result.NextActions == nil {
		result.NextActions = []agentprotocol.NextAction{}
	}
	if err := agentprotocol.ValidateResult(result); err != nil {
		return agentprotocol.Result{}, fmt.Errorf("agent router: invalid action outcome: %w", err)
	}
	return result, nil
}

type capabilityAction struct {
	Tool   agentprotocol.Tool `json:"tool"`
	Action string             `json:"action"`
}

type capabilityReceipt struct {
	Profile         string             `json:"profile"`
	ProtocolVersion int                `json:"protocol_version"`
	Actions         []capabilityAction `json:"actions"`
}

func encodeCapabilities(routes map[routeKey]Action) (json.RawMessage, error) {
	actions := make([]capabilityAction, 0, len(routes)+1)
	actions = append(actions, capabilityAction{Tool: agentprotocol.ToolProject, Action: "capabilities"})
	for key := range routes {
		actions = append(actions, capabilityAction{Tool: key.tool, Action: key.action})
	}
	sort.Slice(actions, func(left, right int) bool {
		if actions[left].Tool != actions[right].Tool {
			return actions[left].Tool < actions[right].Tool
		}
		return actions[left].Action < actions[right].Action
	})
	encoded, err := canonicaljson.Marshal(capabilityReceipt{
		Profile: "cutly.agent-capabilities.v1", ProtocolVersion: agentprotocol.ProtocolVersion, Actions: actions,
	})
	if err != nil {
		return nil, fmt.Errorf("agent router: encode capabilities: %w", err)
	}
	return encoded, nil
}

func validatePair(tool agentprotocol.Tool, action string) error {
	projectID := "project"
	if action == "capabilities" {
		projectID = ""
	}
	_, err := agentprotocol.CanonicalRequest(agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		RequestID:       "route-validation",
		Tool:            tool,
		Action:          action,
		ProjectID:       projectID,
		Payload:         json.RawMessage(`{}`),
	})
	return err
}

func cloneRevision(value *videoedit.Revision) *videoedit.Revision {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneDiagnostics(values []videoedit.Diagnostic) []videoedit.Diagnostic {
	if values == nil {
		return nil
	}
	encoded, err := canonicaljson.Marshal(values)
	if err != nil {
		return append([]videoedit.Diagnostic(nil), values...)
	}
	var result []videoedit.Diagnostic
	if err := json.Unmarshal(encoded, &result); err != nil {
		return append([]videoedit.Diagnostic(nil), values...)
	}
	return result
}

func cloneNextActions(values []agentprotocol.NextAction) []agentprotocol.NextAction {
	if values == nil {
		return nil
	}
	result := make([]agentprotocol.NextAction, len(values))
	copy(result, values)
	for index := range result {
		result[index].Revision = cloneRevision(values[index].Revision)
	}
	return result
}
