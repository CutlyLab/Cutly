//go:build darwin || linux

package chunkcachev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
)

func fixture(t *testing.T) (prepared.VerifiedEnvironmentV2, api.RenderRequestV2) {
	t.Helper()
	raw, e := os.ReadFile("../../conformance/fixtures/document-v2-render-request-gap-silence.canonical.json")
	if e != nil {
		t.Fatal(e)
	}
	r, e := api.DecodeRenderRequestV2(bytes.TrimSpace(raw))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile("../../conformance/fixtures/document-v2-render-plan-gap.document.json")
	if e != nil {
		t.Fatal(e)
	}
	d, e := documentv2.Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	v, e := prepared.VerifyEnvironmentV2(context.Background(), prepared.EnvironmentV2{Document: d, Plan: r.Plan, Manifest: r.Manifest})
	if e != nil {
		t.Fatal(e)
	}
	return v, r
}
func identity(t *testing.T) Identity {
	t.Helper()
	v, _ := fixture(t)
	env, e := v.Environment(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	r := env.Plan.SampleRange
	r.Count = min(r.Count, 65536)
	out, e := NewIdentity(context.Background(), v, api.AudioBlockProfileV2, r)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func setup(t *testing.T) (*Store, *cas.Store, string, Identity, []byte, Receipt) {
	t.Helper()
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(base, "index")
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	objects, e := cas.New(filepath.Join(base, "cas"), cas.Options{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(root, objects, Options{MaxPayloadBytes: 8 << 20})
	if e != nil {
		t.Fatal(e)
	}
	v := identity(t)
	data := bytes.Repeat([]byte{1, 2, 3, 4}, int(v.Range.Count*v.UnitBytes/4))
	r, e := NewReceipt(v, digest(data), int64(len(data)), "producer-original", 2)
	if e != nil {
		t.Fatal(e)
	}
	return s, objects, base, v, data, r
}
func publish(t *testing.T, s *Store, v Identity, data []byte, r Receipt) {
	t.Helper()
	if e := s.Publish(context.Background(), v, r, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
}
func consume(t *testing.T, s *Store, v Identity, want []byte) *Handle {
	t.Helper()
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(h)
	if e != nil || !bytes.Equal(data, want) {
		t.Fatalf("read %d %v", len(data), e)
	}
	if e = h.Close(); e != nil {
		t.Fatal(e)
	}
	return h
}
func TestCrossJobReuseRestartAndProvenance(t *testing.T) {
	s, objects, _, v, data, r := setup(t)
	publish(t, s, v, data, r)
	// Real request JobID/publication changes must not leak into chunk identity.
	env, request := fixture(t)
	request.Job.ID = "job-other"
	request.Job.ArtifactURI = "file:///other/output.mp4"
	again, e := NewIdentity(context.Background(), env, v.Domain, v.Range)
	if e != nil || again != v {
		t.Fatal("job-neutral identity", e)
	}
	s2, e := New(s.root, objects, Options{MaxPayloadBytes: 8 << 20})
	if e != nil {
		t.Fatal(e)
	}
	replacement, e := NewReceipt(v, r.ContentHash, r.SizeBytes, "new-job-invocation", 1)
	if e != nil {
		t.Fatal(e)
	}
	publish(t, s2, v, data, replacement)
	h := consume(t, s2, v, data)
	if h.Receipt() != r {
		t.Fatal("original provenance replaced")
	}
	k, e := Key(v)
	if e != nil || h.Key() != k {
		t.Fatal(e)
	}
	// Every required environment identity stays in the key, not just a jobless range.
	variants := []Identity{v, v, v, v, v, v, v, v}
	variants[0].DocumentHash = digest([]byte("other-doc"))
	variants[1].PlanFingerprint = digest([]byte("other-plan"))
	variants[2].ExecutionManifestFingerprint = digest([]byte("other-manifest"))
	variants[3].ProviderExecutableDigest = digest([]byte("provider"))
	variants[4].MediaRuntime.DecoderProfile += "-other"
	variants[5].Range.First++
	variants[6].Output.VideoCodec += "-other"
	variants[7].MediaRuntime.FFmpegBuild = digest([]byte("ffmpeg"))
	for _, variant := range variants {
		other, e := Key(variant)
		if e != nil {
			t.Fatal(e)
		}
		if other == k {
			t.Fatal("required identity omitted")
		}
		if hit, e := s.Lookup(context.Background(), variant); hit != nil || !errors.Is(e, ErrMiss) {
			t.Fatalf("foreign hit %v %v", hit, e)
		}
	}
}
func TestStrictReceiptCodec(t *testing.T) {
	_, _, _, _, _, r := setup(t)
	raw, e := CanonicalReceipt(r)
	if e != nil {
		t.Fatal(e)
	}
	got, e := DecodeReceipt(raw)
	if e != nil || got != r {
		t.Fatal(e)
	}
	bad := [][]byte{append([]byte(" "), raw...), bytes.Replace(raw, []byte(`"attempt":2`), []byte(`"attempt":2,"attempt":2`), 1), bytes.Replace(raw, []byte(`"attempt":2`), []byte(`"Attempt":2`), 1), bytes.Replace(raw, []byte(`"attempt":2`), []byte(`"attempt":null`), 1), bytes.Replace(raw, []byte(`"attempt":2,`), nil, 1), bytes.Replace(raw, []byte(`"attempt":2`), []byte(`"attempt":2,"unknown":0`), 1), bytes.Repeat([]byte("x"), MaxRecordBytes+1)}
	for _, b := range bad {
		if got, e := DecodeReceipt(b); e == nil || got != (Receipt{}) {
			t.Fatal("accepted hostile receipt")
		}
	}
}
func TestBadEntryOnlyQuarantinesOneIndexAndPreservesActiveReader(t *testing.T) {
	s, objects, _, v, data, r := setup(t)
	publish(t, s, v, data, r)
	other := v
	other.Range.First += other.Range.Count
	otherR, e := NewReceipt(other, r.ContentHash, r.SizeBytes, "other-producer", 1)
	if e != nil {
		t.Fatal(e)
	}
	publish(t, s, other, data, otherR)
	active, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	k, _ := Key(v)
	name := filepath.Join(s.root, "entries", entryName(k))
	if e = os.WriteFile(name, []byte(`{"bad":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrMiss) || !errors.Is(e, ErrCorrupt) {
		t.Fatal(h, e)
	}
	consume(t, s, other, data)
	read, e := io.ReadAll(active)
	if e != nil || !bytes.Equal(read, data) {
		t.Fatal(e)
	}
	if e = active.Close(); e != nil {
		t.Fatal(e)
	}
	// Corrupt index did not remove the valid CAS object.
	direct, e := objects.Open(context.Background(), k, "verify")
	if e != nil {
		t.Fatal(e)
	}
	direct.Close()
	publish(t, s, v, data, r)
	consume(t, s, v, data)
	q, e := os.ReadDir(filepath.Join(s.root, "quarantine"))
	if e != nil || len(q) != 1 {
		t.Fatal(len(q), e)
	}
}
func TestBadCASDataAndMissingObject(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	publish(t, s, v, data, r)
	var payload string
	e := filepath.WalkDir(filepath.Join(base, "cas", "entries"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Name() == "payload" || d.Name() == "payload.bin" {
			payload = path
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if payload == "" {
		t.Fatal("payload not found")
	}
	if e = os.Chmod(payload, 0600); e != nil {
		t.Fatal(e)
	}
	bad := append([]byte(nil), data...)
	bad[0] ^= 255
	if e = os.WriteFile(payload, bad, 0600); e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrMiss) || !errors.Is(e, cas.ErrCorrupt) {
		t.Fatal(h, e)
	}
	publish(t, s, v, data, r)
	consume(t, s, v, data)
}
func TestActiveReaderProtectsCrossStoreGC(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	publish(t, s, v, data, r)
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	gc, e := cas.New(filepath.Join(base, "cas"), cas.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.Heartbeat(context.Background()); e != nil {
		t.Fatal(e)
	}
	report, e := gc.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(e, cas.ErrQuotaUnsatisfied) || len(report.SkippedActive) != 1 || len(report.DeletedDigests) != 0 {
		t.Fatal(report, e)
	}
	if _, e = io.Copy(io.Discard, h); e != nil {
		t.Fatal(e)
	}
	if e = h.Close(); e != nil {
		t.Fatal(e)
	}
	report, e = gc.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if e != nil || len(report.DeletedDigests) != 1 {
		t.Fatal(report, e)
	}
	if hit, e := s.Lookup(context.Background(), v); hit != nil || !errors.Is(e, ErrMiss) {
		t.Fatal(hit, e)
	}
}
func TestCollisionAndFailedProducer(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	publish(t, s, v, data, r)
	other := bytes.Repeat([]byte{9}, len(data))
	bad, e := NewReceipt(v, digest(other), int64(len(other)), "another", 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Publish(context.Background(), v, bad, bytes.NewReader(other)); !errors.Is(e, cas.ErrCollision) {
		t.Fatal(e)
	}
	consume(t, s, v, data)
	foreign := v
	foreign.Range.First += foreign.Range.Count
	f, e := NewReceipt(foreign, r.ContentHash, r.SizeBytes, "another", 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Publish(context.Background(), foreign, f, bytes.NewReader(other)); e == nil {
		t.Fatal("accepted wrong data")
	}
	if h, e := s.Lookup(context.Background(), foreign); h != nil || !errors.Is(e, ErrMiss) {
		t.Fatal(h, e)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("producer") }

type countReader struct{ n int }

func (r *countReader) Read([]byte) (int, error) { r.n++; return 0, io.EOF }
func TestIdentityBeforeIOCancelPanicAndLimits(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	counter := &countReader{}
	bad := v
	bad.ProviderExecutableDigest = "bad"
	if e := s.Publish(context.Background(), bad, r, counter); e == nil || counter.n != 0 {
		t.Fatal(e, counter.n)
	}
	invalid := r
	invalid.ExecutableDigest = digest([]byte("foreign"))
	if e := s.Publish(context.Background(), v, invalid, counter); e == nil || counter.n != 0 {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.Publish(canceled, v, r, counter); !errors.Is(e, context.Canceled) || counter.n != 0 {
		t.Fatal(e)
	}
	small, e := New(s.root, s.artifacts, Options{MaxPayloadBytes: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = small.Publish(context.Background(), v, r, counter); !errors.Is(e, ErrLimit) || counter.n != 0 {
		t.Fatal(e)
	}
	if e = s.Publish(context.Background(), v, r, panicReader{}); e == nil {
		t.Fatal("panic escaped")
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrMiss) {
		t.Fatal(h, e)
	}
	publish(t, s, v, data, r)
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.Close(); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	if e = h.Heartbeat(context.Background()); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
}
func TestFaultRecoveryAndZeroFailure(t *testing.T) {
	for _, point := range []string{"before-write", "before-commit", "after-commit", "after-sync"} {
		t.Run(point, func(t *testing.T) {
			s, _, _, v, data, r := setup(t)
			s.fault = func(p string) error {
				if p == point {
					panic("injected")
				}
				return nil
			}
			e := s.Publish(context.Background(), v, r, bytes.NewReader(data))
			if e == nil {
				t.Fatal("fault not hit")
			}
			committed := strings.HasPrefix(point, "after-")
			if errors.Is(e, ErrDurabilityUncertain) != committed {
				t.Fatal(e)
			}
			fresh, e := New(s.root, s.artifacts, Options{MaxPayloadBytes: 8 << 20})
			if e != nil {
				t.Fatal(e)
			}
			publish(t, fresh, v, data, r)
			consume(t, fresh, v, data)
		})
	}
}
func TestSymlinkAndNamespaceSubstitutionRejected(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	alias := filepath.Join(base, "alias")
	if e := os.Symlink(s.root, alias); e != nil {
		t.Fatal(e)
	}
	if other, e := New(alias, s.artifacts, Options{MaxPayloadBytes: 8 << 20}); other != nil || e == nil {
		t.Fatal(other, e)
	}
	publish(t, s, v, data, r)
	k, _ := Key(v)
	path := filepath.Join(s.root, "entries", entryName(k))
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(base, "external")
	if e := os.WriteFile(target, []byte("untouched"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrCorrupt) {
		t.Fatal(h, e)
	}
	raw, e := os.ReadFile(target)
	if e != nil || string(raw) != "untouched" {
		t.Fatal(e)
	}
	old := filepath.Join(s.root, "old-entries")
	if e = os.Rename(filepath.Join(s.root, "entries"), old); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(s.root, "entries"), 0700); e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrCorrupt) {
		t.Fatal(h, e)
	}
}
func TestConcurrentPublicationAndClose(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Publish(context.Background(), v, r, bytes.NewReader(data)) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 1024)
			_, _ = h.Read(buf)
			_ = h.Heartbeat(context.Background())
			_ = h.Close()
		}()
	}
	wg.Wait()
}
func TestSubprocessCrashRestart(t *testing.T) {
	if os.Getenv("CUTLY_CHUNKCACHE_CRASH") == "1" {
		base := os.Getenv("CUTLY_CHUNKCACHE_ROOT")
		raw, e := os.ReadFile(filepath.Join(base, "producer.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r Receipt
		if e = json.Unmarshal(raw, &r); e != nil {
			t.Fatal(e)
		}
		objects, e := cas.New(filepath.Join(base, "cas"), cas.Options{})
		if e != nil {
			t.Fatal(e)
		}
		s, e := New(filepath.Join(base, "index"), objects, Options{MaxPayloadBytes: 8 << 20})
		if e != nil {
			t.Fatal(e)
		}
		s.fault = func(p string) error {
			if p == os.Getenv("CUTLY_CHUNKCACHE_POINT") {
				os.Exit(71)
			}
			return nil
		}
		data := bytes.Repeat([]byte{1, 2, 3, 4}, int(r.SizeBytes/4))
		_ = s.Publish(context.Background(), r.Identity, r, bytes.NewReader(data))
		os.Exit(72)
	}
	for _, point := range []string{"before-commit", "after-commit"} {
		t.Run(point, func(t *testing.T) {
			s, _, base, v, data, r := setup(t)
			raw, e := CanonicalReceipt(r)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(base, "producer.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCrashRestart$")
			cmd.Env = append(os.Environ(), "CUTLY_CHUNKCACHE_CRASH=1", "CUTLY_CHUNKCACHE_ROOT="+base, "CUTLY_CHUNKCACHE_POINT="+point)
			output, e := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 71 {
				t.Fatalf("child %v %s", e, output)
			}
			if point == "after-commit" {
				consume(t, s, v, data)
			} else {
				if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrMiss) {
					t.Fatal(h, e)
				}
			}
			fresh, e := New(s.root, s.artifacts, Options{MaxPayloadBytes: 8 << 20})
			if e != nil {
				t.Fatal(e)
			}
			publish(t, fresh, v, data, r)
			consume(t, fresh, v, data)
		})
	}
}
func TestLockWaitCancellation(t *testing.T) {
	s, _, _, v, _, _ := setup(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withSession(context.Background(), func(*session) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if h, e := s.Lookup(ctx, v); h != nil || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(h, e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow cancellation")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestCanonicalIndexRejectsUnknownNullAndForeignReceipt(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	publish(t, s, v, data, r)
	k, _ := Key(v)
	name := filepath.Join(s.root, "entries", entryName(k))
	raw, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	foreign := bytes.Replace(raw, []byte(`"profile":"`+IndexProfile+`"`), []byte(`"profile":"foreign.v2"`), 1)
	canonical, e := canonicaljson.Canonicalize(foreign)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(name, canonical, 0600); e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrCorrupt) {
		t.Fatal(h, e)
	}
}

var _ = videoedit.TickRange{}
