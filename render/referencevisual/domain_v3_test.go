package referencevisual

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/nativevideo"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

// This test double implements the real host-owned nativevideo.FrameSource contract.
// It does not claim a real FFmpeg decode or semantic-scan acceptance gate.
type temporalSourceV3 struct {
	mu            sync.Mutex
	identity      videodecode.SourceIdentity
	identityCalls int
	calls         []videoedit.RationalTime
	panicIdentity bool
	panicFrame    bool
	cancel        context.CancelFunc
	oversize      bool
}

func newTemporalSourceV3() *temporalSourceV3 {
	hash := "sha256:" + strings.Repeat("1", 64)
	p := nativevideo.Profiles()
	return &temporalSourceV3{identity: videodecode.SourceIdentity{AdapterFingerprint: hash, ProbeFingerprint: hash, IndexFingerprint: hash, SemanticsFingerprint: hash, ResourceHash: hash, Profiles: videodecode.Profiles{Decode: p.DecodeProfile, Selection: p.SelectionProfile, Color: p.ColorProfile, Normalization: p.NormalizationProfile}}}
}
func (s *temporalSourceV3) Identity() (videodecode.SourceIdentity, error) {
	s.mu.Lock()
	s.identityCalls++
	s.mu.Unlock()
	if s.panicIdentity {
		panic("identity")
	}
	return s.identity, nil
}
func (s *temporalSourceV3) Frame(ctx context.Context, at videoedit.RationalTime) (*cpucompositor.Surface, videodecode.FrameReceipt, error) {
	s.mu.Lock()
	s.calls = append(s.calls, at)
	s.mu.Unlock()
	if s.panicFrame {
		panic("frame")
	}
	if s.cancel != nil {
		s.cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, videodecode.FrameReceipt{}, err
	}
	n := 1
	if s.oversize {
		n = 4
	}
	pixels := make([]cpucompositor.Pixel, n)
	for i := range pixels {
		pixels[i] = cpucompositor.Pixel{R: uint16(1000 + at.Value*100/at.Rate), A: 65535}
	}
	surface, err := cpucompositor.NewSurfaceFromPixels(n, 1, pixels)
	if err != nil {
		return nil, videodecode.FrameReceipt{}, err
	}
	hash, _ := surface.CanonicalHash()
	id := s.identity
	return surface, videodecode.FrameReceipt{AdapterFingerprint: id.AdapterFingerprint, ProbeFingerprint: id.ProbeFingerprint, IndexFingerprint: id.IndexFingerprint, SemanticsFingerprint: id.SemanticsFingerprint, ResourceHash: id.ResourceHash, StreamIndex: id.StreamIndex, Width: n, Height: 1, SurfaceHash: hash, Profiles: id.Profiles}, nil
}

func temporalMixedV3(t *testing.T) (documentv2.Document, evaluator.DomainScope, []Binding, []textlayer.FontBinding, []nativevideo.Binding, *temporalSourceV3) {
	doc, scope, pngs, fonts := domainMixedDocument(t)
	source := newTemporalSourceV3()
	duration := rt(20, 1)
	doc.Assets = append(doc.Assets, documentv2.Asset{ID: "video", Kind: documentv2.AssetKindVideo, URI: "host-owned.mp4", ContentHash: source.identity.ResourceHash, Duration: &duration})
	doc.Timelines[0].Tracks = append(doc.Timelines[0].Tracks, documentv2.Track{ID: "video-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{
		{ID: "a", AssetID: "video", TimelineRange: tr(0, 2), SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(4, 1)}},
		{ID: "b", AssetID: "video", TimelineRange: tr(2, 2), SourceRange: documentv2.TimeRange{Start: rt(3, 1), Duration: rt(2, 1)}},
		{ID: "c", AssetID: "video", TimelineRange: tr(4, 2), SourceRange: documentv2.TimeRange{Start: rt(1, 1), Duration: rt(2, 1)}},
	}})
	doc.Timelines[0].Transitions = []documentv2.Transition{
		{ID: "ab", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "a", IncomingClipID: "b", PreDuration: rt(1, 1), PostDuration: rt(1, 1)},
		{ID: "bc", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "b", IncomingClipID: "c", PreDuration: rt(0, 1), PostDuration: rt(0, 1)},
	}
	scope.RequestedRange = tr(0, 6)
	scope.Selection.ClipIDs = append(scope.Selection.ClipIDs, "a", "b", "c")
	scope.Selection.TransitionIDs = []documentv2.ID{"ab", "bc"}
	hash := referenceVisualDocumentHash(t, doc)
	fonts[0].DocumentHash = hash
	return doc, scope, pngs, fonts, []nativevideo.Binding{{DocumentHash: hash, AssetID: "video", Source: source}}, source
}

