package agentprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestRequestAndResultFixtures(t *testing.T) {
	request := Request{
		ProtocolVersion: 1, RequestID: "req-1", Tool: ToolProject,
		Action: "inspect", ProjectID: "demo", Payload: json.RawMessage(`{}`),
	}
	requestRaw, err := CanonicalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	assertFixture(t, "agent-request-v1.canonical.json", requestRaw)
	decodedRequest, err := DecodeRequest(requestRaw)
	if err != nil || !bytes.Equal(decodedRequest.Payload, []byte(`{}`)) {
		t.Fatalf("decoded request = %+v, %v", decodedRequest, err)
	}

	base, next := videoedit.Revision(0), videoedit.Revision(0)
	result := Result{
		ProtocolVersion: 1, OK: true, RequestID: "req-1", Tool: ToolProject,
		Action: "inspect", State: "current", ProjectID: "demo",
		BaseRevision: &base, NewRevision: &next,
		Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
	}
	resultRaw, err := CanonicalResult(result)
	if err != nil {
		t.Fatal(err)
	}
	assertFixture(t, "agent-result-v1.canonical.json", resultRaw)
	decodedResult, err := DecodeResult(resultRaw)
	if err != nil || !decodedResult.OK || decodedResult.Diagnostics == nil || decodedResult.NextActions == nil {
		t.Fatalf("decoded result = %+v, %v", decodedResult, err)
	}
}

func TestProtocolFailsClosed(t *testing.T) {
	valid := []byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_edit","action":"plan","project_id":"p","payload":{}}`)
	if _, err := DecodeRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"protocol_version":1,"request_id":"r","request_id":"other","tool":"cutly_edit","action":"plan","project_id":"p","payload":{}}`),
		[]byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_edit","action":"inspect","project_id":"p","payload":{}}`),
		[]byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_edit","action":"plan","project_id":"p","payload":{"x":1.0}}`),
		[]byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_edit","action":"plan","project_id":"p","payload":[]}`),
	} {
		if _, err := DecodeRequest(raw); !errors.Is(err, ErrRequestInvalid) {
			t.Fatalf("DecodeRequest(%s) = %v", raw, err)
		}
	}

	rejected := Result{
		ProtocolVersion: 1, OK: false, RequestID: "r", Tool: ToolEdit, Action: "apply", State: "rejected",
		ProjectID: "p", Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
	}
	if err := ValidateResult(rejected); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("rejected without diagnostic = %v", err)
	}
	rejected.Diagnostics = []videoedit.Diagnostic{{Code: "TX.REJECTED", Severity: videoedit.SeverityError, Message: "rejected"}}
	if err := ValidateResult(rejected); err != nil {
		t.Fatal(err)
	}
	rejected.OK = true
	if err := ValidateResult(rejected); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("ok with error diagnostic = %v", err)
	}
}

func TestRenderPrepareIsAProtocolAction(t *testing.T) {
	request := Request{
		ProtocolVersion: ProtocolVersion, RequestID: "prepare-1", Tool: ToolRender,
		Action: "prepare", ProjectID: "project", Payload: json.RawMessage(`{"plan":{},"resources":[]}`),
	}
	if _, err := CanonicalRequest(request); err != nil {
		t.Fatalf("prepare request rejected: %v", err)
	}
	result := Result{
		ProtocolVersion: ProtocolVersion, OK: true, RequestID: request.RequestID,
		Tool: ToolRender, Action: "prepare", State: "prepared", ProjectID: request.ProjectID,
		RenderRequired: true, Diagnostics: []videoedit.Diagnostic{},
		NextActions: []NextAction{{Tool: ToolRender, Action: "preflight", ProjectID: request.ProjectID}},
	}
	if err := ValidateResult(result); err != nil {
		t.Fatalf("prepare result rejected: %v", err)
	}
}

func TestDecodedValuesOwnRawBuffers(t *testing.T) {
	raw := []byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_edit","action":"plan","project_id":"p","payload":{"value":1}}`)
	request, err := DecodeRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	copy(request.Payload, []byte(`{"oops":`))
	again, err := DecodeRequest(raw)
	if err != nil || string(again.Payload) != `{"value":1}` {
		t.Fatalf("decode aliased caller = %s, %v", again.Payload, err)
	}
}

func TestUnicodeLengthAndNestedResultValidation(t *testing.T) {
	identity := strings.Repeat("😀", MaxIdentityScalars)
	request := Request{
		ProtocolVersion: ProtocolVersion, RequestID: identity,
		Tool: ToolProject, Action: "capabilities", Payload: json.RawMessage(`{}`),
	}
	if _, err := CanonicalRequest(request); err != nil {
		t.Fatalf("max-scalar identity rejected: %v", err)
	}
	request.RequestID += "x"
	if _, err := CanonicalRequest(request); !errors.Is(err, ErrRequestInvalid) {
		t.Fatalf("overlong identity = %v", err)
	}

	result := Result{
		ProtocolVersion: ProtocolVersion, OK: true, RequestID: "r",
		Tool: ToolEdit, Action: "apply", State: "applied", ProjectID: "p",
		Diagnostics: []videoedit.Diagnostic{{
			Code: "INFO", Severity: videoedit.SeverityInfo, Message: "ok",
			Fixes: []videoedit.CandidateFix{{
				Description: "fix", Operation: json.RawMessage(`{"op":"delete"}`), SideEffects: []string{""},
			}},
		}},
		NextActions: []NextAction{{Tool: ToolProject, Action: "inspect", ProjectID: "p"}},
	}
	if err := ValidateResult(result); err != nil {
		t.Fatal(err)
	}
	result.Diagnostics[0].Message = ""
	if err := ValidateResult(result); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("empty message = %v", err)
	}
	result.Diagnostics[0].Message = "ok"
	result.Diagnostics[0].Fixes[0].Operation = json.RawMessage(`{"value":1.5}`)
	if err := ValidateResult(result); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("fractional candidate operation = %v", err)
	}
}

func TestResultRequiresProjectScopeAndBoundedLists(t *testing.T) {
	result := Result{
		ProtocolVersion: ProtocolVersion, OK: true, RequestID: "r",
		Tool: ToolProject, Action: "inspect", State: "current",
		Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
	}
	if err := ValidateResult(result); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("unscoped result = %v", err)
	}
	result.ProjectID = "p"
	result.NextActions = []NextAction{{Tool: ToolRender, Action: "status"}}
	if err := ValidateResult(result); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("unscoped next action = %v", err)
	}
	result.NextActions = make([]NextAction, MaxListItems+1)
	if err := ValidateResult(result); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("oversized next action list = %v", err)
	}
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add([]byte(`{"protocol_version":1,"request_id":"r","tool":"cutly_project","action":"capabilities","payload":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		request, err := DecodeRequest(raw)
		if err != nil {
			return
		}
		canonical, err := CanonicalRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		again, err := canonicaljson.Canonicalize(canonical)
		if err != nil || !bytes.Equal(again, canonical) {
			t.Fatalf("canonical drift: %s, %v", canonical, err)
		}
	})
}

func assertFixture(t *testing.T, name string, actual []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), actual) {
		t.Fatalf("fixture %s drift\nwant %s\n got %s", name, bytes.TrimSpace(want), actual)
	}
}
