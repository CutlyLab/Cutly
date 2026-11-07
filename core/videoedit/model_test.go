package videoedit

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validTestDocument(t *testing.T) Document {
	t.Helper()
	zero := mustTime(t, 0, 1)
	ten := mustTime(t, 10, 1)
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            "document-1",
		Revision:      3,
		Assets: []Asset{
			{ID: "video-1", Kind: AssetKindVideo, URI: "footage.mp4", ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Duration: &ten},
			{ID: "audio-1", Kind: AssetKindAudio, URI: "music.wav", ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Duration: &ten},
		},
		Timelines: []Timeline{{
			ID: "timeline-1",
			Tracks: []Track{
				{ID: "video-track", Kind: TrackKindVideo, Clips: []Clip{{
					ID: "video-clip", AssetID: "video-1",
					TimelineRange: TimeRange{Start: zero, Duration: mustTime(t, 5, 1)},
					SourceRange:   TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 5, 1)},
				}}},
				{ID: "audio-track", Kind: TrackKindAudio, Clips: []Clip{{
					ID: "audio-clip", AssetID: "audio-1",
					TimelineRange: TimeRange{Start: zero, Duration: ten},
					SourceRange:   TimeRange{Start: zero, Duration: ten},
				}}},
			},
		}},
	}
}

func TestValidateDocumentAcceptsValidModel(t *testing.T) {
	diagnostics := ValidateDocument(validTestDocument(t))
	if len(diagnostics) != 0 {
		t.Fatalf("valid document diagnostics: %+v", diagnostics)
	}
}

func TestValidateDocumentReportsStableCrossReferenceDiagnostics(t *testing.T) {
	document := validTestDocument(t)
	videoTrack := &document.Timelines[0].Tracks[0]
	videoTrack.Clips = append(videoTrack.Clips, Clip{
		ID:      "video-clip", // duplicate on purpose
		AssetID: "missing-asset",
		TimelineRange: TimeRange{
			Start: mustTime(t, 4, 1), Duration: mustTime(t, 2, 1),
		},
		SourceRange: TimeRange{
			Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1),
		},
	})

	diagnostics := ValidateDocument(document)
	wantCodes := map[string]bool{
		CodeDuplicateID:      false,
		CodeClipAssetMissing: false,
		CodeClipOverlap:      false,
	}
	for _, diagnostic := range diagnostics {
		if _, wanted := wantCodes[diagnostic.Code]; wanted {
			wantCodes[diagnostic.Code] = true
		}
		if diagnostic.Path == "" || diagnostic.Message == "" {
			t.Fatalf("diagnostic must be actionable: %+v", diagnostic)
		}
	}
	for code, found := range wantCodes {
		if !found {
			t.Errorf("missing diagnostic %s: %+v", code, diagnostics)
		}
	}
}

func TestValidateDocumentRejectsSourcePastAssetDuration(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = TimeRange{
		Start: mustTime(t, 8, 1), Duration: mustTime(t, 4, 1),
	}
	diagnostics := ValidateDocument(document)
	if !containsDiagnostic(diagnostics, CodeClipSourceOutOfBounds) {
		t.Fatalf("missing source bound diagnostic: %+v", diagnostics)
	}
}

func TestValidateDocumentRejectsNonCanonicalContentHash(t *testing.T) {
	document := validTestDocument(t)
	document.Assets[0].ContentHash = "sha256:not-a-digest"
	diagnostics := ValidateDocument(document)
	if !containsDiagnosticAt(diagnostics, CodeAssetHashInvalid, "/assets/0/content_hash") {
		t.Fatalf("missing content hash diagnostic: %+v", diagnostics)
	}
}

