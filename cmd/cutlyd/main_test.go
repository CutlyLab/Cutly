package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
)

func TestConfigAllowsOnlyExplicitLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9817", "[::1]:9817", "localhost:9817"} {
		if _, err := parseConfig([]string{"--listen", address}); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{":9817", "0.0.0.0:9817", "[::]:9817", "example.com:9817", "bad"} {
		if _, err := parseConfig([]string{"--listen", address}); err == nil {
			t.Fatalf("%s accepted", address)
		}
	}
	for _, args := range [][]string{
		{"--project-id", "project"},
		{"--project-dir", t.TempDir()},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Fatalf("unpaired project config accepted: %v", args)
		}
	}
}

func TestMuxHealthAndCapabilities(t *testing.T) {
	actionRouter, err := router.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := newMux(actionRouter)

	healthRequest := httptest.NewRequest(http.MethodGet, "http://cutly.invalid/healthz", nil)
	healthResponse := httptest.NewRecorder()
	mux.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health = %d %s", healthResponse.Code, healthResponse.Body.String())
	}

	agentRequest := httptest.NewRequest(http.MethodPost, "http://cutly.invalid/v1/agent", strings.NewReader(
		`{"protocol_version":1,"request_id":"doctor","tool":"cutly_project","action":"capabilities","payload":{}}`,
	))
	agentRequest.Header.Set("Content-Type", "application/json")
	agentResponse := httptest.NewRecorder()
	mux.ServeHTTP(agentResponse, agentRequest)
	if agentResponse.Code != http.StatusOK {
		t.Fatalf("agent = %d %s", agentResponse.Code, agentResponse.Body.String())
	}
	result, err := agentprotocol.DecodeResult(agentResponse.Body.Bytes())
	if err != nil || !result.OK {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil || receipt["profile"] != "cutly.agent-capabilities.v1" {
		t.Fatalf("receipt = %s, err = %v", result.Receipt, err)
	}
}

func TestServeRequiresDependencies(t *testing.T) {
	if err := serve(nil, config{}, nil, nil); err == nil {
		t.Fatal("nil dependencies accepted")
	}
	if err := validateLoopbackAddress("127.0.0.1"); err == nil {
		t.Fatal("missing port accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx // Context behavior is covered by integration builds; tests do not bind sockets.
}
