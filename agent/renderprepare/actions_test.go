package renderprepare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CutlyLab/Cutly/agent/ffmpeghost"
	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const prepareProjectID = " project "

func TestPrepareRouteReturnsCanonicalTypedFrozenReceipt(t *testing.T) {
	plan := routeRenderPlan(t)
	seed := int64(91)
	var calls atomic.Int32
	var captured ffmpeghost.PrepareRequest
	preparer := PreparerFunc(func(_ context.Context, projectID string, request ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		calls.Add(1)
		if projectID != prepareProjectID {
			t.Fatalf("projectID = %q", projectID)
		}
		captured = request
		return routeManifest(t, request.Plan, request.Resources[0].Locator, request.DeterministicSeed), nil
	})
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: preparer})
	if err != nil {
		t.Fatal(err)
	}
	agentRouter, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agentRouter.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "prepare-1",
		Tool: agentprotocol.ToolRender, Action: "prepare", ProjectID: prepareProjectID,
		Payload: routePayload(t, plan, []resourceBinding{{AssetID: "audio", Locator: "/allowed/audio.wav"}}, &seed),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.State != "prepared" || !result.RenderRequired || result.Changed || result.Replayed ||
		len(result.Diagnostics) != 0 || len(result.NextActions) != 1 ||
		result.NextActions[0] != (agentprotocol.NextAction{Tool: agentprotocol.ToolRender, Action: "preflight", ProjectID: prepareProjectID}) {
		t.Fatalf("result = %+v", result)
	}
	if calls.Load() != 1 || captured.DeterministicSeed != seed || captured.Plan.Fingerprint != plan.Fingerprint ||
		len(captured.Resources) != 1 || captured.Resources[0].AssetID != "audio" || captured.Resources[0].Locator != "/allowed/audio.wav" {
		t.Fatalf("preparer request = %+v calls=%d", captured, calls.Load())
	}
	canonical, err := canonicaljson.Canonicalize(result.Receipt)
	if err != nil || !bytes.Equal(canonical, result.Receipt) {
		t.Fatalf("receipt is not canonical: %v\n%s", err, result.Receipt)
	}
	var receipt Receipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Profile != ReceiptProfile || receipt.ExecutionManifest.PlanFingerprint != plan.Fingerprint ||
		receipt.ExecutionManifest.DeterministicSeed != seed {
		t.Fatalf("receipt = %+v", receipt)
	}
	manifestRaw, _ := json.Marshal(receipt.ExecutionManifest)
	if _, err := renderapi.DecodeExecutionManifest(manifestRaw); err != nil {
		t.Fatalf("receipt manifest is not frozen: %v", err)
	}
}

func TestPrepareRouteEnforcesExactProjectBeforePayloadOrHost(t *testing.T) {
	var calls atomic.Int32
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		calls.Add(1)
		return renderapi.ExecutionManifest{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := executeRoute(service, context.Background(), "other", json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, outcome, CodeProjectMismatch)
	if calls.Load() != 0 {
		t.Fatal("wrong project reached preparer")
	}
}

func TestPrepareRouteStrictPayloadBoundary(t *testing.T) {
	plan := routeRenderPlan(t)
	planRaw, err := canonicaljson.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	valid := fmt.Sprintf(`{"plan":%s,"resources":[]}`, planRaw)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "not object", raw: `[]`},
		{name: "missing plan", raw: `{"resources":[]}`},
		{name: "missing resources", raw: fmt.Sprintf(`{"plan":%s}`, planRaw)},
		{name: "null resources", raw: fmt.Sprintf(`{"plan":%s,"resources":null}`, planRaw)},
		{name: "unknown field", raw: strings.TrimSuffix(valid, "}") + `,"backend":"agent"}`},
		{name: "duplicate member", raw: strings.TrimSuffix(valid, "}") + `,"resources":[]}`},
		{name: "fractional seed", raw: strings.TrimSuffix(valid, "}") + `,"deterministic_seed":1.5}`},
		{name: "null seed", raw: strings.TrimSuffix(valid, "}") + `,"deterministic_seed":null}`},
		{name: "resource unknown field", raw: fmt.Sprintf(`{"plan":%s,"resources":[{"asset_id":"audio","locator":"/allowed/audio.wav","hash":"agent"}]}`, planRaw)},
	}
	var calls atomic.Int32
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		calls.Add(1)
		return renderapi.ExecutionManifest{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := executeRoute(service, context.Background(), prepareProjectID, json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			assertRejected(t, outcome, CodePayloadInvalid)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid payloads reached preparer %d times", calls.Load())
	}
}

