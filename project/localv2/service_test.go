package localv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	d "github.com/CutlyLab/Cutly/core/documentv2"
	tx "github.com/CutlyLab/Cutly/core/transactionv2"
)

func projectDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func baseDocument() d.Document {
	return d.Document{SchemaVersion: 2, ID: "document", Metadata: map[string]json.RawMessage{"fraction": json.RawMessage(`0.1250000000000000000001`)}}
}
func createTest(t *testing.T) *Service {
	t.Helper()
	s, err := Create(context.Background(), projectDir(t), baseDocument(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func metadataTransaction(t *testing.T, s *Service, id, key, value string) tx.Transaction {
	t.Helper()
	snap, err := s.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := tx.Create(snap.Document, tx.Transaction{ID: id, IdempotencyKey: "key-" + id, Operations: []tx.Operation{{ID: "op-" + id, Kind: tx.MetadataSet, Metadata: &tx.MetadataEdit{Scope: tx.Scope{Kind: tx.ScopeDocument, ID: snap.Document.ID}, Key: key, Value: json.RawMessage(value)}}}})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}
func commitMetadata(t *testing.T, s *Service, id, key, value string) MutationResult {
	t.Helper()
	p := metadataTransaction(t, s, id, key, value)
	planned, err := s.Plan(context.Background(), p)
	if err != nil || !planned.OK {
		t.Fatalf("plan: %#v %v", planned, err)
	}
	out, err := s.Apply(context.Background(), id)
	if err != nil || !out.OK || !out.Changed {
		t.Fatalf("apply: %#v %v", out, err)
	}
	return out
}

func TestLifecycleAndOwnedNumbers(t *testing.T) {
	ctx := context.Background()
	s := createTest(t)
	initial, err := s.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx1 := metadataTransaction(t, s, "first", "first", `{"raw":1.234567890123456789,"huge":184467440737095516160000}`)
	planned, err := s.Plan(ctx, tx1)
	if err != nil || !planned.OK || planned.Changed {
		t.Fatalf("%#v %v", planned, err)
	}
	same, err := s.Plan(ctx, tx1)
	if err != nil || !same.Replayed {
		t.Fatalf("%#v %v", same, err)
	}
	tx1.Operations[0].Metadata.Value[0] = '[' // persistence owns the caller's input
	applied, err := s.Apply(ctx, "first")
	if err != nil || !applied.OK || applied.Commit.NewRevision != 1 {
		t.Fatalf("%#v %v", applied, err)
	}
	replay, err := s.Apply(ctx, "first")
	if err != nil || !replay.Replayed || replay.Changed {
		t.Fatalf("%#v %v", replay, err)
	}
	reopened, err := New(s.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := reopened.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(snap.Document.Metadata["fraction"]) != string(initial.Document.Metadata["fraction"]) || !bytes.Contains(snap.Document.Metadata["first"], []byte("1.234567890123456789")) {
		t.Fatal("numbers lost")
	}
	snap.Document.Metadata["first"][0] = '['
	if _, err := reopened.Inspect(ctx); err != nil {
		t.Fatal("snapshot aliased", err)
	}
	history, err := s.History(ctx, 0, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("%#v %v", history, err)
	}
	raw, err := EncodeCommitRecord(history[0])
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCommitRecord(raw)
	if err != nil || !reflect.DeepEqual(decoded, history[0]) {
		t.Fatal(err)
	}
	if _, err := s.History(ctx, 0, MaxHistoryPage+1); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, "../../bad"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatal(err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Revision != 1 || status.HistoryCount != 1 || status.AppliedPlans != 1 {
		t.Fatalf("%#v %v", status, err)
	}
}

func TestCASIdempotencyAndDiscard(t *testing.T) {
	ctx := context.Background()
	s := createTest(t)
	first := metadataTransaction(t, s, "a", "a", `1.5`)
	second := metadataTransaction(t, s, "b", "b", `2.5`)
	for _, p := range []tx.Transaction{first, second} {
		r, e := s.Plan(ctx, p)
		if e != nil || !r.OK {
			t.Fatalf("%#v %v", r, e)
		}
	}
	changed := first
	changed.Operations = append([]tx.Operation{}, first.Operations...)
	copy := *first.Operations[0].Metadata
	copy.Value = json.RawMessage(`7.5`)
	changed.Operations[0].Metadata = &copy
	changed.Digest = ""
	changed, err := tx.Create(baseDocument(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changed.ID = "new-id"
	changed.Digest = ""
	changed, err = tx.Create(baseDocument(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, "b"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if r, err := s.Discard(ctx, "b"); err != nil || !r.OK {
		t.Fatalf("%#v %v", r, err)
	}
	if r, err := s.Discard(ctx, "b"); err != nil || !r.Replayed {
		t.Fatalf("%#v %v", r, err)
	}
	if _, err := s.Apply(ctx, "b"); !errors.Is(err, ErrDiscarded) {
		t.Fatal(err)
	}
	if _, err := s.Discard(ctx, "a"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	status, err := s.Status(ctx)
	if err != nil || status.Revision != 1 || status.DiscardedPlans != 1 {
		t.Fatalf("%#v %v", status, err)
	}
}

func TestRevertKeepsUnrelatedAndConflictsOnPostimage(t *testing.T) {
	ctx := context.Background()
	s := createTest(t)
	commitMetadata(t, s, "first", "a", `1.25`)
	commitMetadata(t, s, "later", "b", `2.75`)
	reverted, err := s.Revert(ctx, "first", "undo-first", "undo-key")
	if err != nil || !reverted.OK || !reverted.Changed {
		t.Fatalf("%#v %v", reverted, err)
	}
	snap, err := s.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snap.Document.Metadata["a"]; exists || string(snap.Document.Metadata["b"]) != "2.75" || snap.Document.Revision != 3 {
		t.Fatalf("%#v", snap.Document)
	}
	retry, err := s.Revert(ctx, "first", "undo-first", "undo-key")
	if err != nil || !retry.Replayed {
		t.Fatalf("%#v %v", retry, err)
	}
	history, err := s.History(ctx, 0, 0)
	if err != nil || len(history) != 3 || history[2].RevertsTransactionID != "first" {
		t.Fatalf("%#v %v", history, err)
	}
	commitMetadata(t, s, "set-a", "a", `7.25`)
	commitMetadata(t, s, "overwrite-a", "a", `8.5`)
	before, _ := s.Status(ctx)
	result, err := s.Revert(ctx, "set-a", "bad-undo", "bad-key")
	if err != nil || result.OK || !d.HasErrorDiagnostics(result.Diagnostics) {
		t.Fatalf("%#v %v", result, err)
	}
	after, _ := s.Status(ctx)
	if before != after {
		t.Fatal("failed revert mutated state")
	}
}

func TestConcurrentCAS(t *testing.T) {
	s := createTest(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		if r, e := s.Plan(ctx, metadataTransaction(t, s, id, id, `0.5`)); e != nil || !r.OK {
			t.Fatalf("%#v %v", r, e)
		}
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			other, e := New(s.dir, Options{})
			if e == nil {
				_, e = other.Apply(ctx, id)
			}
			results <- e
		}(id)
	}
	wait.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("%d %d", success, conflicts)
	}
}

func TestRejectLegacySymlinksAndProjectionTamper(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{".cutly/state.json", ".cutly/initialized", ".cutly/commit.intent.json", DocumentName} {
		t.Run(name, func(t *testing.T) {
			dir := projectDir(t)
			if err := os.MkdirAll(filepath.Join(dir, ".cutly"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Create(ctx, dir, baseDocument(), Options{}); !errors.Is(err, ErrProjectExists) {
				t.Fatal(err)
			}
		})
	}
	s := createTest(t)
	if err := os.Remove(filepath.Join(s.dir, DocumentName)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projectDir(t), "doc.json")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(s.dir, DocumentName)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.dir, Options{}); err == nil {
		t.Fatal("followed symlink")
	}
	s = createTest(t)
	raw, err := CanonicalDocument(d.Document{SchemaVersion: 2, ID: "document", Revision: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, DocumentName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s = createTest(t)
	if err := os.Rename(filepath.Join(s.dir, ".cutly/v2/plans"), filepath.Join(s.dir, "hidden")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.dir, "hidden"), filepath.Join(s.dir, ".cutly/v2/plans")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.dir, Options{}); err == nil {
		t.Fatal("accepted directory symlink")
	}
}

func TestAdmissionAndStrictCodecs(t *testing.T) {
	l := DefaultLimits()
	l.MaxDocumentBytes = 100
	dir := projectDir(t)
	if _, err := Create(context.Background(), dir, baseDocument(), Options{Limits: l}); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if _, err := configure("relative", Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	l = DefaultLimits()
	l.MaxOperations++
	if _, err := configure(dir, Options{Limits: l}); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if DefaultLimits().MaxOperations != tx.MaxOperations {
		t.Fatal("limits drift")
	}
	for _, raw := range []string{`{"version":2}`, `{"version":2,"version":2}`, `null`, strings.Repeat("[", 65) + strings.Repeat("]", 65)} {
		if out, err := DecodePlanRecord([]byte(raw)); err == nil || !reflect.DeepEqual(out, PlanRecord{}) {
			t.Fatalf("nonzero %#v %v", out, err)
		}
	}
	if _, err := canonical(map[string]any{"raw": json.RawMessage(`{"a":0.25,"a":0.5}`)}, 1024); err == nil {
		t.Fatal("duplicate key accepted")
	}
	for _, id := range []string{"../outside", "a/b", "sha256:not-a-hash"} {
		if _, err := objectName("plans", id); err == nil {
			t.Fatal("input-derived filename")
		}
	}
	s := createTest(t)
	proposal := metadataTransaction(t, s, "m", "x", `0.125`)
	proposal.Operations = make([]tx.Operation, tx.MaxOperations+1)
	if _, err := s.Plan(context.Background(), proposal); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func TestDocumentBeyondLegacySize(t *testing.T) {
	if testing.Short() {
		t.Skip("large document")
	}
	document := baseDocument()
	document.Metadata["large"] = json.RawMessage(`"` + strings.Repeat("x", 17<<20) + `"`)
	raw, err := CanonicalDocument(document)
	if err != nil || len(raw) < 17<<20 {
		t.Fatalf("%d %v", len(raw), err)
	}
	decoded, err := decodeDocument(raw, DefaultLimits().MaxDocumentBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.Metadata["fraction"]) != string(document.Metadata["fraction"]) {
		t.Fatal("fraction changed")
	}
}

func rewriteState(t *testing.T, s *Service, mutate func(*diskState)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, stateName))
	if err != nil {
		t.Fatal(err)
	}
	var state diskState
	if err := decodeCanonical(raw, &state, s.limits.MaxStateBytes); err != nil {
		t.Fatal(err)
	}
	mutate(&state)
	raw, err = canonical(state, s.limits.MaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := objectName("states", digest(raw))
	if err := os.WriteFile(filepath.Join(s.dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, stateName), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticTamperWithRehashedRecordsRejected(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"candidate", "inverse", "history-chain"} {
		t.Run(kind, func(t *testing.T) {
			s := createTest(t)
			proposal := metadataTransaction(t, s, "tamper", "x", `0.25`)
			if _, err := s.Plan(ctx, proposal); err != nil {
				t.Fatal(err)
			}
			if kind == "history-chain" {
				if _, err := s.Apply(ctx, "tamper"); err != nil {
					t.Fatal(err)
				}
				commitMetadata(t, s, "next", "y", `0.5`)
			}
			rewriteState(t, s, func(state *diskState) {
				if kind == "history-chain" {
					state.History[0].Sequence++
					return
				}
				p := &state.Plans[0]
				name, _ := objectName("plans", p.RecordHash)
				raw, e := os.ReadFile(filepath.Join(s.dir, name))
				if e != nil {
					t.Fatal(e)
				}
				var record PlanRecord
				if e := decodeCanonical(raw, &record, s.limits.MaxRecordBytes); e != nil {
					t.Fatal(e)
				}
				if kind == "inverse" {
					record.Inverse.ID = "invented-inverse"
					record.Inverse.Digest = ""
					hash, e := tx.Digest(record.Inverse)
					if e != nil {
						t.Fatal(e)
					}
					record.Inverse.Digest = hash
				} else {
					name, _ := objectName("documents", record.CandidateDocumentHash)
					raw, e := os.ReadFile(filepath.Join(s.dir, name))
					if e != nil {
						t.Fatal(e)
					}
					doc, e := decodeDocument(raw, s.limits.MaxDocumentBytes)
					if e != nil {
						t.Fatal(e)
					}
					doc.Metadata["x"] = json.RawMessage(`0.75`)
					raw, e = freezeDocument(doc, s.limits.MaxDocumentBytes)
					if e != nil {
						t.Fatal(e)
					}
					hash := digest(raw)
					name, _ = objectName("documents", hash)
					if e := os.WriteFile(filepath.Join(s.dir, name), raw, 0600); e != nil {
						t.Fatal(e)
					}
					record.CandidateDocumentHash = hash
					p.CandidateDocumentHash = hash
				}
				raw, e = canonical(record, s.limits.MaxRecordBytes)
				if e != nil {
					t.Fatal(e)
				}
				hash := digest(raw)
				name, _ = objectName("plans", hash)
				if e := os.WriteFile(filepath.Join(s.dir, name), raw, 0600); e != nil {
					t.Fatal(e)
				}
				p.RecordHash = hash
			})
			if _, err := New(s.dir, Options{}); err == nil {
				t.Fatal("accepted semantically forged record")
			}
		})
	}
}

func TestNoReplace(t *testing.T) {
	s := createTest(t)
	err := s.locked(context.Background(), false, func(x *session) error {
		raw := []byte(`{"immutable":0.25}`)
		hash, err := x.put(context.Background(), "plans", raw)
		if err != nil {
			return err
		}
		name, _ := objectName("plans", hash)
		if err := x.publish(context.Background(), name, []byte(`replacement`), false); !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("expected no replace: %v", err)
		}
		actual, _, err := x.read(context.Background(), name, 1024)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, raw) {
			return errors.New("immutable bytes replaced")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
