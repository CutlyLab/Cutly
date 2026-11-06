package cutly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
)

var (
	// ErrTransport means no authenticated Agent result was obtained. Business
	// rejection is represented by Result.OK=false and is not a transport error.
	ErrTransport = errors.New("cutly sdk: transport failure")
	// ErrCorrelation means the peer returned a valid result for a different
	// request. Such a result must never be accepted by a retrying caller.
	ErrCorrelation = errors.New("cutly sdk: response correlation mismatch")
)

type Request = agentprotocol.Request
type Result = agentprotocol.Result
type Tool = agentprotocol.Tool
type NextAction = agentprotocol.NextAction

const (
	ProtocolVersion = agentprotocol.ProtocolVersion
	ToolProject     = agentprotocol.ToolProject
	ToolEdit        = agentprotocol.ToolEdit
	ToolRender      = agentprotocol.ToolRender
)

// Transport exchanges one canonical request for one encoded result. A
// transport must return an owned response buffer and honor ctx cancellation.
// It must not interpret Result.OK; that is business state, not connectivity.
type Transport interface {
	RoundTrip(ctx context.Context, canonicalRequest []byte) ([]byte, error)
}

// TransportFunc adapts an embedding function into a Transport.
type TransportFunc func(context.Context, []byte) ([]byte, error)

func (fn TransportFunc) RoundTrip(ctx context.Context, request []byte) ([]byte, error) {
	return fn(ctx, request)
}

// Client is safe for concurrent use when its Transport is safe for concurrent
// use. Request IDs are supplied by callers so retries can retain an exact,
// auditable identity; the SDK never invents random IDs.
type Client struct {
	transport Transport
}

func NewClient(transport Transport) (*Client, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: nil transport", ErrTransport)
	}
	return &Client{transport: transport}, nil
}

// NewRequest constructs a request with an owned canonical-profile object
// payload. Validation still occurs in Call, allowing callers to assemble a
// batch before doing I/O.
func NewRequest(requestID string, tool Tool, action, projectID string, payload any) (Request, error) {
	if payload == nil {
		payload = struct{}{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Request{}, fmt.Errorf("cutly sdk: encode payload: %w", err)
	}
	request := Request{
		ProtocolVersion: ProtocolVersion,
		RequestID:       requestID,
		Tool:            tool,
		Action:          action,
		ProjectID:       projectID,
		Payload:         append(json.RawMessage(nil), raw...),
	}
	if _, err := agentprotocol.CanonicalRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Call validates and canonicalizes request, performs exactly one transport
// exchange, validates the result and proves response correlation. An
// application may safely retry with the same RequestID and payload according
// to the action's idempotency contract.
func (client *Client) Call(ctx context.Context, request Request) (Result, error) {
	if client == nil || client.transport == nil {
		return Result{}, fmt.Errorf("%w: uninitialized client", ErrTransport)
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: nil context", ErrTransport)
	}
	canonical, err := agentprotocol.CanonicalRequest(request)
	if err != nil {
		return Result{}, err
	}
	response, err := client.transport.RoundTrip(ctx, append([]byte(nil), canonical...))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	result, err := agentprotocol.DecodeResult(response)
	if err != nil {
		return Result{}, fmt.Errorf("%w: decode result: %v", ErrTransport, err)
	}
	if result.RequestID != request.RequestID || result.Tool != request.Tool || result.Action != request.Action {
		return Result{}, fmt.Errorf(
			"%w: got request_id=%q tool=%q action=%q, want request_id=%q tool=%q action=%q",
			ErrCorrelation,
			result.RequestID, result.Tool, result.Action,
			request.RequestID, request.Tool, request.Action,
		)
	}
	if request.ProjectID != "" && result.ProjectID != request.ProjectID {
		return Result{}, fmt.Errorf("%w: got project_id=%q, want %q", ErrCorrelation, result.ProjectID, request.ProjectID)
	}
	return result, nil
}

// Call constructs and executes a request in one step.
func (client *Client) Invoke(ctx context.Context, requestID string, tool Tool, action, projectID string, payload any) (Result, error) {
	request, err := NewRequest(requestID, tool, action, projectID, payload)
	if err != nil {
		return Result{}, err
	}
	return client.Call(ctx, request)
}
