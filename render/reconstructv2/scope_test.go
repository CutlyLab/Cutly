package reconstructv2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func scopeFixture(t *testing.T) (documentv2.Document, renderplanv2.Plan) {
	t.Helper()
	span := documentv2.TimeRange{Start: documentv2.RationalTime{Rate: 1}, Duration: documentv2.RationalTime{Value: 2, Rate: 1}}
	duration := span.Duration
	d := documentv2.Document{SchemaVersion: 2, ID: "scope-document", Assets: []documentv2.Asset{
		{ID: "image", Kind: documentv2.AssetKindImage, ContentHash: "sha256:" + strings.Repeat("a", 64)},
		{ID: "audio", Kind: documentv2.AssetKindAudio, ContentHash: "sha256:" + strings.Repeat("b", 64), Duration: &duration},
	}, Timelines: []documentv2.Timeline{{ID: "timeline", Tracks: []documentv2.Track{
		{ID: "notes", Kind: documentv2.TrackKindAnnotation},
		{ID: "sound", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "sound-clip", AssetID: "audio", TimelineRange: span, SourceRange: span}}},
		{ID: "picture", Kind: documentv2.TrackKindVideo, Clips: []documentv2.Clip{{ID: "disabled", AssetID: "image", Disabled: true, TimelineRange: documentv2.TimeRange{Start: documentv2.RationalTime{Value: -2, Rate: 1}, Duration: span.Duration}, SourceRange: span}, {ID: "picture-clip", AssetID: "image", TimelineRange: span, SourceRange: span}}},
	}}}}
	target := videoedit.RenderTarget{TimelineID: "timeline", Width: 8, Height: 8, FrameClock: videoedit.FrameClock{Origin: documentv2.RationalTime{Value: -1, Rate: 1}, FramesPerSecond: documentv2.RationalTime{Value: 24, Rate: 1}}, SampleClock: videoedit.SampleClock{Origin: documentv2.RationalTime{Value: -1, Rate: 1}, SamplesPerSecond: 48000}, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb"}
	return d, resolveScopePlan(t, d, span, target)
}

func resolveScopePlan(t *testing.T, d documentv2.Document, span documentv2.TimeRange, target videoedit.RenderTarget) renderplanv2.Plan {
	t.Helper()
	p, diagnostics := renderplanv2.Resolve(context.Background(), d, span, target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve: %+v", diagnostics)
	}
	return p
}

func TestSelectScopesOriginalBindingsAndOwnership(t *testing.T) {
	d, p := scopeFixture(t)
	s, err := selectScopes(context.Background(), d, p)
	if err != nil || s.visual.Domain != evaluator.DomainVisual || s.audio.Domain != evaluator.DomainAudio || !reflect.DeepEqual(s.visual.Selection.ClipIDs, []documentv2.ID{"picture-clip"}) || !reflect.DeepEqual(s.audio.Selection.ClipIDs, []documentv2.ID{"sound-clip"}) {
		t.Fatalf("scopes: %+v %v", s, err)
	}
	if p.Visual[0].Binding.TrackIndex != 2 || p.Visual[0].Binding.ClipIndex != 1 || s.audio.RequestedRange != p.RequestedRange {
		t.Fatal("original indexes or range lost")
	}
	s.visual.Selection.ClipIDs[0] = "changed"
	if p.Visual[0].Binding.ClipID != "picture-clip" || d.Timelines[0].Tracks[2].Clips[1].ID != "picture-clip" {
		t.Fatal("scope aliases input")
	}
}

