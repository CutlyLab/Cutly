package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

func TestCanonicalizeCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"canonicalize"}, strings.NewReader(` {"z":-0,"a":1} `), &stdout, &stderr, nil)
	if code != 0 || stdout.String() != "{\"a\":1,\"z\":0}\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestActionCommandUsesPublicProtocolAndExitCodes(t *testing.T) {
	factory := func(endpoint string) (cutly.Transport, error) {
		if endpoint != "http://test.invalid/agent" {
			t.Fatalf("endpoint = %q", endpoint)
		}
		return cutly.TransportFunc(func(_ context.Context, raw []byte) ([]byte, error) {
			request, err := agentprotocol.DecodeRequest(raw)
			if err != nil {
				return nil, err
			}
			if request.RequestID != "request-1" || request.ProjectID != "project-1" || string(request.Payload) != `{"transaction_id":"tx-1"}` {
				t.Fatalf("request = %+v, payload=%s", request, request.Payload)
			}
			return agentprotocol.CanonicalResult(agentprotocol.Result{
				ProtocolVersion: agentprotocol.ProtocolVersion,
				OK:              false,
				RequestID:       request.RequestID,
				Tool:            request.Tool,
				Action:          request.Action,
				State:           "rejected",
				ProjectID:       request.ProjectID,
				Diagnostics: []videoedit.Diagnostic{{
					Code: "TX.REJECTED", Severity: videoedit.SeverityError, Message: "rejected",
				}},
				NextActions: []agentprotocol.NextAction{},
			})
		}), nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"edit", "apply",
		"--endpoint", "http://test.invalid/agent",
		"--project", "project-1",
		"--request-id", "request-1",
		"--payload", "-",
	}, strings.NewReader(`{"transaction_id":"tx-1"}`), &stdout, &stderr, factory)
	if code != exitRejected || !strings.Contains(stdout.String(), `"ok":false`) || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCallRejectsCorrelationAndUsage(t *testing.T) {
	factory := func(string) (cutly.Transport, error) {
		return cutly.TransportFunc(func(context.Context, []byte) ([]byte, error) {
			return agentprotocol.CanonicalResult(validCLIResult("other"))
		}), nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"call", "--endpoint", "http://test.invalid"}, strings.NewReader(
		`{"protocol_version":1,"request_id":"r","tool":"cutly_project","action":"inspect","project_id":"p","payload":{}}`,
	), &stdout, &stderr, factory)
	if code != exitFailure || !strings.Contains(stderr.String(), "correlation") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"render"}, strings.NewReader(""), &stdout, &stderr, factory); code != exitUsage {
		t.Fatalf("usage code = %d", code)
	}
}

func TestTransportFactoryFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"doctor", "--endpoint", "bad", "--request-id", "r",
	}, strings.NewReader(""), &stdout, &stderr, func(string) (cutly.Transport, error) {
		return nil, errors.New("offline")
	})
	if code != exitFailure || !strings.Contains(stderr.String(), "offline") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func validCLIResult(requestID string) agentprotocol.Result {
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
