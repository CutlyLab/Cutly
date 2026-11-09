package preparev2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/buildidentity"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func hash(c string) string { return "sha256:" + strings.Repeat(c, 64) }
func setup(t *testing.T, name string, verifier ResourceVerifier) (*Authority, documentv2.Document, renderplanv2.Plan, renderapi.ExecutionManifestV2) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("new native provider measurement supported only on macOS/Linux")
	}
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-plan-" + name + ".document.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../conformance/fixtures/document-v2-render-plan-" + name + ".canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := renderplanv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "provider")
	if err = os.WriteFile(exe, []byte("host selected executable identity fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ProviderExecutable: exe, AllowLocalDevelopment: true, CompilerBuild: hash("1"), EvaluatorBuild: hash("2"), Backend: renderapi.BackendCapabilitiesV2{BackendID: "test-host", BuildFingerprint: hash("3"), TrackKinds: append([]documentv2.TrackKind{}, plan.RequiredCapabilities.TrackKinds...), AssetKinds: append([]documentv2.AssetKind{}, plan.RequiredCapabilities.AssetKinds...), Features: append([]videoedit.CapabilityID{}, plan.RequiredCapabilities.Features...), MaxWidth: 4096, MaxHeight: 4096}, MediaRuntime: renderapi.MediaRuntime{FFmpegBuild: hash("4"), DecoderProfile: "test.decoder.v1", EncoderProfile: "test.encoder.v1", ColorProfile: "test.color.v1"}, VerifyResource: verifier}
	host, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating caller-owned config after construction cannot modify host pins.
	if len(cfg.Backend.Features) > 0 {
		cfg.Backend.Features[0] = "mutated.v1"
	}
	target := plan.Target
	draft := renderapi.ExecutionManifestV2{Resources: []renderapi.MaterializedResourceV2{}, Output: renderapi.OutputSpecV2{ReferenceProfile: renderapi.OutputReferenceProfileV2, Format: "mp4", Width: uint32(target.Width), Height: uint32(target.Height), PixelFormat: target.PixelFormat, ColorSpace: target.ColorSpace, VideoCodec: "h264", AudioCodec: "aac", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: target.SampleClock.SamplesPerSecond, Channels: uint32(target.Channels), Intermediate: renderapi.IntermediateProfilesV2{VideoSurfaceProfile: renderapi.VideoSurfaceProfileV2, AudioBlockProfile: renderapi.AudioBlockProfileV2}, DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1}}
	for _, ref := range plan.Resources {
		receipt := hash("5")
		r := renderapi.MaterializedResourceV2{AssetID: ref.AssetID, Kind: ref.Kind, ContentHash: ref.ContentHash, SizeBytes: 12, LocatorScheme: renderapi.LocatorCAS, Locator: "cas://sha256/" + strings.TrimPrefix(ref.ContentHash, "sha256:"), MaterializationProfile: renderapi.AVMaterializationProfileV2}
		if r.ContentHash == "" {
			r.ContentHash = hash("6")
			r.Locator = "cas://sha256/" + strings.TrimPrefix(r.ContentHash, "sha256:")
		}
		if ref.Kind == documentv2.AssetKindImage || ref.Kind == documentv2.AssetKindFont {
			r.InspectionFingerprint = &receipt
			r.MaterializationProfile = renderapi.ImageMaterializationProfileV2
			if ref.Kind == documentv2.AssetKindFont {
				r.MaterializationProfile = renderapi.FontMaterializationProfileV2
			}
		} else {
			r.ProbeFingerprint = &receipt
		}
		draft.Resources = append(draft.Resources, r)
	}
	return host, doc, plan, draft
}
func requestFor(p renderplanv2.Plan, m renderapi.ExecutionManifestV2) renderapi.RenderRequestV2 {
	return renderapi.RenderRequestV2{SchemaVersion: renderapi.RenderRequestSchemaVersionV2, Profile: renderapi.RenderRequestProfileV2, CanonicalJSONProfile: canonicaljson.Profile, Plan: p, Manifest: m, Job: renderapi.RenderJob{SchemaVersion: 1, ID: "job-host", IdempotencyKey: "host-attempt", PlanFingerprint: p.Fingerprint, ExecutionManifestFingerprint: m.Fingerprint, TimelineRange: p.RequestedRange, FrameRange: p.FrameRange, SampleRange: p.SampleRange, ArtifactURI: "file:///tmp/host-output.mp4"}}
}
func TestHostPinsAndOwnedAuthorization(t *testing.T) {
	host, doc, plan, draft := setup(t, "gap", nil)
	draft.ProviderBuild = hash("f")
	draft.CompilerBuild = hash("e")
	m, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	if m.ProviderBuild != host.provider || m.CompilerBuild != hash("1") {
		t.Fatal("draft selected host identity")
	}
	req := requestFor(plan, m)
	authorized, err := host.Authorize(context.Background(), doc, req)
	if err != nil || !authorized.Valid() {
		t.Fatal(err)
	}
	raw, err := authorized.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = '!'
	owned, err := authorized.Request()
	if err != nil {
		t.Fatal(err)
	}
	owned.Manifest.ProviderBuild = hash("a")
	again, err := authorized.Request()
	if err != nil || again.Manifest.ProviderBuild != host.provider {
		t.Fatal("ownership leak", err)
	}
	for _, field := range []string{"provider", "compiler", "evaluator", "backend", "media"} {
		t.Run(field, func(t *testing.T) {
			altered := m
			switch field {
			case "provider":
				altered.ProviderBuild = hash("a")
			case "compiler":
				altered.CompilerBuild = hash("a")
			case "evaluator":
				altered.EvaluatorBuild = hash("a")
			case "backend":
				altered.Backend.BuildFingerprint = hash("a")
			case "media":
				altered.MediaRuntime.FFmpegBuild = hash("a")
			}
			altered, err = renderapi.FreezeExecutionManifestV2(context.Background(), plan, altered)
			if err != nil {
				t.Fatal(err)
			}
			forged := requestFor(plan, altered)
			if _, err := renderapi.VerifyRenderRequestV2(forged); err != nil {
				t.Fatal("attack must remain structurally valid", err)
			}
			rejected, err := host.Authorize(context.Background(), doc, forged)
			if err == nil || rejected.Valid() {
				t.Fatal("self-issued host pin accepted")
			}
		})
	}
	doc.Revision++
	if got, err := host.Authorize(context.Background(), doc, req); err == nil || got.Valid() {
		t.Fatal("different document accepted")
	}
}
func TestConcurrentOwnedAuthorization(t *testing.T) {
	host, doc, plan, draft := setup(t, "gap", nil)
	m, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	req := requestFor(plan, m)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := host.Authorize(context.Background(), doc, req)
			if err != nil || !got.Valid() {
				t.Errorf("concurrent authorization: %v", err)
				return
			}
			copy, err := got.Request()
			if err != nil {
				t.Error(err)
				return
			}
			copy.Manifest.ProviderBuild = "mutated"
		}()
	}
	wg.Wait()
}

