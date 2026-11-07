package wavsource

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
)

func TestInspectMatchesCompiledProbe(t *testing.T) {
	encoded := strictWAV(4, 1, []int16{-32768, 0, 32767, 16384})
	document := audioDocument(encoded, 4, 4, rt(2, 1), rt(1, 1))
	provider := compileProvider(t, document, encoded)
	want := provider.MediaBindings()[0].Manifest
	got, err := Inspect(context.Background(), want.Resource.AssetID, want.Resource.ContentHash, int64(len(encoded)), encoded, wavdecode.DefaultLimits())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native probe parity: %v\ngot=%+v\nwant=%+v", err, got, want)
	}
	gotHash, err := probe.Fingerprint(got)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := probe.Fingerprint(want)
	if err != nil || gotHash != wantHash {
		t.Fatalf("probe fingerprint parity: %v", err)
	}
	encoded[0] = '!'
	if hash, err := probe.Fingerprint(got); err != nil || hash != wantHash {
		t.Fatal("inspection retained encoded bytes", err)
	}
}

type inspectionPanicContext struct{ context.Context }

func (inspectionPanicContext) Err() error { panic("inspection context") }

func TestInspectFailsZeroBeforeDecode(t *testing.T) {
	encoded := []byte("not a WAV")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		hash   string
		size   int64
		limits wavdecode.Limits
		want   error
	}{
		{"nil", nil, digest, int64(len(encoded)), wavdecode.Limits{}, ErrContextInvalid},
		{"cancel", cancelled, digest, int64(len(encoded)), wavdecode.Limits{}, context.Canceled},
		{"panic", inspectionPanicContext{context.Background()}, digest, int64(len(encoded)), wavdecode.Limits{}, ErrDecodeFailed},
		{"hash-before-decode", context.Background(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", int64(len(encoded)), wavdecode.Limits{}, ErrContentMismatch},
		{"size-before-decode", context.Background(), digest, 100, wavdecode.Limits{}, ErrBindingInvalid},
		{"limit-before-decode", context.Background(), digest, int64(len(encoded)), wavdecode.Limits{MaxEncodedBytes: 1}, ErrResourceLimit},
		{"malformed", context.Background(), digest, int64(len(encoded)), wavdecode.Limits{}, ErrDecodeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Inspect(tc.ctx, "audio", tc.hash, tc.size, encoded, tc.limits)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, probe.ProbeManifest{}) {
				t.Fatalf("failure-zero: %v", err)
			}
		})
	}
}
