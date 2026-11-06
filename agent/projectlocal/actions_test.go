package projectlocal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/project/local"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestProjectAndEditRoutesLifecycle(t *testing.T) {
	project := createProject(t, fixtureDocument())
	actionRouter := createRouter(t, map[string]*local.Service{"project-1": project})

	inspected := call(t, actionRouter, agentprotocol.ToolProject, "inspect", "project-1", emptyPayload{})
	if !inspected.OK || inspected.State != "inspected" || inspected.BaseRevision == nil || *inspected.BaseRevision != 3 {
		t.Fatalf("inspect = %+v", inspected)
	}
	assertReceiptProfile(t, inspected.Receipt, "cutly.project-inspect.v1")

	transaction := moveTransaction("tx-move", 3, 2)
	planned := call(t, actionRouter, agentprotocol.ToolEdit, "plan", "project-1", planPayload{Transaction: transaction})
	if !planned.OK || planned.State != "planned" || planned.TransactionID != transaction.ID || planned.Changed || planned.NewRevision == nil || *planned.NewRevision != 4 {
		t.Fatalf("plan = %+v", planned)
	}
	if len(planned.NextActions) != 2 || planned.NextActions[0].Action != "apply" || planned.NextActions[1].Action != "discard" {
		t.Fatalf("plan next actions = %+v", planned.NextActions)
	}

	applied := call(t, actionRouter, agentprotocol.ToolEdit, "apply", "project-1", revisionPayload{
		TransactionID: transaction.ID, ExpectedRevision: 3,
	})
	if !applied.OK || !applied.Changed || !applied.RenderRequired || applied.NewRevision == nil || *applied.NewRevision != 4 {
		t.Fatalf("apply = %+v", applied)
	}
	assertReceiptProfile(t, applied.Receipt, "cutly.project-mutation.v1")

	replayed := call(t, actionRouter, agentprotocol.ToolEdit, "apply", "project-1", revisionPayload{
		TransactionID: transaction.ID, ExpectedRevision: 3,
	})
	if !replayed.OK || !replayed.Replayed || replayed.Changed || replayed.RenderRequired {
		t.Fatalf("apply replay = %+v", replayed)
	}

	history := call(t, actionRouter, agentprotocol.ToolProject, "history", "project-1", historyPayload{Limit: 10})
	if !history.OK {
		t.Fatalf("history = %+v", history)
	}
	var historyValue struct {
		Profile string `json:"profile"`
		Records []struct {
			Transaction struct {
				ID string `json:"id"`
			} `json:"transaction"`
		} `json:"records"`
	}
	if err := json.Unmarshal(history.Receipt, &historyValue); err != nil || len(historyValue.Records) != 1 || historyValue.Records[0].Transaction.ID != transaction.ID {
		t.Fatalf("history receipt = %s, err=%v", history.Receipt, err)
	}

	reverted := call(t, actionRouter, agentprotocol.ToolEdit, "revert", "project-1", revisionPayload{
		TransactionID: transaction.ID, ExpectedRevision: 4,
	})
	if !reverted.OK || !reverted.Changed || !reverted.RenderRequired || reverted.NewRevision == nil || *reverted.NewRevision != 5 {
		t.Fatalf("revert = %+v", reverted)
	}
	status := call(t, actionRouter, agentprotocol.ToolProject, "status", "project-1", emptyPayload{})
	if !status.OK || status.BaseRevision == nil || *status.BaseRevision != 5 {
		t.Fatalf("status = %+v", status)
	}
}

func TestRoutesRejectUnknownProjectAndStrictPayload(t *testing.T) {
	actionRouter := createRouter(t, map[string]*local.Service{"known": createProject(t, fixtureDocument())})
	unknown := call(t, actionRouter, agentprotocol.ToolProject, "status", "missing", emptyPayload{})
	if unknown.OK || unknown.Diagnostics[0].Code != CodeProjectNotFound {
		t.Fatalf("unknown = %+v", unknown)
	}

	result, err := actionRouter.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "request-bad", Tool: agentprotocol.ToolProject,
		Action: "status", ProjectID: "known", Payload: json.RawMessage(`{"unexpected":true}`),
	})
	if err != nil || result.OK || result.Diagnostics[0].Code != CodePayloadInvalid {
		t.Fatalf("strict payload result = %+v, err=%v", result, err)
	}

	_, err = actionRouter.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "request-fraction", Tool: agentprotocol.ToolEdit,
		Action: "plan", ProjectID: "known", Payload: json.RawMessage(`{"transaction":{"id":"x","idempotency_key":"x","expected_revision":3,"operations":[],"extra":1.5}}`),
	})
	if !errors.Is(err, agentprotocol.ErrRequestInvalid) {
		t.Fatalf("direct noncanonical request error = %v", err)
	}
}

