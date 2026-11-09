package reconstructv2

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/nativevideo"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

// MixedBundle is an explicitly versioned execution handle. Close takes an
// exclusive lifecycle lease, waits for active renders, closes every decoder
// source and permanently rejects new renders. Returned surfaces/PCM own their
// storage independently. Do not copy a MixedBundle after first use.
type MixedBundle struct {
	mu                          sync.RWMutex
	closed                      bool
	closeErr                    error
	key                         prepared.KeyV2
	bridgeJSON                  string
	timeline                    documentv2.ID
	frames, samples             videoedit.TickRange
	frameClock                  videoedit.FrameClock
	sampleClock                 videoedit.SampleClock
	visualEnabled, audioEnabled bool
	visual                      *referencevisual.ProgramV3
	audio                       *audiograph.DomainProgram
	wav                         *wavsource.Provider
	sources                     []*videodecode.Source
	pcmLimits                   pcm16le.Limits
	maxBlockSamples             int64
}

func (f *MixedFactory) Reconstruct(ctx context.Context, input prepared.EnvironmentV2) (result *MixedBundle, err error) {
	var sources []*videodecode.Source
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			for _, s := range sources {
				err = errors.Join(err, closeVideo(s))
			}
			result = nil
		}
	}()
	admitted, err := f.preflight(ctx, input)
	if err != nil {
		return nil, err
	}
	env := admitted.environment
	authorized, err := f.static.config.Authority.AuthorizeEnvironment(ctx, env.Document, env.Plan, env.Manifest)
	if err != nil {
		return nil, err
	}
	if !authorized.Valid() {
		return nil, ErrResource
	}
	cfg := f.static.config
	var pngs []referencevisual.Binding
	var fonts []textlayer.FontBinding
	var wavs []wavsource.Binding
	var videos []nativevideo.Binding
	actual := map[documentv2.ID]*resourceidentityv2.Inspected{}
	observations := map[documentv2.ID]openedVideoResource{}
	for _, claim := range env.Manifest.Resources {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if claim.Kind == documentv2.AssetKindVideo {
			v := f.video
			opened, e := openVideoResource(ctx, cfg.Resources, env.Plan.DocumentHash, claim, videoResourceConfig{probe: v.Probe, decoder: v.Decoder, scan: v.Scan, maximumBytes: cfg.Limits.MaxResourceBytes, scratch: v.Scratch})
			if e != nil {
				return nil, e
			}
			sources = append(sources, opened.source)
			if opened.manifest.Probe != v.Probe.Identity() {
				return nil, ErrResource
			}
			observations[claim.AssetID] = opened
			videos = append(videos, nativevideo.Binding{DocumentHash: env.Plan.DocumentHash, AssetID: claim.AssetID, Source: opened.source})
			continue
		}
		binding, encoded, e := snapshotResource(ctx, cfg.Resources, env.Plan.DocumentHash, claim, cfg.Limits.MaxResourceBytes)
		if e != nil {
			return nil, e
		}
		var inspected *resourceidentityv2.Inspected
		switch binding.Kind {
		case documentv2.AssetKindImage:
			inspected, err = resourceidentityv2.InspectPNG(ctx, binding, encoded, cfg.PNGInspection)
		case documentv2.AssetKindFont:
			inspected, err = resourceidentityv2.InspectFont(ctx, binding, encoded, cfg.FontInspection)
		case documentv2.AssetKindAudio:
			inspected, err = resourceidentityv2.InspectAudio(ctx, binding, encoded, cfg.WAVInspection)
		default:
			return nil, ErrUnsupported
		}
		if err != nil {
			return nil, err
		}
		if err = resourceidentityv2.Check(ctx, binding, claim, inspected); err != nil {
			return nil, err
		}
		actual[binding.AssetID] = inspected
		switch binding.Kind {
		case documentv2.AssetKindImage:
			pngs = append(pngs, referencevisual.Binding{AssetID: binding.AssetID, EncodedPNG: encoded})
		case documentv2.AssetKindAudio:
			wavs = append(wavs, wavsource.Binding{DocumentHash: env.Plan.DocumentHash, AssetID: binding.AssetID, EncodedWAV: encoded})
		case documentv2.AssetKindFont:
			font, e := buildFontBinding(ctx, env.Plan.DocumentHash, binding, inspected, defaultFontBuildLimits())
			if e != nil {
				return nil, e
			}
			fonts = append(fonts, font)
		}
	}
	visual, d, err := referencevisual.CompileDomainV3(ctx, env.Document, admitted.scopes.visual, pngs, fonts, videos, f.visual)
	if err != nil || visual == nil || documentv2.HasErrorDiagnostics(d) {
		return nil, errors.Join(ErrUnsupported, err)
	}
	wav, err := wavsource.Compile(ctx, env.Document, wavs, cfg.WAV)
	if err != nil {
		return nil, err
	}
	audio, d := audiograph.CompileDomainV2(ctx, env.Document, admitted.scopes.audio, audiograph.CompileOptions{Offered: fixedMixedAudioOffered(), Bindings: wav.MediaBindings(), Limits: cfg.Audio})
	if audio == nil || documentv2.HasErrorDiagnostics(d) {
		return nil, ErrUnsupported
	}
	if visual.DocumentHash() != env.Plan.DocumentHash || audio.DocumentHash() != env.Plan.DocumentHash || wav.Manifest().DocumentHash != env.Plan.DocumentHash {
		return nil, ErrResource
	}
	if err = checkMixedObservations(ctx, visual.SourceManifest(), wav, actual, observations); err != nil {
		return nil, err
	}
	bridge, err := buildMixedBridge(ctx, env, f, admitted.scopes, visual, audio, wav, observations)
	if err != nil {
		return nil, err
	}
	encoded, err := canonicalMixedBridge(ctx, bridge)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &MixedBundle{key: admitted.verified.Key(), bridgeJSON: string(encoded), timeline: env.Plan.TimelineID, frames: env.Plan.FrameRange, samples: env.Plan.SampleRange, frameClock: env.Manifest.Output.FrameClock, sampleClock: env.Manifest.Output.SampleClock, visualEnabled: env.Manifest.Visual.Enabled, audioEnabled: env.Manifest.Audio.Enabled, visual: visual, audio: audio, wav: wav, sources: sources, pcmLimits: cfg.PCM, maxBlockSamples: cfg.Audio.MaxBlockSamples}, nil
}

