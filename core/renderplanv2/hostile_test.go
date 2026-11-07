package renderplanv2

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

// Corpus entries are complete hostile wire messages, not mutations of legacy
// fixtures. A valid fingerprint is retained on structural attacks so validation
// cannot accidentally be reduced to stale-fingerprint rejection alone.
func TestHostileCorpus(t *testing.T) {
	mixed := resolveOK(t, mixedDoc(t), tr(0, 10))
	transition := resolveOK(t, transitionDoc(), tr(4, 1))
	cases := []struct {
		name   string
		base   Plan
		mutate func(*Plan)
	}{
		{"fingerprint", mixed, func(p *Plan) {
			p.Fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"resource-order", mixed, func(p *Plan) { p.Resources[0], p.Resources[1] = p.Resources[1], p.Resources[0] }},
		{"resource-duplicate", mixed, func(p *Plan) { p.Resources = append(p.Resources[:1], p.Resources...) }},
		{"capability-order", mixed, func(p *Plan) { c := p.RequiredCapabilities.TrackKinds; c[0], c[1] = c[1], c[0] }},
		{"capability-duplicate", mixed, func(p *Plan) {
			p.RequiredCapabilities.Features = append(p.RequiredCapabilities.Features, p.RequiredCapabilities.Features[len(p.RequiredCapabilities.Features)-1])
		}},
		{"font-omission", mixed, func(p *Plan) {
			for i, r := range p.Resources {
				if r.Kind == documentv2.AssetKindFont {
					p.Resources = append(p.Resources[:i], p.Resources[i+1:]...)
					break
				}
			}
		}},
		{"clip-order", mixed, func(p *Plan) { p.Visual[0], p.Visual[1] = p.Visual[1], p.Visual[0] }},
		{"frame-clock", mixed, func(p *Plan) { p.FrameRange.First++ }},
		{"source-projection", mixed, func(p *Plan) { p.Visual[0].VisibleSourceRange.Start.Value++ }},
		{"transition-handle-omission", transition, func(p *Plan) { p.Transitions[0].Incoming.Handles = []HandleSpan{} }},
		{"transition-handle-projection", transition, func(p *Plan) { p.Transitions[0].Incoming.Handles[0].SourceRange.Start.Value++ }},
		{"participant-ordinary-omission", transition, func(p *Plan) { p.Visual = []PlannedClip{} }},
		{"transition-duplicate", transition, func(p *Plan) {
			p.Transitions = append(p.Transitions, p.Transitions[0])
			p.Transitions[1].TransitionIndex++
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := clone(t, tc.base)
			tc.mutate(&p)
			if tc.name != "fingerprint" {
				resign(t, &p)
			}
			raw, e := encode(p)
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join("../../conformance/fixtures", "document-v2-render-plan-"+tc.name+".invalid.json")
			if os.Getenv("CUTLY_UPDATE_RENDERPLANV2_FIXTURES") == "1" {
				if e = os.WriteFile(path, raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			published, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(raw, published) {
				t.Fatal("invalid corpus changed")
			}
			q, e := Decode(published)
			if e == nil || !reflect.DeepEqual(q, Plan{}) {
				t.Fatal("invalid corpus accepted/nonzero")
			}
			if v, e := Verify(p); e == nil || v.Valid() {
				t.Fatal("invalid Go value verified")
			}
		})
	}
}

func TestStrictNestedFieldsAndBounds(t *testing.T) {
	p := resolveOK(t, transitionDoc(), tr(4, 1))
	raw, _ := Canonical(p)
	var root map[string]any
	json.Unmarshal(raw, &root)
	mutations := []func(map[string]any){
		func(m map[string]any) { delete(m["required_capabilities"].(map[string]any), "features") },
		func(m map[string]any) { m["target"].(map[string]any)["width"] = int64(2147483648) },
		func(m map[string]any) {
			m["transitions"].([]any)[0].(map[string]any)["incoming"].(map[string]any)["binding"].(map[string]any)["clip_index"] = uint64(4294967296)
		},
		func(m map[string]any) { m["resources"].([]any)[0].(map[string]any)["uri"] = "machine/path" },
		func(m map[string]any) { delete(m["resources"].([]any)[0].(map[string]any), "content_hash") },
		func(m map[string]any) {
			m["transitions"].([]any)[0].(map[string]any)["incoming"].(map[string]any)["handles"] = nil
		},
	}
	for i, mutate := range mutations {
		var m map[string]any
		json.Unmarshal(raw, &m)
		mutate(m)
		wire, e := canonicaljson.Marshal(m)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = Decode(wire); e == nil {
			t.Fatalf("nested mutation %d accepted", i)
		}
	}
}

type panicContext struct{ context.Context }

func (panicContext) Err() error { panic("hostile context") }
func TestResolvePanicFailureZero(t *testing.T) {
	p, ds := Resolve(panicContext{context.Background()}, gapDoc(), tr(0, 1), target())
	if !reflect.DeepEqual(p, Plan{}) || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("panic must not publish partial plan")
	}
}

func TestStaticTransitionNoTemporalHandles(t *testing.T) {
	d := transitionDoc()
	for i := range d.Assets {
		d.Assets[i].Kind = documentv2.AssetKindImage
		d.Assets[i].URI = "image.png"
	}
	p := resolveOK(t, d, tr(4, 1))
	if len(p.Transitions[0].Incoming.Handles) != 0 || len(p.Transitions[0].Outgoing.Handles) != 0 {
		t.Fatal("static source gained temporal handle")
	}
}
func TestAudioTrackVideoNotImplicitVideoAudio(t *testing.T) {
	d := transitionDoc()
	d.Timelines[0].Transitions = nil
	d.Timelines[0].Tracks = append(d.Timelines[0].Tracks, documentv2.Track{ID: "explicit-audio", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "audio-from-video", AssetID: "out-asset", TimelineRange: tr(0, 5), SourceRange: tr(0, 10)}}})
	p := resolveOK(t, d, tr(0, 1))
	if len(p.Audio) != 1 || p.Audio[0].Binding.AssetKind != documentv2.AssetKindVideo || !contains(p.RequiredCapabilities.Features, videoedit.CapabilityAudioVarispeed) {
		t.Fatal("explicit audio from video")
	}
	d.Timelines[0].Tracks = d.Timelines[0].Tracks[:1]
	if len(resolveOK(t, d, tr(0, 1)).Audio) != 0 {
		t.Fatal("implicit video audio extraction")
	}
}
