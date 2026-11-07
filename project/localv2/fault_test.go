package localv2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func boundary(point string) string {
	for _, kind := range []string{"documents", "plans", "states", "commits"} {
		prefix := ".cutly/v2/" + kind + "/"
		if at := strings.Index(point, prefix); at >= 0 {
			return point[:at] + prefix + "<hash>.json"
		}
	}
	return point
}

type actionCase struct {
	name  string
	setup func(*testing.T) *Service
	run   func(*testing.T, *Service, context.Context) (MutationResult, error)
}

func actionCases() []actionCase {
	return []actionCase{
		{"plan", func(t *testing.T) *Service { return createTest(t) }, func(t *testing.T, s *Service, ctx context.Context) (MutationResult, error) {
			return s.Plan(ctx, metadataTransaction(t, s, "planned", "x", `0.25`))
		}},
		{"apply", func(t *testing.T) *Service {
			s := createTest(t)
			if _, err := s.Plan(context.Background(), metadataTransaction(t, s, "planned", "x", `0.25`)); err != nil {
				t.Fatal(err)
			}
			return s
		}, func(t *testing.T, s *Service, ctx context.Context) (MutationResult, error) {
			return s.Apply(ctx, "planned")
		}},
		{"discard", func(t *testing.T) *Service {
			s := createTest(t)
			if _, err := s.Plan(context.Background(), metadataTransaction(t, s, "planned", "x", `0.25`)); err != nil {
				t.Fatal(err)
			}
			return s
		}, func(t *testing.T, s *Service, ctx context.Context) (MutationResult, error) {
			return s.Discard(ctx, "planned")
		}},
		{"revert", func(t *testing.T) *Service { s := createTest(t); commitMetadata(t, s, "target", "x", `0.25`); return s }, func(t *testing.T, s *Service, ctx context.Context) (MutationResult, error) {
			return s.Revert(ctx, "target", "undo", "undo-key")
		}},
	}
}

