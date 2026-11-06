package ffmpeghost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	hardcutDecoderProfile   = "ffmpeg-hardcut-v1"
	hardcutEncoderProfile   = "libx264-aac-mp4.v1"
	hardcutColorProfile     = "cutly.bt709.v1"
	hardcutReferenceProfile = "cutly.hardcut.bt709.v1"
)

// PrepareResource is the only machine-specific resource choice accepted from
// an Agent prepare request. Runtime, backend, probe, and output identities are
// always selected and observed by the host.
type PrepareResource struct {
	AssetID videoedit.ID
	Locator string
}

// PrepareRequest carries a previously frozen semantic plan plus exact local
// file bindings. The deterministic seed is data, not a runtime selection.
type PrepareRequest struct {
	Plan              videoedit.RenderPlan
	Resources         []PrepareResource
	DeterministicSeed int64
}

// PrepareManifest observes the live host and materializes a new frozen
// ExecutionManifest. Agent input cannot select the backend, runtime profiles,
// codecs, probe fingerprints, or any other executable identity.
func (resolver *Resolver) PrepareManifest(
	ctx context.Context,
	projectID string,
	request PrepareRequest,
) (manifest renderapi.ExecutionManifest, returnErr error) {
	defer func() {
		if recover() != nil {
			manifest = renderapi.ExecutionManifest{}
			returnErr = unavailable("host prepare callback panicked")
		}
	}()
	if resolver == nil || nilLike(resolver.backend) || nilLike(resolver.prober) {
		return renderapi.ExecutionManifest{}, unavailable("host prepare is unavailable")
	}
	if ctx == nil {
		return renderapi.ExecutionManifest{}, unavailable("context is required")
	}
	if err := ctx.Err(); err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	if projectID != resolver.projectID {
		return renderapi.ExecutionManifest{}, unavailable("project scope mismatch")
	}

	plan, err := cloneRenderPlan(request.Plan)
	if err != nil {
		return renderapi.ExecutionManifest{}, unavailable("semantic render plan is invalid")
	}
	bindings, err := exactResourceBindings(plan.Resources, request.Resources)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	if plan.Target.PixelFormat != "yuv420p" || plan.Target.ColorSpace != "bt709" ||
		(plan.Target.Channels != 1 && plan.Target.Channels != 2) {
		return renderapi.ExecutionManifest{}, unavailable("semantic target is unsupported by the hard-cut output profile")
	}

	capabilities, err := resolver.backend.Capabilities(ctx)
	if err := callbackError(ctx, err); err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	capabilities = videoedit.CanonicalBackendCapabilities(capabilities)
	backendID := resolver.backend.ID()
	if backendID == "" || backendID != capabilities.BackendID {
		return renderapi.ExecutionManifest{}, unavailable("live backend identity is inconsistent")
	}
	if diagnostics := videoedit.CheckCapabilities(plan, capabilities); videoedit.HasErrorDiagnostics(diagnostics) {
		return renderapi.ExecutionManifest{}, unavailable("live backend capabilities reject the semantic plan")
	}
	runtimeFingerprint, err := resolver.backend.FFmpegRuntimeFingerprint(ctx)
	if err := callbackError(ctx, err); err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	if !validSHA256(runtimeFingerprint) {
		return renderapi.ExecutionManifest{}, unavailable("FFmpeg runtime identity is invalid")
	}

	resources := make([]renderapi.MaterializedResource, 0, len(plan.Resources))
	for _, planned := range plan.Resources {
		if err := ctx.Err(); err != nil {
			return renderapi.ExecutionManifest{}, err
		}
		locator := bindings[planned.AssetID]
		if !filepath.IsAbs(locator) || filepath.Clean(locator) != locator || strings.ContainsRune(locator, 0) {
			return renderapi.ExecutionManifest{}, unavailable("resource locator is not an absolute clean file path")
		}
		if err := resolver.backend.ValidateInputLocator(locator); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return renderapi.ExecutionManifest{}, ctxErr
			}
			return renderapi.ExecutionManifest{}, unavailable("resource locator is not authorized")
		}

		observed, diagnostics, err := resolver.prober.Probe(ctx, cutlyffprobe.Request{
			AssetID: planned.AssetID, Path: locator, ExpectedContentHash: planned.ContentHash,
		})
		if err := callbackError(ctx, err); err != nil {
			return renderapi.ExecutionManifest{}, err
		}
		if probe.HasErrorDiagnostics(diagnostics) {
			return renderapi.ExecutionManifest{}, unavailable("fresh probe rejected the resource")
		}
		observed, err = cloneProbeManifest(observed)
		if err != nil {
			return renderapi.ExecutionManifest{}, unavailable("fresh probe returned an invalid manifest")
		}
		if observed.Resource.AssetID != planned.AssetID ||
			observed.Resource.ContentHash != planned.ContentHash || observed.Resource.SizeBytes <= 0 {
			return renderapi.ExecutionManifest{}, unavailable("fresh probe resource identity mismatch")
		}
		if !probeSupportsAssetKind(observed, planned.Kind) {
			return renderapi.ExecutionManifest{}, unavailable("fresh probe media kind mismatch")
		}
		probeFingerprint, err := probe.Fingerprint(observed)
		if err != nil {
			return renderapi.ExecutionManifest{}, unavailable("fresh probe fingerprint failed")
		}
		resources = append(resources, renderapi.MaterializedResource{
			AssetID: planned.AssetID, Kind: planned.Kind, ContentHash: planned.ContentHash,
			SizeBytes: observed.Resource.SizeBytes, LocatorScheme: renderapi.LocatorFile,
			Locator: locator, ProbeFingerprint: probeFingerprint,
		})
	}
	if err := ctx.Err(); err != nil {
		return renderapi.ExecutionManifest{}, err
	}

	frozen, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: resolver.expectedCompilerBuild, EvaluatorBuild: resolver.expectedEvaluatorBuild,
		Resources: resources, Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: runtimeFingerprint, DecoderProfile: hardcutDecoderProfile,
			EncoderProfile: hardcutEncoderProfile, ColorProfile: hardcutColorProfile,
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: hardcutReferenceProfile, Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: request.DeterministicSeed,
	})
	if err != nil {
		return renderapi.ExecutionManifest{}, unavailable("host could not freeze the execution manifest")
	}
	if err := ctx.Err(); err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	return frozen, nil
}

