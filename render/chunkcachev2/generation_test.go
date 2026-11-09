//go:build darwin || linux

package chunkcachev2

import (
	"bytes"
	"context"
	"errors"
	"github.com/CutlyLab/Cutly/cache/cas"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerationNewReceiptFinalFingerprintBudget(t *testing.T) {
	_, _, _, v, _, base := setup(t)
	large := strings.Repeat("\x00", 1024)
	v.MediaRuntime.DecoderProfile = large
	v.MediaRuntime.EncoderProfile = large
	v.MediaRuntime.ColorProfile = large
	v.Output.Format = large
	found := false
	for n := 0; n <= 1024; n++ {
		v.Output.VideoCodec = strings.Repeat("\x00", n) + "x"
		if len(v.Output.VideoCodec) > 1024 {
			continue
		}
		r := Receipt{SchemaVersion: 2, Profile: ReceiptProfile, Identity: v, ContentHash: base.ContentHash, SizeBytes: base.SizeBytes, WorkerInvocationID: "review", Attempt: 1, ExecutableDigest: v.ProviderExecutableDigest}
		raw, e := canonicaljson.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		if len(raw) > MaxRecordBytes || len(raw) <= MaxRecordBytes-71 {
			continue
		}
		found = true
		got, e := NewReceipt(v, r.ContentHash, r.SizeBytes, "review", 1)
		if e != nil {
			if got != (Receipt{}) {
				t.Fatal("failure not zero")
			}
			return
		}
		encoded, e := CanonicalReceipt(got)
		if e != nil {
			t.Fatalf("NewReceipt succeeded but cannot serialize: pre=%d err=%v", len(raw), e)
		}
		if _, e = DecodeReceipt(encoded); e != nil {
			t.Fatal(e)
		}
		return
	}
	if !found {
		t.Fatal("boundary setup not found")
	}
}
func TestGenerationOversizeRecordBeforeSource(t *testing.T) {
	s, _, _, v, _, _ := setup(t)
	large := strings.Repeat("\x00", 1024)
	v.Target.TimelineID = "review"
	v.Target.PixelFormat = large
	v.Target.ColorSpace = large
	v.Output.PixelFormat = large
	v.Output.ColorSpace = large
	v.Output.Format = large
	v.Output.VideoCodec = large
	v.Output.AudioCodec = large
	v.MediaRuntime.DecoderProfile = large
	v.MediaRuntime.EncoderProfile = large
	v.MediaRuntime.ColorProfile = large
	if e := validateIdentity(v); e != nil {
		t.Fatal(e)
	}
	r := Receipt{SchemaVersion: 2, Profile: ReceiptProfile, Identity: v, ContentHash: digest([]byte("payload")), SizeBytes: v.Range.Count * v.UnitBytes, WorkerInvocationID: "review", Attempt: 1, ExecutableDigest: v.ProviderExecutableDigest}
	raw, e := canonicaljson.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	r.Fingerprint = digest(raw)
	source := &countReader{}
	if e = s.Publish(context.Background(), v, r, source); e == nil || source.n != 0 {
		t.Fatalf("oversized admission err=%v reads=%d", e, source.n)
	}
	if got, e := NewReceipt(v, r.ContentHash, r.SizeBytes, "review", 1); e == nil || got != (Receipt{}) {
		t.Fatalf("oversized receipt constructor %v", e)
	}
}
func TestGenerationInternalPublishLeaseExpiryFailsClosed(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	opts := cas.Options{LeaseTTL: time.Second, Clock: cas.ClockFunc(func() time.Time { return time.Unix(0, clock.Load()) })}
	objects, e := cas.New(filepath.Join(base, "expired-publish-cas"), opts)
	if e != nil {
		t.Fatal(e)
	}
	s.artifacts = objects
	gc, e := cas.New(filepath.Join(base, "expired-publish-cas"), opts)
	if e != nil {
		t.Fatal(e)
	}
	s.fault = func(p string) error {
		if p == "before-commit" {
			clock.Add(int64(1200 * time.Millisecond))
			rep, e := gc.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
			if e != nil || len(rep.DeletedDigests) != 1 {
				t.Fatalf("expired lease GC %v %v", rep, e)
			}
		}
		return nil
	}
	err := s.Publish(context.Background(), v, r, bytes.NewReader(data))
	if err == nil {
		t.Fatal("Publish success after its hidden internal lease expired and GC deleted payload before index commit")
	}
}
func TestGenerationInternalPublishLeaseAcrossGC(t *testing.T) {
	s, _, base, v, data, r := setup(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	opts := cas.Options{LeaseTTL: time.Second, Clock: cas.ClockFunc(func() time.Time { return time.Unix(0, clock.Load()) })}
	objects, e := cas.New(filepath.Join(base, "publish-lease-cas"), opts)
	if e != nil {
		t.Fatal(e)
	}
	s.artifacts = objects
	gc, e := cas.New(filepath.Join(base, "publish-lease-cas"), opts)
	if e != nil {
		t.Fatal(e)
	}
	s.fault = func(p string) error {
		if p == "before-commit" {
			clock.Add(int64(800 * time.Millisecond))
			rep, e := gc.GC(context.Background(), cas.GCPolicy{MaxBytes: 1, TargetBytes: 0})
			if !errors.Is(e, cas.ErrQuotaUnsatisfied) || len(rep.SkippedActive) != 1 || len(rep.DeletedDigests) != 0 {
				t.Fatalf("internal lease not protecting %v %v", rep, e)
			}
		}
		return nil
	}
	publish(t, s, v, data, r)
	consume(t, s, v, data)
}
func TestGenerationForeignCollisionBeforeCommit(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	k, _ := Key(v)
	path := filepath.Join(s.root, "entries", entryName(k))
	foreign := []byte("foreign-owned-generation")
	s.fault = func(p string) error {
		if p == "before-commit" {
			return os.WriteFile(path, foreign, 0600)
		}
		return nil
	}
	err := s.Publish(context.Background(), v, r, bytes.NewReader(data))
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("wanted collision got %v", err)
	}
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign replaced %q %v", got, e)
	}
}
func TestGenerationForeignReplacementAfterCommit(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	k, _ := Key(v)
	path := filepath.Join(s.root, "entries", entryName(k))
	foreign := []byte("foreign-owned-generation")
	s.fault = func(p string) error {
		if p == "after-commit" {
			tmp := path + ".foreign"
			if e := os.WriteFile(tmp, foreign, 0600); e != nil {
				return e
			}
			return os.Rename(tmp, path)
		}
		return nil
	}
	err := s.Publish(context.Background(), v, r, bytes.NewReader(data))
	if err == nil {
		t.Fatal("Publish returned success after committed receipt inode was replaced with foreign invalid bytes")
	}
	if !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("postcommit error classification %v", err)
	}
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign altered %q %v", got, e)
	}
}
func TestGenerationSameInodeCorruptionAfterCommit(t *testing.T) {
	for _, point := range []string{"after-commit", "after-sync"} {
		t.Run(point, func(t *testing.T) {
			s, _, _, v, data, r := setup(t)
			k, _ := Key(v)
			path := filepath.Join(s.root, "entries", entryName(k))
			s.fault = func(p string) error {
				if p == point {
					return os.WriteFile(path, []byte("corrupt"), 0600)
				}
				return nil
			}
			err := s.Publish(context.Background(), v, r, bytes.NewReader(data))
			if !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("want uncertain got %v", err)
			}
		})
	}
}
func TestGenerationForeignScratchBeforeCommit(t *testing.T) {
	s, _, _, v, data, r := setup(t)
	k, _ := Key(v)
	foreign := []byte("foreign-scratch")
	s.fault = func(p string) error {
		if p == "before-commit" {
			items, e := os.ReadDir(filepath.Join(s.root, "scratch"))
			if e != nil {
				return e
			}
			if len(items) != 1 {
				t.Fatalf("scratch count %d", len(items))
			}
			path := filepath.Join(s.root, "scratch", items[0].Name())
			tmp := path + ".other"
			if e = os.WriteFile(tmp, foreign, 0600); e != nil {
				return e
			}
			return os.Rename(tmp, path)
		}
		return nil
	}
	err := s.Publish(context.Background(), v, r, bytes.NewReader(data))
	if err == nil {
		t.Fatal("committed foreign scratch")
	}
	path := filepath.Join(s.root, "entries", entryName(k))
	if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("foreign committed %v", e)
	}
	items, e := os.ReadDir(filepath.Join(s.root, "scratch"))
	if e != nil {
		t.Fatal(e)
	}
	if len(items) != 1 {
		t.Fatalf("foreign scratch removed, count %d", len(items))
	}
	got, e := os.ReadFile(filepath.Join(s.root, "scratch", items[0].Name()))
	if e != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign scratch altered %q %v", got, e)
	}
}
func TestGenerationLockReplacementBeforeSource(t *testing.T) {
	s, _, _, v, _, r := setup(t)
	path := filepath.Join(s.root, "index.lock")
	if e := os.Rename(path, path+".old"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, nil, 0600); e != nil {
		t.Fatal(e)
	}
	source := &countReader{}
	err := s.Publish(context.Background(), v, r, source)
	if !errors.Is(err, ErrCorrupt) || source.n != 0 {
		t.Fatalf("err=%v reads=%d", err, source.n)
	}
}
