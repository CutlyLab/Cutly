package renderplanv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func rt(v, r int64) documentv2.RationalTime { return documentv2.RationalTime{Value: v, Rate: r} }
func tr(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{Start: rt(start, 1), Duration: rt(duration, 1)}
}
func target() videoedit.RenderTarget {
	return videoedit.RenderTarget{Width: 1920, Height: 1080, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb", FrameClock: videoedit.FrameClock{Origin: rt(-1, 1), FramesPerSecond: rt(24, 1)}, SampleClock: videoedit.SampleClock{Origin: rt(-1, 1), SamplesPerSecond: 48000}}
}
func gapDoc() documentv2.Document {
	return documentv2.Document{SchemaVersion: 2, ID: "gap-document", Revision: 1, Timelines: []documentv2.Timeline{{ID: "main"}}}
}
func transitionDoc() documentv2.Document {
	d := gapDoc()
	d.ID = "transition-document"
	d.Assets = []documentv2.Asset{{ID: "out-asset", Kind: documentv2.AssetKindVideo, URI: "media/out.mp4"}, {ID: "in-asset", Kind: documentv2.AssetKindVideo, URI: "media/in.mp4"}}
	d.Timelines[0].Tracks = []documentv2.Track{{ID: "video-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "out", AssetID: "out-asset", TimelineRange: tr(0, 5), SourceRange: tr(2, 5)}, {ID: "in", AssetID: "in-asset", TimelineRange: tr(5, 5), SourceRange: tr(2, 5)}}}}
	d.Timelines[0].Transitions = []documentv2.Transition{{ID: "dissolve", Profile: documentv2.TransitionDissolveV1, OutgoingClipID: "out", IncomingClipID: "in", PreDuration: rt(1, 1), PostDuration: rt(1, 1)}}
	return d
}
func mixedDoc(t *testing.T) documentv2.Document {
	t.Helper()
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	d.ID = "mixed-plan-document"
	d.Timelines[0].Transitions = nil
	d.Assets = append(d.Assets, documentv2.Asset{ID: "image-source", Kind: documentv2.AssetKindImage, URI: "media/still.png"})
	d.Timelines[0].Tracks[0] = documentv2.Track{ID: "image-track", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "image-clip", AssetID: "image-source", TimelineRange: tr(0, 10), SourceRange: tr(0, 10)}}}
	return d
}
func resolveOK(t *testing.T, d documentv2.Document, r documentv2.TimeRange) Plan {
	t.Helper()
	p, ds := Resolve(context.Background(), d, r, target())
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("resolve: %+v", ds)
	}
	return p
}
func resign(t *testing.T, p *Plan) {
	t.Helper()
	f, err := fingerprint(*p)
	if err != nil {
		t.Fatal(err)
	}
	p.Fingerprint = f
}
func clone(t *testing.T, p Plan) Plan {
	t.Helper()
	raw, err := Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestFixtures(t *testing.T) {
	cases := []struct {
		name string
		doc  documentv2.Document
		r    documentv2.TimeRange
	}{{"gap", gapDoc(), tr(0, 1)}, {"mixed", mixedDoc(t), tr(0, 10)}, {"transition", transitionDoc(), documentv2.TimeRange{Start: rt(4, 1), Duration: rt(1, 2)}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := resolveOK(t, tc.doc, tc.r)
			raw, err := Canonical(p)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Join("../../conformance/fixtures", "document-v2-render-plan-"+tc.name)
			if os.Getenv("CUTLY_UPDATE_RENDERPLANV2_FIXTURES") == "1" {
				pretty, _ := json.MarshalIndent(p, "", "  ")
				doc, _ := json.MarshalIndent(tc.doc, "", "  ")
				sum := sha256.Sum256(raw)
				for suffix, content := range map[string][]byte{".canonical.json": raw, ".valid.json": append(pretty, '\n'), ".document.json": append(doc, '\n'), ".sha256": []byte(hex.EncodeToString(sum[:]) + "\n")} {
					if err := os.WriteFile(base+suffix, content, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			want, err := os.ReadFile(base + ".canonical.json")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, want) {
				t.Fatal("canonical fixture changed")
			}
			hash, err := os.ReadFile(base + ".sha256")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			fixed := map[string]string{"gap": "050c4c5071390bfa8633ae92577110b33df76efc67c94cac328fc677a914e668", "mixed": "bfce3b6ed06071d6f3b8319a05770b19fbf49f71ec82b1f1e35dacafd5bdf7d3", "transition": "9defc9c98938f62c723a76953fc60d210c65a8ac66249b71d03465d51094435b"}
			if hex.EncodeToString(sum[:]) != fixed[tc.name] {
				t.Fatal("fixed protocol digest changed")
			}
			if strings.TrimSpace(string(hash)) != hex.EncodeToString(sum[:]) {
				t.Fatal("fixture SHA-256")
			}
			q, err := Decode(want)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAgainstDocument(context.Background(), q, tc.doc); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTransitionHandleOnlyAndBoundaries(t *testing.T) {
	d := transitionDoc()
	p := resolveOK(t, d, documentv2.TimeRange{Start: rt(4, 1), Duration: rt(1, 2)})
	if len(p.Visual) != 1 || p.Visual[0].Binding.ClipID != "out" || len(p.Transitions) != 1 {
		t.Fatal("handle-only participant became ordinary draw")
	}
	in := p.Transitions[0].Incoming
	if in.Binding.ClipID != "in" || len(in.Handles) != 1 || in.Handles[0].Kind != HandleHead || in.Handles[0].SourceRange.Start != rt(1, 1) {
		t.Fatalf("head: %+v", in)
	}
	after := resolveOK(t, d, documentv2.TimeRange{Start: rt(5, 1), Duration: rt(1, 2)})
	if len(after.Visual) != 1 || after.Visual[0].Binding.ClipID != "in" || after.Transitions[0].Outgoing.Handles[0].Kind != HandleTail {
		t.Fatal("tail participant")
	}
	for _, r := range []documentv2.TimeRange{tr(3, 1), tr(6, 1)} {
		if len(resolveOK(t, d, r).Transitions) != 0 {
			t.Fatal("half-open transition boundary")
		}
	}
	d.Timelines[0].Transitions[0].PreDuration = rt(0, 1)
	d.Timelines[0].Transitions[0].PostDuration = rt(0, 1)
	if len(resolveOK(t, d, tr(4, 2)).Transitions) != 0 {
		t.Fatal("hardcut must be inert")
	}
}
func TestMixedClosureAndAgainstDocument(t *testing.T) {
	d := mixedDoc(t)
	p := resolveOK(t, d, tr(0, 10))
	if len(p.Visual) != 3 || len(p.Audio) != 1 {
		t.Fatalf("mixed groups %d/%d", len(p.Visual), len(p.Audio))
	}
	if len(p.Resources) != 3 {
		t.Fatalf("external-only resource closure: %+v", p.Resources)
	}
	if len(p.AudioBusIDs) != 2 {
		t.Fatalf("bus ancestors: %+v", p.AudioBusIDs)
	}
	for _, r := range p.Resources {
		if r.Kind == documentv2.AssetKindImage && r.ContentHash != "" {
			t.Fatal("fabricated hash")
		}
	}
	q := clone(t, p)
	q.Resources = nil
	resign(t, &q)
	if _, err := Verify(q); err == nil {
		t.Fatal("null/missing resources")
	}
	q = clone(t, p)
	for i, r := range q.Resources {
		if r.Kind == documentv2.AssetKindFont {
			q.Resources = append(q.Resources[:i], q.Resources[i+1:]...)
			break
		}
	}
	resign(t, &q)
	if _, err := Verify(q); err == nil {
		t.Fatal("missing indirect font accepted")
	}
	q = clone(t, p)
	q.DocumentHash = "sha256:" + strings.Repeat("a", 64)
	resign(t, &q)
	if _, err := Verify(q); err != nil {
		t.Fatalf("standalone cannot know original document hash: %v", err)
	}
	if err := VerifyAgainstDocument(context.Background(), q, d); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatal("wrong document hash")
	}
	q = clone(t, p)
	q.RequiredCapabilities.Features = append(q.RequiredCapabilities.Features, videoedit.CapabilityID("zzz.unused.v1"))
	resign(t, &q)
	if _, err := Verify(q); err != nil {
		t.Fatal(err)
	}
	if VerifyAgainstDocument(context.Background(), q, d) == nil {
		t.Fatal("extra semantic capability not detected against document")
	}
}
func TestScopedDisabledOutOfRangeAndTimelines(t *testing.T) {
	d := transitionDoc()
	d.Timelines[0].Transitions = nil
	d.Timelines[0].Tracks[0].Clips[1].Disabled = true
	d.Timelines[0].Tracks[0].Clips[1].PropertySet = &documentv2.PropertySet{Opacity: &documentv2.OpacityProperty{Value: documentv2.Rational{Numerator: 1, Denominator: 2}}}
	d.Timelines = append(d.Timelines, documentv2.Timeline{ID: "unrelated", Tracks: []documentv2.Track{{ID: "other-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "other-clip", AssetID: "in-asset", TimelineRange: tr(0, 10), SourceRange: tr(0, 10)}}}}})
	tg := target()
	tg.TimelineID = "main"
	p, ds := Resolve(context.Background(), d, tr(0, 1), tg)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	if len(p.Resources) != 1 || len(p.RequiredCapabilities.Features) != 0 || len(p.Audio) != 0 {
		t.Fatal("unrelated execution requirements leaked")
	}
	if _, ds := Resolve(context.Background(), d, tr(0, 1), target()); !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("ambiguous timeline")
	}
}
func TestStrictCodec(t *testing.T) {
	p := resolveOK(t, gapDoc(), tr(0, 1))
	raw, _ := Canonical(p)
	mutations := map[string][]byte{
		"duplicate":       bytes.Replace(raw, []byte(`"revision":1`), []byte(`"revision":1,"revision":1`), 1),
		"unknown":         bytes.Replace(raw, []byte(`"revision":1`), []byte(`"revision":1,"unknown":0`), 1),
		"alias":           bytes.Replace(raw, []byte(`"revision":1`), []byte(`"Revision":1`), 1),
		"missing":         bytes.Replace(raw, []byte(`"revision":1,`), nil, 1),
		"null":            bytes.Replace(raw, []byte(`"visual":[]`), []byte(`"visual":null`), 1),
		"nested-required": bytes.Replace(raw, []byte(`"first":24`), []byte(`"missing":24`), 1),
		"float":           bytes.Replace(raw, []byte(`"revision":1`), []byte(`"revision":1.0`), 1),
		"utf8":            append([]byte{0xff}, raw...), "space": append([]byte(" "), raw...), "oversize": bytes.Repeat([]byte(" "), MaxPlanBytes+1),
		"surrogate": bytes.Replace(raw, []byte(`"gap-document"`), []byte(`"\ud800"`), 1),
	}
	for name, b := range mutations {
		t.Run(name, func(t *testing.T) {
			q, e := Decode(b)
			if e == nil {
				t.Fatal("accepted hostile JSON")
			}
			if !reflect.DeepEqual(q, Plan{}) {
				t.Fatal("failure not zero")
			}
		})
	}
	for _, path := range []string{"../../conformance/fixtures/render-plan-v3.canonical.json", "../../conformance/fixtures/document-v2.canonical.json"} {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = Decode(b); e == nil {
			t.Fatal("cross-decode")
		}
	}
	if _, err := videoedit.DecodeRenderPlan(raw); err == nil {
		t.Fatal("legacy accepted v2 semantic plan")
	}
}
func TestOwnershipConcurrentAndPreimage(t *testing.T) {
	p := resolveOK(t, mixedDoc(t), tr(0, 10))
	v, err := Verify(p)
	if err != nil {
		t.Fatal(err)
	}
	want := v.Fingerprint()
	p.Resources[0].ContentHash = "changed"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				q, e := v.Plan()
				if e != nil {
					t.Error(e)
					return
				}
				q.Resources[0].ContentHash = "changed"
				b, e := v.Canonical()
				if e != nil {
					t.Error(e)
					return
				}
				b[0] = '!'
				if v.Fingerprint() != want {
					t.Error("getter leaked ownership")
				}
			}
		}()
	}
	wg.Wait()
	if (VerifiedPlan{}).Valid() {
		t.Fatal("zero valid")
	}
	if _, e := (VerifiedPlan{}).Plan(); e == nil {
		t.Fatal("zero getter")
	}
	q, _ := v.Plan()
	raw, _ := Canonical(q)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	delete(fields, "fingerprint")
	pre, _ := canonicaljson.Marshal(fields)
	sum := sha256.Sum256(pre)
	if "sha256:"+hex.EncodeToString(sum[:]) != want {
		t.Fatal("fingerprint member not excluded")
	}
	fields["fingerprint"] = json.RawMessage(`""`)
	pre, _ = canonicaljson.Marshal(fields)
	sum = sha256.Sum256(pre)
	if "sha256:"+hex.EncodeToString(sum[:]) == want {
		t.Fatal("legacy placeholder preimage")
	}
}
func TestClockAuthoredTimeAndDocumentProfile(t *testing.T) {
	d := transitionDoc()
	d.Timelines[0].Transitions = nil
	d.Timelines[0].Tracks[0].Clips[0].TimelineRange = documentv2.TimeRange{Start: rt(0, 24), Duration: rt(120, 24)}
	d.Metadata = map[string]json.RawMessage{"fraction": json.RawMessage(`0.25`)}
	p := resolveOK(t, d, documentv2.TimeRange{Start: rt(1, 1000), Duration: rt(1, 1000)})
	if p.FrameRange.Count != 0 || p.FrameRange.First != 25 || p.SampleRange.Count != 48 {
		t.Fatalf("clock grids: %+v %+v", p.FrameRange, p.SampleRange)
	}
	if p.Visual[0].Binding.TimelineRange.Duration != rt(120, 24) {
		t.Fatal("authored time base lost")
	}
	hash, _ := documentv2.DocumentHash(d)
	if p.DocumentHash != hash {
		t.Fatal("original document profile")
	}
	if _, err := canonicaljson.Marshal(d); err == nil {
		t.Fatal("test must exercise noninteger document metadata")
	}
}
func TestFailuresCancellationLimitsOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		p, ds := Resolve(c, gapDoc(), tr(0, 1), target())
		if !reflect.DeepEqual(p, Plan{}) || !documentv2.HasErrorDiagnostics(ds) {
			t.Fatal("cancel failure not zero")
		}
	}
	d := gapDoc()
	d.Metadata = map[string]json.RawMessage{"large": bytes.Repeat([]byte(" "), MaxStringBytes+1)}
	p, ds := Resolve(context.Background(), d, tr(0, 1), target())
	if !reflect.DeepEqual(p, Plan{}) || ds[0].Code != CodeLimit {
		t.Fatal("must bound before document validation")
	}
	tg := target()
	tg.FrameClock.FramesPerSecond = rt(math.MaxInt64, 1)
	p, ds = Resolve(context.Background(), gapDoc(), tr(1, 2), tg)
	if !reflect.DeepEqual(p, Plan{}) || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("tick overflow")
	}
	if err := VerifyAgainstDocument(ctx, Plan{}, gapDoc()); !errors.Is(err, context.Canceled) {
		t.Fatal("against cancellation")
	}
}
