package localv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	tx "github.com/CutlyLab/Cutly/core/transactionv2"
)

func TestOpenMetadataArrayNamesRemainOpen(t *testing.T) {
	ctx := context.Background()
	large := bytes.Repeat([]byte("0,"), 4096)
	large = append(large, '0')
	blob := append([]byte(`{"operations":[`), large...)
	blob = append(blob, []byte(`],"plans":[`)...)
	blob = append(blob, large...)
	blob = append(blob, []byte(`],"history":[`)...)
	blob = append(blob, large...)
	blob = append(blob, ']', '}')
	document := baseDocument()
	document.Metadata["blob"] = json.RawMessage(blob)
	if diagnostics := d.Validate(document); d.HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	if _, err := CanonicalDocument(document); err != nil {
		t.Fatal("open document metadata rejected", err)
	}
	s, err := Create(ctx, projectDir(t), document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	proposal := metadataTransaction(t, s, "open-names", "second", string(blob))
	if result, err := s.Plan(ctx, proposal); err != nil || !result.OK {
		t.Fatalf("%#v %v", result, err)
	}
	if result, err := s.Apply(ctx, proposal.ID); err != nil || !result.OK {
		t.Fatalf("%#v %v", result, err)
	}
	reopened, err := New(s.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Document.Metadata["blob"]; !ok {
		t.Fatal("metadata lost")
	}
	// The same names at actual model paths still fail before typed allocation.
	array := append([]byte("["), bytes.Repeat([]byte("{},"), 4096)...)
	array = append(array, '{', '}', ']')
	for _, item := range []struct {
		raw   []byte
		model any
	}{
		{append(append([]byte(`{"operations":`), array...), '}'), tx.Transaction{}},
		{append(append([]byte(`{"transaction":{"operations":`), array...), '}', '}'), PlanRecord{}},
		{append(append([]byte(`{"inverse":{"operations":`), array...), '}', '}'), CommitRecord{}},
		{append(append([]byte(`{"plans":`), array...), '}'), diskState{}},
		{append(append([]byte(`{"history":`), array...), '}'), diskState{}},
	} {
		if err := scan(item.raw, item.model); !errors.Is(err, ErrLimit) {
			t.Fatalf("typed cap missing for %T: %v", item.model, err)
		}
	}
}

func TestCurrentSnapshotDependencyRequiredWithoutPlans(t *testing.T) {
	s := createTest(t)
	ctx := context.Background()
	status, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name, err := objectName("documents", status.DocumentHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.dir, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("open accepted missing snapshot", err)
	}
	if _, err := s.Status(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatal("status accepted missing snapshot", err)
	}
	if _, err := s.Inspect(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatal("inspect accepted missing snapshot", err)
	}
	if _, err := Create(ctx, s.dir, baseDocument(), Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("create retry accepted missing snapshot", err)
	}
}
