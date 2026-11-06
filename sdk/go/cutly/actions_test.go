package cutly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestTypedAgentActionsBuildExactPayloads(t *testing.T) {
	var requests []agentprotocol.Request
	client, err := NewClient(TransportFunc(func(_ context.Context, raw []byte) ([]byte, error) {
		request, err := agentprotocol.DecodeRequest(raw)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
		return agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion, OK: true, RequestID: request.RequestID,
			Tool: request.Tool, Action: request.Action, State: "ok", ProjectID: request.ProjectID,
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	transaction := TransactionV1{
		ID: "tx", IdempotencyKey: "key", ExpectedRevision: 7,
		Operations: []OperationV1{{ID: "delete", Kind: OperationDeleteV1, ClipID: "clip"}},
	}
	renderRange := TimeRange{
		Start: RationalTime{Value: 0, Rate: 1}, Duration: RationalTime{Value: 1, Rate: 1},
	}
	renderTarget := RenderTargetV3{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock: FrameClock{
			Origin: RationalTime{Value: 0, Rate: 1}, FramesPerSecond: RationalTime{Value: 30, Rate: 1},
		},
		SampleClock: SampleClock{Origin: RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	}
	calls := []func() (Result, error){
		func() (Result, error) { return client.InspectProject(context.Background(), "r1", "project") },
		func() (Result, error) { return client.ProjectStatus(context.Background(), "r2", "project") },
		func() (Result, error) { return client.ProjectHistory(context.Background(), "r3", "project", 4, 10) },
		func() (Result, error) { return client.PlanEditV1(context.Background(), "r4", "project", transaction) },
		func() (Result, error) { return client.ApplyEditV1(context.Background(), "r5", "project", "tx", 7) },
		func() (Result, error) { return client.DiscardEditV1(context.Background(), "r6", "project", "tx") },
		func() (Result, error) { return client.RevertEditV1(context.Background(), "r7", "project", "tx", 8) },
		func() (Result, error) {
			return client.RenderPlan(context.Background(), "r8", "project", renderRange, renderTarget)
		},
		func() (Result, error) {
			seed := int64(42)
			return client.PrepareRender(context.Background(), "r9", "project", RenderPlanV3{}, []RenderPrepareResourceV1{{
				AssetID: "asset", Locator: "/media/asset.mov",
			}}, &seed)
		},
		func() (Result, error) {
			return client.PreflightRender(context.Background(), "r10", "project", RenderPlanV3{}, ExecutionManifestV1{})
		},
		func() (Result, error) {
			return client.RenderFrame(context.Background(), "r11", "project", RenderPlanV3{}, ExecutionManifestV1{}, ExactFrameRequestV1{})
		},
		func() (Result, error) {
			return client.SubmitRender(context.Background(), "r12", "project", RenderJobV1{})
		},
		func() (Result, error) { return client.RenderStatus(context.Background(), "r13", "project", "job") },
		func() (Result, error) { return client.CancelRender(context.Background(), "r14", "project", "job") },
	}
	for index, call := range calls {
		if _, err := call(); err != nil {
			t.Fatalf("call %d: %v", index, err)
		}
	}
	if len(requests) != len(calls) {
		t.Fatalf("requests = %d", len(requests))
	}
	if got := string(requests[2].Payload); got != `{"after_sequence":4,"limit":10}` {
		t.Fatalf("history payload = %s", got)
	}
	if got := string(requests[4].Payload); got != `{"expected_revision":7,"transaction_id":"tx"}` {
		t.Fatalf("apply payload = %s", got)
	}
	wantPlan, err := canonicaljson.Marshal(renderPlanPayload{Range: renderRange, Target: renderTarget})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[7].Payload); got != string(wantPlan) {
		t.Fatalf("render plan payload = %s, want %s", got, wantPlan)
	}
	wantPrepare, err := canonicaljson.Marshal(renderPreparePayload{
		Plan: RenderPlanV3{}, Resources: []RenderPrepareResourceV1{{AssetID: "asset", Locator: "/media/asset.mov"}},
		DeterministicSeed: func() *int64 { value := int64(42); return &value }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[8].Payload); got != string(wantPrepare) {
		t.Fatalf("prepare payload = %s, want %s", got, wantPrepare)
	}
	wantPreflight, err := canonicaljson.Marshal(renderPreflightPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[9].Payload); got != string(wantPreflight) {
		t.Fatalf("preflight payload = %s, want %s", got, wantPreflight)
	}
	wantFrame, err := canonicaljson.Marshal(renderFramePayload{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[10].Payload); got != string(wantFrame) {
		t.Fatalf("frame payload = %s, want %s", got, wantFrame)
	}
	wantRender, err := canonicaljson.Marshal(renderPayload{Job: RenderJobV1{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(requests[11].Payload); got != string(wantRender) {
		t.Fatalf("render payload = %s, want %s", got, wantRender)
	}
}

func TestProjectReceiptDecoders(t *testing.T) {
	document := DocumentV1{SchemaVersion: videoedit.SchemaVersion, ID: "doc", Revision: 2}
	inspectRaw, err := canonicaljson.Marshal(ProjectInspectReceiptV1{
		Profile: "cutly.project-inspect.v1",
		Snapshot: ProjectSnapshotV1{
			Document: document, DocumentHash: digestString('a'), PendingPlans: []ProjectPlanSummaryV1{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := DecodeProjectInspectReceipt(inspectRaw)
	if err != nil || inspect.Snapshot.Document.ID != "doc" {
		t.Fatalf("inspect = %+v, %v", inspect, err)
	}

	historyRaw, err := canonicaljson.Marshal(ProjectHistoryReceiptV1{
		Profile: "cutly.project-history.v1", Records: []ProjectHistoryRecordV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := DecodeProjectHistoryReceipt(historyRaw); err != nil || receipt.Records == nil {
		t.Fatalf("history = %+v, %v", receipt, err)
	}

	mutationRaw, err := canonicaljson.Marshal(ProjectMutationReceiptV1{
		Profile: "cutly.project-mutation.v1", Action: "apply", State: "applied", TransactionID: "tx",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := DecodeProjectMutationReceipt(mutationRaw); err != nil || receipt.TransactionID != "tx" {
		t.Fatalf("mutation = %+v, %v", receipt, err)
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(` {"profile":"cutly.project-mutation.v1","action":"apply","state":"applied"}`),
		json.RawMessage(`{"action":"apply","profile":"wrong.v1","state":"applied"}`),
		json.RawMessage(`{"action":"apply","extra":1,"profile":"cutly.project-mutation.v1","state":"applied"}`),
	} {
		if _, err := DecodeProjectMutationReceipt(raw); !errors.Is(err, ErrReceiptInvalid) {
			t.Fatalf("invalid receipt %s: %v", raw, err)
		}
	}
}

func TestRenderReceiptDecoders(t *testing.T) {
	plan, manifest, resources := renderPrepareSDKFixture(t)
	planRaw, err := canonicaljson.Marshal(RenderPlanReceiptV1{
		Profile: RenderPlanReceiptProfileV1, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := DecodeRenderPlanReceipt(planRaw)
	if err != nil || planned.Plan.Fingerprint != plan.Fingerprint ||
		ValidateRenderPlanReceipt(planned, plan.RequestedRange, plan.Target) != nil {
		t.Fatalf("plan = %+v, decode=%v validate=%v", planned, err, ValidateRenderPlanReceipt(planned, plan.RequestedRange, plan.Target))
	}
	autoTarget := plan.Target
	autoTarget.TimelineID = ""
	if err := ValidateRenderPlanReceipt(planned, plan.RequestedRange, autoTarget); err != nil {
		t.Fatalf("plan rejected host-selected timeline: %v", err)
	}
	wrongTarget := plan.Target
	wrongTarget.Width++
	if err := ValidateRenderPlanReceipt(planned, plan.RequestedRange, wrongTarget); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("plan accepted wrong target: %v", err)
	}
	badPlan := planned
	badPlan.Plan.Fingerprint = digestString('0')
	badPlanRaw, err := canonicaljson.Marshal(badPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRenderPlanReceipt(badPlanRaw); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("plan accepted invalid fingerprint: %v", err)
	}

	prepareRaw, err := canonicaljson.Marshal(RenderPrepareReceiptV1{
		Profile: RenderPrepareReceiptProfileV1, ExecutionManifest: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := DecodeRenderPrepareReceipt(prepareRaw)
	seed := int64(7)
	if err != nil || prepared.ExecutionManifest.Fingerprint != manifest.Fingerprint ||
		ValidateRenderPrepareReceipt(prepared, plan, resources, &seed) != nil {
		t.Fatalf("prepare = %+v, decode=%v validate=%v", prepared, err, ValidateRenderPrepareReceipt(prepared, plan, resources, &seed))
	}
	wrongResources := append([]RenderPrepareResourceV1(nil), resources...)
	wrongResources[0].Locator = "/media/other.wav"
	if err := ValidateRenderPrepareReceipt(prepared, plan, wrongResources, &seed); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("prepare accepted replaced locator: %v", err)
	}
	wrongSeed := int64(8)
	if err := ValidateRenderPrepareReceipt(prepared, plan, resources, &wrongSeed); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("prepare accepted wrong seed: %v", err)
	}

	preflightRaw, err := canonicaljson.Marshal(RenderPreflightReceiptV1{
		Profile:         "cutly.agent-render-preflight.v1",
		PlanFingerprint: digestString('a'), ExecutionManifestFingerprint: digestString('b'),
		BackendID: "native-cpu", BackendBuildFingerprint: digestString('c'),
	})
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := DecodeRenderPreflightReceipt(preflightRaw)
	if err != nil || preflight.BackendID != "native-cpu" {
		t.Fatalf("preflight = %+v, %v", preflight, err)
	}

	frameRaw, err := canonicaljson.Marshal(RenderFrameReceiptV1{
		Profile: "cutly.agent-render-frame.v1",
		Request: ExactFrameRequestV1{
			SchemaVersion: 1, ID: "frame", IdempotencyKey: "frame-once",
			PlanFingerprint: digestString('a'), ExecutionManifestFingerprint: digestString('b'),
			ArtifactURI: "file:///controlled/frame.png",
		},
		Artifact: ExactFrameArtifactV1{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := DecodeRenderFrameReceipt(frameRaw); err != nil || receipt.Request.ID != "frame" {
		t.Fatalf("frame = %+v, %v", receipt, err)
	}

	bad := append([]byte(nil), preflightRaw...)
	bad = []byte(strings.Replace(string(bad), digestString('a'), "sha256:ABC", 1))
	if _, err := DecodeRenderPreflightReceipt(bad); !errors.Is(err, ErrReceiptInvalid) {
		t.Fatalf("bad preflight = %v", err)
	}
}

func TestPrepareRenderOmitsAbsentSeedAndOwnsEmptyResources(t *testing.T) {
	var captured agentprotocol.Request
	client, err := NewClient(TransportFunc(func(_ context.Context, raw []byte) ([]byte, error) {
		decoded, decodeErr := agentprotocol.DecodeRequest(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		captured = decoded
		return agentprotocol.CanonicalResult(Result{
			ProtocolVersion: ProtocolVersion, OK: true, RequestID: captured.RequestID,
			Tool: captured.Tool, Action: captured.Action, ProjectID: captured.ProjectID, State: "prepared",
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []NextAction{},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PrepareRender(context.Background(), "prepare", "project", RenderPlanV3{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(captured.Payload); !strings.Contains(got, `"resources":[]`) || strings.Contains(got, "deterministic_seed") {
		t.Fatalf("prepare payload = %s", got)
	}
}

func renderPrepareSDKFixture(t *testing.T) (RenderPlanV3, ExecutionManifestV1, []RenderPrepareResourceV1) {
	t.Helper()
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	one := videoedit.RationalTime{Value: 1, Rate: 1}
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion, ID: "document", Revision: 1,
		Assets: []videoedit.Asset{{
			ID: "audio", Kind: videoedit.AssetKindAudio, URI: "audio.wav", ContentHash: digestString('a'), Duration: &one,
		}},
		Timelines: []videoedit.Timeline{{ID: "timeline", Tracks: []videoedit.Track{{
			ID: "audio-track", Kind: videoedit.TrackKindAudio, Clips: []videoedit.Clip{{
				ID: "audio-clip", AssetID: "audio", TimelineRange: videoedit.TimeRange{Start: zero, Duration: one},
				SourceRange: videoedit.TimeRange{Start: zero, Duration: one},
			}},
		}}}},
	}
	plan, diagnostics := videoedit.Resolve(document, videoedit.TimeRange{Start: zero, Duration: one}, videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock:  videoedit.FrameClock{Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 30, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("plan diagnostics = %+v", diagnostics)
	}
	capabilities := videoedit.BackendCapabilities{
		BackendID: "cutly.ffmpeg-hardcut.v1", BuildFingerprint: digestString('b'),
		TrackKinds: []videoedit.TrackKind{videoedit.TrackKindAudio},
		AssetKinds: []videoedit.AssetKind{videoedit.AssetKindAudio}, MaxWidth: 8192, MaxHeight: 8192,
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digestString('c'), EvaluatorBuild: digestString('d'),
		Resources: []renderapi.MaterializedResource{{
			AssetID: "audio", Kind: videoedit.AssetKindAudio, ContentHash: digestString('a'), SizeBytes: 10,
			LocatorScheme: renderapi.LocatorFile, Locator: "/media/audio.wav", ProbeFingerprint: digestString('f'),
		}},
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digestString('e'), DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4", Width: 64, Height: 64,
			PixelFormat: "yuv420p", ColorSpace: "bt709", VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: 48_000, Channels: 2, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	resources := []RenderPrepareResourceV1{{AssetID: "audio", Locator: "/media/audio.wav"}}
	return plan, manifest, resources
}

func digestString(fill byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = fill
	}
	return "sha256:" + string(value)
}
