package videoedit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeLegacyEDLQuantizesAndGeneratesStableIDs(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "timeline":[
    {"start":0.1234564,"end":1.5000005,"text":"first"},
    {"start":3,"end":4,"text":"second"}
  ]
}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("DecodeLegacyEDL: diagnostics=%+v err=%v", diagnostics, err)
	}
	clips := document.Timelines[0].Tracks[0].Clips
	if got, want := clips[0].SourceRange.Start, (RationalTime{Value: 123456, Rate: LegacyTimeRate}); got != want {
		t.Fatalf("start = %+v, want %+v", got, want)
	}
	if got, want := clips[0].SourceRange.Duration, (RationalTime{Value: 1376545, Rate: LegacyTimeRate}); got != want {
		t.Fatalf("duration = %+v, want %+v", got, want)
	}
	if clips[0].ID == "" || envelope.Segments[clips[0].ID].Text != "first" {
		t.Fatalf("generated clip/envelope = %q %+v", clips[0].ID, envelope.Segments)
	}

	reordered := []byte(`{"source":"footage.mp4","timeline":[{"start":3,"end":4,"text":"second"},{"start":0.1234564,"end":1.5000005,"text":"first"}]}`)
	other, _, otherDiagnostics, err := DecodeLegacyEDL(reordered)
	if err != nil || HasErrorDiagnostics(otherDiagnostics) {
		t.Fatalf("DecodeLegacyEDL reordered: diagnostics=%+v err=%v", otherDiagnostics, err)
	}
	idsByStart := map[int64]ID{}
	for _, clip := range clips {
		idsByStart[clip.SourceRange.Start.Value] = clip.ID
	}
	for _, clip := range other.Timelines[0].Tracks[0].Clips {
		if idsByStart[clip.SourceRange.Start.Value] != clip.ID {
			t.Fatalf("ID changed after reorder for start %d: %q != %q", clip.SourceRange.Start.Value, idsByStart[clip.SourceRange.Start.Value], clip.ID)
		}
	}
	if document.ID != other.ID {
		t.Fatalf("document ID changed after reorder: %q != %q", document.ID, other.ID)
	}
}

