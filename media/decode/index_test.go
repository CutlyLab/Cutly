package videodecode

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestSelectFrameUsesHalfOpenDisplayIntervalsAndKeyframe(t *testing.T) {
	index := fixtureIndex(t, []FrameRecord{
		{PTS: 0, DurationPTS: 40, Keyframe: true},
		{PTS: 40, DurationPTS: 20},
		{PTS: 70, DurationPTS: 10, Keyframe: true},
		{PTS: 80, DurationPTS: 20},
	})
	tests := []struct {
		name string
		ms   int64
		want int
		seek int64
		err  error
	}{
		{name: "inside first", ms: 39, want: 0, seek: 0},
		{name: "boundary selects next", ms: 40, want: 1, seek: 0},
		{name: "gap fails", ms: 65, err: ErrFrameUnavailable},
		{name: "second gop", ms: 85, want: 3, seek: 70},
		{name: "exclusive end", ms: 100, err: ErrFrameUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := SelectFrame(index, videoedit.RationalTime{Value: test.ms, Rate: 1000})
			if !errors.Is(err, test.err) {
				t.Fatalf("SelectFrame error = %v, want %v", err, test.err)
			}
			if test.err == nil && (selection.FrameOrdinal != test.want || selection.SeekPTS != test.seek) {
				t.Fatalf("selection = %+v, want ordinal=%d seek=%d", selection, test.want, test.seek)
			}
		})
	}
}

func TestFrameIndexStrictCanonicalRoundTripAndOwnership(t *testing.T) {
	index := fixtureIndex(t, []FrameRecord{{PTS: 0, DurationPTS: 1, Keyframe: true}})
	raw, err := CanonicalFrameIndex(index, 10)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrameIndex(raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	index.Frames[0].PTS = 99
	if decoded.Frames[0].PTS != 0 {
		t.Fatal("decoded index retained caller storage")
	}
	unknown := []byte(strings.Replace(string(raw), `"frames":`, `"unexpected":1,"frames":`, 1))
	if _, err := DecodeFrameIndex(unknown, 10); err == nil {
		t.Fatal("unknown member was accepted")
	}
	duplicate := []byte(strings.Replace(string(raw), `"profile":`, `"profile":"duplicate","profile":`, 1))
	if _, err := DecodeFrameIndex(duplicate, 10); err == nil {
		t.Fatal("duplicate member was accepted")
	}
}

func TestFrameIndexCanonicalGolden(t *testing.T) {
	index := fixtureIndex(t, []FrameRecord{{PTS: 0, DurationPTS: 40, Keyframe: true}, {PTS: 40, DurationPTS: 60}})
	raw, err := CanonicalFrameIndex(index, 10)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/frame-index-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != strings.TrimSpace(string(want)) {
		t.Fatalf("canonical index changed\ngot:  %s\nwant: %s", raw, strings.TrimSpace(string(want)))
	}
}

func TestNewFrameIndexBindsAndOwnsRecords(t *testing.T) {
	frames := []FrameRecord{{PTS: 0, DurationPTS: 100, Keyframe: true}}
	manifest, _ := fixtureManifestAndIndex(t, []byte("fixture"), frames)
	index, fingerprint, err := NewFrameIndex(context.Background(), manifest, 0, frames, 10)
	if err != nil {
		t.Fatal(err)
	}
	frames[0].PTS = 99
	if index.Frames[0].PTS != 0 || !canonicalSHA256(fingerprint) {
		t.Fatalf("index/fingerprint = %+v %q", index, fingerprint)
	}
}

func TestBindFrameIndexRejectsProbeAndKeyframeSubstitution(t *testing.T) {
	frames := []FrameRecord{{PTS: 0, DurationPTS: 40, Keyframe: true}, {PTS: 40, DurationPTS: 60}}
	manifest, index := fixtureManifestAndIndex(t, []byte("fixture"), frames)
	if err := BindFrameIndex(context.Background(), manifest, index, 10); err != nil {
		t.Fatalf("valid bind: %v", err)
	}
	mutated := index
	mutated.ResourceHash = "sha256:" + strings.Repeat("0", 64)
	if !errors.Is(BindFrameIndex(context.Background(), manifest, mutated, 10), ErrIndexMismatch) {
		t.Fatal("resource substitution was accepted")
	}
	mutated = index
	mutated.Frames = append([]FrameRecord(nil), index.Frames...)
	mutated.Frames[1].Keyframe = true
	if !errors.Is(BindFrameIndex(context.Background(), manifest, mutated, 10), ErrIndexMismatch) {
		t.Fatal("keyframe substitution was accepted")
	}
}

func TestValidateFrameIndexRejectsOverlapAndMissingInitialKeyframe(t *testing.T) {
	index := fixtureIndex(t, []FrameRecord{{PTS: 0, DurationPTS: 10, Keyframe: true}, {PTS: 9, DurationPTS: 1}})
	if !errors.Is(ValidateFrameIndex(context.Background(), index, 10), ErrIndexInvalid) {
		t.Fatal("overlap accepted")
	}
	index.Frames = []FrameRecord{{PTS: 0, DurationPTS: 10}}
	if !errors.Is(ValidateFrameIndex(context.Background(), index, 10), ErrIndexInvalid) {
		t.Fatal("index without initial keyframe accepted")
	}
}

func FuzzDecodeFrameIndex(f *testing.F) {
	index := fixtureIndexForFuzz([]FrameRecord{{PTS: 0, DurationPTS: 1, Keyframe: true}})
	raw, _ := CanonicalFrameIndex(index, 10)
	f.Add(raw)
	f.Add([]byte(`{"frames":[]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodeFrameIndex(raw, 100)
	})
}

func fixtureIndex(t *testing.T, frames []FrameRecord) FrameIndex {
	t.Helper()
	index := fixtureIndexForFuzz(frames)
	if err := ValidateFrameIndex(context.Background(), index, 100); err != nil && !(len(frames) > 1 && frames[1].PTS < frames[0].PTS+frames[0].DurationPTS) {
		t.Fatalf("fixture index: %v", err)
	}
	return index
}

func fixtureIndexForFuzz(frames []FrameRecord) FrameIndex {
	return FrameIndex{
		SchemaVersion: FrameIndexSchemaVersion, Profile: FrameIndexProfileV1,
		ProbeFingerprint: "sha256:" + strings.Repeat("1", 64), ResourceHash: "sha256:" + strings.Repeat("2", 64),
		StreamIndex: 0, TimeBase: videoedit.RationalTime{Value: 1, Rate: 1000}, Frames: append([]FrameRecord(nil), frames...),
	}
}