type panicContext struct{ context.Context }

func (panicContext) Err() error { panic("hostile context") }

func TestHostConstructionFailureZero(t *testing.T) {
	for _, ctx := range []context.Context{nil, panicContext{context.Background()}} {
		if got, err := New(ctx, Config{}); err == nil || got != nil {
			t.Fatal("invalid context constructed authority")
		}
	}
	if got, err := New(context.Background(), Config{CompilerBuild: hash("1"), EvaluatorBuild: hash("2")}); err == nil || got != nil {
		t.Fatal("implicit local development allowed")
	}
	host, doc, plan, draft := setup(t, "gap", nil)
	ctx := panicContext{context.Background()}
	if got, err := host.Freeze(ctx, doc, plan, draft); err == nil || !reflect.DeepEqual(got, renderapi.ExecutionManifestV2{}) {
		t.Fatal("panic published manifest")
	}
	if got, err := host.Authorize(ctx, doc, renderapi.RenderRequestV2{}); err == nil || got.Valid() {
		t.Fatal("panic published request")
	}
}

func TestProviderBindingDoesNotRequireSelfReferentialReleasePin(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native provider measurement supported only on macOS/Linux")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "already-launched-provider")
	if err = os.WriteFile(executable, []byte("measured child bytes\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := buildidentity.MeasureProviderExecutableBinding(context.Background(), executable)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		ProviderExecutable:    executable,
		AllowLocalDevelopment: false,
		CompilerBuild:         hash("1"),
		EvaluatorBuild:        hash("2"),
	}
	if authority, err := NewWithProviderBinding(context.Background(), config, identity); err != nil || authority == nil || authority.provider != identity.Digest() {
		t.Fatalf("measured child binding rejected: authority=%v err=%v", authority, err)
	}
	if authority, err := New(context.Background(), config); err == nil || authority != nil {
		t.Fatal("development host policy unexpectedly authorized packaged launch configuration")
	}
}

func TestResourceVerificationFailureZero(t *testing.T) {
	for _, mode := range []string{"nil", "error", "panic", "drift", "oversize", "mutate"} {
		t.Run(mode, func(t *testing.T) {
			var verifier ResourceVerifier
			if mode != "nil" {
				verifier = func(ctx context.Context, documentHash string, r renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
					switch mode {
					case "error":
						return r, fmt.Errorf("inspection failed")
					case "panic":
						panic("probe crash")
					case "drift":
						r.SizeBytes++
					case "oversize":
						r.Locator = strings.Repeat("x", (1<<20)+1)
					case "mutate":
						if r.InspectionFingerprint != nil {
							*r.InspectionFingerprint = hash("f")
						} else {
							*r.ProbeFingerprint = hash("f")
						}
					}
					return r, nil
				}
			}
			host, doc, plan, draft := setup(t, "mixed", verifier)
			if got, err := host.Freeze(context.Background(), doc, plan, draft); err == nil || !reflect.DeepEqual(got, renderapi.ExecutionManifestV2{}) {
				t.Fatal("failed verifier published manifest", err)
			}
		})
	}
}
func TestCancelDuringResourceVerification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	table := map[documentv2.ID]renderapi.MaterializedResourceV2{}
	verifier := func(_ context.Context, _ string, r renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		cancel()
		return table[r.AssetID], nil
	}
	host, doc, plan, draft := setup(t, "mixed", verifier)
	for _, r := range draft.Resources {
		table[r.AssetID] = r
	}
	if got, err := host.Freeze(ctx, doc, plan, draft); err == nil || !reflect.DeepEqual(got, renderapi.ExecutionManifestV2{}) {
		t.Fatal("mid-verification cancellation published manifest")
	}
}

