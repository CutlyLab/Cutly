package renderexec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestPreflightAndExactFrameRoutes(t *testing.T) {
	plan, manifest := testEnvironment(t)
	backend := &testBackend{capabilities: manifest.Backend}
	actionRouter := testRouter(t, backend)

	preflight := handle(t, actionRouter, "preflight", struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
	}{Plan: plan, ExecutionManifest: manifest})
	if !preflight.OK || preflight.State != "ready" || !preflight.RenderRequired || backend.preflightCalls != 1 {
		t.Fatalf("preflight = %+v, calls=%d", preflight, backend.preflightCalls)
	}
	var preflightValue preflightReceipt
	if err := json.Unmarshal(preflight.Receipt, &preflightValue); err != nil ||
		preflightValue.PlanFingerprint != plan.Fingerprint || preflightValue.ExecutionManifestFingerprint != manifest.Fingerprint {
		t.Fatalf("preflight receipt=%+v err=%v", preflightValue, err)
	}

	frame := renderapi.ExactFrameRequest{
		SchemaVersion: renderapi.ExactFrameRequestSchemaVersion,
		ID:            "frame-1", IdempotencyKey: "frame-once",
		PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
		CompositionFrameIndex: plan.FrameRange.First, ArtifactURI: "file:///controlled/frame.png",
	}
	framed := handle(t, actionRouter, "frame", struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
		Frame             renderapi.ExactFrameRequest `json:"frame"`
	}{Plan: plan, ExecutionManifest: manifest, Frame: frame})
	if !framed.OK || framed.State != "complete" || !framed.Changed || !framed.RenderRequired || backend.frameCalls != 1 {
		t.Fatalf("frame = %+v, calls=%d", framed, backend.frameCalls)
	}
	var frameValue frameReceipt
	if err := json.Unmarshal(framed.Receipt, &frameValue); err != nil || frameValue.Artifact.CompositionFrameIndex != frame.CompositionFrameIndex {
		t.Fatalf("frame receipt=%+v err=%v", frameValue, err)
	}
}

func TestPreflightRejectsStrictPayloadAndBackendDrift(t *testing.T) {
	plan, manifest := testEnvironment(t)
	backend := &testBackend{capabilities: manifest.Backend}
	actionRouter := testRouter(t, backend)

	invalidPayload := json.RawMessage(`{"plan":{},"execution_manifest":{},"surprise":true}`)
	result := handleRaw(t, actionRouter, "preflight", invalidPayload)
	assertDiagnostic(t, result, CodePayloadInvalid)

	drifted := *backend
	drifted.capabilities.BuildFingerprint = digest('9')
	driftRouter := testRouter(t, &drifted)
	result = handle(t, driftRouter, "preflight", struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
	}{Plan: plan, ExecutionManifest: manifest})
	assertDiagnostic(t, result, CodeBackendMismatch)
	if drifted.preflightCalls != 0 {
		t.Fatal("backend preflight ran before live capability binding was verified")
	}
}

func TestPreflightPreservesBackendDiagnostics(t *testing.T) {
	plan, manifest := testEnvironment(t)
	backend := &testBackend{
		capabilities: manifest.Backend,
		diagnostics: []videoedit.Diagnostic{{
			Code: "BACKEND.MEDIA_REJECTED", Severity: videoedit.SeverityError,
			Path: "/resources/0", Message: "fixture media is unsupported",
		}},
	}
	result := handle(t, testRouter(t, backend), "preflight", struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
	}{Plan: plan, ExecutionManifest: manifest})
	assertDiagnostic(t, result, "BACKEND.MEDIA_REJECTED")
	if result.State != "rejected" || result.RenderRequired {
		t.Fatalf("preflight rejection = %+v", result)
	}
}