func TestDomainV3MixedDispatchExactHandlesAndLegacyUnchanged(t *testing.T) {
	ctx := context.Background()
	doc, scope, pngs, fonts, videos, source := temporalMixedV3(t)
	config := DefaultConfigV3(16, 16)
	pre, ds, err := PreflightDomainV3(ctx, doc, scope, config)
	if err != nil {
		t.Fatalf("preflight %v %v", ds, err)
	}
	p, ds, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, config)
	if err != nil {
		t.Fatalf("compile %v %v", ds, err)
	}
	if pre.DocumentHash() != p.DocumentHash() || pre.DomainScopeHash() != p.DomainScopeHash() || p.DocumentHash() != referenceVisualDocumentHash(t, doc) {
		t.Fatal("identity drift")
	}
	if len(source.calls) != 0 {
		t.Fatal("compile decoded frame")
	}
	r, err := p.RenderAt(ctx, "timeline", rt(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile != DomainProfileV3 || r.Visual.Profile != visualcompositor.DomainProfileV2 || r.SourceManifest.SchemaVersion != 3 || len(r.Visual.Sources) != 5 || r.PNG.Artifact == nil {
		t.Fatalf("mixed receipt %+v", r)
	}
	// Audio is track 0, PNG/vector/Text tracks 1/2/3 and video track 4.
	if r.Visual.Sources[0].Native.Request.AssetKind != documentv2.AssetKindImage || r.Visual.Sources[1].Kind != visualcompositor.SourceKindExpression || r.Visual.Sources[2].Kind != visualcompositor.SourceKindText {
		t.Fatal("mixed dispatch")
	}
	outgoing, incoming := r.Visual.Sources[3], r.Visual.Sources[4]
	if outgoing.Evaluation.Transition == nil || incoming.Evaluation.Transition == nil || !outgoing.Evaluation.Transition.Clip.Visible || incoming.Evaluation.Transition.Clip.Visible || outgoing.Native.Request.AssetIndex != 5 || outgoing.Native.Request.TrackIndex != 4 || incoming.Native.Request.ClipIndex != 1 {
		t.Fatal("handle original index/visible drift")
	}
	for i, want := range []videoedit.RationalTime{rt(4, 1), rt(5, 2)} {
		if cmp, e := source.calls[i].Compare(want); e != nil || cmp != 0 {
			t.Fatalf("source-time got %v want %v", source.calls[i], want)
		}
	}
	// At t=1.5 the dissolve is 1/4 complete: 3/4*1400 + 1/4*1250,
	// ties-to-even in the shared RGBA16 kernel, over all static layers.
	pixel, pixelErr := r.Visual.Surface.Pixel(0, 0)
	if pixelErr != nil || pixel.R != 1362 || pixel.A != 65535 {
		t.Fatalf("temporal dissolve pixel=%+v err=%v", pixel, pixelErr)
	}
	late, lateErr := p.RenderSurfaceAt(ctx, "timeline", rt(5, 2))
	if lateErr != nil || len(late.Sources) != 2 || late.Sources[0].Evaluation.Transition.Clip.Visible || !late.Sources[1].Evaluation.Transition.Clip.Visible {
		t.Fatalf("outgoing post-visible handle %+v %v", late, lateErr)
	}
	if cmp, e := late.Sources[0].Native.Request.SourceTime.Compare(rt(6, 1)); e != nil || cmp != 0 {
		t.Fatal("outgoing handle retime clamped to visible source range")
	}
	if err = p.ValidateResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(r)
	var serialized ResultV3
	if err = json.Unmarshal(encoded, &serialized); err != nil {
		t.Fatal(err)
	}
	if err = p.ValidateSerializedReceipt(ctx, serialized); err != nil {
		t.Fatal(err)
	}
	if err = p.ValidateResult(ctx, serialized); err == nil {
		t.Fatal("serialized passed live")
	}
	hard, err := p.RenderAt(ctx, "timeline", rt(4, 1))
	if err != nil || len(hard.Visual.Sources) != 1 || hard.Visual.Sources[0].Evaluation.Transition != nil || hard.Visual.Sources[0].Native.Request.ClipID != "c" {
		t.Fatalf("hardcut %+v %v", hard, err)
	}
	clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: rt(2, 1)}
	frame, err := p.RenderSurfaceFrame(ctx, "timeline", clock, 3)
	if err != nil || frame.Result.PixelHash != r.Visual.PixelHash {
		t.Fatalf("frame %v", err)
	}
	if old, _, err := CompileDomainV2(ctx, doc, scope, pngs, fonts, config.Static); old != nil || err == nil {
		t.Fatal("S3 accepted temporal document")
	}
	if old, _, err := PreflightDomainV2(ctx, doc, scope, config.Static); old != nil || err == nil {
		t.Fatal("S3 preflight accepted temporal document")
	}
	// Old static domain still compiles and keeps frozen profile hashes in existing tests.
	static, ss, sp, sf := domainMixedDocument(t)
	if old, _, err := CompileDomainV2(ctx, static, ss, sp, sf, config.Static); err != nil || old.Profile() != DomainProfileV1 {
		t.Fatal("legacy domain changed")
	}
	// Defensive getters and manifests do not lend mutable storage.
	manifest := p.SourceManifest()
	manifest.Video[0].Identity.ResourceHash = "mutated"
	manifest.DomainScope.Scope.Selection.TransitionIDs[0] = "mutated"
	got := p.Config()
	got.Static.TextConfig.Limits.MaxBindings = 1
	if p.SourceManifest().Video[0].Identity.ResourceHash == "mutated" || p.SourceManifest().DomainScope.Scope.Selection.TransitionIDs[0] == "mutated" || p.Config().Static.TextConfig.Limits.MaxBindings == 1 {
		t.Fatal("ownership escaped")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := rt(int64(i%6), 2)
			a, e := p.RenderAt(ctx, "timeline", at)
			b, e2 := p.RenderAt(ctx, "timeline", at)
			if e != nil || e2 != nil || a.PNG.ArtifactHash != b.PNG.ArtifactHash {
				t.Errorf("concurrent render %v %v", e, e2)
			}
		}(i)
	}
	wg.Wait()
}

