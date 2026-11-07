package documentv2

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestRequiredCapabilitiesForSelectionExcludesUnselectedSemantics(t *testing.T) {
	document := advancedCapabilityDocument()
	// Put all advanced rendering semantics on unselected clips/tracks/buses/assets.
	plain := document.Timelines[0].Tracks[0].Clips[0]
	plain.ID = "plain"
	plain.TimelineRange = capabilityRange(10, 4)
	plain.PropertySet, plain.KeyframeLanes = nil, nil
	document.Timelines[0].Tracks[0].Clips = append(document.Timelines[0].Tracks[0].Clips, plain)
	document.Assets = append(document.Assets, Asset{ID: "unused-data", Kind: AssetKindData, URI: "data.bin"})
	before, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	selection := CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"plain"}}
	got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, selection)
	want := CapabilityRequirements{TrackKinds: []TrackKind{TrackKindVideo}, AssetKinds: []AssetKind{AssetKindVideo}, Features: []videoedit.CapabilityID{}}
	if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements=%+v diagnostics=%+v, want %+v", got, diagnostics, want)
	}
	after, err := json.Marshal(document)
	if err != nil || string(before) != string(after) || !slices.Equal(selection.ClipIDs, []ID{"plain"}) {
		t.Fatalf("inputs mutated: err=%v", err)
	}
	// The additive helper must not change whole-document negotiation.
	whole, diagnostics := RequiredCapabilities(document)
	if HasErrorDiagnostics(diagnostics) || len(whole.Features) == 0 || !slices.Contains(whole.TrackKinds, TrackKindAudio) {
		t.Fatalf("whole-document requirements changed: %+v %+v", whole, diagnostics)
	}
	for _, selection := range []CapabilitySelection{
		{TimelineID: "timeline"},
		{TimelineID: "timeline", ClipIDs: []ID{"plain"}},
	} {
		got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, selection)
		if HasErrorDiagnostics(diagnostics) || slices.Contains(got.Features, CapabilityAudioRouteV1) ||
			slices.Contains(got.Features, CapabilityAudioSamplePeakLimiterV1) || slices.Contains(got.AssetKinds, AssetKindFont) {
			t.Fatalf("unselected dependencies leaked: %+v %+v", got, diagnostics)
		}
	}
}

func TestRequiredCapabilitiesForSelectionPreservesClipSemantics(t *testing.T) {
	for _, test := range []struct {
		name string
		clip ID
	}{
		{"properties keyframes extension", "outgoing"},
		{"video retime", "incoming"},
		{"audio retime matrix and bus", "audio-clip"},
		{"legacy text indirect font", "text-clip"},
		{"legacy vector", "vector-clip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := advancedCapabilityDocument()
			got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, CapabilitySelection{
				TimelineID: "timeline", ClipIDs: []ID{test.clip},
			})
			if HasErrorDiagnostics(diagnostics) {
				t.Fatalf("selection diagnostics: %+v", diagnostics)
			}
			// Compare the established whole-document semantics on a deliberately reduced fixture.
			document.Timelines[0].Transitions = nil
			var selectedTrack Track
			for _, track := range document.Timelines[0].Tracks {
				for _, clip := range track.Clips {
					if clip.ID == test.clip {
						selectedTrack = track
						selectedTrack.Clips = []Clip{clip}
					}
				}
			}
			document.Timelines[0].Tracks = []Track{selectedTrack}
			if selectedTrack.Kind != TrackKindAudio {
				document.AudioBuses = nil
			}
			want, diagnostics := RequiredCapabilities(document)
			if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(got, want) {
				t.Fatalf("scoped=%+v whole reduced=%+v diagnostics=%+v", got, want, diagnostics)
			}
		})
	}
}

func TestRequiredCapabilitiesForSelectionPinnedTextFontAndKeyframe(t *testing.T) {
	document := textV2Document("Cutly", TextScriptLatin, TextDirectionLTR)
	clip := &document.Timelines[0].Tracks[0].Clips[0]
	clip.SourceRange.Duration = capabilityTime(1, 1) // Static text must not request retime.
	clip.KeyframeLanes = []KeyframeLane{{
		ID: "fill", Target: KeyframeTargetTextFill, ValueType: KeyframeValueColor,
		Keyframes: []Keyframe{
			{Offset: capabilityTime(0, 1), Value: KeyframeValue{Color: &ColorRGBA16{A: 65535}}, Interpolation: InterpolationHold},
			{Offset: capabilityTime(4, 1), Value: KeyframeValue{Color: &ColorRGBA16{A: 65535}}},
		},
	}}
	got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, CapabilitySelection{
		TimelineID: "timeline", ClipIDs: []ID{"text-clip"},
	})
	want := CapabilityRequirements{
		TrackKinds: []TrackKind{TrackKindText}, AssetKinds: []AssetKind{AssetKindFont, AssetKindText},
		Features: []videoedit.CapabilityID{CapabilityInterpolationHoldV1, CapabilityKeyframesV1, CapabilityTextPinnedFontViewportPXV2},
	}
	if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements=%+v diagnostics=%+v, want %+v", got, diagnostics, want)
	}
}