func TestUnauthorizedBuildRejectedBeforeResourceCallback(t *testing.T) {
	calls := 0
	host, doc, plan, draft := setup(t, "mixed", func(_ context.Context, _ string, r renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		calls++
		return r, nil
	})
	// Create a structurally valid request outside the host factory. The callback
	// is never invoked because its provider executable has not been authorized.
	var pins hostPins
	if err := json.Unmarshal([]byte(host.pins), &pins); err != nil {
		t.Fatal(err)
	}
	draft.CompilerBuild, draft.EvaluatorBuild, draft.ProviderBuild = pins.CompilerBuild, pins.EvaluatorBuild, hash("f")
	draft.Backend, draft.MediaRuntime = pins.Backend, pins.MediaRuntime
	m, err := renderapi.FreezeExecutionManifestV2(context.Background(), plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := host.Authorize(context.Background(), doc, requestFor(plan, m)); err == nil || got.Valid() || calls != 0 {
		t.Fatal("unauthorized request reached resource callback", calls, err)
	}
}

func TestResourceObservedBytesAndCancellation(t *testing.T) {
	// This host stub models an independently measured resource table, not a
	// production filesystem inspector. Swapping the table after freeze is detected.
	table := map[documentv2.ID]renderapi.MaterializedResourceV2{}
	verifier := func(ctx context.Context, dh string, r renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		return table[r.AssetID], nil
	}
	host, doc, plan, draft := setup(t, "mixed", verifier)
	for _, r := range draft.Resources {
		table[r.AssetID] = r
	}
	m, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	req := requestFor(plan, m)
	if _, err := host.Authorize(context.Background(), doc, req); err != nil {
		t.Fatal(err)
	}
	first := draft.Resources[0]
	changed := table[first.AssetID]
	changed.ContentHash = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("changed bytes")))
	table[first.AssetID] = changed
	if got, err := host.Authorize(context.Background(), doc, req); err == nil || got.Valid() {
		t.Fatal("resource drift accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := host.Freeze(ctx, doc, plan, draft); err == nil || !reflect.DeepEqual(got, renderapi.ExecutionManifestV2{}) {
		t.Fatal("canceled freeze")
	}
	if got, err := host.Authorize(ctx, doc, req); err == nil || got.Valid() {
		t.Fatal("canceled authorization")
	}
	var zero *Authority
	if got, err := zero.Authorize(context.Background(), doc, req); err == nil || got.Valid() {
		t.Fatal("zero authority")
	}
	if got, err := host.Authorize(nil, doc, req); err == nil || got.Valid() {
		t.Fatal("nil context")
	}
}
