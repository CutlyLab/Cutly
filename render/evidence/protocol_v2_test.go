package evidence

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func manifestFixtureV2(t *testing.T) ManifestV2 {
	t.Helper()
	raw, err := os.ReadFile("../../conformance/fixtures/evidence-document-v2-mixed.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeManifestV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestManifestV2DecodedBinaryRootParity(t *testing.T) {
	m := manifestFixtureV2(t)
	for _, s := range []*DecodedSequence{m.DecodedVideo, m.DecodedAudio} {
		got, err := decodedRootV2(context.Background(), *s)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ComputeDecodedRoot(*s)
		if err != nil || want != got {
			t.Fatal("V1 binary algorithm drift", err)
		}
	}
	// A lossless PCM layout uses the same root only for identical range,
	// partition and content. RGBA16 is never compared to final RGBA8.
	s := DecodedSequence{Domain: m.Assembly.Aggregate.Audio.Domain, Unit: EvidenceSamples, UnitBytes: m.Assembly.Aggregate.Audio.UnitBytes, Range: m.Assembly.SampleRange}
	for _, d := range m.Assembly.Audio {
		s.Chunks = append(s.Chunks, DecodedChunk{First: d.Range.First, Count: d.Range.Count, ContentHash: d.ContentHash})
	}
	root, err := decodedRootV2(context.Background(), s)
	if err != nil || root != m.Assembly.Aggregate.Audio.RootHash {
		t.Fatal("lossless root drift", err)
	}
	// Protocol-level lossless example: supplying precisely the same canonical
	// PCM bytes/partition preserves its root. This is not a real codec test.
	m.Assembly.Output.AudioCodec = "pcm_s16le"
	m.Assembly.Fingerprint, err = bindingFingerprintV2(m.Assembly)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := NewManifestV2(context.Background(), m.Assembly, m.WorkerExecutableDigest, m.Artifact.ContentHash, m.Artifact.SizeBytes, m.DecodedVideo, &s)
	if err != nil || frozen.DecodedAudio.RootHash != frozen.Assembly.Aggregate.Audio.RootHash {
		t.Fatal("lossless evidence root mismatch", err)
	}
}
func TestManifestV2ResignedSemanticAttacks(t *testing.T) {
	for name, mutate := range map[string]func(*ManifestV2){
		"artifact-uri":       func(m *ManifestV2) { m.Artifact.URI = "file:///foreign.mp4" },
		"worker":             func(m *ManifestV2) { m.WorkerExecutableDigest = "SHA256:" + strings.Repeat("0", 64) },
		"final-video-layout": func(m *ManifestV2) { m.DecodedVideo.Domain = m.Assembly.Output.Intermediate.VideoSurfaceProfile },
		"final-audio-layout": func(m *ManifestV2) { m.DecodedAudio.UnitBytes++ },
		"root":               func(m *ManifestV2) { m.DecodedVideo.RootHash = m.WorkerExecutableDigest },
		"coverage":           func(m *ManifestV2) { m.DecodedVideo.Chunks[0].Count-- },
		"overflow":           func(m *ManifestV2) { m.DecodedAudio.Chunks[0].First = math.MaxInt64 },
		"descriptor-size":    func(m *ManifestV2) { m.Assembly.Video[0].SizeBytes++ },
		"aggregate-root":     func(m *ManifestV2) { m.Assembly.Aggregate.Video.RootHash = m.WorkerExecutableDigest },
		"duplicate-key":      func(m *ManifestV2) { m.Assembly.Video[0].CacheKey = m.Assembly.Audio[0].CacheKey },
		"wrong-partition":    func(m *ManifestV2) { m.Assembly.Audio[0].Range.Count-- },
		"seed":               func(m *ManifestV2) { m.Assembly.DeterministicSeed = 1 },
		"output-clock":       func(m *ManifestV2) { m.Assembly.Output.SampleRate++ },
	} {
		t.Run(name, func(t *testing.T) {
			m := manifestFixtureV2(t)
			mutate(&m)
			var err error
			m.Assembly.Fingerprint, err = bindingFingerprintV2(m.Assembly)
			if err != nil {
				t.Fatal(err)
			}
			m.Fingerprint, err = fingerprintV2(m)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeV2(m)
			if err != nil {
				t.Fatal(err)
			}
			out, err := DecodeManifestV2(raw)
			if err == nil || !reflect.DeepEqual(out, ManifestV2{}) {
				t.Fatal("resigned attack accepted")
			}
		})
	}
}

type panicContextV2 struct{}

func (panicContextV2) Deadline() (time.Time, bool) { return time.Time{}, false }
func (panicContextV2) Done() <-chan struct{}       { return nil }
func (panicContextV2) Err() error                  { panic("context panic") }
func (panicContextV2) Value(any) any               { return nil }
func TestManifestV2Boundaries(t *testing.T) {
	m := manifestFixtureV2(t)
	for _, ctx := range []context.Context{nil, panicContextV2{}} {
		v, err := VerifyManifestV2AgainstBinding(ctx, m, m.Assembly)
		if err == nil || v.Valid() {
			t.Fatal("failure must be zero")
		}
		out, err := NewManifestV2(ctx, m.Assembly, m.WorkerExecutableDigest, m.Artifact.ContentHash, m.Artifact.SizeBytes, m.DecodedVideo, m.DecodedAudio)
		if err == nil || !reflect.DeepEqual(out, ManifestV2{}) {
			t.Fatal("failure returned draft")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyManifestV2AgainstBinding(ctx, m, m.Assembly); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	v, err := VerifyManifestV2(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := v.Canonical()
	raw[0] = '!'
	a, _ := v.Manifest()
	a.Assembly.Audio[0].ContentHash = "changed"
	a.DecodedVideo.Chunks[0].ContentHash = "changed"
	b, _ := v.Manifest()
	if b.Assembly.Audio[0].ContentHash == "changed" || b.DecodedVideo.Chunks[0].ContentHash == "changed" {
		t.Fatal("aliased getter")
	}
	oversized := bytes.Repeat([]byte(" "), MaxEvidenceBytesV2+1)
	if _, err := DecodeManifestV2(oversized); !errors.Is(err, ErrEvidenceLimitV2) {
		t.Fatal(err)
	}
	binding, err := VerifyBindingV2(context.Background(), m.Assembly)
	if err != nil || !binding.Valid() {
		t.Fatal(err)
	}
	bindingRaw, _ := binding.Canonical()
	bindingRaw[0] = '!'
	binding1, _ := binding.Binding()
	binding1.Audio[0].ContentHash = "changed"
	binding1.Aggregate.Video.RootHash = "changed"
	binding2, _ := binding.Binding()
	if binding2.Audio[0].ContentHash == "changed" || binding2.Aggregate.Video.RootHash == "changed" {
		t.Fatal("binding getter aliases state")
	}
	if _, err := (VerifiedBindingV2{}).Binding(); err == nil {
		t.Fatal("zero binding accepted")
	}
	for _, ctx := range []context.Context{nil, panicContextV2{}} {
		if out, err := VerifyBindingV2(ctx, m.Assembly); err == nil || out.Valid() {
			t.Fatal("binding failure not zero")
		}
	}
	m.DecodedVideo.Chunks = make([]DecodedChunk, MaxEvidenceDescriptorsV2+1)
	if _, err := VerifyManifestV2(m); !errors.Is(err, ErrEvidenceLimitV2) {
		t.Fatal(err)
	}
}
