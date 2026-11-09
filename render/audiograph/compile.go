package audiograph

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

const (
	// ProfileV1 is the authoritative graph topology/mixing contract declared by Document v2.
	ProfileV1 = documentv2.AudioGraphProfileV1
	// EvaluatorProfileV1 identifies this pure exact evaluator contract.
	EvaluatorProfileV1 = documentv2.EvaluatorAudioGraphV1
	// SampleDomainProfileV1 states that outputs are exact rational nominal-full-scale stereo samples
	// before any PCM quantization, clipping, dithering, codec, or container processing.
	SampleDomainProfileV1 = "cutly.audio.sample.exact-rational-stereo.v1"
	// SourceSampleDomainProfileV1 identifies the decoder-ordered, pre-matrix provider domain. It
	// is deliberately not called stereo: an explicitly authored matrix may bind more channels.
	SourceSampleDomainProfileV1 = "cutly.audio.sample.exact-rational-ordered-channels.v1"
	// SourceProfileV1 identifies the complete request/echo/receipt contract at the provider edge.
	SourceProfileV1 = "cutly.audio.source.bound-exact-sample.v1"
	// SourceContractVersion versions the concrete request and response structs independently from
	// the graph/DSP profile.
	SourceContractVersion = 1
)

const (
	CodeBindingMissing       = "AUDIOGRAPH.V1.BINDING_MISSING"
	CodeBindingInvalid       = "AUDIOGRAPH.V1.BINDING_INVALID"
	CodeBindingDuplicate     = "AUDIOGRAPH.V1.BINDING_DUPLICATE"
	CodeProbeInvalid         = "AUDIOGRAPH.V1.PROBE_INVALID"
	CodeResourceMismatch     = "AUDIOGRAPH.V1.RESOURCE_MISMATCH"
	CodeStreamInvalid        = "AUDIOGRAPH.V1.STREAM_INVALID"
	CodeChannelOrderInvalid  = "AUDIOGRAPH.V1.CHANNEL_ORDER_INVALID"
	CodeChannelLayoutInvalid = "AUDIOGRAPH.V1.CHANNEL_LAYOUT_INVALID"
	CodeCompileLimit         = "AUDIOGRAPH.V1.COMPILE_LIMIT"
	CodeExtensionUnsupported = "AUDIOGRAPH.V1.EXTENSION_UNSUPPORTED"
	CodeSourceProfileInvalid = "AUDIOGRAPH.V1.SOURCE_PROFILE_INVALID"
)

const (
	HardMaxBindings                        = 65536
	HardMaxProbeStreamsPerBinding          = 4096
	HardMaxTimelines                       = 65536
	HardMaxAudioTracksPerTimeline          = 65536
	HardMaxClipsPerTrack                   = 1_000_000
	HardMaxBuses                           = 65536
	HardMaxBlockSamples              int64 = 4_000_000
	HardMaxGraphExactBits                  = 4096
	HardMaxCapabilityEntries               = 65536
	DefaultMaxDocumentNodes                = 262144
	HardMaxDocumentNodes                   = 4 << 20
	DefaultMaxTracksPerTimeline            = 4096
	HardMaxTracksPerTimeline               = 65536
	DefaultMaxCanonicalDocumentBytes       = 128 << 20
	HardMaxCanonicalDocumentBytes          = 1 << 30
	DefaultMaxCanonicalManifestBytes       = 4 << 20
	HardMaxCanonicalManifestBytes          = 64 << 20
)

// Limits bounds compile-time graph size, complete typed Document/ProbeManifest inputs and block
// evaluation memory. The byte limits are enforced conservatively before canonical encoding and
// exactly on canonical bytes afterward. Zero fields select defaults; negative signed fields and
// values above the package hard maxima are invalid.
type Limits struct {
	MaxBindings               int    `json:"max_bindings"`
	MaxProbeStreamsPerBinding int    `json:"max_probe_streams_per_binding"`
	MaxTimelines              int    `json:"max_timelines"`
	MaxAudioTracksPerTimeline int    `json:"max_audio_tracks_per_timeline"`
	MaxClipsPerTrack          int    `json:"max_clips_per_track"`
	MaxBuses                  int    `json:"max_buses"`
	MaxBlockSamples           int64  `json:"max_block_samples"`
	MaxExactBits              int    `json:"max_exact_bits"`
	MaxDocumentNodes          int    `json:"max_document_nodes"`
	MaxTracksPerTimeline      int    `json:"max_tracks_per_timeline"`
	MaxCanonicalDocumentBytes uint64 `json:"max_canonical_document_bytes"`
	MaxCanonicalManifestBytes uint64 `json:"max_canonical_manifest_bytes"`
	MaxCapabilityEntries      int    `json:"max_capability_entries"`
}