func TestFrameFailsClosedForUnsupportedOrInvalidBackendResult(t *testing.T) {
	plan, manifest := testEnvironment(t)
	frame := renderapi.ExactFrameRequest{
		SchemaVersion: renderapi.ExactFrameRequestSchemaVersion,
		ID:            "frame-1", IdempotencyKey: "frame-once",
		PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
		CompositionFrameIndex: plan.FrameRange.First, ArtifactURI: "file:///controlled/frame.png",
	}
	payload := struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
		Frame             renderapi.ExactFrameRequest `json:"frame"`
	}{Plan: plan, ExecutionManifest: manifest, Frame: frame}

	unsupported := &renderOnlyBackend{capabilities: manifest.Backend}
	result := handle(t, testRouterWithResolver(t, ResolverFunc(func(context.Context, string, renderapi.ExecutionManifest) (renderapi.RenderBackend, error) {
		return unsupported, nil
	})), "frame", payload)
	assertDiagnostic(t, result, CodeFrameUnsupported)

	invalid := &testBackend{capabilities: manifest.Backend, corruptArtifact: true}
	result = handle(t, testRouter(t, invalid), "frame", payload)
	assertDiagnostic(t, result, CodeFrameArtifactInvalid)
}

func TestBackendUnavailableIsBusinessRejectionAndUnexpectedResolverFailureIsTransportError(t *testing.T) {
	plan, manifest := testEnvironment(t)
	payload := struct {
		Plan              videoedit.RenderPlan        `json:"plan"`
		ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
	}{Plan: plan, ExecutionManifest: manifest}

	unavailable := ResolverFunc(func(context.Context, string, renderapi.ExecutionManifest) (renderapi.RenderBackend, error) {
		return nil, errors.Join(ErrBackendUnavailable, errors.New("native backend is not installed"))
	})
	result := handle(t, testRouterWithResolver(t, unavailable), "preflight", payload)
	assertDiagnostic(t, result, CodeBackendUnavailable)

	want := errors.New("resolver storage failed")
	failing := ResolverFunc(func(context.Context, string, renderapi.ExecutionManifest) (renderapi.RenderBackend, error) {
		return nil, want
	})
	raw := canonicalPayload(t, payload)
	request := testRequest("preflight", raw)
	if _, err := testRouterWithResolver(t, failing).HandleAgent(context.Background(), request); !errors.Is(err, want) {
		t.Fatalf("transport error = %v, want %v", err, want)
	}
}

type testBackend struct {
	capabilities    videoedit.BackendCapabilities
	diagnostics     []videoedit.Diagnostic
	corruptArtifact bool
	preflightCalls  int
	frameCalls      int
}

func (backend *testBackend) ID() string { return backend.capabilities.BackendID }

func (backend *testBackend) Capabilities(context.Context) (videoedit.BackendCapabilities, error) {
	return backend.capabilities, nil
}

func (backend *testBackend) Preflight(context.Context, videoedit.RenderPlan, renderapi.ExecutionManifest) []videoedit.Diagnostic {
	backend.preflightCalls++
	return append([]videoedit.Diagnostic(nil), backend.diagnostics...)
}

func (*testBackend) Render(context.Context, renderapi.RenderRequest, renderapi.EventSink) (renderapi.RenderArtifact, error) {
	return renderapi.RenderArtifact{}, errors.New("not used")
}

func (backend *testBackend) RenderFrame(_ context.Context, request renderapi.ExactFrameRenderRequest, _ renderapi.EventSink) (renderapi.ExactFrameArtifact, error) {
	backend.frameCalls++
	evaluation, err := videoedit.EvaluateFrame(request.Plan, request.Frame.CompositionFrameIndex)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	fingerprint, err := renderapi.FrameEvaluationFingerprint(evaluation)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	contentHash := digest('7')
	if backend.corruptArtifact {
		contentHash = ""
	}
	return renderapi.ExactFrameArtifact{
		URI: request.Frame.ArtifactURI, ContentHash: contentHash, SizeBytes: 123,
		DecodedContentHash: digest('8'), PlanFingerprint: request.Plan.Fingerprint,
		ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		BackendID:                    request.Manifest.Backend.BackendID,
		BackendBuildFingerprint:      request.Manifest.Backend.BuildFingerprint,
		ReferenceProfile:             request.Manifest.Output.ReferenceProfile,
		PNGEncodingProfile:           renderapi.ExactFramePNGProfileV1,
		DecodedVideoProfile:          request.Manifest.Output.DecodedVideoProfile,
		Width:                        request.Manifest.Output.Width, Height: request.Manifest.Output.Height,
		CompositionFrameIndex: request.Frame.CompositionFrameIndex,
		TimelineTime:          evaluation.TimelineTime, EvaluationFingerprint: fingerprint, Evaluation: evaluation,
	}, nil
}

