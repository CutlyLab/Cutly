package localv2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	legacy "github.com/CutlyLab/Cutly/project/local"
)

func TestLegacyCreateCannotOverwriteV2InitializationIntent(t *testing.T) {
	ctx := context.Background()
	dir := projectDir(t)
	service, err := configure(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document := baseDocument()
	raw, err := CanonicalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	service.fault = func(point string) error {
		if point == "before_publish:"+DocumentName {
			return errors.New("interrupt first projection")
		}
		return nil
	}
	err = service.locked(ctx, true, func(session *session) error {
		return session.transition(ctx, diskState{Version: Version, DocumentID: document.ID, DocumentHash: digest(raw), Plans: []PlanSummary{}, History: []historyRef{}}, raw)
	})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("initialization outcome: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DocumentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected initial projection: %v", err)
	}
	if _, err := legacy.Create(ctx, dir, videoedit.Document{SchemaVersion: 1, ID: "legacy"}, legacy.Options{}); !errors.Is(err, legacy.ErrInvalid) {
		t.Fatalf("legacy Create: %v", err)
	}
	recovered, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := recovered.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentHash != digest(raw) || snapshot.Document.ID != document.ID || snapshot.Document.Revision != document.Revision {
		t.Fatal("recovery did not preserve original v2 identity")
	}
}