// DefaultLimits returns a fresh copy of the reference evaluator limits.
func DefaultLimits() Limits {
	return Limits{
		MaxBindings: 4096, MaxProbeStreamsPerBinding: 256, MaxTimelines: 1024, MaxAudioTracksPerTimeline: 4096,
		MaxClipsPerTrack: 1_000_000, MaxBuses: 4096, MaxBlockSamples: 1_000_000,
		MaxExactBits: 4096, MaxDocumentNodes: DefaultMaxDocumentNodes,
		MaxTracksPerTimeline:      DefaultMaxTracksPerTimeline,
		MaxCanonicalDocumentBytes: DefaultMaxCanonicalDocumentBytes,
		MaxCanonicalManifestBytes: DefaultMaxCanonicalManifestBytes,
		MaxCapabilityEntries:      4096,
	}
}

// MediaBinding proves which immutable resource, probed audio stream, decoder channel order and
// versioned provider algorithms one asset will use. DecoderChannels and the three profile IDs are
// semantic evidence, not hints: Compile freezes them exactly and never infers a host default.
type MediaBinding struct {
	AssetID          documentv2.ID       `json:"asset_id"`
	Manifest         probe.ProbeManifest `json:"manifest"`
	StreamIndex      int                 `json:"stream_index"`
	DecoderChannels  []string            `json:"decoder_channels"`
	DecodeProfile    string              `json:"decode_profile"`
	SelectionProfile string              `json:"selection_profile"`
	RetimeProfile    string              `json:"retime_profile"`
}

// CompileOptions supplies backend semantic capabilities and immutable media evidence.
type CompileOptions struct {
	Offered  videoedit.BackendCapabilities `json:"offered"`
	Bindings []MediaBinding                `json:"bindings"`
	Limits   Limits                        `json:"limits"`
}

type compiledBinding struct {
	assetID          documentv2.ID
	assetIndex       int
	assetKind        documentv2.AssetKind
	manifest         probe.ProbeManifest
	manifestHash     string
	streamIndex      int
	audio            probe.AudioStream
	decoderChannels  []string
	decodeProfile    string
	selectionProfile string
	retimeProfile    string
}

type compiledClip struct {
	id         documentv2.ID
	assetID    documentv2.ID
	trackID    documentv2.ID
	trackIndex int
	clipIndex  int
	start      *big.Rat
	end        *big.Rat
	usesMatrix bool
}

type compiledTrack struct {
	id       documentv2.ID
	index    int
	busIndex int
	clips    []compiledClip
}

type compiledTimeline struct {
	id     documentv2.ID
	index  int
	tracks []compiledTrack
}

type compiledBus struct {
	id          documentv2.ID
	index       int
	parentIndex int
	children    []int
	gain        Exact
	pan         Exact
	limiter     *Exact
}

// Program is an immutable, capability-checked graph snapshot safe for concurrent arbitrary seeks.
// Its maps and slices are never exposed; every provider request and evaluation result owns its
// mutable slices.
type Program struct {
	documentHash string
	evaluator    clipEvaluator
	bindings     map[documentv2.ID]compiledBinding
	timelines    map[documentv2.ID]compiledTimeline
	buses        []compiledBus
	masterBus    int
	limits       Limits
}