type renderOnlyBackend struct {
	capabilities videoedit.BackendCapabilities
}

func (backend *renderOnlyBackend) ID() string { return backend.capabilities.BackendID }
func (backend *renderOnlyBackend) Capabilities(context.Context) (videoedit.BackendCapabilities, error) {
	return backend.capabilities, nil
}
func (*renderOnlyBackend) Preflight(context.Context, videoedit.RenderPlan, renderapi.ExecutionManifest) []videoedit.Diagnostic {
	return []videoedit.Diagnostic{}
}
func (*renderOnlyBackend) Render(context.Context, renderapi.RenderRequest, renderapi.EventSink) (renderapi.RenderArtifact, error) {
	return renderapi.RenderArtifact{}, errors.New("not used")
}

func testRouter(t *testing.T, backend renderapi.RenderBackend) *router.Router {
	t.Helper()
	return testRouterWithResolver(t, ResolverFunc(func(_ context.Context, projectID string, _ renderapi.ExecutionManifest) (renderapi.RenderBackend, error) {
		if projectID != "project" {
			t.Fatalf("project ID = %q", projectID)
		}
		return backend, nil
	}))
}

func testRouterWithResolver(t *testing.T, resolver Resolver) *router.Router {
	t.Helper()
	service, err := New(resolver)
	if err != nil {
		t.Fatal(err)
	}
	value, err := router.New(service.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func handle(t *testing.T, actionRouter *router.Router, action string, payload any) agentprotocol.Result {
	t.Helper()
	return handleRaw(t, actionRouter, action, canonicalPayload(t, payload))
}

func handleRaw(t *testing.T, actionRouter *router.Router, action string, payload json.RawMessage) agentprotocol.Result {
	t.Helper()
	result, err := actionRouter.HandleAgent(context.Background(), testRequest(action, payload))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testRequest(action string, payload json.RawMessage) agentprotocol.Request {
	return agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "request-1",
		Tool: agentprotocol.ToolRender, Action: action, ProjectID: "project", Payload: payload,
	}
}

func canonicalPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertDiagnostic(t *testing.T, result agentprotocol.Result, code string) {
	t.Helper()
	if result.OK || len(result.Diagnostics) == 0 || result.Diagnostics[0].Code != code {
		t.Fatalf("result = %+v, want diagnostic %s", result, code)
	}
}

func testEnvironment(t *testing.T) (videoedit.RenderPlan, renderapi.ExecutionManifest) {
	t.Helper()
	duration := rational(10, 1)
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{
			ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4",
			ContentHash: digest('a'), Duration: &duration,
		}},
		Timelines: []videoedit.Timeline{{
			ID: "timeline", Tracks: []videoedit.Track{{
				ID: "video-track", Kind: videoedit.TrackKindVideo,
				Clips: []videoedit.Clip{{ID: "clip", AssetID: "video", TimelineRange: timeRange(0, 2), SourceRange: timeRange(0, 2)}},
			}},
		}},
	}
	plan, diagnostics := videoedit.Resolve(document, timeRange(0, 2), videoedit.RenderTarget{
		TimelineID: "timeline", Width: 1920, Height: 1080,
		FrameClock:  videoedit.FrameClock{Origin: rational(0, 1), FramesPerSecond: rational(30, 1)},
		SampleClock: videoedit.SampleClock{Origin: rational(0, 1), SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: "native-cpu", BuildFingerprint: digest('1'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindVideo},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindVideo},
		MaxWidth:   3840, MaxHeight: 2160,
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digest('c'), EvaluatorBuild: digest('d'),
		Resources: []renderapi.MaterializedResource{{
			AssetID: "video", Kind: videoedit.AssetKindVideo, ContentHash: digest('a'), SizeBytes: 100,
			LocatorScheme: renderapi.LocatorFile, Locator: "/controlled/video.mp4", ProbeFingerprint: digest('e'),
		}},
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digest('2'), DecoderProfile: "ffmpeg-sw-v1",
			EncoderProfile: "libx264-aac-v1", ColorProfile: "cutly.bt709-linear.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.reference.hardcut.v1", Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan, manifest
}

func rational(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func timeRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: rational(start, 1), Duration: rational(duration, 1)}
}

func digest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