func exactResourceBindings(planned []videoedit.ResourceRef, supplied []PrepareResource) (map[videoedit.ID]string, error) {
	plannedIDs := make(map[videoedit.ID]struct{}, len(planned))
	for _, resource := range planned {
		if !validSHA256(resource.ContentHash) {
			return nil, unavailable("planned resource has no canonical immutable content hash")
		}
		plannedIDs[resource.AssetID] = struct{}{}
	}
	bindings := make(map[videoedit.ID]string, len(supplied))
	for _, resource := range supplied {
		if _, duplicate := bindings[resource.AssetID]; duplicate {
			return nil, unavailable("resource mapping contains a duplicate asset")
		}
		if _, exists := plannedIDs[resource.AssetID]; !exists {
			return nil, unavailable("resource mapping contains an unknown asset")
		}
		bindings[resource.AssetID] = resource.Locator
	}
	if len(bindings) != len(planned) {
		return nil, unavailable("resource mapping is incomplete")
	}
	for assetID := range plannedIDs {
		if _, exists := bindings[assetID]; !exists {
			return nil, unavailable("resource mapping is incomplete")
		}
	}
	return bindings, nil
}

func cloneRenderPlan(plan videoedit.RenderPlan) (videoedit.RenderPlan, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return videoedit.RenderPlan{}, err
	}
	return videoedit.DecodeRenderPlan(raw)
}

func probeSupportsAssetKind(manifest probe.ProbeManifest, kind videoedit.AssetKind) bool {
	wanted := probe.StreamKindVideo
	if kind == videoedit.AssetKindAudio {
		wanted = probe.StreamKindAudio
	} else if kind != videoedit.AssetKindVideo {
		return false
	}
	for _, stream := range manifest.Streams {
		if stream.Kind == wanted {
			return true
		}
	}
	return false
}
