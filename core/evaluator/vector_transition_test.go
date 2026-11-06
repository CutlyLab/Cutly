package evaluator

import (
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestEvaluateViewportVectorDissolveHandles(t *testing.T) {
	raw, err := os.ReadFile("../documentv2/testdata/vector-transition-document-v2.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	program, diagnostics := Compile(document, videoedit.BackendCapabilities{
		BackendID:  "vector-transition-evaluator-test",
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKind(documentv2.TrackKindVector)},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKind(documentv2.AssetKindVector)},
		Features: []videoedit.CapabilityID{
			documentv2.CapabilityVectorPathViewportPXV2,
			documentv2.CapabilityVectorTransitionScopeV1,
			videoedit.CapabilityID(documentv2.TransitionDissolveV1),
		},
	})
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v diagnostics=%+v", program, diagnostics)
	}
	for _, test := range []struct {
		at              documentv2.RationalTime
		outgoingVisible bool
		incomingVisible bool
	}{
		{at: documentv2.RationalTime{Value: 3, Rate: 2}, outgoingVisible: true, incomingVisible: false},
		{at: documentv2.RationalTime{Value: 5, Rate: 2}, outgoingVisible: false, incomingVisible: true},
	} {
		value, evalErr := program.EvaluateTimeline("timeline", test.at)
		if evalErr != nil || len(value.Layers) != 1 || value.Layers[0].Transition == nil {
			t.Fatalf("at %v: value=%+v err=%v", test.at, value, evalErr)
		}
		transition := value.Layers[0].Transition
		if transition.Phase != documentv2.TransitionPhaseActive || transition.Outgoing == nil || transition.Incoming == nil ||
			transition.Outgoing.Visible != test.outgoingVisible || transition.Incoming.Visible != test.incomingVisible {
			t.Fatalf("at %v: transition=%+v", test.at, transition)
		}
	}
}
