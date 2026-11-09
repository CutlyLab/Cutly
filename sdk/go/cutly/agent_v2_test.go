package cutly

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/agent/httpapiv2"
	"github.com/CutlyLab/Cutly/agent/projectlocalv2"
	"github.com/CutlyLab/Cutly/agent/routerv2"
	localprojectv2 "github.com/CutlyLab/Cutly/project/localv2"
)

func TestPublicAgentV2ProjectEditOverHTTP(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := DecodeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := localprojectv2.Create(ctx, root, document, localprojectv2.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := projectlocalv2.NewRegistry(map[string]*localprojectv2.Service{"project": project})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := projectlocalv2.New(registry)
	if err != nil {
		t.Fatal(err)
	}
	router, err := routerv2.New(actions.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapiv2.Server{Handler: router})
	defer server.Close()
	transport, err := NewAgentHTTPTransportV2(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAgentClientV2(transport)
	if err != nil {
		t.Fatal(err)
	}

	status, err := client.ProjectStatus(ctx, "status", "project")
	if err != nil || !status.OK || status.State != "available" {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	transaction, err := CreateTransactionV2(document, TransactionV2{
		ID: "agent-edit", IdempotencyKey: "agent-edit-key",
		Operations: []OperationV2{{ID: "metadata", Kind: OperationMetadataSetV2, Metadata: &MetadataEditV2{Scope: ScopeV2{Kind: ScopeDocumentV2, ID: document.ID}, Key: "agent", Value: json.RawMessage(`1.2500`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := client.PlanEdit(ctx, "plan", "project", transaction)
	if err != nil || !planned.OK || planned.State != "planned" {
		t.Fatalf("planned=%+v error=%v", planned, err)
	}
	applied, err := client.ApplyEdit(ctx, "apply", "project", transaction.ID)
	if err != nil || !applied.OK || applied.State != "applied" {
		t.Fatalf("applied=%+v error=%v", applied, err)
	}
	after, err := project.Inspect(ctx)
	if err != nil || string(after.Document.Metadata["agent"]) != `1.2500` {
		t.Fatalf("snapshot=%+v error=%v", after, err)
	}
}

func TestPublicAgentV2RejectsUncorrelatedResult(t *testing.T) {
	transport := TransportFunc(func(ctx context.Context, request []byte) ([]byte, error) {
		decoded, err := DecodeAgentRequestV2(ctx, request)
		if err != nil {
			return nil, err
		}
		return CanonicalAgentResultV2(ctx, AgentResultV2{
			SchemaVersion: AgentSchemaVersionV2, Profile: AgentResultProfileV2, OK: true,
			RequestID: "different", Tool: decoded.Tool, Action: decoded.Action, ProjectID: decoded.ProjectID,
			State: "available", Diagnostics: []Diagnostic{}, Receipt: json.RawMessage(`{}`),
		})
	})
	client, err := NewAgentClientV2(transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProjectStatus(context.Background(), "request", "project"); err == nil {
		t.Fatal("uncorrelated result accepted")
	}
}
