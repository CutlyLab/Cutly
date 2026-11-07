package videoedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func FuzzCanonicalJSONIsIdempotent(f *testing.F) {
	for _, seed := range []string{
		`null`,
		`{"z":1,"a":[true,false,null]}`,
		`{"nested":{"b":2,"a":1},"number":1e3}`,
		`{"duplicate":1,"duplicate":2}`,
		`[0,-1,9223372036854775807]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		canonical, err := CanonicalJSON(json.RawMessage(raw))
		if err != nil {
			return
		}
		again, err := CanonicalJSON(json.RawMessage(canonical))
		if err != nil {
			t.Fatalf("canonical output was not accepted: %v; output=%q", err, canonical)
		}
		if !bytes.Equal(canonical, again) {
			t.Fatalf("canonicalization is not idempotent: first=%q second=%q", canonical, again)
		}
	})
}

func FuzzRationalAddSubtractRoundTrip(f *testing.F) {
	for _, seed := range [][4]int64{
		{0, 1, 1, 1},
		{1001, 30000, -1, 48000},
		{math.MaxInt64, math.MaxInt64, -1, math.MaxInt64},
		{math.MinInt64, math.MaxInt64, 1, math.MaxInt64},
	} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}
	f.Fuzz(func(t *testing.T, aValue, aRate, bValue, bRate int64) {
		if aRate == 0 || bRate == 0 {
			return
		}
		a, err := NewRationalTime(aValue, aRate)
		if err != nil {
			return
		}
		b, err := NewRationalTime(bValue, bRate)
		if err != nil {
			return
		}
		sum, err := a.Add(b)
		if errors.Is(err, ErrTimeOverflow) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := sum.Sub(b)
		if err != nil {
			t.Fatalf("representable round trip failed: (%v + %v) - %v: %v", a, b, b, err)
		}
		if recovered != a {
			t.Fatalf("round trip = %+v, want %+v", recovered, a)
		}
	})
}

func FuzzTickRangeContainment(f *testing.F) {
	for _, seed := range [][2]int64{{0, 0}, {0, 1}, {-10, 20}, {math.MaxInt64 - 1, 1}, {math.MinInt64, math.MaxInt64}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, first, count int64) {
		value := TickRange{First: first, Count: count}
		end, err := value.EndExclusive()
		if err != nil {
			return
		}
		atFirst, err := value.Contains(first)
		if err != nil {
			t.Fatal(err)
		}
		atEnd, err := value.Contains(end)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			if atFirst || atEnd {
				t.Fatalf("empty range contains a tick: %+v", value)
			}
			return
		}
		if !atFirst || atEnd {
			t.Fatalf("half-open containment failed for %+v: first=%t end=%t", value, atFirst, atEnd)
		}
		last, err := value.Contains(end - 1)
		if err != nil || !last {
			t.Fatalf("range does not contain its last tick: %+v contains=%t err=%v", value, last, err)
		}
	})
}

func FuzzApplyInverseRestoresDocument(f *testing.F) {
	for _, start := range []int64{-100, -1, 0, 1, 100, math.MaxInt32} {
		f.Add(start)
	}
	f.Fuzz(func(t *testing.T, start int64) {
		duration := RationalTime{Value: 10, Rate: 1}
		at := RationalTime{Value: start, Rate: 1}
		if _, err := at.Add(duration); err != nil {
			return
		}
		document := Document{
			SchemaVersion: SchemaVersion,
			ID:            "property-document",
			Revision:      7,
			Assets: []Asset{{
				ID: "asset", Kind: AssetKindVideo, URI: "asset.mp4", Duration: &duration,
			}},
			Timelines: []Timeline{{
				ID: "timeline",
				Tracks: []Track{{
					ID: "track", Kind: TrackKindVideo,
					Clips: []Clip{{
						ID: "clip", AssetID: "asset",
						TimelineRange: TimeRange{Start: RationalTime{Value: 0, Rate: 1}, Duration: duration},
						SourceRange:   TimeRange{Start: RationalTime{Value: 0, Rate: 1}, Duration: duration},
					}},
				}},
			}},
		}
		applied, err := Apply(document, Transaction{
			ID: "move", IdempotencyKey: "move-once", ExpectedRevision: document.Revision,
			Operations: []Operation{{ID: "move-clip", Kind: OperationMove, ClipID: "clip", At: &at}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if HasErrorDiagnostics(applied.Diagnostics) {
			t.Fatalf("apply diagnostics: %+v", applied.Diagnostics)
		}
		reverted, err := Apply(applied.Document, applied.Inverse, nil)
		if err != nil {
			t.Fatal(err)
		}
		if HasErrorDiagnostics(reverted.Diagnostics) {
			t.Fatalf("inverse diagnostics: %+v", reverted.Diagnostics)
		}
		restored := reverted.Document
		restored.Revision = document.Revision
		want, err := CanonicalJSON(document)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CanonicalJSON(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("inverse did not restore document\nwant %s\n got %s", want, got)
		}
	})
}
