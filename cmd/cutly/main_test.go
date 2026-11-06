package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestInitCommandCreatesAndReplaysLocalProject(t *testing.T) {
	document := cliProjectDocument()
	raw, err := videoedit.CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	args := []string{"init", "--project-dir", projectDir, "--document", "-"}
	var firstOut, firstErr bytes.Buffer
	code := run(context.Background(), args, bytes.NewReader(raw), &firstOut, &firstErr, nil)
	if code != 0 || firstErr.Len() != 0 {
		t.Fatalf("first init code=%d stdout=%q stderr=%q", code, firstOut.String(), firstErr.String())
	}
	if !strings.Contains(firstOut.String(), `"profile":"cutly.cli-project-init.v1"`) {
		t.Fatalf("init receipt = %q", firstOut.String())
	}
	opened, err := cutly.OpenLocalProjectV1(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := opened.Inspect(context.Background())
	if err != nil || snapshot.Document.ID != document.ID {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}

	var replayOut, replayErr bytes.Buffer
	code = run(context.Background(), args, bytes.NewReader(raw), &replayOut, &replayErr, nil)
	if code != 0 || replayErr.Len() != 0 || replayOut.String() != firstOut.String() {
		t.Fatalf("replay code=%d stdout=%q stderr=%q", code, replayOut.String(), replayErr.String())
	}

	different := document
	different.Revision++
	differentRaw, err := videoedit.CanonicalJSON(different)
	if err != nil {
		t.Fatal(err)
	}
	var rejectedOut, rejectedErr bytes.Buffer
	code = run(context.Background(), args, bytes.NewReader(differentRaw), &rejectedOut, &rejectedErr, nil)
	if code != exitFailure || !strings.Contains(rejectedErr.String(), "project already exists") || rejectedOut.Len() != 0 {
		t.Fatalf("different init code=%d stdout=%q stderr=%q", code, rejectedOut.String(), rejectedErr.String())
	}

	documentPath := filepath.Join(root, "document.json")
	if err := os.WriteFile(documentPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fileProjectDir := filepath.Join(root, "file-project")
	var fileOut, fileErr bytes.Buffer
	code = run(context.Background(), []string{
		"init", "--project-dir", fileProjectDir, "--document", documentPath,
	}, strings.NewReader("unused"), &fileOut, &fileErr, nil)
	if code != 0 || fileErr.Len() != 0 {
		t.Fatalf("file init code=%d stdout=%q stderr=%q", code, fileOut.String(), fileErr.String())
	}
}

func TestReadInputEnforcesStdinAndFileBounds(t *testing.T) {
	if _, err := readInput("-", strings.NewReader("12345"), 4); err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("stdin bound error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInput(path, strings.NewReader("unused"), 4); err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("file bound error = %v", err)
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

func TestUsageAdvertisesRenderPrepare(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"help"}, strings.NewReader(""), &stdout, &stderr, nil); code != 0 {
		t.Fatalf("help code = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Render actions:  prepare, preflight") {
		t.Fatalf("help does not advertise prepare: %q", stdout.String())
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

func cliProjectDocument() videoedit.Document {
	duration := videoedit.RationalTime{Value: 1, Rate: 1}
	return videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "project-document", Revision: 1,
		Assets: []videoedit.Asset{{
			ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4",
			ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Duration: &duration,
		}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "video-track", Kind: videoedit.TrackKindVideo, Clips: []videoedit.Clip{{
				ID: "video-clip", AssetID: "video",
				TimelineRange: videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
				SourceRange:   videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
			}},
		}}}},
	}
}
