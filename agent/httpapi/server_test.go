package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestServerReturnsCanonicalBusinessResult(t *testing.T) {
	server := Server{Handler: HandlerFunc(func(_ context.Context, request agentprotocol.Request) (agentprotocol.Result, error) {
		return agentprotocol.Result{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			OK:              true,
			RequestID:       request.RequestID,
			Tool:            request.Tool,
			Action:          request.Action,
			State:           "current",
			ProjectID:       request.ProjectID,
			Diagnostics:     []videoedit.Diagnostic{},
			NextActions:     []agentprotocol.NextAction{},
		}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "http://cutly.invalid/v1/agent", strings.NewReader(
		`{"protocol_version":1,"request_id":"r","tool":"cutly_project","action":"inspect","project_id":"p","payload":{}}`,
	))
	request.Header.Set("Content-Type", agentprotocol.MediaType+`; profile="cutly.json.canonical-integer.v1"`)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, headers = %v, body = %s", response.Code, response.Header(), response.Body.String())
	}
	decoded, err := agentprotocol.DecodeResult(response.Body.Bytes())
	if err != nil || !decoded.OK || decoded.ProjectID != "p" {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
}

func TestServerSeparatesTransportAndBusinessFailures(t *testing.T) {
	tests := []struct {
		name        string
		server      Server
		method      string
		contentType string
		body        string
		want        int
	}{
		{name: "method", server: Server{}, method: http.MethodGet, contentType: "application/json", want: http.StatusMethodNotAllowed},
		{name: "handler absent", server: Server{}, method: http.MethodPost, contentType: "application/json", body: `{}`, want: http.StatusServiceUnavailable},
		{name: "media", server: Server{Handler: inertHandler()}, method: http.MethodPost, contentType: "text/plain", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "invalid", server: Server{Handler: inertHandler()}, method: http.MethodPost, contentType: "application/json", body: `{}`, want: http.StatusBadRequest},
		{name: "handler error", server: Server{Handler: HandlerFunc(func(context.Context, agentprotocol.Request) (agentprotocol.Result, error) {
			return agentprotocol.Result{}, errors.New("secret")
		})}, method: http.MethodPost, contentType: "application/json", body: validRequest(), want: http.StatusInternalServerError},
		{name: "mismatch", server: Server{Handler: HandlerFunc(func(context.Context, agentprotocol.Request) (agentprotocol.Result, error) {
			return validResult("other"), nil
		})}, method: http.MethodPost, contentType: "application/json", body: validRequest(), want: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://cutly.invalid/v1/agent", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			test.server.ServeHTTP(response, request)
			if response.Code != test.want || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestServerRejectsOversizedBeforeHandler(t *testing.T) {
	called := false
	server := Server{Handler: HandlerFunc(func(context.Context, agentprotocol.Request) (agentprotocol.Result, error) {
		called = true
		return agentprotocol.Result{}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "http://cutly.invalid/v1/agent", strings.NewReader("{}"))
	request.ContentLength = agentprotocol.MaxRequestBytes + 1
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("status = %d, called = %t", response.Code, called)
	}
}

func TestValidContentTypeRequiresTheAgentJSONContract(t *testing.T) {
	tests := []struct {
		contentType string
		valid       bool
	}{
		{contentType: agentprotocol.MediaType, valid: true},
		{contentType: agentprotocol.MediaType + `; profile="cutly.json.canonical-integer.v1"`, valid: true},
		{contentType: `Application/JSON; PROFILE=cutly.json.canonical-integer.v1`, valid: true},
		{contentType: "", valid: false},
		{contentType: "application/jsonp", valid: false},
		{contentType: agentprotocol.MediaType + "-evil", valid: false},
		{contentType: "application/json; charset=utf-8", valid: false},
		{contentType: agentprotocol.MediaType + `; profile="other"`, valid: false},
		{contentType: agentprotocol.MediaType + `; profile="cutly.json.canonical-integer.v1"; profile="cutly.json.canonical-integer.v1"`, valid: false},
		{contentType: agentprotocol.MediaType + "; broken", valid: false},
		{contentType: "application/json;", valid: false},
		{contentType: agentprotocol.MediaType + `; profile*=UTF-8''cutly.json.canonical-integer.v1`, valid: false},
	}
	for _, test := range tests {
		t.Run(test.contentType, func(t *testing.T) {
			if got := agentprotocol.ValidContentType(test.contentType); got != test.valid {
				t.Fatalf("ValidContentType(%q) = %t, want %t", test.contentType, got, test.valid)
			}
		})
	}
}

func inertHandler() Handler {
	return HandlerFunc(func(context.Context, agentprotocol.Request) (agentprotocol.Result, error) {
		panic("must not be called")
	})
}

func validRequest() string {
	return `{"protocol_version":1,"request_id":"r","tool":"cutly_project","action":"inspect","project_id":"p","payload":{}}`
}

func validResult(requestID string) agentprotocol.Result {
	return agentprotocol.Result{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		OK:              true,
		RequestID:       requestID,
		Tool:            agentprotocol.ToolProject,
		Action:          "inspect",
		State:           "current",
		ProjectID:       "p",
		Diagnostics:     []videoedit.Diagnostic{},
		NextActions:     []agentprotocol.NextAction{},
	}
}