// Compile validates Document v2 capabilities, freezes a canonical snapshot, binds all enabled
// audio clips to probed immutable media, and compiles stable track/bus indexes. Any error
// diagnostic returns a nil Program, preventing partially preflighted rendering.
func Compile(document documentv2.Document, options CompileOptions) (*Program, []documentv2.Diagnostic) {
	limits, limitDiagnostics := normalizeLimits(options.Limits)
	if documentv2.HasErrorDiagnostics(limitDiagnostics) {
		return nil, limitDiagnostics
	}
	if rawLimitDiagnostics := preflightRawLimits(document, options.Offered, options.Bindings, limits); documentv2.HasErrorDiagnostics(rawLimitDiagnostics) {
		return nil, rawLimitDiagnostics
	}

	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(CodeBindingInvalid, "/", fmt.Sprintf("freeze document: %v", err))}
	}
	if uint64(len(canonical)) > limits.MaxCanonicalDocumentBytes {
		return nil, []documentv2.Diagnostic{compileDiagnostic(CodeCompileLimit, "/",
			fmt.Sprintf("canonical document bytes %d exceed limit %d", len(canonical), limits.MaxCanonicalDocumentBytes))}
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, []documentv2.Diagnostic{compileDiagnostic(CodeBindingInvalid, "/", fmt.Sprintf("decode frozen document: %v", err))}
	}
	evaluationProgram, diagnostics := evaluator.Compile(frozen, options.Offered)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}

	if len(frozen.Timelines) > limits.MaxTimelines {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/timelines", fmt.Sprintf("timeline count %d exceeds limit %d", len(frozen.Timelines), limits.MaxTimelines)))
	}
	if len(frozen.AudioBuses) > limits.MaxBuses {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/audio_buses", fmt.Sprintf("bus count %d exceeds limit %d", len(frozen.AudioBuses), limits.MaxBuses)))
	}
	if len(options.Bindings) > limits.MaxBindings {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/bindings", fmt.Sprintf("binding count %d exceeds limit %d", len(options.Bindings), limits.MaxBindings)))
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}

	assets := make(map[documentv2.ID]documentv2.Asset, len(frozen.Assets))
	assetIndexes := make(map[documentv2.ID]int, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		assets[asset.ID] = asset
		assetIndexes[asset.ID] = index
	}
	bindings := compileBindings(options.Bindings, assets, limits, &diagnostics)
	for assetID, binding := range bindings {
		binding.assetIndex = assetIndexes[assetID]
		bindings[assetID] = binding
	}

	buses, masterBus := compileBuses(frozen.AudioBuses)
	busIndexes := make(map[documentv2.ID]int, len(buses))
	for index := range buses {
		busIndexes[buses[index].id] = index
	}
	for index := range buses {
		if buses[index].parentIndex >= 0 {
			parent := buses[index].parentIndex
			buses[parent].children = append(buses[parent].children, index)
		}
	}

	timelines := make(map[documentv2.ID]compiledTimeline, len(frozen.Timelines))
	for timelineIndex, timeline := range frozen.Timelines {
		compiled := compiledTimeline{id: timeline.ID, index: timelineIndex}
		audioTrackCount := 0
		for trackIndex, track := range timeline.Tracks {
			if track.Kind != documentv2.TrackKindAudio {
				continue
			}
			audioTrackCount++
			trackPath := fmt.Sprintf("/timelines/%d/tracks/%d", timelineIndex, trackIndex)
			if len(track.Clips) > limits.MaxClipsPerTrack {
				diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, trackPath+"/clips", fmt.Sprintf("clip count %d exceeds limit %d", len(track.Clips), limits.MaxClipsPerTrack)))
				continue
			}
			busIndex := -1
			if len(buses) != 0 {
				busIndex = busIndexes[track.AudioBusID]
			}
			compiledTrack := compiledTrack{id: track.ID, index: trackIndex, busIndex: busIndex}
			for clipIndex, clip := range track.Clips {
				if clip.Disabled {
					continue
				}
				clipPath := fmt.Sprintf("%s/clips/%d", trackPath, clipIndex)
				if clip.PropertySet != nil {
					for extensionIndex, extension := range clip.PropertySet.Extensions {
						if extension.AffectsRendering {
							diagnostics = append(diagnostics, compileDiagnostic(CodeExtensionUnsupported,
								fmt.Sprintf("%s/property_set/extensions/%d", clipPath, extensionIndex),
								fmt.Sprintf("audio graph has no consumer for rendering extension %q", extension.Namespace)))
						}
					}
				}
				binding, exists := bindings[clip.AssetID]
				if !exists {
					diagnostics = append(diagnostics, compileDiagnostic(CodeBindingMissing, clipPath+"/asset_id", fmt.Sprintf("audio asset %q has no media binding", clip.AssetID)))
					continue
				}
				asset := assets[clip.AssetID]
				validateClipBinding(asset, clip, binding, clipPath, &diagnostics)
				end, endErr := clip.TimelineRange.End()
				if endErr != nil {
					diagnostics = append(diagnostics, compileDiagnostic(CodeBindingInvalid, clipPath+"/timeline_range", endErr.Error()))
					continue
				}
				startBig := new(big.Rat).SetFrac64(clip.TimelineRange.Start.Value, clip.TimelineRange.Start.Rate)
				endBig := new(big.Rat).SetFrac64(end.Value, end.Rate)
				compiledTrack.clips = append(compiledTrack.clips, compiledClip{
					id: clip.ID, assetID: clip.AssetID, trackID: track.ID,
					trackIndex: trackIndex, clipIndex: clipIndex, start: startBig, end: endBig,
					usesMatrix: clip.PropertySet != nil && clip.PropertySet.AudioChannelMatrix != nil,
				})
			}
			compiled.tracks = append(compiled.tracks, compiledTrack)
		}
		if audioTrackCount > limits.MaxAudioTracksPerTimeline {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/timelines/%d/tracks", timelineIndex),
				fmt.Sprintf("audio track count %d exceeds limit %d", audioTrackCount, limits.MaxAudioTracksPerTimeline)))
		}
		timelines[timeline.ID] = compiled
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}

	return &Program{
		documentHash: evaluationProgram.DocumentHash(), evaluator: evaluationProgram,
		bindings: bindings, timelines: timelines, buses: buses, masterBus: masterBus, limits: limits,
	}, diagnostics
}