func TestRequiredCapabilitiesForSelectionStaticImageDoesNotRetime(t *testing.T) {
	document := baseCapabilityDocument()
	document.Assets[0] = Asset{ID: "video", Kind: AssetKindImage, URI: "still.png"}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = capabilityTime(1, 1)
	got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, CapabilitySelection{
		TimelineID: "timeline", ClipIDs: []ID{"video-clip"},
	})
	if HasErrorDiagnostics(diagnostics) || len(got.Features) != 0 || !slices.Equal(got.AssetKinds, []AssetKind{AssetKindImage}) {
		t.Fatalf("static image requirements=%+v diagnostics=%+v", got, diagnostics)
	}
}

func TestRequiredCapabilitiesForSelectionTransitionsExplicitAndHardCut(t *testing.T) {
	text := textV2Document("Cutly", TextScriptLatin, TextDirectionLTR)
	outgoing := &text.Timelines[0].Tracks[0].Clips[0]
	outgoing.TimelineRange = capabilityRange(0, 2)
	incoming := *outgoing
	incoming.ID, incoming.TimelineRange = "incoming", capabilityRange(2, 2)
	text.Timelines[0].Tracks[0].Clips = append(text.Timelines[0].Tracks[0].Clips, incoming)
	text.Timelines[0].Transitions = []Transition{{
		ID: "text-dissolve", Profile: TransitionDissolveV1, OutgoingClipID: "text-clip", IncomingClipID: "incoming",
		PreDuration: capabilityTime(1, 1), PostDuration: capabilityTime(1, 1),
	}}
	for _, test := range []struct {
		name     string
		document Document
		scope    videoedit.CapabilityID
	}{
		{"video", transitionCapabilityDocument(true), ""},
		{"vector", vectorTransitionDocument(), CapabilityVectorTransitionScopeV1},
		{"text", text, CapabilityTextTransitionScopeV1},
	} {
		t.Run(test.name, func(t *testing.T) {
			transition := test.document.Timelines[0].Transitions[0]
			selection := CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{transition.OutgoingClipID, transition.IncomingClipID}}
			without, diagnostics := RequiredCapabilitiesForSelection(context.Background(), test.document, selection)
			if HasErrorDiagnostics(diagnostics) || slices.Contains(without.Features, videoedit.CapabilityID(TransitionDissolveV1)) ||
				slices.Contains(without.Features, test.scope) {
				t.Fatalf("unselected transition requirements=%+v diagnostics=%+v", without, diagnostics)
			}
			selection.TransitionIDs = []ID{transition.ID}
			with, diagnostics := RequiredCapabilitiesForSelection(context.Background(), test.document, selection)
			if HasErrorDiagnostics(diagnostics) || !slices.Contains(with.Features, videoedit.CapabilityID(TransitionDissolveV1)) ||
				(test.scope != "" && !slices.Contains(with.Features, test.scope)) {
				t.Fatalf("selected transition requirements=%+v diagnostics=%+v", with, diagnostics)
			}
			selection.ClipIDs[0], selection.ClipIDs[1] = selection.ClipIDs[1], selection.ClipIDs[0]
			reordered, diagnostics := RequiredCapabilitiesForSelection(context.Background(), test.document, selection)
			if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(reordered, with) {
				t.Fatalf("order-dependent requirements=%+v original=%+v diagnostics=%+v", reordered, with, diagnostics)
			}
			test.document.Timelines[0].Transitions[0].PreDuration = capabilityTime(0, 1)
			test.document.Timelines[0].Transitions[0].PostDuration = capabilityTime(0, 1)
			hardcut, diagnostics := RequiredCapabilitiesForSelection(context.Background(), test.document, selection)
			if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(hardcut, without) {
				t.Fatalf("hardcut requirements=%+v without=%+v diagnostics=%+v", hardcut, without, diagnostics)
			}
		})
	}
}