// Exercise errors, panic and cancellation at every actual write, sync,
// no-replace/replace publication and intent-removal boundary for all mutations.
func TestEveryPublicationBoundary(t *testing.T) {
	for _, action := range actionCases() {
		t.Run(action.name, func(t *testing.T) {
			probe := action.setup(t)
			points := []string{}
			seen := map[string]bool{}
			probe.fault = func(p string) error {
				p = boundary(p)
				if !seen[p] {
					points = append(points, p)
					seen[p] = true
				}
				return nil
			}
			if r, e := action.run(t, probe, context.Background()); e != nil || !r.OK {
				t.Fatalf("probe %#v %v", r, e)
			}
			probe.fault = nil
			expected, e := probe.Status(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			for _, point := range points {
				for _, mode := range []string{"error", "panic", "cancel"} {
					t.Run(mode+"/"+point, func(t *testing.T) {
						s := action.setup(t)
						before, e := s.Status(context.Background())
						if e != nil {
							t.Fatal(e)
						}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						fired := false
						s.fault = func(p string) error {
							if boundary(p) != point || fired {
								return nil
							}
							fired = true
							switch mode {
							case "error":
								return errors.New("injected")
							case "panic":
								panic("injected")
							case "cancel":
								cancel()
							}
							return nil
						}
						result, err := action.run(t, s, ctx)
						s.fault = nil
						if !fired {
							t.Fatal("boundary not reached")
						}
						if err != nil && !reflect.DeepEqual(result, MutationResult{}) {
							t.Fatalf("failure leaked result %#v", result)
						}
						reopened, e := New(s.dir, Options{})
						if e != nil {
							t.Fatal("reopen", e)
						}
						after, e := reopened.Status(context.Background())
						if e != nil {
							t.Fatal(e)
						}
						if err != nil && !errors.Is(err, ErrCommitUnknown) {
							if after != before {
								t.Fatalf("precommit %v changed %#v -> %#v", err, before, after)
							}
						} else {
							if after != expected {
								t.Fatalf("postcommit %v unexpected %#v want %#v", err, after, expected)
							}
						}
						if mode == "panic" && err != nil && !errors.Is(err, ErrPanic) {
							t.Fatal(err)
						}
						if errors.Is(err, ErrCommitUnknown) {
							retry, e := action.run(t, reopened, context.Background())
							if e != nil || !retry.OK {
								t.Fatalf("retry %#v %v", retry, e)
							}
						}
					})
				}
			}
		})
	}
}

func TestCreatePublicationBoundaries(t *testing.T) {
	for _, point := range []string{"before_publish:" + intentName, "after_publish:" + intentName, "before_publish:" + DocumentName, "after_publish:" + DocumentName, "before_publish:" + stateName, "after_publish:" + stateName, "before_remove_intent", "after_remove_intent"} {
		for _, mode := range []string{"error", "panic"} {
			t.Run(mode+"/"+point, func(t *testing.T) {
				dir := projectDir(t)
				s, err := configure(dir, Options{})
				if err != nil {
					t.Fatal(err)
				}
				s.fault = func(p string) error {
					if p != point {
						return nil
					}
					if mode == "panic" {
						panic("create fault")
					}
					return errors.New("create fault")
				}
				raw, err := CanonicalDocument(baseDocument())
				if err != nil {
					t.Fatal(err)
				}
				err = s.locked(context.Background(), true, func(x *session) error {
					return x.transition(context.Background(), diskState{Version: Version, DocumentID: baseDocument().ID, DocumentHash: digest(raw), Plans: []PlanSummary{}, History: []historyRef{}}, raw)
				})
				if err == nil {
					t.Fatal("fault not returned")
				}
				if errors.Is(err, ErrCommitUnknown) {
					opened, e := New(dir, Options{})
					if e != nil {
						t.Fatal(e)
					}
					if got, e := opened.Inspect(context.Background()); e != nil || got.Document.Revision != 0 {
						t.Fatalf("%#v %v", got, e)
					}
				} else {
					if _, e := os.Stat(filepath.Join(dir, DocumentName)); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("precommit create changed doc", e)
					}
				}
				if _, e := Create(context.Background(), dir, baseDocument(), Options{}); e != nil {
					t.Fatal("retry", e)
				}
			})
		}
	}
}

func TestRecoveryRejectsInadmissiblePairs(t *testing.T) {
	s := createTest(t)
	ctx := context.Background()
	if _, e := s.Plan(ctx, metadataTransaction(t, s, "p", "x", `0.25`)); e != nil {
		t.Fatal(e)
	}
	oldDoc, e := os.ReadFile(filepath.Join(s.dir, DocumentName))
	if e != nil {
		t.Fatal(e)
	}
	s.fault = func(point string) error {
		if point == "after_publish:"+stateName {
			return errors.New("stop after state")
		}
		return nil
	}
	if _, e := s.Apply(ctx, "p"); !errors.Is(e, ErrCommitUnknown) {
		t.Fatal(e)
	}
	s.fault = nil
	// Rewind just document, leaving new state: impossible publication order.
	if e := os.WriteFile(filepath.Join(s.dir, DocumentName), oldDoc, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := New(s.dir, Options{}); !errors.Is(e, ErrInvalid) {
		t.Fatal("accepted old-doc/new-state", e)
	}
}

func TestRevertPrecommitLeavesNoPendingPlan(t *testing.T) {
	s := createTest(t)
	commitMetadata(t, s, "target", "x", `0.25`)
	before, e := s.Status(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s.fault = func(point string) error {
		if point == "before_publish:"+intentName {
			return errors.New("stop")
		}
		return nil
	}
	if _, e := s.Revert(context.Background(), "target", "undo", "undo-key"); e == nil || errors.Is(e, ErrCommitUnknown) {
		t.Fatal(e)
	}
	s.fault = nil
	after, e := s.Status(context.Background())
	if e != nil || before != after {
		t.Fatalf("precommit revert changed state %#v %#v %v", before, after, e)
	}
}