func TestDecodeLegacyEDLStableIDsIncludeCanonicalSegmentContent(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "timeline":[
    {"start":1,"end":2,"text":"alpha","speaker":"Ada","properties":{"z":2,"a":1}},
    {"start":1,"end":2,"text":"beta","speaker":"Grace","properties":{"enabled":true}}
  ]
}`)
	reordered := []byte(`{
  "timeline":[
    {"properties":{"enabled":true},"speaker":"Grace","text":"beta","end":2,"start":1},
    {"properties":{"a":1,"z":2},"speaker":"Ada","text":"alpha","end":2,"start":1}
  ],
  "source":"footage.mp4"
}`)

	_, firstEnvelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("first decode: diagnostics=%+v err=%v", diagnostics, err)
	}
	_, secondEnvelope, diagnostics, err := DecodeLegacyEDL(reordered)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("reordered decode: diagnostics=%+v err=%v", diagnostics, err)
	}

	idsByText := func(envelope LegacyEnvelope) map[string]ID {
		result := make(map[string]ID, len(envelope.Segments))
		for id, segment := range envelope.Segments {
			result[segment.Text] = id
		}
		return result
	}
	firstIDs := idsByText(firstEnvelope)
	secondIDs := idsByText(secondEnvelope)
	for _, text := range []string{"alpha", "beta"} {
		if firstIDs[text] == "" || firstIDs[text] != secondIDs[text] {
			t.Fatalf("stable ID for %q changed after reorder/canonicalization: %q != %q", text, firstIDs[text], secondIDs[text])
		}
	}
	if firstIDs["alpha"] == firstIDs["beta"] {
		t.Fatalf("different segment content produced the same ID %q", firstIDs["alpha"])
	}
}

func TestLegacyRoundTripPreservesExtensions(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "output":"final.mp4",
  "subtitles":"burn",
  "overlays":[{"image":"logo.png","vendor":{"mode":"screen"}}],
  "owner_extension":{"color":"blue"},
  "schema_version":1,
  "document_id":"doc_explicit",
  "revision":7,
  "timeline":[{
    "id":"clip_intro",
    "start":1.25,
    "end":2.5,
    "text":"hello",
    "speaker":"Ada",
    "properties":{"transform":{"x":12}}
  }]
}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode: diagnostics=%+v err=%v", diagnostics, err)
	}
	if document.ID != "doc_explicit" || document.Revision != 7 {
		t.Fatalf("extensions not imported: id=%q revision=%d", document.ID, document.Revision)
	}
	encoded, diagnostics, err := EncodeLegacyEDL(document, envelope)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("encode: diagnostics=%+v err=%v", diagnostics, err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "output", "subtitles", "overlays", "owner_extension", "schema_version", "document_id", "revision", "timeline"} {
		if _, exists := root[key]; !exists {
			t.Fatalf("round trip dropped top-level field %q: %s", key, encoded)
		}
	}
	var timeline []map[string]json.RawMessage
	if err := json.Unmarshal(root["timeline"], &timeline); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "start", "end", "text", "speaker", "properties"} {
		if _, exists := timeline[0][key]; !exists {
			t.Fatalf("round trip dropped segment field %q: %s", key, encoded)
		}
	}
	if string(timeline[0]["start"]) != "1.25" || string(timeline[0]["end"]) != "2.5" {
		t.Fatalf("unexpected legacy time spelling: start=%s end=%s", timeline[0]["start"], timeline[0]["end"])
	}

	again, _, againDiagnostics, err := DecodeLegacyEDL(encoded)
	if err != nil || HasErrorDiagnostics(againDiagnostics) {
		t.Fatalf("decode round trip: diagnostics=%+v err=%v", againDiagnostics, err)
	}
	wantHash, _ := DocumentHash(document)
	gotHash, _ := DocumentHash(again)
	if gotHash != wantHash {
		t.Fatalf("canonical document changed after round trip: %s != %s\n%s", gotHash, wantHash, encoded)
	}
}

func TestLegacyRoundTripPreservesExplicitEmptyFields(t *testing.T) {
	raw := []byte(`{
  "source":"footage.mp4",
  "output":"",
  "subtitles":"",
  "overlays":[],
  "timeline":[{"start":0,"end":1,"text":"","properties":{},"extension":null}]
}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode: diagnostics=%+v err=%v", diagnostics, err)
	}
	// Exercise the same canonical JSON clone used by planned legacy edits. Empty maps are
	// omitted by the core model, so the envelope must restore the explicit object on export.
	document, diagnostics, err = NormalizeLegacyTimeline(document)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("normalize: diagnostics=%+v err=%v", diagnostics, err)
	}
	encoded, diagnostics, err := EncodeLegacyEDL(document, envelope)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("encode: diagnostics=%+v err=%v", diagnostics, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"output", "subtitles", "overlays"} {
		if _, exists := root[key]; !exists {
			t.Fatalf("explicit empty top-level field %q was dropped: %s", key, encoded)
		}
	}
	var timeline []map[string]json.RawMessage
	if err := json.Unmarshal(root["timeline"], &timeline); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"text", "properties", "extension"} {
		if _, exists := timeline[0][key]; !exists {
			t.Fatalf("explicit empty segment field %q was dropped: %s", key, encoded)
		}
	}
}

func TestDecodeLegacyEDLReportsStrictDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code string
	}{
		{name: "missing source", raw: `{"timeline":[{"start":0,"end":1}]}`, code: CodeLegacySourceRequired},
		{name: "missing timeline", raw: `{"source":"a.mp4"}`, code: CodeLegacyTimelineRequired},
		{name: "null timeline", raw: `{"source":"a.mp4","timeline":null}`, code: CodeLegacyTimelineRequired},
		{name: "object timeline", raw: `{"source":"a.mp4","timeline":{}}`, code: CodeLegacyTimelineRequired},
		{name: "empty timeline", raw: `{"source":"a.mp4","timeline":[]}`, code: CodeLegacyTimelineEmpty},
		{name: "negative start", raw: `{"source":"a.mp4","timeline":[{"start":-1,"end":1}]}`, code: CodeLegacyRangeInvalid},
		{name: "collapses during quantization", raw: `{"source":"a.mp4","timeline":[{"start":1.0000001,"end":1.0000004}]}`, code: CodeLegacyRangeInvalid},
		{name: "string time", raw: `{"source":"a.mp4","timeline":[{"start":"0","end":1}]}`, code: CodeLegacyTimeInvalid},
		{name: "float overflow", raw: `{"source":"a.mp4","timeline":[{"start":0,"end":1e999}]}`, code: CodeLegacyTimeInvalid},
		{name: "bad overlays", raw: `{"source":"a.mp4","overlays":{},"timeline":[{"start":0,"end":1}]}`, code: CodeLegacyFieldInvalid},
		{name: "null overlays", raw: `{"source":"a.mp4","overlays":null,"timeline":[{"start":0,"end":1}]}`, code: CodeLegacyFieldInvalid},
		{name: "null properties", raw: `{"source":"a.mp4","timeline":[{"start":0,"end":1,"properties":null}]}`, code: CodeLegacyFieldInvalid},
		{name: "array properties", raw: `{"source":"a.mp4","timeline":[{"start":0,"end":1,"properties":[]}]}`, code: CodeLegacyFieldInvalid},
		{name: "bad schema", raw: `{"source":"a.mp4","schema_version":2,"timeline":[{"start":0,"end":1}]}`, code: CodeSchemaUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, diagnostics, err := DecodeLegacyEDL([]byte(test.raw))
			if err != nil {
				t.Fatalf("unexpected syntax error: %v", err)
			}
			if !hasDiagnostic(diagnostics, test.code) {
				t.Fatalf("diagnostics=%+v, want %s", diagnostics, test.code)
			}
		})
	}

	if _, _, _, err := DecodeLegacyEDL([]byte(`{"source":`)); err == nil {
		t.Fatal("malformed JSON should return an error")
	}
}

