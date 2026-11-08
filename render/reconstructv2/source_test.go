package reconstructv2

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

type testSource struct {
	authorize func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error)
	open      func(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error)
}

func (s testSource) Authorize(ctx context.Context, hash string, claim renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
	return s.authorize(ctx, hash, claim)
}
func (s testSource) Open(ctx context.Context, hash string, claim renderapi.MaterializedResourceV2, binding resourceidentityv2.Binding) (io.ReadCloser, error) {
	return s.open(ctx, hash, claim, binding)
}

type testReader struct {
	read   func([]byte) (int, error)
	close  func() error
	closed int
}

func (r *testReader) Read(p []byte) (int, error) { return r.read(p) }
func (r *testReader) Close() error {
	r.closed++
	if r.close != nil {
		return r.close()
	}
	return nil
}

func sourceFixture() (renderapi.MaterializedResourceV2, resourceidentityv2.Binding) {
	fp := "inspection"
	claim := renderapi.MaterializedResourceV2{AssetID: "image", Kind: documentv2.AssetKindImage, ContentHash: "content", SizeBytes: 3, InspectionFingerprint: &fp}
	return claim, resourceidentityv2.Binding{AssetID: claim.AssetID, Kind: claim.Kind, ContentHash: claim.ContentHash, SizeBytes: claim.SizeBytes}
}

func TestSnapshotResourceOwnsBytesAndClaims(t *testing.T) {
	claim, expected := sourceFixture()
	raw := []byte{1, 2, 3}
	r := &testReader{read: bytes.NewReader(raw).Read}
	s := testSource{
		authorize: func(_ context.Context, hash string, c renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
			if hash != "document" {
				t.Fatal("original hash lost")
			}
			*c.InspectionFingerprint = "mutated"
			return expected, nil
		},
		open: func(_ context.Context, hash string, c renderapi.MaterializedResourceV2, b resourceidentityv2.Binding) (io.ReadCloser, error) {
			if hash != "document" || *c.InspectionFingerprint != "inspection" || b != expected {
				t.Fatal("callback claim alias")
			}
			*c.InspectionFingerprint = "mutated again"
			return r, nil
		},
	}
	b, got, err := snapshotResource(context.Background(), s, "document", claim, 3)
	if err != nil || b != expected || !bytes.Equal(got, raw) || r.closed != 1 || *claim.InspectionFingerprint != "inspection" {
		t.Fatalf("snapshot: %v %v %v", b, got, err)
	}
	raw[0] = 99
	if got[0] != 1 {
		t.Fatal("borrowed reader storage escaped")
	}
}

func TestSnapshotResourceFailureClosesAndPublishesZero(t *testing.T) {
	for _, name := range []string{"short", "extra", "no-progress", "negative-count", "oversized-count", "read-error", "read-panic", "close-error", "close-panic", "close-cancel", "open-error-reader", "open-panic", "nil-reader", "authorize-error", "authorize-panic", "binding-mismatch", "cancel-read"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			claim, expected := sourceFixture()
			r := &testReader{read: bytes.NewReader([]byte{1, 2, 3}).Read}
			opened := false
			s := testSource{
				authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
					switch name {
					case "authorize-error":
						return expected, io.ErrUnexpectedEOF
					case "authorize-panic":
						panic("test")
					case "binding-mismatch":
						b := expected
						b.SizeBytes++
						return b, nil
					}
					return expected, nil
				},
				open: func(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error) {
					if name == "open-panic" {
						panic("test")
					}
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
			case "no-progress":
				r.read = func([]byte) (int, error) { return 0, nil }
			case "negative-count":
				r.read = func([]byte) (int, error) { return -1, nil }
			case "oversized-count":
				r.read = func(p []byte) (int, error) { return len(p) + 1, nil }
			case "read-error":
				r.read = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
			case "read-panic":
				r.read = func([]byte) (int, error) { panic("test") }
			case "close-error":
				r.close = func() error { return io.ErrUnexpectedEOF }
			case "close-panic":
				r.close = func() error { panic("test") }
			case "close-cancel":
				r.close = func() error { cancel(); return nil }
			case "cancel-read":
				r.read = func(p []byte) (int, error) { cancel(); return len(p), nil }
			}
			b, got, err := snapshotResource(ctx, s, "document", claim, 3)
			if err == nil || b != (resourceidentityv2.Binding{}) || got != nil {
				t.Fatalf("not failure zero: %v %v %v", b, got, err)
			}
			if opened && r.closed != 1 {
				t.Fatalf("reader closed %d times", r.closed)
			}
		})
	}
}

func TestSnapshotResourceAdmissionBeforeCallbacks(t *testing.T) {
	claim, _ := sourceFixture()
	s := testSource{authorize: func(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
		t.Fatal("callback before admission")
		return resourceidentityv2.Binding{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx     context.Context
		size    int64
		maximum uint64
		want    error
	}{
		{nil, 3, 3, ErrContext}, {ctx, 3, 3, context.Canceled}, {context.Background(), 0, 3, ErrLimit}, {context.Background(), -1, 3, ErrLimit}, {context.Background(), 3, 2, ErrLimit},
	} {
		claim.SizeBytes = tc.size
		b, raw, err := snapshotResource(tc.ctx, s, "document", claim, tc.maximum)
		if !errors.Is(err, tc.want) || !reflect.DeepEqual(b, resourceidentityv2.Binding{}) || raw != nil {
			t.Fatalf("admission: %v %v %v", b, raw, err)
		}
	}
}
