package ffmpegbackend

import (
	"context"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestHardCutBackendCapabilitiesRejectDocumentV2RenderingSemantics(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	offered, err := fixture.backend.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	duration := videoedit.RationalTime{Value: 4, Rate: 1}
	document := documentv2.Document{
		SchemaVersion: documentv2.SchemaVersion, ID: "v2-document", Revision: 1,
		Assets: []documentv2.Asset{{
			ID: "video", Kind: documentv2.AssetKindVideo, URI: "video.mp4", Duration: &duration,
		}},
		Timelines: []documentv2.Timeline{{
			ID: "timeline",
			Tracks: []documentv2.Track{{
				ID: "video-track", Kind: documentv2.TrackKindVideo,
				Clips: []documentv2.Clip{{
					ID: "video-clip", AssetID: "video",
					TimelineRange: videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
					SourceRange:   videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
					PropertySet: &documentv2.PropertySet{Opacity: &documentv2.OpacityProperty{
						Value: documentv2.Rational{Numerator: 1, Denominator: 2},
					}},
				}},
			}},
		}},
	}

	_, mappingDiagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(mappingDiagnostics) {
		t.Fatalf("mapping diagnostics: %+v", mappingDiagnostics)
	}
	diagnostics := documentv2.PreflightCapabilities(document, offered)
	if !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatal("ffmpeg hard-cut backend silently accepted v2 opacity")
	}
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == documentv2.CodeCapabilityFeature && strings.Contains(diagnostic.Message, string(documentv2.CapabilityOpacityV1)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing stable opacity capability rejection: %+v", diagnostics)
	}
}