func TestEncodeLegacyEDLRejectsUnrepresentableDocument(t *testing.T) {
	raw := []byte(`{"source":"a.mp4","timeline":[{"start":0,"end":2}]}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("fixture: diagnostics=%+v err=%v", diagnostics, err)
	}

	retimed := document
	retimed.Timelines = append([]Timeline(nil), document.Timelines...)
	retimed.Timelines[0].Tracks = append([]Track(nil), document.Timelines[0].Tracks...)
	retimed.Timelines[0].Tracks[0].Clips = append([]Clip(nil), document.Timelines[0].Tracks[0].Clips...)
	retimed.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration = RationalTime{Value: 1, Rate: 1}
	_, diagnostics, err = EncodeLegacyEDL(retimed, envelope)
	if err != nil || !hasDiagnostic(diagnostics, CodeLegacyRetimeUnsupported) {
		t.Fatalf("retime diagnostics=%+v err=%v", diagnostics, err)
	}

	multiple := document
	multiple.Timelines = append([]Timeline(nil), document.Timelines...)
	multiple.Timelines = append(multiple.Timelines, Timeline{ID: "another"})
	_, diagnostics, err = EncodeLegacyEDL(multiple, envelope)
	if err != nil || !hasDiagnostic(diagnostics, CodeLegacyShapeUnsupported) {
		t.Fatalf("shape diagnostics=%+v err=%v", diagnostics, err)
	}

	for _, rawOverlays := range []string{"null", `{}`} {
		t.Run("overlays "+rawOverlays, func(t *testing.T) {
			invalidEnvelope := envelope
			invalidEnvelope.Overlays = json.RawMessage(rawOverlays)
			_, overlayDiagnostics, overlayErr := EncodeLegacyEDL(document, invalidEnvelope)
			if overlayErr != nil || !hasDiagnostic(overlayDiagnostics, CodeLegacyFieldInvalid) {
				t.Fatalf("overlays diagnostics=%+v err=%v", overlayDiagnostics, overlayErr)
			}
		})
	}
}

func TestLegacyGapRequiresExplicitNormalization(t *testing.T) {
	raw := []byte(`{"source":"a.mp4","timeline":[{"start":0,"end":1},{"start":2,"end":3}]}`)
	document, envelope, diagnostics, err := DecodeLegacyEDL(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("fixture: diagnostics=%+v err=%v", diagnostics, err)
	}
	document.Timelines[0].Tracks[0].Clips[1].TimelineRange.Start = RationalTime{Value: 2, Rate: 1}
	if _, diagnostics, err = EncodeLegacyEDL(document, envelope); err != nil || !hasDiagnostic(diagnostics, CodeLegacyShapeUnsupported) {
		t.Fatalf("gap should be rejected: diagnostics=%+v err=%v", diagnostics, err)
	}

	normalized, diagnostics, err := NormalizeLegacyTimeline(document)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("normalize: diagnostics=%+v err=%v", diagnostics, err)
	}
	encoded, diagnostics, err := EncodeLegacyEDL(normalized, envelope)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("encode normalized: diagnostics=%+v err=%v", diagnostics, err)
	}
	decoded, _, diagnostics, err := DecodeLegacyEDL(encoded)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode normalized: diagnostics=%+v err=%v", diagnostics, err)
	}
	wantHash, _ := DocumentHash(normalized)
	gotHash, _ := DocumentHash(decoded)
	if gotHash != wantHash {
		t.Fatalf("normalized encode/decode changed semantics: %s != %s\n%s", gotHash, wantHash, encoded)
	}
}

func TestLegacyTickRoundsHalfAwayFromZero(t *testing.T) {
	for _, test := range []struct {
		value RationalTime
		want  int64
	}{
		{value: RationalTime{Value: 1, Rate: 2_000_000}, want: 1},
		{value: RationalTime{Value: -1, Rate: 2_000_000}, want: -1},
		{value: RationalTime{Value: 1, Rate: 3_000_000}, want: 0},
	} {
		got, err := legacyTick(test.value)
		if err != nil || got != test.want {
			t.Fatalf("legacyTick(%+v) = %d, %v; want %d", test.value, got, err, test.want)
		}
	}
	if got := formatLegacyTick(-1_250_000); got != "-1.25" {
		t.Fatalf("formatLegacyTick = %q", got)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestLegacyEncodedJSONRemainsReadableByOldShape(t *testing.T) {
	document, envelope, diagnostics, err := DecodeLegacyEDL([]byte(`{"source":"a.mp4","timeline":[{"start":0,"end":1,"text":"x"}]}`))
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("fixture: %+v %v", diagnostics, err)
	}
	encoded, diagnostics, err := EncodeLegacyEDL(document, envelope)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("encode: %+v %v", diagnostics, err)
	}
	var oldShape struct {
		Source   string `json:"source"`
		Timeline []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"timeline"`
	}
	if err := json.Unmarshal(encoded, &oldShape); err != nil {
		t.Fatal(err)
	}
	if oldShape.Source != "a.mp4" || len(oldShape.Timeline) != 1 || oldShape.Timeline[0].End != 1 || strings.TrimSpace(oldShape.Timeline[0].Text) != "x" {
		t.Fatalf("old shape cannot read encoded EDL: %+v\n%s", oldShape, encoded)
	}
}