func normalizeLimits(value Limits) (Limits, []documentv2.Diagnostic) {
	defaults := DefaultLimits()
	fields := []struct {
		path         string
		value        *int
		defaultValue int
	}{
		{"/limits/max_bindings", &value.MaxBindings, defaults.MaxBindings},
		{"/limits/max_probe_streams_per_binding", &value.MaxProbeStreamsPerBinding, defaults.MaxProbeStreamsPerBinding},
		{"/limits/max_timelines", &value.MaxTimelines, defaults.MaxTimelines},
		{"/limits/max_audio_tracks_per_timeline", &value.MaxAudioTracksPerTimeline, defaults.MaxAudioTracksPerTimeline},
		{"/limits/max_clips_per_track", &value.MaxClipsPerTrack, defaults.MaxClipsPerTrack},
		{"/limits/max_buses", &value.MaxBuses, defaults.MaxBuses},
		{"/limits/max_exact_bits", &value.MaxExactBits, defaults.MaxExactBits},
		{"/limits/max_document_nodes", &value.MaxDocumentNodes, defaults.MaxDocumentNodes},
		{"/limits/max_tracks_per_timeline", &value.MaxTracksPerTimeline, defaults.MaxTracksPerTimeline},
		{"/limits/max_capability_entries", &value.MaxCapabilityEntries, defaults.MaxCapabilityEntries},
	}
	var diagnostics []documentv2.Diagnostic
	for _, field := range fields {
		if *field.value < 0 {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, field.path, "limit must not be negative"))
		} else if *field.value == 0 {
			*field.value = field.defaultValue
		}
	}
	if value.MaxBlockSamples < 0 {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/limits/max_block_samples", "limit must not be negative"))
	} else if value.MaxBlockSamples == 0 {
		value.MaxBlockSamples = defaults.MaxBlockSamples
	}
	if value.MaxDocumentNodes > HardMaxDocumentNodes || value.MaxTracksPerTimeline > HardMaxTracksPerTimeline {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/limits", "document structural limit exceeds hard maximum"))
	}
	if value.MaxBindings > HardMaxBindings || value.MaxProbeStreamsPerBinding > HardMaxProbeStreamsPerBinding ||
		value.MaxTimelines > HardMaxTimelines || value.MaxAudioTracksPerTimeline > HardMaxAudioTracksPerTimeline ||
		value.MaxClipsPerTrack > HardMaxClipsPerTrack || value.MaxBuses > HardMaxBuses ||
		value.MaxBlockSamples > HardMaxBlockSamples || value.MaxExactBits > HardMaxGraphExactBits ||
		value.MaxCapabilityEntries > HardMaxCapabilityEntries {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/limits", "graph limit exceeds hard maximum"))
	}
	if value.MaxCanonicalDocumentBytes == 0 {
		value.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
	} else if value.MaxCanonicalDocumentBytes > HardMaxCanonicalDocumentBytes {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/limits/max_canonical_document_bytes", "limit exceeds hard maximum"))
	}
	if value.MaxCanonicalManifestBytes == 0 {
		value.MaxCanonicalManifestBytes = defaults.MaxCanonicalManifestBytes
	} else if value.MaxCanonicalManifestBytes > HardMaxCanonicalManifestBytes {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/limits/max_canonical_manifest_bytes", "limit exceeds hard maximum"))
	}
	return value, diagnostics
}