func TestMutationRejectsLegacyFractionBeforeWritingPlan(t *testing.T) {
	document := fixtureDocument()
	document.Metadata = map[string]json.RawMessage{"legacy_fraction": json.RawMessage(`1.5`)}
	project := createProject(t, document)
	actionRouter := createRouter(t, map[string]*local.Service{"legacy": project})

	result := call(t, actionRouter, agentprotocol.ToolEdit, "plan", "legacy", planPayload{
		Transaction: moveTransaction("tx-legacy", 3, 2),
	})
	if result.OK || result.Diagnostics[0].Code != CodeCanonicalProfileUnsupported {
		t.Fatalf("legacy mutation = %+v", result)
	}
	status, err := project.Status(context.Background())
	if err != nil || status.PendingPlans != 0 || status.Revision != 3 {
		t.Fatalf("legacy project mutated: %+v, err=%v", status, err)
	}
}

func TestRegistryOwnsMapAndCancellation(t *testing.T) {
	project := createProject(t, fixtureDocument())
	projects := map[string]*local.Service{"one": project}
	registry, err := NewRegistry(projects)
	if err != nil {
		t.Fatal(err)
	}
	delete(projects, "one")
	if resolved, err := registry.ResolveProject(context.Background(), "one"); err != nil || resolved != project {
		t.Fatalf("owned registry = %p, %v", resolved, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveProject(ctx, "one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolve = %v", err)
	}
	if _, err := NewRegistry(map[string]*local.Service{"": project}); err == nil {
		t.Fatal("blank project ID accepted")
	}
}

func createProject(t *testing.T, document videoedit.Document) *local.Service {
	t.Helper()
	service, err := local.Create(context.Background(), t.TempDir(), document, local.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func createRouter(t *testing.T, projects map[string]*local.Service) *router.Router {
	t.Helper()
	registry, err := NewRegistry(projects)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(registry)
	if err != nil {
		t.Fatal(err)
	}
	value, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func call(t *testing.T, value *router.Router, tool agentprotocol.Tool, action, projectID string, payload any) agentprotocol.Result {
	t.Helper()
	encoded, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "request-" + action,
		Tool: tool, Action: action, ProjectID: projectID, Payload: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentprotocol.CanonicalResult(result); err != nil {
		t.Fatalf("invalid result: %+v: %v", result, err)
	}
	return result
}

func assertReceiptProfile(t *testing.T, raw json.RawMessage, profile string) {
	t.Helper()
	var value struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.Profile != profile {
		t.Fatalf("receipt profile = %q, want %q, raw=%s, err=%v", value.Profile, profile, raw, err)
	}
}

func fixtureDocument() videoedit.Document {
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	ten := videoedit.RationalTime{Value: 10, Rate: 1}
	twenty := videoedit.RationalTime{Value: 20, Rate: 1}
	return videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "doc-1", Revision: 3,
		Assets: []videoedit.Asset{{ID: "asset-1", Kind: videoedit.AssetKindVideo, URI: "footage.mp4", Duration: &twenty}},
		Timelines: []videoedit.Timeline{{ID: "timeline-1", Tracks: []videoedit.Track{{
			ID: "video-1", Kind: videoedit.TrackKindVideo,
			Clips: []videoedit.Clip{{
				ID: "clip-1", AssetID: "asset-1",
				TimelineRange: videoedit.TimeRange{Start: zero, Duration: ten},
				SourceRange:   videoedit.TimeRange{Start: zero, Duration: ten},
			}},
		}}}},
	}
}

func moveTransaction(id string, revision videoedit.Revision, at int64) videoedit.Transaction {
	position := videoedit.RationalTime{Value: at, Rate: 1}
	return videoedit.Transaction{
		ID: id, IdempotencyKey: "key-" + id, ExpectedRevision: revision,
		Operations: []videoedit.Operation{{ID: "move-" + id, Kind: videoedit.OperationMove, ClipID: "clip-1", At: &position}},
	}
}
