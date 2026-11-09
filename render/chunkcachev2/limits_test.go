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
	"sync/atomic"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
	api "github.com/CutlyLab/Cutly/render/api"
)

func TestTotalEncodedBudgetBeforeIO(t *testing.T) {
	s, _, _, id, _, r := setup(t)
	// Individual strings fit, but canonical escaping makes the total exceed the
	// durable wire budget. Constructors must reject before producing a bad record.
	large := string(bytes.Repeat([]byte{1}, 1024))
	id.MediaRuntime.DecoderProfile = large
	id.MediaRuntime.EncoderProfile = large
	id.MediaRuntime.ColorProfile = large
	id.Output.Format = large
	id.Output.VideoCodec = large
	id.Output.AudioCodec = large
	if got, e := NewReceipt(id, r.ContentHash, r.SizeBytes, "producer", 1); got != (Receipt{}) || !errors.Is(e, ErrLimit) {
		t.Fatal(got, e)
	}
	source := &countReader{}
	if e := s.Publish(context.Background(), id, r, source); e == nil || source.n != 0 {
		t.Fatal(e, source.n)
	}
}

func TestVideoIdentityAndPartitionAdmission(t *testing.T) {
	v, _ := fixture(t)
	e, err := v.Environment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	frames := e.Plan.FrameRange
	frames.Count = min(frames.Count, 64)
	id, err := NewIdentity(context.Background(), v, api.VideoSurfaceProfileV2, frames)
	if err != nil {
		t.Fatal(err)
	}
	if id.UnitBytes != int64(id.Target.Width)*int64(id.Target.Height)*8 {
		t.Fatal("video unit")
	}
	if _, err = Key(id); err != nil {
		t.Fatal(err)
	}
	frames.First++
	if got, err := NewIdentity(context.Background(), v, api.VideoSurfaceProfileV2, frames); err == nil || got != (Identity{}) {
		t.Fatal(got, err)
	}
}
func TestIndexCapacityBeforePayloadIO(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	one, e := New(s.root, s.artifacts, Options{MaxPayloadBytes: 8 << 20, MaxEntries: 1})
	if e != nil {
		t.Fatal(e)
	}
	publish(t, one, v, data, r)
	other := v
	other.Range.First += other.Range.Count
	receipt, e := NewReceipt(other, r.ContentHash, r.SizeBytes, "other", 1)
	if e != nil {
		t.Fatal(e)
	}
	source := &countReader{}
	if e = one.Publish(context.Background(), other, receipt, source); !errors.Is(e, ErrLimit) || source.n != 0 {
		t.Fatal(e, source.n)
	}
	consume(t, one, v, data)
}
func TestReadCancellationCloseReleasesLease(t *testing.T) {
	s, objects, _, v, data, r := setup(t)
	publish(t, s, v, data, r)
	ctx, cancel := context.WithCancel(context.Background())
	h, e := s.Lookup(ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if n, e := h.Read(make([]byte, 1)); n != 0 || !errors.Is(e, context.Canceled) {
		t.Fatal(n, e)
	}
	if e = h.Close(); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	report, e := objects.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if e != nil || len(report.DeletedDigests) != 1 {
		t.Fatal(report, e)
	}
}
func TestGCLeaseAcrossProcess(t *testing.T) {
	if os.Getenv("CUTLY_CHUNKCACHE_GC_CHILD") == "1" {
		store, e := cas.New(os.Getenv("CUTLY_CHUNKCACHE_GC_ROOT"), cas.Options{})
		if e != nil {
			t.Fatal(e)
		}
		report, e := store.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
		if !errors.Is(e, cas.ErrQuotaUnsatisfied) || len(report.SkippedActive) != 1 || len(report.DeletedDigests) != 0 {
			t.Fatal(report, e)
		}
		return
	}
	s, _, base, v, data, r := setup(t)
	publish(t, s, v, data, r)
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGCLeaseAcrossProcess$")
	cmd.Env = append(os.Environ(), "CUTLY_CHUNKCACHE_GC_CHILD=1", "CUTLY_CHUNKCACHE_GC_ROOT="+filepath.Join(base, "cas"))
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("GC child %v %s", e, out)
	}
	if _, e = io.Copy(io.Discard, h); e != nil {
		t.Fatal(e)
	}
	if e = h.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestHeartbeatExtendsRealCASLease(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	options := cas.Options{LeaseTTL: time.Second, Clock: cas.ClockFunc(func() time.Time { return time.Unix(0, clock.Load()) })}
	objects, e := cas.New(filepath.Join(base, "heartbeat-cas"), options)
	if e != nil {
		t.Fatal(e)
	}
	s.artifacts = objects
	publish(t, s, v, data, r)
	h, e := s.Lookup(context.Background(), v)
	if e != nil {
		t.Fatal(e)
	}
	clock.Add(int64(800 * time.Millisecond))
	if e = h.Heartbeat(context.Background()); e != nil {
		t.Fatal(e)
	}
	clock.Add(int64(500 * time.Millisecond)) // Beyond original expiry, inside renewed lease.
	gc, e := cas.New(filepath.Join(base, "heartbeat-cas"), options)
	if e != nil {
		t.Fatal(e)
	}
	report, e := gc.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
	if !errors.Is(e, cas.ErrQuotaUnsatisfied) || len(report.SkippedActive) != 1 {
		t.Fatal(report, e)
	}
	if _, e = io.Copy(io.Discard, h); e != nil {
		t.Fatal(e)
	}
	if e = h.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestRejectHardlinkedIndexWithoutTouchingOtherLink(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	publish(t, s, v, data, r)
	k, _ := Key(v)
	path := filepath.Join(s.root, "entries", entryName(k))
	alias := filepath.Join(base, "external-index")
	if e := os.Link(path, alias); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(alias)
	if e != nil {
		t.Fatal(e)
	}
	if h, e := s.Lookup(context.Background(), v); h != nil || !errors.Is(e, ErrCorrupt) {
		t.Fatal(h, e)
	}
	after, e := os.ReadFile(alias)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal(e)
	}
}
func TestStrictNestedIndexMembers(t *testing.T) {
	_, _, _, v, _, r := setup(t)
	_, raw, e := encodeRecord(v, r)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"unit_bytes":4`), []byte(`"unit_bytes":null`), 1),
		bytes.Replace(raw, []byte(`"unit_bytes":4`), []byte(`"Unit_Bytes":4`), 1),
		bytes.Replace(raw, []byte(`"unit_bytes":4`), []byte(`"unit_bytes":4,"unit_bytes":4`), 1),
		bytes.Replace(raw, []byte(`"unit_bytes":4`), []byte(`"unit_bytes":4,"unexpected":1`), 1),
	} {
		if bytes.Equal(bad, raw) {
			t.Fatal("mutation did not apply")
		}
		if _, e := decodeRecord(bad); e == nil {
			t.Fatal("accepted hostile index")
		}
	}
	var obj map[string]json.RawMessage
	if e = json.Unmarshal(raw, &obj); e != nil {
		t.Fatal(e)
	}
	obj["receipt"] = []byte("null")
	bad, e := json.Marshal(obj)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = decodeRecord(bad); e == nil {
		t.Fatal("accepted null receipt")
	}
}
