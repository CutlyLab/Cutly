package cutly

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicLocalProjectV2Lifecycle(t *testing.T) {
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
	project, err := CreateLocalProjectV2(context.Background(), root, document)
	if err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(`"public-sdk"`)
	proposal := TransactionV2{ID: "public-sdk-plan", IdempotencyKey: "public-sdk-key", Operations: []OperationV2{{ID: "metadata", Kind: OperationMetadataSetV2, Metadata: &MetadataEditV2{Scope: ScopeV2{Kind: ScopeDocumentV2, ID: document.ID}, Key: "sdk", Value: value}}}}
	transaction, err := CreateTransactionV2(document, proposal)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := project.Plan(context.Background(), transaction)
	if err != nil || !plan.OK || plan.Plan == nil || plan.Plan.State != LocalProjectPlanPendingV2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	commit, err := project.Apply(context.Background(), transaction.ID)
	if err != nil || !commit.OK || commit.Commit == nil || !commit.Changed {
		t.Fatalf("commit=%+v err=%v", commit, err)
	}
	status, err := project.Status(context.Background())
	if err != nil || status.Revision != document.Revision+1 || status.HistoryCount != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	history, err := project.History(context.Background(), 0, 0)
	if err != nil || len(history) != 1 || history[0].TransactionID != transaction.ID {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	reverted, err := project.Revert(context.Background(), transaction.ID, "public-sdk-revert", "public-sdk-revert-key")
	if err != nil || !reverted.OK || reverted.Commit == nil || !reverted.Changed {
		t.Fatalf("revert=%+v err=%v", reverted, err)
	}
}

func TestPublicLocalProjectV2NilHandleFailsClosed(t *testing.T) {
	var project *LocalProjectV2
	if _, err := project.Inspect(context.Background()); !errors.Is(err, ErrLocalProjectV2Unavailable) {
		t.Fatalf("nil project error=%v", err)
	}
}
