package renderplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const planProjectID = " project "

func TestPlanRouteReturnsCanonicalFrozenPlan(t *testing.T) {
	document := planDocument()
	documentBefore, _ := json.Marshal(document)
	var calls atomic.Int32
	resolver := DocumentResolverFunc(func(_ context.Context, projectID string) (videoedit.Document, error) {
		calls.Add(1)
		if projectID != planProjectID {
			t.Fatalf("project ID = %q", projectID)
		}
		return document, nil
	})
	service, err := New(Config{ProjectID: planProjectID, Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.plan(context.Background(), planRequest(t, planProjectID, planRange(), planTarget()))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.OK || outcome.State != "planned" || !outcome.RenderRequired || outcome.Changed || outcome.Replayed ||
		outcome.BaseRevision == nil || *outcome.BaseRevision != document.Revision || len(outcome.Diagnostics) != 0 ||
		len(outcome.NextActions) != 1 || outcome.NextActions[0] != (agentprotocol.NextAction{
		Tool: agentprotocol.ToolRender, Action: "prepare", ProjectID: planProjectID,
	}) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver calls = %d", calls.Load())
	}
	canonical, err := canonicaljson.Canonicalize(outcome.Receipt)
	if err != nil || !bytes.Equal(canonical, outcome.Receipt) {
		t.Fatalf("receipt is not canonical: %v\n%s", err, outcome.Receipt)
	}
	var receipt Receipt
	if err := json.Unmarshal(outcome.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Profile != ReceiptProfile || receipt.Plan.DocumentID != document.ID || receipt.Plan.Fingerprint == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	planRaw, _ := canonicaljson.Marshal(receipt.Plan)
	if verified, err := videoedit.DecodeRenderPlan(planRaw); err != nil || !reflect.DeepEqual(verified, receipt.Plan) {
		t.Fatalf("plan is not frozen: %v", err)
	}
	document.Assets[0].URI = "mutated.wav"
	var after Receipt
	if err := json.Unmarshal(outcome.Receipt, &after); err != nil || after.Plan.Resources[0].ContentHash != testDigest('a') {
		t.Fatalf("receipt retained resolver storage: %+v %v", after, err)
	}
	if bytes.Equal(documentBefore, mustJSON(t, document)) {
		t.Fatal("test did not mutate source document")
	}
}

func TestPlanRouteRejectsProjectBeforePayloadOrResolver(t *testing.T) {
	var calls atomic.Int32
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		calls.Add(1)
		return videoedit.Document{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.plan(context.Background(), agentprotocol.Request{
		ProjectID: "other", Payload: json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPlanRejected(t, outcome, CodeProjectMismatch)
	if calls.Load() != 0 {
		t.Fatal("wrong project reached resolver")
	}
}

func TestPlanRouteStrictPayloadBoundary(t *testing.T) {
	validRange := string(mustCanonical(t, planRange()))
	validTarget := string(mustCanonical(t, planTarget()))
	valid := fmt.Sprintf(`{"range":%s,"target":%s}`, validRange, validTarget)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "not object", raw: `[]`},
		{name: "missing range", raw: fmt.Sprintf(`{"target":%s}`, validTarget)},
		{name: "missing target", raw: fmt.Sprintf(`{"range":%s}`, validRange)},
		{name: "null range", raw: fmt.Sprintf(`{"range":null,"target":%s}`, validTarget)},
		{name: "null target", raw: fmt.Sprintf(`{"range":%s,"target":null}`, validRange)},
		{name: "unknown payload", raw: strings.TrimSuffix(valid, "}") + `,"backend":"agent"}`},
		{name: "duplicate payload", raw: strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,"target":%s}`, validTarget)},
		{name: "unknown range", raw: fmt.Sprintf(`{"range":{"duration":{"rate":1,"value":1},"start":{"rate":1,"value":0},"x":1},"target":%s}`, validTarget)},
		{name: "unknown target", raw: fmt.Sprintf(`{"range":%s,"target":{"channels":2,"color_space":"bt709","frame_clock":{"frames_per_second":{"rate":1,"value":30},"origin":{"rate":1,"value":0}},"height":64,"pixel_format":"yuv420p","sample_clock":{"origin":{"rate":1,"value":0},"samples_per_second":48000},"timeline_id":"timeline","width":64,"x":1}}`, validRange)},
	}
	var calls atomic.Int32
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		calls.Add(1)
		return planDocument(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := service.plan(context.Background(), agentprotocol.Request{ProjectID: planProjectID, Payload: json.RawMessage(test.raw)})
			if err != nil {
				t.Fatal(err)
			}
			assertPlanRejected(t, outcome, CodePayloadInvalid)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid payload reached resolver %d times", calls.Load())
	}
}

func TestPlanRouteReturnsStableResolveDiagnostics(t *testing.T) {
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		return planDocument(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	invalidRange := planRange()
	invalidRange.Duration.Value = 0
	outcome, err := service.plan(context.Background(), planRequest(t, planProjectID, invalidRange, planTarget()))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OK || outcome.State != "rejected" || !videoedit.HasErrorDiagnostics(outcome.Diagnostics) ||
		outcome.Diagnostics[0].Code != videoedit.CodeRenderRangeInvalid || outcome.Receipt != nil || len(outcome.NextActions) != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestPlanRouteMapsNonAgentSafeDiagnosticsBeforeRouterBoundary(t *testing.T) {
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		return planDocument(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	target := planTarget()
	target.TimelineID = videoedit.ID(strings.Repeat("x", agentprotocol.MaxMessageBytes+1))
	result := handlePlanThroughRouter(t, service, planRange(), target)
	if result.OK || result.State != "rejected" || len(result.Diagnostics) != 1 ||
		result.Diagnostics[0].Code != CodePlanRejected || len(result.NextActions) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPlanRouteMapsOversizedReceiptToStableRejection(t *testing.T) {
	document := planDocument()
	document.Timelines[0].Tracks[0].Clips[0].Properties = map[string]json.RawMessage{
		"oversized": json.RawMessage(`"` + strings.Repeat("x", agentprotocol.MaxResultBytes) + `"`),
	}
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		return document, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	result := handlePlanThroughRouter(t, service, planRange(), planTarget())
	if result.OK || result.State != "rejected" || len(result.Diagnostics) != 1 ||
		result.Diagnostics[0].Code != CodePlanRejected || result.Receipt != nil || len(result.NextActions) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPlanRouteRedactsResolverFailuresAndPanics(t *testing.T) {
	for _, test := range []struct {
		name     string
		resolver DocumentResolver
	}{
		{name: "error", resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
			return videoedit.Document{}, errors.New("private /secret/project path")
		})},
		{name: "panic", resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
			panic("private /secret/panic")
		})},
		{name: "invalid document", resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
			document := planDocument()
			document.Assets[0].Metadata = map[string]json.RawMessage{"bad": json.RawMessage(`1.5`)}
			return document, nil
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(Config{ProjectID: planProjectID, Resolver: test.resolver})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := service.plan(context.Background(), planRequest(t, planProjectID, planRange(), planTarget()))
			if err != nil {
				t.Fatal(err)
			}
			assertPlanRejected(t, outcome, CodePlanRejected)
			if strings.Contains(outcome.Diagnostics[0].Message, "private") || strings.Contains(outcome.Diagnostics[0].Message, "/secret") {
				t.Fatalf("private resolver detail leaked: %+v", outcome.Diagnostics)
			}
		})
	}
}

func TestPlanRoutePreservesCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	service, err := New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		t.Fatal("pre-canceled request reached resolver")
		return videoedit.Document{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.plan(canceled, planRequest(t, planProjectID, planRange(), planTarget())); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	service, err = New(Config{ProjectID: planProjectID, Resolver: DocumentResolverFunc(func(context.Context, string) (videoedit.Document, error) {
		cancel()
		return planDocument(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.plan(ctx, planRequest(t, planProjectID, planRange(), planTarget())); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-callback canceled error = %v", err)
	}
}

func TestPlanServiceRejectsTypedNilConfiguration(t *testing.T) {
	var resolver *typedNilResolver
	if _, err := New(Config{ProjectID: planProjectID, Resolver: resolver}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("typed nil error = %v", err)
	}
	if routes := (*Service)(nil).Routes(); routes != nil {
		t.Fatalf("nil routes = %+v", routes)
	}
}

type typedNilResolver struct{}

func (*typedNilResolver) ResolveDocument(context.Context, string) (videoedit.Document, error) {
	return videoedit.Document{}, nil
}

func planRequest(t *testing.T, projectID string, renderRange videoedit.TimeRange, target videoedit.RenderTarget) agentprotocol.Request {
	t.Helper()
	payload, err := canonicaljson.Marshal(map[string]any{"range": renderRange, "target": target})
	if err != nil {
		t.Fatal(err)
	}
	return agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "plan-1",
		Tool: agentprotocol.ToolRender, Action: "plan", ProjectID: projectID, Payload: payload,
	}
}

func handlePlanThroughRouter(
	t *testing.T,
	service *Service,
	renderRange videoedit.TimeRange,
	target videoedit.RenderTarget,
) agentprotocol.Result {
	t.Helper()
	actionRouter, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := actionRouter.HandleAgent(context.Background(), planRequest(t, planProjectID, renderRange, target))
	if err != nil {
		t.Fatalf("router returned a transport error: %v", err)
	}
	return result
}

func planDocument() videoedit.Document {
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	one := videoedit.RationalTime{Value: 1, Rate: 1}
	return videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 3,
		Assets: []videoedit.Asset{{
			ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: testDigest('a'), Duration: &one,
		}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "audio-track", Kind: videoedit.TrackKindAudio,
			Clips: []videoedit.Clip{{
				ID: "audio-clip", AssetID: "audio",
				TimelineRange: videoedit.TimeRange{Start: zero, Duration: one},
				SourceRange:   videoedit.TimeRange{Start: zero, Duration: one},
			}},
		}}}},
	}
}

func planRange() videoedit.TimeRange {
	return videoedit.TimeRange{
		Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 1, Rate: 1},
	}
}

func planTarget() videoedit.RenderTarget {
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	return videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock:  videoedit.FrameClock{Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	}
}

func assertPlanRejected(t *testing.T, outcome router.Outcome, code string) {
	t.Helper()
	if outcome.OK || outcome.State != "rejected" || len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Code != code ||
		outcome.Receipt != nil || len(outcome.NextActions) != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}
