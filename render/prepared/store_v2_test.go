//go:build darwin || linux

package prepared

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func newTestStoreV2(t *testing.T, options Options) *Store {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(base, "registry"), options)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func entryTestV2(t *testing.T, s *Store, e EnvironmentV2) string {
	t.Helper()
	n, err := keyNameV2(e.Plan.Fingerprint, e.Manifest.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(s.root, "v2", "entries", n)
}
func putTestV2(t *testing.T, s *Store, e EnvironmentV2) {
	t.Helper()
	out, made, err := s.PutV2(context.Background(), e.Document, e.Plan, e.Manifest)
	if err != nil || !made || !bytes.Equal(rawEnvironmentV2(t, out), rawEnvironmentV2(t, e)) {
		t.Fatalf("put: created=%v err=%v", made, err)
	}
}
func zeroStorageV2(t *testing.T, e EnvironmentV2, made bool, err error) {
	t.Helper()
	if err == nil || made || !reflect.DeepEqual(e, EnvironmentV2{}) {
		t.Fatalf("failure not zero: made=%v err=%v", made, err)
	}
}
func TestStoreV2ReopenOwnedAndLazy(t *testing.T) {
	ctx := context.Background()
	s := newTestStoreV2(t, Options{})
	e := environmentFixtureV2(t, "mixed")
	expected := rawEnvironmentV2(t, e)
	if _, err := os.Stat(filepath.Join(s.root, "v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("New created v2 namespace")
	}
	if _, err := s.ResolveV2(ctx, e.Plan.Fingerprint, e.Manifest.Fingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing Resolve created namespace")
	}
	out, made, err := s.PutV2(ctx, e.Document, e.Plan, e.Manifest)
	if err != nil || !made {
		t.Fatal(made, err)
	}
	e.Document.ID = "mutated-input"
	out.Document.ID = "mutated-output"
	reopened, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.ResolveV2(ctx, e.Plan.Fingerprint, e.Manifest.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, rawEnvironmentV2(t, got)) {
		t.Fatal("stored bytes changed")
	}
	got.Document.ID = "mutated-resolve"
	original := environmentFixtureV2(t, "mixed")
	got, made, err = reopened.PutV2(ctx, original.Document, original.Plan, original.Manifest)
	if err != nil || made {
		t.Fatal(made, err)
	}
	if !bytes.Equal(expected, rawEnvironmentV2(t, got)) {
		t.Fatal("idempotent bytes changed")
	}
	if _, err = s.Resolve(ctx, e.Plan.Fingerprint, e.Manifest.Fingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatal("v1 fell back to v2", err)
	}
	if s.maxRecordBytes != 64<<20 || s.maxRecordBytesV2 != 160<<20 {
		t.Fatal("limits coupled")
	}
	v1plan, v1manifest := testEnvironment(t, 1)
	if _, _, err = s.Put(ctx, v1plan, v1manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveV2(ctx, v1plan.Fingerprint, v1manifest.Fingerprint); !errors.Is(err, ErrNotFound) {
		t.Fatal("v2 fell back to v1", err)
	}
}
func TestStoreV2BoundsAndCorruption(t *testing.T) {
	fixture := environmentFixtureV2(t, "mixed")
	for _, test := range []struct {
		name   string
		damage func(*testing.T, string)
	}{
		{"truncated", func(t *testing.T, p string) {
			if e := os.Truncate(filepath.Join(p, recordName), 3); e != nil {
				t.Fatal(e)
			}
		}},
		{"marker-missing", func(t *testing.T, p string) {
			if e := os.Remove(filepath.Join(p, "complete.v2")); e != nil {
				t.Fatal(e)
			}
		}},
		{"marker-v1", func(t *testing.T, p string) {
			if e := os.WriteFile(filepath.Join(p, "complete.v2"), completion(rawEnvironmentV2(t, fixture)), 0400); e != nil {
				t.Fatal(e)
			}
		}},
		{"oversize", func(t *testing.T, p string) {
			if e := os.Truncate(filepath.Join(p, recordName), MaxRecordBytesV2+1); e != nil {
				t.Fatal(e)
			}
		}},
		{"record-symlink", func(t *testing.T, p string) {
			os.Remove(filepath.Join(p, recordName))
			if e := os.Symlink("complete.v2", filepath.Join(p, recordName)); e != nil {
				t.Fatal(e)
			}
		}},
		{"record-fifo", func(t *testing.T, p string) {
			os.Remove(filepath.Join(p, recordName))
			if e := syscall.Mkfifo(filepath.Join(p, recordName), 0600); e != nil {
				t.Fatal(e)
			}
		}},
		{"record-hardlink", func(t *testing.T, p string) {
			if e := os.Link(filepath.Join(p, recordName), filepath.Join(p, "..", "alias")); e != nil {
				t.Fatal(e)
			}
		}},
		{"directory-permissions", func(t *testing.T, p string) {
			if e := os.Chmod(p, 0755); e != nil {
				t.Fatal(e)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestStoreV2(t, Options{})
			putTestV2(t, s, fixture)
			p := entryTestV2(t, s, fixture)
			if err := os.Chmod(filepath.Join(p, recordName), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(p, "complete.v2"), 0600); err != nil {
				t.Fatal(err)
			}
			test.damage(t, p)
			out, err := s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
			zeroStorageV2(t, out, false, err)
		})
	}
	for _, maximum := range []int{-1, 100, MaxRecordBytesV2 + 1} {
		base, _ := filepath.EvalSymlinks(t.TempDir())
		if _, err := New(filepath.Join(base, "invalid"), Options{MaxRecordBytesV2: maximum}); !errors.Is(err, ErrConfiguration) {
			t.Fatal(maximum, err)
		}
	}
	s := newTestStoreV2(t, Options{MaxRecordBytesV2: 1024})
	out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
	zeroStorageV2(t, out, made, err)
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	s = newTestStoreV2(t, Options{MaxEntries: 1})
	putTestV2(t, s, fixture)
	other := environmentFixtureV2(t, "gap")
	out, made, err = s.PutV2(context.Background(), other.Document, other.Plan, other.Manifest)
	zeroStorageV2(t, out, made, err)
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	s = newTestStoreV2(t, Options{MaxScratchEntries: 1})
	putTestV2(t, s, fixture)
	if err = os.Mkdir(filepath.Join(s.root, "v2", "scratch", "stale"), 0700); err != nil {
		t.Fatal(err)
	}
	out, made, err = s.PutV2(context.Background(), other.Document, other.Plan, other.Manifest)
	zeroStorageV2(t, out, made, err)
	if !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
func TestStoreV2Substitution(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, target := range []string{"root", "v2", "v2/entries", "v2/scratch", "v2/locks", "v2/locks/global.lock"} {
		for _, link := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/symlink=%v", target, link), func(t *testing.T) {
				s := newTestStoreV2(t, Options{})
				putTestV2(t, s, fixture)
				path := s.root
				if target != "root" {
					path = filepath.Join(s.root, target)
				}
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if link {
					if err := os.Symlink(filepath.Base(path)+"-old", path); err != nil {
						t.Fatal(err)
					}
				} else if strings.HasSuffix(path, ".lock") {
					if err := os.WriteFile(path, nil, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
				zeroStorageV2(t, out, made, err)
			})
		}
	}
	t.Run("ancestor-symlink", func(t *testing.T) {
		s := newTestStoreV2(t, Options{})
		putTestV2(t, s, fixture)
		parent := filepath.Dir(s.root)
		if err := os.Rename(parent, parent+"-moved"); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(parent+"-moved", parent)
		if err := os.Symlink(parent+"-moved", parent); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(parent)
		out, err := s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
		zeroStorageV2(t, out, false, err)
	})
	t.Run("owner", func(t *testing.T) {
		s := newTestStoreV2(t, Options{})
		putTestV2(t, s, fixture)
		p := filepath.Join(s.root, "v2")
		if err := os.Chown(p, os.Geteuid()+1, -1); err != nil {
			t.Skip("chown unavailable")
		}
		defer os.Chown(p, os.Geteuid(), -1)
		out, err := s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
		zeroStorageV2(t, out, false, err)
	})
}
func TestStoreV2FaultCancellationAndPanic(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, point := range []string{"locked", "publish.before_write", "file.before_sync:environment.json", "file.before_sync:complete.v2", "publish.before_rename", "publish.after_rename", "publish.before_sync", "publish.after_sync"} {
		for _, mode := range []string{"error", "panic", "cancel"} {
			t.Run(point+"/"+mode, func(t *testing.T) {
				s := newTestStoreV2(t, Options{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hit := false
				s.fault = func(p string) error {
					if p != "v2."+point {
						return nil
					}
					hit = true
					switch mode {
					case "panic":
						panic("injected")
					case "cancel":
						cancel()
						return nil
					}
					return errors.New("injected")
				}
				out, made, err := s.PutV2(ctx, fixture.Document, fixture.Plan, fixture.Manifest)
				zeroStorageV2(t, out, made, err)
				if !hit {
					t.Fatal("fault not reached")
				}
				after := point == "publish.after_rename" || point == "publish.before_sync" || point == "publish.after_sync"
				if errors.Is(err, ErrDurabilityUncertain) != after {
					t.Fatalf("uncertainty=%v err=%v", after, err)
				}
				s.fault = nil
				_, err = s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
				if after && err != nil {
					t.Fatal("published entry lost", err)
				}
				if !after && !errors.Is(err, ErrNotFound) {
					t.Fatal("early publication", err)
				}
				names, err := os.ReadDir(filepath.Join(s.root, "v2", "scratch"))
				if err != nil || len(names) != 0 {
					t.Fatal("scratch leak", names, err)
				}
			})
		}
	}
	t.Run("cancel-before-call", func(t *testing.T) {
		s := newTestStoreV2(t, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out, made, err := s.PutV2(ctx, fixture.Document, fixture.Plan, fixture.Manifest)
		zeroStorageV2(t, out, made, err)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
func TestStoreV2AnchoredMutationAtCommit(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, point := range []string{"locked", "publish.before_rename", "publish.after_rename"} {
		for _, part := range []string{"entries", "scratch", "locks/global.lock"} {
			t.Run(point+"/"+part, func(t *testing.T) {
				s := newTestStoreV2(t, Options{})
				s.fault = func(p string) error {
					if p != "v2."+point {
						return nil
					}
					path := filepath.Join(s.root, "v2", part)
					if e := os.Rename(path, path+"-old"); e != nil {
						return e
					}
					return os.Symlink(filepath.Base(path)+"-old", path)
				}
				out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
				zeroStorageV2(t, out, made, err)
				if errors.Is(err, ErrDurabilityUncertain) != (point == "publish.after_rename") {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestStoreV2CollisionNoReplace(t *testing.T) {
	s := newTestStoreV2(t, Options{})
	fixture := environmentFixtureV2(t, "gap")
	name, _ := keyNameV2(fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
	s.fault = func(point string) error {
		if point != "v2.publish.before_rename" {
			return nil
		}
		p := entryTestV2(t, s, fixture)
		if err := os.Mkdir(p, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(p, "hostile"), []byte("do not replace"), 0600)
	}
	out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
	zeroStorageV2(t, out, made, err)
	if !errors.Is(err, ErrCollision) {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "v2", "entries", name, "hostile"))
	if err != nil || string(b) != "do not replace" {
		t.Fatal("replacement", err)
	}
	s = newTestStoreV2(t, Options{})
	putTestV2(t, s, fixture)
	p := entryTestV2(t, s, fixture)
	if err = os.Chmod(filepath.Join(p, recordName), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(p, "complete.v2"), 0600); err != nil {
		t.Fatal(err)
	}
	other := rawEnvironmentV2(t, environmentFixtureV2(t, "mixed"))
	if err = os.WriteFile(filepath.Join(p, recordName), other, 0400); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p, "complete.v2"), completionBytesV2(other), 0400); err != nil {
		t.Fatal(err)
	}
	out, made, err = s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
	zeroStorageV2(t, out, made, err)
	if !errors.Is(err, ErrCollision) {
		t.Fatal(err)
	}
	out, err = s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
	zeroStorageV2(t, out, false, err)
	if !errors.Is(err, ErrCollision) {
		t.Fatal(err)
	}
}
func TestStoreV2ConcurrentAndLockCancellation(t *testing.T) {
	s := newTestStoreV2(t, Options{})
	fixture := environmentFixtureV2(t, "gap")
	var made atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
			if err != nil {
				t.Error(err)
			}
			if created {
				made.Add(1)
			}
		}()
	}
	wg.Wait()
	if made.Load() != 1 {
		t.Fatal(made.Load())
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withStorageV2(context.Background(), false, func(storageSessionV2) error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	out, err := s.ResolveV2(ctx, fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint)
	zeroStorageV2(t, out, false, err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestStoreV2ProcessRace(t *testing.T) {
	if root := os.Getenv("CUTLY_PREPARED_V2_RACE_ROOT"); root != "" {
		s, err := New(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		fixture := environmentFixtureV2(t, "gap")
		_, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("CREATED=%v\n", made)
		return
	}
	s := newTestStoreV2(t, Options{})
	commands := make([]*exec.Cmd, 4)
	outputs := make([]bytes.Buffer, 4)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestStoreV2ProcessRace$", "-test.count=1")
		commands[i].Env = append(os.Environ(), "CUTLY_PREPARED_V2_RACE_ROOT="+s.root)
		commands[i].Stdout = &outputs[i]
		commands[i].Stderr = &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	failed := false
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Error(err, outputs[i].String())
			failed = true
		}
		count += strings.Count(outputs[i].String(), "CREATED=true")
	}
	if failed {
		t.FailNow()
	}
	if count != 1 {
		t.Fatal("process creators", count)
	}
	fixture := environmentFixtureV2(t, "gap")
	if _, err := s.ResolveV2(context.Background(), fixture.Plan.Fingerprint, fixture.Manifest.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestStoreV2RetainedEntryAndFiles(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, point := range []string{"publish.before_rename", "publish.after_rename", "publish.before_sync", "committed.file.before_sync:environment.json", "committed.file.after_sync:environment.json", "committed.file.after_sync:complete.v2", "publish.after_sync"} {
		for _, target := range []string{"entry", "record", "marker"} {
			if point == "publish.before_rename" && target != "record" {
				continue
			}
			t.Run(point+"/"+target, func(t *testing.T) {
				s := newTestStoreV2(t, Options{})
				s.fault = func(p string) error {
					if p != "v2."+point {
						return nil
					}
					path := entryTestV2(t, s, fixture)
					if point == "publish.before_rename" {
						items, e := os.ReadDir(filepath.Join(s.root, "v2", "scratch"))
						if e != nil {
							return e
						}
						if len(items) != 1 {
							return errors.New("missing staging")
						}
						path = filepath.Join(s.root, "v2", "scratch", items[0].Name())
					}
					if target == "entry" {
						return os.Rename(path, path+"-moved")
					}
					name := recordName
					if target == "marker" {
						name = "complete.v2"
					}
					raw, e := os.ReadFile(filepath.Join(path, name))
					if e != nil {
						return e
					}
					if point == "publish.before_rename" {
						raw = []byte("hostile")
					}
					original, e := os.Open(filepath.Join(path, name))
					if e != nil {
						return e
					}
					t.Cleanup(func() { _ = original.Close() }) // prevent inode reuse
					if e = os.Remove(filepath.Join(path, name)); e != nil {
						return e
					}
					// Identical bytes on a different inode must still be refused if an
					// already-open record is substituted during the fsync boundary.
					return os.WriteFile(filepath.Join(path, name), raw, 0400)
				}
				out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
				if point == "publish.after_rename" && target != "entry" {
					// Publication identity is first established after this hook; identical
					// bytes are valid, and are independently reread before success.
					if err != nil || !made {
						t.Fatal(made, err)
					}
					return
				}
				zeroStorageV2(t, out, made, err)
				if !errors.Is(err, ErrDurabilityUncertain) {
					t.Fatal("published failure lacks uncertainty", err)
				}
			})
		}
	}
}

// Both newly published replacements and idempotent recovery must sync the
// actual verified record and marker, not only the original staging inodes.
func TestStoreV2CommittedFileDurability(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, idempotent := range []bool{false, true} {
		for _, target := range []string{recordName, "complete.v2"} {
			t.Run(fmt.Sprintf("idempotent=%v/%s", idempotent, target), func(t *testing.T) {
				for _, mode := range []string{"success", "error", "panic", "cancel"} {
					t.Run(mode, func(t *testing.T) {
						s := newTestStoreV2(t, Options{})
						replace := func() error {
							path := filepath.Join(entryTestV2(t, s, fixture), target)
							original, e := os.Open(path)
							if e != nil {
								return e
							}
							t.Cleanup(func() { _ = original.Close() })
							raw, e := os.ReadFile(path)
							if e != nil {
								return e
							}
							if e = os.Remove(path); e != nil {
								return e
							}
							return os.WriteFile(path, raw, 0400)
						}
						if idempotent {
							putTestV2(t, s, fixture)
							if err := replace(); err != nil {
								t.Fatal(err)
							}
						}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						before, after := map[string]bool{}, map[string]bool{}
						s.fault = func(point string) error {
							if !idempotent && point == "v2.publish.after_rename" {
								return replace()
							}
							for _, name := range []string{recordName, "complete.v2"} {
								if point == "v2.committed.file.before_sync:"+name {
									before[name] = true
								}
								if point == "v2.committed.file.after_sync:"+name {
									after[name] = true
								}
							}
							if point != "v2.committed.file.before_sync:"+target {
								return nil
							}
							switch mode {
							case "error":
								return errors.New("committed sync failure")
							case "panic":
								panic("committed sync panic")
							case "cancel":
								cancel()
								return ctx.Err()
							}
							return nil
						}
						out, made, err := s.PutV2(ctx, fixture.Document, fixture.Plan, fixture.Manifest)
						if mode == "success" {
							if err != nil || made == idempotent {
								t.Fatal(made, err)
							}
							for _, name := range []string{recordName, "complete.v2"} {
								if !before[name] || !after[name] {
									t.Fatal("verified file not synced", name)
								}
							}
						} else {
							zeroStorageV2(t, out, made, err)
							if !before[target] || !errors.Is(err, ErrDurabilityUncertain) {
								t.Fatal("missing uncertain file-sync failure", err)
							}
							// A fresh retry must establish durability for the current verified
							// files even though it did not create their directory entry.
							s.fault = nil
							out, made, err = s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
							if err != nil || made {
								t.Fatal("retry", made, err)
							}
						}
					})
				}
			})
		}
	}
}

func TestStoreV2CommittedFilePostSyncContent(t *testing.T) {
	fixture := environmentFixtureV2(t, "gap")
	for _, target := range []string{recordName, "complete.v2"} {
		t.Run(target, func(t *testing.T) {
			s := newTestStoreV2(t, Options{})
			s.fault = func(point string) error {
				if point != "v2.committed.file.after_sync:"+target {
					return nil
				}
				path := filepath.Join(entryTestV2(t, s, fixture), target)
				if err := os.Chmod(path, 0600); err != nil {
					return err
				}
				return os.Truncate(path, 1)
			}
			out, made, err := s.PutV2(context.Background(), fixture.Document, fixture.Plan, fixture.Manifest)
			zeroStorageV2(t, out, made, err)
			if !errors.Is(err, ErrDurabilityUncertain) || !errors.Is(err, ErrCorrupt) {
				t.Fatal(err)
			}
		})
	}
}