func TestRequiredCapabilitiesForSelectionRoutedBusAncestorMasterClosure(t *testing.T) {
	document := advancedCapabilityDocument()
	properties := AudioBusProperties{GainLinear: rationalOne(), Pan: rationalZero()}
	document.AudioBuses = append(document.AudioBuses,
		AudioBus{ID: "group-parent", Kind: AudioBusGroup, ParentBusID: "master", Properties: properties},
		AudioBus{ID: "group-child", Kind: AudioBusGroup, ParentBusID: "group-parent", Properties: properties},
		AudioBus{ID: "unselected-group", Kind: AudioBusGroup, ParentBusID: "master", Properties: properties},
	)
	document.Timelines[0].Tracks[1].AudioBusID = "group-child"
	selection := CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"audio-clip"}}
	got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, selection)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	for _, feature := range []videoedit.CapabilityID{CapabilityAudioRouteV1, CapabilityAudioGainV1, CapabilityAudioPanV1, CapabilityAudioSamplePeakLimiterV1} {
		if !slices.Contains(got.Features, feature) {
			t.Errorf("ancestor/master closure missing %s: %+v", feature, got)
		}
	}
	// Selecting the master directly produces the same semantic requirements; irrelevant groups
	// are not themselves a requirement. The limiter is inherited from the master in both cases.
	document.Timelines[0].Tracks[1].AudioBusID = "master"
	direct, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, selection)
	if HasErrorDiagnostics(diagnostics) || !reflect.DeepEqual(got, direct) {
		t.Fatalf("nested=%+v direct=%+v diagnostics=%+v", got, direct, diagnostics)
	}
	// Legacy implicit master contributes no graph features, even for selected audio.
	document.AudioBuses = nil
	document.Timelines[0].Tracks[1].AudioBusID = ""
	document.Timelines[0].Tracks[1].Clips[0].PropertySet = nil
	legacy, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, selection)
	if HasErrorDiagnostics(diagnostics) || !slices.Equal(legacy.Features, []videoedit.CapabilityID{videoedit.CapabilityAudioVarispeed}) {
		t.Fatalf("legacy implicit-master requirements=%+v diagnostics=%+v", legacy, diagnostics)
	}
}

func TestRequiredCapabilitiesForSelectionRejectsInvalidSelection(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection CapabilitySelection
		mutate    func(*Document)
		path      string
	}{
		{"missing timeline", CapabilitySelection{TimelineID: "missing"}, nil, "/selection/timeline_id"},
		{"empty timeline", CapabilitySelection{}, nil, "/selection/timeline_id"},
		{"missing clip", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"missing"}}, nil, "/selection/clip_ids/0"},
		{"empty clip", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{""}}, nil, "/selection/clip_ids/0"},
		{"duplicate clip", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"outgoing", "outgoing"}}, nil, "/selection/clip_ids/1"},
		{"disabled clip", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"outgoing"}}, func(d *Document) {
			d.Timelines[0].Transitions = nil
			d.Timelines[0].Tracks[0].Clips[0].Disabled = true
		}, "/selection/clip_ids/0"},
		{"too many clips", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"outgoing", "incoming", "missing"}}, nil, "/selection/clip_ids"},
		{"missing transition", CapabilitySelection{TimelineID: "timeline", TransitionIDs: []ID{"missing"}}, nil, "/selection/transition_ids/0"},
		{"empty transition", CapabilitySelection{TimelineID: "timeline", TransitionIDs: []ID{""}}, nil, "/selection/transition_ids/0"},
		{"too many transitions", CapabilitySelection{TimelineID: "timeline", TransitionIDs: []ID{"transition", "transition"}}, nil, "/selection/transition_ids"},
		{"missing outgoing", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"incoming"}, TransitionIDs: []ID{"transition"}}, nil, "/selection/transition_ids/0"},
		{"missing incoming", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"outgoing"}, TransitionIDs: []ID{"transition"}}, nil, "/selection/transition_ids/0"},
		{"foreign clip", CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"foreign-clip"}}, nil, "/selection/clip_ids/0"},
		{"foreign transition", CapabilitySelection{TimelineID: "other", ClipIDs: []ID{"foreign-clip"}, TransitionIDs: []ID{"transition"}}, nil, "/selection/transition_ids/0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := transitionCapabilityDocument(true)
			document.Timelines = append(document.Timelines, Timeline{
				ID: "other", Tracks: []Track{{ID: "foreign-track", Kind: TrackKindVideo, Clips: []Clip{{
					ID: "foreign-clip", AssetID: "video", TimelineRange: capabilityRange(0, 4), SourceRange: capabilityRange(0, 4),
				}, {
					ID: "foreign-incoming", AssetID: "video", TimelineRange: capabilityRange(4, 4), SourceRange: capabilityRange(1, 4),
				}}}}, Transitions: []Transition{{
					ID: "foreign-transition", Profile: TransitionDissolveV1, OutgoingClipID: "foreign-clip", IncomingClipID: "foreign-incoming",
					PreDuration: capabilityTime(1, 1), PostDuration: capabilityTime(1, 1),
				}},
			})
			if test.mutate != nil {
				test.mutate(&document)
			}
			got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, test.selection)
			if got.TrackKinds != nil || got.AssetKinds != nil || got.Features != nil || len(diagnostics) != 1 ||
				diagnostics[0].Code != CodeCapabilitySelectionInvalid || diagnostics[0].Path != test.path {
				t.Fatalf("requirements=%+v diagnostics=%+v, want invalid at %s", got, diagnostics, test.path)
			}
			again, repeat := RequiredCapabilitiesForSelection(context.Background(), document, test.selection)
			if !reflect.DeepEqual(again, got) || !reflect.DeepEqual(repeat, diagnostics) {
				t.Fatalf("unstable failure: %+v %+v", again, repeat)
			}
		})
	}
}