func TestDomainV3FailureZeroBudgetsPanicCancelAndReplay(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"frame panic", "cancel", "oversize", "identity panic", "budget", "scope", "missing", "extra", "nil source"} {
		t.Run(mode, func(t *testing.T) {
			doc, scope, pngs, fonts, videos, source := temporalMixedV3(t)
			config := DefaultConfigV3(16, 16)
			switch mode {
			case "identity panic":
				source.panicIdentity = true
			case "oversize":
				config.MaxVideoFrameSurfaceBytes = 8
			case "budget":
				config.Static.CompositorLimits.MaxDocumentNodes = 1
			case "scope":
				scope.Selection.TransitionIDs = nil
			case "missing":
				videos = nil
			case "extra":
				videos = append(videos, videos[0])
			case "nil source":
				var nilSource *temporalSourceV3
				videos[0].Source = nilSource
			}
			p, _, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, config)
			if mode != "frame panic" && mode != "cancel" && mode != "oversize" {
				if err == nil || p != nil {
					t.Fatalf("compile got %v %v", p, err)
				}
				if len(source.calls) != 0 {
					t.Fatal("invalid compile decoded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			runtimeCtx := ctx
			switch mode {
			case "frame panic":
				source.panicFrame = true
			case "cancel":
				var cancel context.CancelFunc
				runtimeCtx, cancel = context.WithCancel(ctx)
				source.cancel = cancel
			case "oversize":
				source.oversize = true
			}
			value, err := p.RenderAt(runtimeCtx, "timeline", rt(3, 2))
			if err == nil || !reflect.DeepEqual(value, ResultV3{}) {
				t.Fatalf("nonzero failure %v %v", value, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel cause %v", err)
			}
		})
	}
	doc, scope, pngs, fonts, videos, _ := temporalMixedV3(t)
	p, _, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, DefaultConfigV3(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []videoedit.RationalTime{rt(-1, 1), rt(6, 1), rt(1, 0)} {
		if r, e := p.RenderAt(ctx, "timeline", at); e == nil || !reflect.DeepEqual(r, ResultV3{}) {
			t.Fatal("scope time escaped")
		}
	}
	r, err := p.RenderAt(ctx, "timeline", rt(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	forged := r
	forged.SourceManifest = p.SourceManifest()
	forged.SourceManifest.Video[0].Identity.IndexFingerprint = "sha256:" + strings.Repeat("2", 64)
	if p.ValidateSerializedReceipt(ctx, forged) == nil {
		t.Fatal("decoder lineage substitution accepted")
	}
	otherScope := scope
	otherScope.RequestedRange = tr(0, 7)
	other, _, err := CompileDomainV3(ctx, doc, otherScope, pngs, fonts, videos, DefaultConfigV3(16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if other.ValidateSerializedReceipt(ctx, r) == nil {
		t.Fatal("cross-scope replay accepted")
	}
}

func TestDomainV3HeterogeneousNativeDissolveAndBoundary(t *testing.T) {
	ctx := context.Background()
	doc, scope, pngs, fonts, videos, _ := temporalMixedV3(t)
	doc.Timelines[0].Tracks[4].Clips[1].AssetID = "image"
	hash := referenceVisualDocumentHash(t, doc)
	fonts[0].DocumentHash, videos[0].DocumentHash = hash, hash
	p, ds, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, DefaultConfigV3(16, 16))
	if err != nil {
		t.Fatalf("heterogeneous compile %v %v", ds, err)
	}
	for _, at := range []videoedit.RationalTime{rt(1, 1), rt(3, 2), rt(5, 2)} {
		r, err := p.RenderAt(ctx, "timeline", at)
		if err != nil {
			t.Fatal(err)
		}
		n := len(r.Visual.Sources)
		if n < 2 || r.Visual.Sources[n-2].Native.Request.AssetKind != documentv2.AssetKindVideo || r.Visual.Sources[n-1].Native.Request.AssetKind != documentv2.AssetKindImage || r.Visual.Sources[n-1].Evaluation.Transition == nil {
			t.Fatal("heterogeneous handles dispatch lost")
		}
	}
	// Dissolve end is exclusive; there is exactly one ordinary PNG clip at t=3.
	r, err := p.RenderAt(ctx, "timeline", rt(3, 1))
	if err != nil || len(r.Visual.Sources) != 1 || r.Visual.Sources[0].Native.Request.AssetKind != documentv2.AssetKindImage || r.Visual.Sources[0].Evaluation.Transition != nil {
		t.Fatalf("dissolve end %+v %v", r, err)
	}
}

func TestDomainV3VideoScopeAdmissionBeforeHostCallbacks(t *testing.T) {
	ctx := context.Background()
	for _, limit := range []int{1, 2, 3} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			doc, scope, pngs, fonts, videos, source := temporalMixedV3(t)
			config := DefaultConfigV3(16, 16)
			config.VideoLimits.MaxClipScopes = limit
			preflight, _, preflightErr := PreflightDomainV3(ctx, doc, scope, config)
			program, _, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, config)
			if limit < 3 {
				if program != nil || preflight != nil || !errors.Is(err, ErrResourceLimit) || !errors.Is(err, nativevideo.ErrResourceLimit) || !errors.Is(preflightErr, ErrResourceLimit) {
					t.Fatalf("scope admission: program=%v preflight=%v err=%v preflightErr=%v", program, preflight, err, preflightErr)
				}
				if source.identityCalls != 0 || len(source.calls) != 0 {
					t.Fatalf("callbacks before admission: identity=%d frame=%d", source.identityCalls, len(source.calls))
				}
				// Admission also precedes pinned-font callbacks: invalid host binding
				// must not mask the nativevideo scope limit with a text bind failure.
				fonts[0].Provider = nil
				if program, _, err := CompileDomainV3(ctx, doc, scope, pngs, fonts, videos, config); program != nil || !errors.Is(err, ErrResourceLimit) {
					t.Fatalf("font binding reached before scope admission: %v", err)
				}
				return
			}
			if program == nil || preflight == nil || err != nil || preflightErr != nil || source.identityCalls != 1 || len(source.calls) != 0 {
				t.Fatalf("exact scope limit rejected: program=%v err=%v preflightErr=%v identities=%d frames=%d", program, err, preflightErr, source.identityCalls, len(source.calls))
			}
		})
	}
}

func TestDomainV3VectorTransitionUsesHandleProvider(t *testing.T) {
	doc := vectorTransitionDocument(1, 1)
	scope := evaluator.DomainScope{Domain: evaluator.DomainVisual, RequestedRange: tr(0, 4), Selection: documentv2.CapabilitySelection{TimelineID: "timeline", ClipIDs: []documentv2.ID{"outgoing", "incoming"}, TransitionIDs: []documentv2.ID{"dissolve"}}}
	p, ds, err := CompileDomainV3(context.Background(), doc, scope, nil, nil, nil, DefaultConfigV3(2, 2))
	if err != nil {
		t.Fatalf("%v %v", ds, err)
	}
	r, err := p.RenderAt(context.Background(), "timeline", rt(3, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Visual.Sources) != 2 || r.Visual.Sources[0].Expression.TransitionRequest == nil || r.Visual.Sources[1].Expression.TransitionRequest == nil {
		t.Fatal("vector handles lost")
	}
}
