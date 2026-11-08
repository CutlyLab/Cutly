package reconstructv2

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

// Bundle is an immutable, host-reconstructed execution handle. It cannot be
// decoded from wire receipts. No raw native program/provider escapes its methods.
// Each render uses the original composition clocks and the admitted plan range.
// It is safe for concurrent reads; callers must not overwrite the Bundle itself.
// Native programs and byte snapshots are never persisted by this type.
type Bundle struct {
	key             prepared.KeyV2
	bridgeJSON      string
	timeline        documentv2.ID
	frames          videoedit.TickRange
	samples         videoedit.TickRange
	frameClock      videoedit.FrameClock
	sampleClock     videoedit.SampleClock
	visualEnabled   bool
	audioEnabled    bool
	visual          *referencevisual.Program
	audio           *audiograph.DomainProgram
	wav             *wavsource.Provider
	pcmLimits       pcm16le.Limits
	maxBlockSamples int64
}

// Reconstruct verifies an owned environment and current host pins, rejects
// unsupported scopes/configurations before any resource callback, then authorizes
// and re-inspects private byte snapshots. No partially reconstructed handle is
// returned if either domain or any resource fails. Authorization callbacks remain
// host-owned; success here grants neither a publication path nor process launch.
func (factory *Factory) Reconstruct(ctx context.Context, input prepared.EnvironmentV2) (result *Bundle, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = nil
		}
	}()
	admitted, err := factory.preflight(ctx, input)
	if err != nil {
		return nil, err
	}
	env := admitted.environment
	authorized, err := factory.config.Authority.AuthorizeEnvironment(ctx, env.Document, env.Plan, env.Manifest)
	if err != nil {
		return nil, err
	}
	if !authorized.Valid() {
		return nil, ErrResource
	}

	pngBindings := make([]referencevisual.Binding, 0)
	fontBindings := make([]textlayer.FontBinding, 0)
	wavBindings := make([]wavsource.Binding, 0)
	inspected := make(map[documentv2.ID]*resourceidentityv2.Inspected, len(env.Manifest.Resources))
	for _, claim := range env.Manifest.Resources {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		binding, encoded, e := snapshotResource(ctx, factory.config.Resources, env.Plan.DocumentHash, claim, factory.config.Limits.MaxResourceBytes)
		if e != nil {
			return nil, e
		}
		var actual *resourceidentityv2.Inspected
		switch binding.Kind {
		case documentv2.AssetKindImage:
			actual, err = resourceidentityv2.InspectPNG(ctx, binding, encoded, factory.config.PNGInspection)
		case documentv2.AssetKindFont:
			actual, err = resourceidentityv2.InspectFont(ctx, binding, encoded, factory.config.FontInspection)
		case documentv2.AssetKindAudio:
			actual, err = resourceidentityv2.InspectAudio(ctx, binding, encoded, factory.config.WAVInspection)
		default:
			return nil, ErrUnsupported
		}
		if err != nil {
			return nil, err
		}
		if err = resourceidentityv2.Check(ctx, binding, claim, actual); err != nil {
			return nil, err
		}
		inspected[binding.AssetID] = actual
		switch binding.Kind {
		case documentv2.AssetKindImage:
			pngBindings = append(pngBindings, referencevisual.Binding{AssetID: binding.AssetID, EncodedPNG: encoded})
		case documentv2.AssetKindAudio:
			wavBindings = append(wavBindings, wavsource.Binding{DocumentHash: env.Plan.DocumentHash, AssetID: binding.AssetID, EncodedWAV: encoded})
		case documentv2.AssetKindFont:
			font, e := buildFontBinding(ctx, env.Plan.DocumentHash, binding, actual, defaultFontBuildLimits())
			if e != nil {
				return nil, e
			}
			fontBindings = append(fontBindings, font)
		}
	}
	// Source compilers borrow only the private snapshots above. Their decoded
	// immutable storage is owned independently of the source and returned handle.
	visual, diagnostics, err := referencevisual.CompileDomainV2(ctx, env.Document, admitted.scopes.visual, pngBindings, fontBindings, factory.config.Visual)
	if err != nil || visual == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, errors.Join(ErrUnsupported, err)
	}
	wav, err := wavsource.Compile(ctx, env.Document, wavBindings, factory.config.WAV)
	if err != nil {
		return nil, err
	}
	audio, diagnostics := audiograph.CompileDomain(ctx, env.Document, admitted.scopes.audio, audiograph.CompileOptions{Offered: fixedAudioOffered(), Bindings: wav.MediaBindings(), Limits: factory.config.Audio})
	if audio == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, ErrUnsupported
	}
	if visual.DocumentHash() != env.Plan.DocumentHash || audio.DocumentHash() != env.Plan.DocumentHash || wav.Manifest().DocumentHash != env.Plan.DocumentHash {
		return nil, ErrResource
	}
	if err = checkNativeObservations(ctx, visual, wav, inspected); err != nil {
		return nil, err
	}
	bridge, err := buildBridge(ctx, env, admitted.configHash, visual, audio, wav, factory.config.PCM)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalBridge(ctx, bridge)
	if err != nil {
		return nil, err
	}
	result = &Bundle{key: admitted.verified.Key(), bridgeJSON: string(canonical), timeline: env.Plan.TimelineID, frames: env.Plan.FrameRange, samples: env.Plan.SampleRange, frameClock: env.Manifest.Output.FrameClock, sampleClock: env.Manifest.Output.SampleClock, visualEnabled: env.Manifest.Visual.Enabled, audioEnabled: env.Manifest.Audio.Enabled, visual: visual, audio: audio, wav: wav, pcmLimits: factory.config.PCM, maxBlockSamples: factory.config.Audio.MaxBlockSamples}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Check actual source compiler observations against the earlier inspections, not