func TestRequiredCapabilitiesForSelectionRejectsDuplicateTransitions(t *testing.T) {
	document := transitionCapabilityDocument(false)
	track := &document.Timelines[0].Tracks[0]
	track.Clips = append(track.Clips, Clip{ID: "third", AssetID: "video", TimelineRange: capabilityRange(8, 4), SourceRange: capabilityRange(0, 4)})
	document.Timelines[0].Transitions = append(document.Timelines[0].Transitions, Transition{
		ID: "second", Profile: TransitionDissolveV1, OutgoingClipID: "incoming", IncomingClipID: "third",
		PreDuration: capabilityTime(0, 1), PostDuration: capabilityTime(0, 1),
	})
	got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, CapabilitySelection{
		TimelineID: "timeline", ClipIDs: []ID{"outgoing", "incoming"}, TransitionIDs: []ID{"transition", "transition"},
	})
	if !reflect.DeepEqual(got, CapabilityRequirements{}) || !capabilityHasDiagnostic(diagnostics, CodeCapabilitySelectionInvalid, "/selection/transition_ids/1") {
		t.Fatalf("duplicate transitions requirements=%+v diagnostics=%+v", got, diagnostics)
	}
}

func TestRequiredCapabilitiesForSelectionValidatesUnselectedDocument(t *testing.T) {
	document := baseCapabilityDocument()
	document.Assets = append(document.Assets, Asset{ID: "unused", Kind: AssetKindFont, URI: "unpinned.ttf"})
	for _, ids := range [][]ID{nil, {"video-clip"}} {
		got, diagnostics := RequiredCapabilitiesForSelection(context.Background(), document, CapabilitySelection{TimelineID: "timeline", ClipIDs: ids})
		if !reflect.DeepEqual(got, CapabilityRequirements{}) || !capabilityHasDiagnostic(diagnostics, CodeAssetInvalid, "/assets/1/content_hash") {
			t.Fatalf("invalid unselected font returned requirements=%+v diagnostics=%+v", got, diagnostics)
		}
	}
}

func TestRequiredCapabilitiesForSelectionCancellationIsStableAndFailureZero(t *testing.T) {
	document := advancedCapabilityDocument()
	selection := CapabilitySelection{TimelineID: "timeline", ClipIDs: []ID{"outgoing", "incoming", "audio-clip", "text-clip", "vector-clip"}, TransitionIDs: []ID{"transition"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := []Diagnostic{capabilityDiagnostic(CodeCapabilitySelectionCanceled, "/selection", "capability selection canceled")}
	contexts := []context.Context{ctx}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	contexts = append(contexts, deadline)
	// A deterministic context cancels at checkpoints both before and after synchronous Validate,
	// during selection construction and during requirement derivation, without timing races.
	measure := &capabilitySelectionCancelContext{Context: context.Background(), remaining: 100_000}
	if _, diagnostics := RequiredCapabilitiesForSelection(measure, document, selection); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("measure cancellation checkpoints: %+v", diagnostics)
	}
	for count := 1; count <= 100_000-measure.remaining; count++ {
		contexts = append(contexts, &capabilitySelectionCancelContext{Context: context.Background(), remaining: count})
	}
	for index, ctx := range contexts {
		got, diagnostics := RequiredCapabilitiesForSelection(ctx, document, selection)
		if !reflect.DeepEqual(got, CapabilityRequirements{}) || !reflect.DeepEqual(diagnostics, want) {
			t.Fatalf("cancellation case %d: requirements=%+v diagnostics=%+v", index, got, diagnostics)
		}
	}
	got, diagnostics := RequiredCapabilitiesForSelection(nil, document, selection)
	if !reflect.DeepEqual(got, CapabilityRequirements{}) || !capabilityHasDiagnostic(diagnostics, CodeCapabilitySelectionInvalid, "/selection") {
		t.Fatalf("nil context requirements=%+v diagnostics=%+v", got, diagnostics)
	}
}

type capabilitySelectionCancelContext struct {
	context.Context
	remaining int
}

func (ctx *capabilitySelectionCancelContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
