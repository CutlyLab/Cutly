package reconstructv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

func diskFixture(raw []byte) (renderapi.MaterializedResourceV2, resourceidentityv2.Binding) {
	h := sha256.Sum256(raw)
	claim := renderapi.MaterializedResourceV2{AssetID: "video", Kind: documentv2.AssetKindVideo, ContentHash: "sha256:" + hex.EncodeToString(h[:]), SizeBytes: int64(len(raw))}
	return claim, resourceidentityv2.Binding{AssetID: claim.AssetID, Kind: claim.Kind, ContentHash: claim.ContentHash, SizeBytes: claim.SizeBytes}
}

func TestDiskSnapshotOwnsVerifiedFile(t *testing.T) {
	raw := bytes.Repeat([]byte{1, 2, 3}, 40000)
	claim, binding := diskFixture(raw)
	r := &testReader{read: bytes.NewReader(raw).Read}
	source := testSource{
		authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
			return binding, nil
		},
		open: func(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error) {
			return r, nil
		},
	}
	root := t.TempDir()
	got, snapshot, err := snapshotResourceFile(context.Background(), source, "document", claim, uint64(len(raw)), root)
	if err != nil || got != binding || snapshot == nil || r.closed != 1 {
		t.Fatalf("snapshot: %v %v %v", got, snapshot, err)
	}
	defer snapshot.Close()
	stored, err := os.ReadFile(snapshot.path)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatalf("snapshot bytes: %v", err)
	}
	info, err := os.Stat(snapshot.path)
	if err != nil || info.Mode().Perm() != 0400 || filepath.Dir(snapshot.root) != root {
		t.Fatalf("private path/mode: %v %v", info, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := snapshot.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: %v %v", entries, err)
	}
}

func TestDiskSnapshotFailureZeroAndCleanup(t *testing.T) {
	for _, name := range []string{"short", "extra", "hash", "read-panic", "close-panic", "cancel-read", "cancel-close", "open-error-reader", "nil-reader", "negative-count", "large-count", "no-progress", "binding", "authorize-panic"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			claim, binding := diskFixture([]byte{1, 2, 3})
			r := &testReader{read: bytes.NewReader([]byte{1, 2, 3}).Read}
			opened := false
			source := testSource{
				authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
					if name == "authorize-panic" {
						panic("test")
					}
					if name == "binding" {
						b := binding
						b.SizeBytes++
						return b, nil
					}
					return binding, nil
				},
				open: func(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error) {
					if name == "nil-reader" {
						return nil, nil
					}
					opened = true
					if name == "open-error-reader" {
						return r, io.ErrUnexpectedEOF
					}
					return r, nil
				},
			}
			switch name {
			case "short":
				r.read = bytes.NewReader([]byte{1, 2}).Read
			case "extra":
				r.read = bytes.NewReader([]byte{1, 2, 3, 4}).Read
			case "hash":
				r.read = bytes.NewReader([]byte{9, 2, 3}).Read
			case "read-panic":
				r.read = func([]byte) (int, error) { panic("test") }
			case "close-panic":
				r.close = func() error { panic("test") }
			case "cancel-close":
				r.close = func() error { cancel(); return nil }
			case "cancel-read":
				r.read = func(p []byte) (int, error) { cancel(); return len(p), nil }
			case "negative-count":
				r.read = func([]byte) (int, error) { return -1, nil }
			case "large-count":
				r.read = func(p []byte) (int, error) { return len(p) + 1, nil }
			case "no-progress":
				r.read = func([]byte) (int, error) { return 0, nil }
			}
			root := t.TempDir()
			b, snapshot, err := snapshotResourceFile(ctx, source, "document", claim, 3, root)
			if err == nil || snapshot != nil || b != (resourceidentityv2.Binding{}) {
				t.Fatalf("failure zero: %v %v %v", b, snapshot, err)
			}
			if opened && r.closed != 1 {
				t.Fatalf("closed %d", r.closed)
			}
			entries, e := os.ReadDir(root)
			if e != nil || len(entries) != 0 {
				t.Fatalf("cleanup: %v %v", entries, e)
			}
		})
	}
}

func TestDiskSnapshotAdmissionBeforeCallbacks(t *testing.T) {
	claim, _ := diskFixture([]byte{1, 2, 3})
	source := testSource{authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
		t.Fatal("unexpected callback")
		return resourceidentityv2.Binding{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx     context.Context
		size    int64
		maximum uint64
		scratch string
		want    error
	}{
		{nil, 3, 3, t.TempDir(), ErrContext},
		{ctx, 3, 3, t.TempDir(), context.Canceled},
		{context.Background(), 3, 2, t.TempDir(), ErrLimit},
		{context.Background(), 0, 3, t.TempDir(), ErrLimit},
		{context.Background(), 3, 3, "relative", ErrConfig},
	} {
		claim.SizeBytes = tc.size
		b, snapshot, err := snapshotResourceFile(tc.ctx, source, "document", claim, tc.maximum, tc.scratch)
		if !errors.Is(err, tc.want) || snapshot != nil || !reflect.DeepEqual(b, resourceidentityv2.Binding{}) {
			t.Fatalf("admission: %v %v %v", b, snapshot, err)
		}
	}
}
