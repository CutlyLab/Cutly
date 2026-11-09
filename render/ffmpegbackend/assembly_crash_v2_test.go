//go:build darwin || linux

package ffmpegbackend

import (
	"context"
	"encoding/json"
	"errors"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type crashInputV2 struct {
	Options    ChunkAssemblerOptionsV2
	Binding    evidence.BindingV2
	Data       map[string][]byte
	Checkpoint string
}

func TestChunkAssemblerV2CrashChild(t *testing.T) {
	inputPath := os.Getenv("CUTLY_ASSEMBLY_CRASH_INPUT_V2")
	if inputPath == "" {
		return
	}
	raw, e := os.ReadFile(inputPath)
	if e != nil {
		t.Fatal(e)
	}
	var input crashInputV2
	if e = json.Unmarshal(raw, &input); e != nil {
		t.Fatal(e)
	}
	b, e := NewChunkAssemblerV2(context.Background(), input.Options)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	v, e := evidence.VerifyBindingV2(context.Background(), input.Binding)
	if e != nil {
		t.Fatal(e)
	}
	b.journalHook = func(s string) {
		if s == input.Checkpoint {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	source := sourceFuncV2(func(ctx context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(input.Data[d.CacheKey.Digest]))), nil
	})
	if _, e = b.AssembleChunks(context.Background(), v, source, nil); e != nil {
		t.Fatal(e)
	}
	t.Fatal("expected crash checkpoint was not reached")
}

func killAssemblyV2(t *testing.T, f *assemblyFixtureNativeV2, checkpoint string) {
	t.Helper()
	data := map[string][]byte{}
	for k, v := range f.data {
		data[k.Digest] = v
	}
	input := crashInputV2{Options: f.o, Binding: f.r, Data: data, Checkpoint: checkpoint}
	raw, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "crash-input.json")
	if e = os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChunkAssemblerV2CrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CUTLY_ASSEMBLY_CRASH_INPUT_V2="+p)
	output, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) {
		t.Fatalf("child did not exit by SIGKILL: %v %s", e, output)
	}
	st, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !st.Signaled() || st.Signal() != syscall.SIGKILL {
		t.Fatalf("wrong child crash %v %s", e, output)
	}
}

func TestChunkAssemblerV2SIGKILLRestartOwnedRecovery(t *testing.T) {
	for _, point := range []string{"journal-alias-durable", "journal-durable", "encoded", "evidence-linked", "media-linked", "pair-synced"} {
		t.Run(point, func(t *testing.T) {
			f := assemblyNativeV2(t, 48001)
			killAssemblyV2(t, f, point)
			restarted, e := NewChunkAssemblerV2(context.Background(), f.o)
			if e != nil {
				t.Fatal(e)
			}
			defer restarted.Close()
			count := 0
			source := sourceFuncV2(func(ctx context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
				count++
				return f.source().Open(ctx, d)
			})
			a, e := restarted.AssembleChunks(context.Background(), f.v, source, nil)
			if e != nil {
				t.Fatal(e)
			}
			if a.ContentHash == "" {
				t.Fatal("missing restart publication")
			}
			if (point == "media-linked" || point == "pair-synced") && count != 0 {
				t.Fatal("recomputed valid pair chunks")
			}
			entries, e := os.ReadDir(f.o.OutputRoot)
			if e != nil {
				t.Fatal(e)
			}
			if len(entries) != 2 {
				t.Fatalf("restart left owned remnants: %v", entries)
			}
			if _, found, e := restarted.InspectChunkAssembly(context.Background(), f.v); e != nil || !found {
				t.Fatalf("restarted pair %v %v", found, e)
			}
		})
	}
}

func TestChunkAssemblerV2JournalForeignReplacementFailsClosed(t *testing.T) {
	for _, mode := range []string{"media-inode", "journal-inode", "journal-tamper", "unknown", "pre-journal-crash"} {
		t.Run(mode, func(t *testing.T) {
			f := assemblyNativeV2(t, 48000)
			if mode == "unknown" {
				if e := os.WriteFile(filepath.Join(f.o.OutputRoot, ".cutly-owned-v2-unknown.mp4"), []byte("unknown"), 0600); e != nil {
					t.Fatal(e)
				}
			} else if mode == "pre-journal-crash" {
				killAssemblyV2(t, f, "pre-journal-created")
			} else {
				killAssemblyV2(t, f, "journal-durable")
				j, e := f.b.readJournalV2(context.Background(), f.r)
				if e != nil {
					t.Fatal(e)
				}
				name := j.name
				payload := j.raw
				if mode == "media-inode" {
					name = j.record.Media.Name
					payload = []byte("foreign")
				}
				if e = j.close(); e != nil {
					t.Fatal(e)
				}
				path := filepath.Join(f.o.OutputRoot, name)
				if mode != "journal-tamper" {
					if e = os.Rename(path, path+".original"); e != nil {
						t.Fatal(e)
					}
				} else {
					payload = []byte("{}")
				}
				if e = os.WriteFile(path, payload, 0600); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadDir(f.o.OutputRoot)
			if e != nil {
				t.Fatal(e)
			}
			a, e := f.b.AssembleChunks(context.Background(), f.v, f.source(), nil)
			if e == nil || a != (api.RenderArtifact{}) {
				t.Fatalf("accepted foreign recovery %v", e)
			}
			after, e := os.ReadDir(f.o.OutputRoot)
			if e != nil {
				t.Fatal(e)
			}
			if len(before) != len(after) {
				t.Fatal("deleted unowned remnants")
			}
		})
	}
}

func TestChunkAssemblerV2LockCancellationAndRootReplacement(t *testing.T) {
	f := assemblyNativeV2(t, 48000)
	unlock, e := f.b.lockV2(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	a, e := f.b.AssembleChunks(ctx, f.v, f.source(), nil)
	if !errors.Is(e, context.DeadlineExceeded) || a != (api.RenderArtifact{}) || time.Since(start) > 2*time.Second {
		t.Fatalf("lock ignored cancellation: %v", e)
	}
	if e = unlock(); e != nil {
		t.Fatal(e)
	}
	old := f.o.OutputRoot + "-original"
	if e = os.Rename(f.o.OutputRoot, old); e != nil {
		t.Fatal(e)
	}
	defer os.Rename(old, f.o.OutputRoot)
	if e = os.Mkdir(f.o.OutputRoot, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.Remove(f.o.OutputRoot)
	if _, e = f.b.AssembleChunks(context.Background(), f.v, f.source(), nil); e == nil {
		t.Fatal("accepted replaced output root")
	}
}