func preflightRawLimits(document documentv2.Document, offered videoedit.BackendCapabilities, bindings []MediaBinding, limits Limits) []documentv2.Diagnostic {
	return preflightRawLimitsContext(nil, document, offered, bindings, limits)
}

func preflightRawLimitsContext(ctx context.Context, document documentv2.Document, offered videoedit.BackendCapabilities, bindings []MediaBinding, limits Limits) []documentv2.Diagnostic {
	var diagnostics []documentv2.Diagnostic
	if path, err := preflightValueLimitsContext(ctx, document, uint64(limits.MaxDocumentNodes), limits.MaxCanonicalDocumentBytes); err != nil {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, path, err.Error()))
		return diagnostics
	}
	capabilityEntries := uint64(len(offered.TrackKinds))
	for _, count := range []int{len(offered.AssetKinds), len(offered.Features)} {
		if uint64(count) > math.MaxUint64-capabilityEntries {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/offered", "capability entry count overflows"))
			return diagnostics
		}
		capabilityEntries += uint64(count)
	}
	if capabilityEntries > uint64(limits.MaxCapabilityEntries) {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/offered",
			fmt.Sprintf("capability entry count %d exceeds limit %d", capabilityEntries, limits.MaxCapabilityEntries)))
		return diagnostics
	}
	if _, err := preflightValueLimitsContext(ctx, offered, uint64(limits.MaxCapabilityEntries)+16, limits.MaxCanonicalManifestBytes); err != nil {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/offered", err.Error()))
		return diagnostics
	}
	if len(document.Timelines) > limits.MaxTimelines {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/timelines", fmt.Sprintf("timeline count %d exceeds limit %d", len(document.Timelines), limits.MaxTimelines)))
	}
	if len(document.AudioBuses) > limits.MaxBuses {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/audio_buses", fmt.Sprintf("bus count %d exceeds limit %d", len(document.AudioBuses), limits.MaxBuses)))
	}
	if len(bindings) > limits.MaxBindings {
		diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, "/bindings", fmt.Sprintf("binding count %d exceeds limit %d", len(bindings), limits.MaxBindings)))
	}
	for bindingIndex, binding := range bindings {
		if ctx != nil && ctx.Err() != nil {
			return []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		streamLimit := uint64(limits.MaxProbeStreamsPerBinding)
		if streamLimit > (math.MaxUint64-64)/16 {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/bindings/%d/manifest", bindingIndex), "manifest node limit overflows"))
			continue
		}
		manifestNodeLimit := streamLimit*16 + 64
		if _, err := preflightValueLimitsContext(ctx, binding.Manifest, manifestNodeLimit, limits.MaxCanonicalManifestBytes); err != nil {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/bindings/%d/manifest", bindingIndex), err.Error()))
		}
		if len(binding.Manifest.Streams) > limits.MaxProbeStreamsPerBinding {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/bindings/%d/manifest/streams", bindingIndex),
				fmt.Sprintf("probe stream count %d exceeds limit %d", len(binding.Manifest.Streams), limits.MaxProbeStreamsPerBinding)))
		}
		if len(binding.DecoderChannels) > documentv2.AudioChannelMatrixMaxInputs {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/bindings/%d/decoder_channels", bindingIndex),
				fmt.Sprintf("decoder channel count %d exceeds limit %d", len(binding.DecoderChannels), documentv2.AudioChannelMatrixMaxInputs)))
		}
	}
	for timelineIndex, timeline := range document.Timelines {
		if ctx != nil && ctx.Err() != nil {
			return []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		if len(timeline.Tracks) > limits.MaxTracksPerTimeline {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/timelines/%d/tracks", timelineIndex),
				fmt.Sprintf("track count %d exceeds limit %d", len(timeline.Tracks), limits.MaxTracksPerTimeline)))
			continue
		}
		audioTracks := 0
		for trackIndex, track := range timeline.Tracks {
			if ctx != nil && ctx.Err() != nil {
				return []documentv2.Diagnostic{domainCanceledDiagnostic()}
			}
			if len(track.Clips) > limits.MaxClipsPerTrack {
				diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit,
					fmt.Sprintf("/timelines/%d/tracks/%d/clips", timelineIndex, trackIndex),
					fmt.Sprintf("clip count %d exceeds limit %d", len(track.Clips), limits.MaxClipsPerTrack)))
			}
			if track.Kind != documentv2.TrackKindAudio {
				continue
			}
			audioTracks++
		}
		if audioTracks > limits.MaxAudioTracksPerTimeline {
			diagnostics = append(diagnostics, compileDiagnostic(CodeCompileLimit, fmt.Sprintf("/timelines/%d/tracks", timelineIndex),
				fmt.Sprintf("audio track count %d exceeds limit %d", audioTracks, limits.MaxAudioTracksPerTimeline)))
		}
	}
	return diagnostics
}

