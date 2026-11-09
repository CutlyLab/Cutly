package visualcompositor

import (
	"context"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
)

func TestDomainV2TemporalScopeIsAdditive(t *testing.T) {
	document := vectorTransitionDocument()
	scope := evaluator.DomainScope{Domain: evaluator.DomainVisual, RequestedRange: tr(0, 4), Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: []documentv2.ID{"outgoing", "incoming"}, TransitionIDs: []documentv2.ID{"vector-dissolve"}}}
	config := DefaultConfig(2, 2)
	config.Source.VectorExpression = true
	p, ds := CompileDomainV2(context.Background(), document, scope, config)
	if p == nil {
		t.Fatalf("domain V2 diagnostics %v", ds)
	}
	if p.profile() != DomainProfileV2 || p.DocumentHash() == "" || p.Scope().Selection.TransitionIDs[0] != "vector-dissolve" {
		t.Fatal("V2 authority lost")
	}
	if legacy, _ := CompileDomain(context.Background(), document, scope, config); legacy != nil {
		t.Fatal("V1 domain gained transition authority")
	}
	before := DescriptorV2()
	if DomainDescriptorV2().Profile != DomainProfileV2 || !reflect.DeepEqual(before, DescriptorV2()) || DomainDescriptor().Profile != DomainProfileV1 {
		t.Fatal("legacy descriptor changed")
	}
}
