package documentv2

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

func TestVectorViewportDissolveValidationCapabilitiesAndEvaluation(t *testing.T) {
	document := vectorTransitionDocument()
	assertNoDiagnostics(t, Validate(document))
	required, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) ||
		!slices.Equal(required.TrackKinds, []TrackKind{TrackKindVector}) ||
		!slices.Equal(required.AssetKinds, []AssetKind{AssetKindVector}) ||
		!slices.Equal(required.Features, []videoedit.CapabilityID{
			CapabilityVectorTransitionScopeV1, videoedit.CapabilityID(TransitionDissolveV1), CapabilityVectorPathViewportPXV2,
		}) {
		t.Fatalf("requirements=%+v diagnostics=%+v", required, diagnostics)
	}
	offered := videoedit.BackendCapabilities{
		BackendID:  "vector-transition-test",
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKind(TrackKindVector)},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKind(AssetKindVector)},
		Features: []videoedit.CapabilityID{
			CapabilityVectorTransitionScopeV1, videoedit.CapabilityID(TransitionDissolveV1), CapabilityVectorPathViewportPXV2,
		},
	}
	if diagnostics := PreflightCapabilities(document, offered); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("matching vector transition backend rejected: %+v", diagnostics)
	}
	offered.Features = []videoedit.CapabilityID{
		videoedit.CapabilityID(TransitionDissolveV1), CapabilityVectorPathViewportPXV2,
	}
	if diagnostics := PreflightCapabilities(document, offered); !HasErrorDiagnostics(diagnostics) {
		t.Fatalf("backend without scoped vector-transition capability accepted: %+v", diagnostics)
	}

	hardCut := vectorTransitionDocument()
	hardCut.Timelines[0].Transitions[0].PreDuration = RationalTime{Value: 0, Rate: 1}
	hardCut.Timelines[0].Transitions[0].PostDuration = RationalTime{Value: 0, Rate: 1}
	hardCutRequired, hardCutDiagnostics := RequiredCapabilities(hardCut)
	if HasErrorDiagnostics(hardCutDiagnostics) || !slices.Equal(hardCutRequired.Features, []videoedit.CapabilityID{CapabilityVectorPathViewportPXV2}) {
		t.Fatalf("hard cut requirements=%+v diagnostics=%+v", hardCutRequired, hardCutDiagnostics)
	}
}

