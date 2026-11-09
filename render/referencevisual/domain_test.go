package referencevisual

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdcolor "image/color"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

func domainMixedDocument(t *testing.T) (documentv2.Document, evaluator.DomainScope, []Binding, []textlayer.FontBinding) {
	t.Helper()
	font, fontHash := buildReferenceVisualTextProvider(t, context.Background())
	encoded := strictPNG(t, 2, 2, stdcolor.NRGBA{R: 255, A: 255}, 0)
	doc := mixedDocument(contentHash(encoded))
	text := referenceVisualTextDocument(fontHash, false)
	audio := documentv2.Asset{ID: "audio", Kind: documentv2.AssetKindAudio, URI: "untouched.wav"}
	// Deliberately interleave audio with visual assets and tracks; disabled clips preserve indexes.
	doc.Assets = []documentv2.Asset{audio, doc.Assets[0], text.Assets[0], doc.Assets[1], text.Assets[1]}
	audioTrack := documentv2.Track{ID: "audio-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "audio-clip", AssetID: "audio", TimelineRange: tr(0, 4), SourceRange: tr(0, 4)}}}
	doc.Timelines[0].Tracks = []documentv2.Track{audioTrack, doc.Timelines[0].Tracks[0], doc.Timelines[0].Tracks[1], text.Timelines[0].Tracks[0]}
	disabled := doc.Timelines[0].Tracks[1].Clips[0]
	disabled.ID, disabled.Disabled = "disabled-image", true
	disabled.TimelineRange, disabled.SourceRange = tr(0, 1), tr(0, 1)
	doc.Timelines[0].Tracks[1].Clips[0].TimelineRange = tr(1, 1)
	doc.Timelines[0].Tracks[1].Clips[0].SourceRange = tr(0, 1)
	doc.Timelines[0].Tracks[1].Clips = append([]documentv2.Clip{disabled}, doc.Timelines[0].Tracks[1].Clips...)
	scope := evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: []documentv2.ID{"vector-clip", "text-clip", "image-clip"}}, RequestedRange: tr(0, 4)}
	hash := referenceVisualDocumentHash(t, doc)
	return doc, scope, []Binding{{AssetID: "image", EncodedPNG: encoded}}, []textlayer.FontBinding{{DocumentHash: hash, FontAssetID: "font", Provider: font}}
}