func TestPrepareRouteRejectsOversizedResourceList(t *testing.T) {
	plan := routeRenderPlan(t)
	resources := make([]resourceBinding, agentprotocol.MaxListItems+1)
	for index := range resources {
		resources[index] = resourceBinding{AssetID: videoedit.ID(fmt.Sprintf("asset-%d", index)), Locator: "/allowed/audio.wav"}
	}
	var calls atomic.Int32
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		calls.Add(1)
		return renderapi.ExecutionManifest{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := executeRoute(service, context.Background(), prepareProjectID, routePayload(t, plan, resources, nil))
	if err != nil {
		t.Fatal(err)
	}
	assertRejected(t, outcome, CodePayloadInvalid)
	if calls.Load() != 0 {
		t.Fatal("oversized resource list reached preparer")
	}
}

func TestPrepareRouteContainsAndSanitizesHostFailure(t *testing.T) {
	tests := []struct {
		name     string
		preparer Preparer
	}{
		{name: "error", preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
			return renderapi.ExecutionManifest{}, errors.New("secret host path /private/input.mov")
		})},
		{name: "panic", preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
			panic("secret panic /private/input.mov")
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(Config{ProjectID: prepareProjectID, Preparer: test.preparer})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := executeRoute(service, context.Background(), prepareProjectID, routePayload(t, routeRenderPlan(t), []resourceBinding{{AssetID: "audio", Locator: "/allowed/audio.wav"}}, nil))
			if err != nil {
				t.Fatal(err)
			}
			assertRejected(t, outcome, CodePrepareRejected)
			message := outcome.Diagnostics[0].Message
			if strings.Contains(message, "secret") || strings.Contains(message, "/private") || strings.Contains(message, "panic") {
				t.Fatalf("host detail leaked: %q", message)
			}
		})
	}
}

func TestPrepareRouteRejectsInvalidOrMismatchedHostManifest(t *testing.T) {
	plan := routeRenderPlan(t)
	tests := []struct {
		name     string
		manifest func() renderapi.ExecutionManifest
	}{
		{name: "zero", manifest: func() renderapi.ExecutionManifest { return renderapi.ExecutionManifest{} }},
		{name: "tampered", manifest: func() renderapi.ExecutionManifest {
			value := routeManifest(t, plan, "/allowed/audio.wav", 0)
			value.Output.Width++
			return value
		}},
		{name: "seed", manifest: func() renderapi.ExecutionManifest { return routeManifest(t, plan, "/allowed/audio.wav", 1) }},
		{name: "locator", manifest: func() renderapi.ExecutionManifest {
			return routeManifest(t, plan, "/allowed/other.wav", 0)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
				return test.manifest(), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := executeRoute(service, context.Background(), prepareProjectID, routePayload(t, plan, []resourceBinding{{AssetID: "audio", Locator: "/allowed/audio.wav"}}, nil))
			if err != nil {
				t.Fatal(err)
			}
			assertRejected(t, outcome, CodePrepareRejected)
		})
	}
}