func TestVectorViewportDissolveRejectsUnsupportedScopes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
	}{
		{"legacy outgoing vector", func(value *Document) {
			value.Assets[0].Vector.Profile = VectorProfilePathV1
			value.Assets[0].Vector.Viewport = nil
		}},
		{"legacy incoming vector", func(value *Document) {
			value.Assets[1].Vector.Profile = VectorProfilePathV1
			value.Assets[1].Vector.Viewport = nil
		}},
		{"disabled incoming", func(value *Document) {
			value.Timelines[0].Tracks[0].Clips[1].Disabled = true
		}},
		{"nonadjacent clips", func(value *Document) {
			middle := value.Timelines[0].Tracks[0].Clips[1]
			middle.ID = "middle"
			middle.TimelineRange = testRange(2, 1)
			value.Timelines[0].Tracks[0].Clips[1].TimelineRange = testRange(3, 2)
			value.Timelines[0].Tracks[0].Clips = append(
				value.Timelines[0].Tracks[0].Clips[:1], middle, value.Timelines[0].Tracks[0].Clips[1],
			)
		}},
		{"mixed track", func(value *Document) {
			value.Timelines[0].Tracks[0].Kind = TrackKindText
		}},
		{"different tracks", func(value *Document) {
			incoming := value.Timelines[0].Tracks[0].Clips[1]
			value.Timelines[0].Tracks[0].Clips = value.Timelines[0].Tracks[0].Clips[:1]
			value.Timelines[0].Tracks = append(value.Timelines[0].Tracks, Track{
				ID: "other-vector-track", Kind: TrackKindVector, Clips: []Clip{incoming},
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := vectorTransitionDocument()
			test.mutate(&document)
			if diagnostics := Validate(document); !HasErrorDiagnostics(diagnostics) {
				t.Fatalf("unsupported transition accepted: %+v", diagnostics)
			}
		})
	}
}

func TestVectorTransitionScopeDescriptorAndDocumentFixtures(t *testing.T) {
	descriptor := VectorTransitionScope()
	if descriptor.RequiredCapability != string(CapabilityVectorTransitionScopeV1) {
		t.Fatalf("required capability=%q", descriptor.RequiredCapability)
	}
	encoded, err := videoedit.CanonicalJSON(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	want := vectorTransitionFixture(t, "vector-transition-scope-v1.canonical.json")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("descriptor drift\nwant %s\n got %s", want, encoded)
	}
	var decoded VectorTransitionScopeDescriptor
	if err := wirejson.Decode(want, &decoded); err != nil || decoded != descriptor {
		t.Fatalf("strict descriptor decode: decoded=%+v err=%v", decoded, err)
	}

	documentEncoded, err := CanonicalJSON(vectorTransitionDocument())
	if err != nil {
		t.Fatal(err)
	}
	documentWant := vectorTransitionFixture(t, "vector-transition-document-v2.canonical.json")
	if !bytes.Equal(documentEncoded, documentWant) {
		t.Fatalf("document drift\nwant %s\n got %s", documentWant, documentEncoded)
	}
	if decodedDocument, err := Decode(documentWant); err != nil {
		t.Fatalf("strict document decode: %v", err)
	} else if roundTrip, roundErr := CanonicalJSON(decodedDocument); roundErr != nil || !bytes.Equal(roundTrip, documentWant) {
		t.Fatalf("document roundtrip drift: %v", roundErr)
	}
}

func TestSemanticDescriptorCatalogV2FixtureAndDefensiveCopies(t *testing.T) {
	v1, err := videoedit.CanonicalJSON(SemanticDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	v1Want := vectorTransitionFixture(t, "semantic-descriptors-v1.canonical.json")
	if !bytes.Equal(v1, v1Want) {
		t.Fatalf("v1 catalog drift\nwant %s\n got %s", v1Want, v1)
	}

	catalog := SemanticDescriptorsV2()
	if catalog.SchemaVersion != DescriptorCatalogVersionV2 || len(catalog.TransitionScopes) != 1 || catalog.TransitionScopes[0] != VectorTransitionScope() {
		t.Fatalf("catalog=%+v", catalog)
	}
	encoded, err := videoedit.CanonicalJSON(catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := vectorTransitionFixture(t, "semantic-descriptors-v2.canonical.json")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("v2 catalog drift\nwant %s\n got %s", want, encoded)
	}
	catalog.TransitionScopes[0].Profile = "mutated"
	if SemanticDescriptorsV2().TransitionScopes[0].Profile != VectorTransitionScopeProfileV1 {
		t.Fatal("transition scope registry aliased caller storage")
	}
}

func vectorTransitionDocument() Document {
	asset := func(id, pathID string, fill ColorRGBA16) Asset {
		point := func(x, y int64) Point {
			return Point{X: Rational{Numerator: x, Denominator: 1}, Y: Rational{Numerator: y, Denominator: 1}}
		}
		return Asset{ID: ID(id), Kind: AssetKindVector, Vector: &VectorSource{
			Profile:  VectorProfilePathViewportPXV2,
			ViewBox:  Rect{X: rationalZero(), Y: rationalZero(), Width: Rational{Numerator: 2, Denominator: 1}, Height: Rational{Numerator: 2, Denominator: 1}},
			Viewport: &RasterViewport{WidthPixels: 2, HeightPixels: 2},
			Paths: []VectorPath{{
				ID: ID(pathID), FillRule: VectorFillNonZero, Fill: &fill,
				Commands: []VectorCommand{
					{Op: VectorMoveTo, Points: []Point{point(0, 0)}},
					{Op: VectorLineTo, Points: []Point{point(2, 0)}},
					{Op: VectorLineTo, Points: []Point{point(2, 2)}},
					{Op: VectorLineTo, Points: []Point{point(0, 2)}},
					{Op: VectorClose},
				},
			}},
		}}
	}
	return Document{
		SchemaVersion: SchemaVersion, ID: "vector-transition-document", Revision: 1,
		Assets: []Asset{
			asset("vector-red", "path-red", ColorRGBA16{R: 65_535, A: 65_535}),
			asset("vector-blue", "path-blue", ColorRGBA16{B: 65_535, A: 65_535}),
		},
		Timelines: []Timeline{{
			ID: "timeline",
			Tracks: []Track{{
				ID: "vector-track", Kind: TrackKindVector,
				Clips: []Clip{
					{ID: "outgoing", AssetID: "vector-red", TimelineRange: testRange(0, 2), SourceRange: testRange(0, 2)},
					{ID: "incoming", AssetID: "vector-blue", TimelineRange: testRange(2, 2), SourceRange: testRange(0, 2)},
				},
			}},
			Transitions: []Transition{{
				ID: "vector-dissolve", Profile: TransitionDissolveV1,
				OutgoingClipID: "outgoing", IncomingClipID: "incoming",
				PreDuration: RationalTime{Value: 1, Rate: 1}, PostDuration: RationalTime{Value: 1, Rate: 1},
			}},
		}},
	}
}

func vectorTransitionFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(raw)
}