func TestDomainV2MixedOriginalIdentityAndProviderLineage(t *testing.T) {
	ctx := context.Background()
	doc, scope, pngs, fonts := domainMixedDocument(t)
	original, _ := documentv2.CanonicalJSON(doc)
	hash := referenceVisualDocumentHash(t, doc)
	config := DefaultConfigV2(16, 16)
	if legacy, ds, err := CompileV2(ctx, doc, pngs, fonts, config); legacy != nil || err == nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("legacy accepted mixed doc: %v %v", ds, err)
	}
	preflight, ds, err := PreflightDomainV2(ctx, doc, scope, config)
	if err != nil || preflight == nil {
		t.Fatalf("preflight %v %v", ds, err)
	}
	program, ds, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, config)
	if err != nil || program == nil {
		t.Fatalf("compile %v %v", ds, err)
	}
	if program.DocumentHash() != hash || preflight.DocumentHash() != hash || program.DomainScopeHash() != preflight.DomainScopeHash() || program.visual.Config() != preflight.Config() {
		t.Fatal("full document or preflight identity drift")
	}
	after, _ := documentv2.CanonicalJSON(doc)
	if !bytes.Equal(original, after) {
		t.Fatal("caller document changed")
	}
	result, err := program.RenderAt(ctx, "timeline", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != DomainProfileV1 || result.Visual.Profile != visualcompositor.DomainProfileV1 || result.SourceManifest.Profile != DomainManifestProfileV1 || result.Visual.DomainScopeHash != program.DomainScopeHash() {
		t.Fatal("domain profile drift")
	}
	if len(result.Visual.Sources) != 3 {
		t.Fatalf("sources=%+v", result.Visual.Sources)
	}
	native, vector, text := result.Visual.Sources[0], result.Visual.Sources[1], result.Visual.Sources[2]
	if native.Kind != visualcompositor.SourceKindNative || vector.Kind != visualcompositor.SourceKindExpression || text.Kind != visualcompositor.SourceKindText ||
		native.Native.Request.AssetIndex != 1 || native.Native.Request.TrackIndex != 1 || native.Native.Request.ClipIndex != 1 ||
		vector.Expression.VisibleRequest.AssetIndex != 3 || vector.Expression.VisibleRequest.TrackIndex != 2 ||
		text.Text.Request.AssetIndex != 4 || text.Text.Request.FontAssetIndex != 2 || text.Text.Request.TrackIndex != 3 || text.Text.Assembly.ShapeProviderProfile == "" {
		t.Fatalf("original-index lineage lost: %+v", result.Visual.Sources)
	}
	if err := program.ValidateResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte(`"domain_scope"`), []byte(`"requested_range"`), []byte(`"clip_ids"`), []byte(`"timeline_id"`)} {
		if !bytes.Contains(wire, want) {
			t.Fatalf("missing lowercase key %s", want)
		}
	}
	for _, bad := range [][]byte{[]byte(`"Selection"`), []byte(`"ClipIDs"`), []byte(`"RequestedRange"`)} {
		if bytes.Contains(wire, bad) {
			t.Fatalf("Go field casing leaked %s", bad)
		}
	}
	var serialized Result
	if err := json.Unmarshal(wire, &serialized); err != nil {
		t.Fatal(err)
	}
	if err := program.ValidateSerializedReceipt(ctx, serialized); err != nil {
		t.Fatal(err)
	}
	// Scope changes identity without changing the full original document hash, even for gaps.
	otherScope := scope
	otherScope.RequestedRange = tr(0, 5)
	other, _, err := CompileDomainV2(ctx, doc, otherScope, pngs, fonts, config)
	if err != nil {
		t.Fatal(err)
	}
	if other.DocumentHash() != hash || other.DomainScopeHash() == program.DomainScopeHash() {
		t.Fatal("scope identity not separate from document")
	}
	if err := other.ValidateResult(ctx, result); err == nil {
		t.Fatal("cross-domain result accepted")
	}
	if err := other.ValidateSerializedReceipt(ctx, serialized); err == nil {
		t.Fatal("cross-domain serialized result accepted")
	}
	gap, err := program.RenderAt(ctx, "timeline", rt(3, 1))
	if err != nil || len(gap.Visual.Sources) != 0 {
		t.Fatalf("gap=%+v err=%v", gap, err)
	}
	if err := other.visual.ValidateResult(ctx, gap.Visual); err == nil {
		t.Fatal("cross-domain gap accepted")
	}
	// Other-domain authored metadata is not pruned out of DocumentHash.
	changed := doc
	changed.Assets = append([]documentv2.Asset(nil), doc.Assets...)
	changed.Assets[0].URI = "different-audio.wav"
	changedPreflight, _, err := PreflightDomainV2(ctx, changed, scope, config)
	if err != nil || changedPreflight.DocumentHash() == hash {
		t.Fatalf("audio did not affect full identity: %v", err)
	}
	if hash != "sha256:23dfd751477da18169e32333ebe656a0f1570db16a6a62120f50dc97a2623cc4" ||
		program.DomainScopeHash() != "sha256:d04cba432414eb4c5a4aac2888f07727a6f8239a44bb654b8f954ae609410bf0" ||
		result.PNG.ArtifactHash != "sha256:94c632f246afbbcc8be2c767fe359515473872f8697702bc43d4eb911d584231" {
		t.Fatalf("domain fixed identity drift: document=%s scope=%s PNG=%s", hash, program.DomainScopeHash(), result.PNG.ArtifactHash)
	}
}

func TestDomainV2OwnershipRuntimeBoundsAndConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	doc, scope, pngs, fonts := domainMixedDocument(t)
	config := DefaultConfigV2(16, 16)
	p, _, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, config)
	if err != nil {
		t.Fatal(err)
	}
	scope.Selection.ClipIDs[0] = "mutated"
	config.TextConfig.Limits.MaxBindings = 1
	pngs[0].EncodedPNG[0] = 0
	fonts[0].Provider = nil
	doc.Assets[1].ContentHash = "mutated"
	got := p.Scope()
	got.Selection.ClipIDs[0] = "escape"
	gotConfig := p.Config()
	gotConfig.TextConfig.Limits.MaxBindings = 1
	manifest := p.SourceManifest()
	manifest.DomainScope.Scope.Selection.ClipIDs[0] = "escape"
	manifest.Text.Assets[0].Assembly.ShapeProviderProfile = "escape"
	if p.Scope().Selection.ClipIDs[0] != "image-clip" || p.Config().TextConfig.Limits.MaxBindings == 1 || p.SourceManifest().DomainScope.Scope.Selection.ClipIDs[0] != "image-clip" || p.SourceManifest().Text.Assets[0].Assembly.ShapeProviderProfile == "escape" {
		t.Fatal("retained pointer/slice header escaped")
	}
	first, err := p.RenderAt(ctx, "timeline", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	first.SourceManifest.DomainScope.Scope.Selection.ClipIDs[0] = "escape"
	for _, tc := range []struct {
		id documentv2.ID
		at documentv2.RationalTime
	}{{"wrong", rt(1, 1)}, {"timeline", rt(-1, 1)}, {"timeline", rt(4, 1)}, {"timeline", rt(1, 0)}} {
		got, err := p.RenderAt(ctx, tc.id, tc.at)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("bounds id=%s at=%v result=%+v err=%v", tc.id, tc.at, got, err)
		}
	}
	for _, clock := range []videoedit.FrameClock{{}, {Origin: videoedit.RationalTime{Value: math.MaxInt64, Rate: 1}, FramesPerSecond: videoedit.RationalTime{Value: 1, Rate: 1}}} {
		got, err := p.RenderFrame(ctx, "timeline", clock, math.MaxInt64)
		if err == nil || !reflect.DeepEqual(got, FrameResult{}) {
			t.Fatalf("clock result=%+v err=%v", got, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := p.RenderAt(canceled, "timeline", rt(1, 1)); !errors.Is(err, ErrCanceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancel result=%+v err=%v", got, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				r, err := p.RenderAt(ctx, "timeline", rt(int64(j), 1))
				if err != nil {
					t.Error(err)
					return
				}
				if err := p.ValidateSerializedReceipt(ctx, r); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestDomainV2RejectsBeforeResourcesAndSharesAggregatePreflight(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"same-asset-outside", "extra-visual", "extra-timeline", "audio-domain", "bad-scope", "transition", "static-text"} {
		t.Run(mode, func(t *testing.T) {
			doc, scope, pngs, fonts := domainMixedDocument(t)
			switch mode {
			case "same-asset-outside", "extra-visual":
				clip := doc.Timelines[0].Tracks[1].Clips[1]
				clip.ID = "extra"
				clip.TimelineRange = tr(6, 2)
				if mode == "extra-visual" {
					clip.TimelineRange = tr(2, 2)
				}
				doc.Timelines[0].Tracks[1].Clips = append(doc.Timelines[0].Tracks[1].Clips, clip)
			case "extra-timeline":
				doc.Timelines = append(doc.Timelines, documentv2.Timeline{ID: "other"})
			case "audio-domain":
				scope.Domain = evaluator.DomainAudio
			case "bad-scope":
				scope.Selection.ClipIDs = append(scope.Selection.ClipIDs, "audio-clip")
			case "transition":
				clip := doc.Timelines[0].Tracks[1].Clips[1]
				clip.ID = "incoming"
				clip.TimelineRange = tr(2, 2)
				doc.Timelines[0].Tracks[1].Clips = append(doc.Timelines[0].Tracks[1].Clips, clip)
				scope.Selection.ClipIDs = append(scope.Selection.ClipIDs, clip.ID)
				doc.Timelines[0].Transitions = []documentv2.Transition{{ID: "hard-cut", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "image-clip", IncomingClipID: "incoming", PreDuration: rt(0, 1), PostDuration: rt(0, 1)}}
			case "static-text":
				fill := documentv2.ColorRGBA16{R: 65535, A: 65535}
				doc.Timelines[0].Tracks[3].Clips[0].KeyframeLanes = []documentv2.KeyframeLane{{ID: "fill", Target: documentv2.KeyframeTargetTextFill, ValueType: documentv2.KeyframeValueColor, Keyframes: []documentv2.Keyframe{{Offset: rt(0, 1), Value: documentv2.KeyframeValue{Color: &fill}}}}}
			}
			// If any source compiler runs, these deliberately broken bindings would classify a source error.
			pngs[0].EncodedPNG = []byte("not png")
			fonts[0].Provider = nil
			p, ds, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, DefaultConfigV2(16, 16))
			if p != nil || !errors.Is(err, ErrDocumentInvalid) || !documentv2.HasErrorDiagnostics(ds) {
				t.Fatalf("did not fail scope before source work: %v %v", ds, err)
			}
			q, qs, qe := PreflightDomainV2(ctx, doc, scope, DefaultConfigV2(16, 16))
			if q != nil || !errors.Is(qe, ErrDocumentInvalid) || !documentv2.HasErrorDiagnostics(qs) {
				t.Fatalf("preflight drift %v %v", qs, qe)
			}
		})
	}
	doc, scope, pngs, fonts := domainMixedDocument(t)
	bad := DefaultConfigV2(16, 16)
	bad.PipelineLimits.MaxCompileWorkingBytes = 1
	p, _, err := PreflightDomainV2(ctx, doc, scope, bad)
	if p != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("preflight aggregate limits: %v", err)
	}
	q, _, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, bad)
	if q != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("compile aggregate limits: %v", err)
	}
}