func compileBindings(values []MediaBinding, assets map[documentv2.ID]documentv2.Asset, limits Limits, diagnostics *[]documentv2.Diagnostic) map[documentv2.ID]compiledBinding {
	return compileBindingsContext(nil, values, assets, limits, diagnostics)
}

func compileBindingsContext(ctx context.Context, values []MediaBinding, assets map[documentv2.ID]documentv2.Asset, limits Limits, diagnostics *[]documentv2.Diagnostic) map[documentv2.ID]compiledBinding {
	result := make(map[documentv2.ID]compiledBinding, len(values))
	seenBindings := make(map[documentv2.ID]struct{}, len(values))
	for index, value := range values {
		if ctx != nil && ctx.Err() != nil {
			*diagnostics = append(*diagnostics, domainCanceledDiagnostic())
			return nil
		}
		path := fmt.Sprintf("/bindings/%d", index)
		if strings.TrimSpace(string(value.AssetID)) == "" {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeBindingInvalid, path+"/asset_id", "asset_id is required"))
			continue
		}
		if _, duplicate := seenBindings[value.AssetID]; duplicate {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeBindingDuplicate, path+"/asset_id", fmt.Sprintf("asset %q already has a binding", value.AssetID)))
			continue
		}
		seenBindings[value.AssetID] = struct{}{}
		if len(value.Manifest.Streams) > limits.MaxProbeStreamsPerBinding || len(value.DecoderChannels) > documentv2.AudioChannelMatrixMaxInputs {
			continue // preflightRawLimits already emitted the stable bounded-input diagnostic.
		}
		asset, exists := assets[value.AssetID]
		if !exists {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeBindingInvalid, path+"/asset_id", fmt.Sprintf("asset %q does not exist", value.AssetID)))
			continue
		}
		if asset.Kind != documentv2.AssetKindAudio && asset.Kind != documentv2.AssetKindVideo {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeBindingInvalid, path+"/asset_id",
				fmt.Sprintf("asset %q kind %q cannot provide an audio stream", value.AssetID, asset.Kind)))
			continue
		}
		profiles := []struct {
			path  string
			value string
		}{
			{path: path + "/decode_profile", value: value.DecodeProfile},
			{path: path + "/selection_profile", value: value.SelectionProfile},
			{path: path + "/retime_profile", value: value.RetimeProfile},
		}
		profilesValid := true
		for _, profile := range profiles {
			if !canonicalProfileToken(profile.value) {
				*diagnostics = append(*diagnostics, compileDiagnostic(CodeSourceProfileInvalid, profile.path,
					"source profile must be a canonical versioned identifier"))
				profilesValid = false
			}
		}
		if !profilesValid {
			continue
		}
		manifest, valid := freezeManifest(value.Manifest, path+"/manifest", limits.MaxCanonicalManifestBytes, diagnostics)
		if !valid {
			continue
		}
		if manifest.Resource.AssetID != value.AssetID {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeResourceMismatch, path+"/manifest/resource/asset_id", "probe resource asset_id does not match binding"))
		}
		if asset.ContentHash == "" {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeResourceMismatch, path+"/manifest/resource/content_hash", "audio rendering requires a content-addressed Document asset"))
		} else if manifest.Resource.ContentHash != asset.ContentHash {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeResourceMismatch, path+"/manifest/resource/content_hash", "probe resource hash does not match Document asset"))
		}
		var selected *probe.Stream
		for streamIndex := range manifest.Streams {
			if ctx != nil && ctx.Err() != nil {
				*diagnostics = append(*diagnostics, domainCanceledDiagnostic())
				return nil
			}
			if manifest.Streams[streamIndex].Index == value.StreamIndex {
				selected = &manifest.Streams[streamIndex]
				break
			}
		}
		if selected == nil || selected.Kind != probe.StreamKindAudio || selected.Audio == nil {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeStreamInvalid, path+"/stream_index", fmt.Sprintf("stream %d is not a probed audio stream", value.StreamIndex)))
			continue
		}
		if len(value.DecoderChannels) != selected.Audio.Channels {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelOrderInvalid, path+"/decoder_channels", fmt.Sprintf("decoder channel count %d does not match probed count %d", len(value.DecoderChannels), selected.Audio.Channels)))
		}
		if len(value.DecoderChannels) == 0 || len(value.DecoderChannels) > documentv2.AudioChannelMatrixMaxInputs {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelOrderInvalid, path+"/decoder_channels", fmt.Sprintf("decoder channels must contain 1..%d entries", documentv2.AudioChannelMatrixMaxInputs)))
		}
		seen := make(map[string]struct{}, len(value.DecoderChannels))
		for channelIndex, channel := range value.DecoderChannels {
			channelPath := fmt.Sprintf("%s/decoder_channels/%d", path, channelIndex)
			if !canonicalChannelToken(channel) {
				*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelOrderInvalid, channelPath, "channel must be a canonical uppercase token"))
			}
			if _, duplicate := seen[channel]; duplicate {
				*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelOrderInvalid, channelPath, fmt.Sprintf("channel %q is duplicated", channel)))
			}
			seen[channel] = struct{}{}
		}
		fingerprint, fingerprintErr := probe.Fingerprint(manifest)
		if fingerprintErr != nil {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeProbeInvalid, path+"/manifest", fingerprintErr.Error()))
			continue
		}
		result[value.AssetID] = compiledBinding{
			assetID: value.AssetID, assetKind: asset.Kind, manifest: manifest, manifestHash: fingerprint,
			streamIndex: value.StreamIndex, audio: *selected.Audio,
			decoderChannels: append([]string(nil), value.DecoderChannels...),
			decodeProfile:   value.DecodeProfile, selectionProfile: value.SelectionProfile,
			retimeProfile: value.RetimeProfile,
		}
	}
	return result
}