// just each other's claims. Fonts already cross-bind the native lineage adapter.
func checkNativeObservations(ctx context.Context, visual *referencevisual.Program, wav *wavsource.Provider, actual map[documentv2.ID]*resourceidentityv2.Inspected) error {
	seen := make(map[documentv2.ID]bool)
	for _, asset := range visual.SourceManifest().NativePNG.Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := actual[asset.AssetID]
		if observation == nil || seen[asset.AssetID] {
			return ErrResource
		}
		r, err := observation.Receipt()
		if err != nil || r.PNG == nil || r.Resource.ContentHash != asset.EncodedContentHash || r.PNG.PixelHash != asset.PixelHash || r.PNG.Width != uint32(asset.Width) || r.PNG.Height != uint32(asset.Height) {
			return ErrResource
		}
		seen[asset.AssetID] = true
	}
	for _, asset := range wav.Manifest().Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation := actual[asset.AssetID]
		if observation == nil || seen[asset.AssetID] {
			return ErrResource
		}
		p, err := observation.Probe()
		if err != nil {
			return err
		}
		fp, err := probe.Fingerprint(p)
		if err != nil || fp != asset.ManifestFingerprint || p.Resource.ContentHash != asset.EncodedContentHash {
			return ErrResource
		}
		seen[asset.AssetID] = true
	}
	if text := visual.SourceManifest().Text; text != nil {
		for _, asset := range text.Assets {
			if err := ctx.Err(); err != nil {
				return err
			}
			observation := actual[asset.FontAssetID]
			if observation == nil {
				return ErrResource
			}
			binding, err := observation.Binding()
			if err != nil || binding.Kind != documentv2.AssetKindFont || binding.ContentHash != asset.FontContentHash {
				return ErrResource
			}
			seen[asset.FontAssetID] = true
		}
	}
	for id := range actual {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !seen[id] {
			return ErrResource
		}
	}
	return ctx.Err()
}

func (bundle *Bundle) Valid() bool {
	return bundle != nil && bundle.bridgeJSON != "" && bundle.visual != nil && bundle.audio != nil && bundle.wav != nil
}

func (bundle *Bundle) Key() prepared.KeyV2 {
	if !bundle.Valid() {
		return prepared.KeyV2{}
	}
	return bundle.key
}

// Bridge returns a fresh receipt copy, never authority to reconstruct a Bundle.
func (bundle *Bundle) Bridge(ctx context.Context) (result Bridge, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = Bridge{}
		}
	}()
	if ctx == nil {
		return result, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if !bundle.Valid() {
		return result, ErrResource
	}
	if err = json.Unmarshal([]byte(bundle.bridgeJSON), &result); err != nil {
		return Bridge{}, err
	}
	if err = ctx.Err(); err != nil {
		return Bridge{}, err
	}
	return result, nil
}
