package cutly

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestHTTPTransportRoundTrip(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") == "" {
			t.Errorf("request = %s %q", request.Method, request.Header.Get("Content-Type"))
		}
		decoded, err := agentprotocol.DecodeRequest(mustReadRequest(t, request))
		if err != nil {
			return nil, err
		}
		result, err := agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion, OK: true, RequestID: decoded.RequestID,
			Tool: decoded.Tool, Action: decoded.Action, State: "current", ProjectID: decoded.ProjectID,
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{AgentMediaType}},
			Body:       io.NopCloser(bytes.NewReader(result)),
		}, nil
	})}

	transport, err := NewHTTPTransport("http://127.0.0.1:9817/v1/agent", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(transport)
	result, err := client.Invoke(context.Background(), "r1", ToolProject, "inspect", "project", struct{}{})
	if err != nil || !result.OK {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestHTTPTransportRejectsRedirectAndInvalidEndpoint(t *testing.T) {
	transport, err := NewHTTPTransport("http://127.0.0.1:9817/v1/agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirectRequest, _ := http.NewRequest(http.MethodPost, "https://example.invalid/steal", nil)
	if err := transport.client.CheckRedirect(redirectRequest, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy = %v", err)
	}
	for _, endpoint := range []string{"", "file:///tmp/socket", "https://user@example.invalid/"} {
		if _, err := NewHTTPTransport(endpoint, nil); err == nil {
			t.Fatalf("endpoint %q accepted", endpoint)
		}
	}
}

func TestHTTPTransportAcceptsOnlyAgentJSONContentTypes(t *testing.T) {
	tests := []struct {
		contentType string
		valid       bool
	}{
		{contentType: AgentMediaType, valid: true},
		{contentType: AgentMediaType + `; profile="cutly.json.canonical-integer.v1"`, valid: true},
		{contentType: `Application/JSON; PROFILE=cutly.json.canonical-integer.v1`, valid: true},
		{contentType: "", valid: false},
		{contentType: "application/jsonp", valid: false},
		{contentType: AgentMediaType + "-evil", valid: false},
		{contentType: "text/json", valid: false},
		{contentType: "application/json; charset=utf-8", valid: false},
		{contentType: AgentMediaType + `; profile="other"`, valid: false},
		{contentType: AgentMediaType + `; profile="cutly.json.canonical-integer.v1"; profile="cutly.json.canonical-integer.v1"`, valid: false},
		{contentType: AgentMediaType + "; broken", valid: false},
		{contentType: "application/json;", valid: false},
		{contentType: AgentMediaType + `; profile*=UTF-8''cutly.json.canonical-integer.v1`, valid: false},
	}
	for _, test := range tests {
		t.Run(test.contentType, func(t *testing.T) {
			if got := agentprotocol.ValidContentType(test.contentType); got != test.valid {
				t.Fatalf("ValidContentType(%q) = %t, want %t", test.contentType, got, test.valid)
			}
		})
	}
}

func mustReadRequest(t *testing.T, request *http.Request) []byte {
	t.Helper()
	defer request.Body.Close()
	buffer, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	return buffer
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