func closeVideo(source *videodecode.Source) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
	}()
	return source.Close()
}
func (b *MixedBundle) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return b.closeErr
	}
	b.closed = true
	for _, s := range b.sources {
		b.closeErr = errors.Join(b.closeErr, closeVideo(s))
	}
	b.sources = nil
	return b.closeErr
}
func (b *MixedBundle) validLocked() bool {
	return b != nil && !b.closed && b.bridgeJSON != "" && b.visual != nil && b.audio != nil && b.wav != nil
}
func (b *MixedBundle) Valid() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.validLocked()
}
func (b *MixedBundle) Key() prepared.KeyV2 {
	if b == nil {
		return prepared.KeyV2{}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.validLocked() {
		return prepared.KeyV2{}
	}
	return b.key
}
func (b *MixedBundle) Bridge(ctx context.Context) (result MixedBridge, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = MixedBridge{}
		}
	}()
	if ctx == nil {
		return result, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if b == nil {
		return result, ErrResource
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.validLocked() {
		return result, ErrResource
	}
	if err = json.Unmarshal([]byte(b.bridgeJSON), &result); err != nil {
		return MixedBridge{}, err
	}
	return result, ctx.Err()
}

func checkMixedObservations(ctx context.Context, manifest referencevisual.SourceManifestV3, wav *wavsource.Provider, actual map[documentv2.ID]*resourceidentityv2.Inspected, videos map[documentv2.ID]openedVideoResource) error {
	seen := map[documentv2.ID]bool{}
	for _, asset := range manifest.Static.NativePNG.Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := actual[asset.AssetID]
		if a == nil || seen[asset.AssetID] {
			return ErrResource
		}
		r, e := a.Receipt()
		if e != nil || r.PNG == nil || r.Resource.ContentHash != asset.EncodedContentHash || r.PNG.PixelHash != asset.PixelHash || r.PNG.Width != uint32(asset.Width) || r.PNG.Height != uint32(asset.Height) {
			return ErrResource
		}
		seen[asset.AssetID] = true
	}
	for _, asset := range wav.Manifest().Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := actual[asset.AssetID]
		if a == nil || seen[asset.AssetID] {
			return ErrResource
		}
		p, e := a.Probe()
		if e != nil {
			return e
		}
		fp, e := probe.Fingerprint(p)
		if e != nil || fp != asset.ManifestFingerprint || p.Resource.ContentHash != asset.EncodedContentHash {
			return ErrResource
		}
		seen[asset.AssetID] = true
	}
	if text := manifest.Static.Text; text != nil {
		for _, asset := range text.Assets {
			if err := ctx.Err(); err != nil {
				return err
			}
			a := actual[asset.FontAssetID]
			if a == nil {
				return ErrResource
			}
			binding, e := a.Binding()
			if e != nil || binding.Kind != documentv2.AssetKindFont || binding.ContentHash != asset.FontContentHash {
				return ErrResource
			}
			seen[asset.FontAssetID] = true
		}
	}
	if len(seen) != len(actual) {
		return ErrResource
	}
	if len(manifest.Video) != len(videos) {
		return ErrResource
	}
	for _, asset := range manifest.Video {
		opened, ok := videos[asset.AssetID]
		if !ok {
			return ErrResource
		}
		identity, e := opened.source.Identity()
		if e != nil || identity != asset.Identity {
			return ErrResource
		}
	}
	return ctx.Err()
}