type domainPanicContext struct{ context.Context }

func (domainPanicContext) Err() error { panic(struct{}{}) }

func TestDomainV2PanicBeforeRuntimeGuardPublishesZero(t *testing.T) {
	doc, scope, pngs, fonts := domainMixedDocument(t)
	p, _, err := CompileDomainV2(context.Background(), doc, scope, pngs, fonts, DefaultConfigV2(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	ctx := domainPanicContext{context.Background()}
	if r, err := p.RenderAt(ctx, "timeline", rt(1, 1)); err == nil || !reflect.DeepEqual(r, Result{}) {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if r, err := p.RenderSurfaceAt(ctx, "timeline", rt(1, 1)); err == nil || !reflect.DeepEqual(r, visualcompositor.Result{}) {
		t.Fatalf("surface=%+v err=%v", r, err)
	}
	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(1, 1)}
	if r, err := p.RenderFrame(ctx, "timeline", clock, 1); err == nil || !reflect.DeepEqual(r, FrameResult{}) {
		t.Fatalf("frame=%+v err=%v", r, err)
	}
	if r, err := p.RenderSurfaceFrame(ctx, "timeline", clock, 1); err == nil || !reflect.DeepEqual(r, SurfaceFrameResult{}) {
		t.Fatalf("surface frame=%+v err=%v", r, err)
	}
	if err := p.ValidateResult(ctx, Result{}); err == nil {
		t.Fatal("validation panic escaped")
	}
	if err := p.ValidateSerializedReceipt(ctx, Result{}); err == nil {
		t.Fatal("serialized panic escaped")
	}
	if p, _, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, DefaultConfigV2(16, 16)); p != nil || err == nil {
		t.Fatal("compile panic published program")
	}
	if p, _, err := PreflightDomainV2(ctx, doc, scope, DefaultConfigV2(16, 16)); p != nil || err == nil {
		t.Fatal("preflight panic published program")
	}
}

func TestDomainV2EmptyVisualClosurePositiveGap(t *testing.T) {
	doc, _, _, _ := domainMixedDocument(t)
	doc.Timelines[0].Tracks = doc.Timelines[0].Tracks[:1]
	scope := evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: "timeline"}, RequestedRange: tr(2, 2)}
	p, ds, err := CompileDomainV2(context.Background(), doc, scope, nil, nil, DefaultConfigV2(2, 2))
	if err != nil {
		t.Fatalf("%v %v", ds, err)
	}
	r, err := p.RenderAt(context.Background(), "timeline", rt(3, 1))
	if err != nil || len(r.Visual.Sources) != 0 || r.Visual.Surface == nil {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := p.RenderAt(context.Background(), "timeline", rt(4, 1)); err == nil {
		t.Fatal("empty closure escaped range")
	}
}
