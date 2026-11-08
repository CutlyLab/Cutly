package visualcompositor

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func imageDomainScope() evaluator.DomainScope {
	return evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: []documentv2.ID{"image-clip"}}, RequestedRange: tr(0, 4)}
}

func TestDomainCompositorGuardBeforeProviderAndFailureZero(t *testing.T) {
	ctx := context.Background()
	doc := imageDocument(false)
	scope := imageDomainScope()
	p, ds := CompileDomain(ctx, doc, scope, Config{Width: 2, Height: 2, Source: SourceCapabilities{Image: true}})
	if p == nil {
		t.Fatalf("%+v", ds)
	}
	calls := 0
	panicProvider := rastercompositor.NativeSourceProviderFunc(func(context.Context, rastercompositor.NativeSourceRequest) (rastercompositor.NativeSourceFrame, error) {
		calls++
		panic(struct{}{})
	})
	providers := Providers{Native: panicProvider}
	for _, at := range []documentv2.RationalTime{rt(-1, 1), rt(4, 1), rt(8, 1), rt(1, 0)} {
		r, err := p.RenderAt(ctx, "timeline", at, providers)
		if err == nil || !reflect.DeepEqual(r, Result{}) {
			t.Fatalf("at=%v result=%+v err=%v", at, r, err)
		}
	}
	if r, err := p.RenderAt(ctx, "wrong", rt(1, 1), providers); err == nil || !reflect.DeepEqual(r, Result{}) {
		t.Fatal("wrong timeline accepted")
	}
	for _, clock := range []videoedit.FrameClock{{}, {Origin: rt(math.MaxInt64, 1), FramesPerSecond: rt(1, 1)}} {
		r, err := p.RenderFrame(ctx, "timeline", clock, math.MaxInt64, providers)
		if err == nil || !reflect.DeepEqual(r, FrameResult{}) {
			t.Fatalf("overflow=%+v err=%v", r, err)
		}
	}
	if calls != 0 {
		t.Fatal("scope guard called source")
	}
	gap, err := p.RenderAt(ctx, "timeline", rt(3, 1), providers)
	if err != nil || len(gap.Sources) != 0 || calls != 0 {
		t.Fatalf("gap=%+v err=%v calls=%d", gap, err, calls)
	}
	if r, err := p.RenderAt(ctx, "timeline", rt(1, 1), providers); !errors.Is(err, ErrSourceFailed) || !reflect.DeepEqual(r, Result{}) {
		t.Fatalf("panic not zero result=%+v err=%v", r, err)
	}
	if r, err := p.RenderFrame(ctx, "timeline", videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(1, 1)}, 1, providers); !errors.Is(err, ErrSourceFailed) || !reflect.DeepEqual(r, FrameResult{}) {
		t.Fatalf("frame panic result=%+v err=%v", r, err)
	}
	surface := testSurface(t, 2, 2, []cpucompositor.Pixel{{R: 65535, A: 65535}, {}, {}, {}})
	for _, mode := range []string{"scope", "time", "shape", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			provider := nativeProvider(surface, func(f *rastercompositor.NativeSourceFrame) {
				switch mode {
				case "scope":
					f.Request.ClipID = "wrong"
				case "time":
					f.Request.TimelineTime = rt(3, 2)
				case "shape":
					f.Width = 1
				case "cancel":
					cancel()
				}
			})
			r, err := p.RenderAt(callCtx, "timeline", rt(1, 1), Providers{Native: provider})
			if err == nil || !reflect.DeepEqual(r, Result{}) {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			if mode == "cancel" && !errors.Is(err, ErrCanceled) {
				t.Fatal(err)
			}
		})
	}
	// Config, offered capabilities, scope and scope binding never expose retained storage.
	scope.Selection.ClipIDs[0] = "caller-mutation"
	read := p.Scope()
	read.Selection.ClipIDs[0] = "getter-mutation"
	binding := p.ScopeBinding()
	binding.Scope.Selection.ClipIDs[0] = "binding-mutation"
	offered := p.Capabilities()
	offered.TrackKinds[0] = "wrong"
	if p.Scope().Selection.ClipIDs[0] != "image-clip" || p.Capabilities().TrackKinds[0] == "wrong" {
		t.Fatal("owned state escaped")
	}
	otherScope := imageDomainScope()
	otherScope.RequestedRange = tr(0, 5)
	other, ds := CompileDomain(ctx, doc, otherScope, p.Config())
	if other == nil {
		t.Fatal(ds)
	}
	if err := other.ValidateSerializedReceipt(ctx, gap); err == nil {
		t.Fatal("same-hash different-scope gap accepted")
	}
	if p.DocumentHash() != other.DocumentHash() || p.DomainScopeHash() == other.DomainScopeHash() {
		t.Fatal("scope and document identity conflated")
	}
}

func TestDomainCompositorFixedOffersAndEvaluatorScopeChecks(t *testing.T) {
	ctx := context.Background()
	doc := imageDocument(false)
	if p, ds := CompileDomain(ctx, doc, imageDomainScope(), Config{Width: 2, Height: 2}); p != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("capabilities synthesized from doc")
	}
	for _, mode := range []string{"nil-context", "cancel", "wrong-domain", "duplicate", "unselected", "range", "transitions"} {
		t.Run(mode, func(t *testing.T) {
			scope := imageDomainScope()
			doc := imageDocument(false)
			callCtx := ctx
			switch mode {
			case "nil-context":
				callCtx = nil
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				callCtx = c
			case "wrong-domain":
				scope.Domain = evaluator.DomainAudio
			case "duplicate":
				scope.Selection.ClipIDs = append(scope.Selection.ClipIDs, "image-clip")
			case "unselected":
				scope.Selection.ClipIDs = nil
			case "range":
				scope.RequestedRange = tr(3, 1)
			case "transitions":
				scope.Selection.TransitionIDs = []documentv2.ID{"anything"}
			}
			if p, ds := CompileDomain(callCtx, doc, scope, Config{Width: 2, Height: 2, Source: SourceCapabilities{Image: true}}); p != nil || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("scope accepted %+v", ds)
			}
		})
	}
}