func TestValidateDocumentRejectsOverflowingRangeEnds(t *testing.T) {
	document := validTestDocument(t)
	overflowing := TimeRange{
		Start:    RationalTime{Value: math.MaxInt64, Rate: 1},
		Duration: RationalTime{Value: 1, Rate: 1},
	}
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange = overflowing
	document.Timelines[0].Tracks[0].Clips[0].SourceRange = overflowing
	document.Annotations = []Annotation{{
		ID:        "overflowing-annotation",
		Namespace: "test.range",
		Range:     &overflowing,
		Value:     json.RawMessage(`true`),
	}}

	diagnostics := ValidateDocument(document)
	for _, path := range []string{
		"/timelines/0/tracks/0/clips/0/timeline_range",
		"/timelines/0/tracks/0/clips/0/source_range",
		"/annotations/0/range",
	} {
		if !containsDiagnosticAt(diagnostics, CodeTimeRangeInvalid, path) {
			t.Fatalf("missing overflowing range diagnostic at %s: %+v", path, diagnostics)
		}
	}
}

func TestValidateDocumentRequiresChronologicalClipOrder(t *testing.T) {
	document := validTestDocument(t)
	first := document.Timelines[0].Tracks[0].Clips[0]
	later := first
	later.ID = "later-clip"
	later.TimelineRange = TimeRange{Start: mustTime(t, 6, 1), Duration: mustTime(t, 1, 1)}
	later.SourceRange = TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)}
	document.Timelines[0].Tracks[0].Clips = []Clip{later, first}

	diagnostics := ValidateDocument(document)
	if !containsDiagnosticAt(diagnostics, CodeClipOrderInvalid, "/timelines/0/tracks/0/clips/1/timeline_range/start") {
		t.Fatalf("missing clip order diagnostic: %+v", diagnostics)
	}
}

func TestValidateDocumentReportsEveryNestedOverlap(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips = []Clip{
		{
			ID: "outer", AssetID: "video-1",
			TimelineRange: TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 10, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 10, 1)},
		},
		{
			ID: "nested-1", AssetID: "video-1",
			TimelineRange: TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 1, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		},
		{
			ID: "nested-2", AssetID: "video-1",
			TimelineRange: TimeRange{Start: mustTime(t, 3, 1), Duration: mustTime(t, 1, 1)},
			SourceRange:   TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		},
	}

	diagnostics := ValidateDocument(document)
	overlaps := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == CodeClipOverlap {
			overlaps++
		}
	}
	if overlaps != 2 {
		t.Fatalf("nested overlap diagnostics = %d, want 2: %+v", overlaps, diagnostics)
	}
}

func TestCanonicalJSONNormalizesMapsAndRawMessages(t *testing.T) {
	left := validTestDocument(t)
	left.Metadata = map[string]json.RawMessage{
		"z": json.RawMessage(` { "b": 2, "a": 1 } `),
		"a": json.RawMessage(`true`),
	}
	right := validTestDocument(t)
	right.Metadata = map[string]json.RawMessage{
		"a": json.RawMessage(`true`),
		"z": json.RawMessage(`{"a":1,"b":2}`),
	}

	leftJSON, err := CanonicalJSON(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := CanonicalJSON(right)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", leftJSON, rightJSON)
	}
	leftHash, _ := DocumentHash(left)
	rightHash, _ := DocumentHash(right)
	if leftHash != rightHash || !strings.HasPrefix(leftHash, "sha256:") {
		t.Fatalf("hashes not stable: %q %q", leftHash, rightHash)
	}
}

func TestCanonicalJSONRejectsDuplicateRawMessageMembers(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].Properties = map[string]json.RawMessage{
		"mode": json.RawMessage(`{"mode":"safe","mode":"evil"}`),
	}
	if _, err := CanonicalJSON(document); err == nil || !strings.Contains(err.Error(), `duplicate object member "mode"`) {
		t.Fatalf("CanonicalJSON error = %v", err)
	}
	if _, err := DocumentHash(document); err == nil || !strings.Contains(err.Error(), `duplicate object member "mode"`) {
		t.Fatalf("DocumentHash error = %v", err)
	}

	clean := validTestDocument(t)
	clean.Timelines[0].Tracks[0].Clips[0].Properties = map[string]json.RawMessage{
		"mode": json.RawMessage(`{"mode":"evil"}`),
	}
	if _, err := DocumentHash(clean); err != nil {
		t.Fatalf("clean DocumentHash: %v", err)
	}
}

func containsDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func containsDiagnosticAt(diagnostics []Diagnostic, code, path string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && diagnostic.Path == path {
			return true
		}
	}
	return false
}