func TestPrepareRouteIsolatesHostMutation(t *testing.T) {
	plan := routeRenderPlan(t)
	valid := routeManifest(t, plan, "/allowed/audio.wav", 0)
	payload := routePayload(t, plan, []resourceBinding{{AssetID: "audio", Locator: "/allowed/audio.wav"}}, nil)
	payloadBefore := append([]byte(nil), payload...)
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(_ context.Context, _ string, request ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		request.Plan.Resources[0].ContentHash = routeDigest('f')
		request.Plan.RequiredCapabilities.AssetKinds[0] = videoedit.AssetKindVideo
		request.Resources[0].AssetID = "mutated"
		return valid, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := executeRoute(service, context.Background(), prepareProjectID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.OK || !bytes.Equal(payload, payloadBefore) || plan.Resources[0].ContentHash != routeDigest('a') ||
		plan.RequiredCapabilities.AssetKinds[0] != videoedit.AssetKindAudio {
		t.Fatalf("host mutation escaped: outcome=%+v plan=%+v", outcome, plan)
	}
}

func TestPrepareRoutePreservesCancellation(t *testing.T) {
	plan := routeRenderPlan(t)
	payload := routePayload(t, plan, []resourceBinding{{AssetID: "audio", Locator: "/allowed/audio.wav"}}, nil)
	var calls atomic.Int32
	service, err := New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		calls.Add(1)
		return renderapi.ExecutionManifest{}, errors.New("unexpected")
	})})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeRoute(service, canceled, prepareProjectID, payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("pre-canceled request reached preparer")
	}

	ctx, cancel := context.WithCancel(context.Background())
	service, err = New(Config{ProjectID: prepareProjectID, Preparer: PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		cancel()
		return routeManifest(t, plan, "/allowed/audio.wav", 0), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeRoute(service, ctx, prepareProjectID, payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight canceled error = %v", err)
	}
}

func TestPrepareNewRejectsInvalidAndTypedNilConfiguration(t *testing.T) {
	valid := PreparerFunc(func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
		return renderapi.ExecutionManifest{}, nil
	})
	var nilFunction PreparerFunc
	var nilPointer *pointerPreparer
	tests := []Config{
		{},
		{ProjectID: " \t", Preparer: valid},
		{ProjectID: string([]byte{0xff}), Preparer: valid},
		{ProjectID: prepareProjectID},
		{ProjectID: prepareProjectID, Preparer: nilFunction},
		{ProjectID: prepareProjectID, Preparer: nilPointer},
	}
	for index, config := range tests {
		if _, err := New(config); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

type pointerPreparer struct{}

func (*pointerPreparer) PrepareManifest(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
	return renderapi.ExecutionManifest{}, nil
}

func executeRoute(service *Service, ctx context.Context, projectID string, payload json.RawMessage) (router.Outcome, error) {
	routes := service.Routes()
	return routes[0].Handler.Execute(ctx, agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "request",
		Tool: agentprotocol.ToolRender, Action: "prepare", ProjectID: projectID, Payload: payload,
	})
}

func assertRejected(t *testing.T, outcome router.Outcome, code string) {
	t.Helper()
	if outcome.OK || outcome.State != "rejected" || len(outcome.Diagnostics) != 1 ||
		outcome.Diagnostics[0].Code != code || outcome.Diagnostics[0].Severity != videoedit.SeverityError ||
		outcome.NextActions == nil || len(outcome.NextActions) != 0 {
		t.Fatalf("outcome = %+v, want rejection %s", outcome, code)
	}
}

func routePayload(t *testing.T, plan videoedit.RenderPlan, resources []resourceBinding, seed *int64) json.RawMessage {
	t.Helper()
	raw, err := canonicaljson.Marshal(struct {
		Plan              json.RawMessage   `json:"plan"`
		Resources         []resourceBinding `json:"resources"`
		DeterministicSeed *int64            `json:"deterministic_seed,omitempty"`
	}{Plan: mustCanonical(t, plan), Resources: resources, DeterministicSeed: seed})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustCanonical(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func routeManifest(t *testing.T, plan videoedit.RenderPlan, locator string, seed int64) renderapi.ExecutionManifest {
	t.Helper()
	capabilities := videoedit.BackendCapabilities{
		BackendID: "cutly.ffmpeg-hardcut.v1", BuildFingerprint: routeDigest('b'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindAudio},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindAudio}, MaxWidth: 8192, MaxHeight: 8192,
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: routeDigest('c'), EvaluatorBuild: routeDigest('d'),
		Resources: []renderapi.MaterializedResource{{
			AssetID: "audio", Kind: videoedit.AssetKindAudio, ContentHash: routeDigest('a'), SizeBytes: 10,
			LocatorScheme: renderapi.LocatorFile, Locator: locator, ProbeFingerprint: routeDigest('f'),
		}},
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: routeDigest('e'), DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: seed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func routeRenderPlan(t *testing.T) videoedit.RenderPlan {
	t.Helper()
	one := videoedit.RationalTime{Value: 1, Rate: 1}
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{
			ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: routeDigest('a'), Duration: &one,
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
	plan, diagnostics := videoedit.Resolve(document, videoedit.TimeRange{Start: zero, Duration: one}, videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock:  videoedit.FrameClock{Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("plan diagnostics = %+v", diagnostics)
	}
	return plan
}

func routeDigest(value byte) string {
	return fmt.Sprintf("sha256:%s", strings.Repeat(string(value), 64))
}
