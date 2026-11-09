// Package routerv2 dispatches the independent Document v2 Agent protocol.
package routerv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

var (
	ErrRouteInvalid   = errors.New("agent routerv2: route is invalid")
	ErrRouteDuplicate = errors.New("agent routerv2: route is duplicated")
)

const CodeActionUnavailable = "AGENT.V2.ACTION_UNAVAILABLE"

type Outcome struct {
	OK          bool
	State       string
	Diagnostics []documentv2.Diagnostic
	Receipt     json.RawMessage
}

type Action interface {
	Execute(context.Context, protocolv2.Request) (Outcome, error)
}

type ActionFunc func(context.Context, protocolv2.Request) (Outcome, error)

func (fn ActionFunc) Execute(ctx context.Context, request protocolv2.Request) (Outcome, error) {
	return fn(ctx, request)
}

type Route struct {
	Tool    protocolv2.Tool
	Action  string
	Handler Action
}

type routeKey struct {
	tool   protocolv2.Tool
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
		if route.Handler == nil || route.Action == "capabilities" || protocolv2.PayloadProfile(route.Tool, route.Action) == "" {
			return nil, fmt.Errorf("%w: route %d", ErrRouteInvalid, index)
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

func (router *Router) HandleAgentV2(ctx context.Context, request protocolv2.Request) (protocolv2.Result, error) {
	if router == nil {
		return protocolv2.Result{}, errors.New("agent routerv2: uninitialized")
	}
	canonical, err := protocolv2.CanonicalRequest(ctx, request)
	if err != nil {
		return protocolv2.Result{}, fmt.Errorf("agent routerv2: invalid request: %w", err)
	}
	request, err = protocolv2.DecodeRequest(ctx, canonical)
	if err != nil {
		return protocolv2.Result{}, fmt.Errorf("agent routerv2: refreeze request: %w", err)
	}
	if request.Tool == protocolv2.ToolProject && request.Action == "capabilities" {
		return router.wrap(ctx, request, Outcome{OK: true, State: "available", Diagnostics: []documentv2.Diagnostic{}, Receipt: append(json.RawMessage(nil), router.capabilities...)})
	}
	handler := router.routes[routeKey{tool: request.Tool, action: request.Action}]
	if handler == nil {
		return router.wrap(ctx, request, Outcome{
			State:       "unavailable",
			Diagnostics: []documentv2.Diagnostic{{Code: CodeActionUnavailable, Severity: documentv2.SeverityError, Path: "/action", Message: fmt.Sprintf("%s/%s is not installed in this daemon", request.Tool, request.Action)}},
			Receipt:     json.RawMessage(`{}`),
		})
	}
	outcome, err := handler.Execute(ctx, request)
	if err != nil {
		return protocolv2.Result{}, err
	}
	return router.wrap(ctx, request, outcome)
}

func (router *Router) wrap(ctx context.Context, request protocolv2.Request, outcome Outcome) (protocolv2.Result, error) {
	result := protocolv2.Result{
		SchemaVersion: protocolv2.SchemaVersion,
		Profile:       protocolv2.ResultProfile,
		OK:            outcome.OK,
		RequestID:     request.RequestID,
		Tool:          request.Tool,
		Action:        request.Action,
		ProjectID:     request.ProjectID,
		State:         outcome.State,
		Diagnostics:   cloneDiagnostics(outcome.Diagnostics),
		Receipt:       append(json.RawMessage(nil), outcome.Receipt...),
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []documentv2.Diagnostic{}
	}
	if len(result.Receipt) == 0 {
		result.Receipt = json.RawMessage(`{}`)
	}
	if _, err := protocolv2.CanonicalResult(ctx, result); err != nil {
		return protocolv2.Result{}, fmt.Errorf("agent routerv2: invalid action outcome: %w", err)
	}
	return result, nil
}

type capabilityAction struct {
	Tool           protocolv2.Tool `json:"tool"`
	Action         string          `json:"action"`
	PayloadProfile string          `json:"payload_profile"`
}

type capabilityReceipt struct {
	Profile       string             `json:"profile"`
	SchemaVersion int                `json:"schema_version"`
	Actions       []capabilityAction `json:"actions"`
}

func encodeCapabilities(routes map[routeKey]Action) (json.RawMessage, error) {
	actions := make([]capabilityAction, 0, len(routes)+1)
	actions = append(actions, capabilityAction{Tool: protocolv2.ToolProject, Action: "capabilities", PayloadProfile: protocolv2.PayloadProfile(protocolv2.ToolProject, "capabilities")})
	for key := range routes {
		actions = append(actions, capabilityAction{Tool: key.tool, Action: key.action, PayloadProfile: protocolv2.PayloadProfile(key.tool, key.action)})
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Tool != actions[j].Tool {
			return actions[i].Tool < actions[j].Tool
		}
		return actions[i].Action < actions[j].Action
	})
	raw, err := videoedit.CanonicalJSON(capabilityReceipt{Profile: "cutly.agent-capabilities.document-v2.v2", SchemaVersion: protocolv2.SchemaVersion, Actions: actions})
	if err != nil {
		return nil, fmt.Errorf("agent routerv2: encode capabilities: %w", err)
	}
	return raw, nil
}

func cloneDiagnostics(values []documentv2.Diagnostic) []documentv2.Diagnostic {
	if values == nil {
		return nil
	}
	result := make([]documentv2.Diagnostic, len(values))
	copy(result, values)
	return result
}