func TestSelectScopesRejectsUnsupportedBeforeNativeCompile(t *testing.T) {
	for _, name := range []string{"missing", "extra", "duplicate", "wrong-domain", "index", "range", "resource-missing", "resource-extra", "resource-duplicate", "resource-hash", "uri-only", "video", "zero-visual", "zero-audio", "timeline", "buses", "transition", "out-of-range-same-asset"} {
		t.Run(name, func(t *testing.T) {
			d, p := scopeFixture(t)
			switch name {
			case "missing":
				p.Visual = nil
			case "extra":
				p.Visual = append(p.Visual, renderplanv2.PlannedClip{Binding: renderplanv2.ClipBinding{ClipID: "extra"}})
			case "duplicate":
				p.Visual = append(p.Visual, p.Visual[0])
			case "wrong-domain":
				p.Audio, p.Visual = p.Visual, p.Audio
			case "index":
				p.Visual[0].Binding.ClipIndex = 0
			case "range":
				p.Visual[0].Binding.SourceRange.Start.Value++
			case "resource-missing":
				p.Resources = p.Resources[:1]
			case "resource-extra":
				p.Resources = append(p.Resources, renderplanv2.ResourceRef{AssetID: "extra"})
			case "resource-duplicate":
				p.Resources[1] = p.Resources[0]
			case "resource-hash":
				p.Resources[0].ContentHash = "different"
			case "uri-only":
				d.Assets[0].ContentHash = ""
				d.Assets[0].URI = "image.png"
				p = resolveScopePlan(t, d, p.RequestedRange, p.Target)
			case "video":
				d.Assets[0].Kind = documentv2.AssetKindVideo
				d.Assets[0].Duration = d.Assets[1].Duration
				p = resolveScopePlan(t, d, p.RequestedRange, p.Target)
			case "zero-visual":
				p.FrameRange.Count = 0
			case "zero-audio":
				p.SampleRange.Count = 0
			case "timeline":
				d.Timelines = append(d.Timelines, documentv2.Timeline{ID: "other"})
			case "buses":
				d.AudioBuses = []documentv2.AudioBus{{}}
			case "transition":
				d.Timelines[0].Transitions = []documentv2.Transition{{}}
			case "out-of-range-same-asset":
				clip := d.Timelines[0].Tracks[2].Clips[1]
				clip.ID = "later"
				clip.TimelineRange.Start.Value = 3
				d.Timelines[0].Tracks[2].Clips = append(d.Timelines[0].Tracks[2].Clips, clip)
				p = resolveScopePlan(t, d, p.RequestedRange, p.Target)
			}
			got, err := selectScopes(context.Background(), d, p)
			if !errors.Is(err, ErrUnsupported) || !reflect.DeepEqual(got, reconstructionScopes{}) {
				t.Fatalf("not failure zero: %+v %v", got, err)
			}
		})
	}
}

func TestSelectScopesIndirectFontAndGap(t *testing.T) {
	// Existing corpus supplies an authored indirect font edge; only this private
	// support gate is exercised here, not native text/vector profile admission.
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-plan-mixed.document.json")
	if err != nil {
		t.Fatal(err)
	}
	var d documentv2.Document
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	_, base := scopeFixture(t)
	d.AudioBuses = nil
	d.Timelines[0].Tracks[1].AudioBusID = ""
	for i := range d.Assets {
		if d.Assets[i].Kind == documentv2.AssetKindImage {
			d.Assets[i].ContentHash = "sha256:" + strings.Repeat("c", 64)
		}
		if d.Assets[i].Text != nil {
			text := d.Assets[i].Text
			text.Profile = documentv2.TextProfilePinnedFontViewportPXV2
			text.Script = documentv2.TextScriptHan
			text.Text = "文本"
			text.LineHeight = &documentv2.Rational{Numerator: 60, Denominator: 1}
			text.Viewport = &documentv2.RasterViewport{WidthPixels: 64, HeightPixels: 64}
		}
	}
	target := base.Target
	target.TimelineID = d.Timelines[0].ID
	span := base.RequestedRange
	span.Duration.Value = 10
	p := resolveScopePlan(t, d, span, target)
	if err = renderplanv2.VerifyAgainstDocument(context.Background(), p, d); err != nil {
		t.Fatal(err)
	}
	if _, err = selectScopes(context.Background(), d, p); err != nil {
		t.Fatal(err)
	}
	for i, r := range p.Resources {
		if r.Kind == documentv2.AssetKindFont {
			p.Resources = append(p.Resources[:i], p.Resources[i+1:]...)
			break
		}
	}
	if _, err = selectScopes(context.Background(), d, p); !errors.Is(err, ErrUnsupported) {
		t.Fatal("missing indirect font accepted")
	}
	gap := documentv2.Document{SchemaVersion: 2, ID: "gap", Timelines: []documentv2.Timeline{{ID: "timeline"}}}
	gp := resolveScopePlan(t, gap, base.RequestedRange, base.Target)
	s, err := selectScopes(context.Background(), gap, gp)
	if err != nil || len(s.visual.Selection.ClipIDs) != 0 || len(s.audio.Selection.ClipIDs) != 0 {
		t.Fatalf("gap: %+v %v", s, err)
	}
}

func TestSelectScopesContextFailureZero(t *testing.T) {
	d, p := scopeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		s, err := selectScopes(c, d, p)
		if err == nil || !reflect.DeepEqual(s, reconstructionScopes{}) {
			t.Fatalf("context: %+v %v", s, err)
		}
	}
}