func freezeManifest(value probe.ProbeManifest, path string, maxCanonicalBytes uint64, diagnostics *[]documentv2.Diagnostic) (probe.ProbeManifest, bool) {
	probeDiagnostics := probe.Validate(value)
	if probe.HasErrorDiagnostics(probeDiagnostics) {
		for _, diagnostic := range probeDiagnostics {
			if diagnostic.Severity != probe.SeverityError {
				continue
			}
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeProbeInvalid, path+diagnostic.Path, diagnostic.Code+": "+diagnostic.Message))
		}
		return probe.ProbeManifest{}, false
	}
	canonical, err := videoedit.CanonicalJSON(value)
	if err != nil {
		*diagnostics = append(*diagnostics, compileDiagnostic(CodeProbeInvalid, path, err.Error()))
		return probe.ProbeManifest{}, false
	}
	if uint64(len(canonical)) > maxCanonicalBytes {
		*diagnostics = append(*diagnostics, compileDiagnostic(CodeCompileLimit, path,
			fmt.Sprintf("canonical manifest bytes %d exceed limit %d", len(canonical), maxCanonicalBytes)))
		return probe.ProbeManifest{}, false
	}
	frozen, decodedDiagnostics, err := probe.Decode(canonical)
	if err != nil || probe.HasErrorDiagnostics(decodedDiagnostics) {
		*diagnostics = append(*diagnostics, compileDiagnostic(CodeProbeInvalid, path, fmt.Sprintf("freeze manifest: %v", err)))
		return probe.ProbeManifest{}, false
	}
	return frozen, true
}

func validateClipBinding(asset documentv2.Asset, clip documentv2.Clip, binding compiledBinding, path string, diagnostics *[]documentv2.Diagnostic) {
	if binding.assetID != asset.ID {
		*diagnostics = append(*diagnostics, compileDiagnostic(CodeResourceMismatch, path+"/asset_id", "compiled media binding does not match clip asset"))
		return
	}
	if clip.PropertySet != nil && clip.PropertySet.AudioChannelMatrix != nil {
		matrix := clip.PropertySet.AudioChannelMatrix
		if matrix.SourceLayout != binding.audio.ChannelLayout {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelLayoutInvalid, path+"/property_set/audio_channel_matrix/source_layout",
				fmt.Sprintf("authored layout %q does not exactly match probe layout %q", matrix.SourceLayout, binding.audio.ChannelLayout)))
		}
		if len(matrix.SourceChannels) != binding.audio.Channels || !equalStrings(matrix.SourceChannels, binding.decoderChannels) {
			*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelOrderInvalid, path+"/property_set/audio_channel_matrix/source_channels",
				"authored source channel order does not exactly match the selected decoder order"))
		}
		return
	}
	validMono := binding.audio.Channels == 1 && binding.audio.ChannelLayout == "mono" && equalStrings(binding.decoderChannels, []string{"FC"})
	validStereo := binding.audio.Channels == 2 && binding.audio.ChannelLayout == "stereo" && equalStrings(binding.decoderChannels, []string{"FL", "FR"})
	if !validMono && !validStereo {
		*diagnostics = append(*diagnostics, compileDiagnostic(CodeChannelLayoutInvalid, path+"/property_set",
			"without an explicit channel matrix, input must be exact mono/FC or stereo/FL,FR"))
	}
}

func compileBuses(values []documentv2.AudioBus) ([]compiledBus, int) {
	if len(values) == 0 {
		return nil, -1
	}
	indexes := make(map[documentv2.ID]int, len(values))
	for index, bus := range values {
		indexes[bus.ID] = index
	}
	result := make([]compiledBus, len(values))
	master := -1
	for index, bus := range values {
		gain, _ := exactFromDocument(bus.Properties.GainLinear)
		pan, _ := exactFromDocument(bus.Properties.Pan)
		compiled := compiledBus{id: bus.ID, index: index, parentIndex: -1, gain: gain, pan: pan}
		if bus.ParentBusID != "" {
			compiled.parentIndex = indexes[bus.ParentBusID]
		}
		if bus.Properties.SamplePeakLimiter != nil {
			ceiling, _ := exactFromDocument(bus.Properties.SamplePeakLimiter.CeilingLinear)
			compiled.limiter = &ceiling
		}
		if bus.Kind == documentv2.AudioBusMaster {
			master = index
		}
		result[index] = compiled
	}
	return result, master
}

func canonicalChannelToken(value string) bool {
	if len(value) == 0 || len(value) > 16 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func canonicalProfileToken(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' || character == ':') {
			return false
		}
	}
	version := strings.LastIndex(value, ".v")
	if version <= 0 || version+2 >= len(value) {
		return false
	}
	for _, character := range value[version+2:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func compileDiagnostic(code, path, message string) documentv2.Diagnostic {
	return documentv2.Diagnostic{Code: code, Severity: documentv2.SeverityError, Path: path, Message: message}
}

// DocumentHash is the frozen canonical document identity.
func (program *Program) DocumentHash() string {
	if program == nil {
		return ""
	}
	return program.documentHash
}

// TimelineIDs returns a sorted defensive list of compiled timelines.
func (program *Program) TimelineIDs() []documentv2.ID {
	if program == nil {
		return nil
	}
	result := make([]documentv2.ID, 0, len(program.timelines))
	for id := range program.timelines {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}
