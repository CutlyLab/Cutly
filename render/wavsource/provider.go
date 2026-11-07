package wavsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/audiograph"
)

type Binding struct {
	DocumentHash string        `json:"document_hash"`
	AssetID      documentv2.ID `json:"asset_id"`
	// EncodedWAV is borrowed and must remain immutable until Compile returns. The provider retains
	// only decoder-owned PCM storage.
	EncodedWAV []byte `json:"-"`
}

type indexedBinding struct {
	index   int
	binding Binding
}

type AssetReceipt struct {
	AssetID                 documentv2.ID `json:"asset_id"`
	AssetIndex              int           `json:"asset_index"`
	EncodedContentHash      string        `json:"encoded_content_hash"`
	ContentHashProfile      string        `json:"content_hash_profile"`
	SampleHash              string        `json:"sample_hash"`
	SampleHashProfile       string        `json:"sample_hash_profile"`
	DecodedSampleDomain     string        `json:"decoded_sample_domain"`
	SampleRate              uint32        `json:"sample_rate"`
	Channels                uint16        `json:"channels"`
	ChannelLayout           string        `json:"channel_layout"`
	DecoderChannels         []string      `json:"decoder_channels"`
	FrameCount              uint64        `json:"frame_count"`
	DecodeProfile           string        `json:"decode_profile"`
	SelectionProfile        string        `json:"selection_profile"`
	RetimeProfile           string        `json:"retime_profile"`
	SourceProfile           string        `json:"source_profile"`
	SampleDomain            string        `json:"sample_domain"`
	ProbeImplementation     string        `json:"probe_implementation"`
	ProbeBuildFingerprint   string        `json:"probe_build_fingerprint"`
	ProbeOptionsFingerprint string        `json:"probe_options_fingerprint"`
	ManifestFingerprint     string        `json:"manifest_fingerprint"`
}

type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       string         `json:"profile"`
	DocumentHash  string         `json:"document_hash"`
	Assets        []AssetReceipt `json:"assets,omitempty"`
}

type clipKey struct {
	timeline documentv2.ID
	track    documentv2.ID
	clip     documentv2.ID
}

type allowedClip struct {
	timelineIndex int
	trackIndex    int
	clipIndex     int
	assetID       documentv2.ID
	assetIndex    int
	timelineRange documentv2.TimeRange
	sourceRange   documentv2.TimeRange
}

type assetEntry struct {
	index       int
	pcm         *wavdecode.PCM16
	decoded     wavdecode.Result
	probe       probe.ProbeManifest
	fingerprint string
}

// Provider is immutable after Compile and safe for concurrent arbitrary seeks.
type Provider struct {
	documentHash string
	limits       Limits
	assets       map[documentv2.ID]assetEntry
	clips        map[clipKey]allowedClip
	bindings     []audiograph.MediaBinding
	receipts     []AssetReceipt
}

var _ audiograph.SourceProvider = (*Provider)(nil)

func Compile(ctx context.Context, document documentv2.Document, bindings []Binding, config Config) (*Provider, error) {
	if ctx == nil {
		return nil, sourceError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return nil, err
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := preflightDocument(ctx, document, normalized.Limits); err != nil {
		return nil, err
	}
	if err := preflightBindings(bindings, normalized.Limits); err != nil {
		return nil, err
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if uint64(len(canonical)) > normalized.Limits.MaxCanonicalDocumentBytes {
		return nil, sourceError(CodeResourceLimit, "document/canonical_bytes", ErrResourceLimit)
	}
	if err := contextFailure(ctx, "document"); err != nil {
		return nil, err
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, sourceError(CodeDocumentInvalid, "document", errors.Join(ErrDocumentInvalid, err))
	}
	if err := contextFailure(ctx, "document/freeze"); err != nil {
		return nil, err
	}
	documentHash, err := hashBytes(ctx, canonical, "document/hash")
	if err != nil {
		return nil, err
	}
	canonical = nil

	assets := make(map[documentv2.ID]documentv2.Asset, len(frozen.Assets))
	assetIndexes := make(map[documentv2.ID]int, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "document/assets"); err != nil {
				return nil, err
			}
		}
		assets[asset.ID] = asset
		assetIndexes[asset.ID] = index
	}
	required := make(map[documentv2.ID]struct{})
	clips := make(map[clipKey]allowedClip)
	var scopeVisits uint64
	for timelineIndex, timeline := range frozen.Timelines {
		if scopeVisits&1023 == 0 {
			if err := contextFailure(ctx, "document/audio_clip_scopes"); err != nil {
				return nil, err
			}
		}
		scopeVisits++
		for trackIndex, track := range timeline.Tracks {
			if scopeVisits&1023 == 0 {
				if err := contextFailure(ctx, "document/audio_clip_scopes"); err != nil {
					return nil, err
				}
			}
			scopeVisits++
			if track.Kind != documentv2.TrackKindAudio {
				continue
			}
			for clipIndex, clip := range track.Clips {
				if scopeVisits&1023 == 0 {
					if err := contextFailure(ctx, "document/audio_clip_scopes"); err != nil {
						return nil, err
					}
				}
				scopeVisits++
				if clip.Disabled {
					continue
				}
				if len(clips) >= normalized.Limits.MaxClipScopes {
					return nil, sourceError(CodeResourceLimit, "document/enabled_audio_clip_scopes", ErrResourceLimit)
				}
				asset, exists := assets[clip.AssetID]
				path := fmt.Sprintf("document/timelines/%d/tracks/%d/clips/%d/asset_id", timelineIndex, trackIndex, clipIndex)
				if !exists || asset.Kind != documentv2.AssetKindAudio {
					return nil, sourceError(CodeDocumentInvalid, path, ErrDocumentInvalid)
				}
				if !canonicalSHA256(asset.ContentHash) || asset.Duration == nil {
					return nil, sourceError(CodeDocumentInvalid, path, ErrDocumentInvalid)
				}
				if _, exists := required[asset.ID]; !exists {
					if len(required) >= normalized.Limits.MaxBindings {
						return nil, sourceError(CodeResourceLimit, "document/referenced_audio_assets", ErrResourceLimit)
					}
					required[asset.ID] = struct{}{}
				}
				key := clipKey{timeline: timeline.ID, track: track.ID, clip: clip.ID}
				clips[key] = allowedClip{
					timelineIndex: timelineIndex, trackIndex: trackIndex, clipIndex: clipIndex,
					assetID: asset.ID, assetIndex: assetIndexes[asset.ID],
					timelineRange: clip.TimelineRange, sourceRange: clip.SourceRange,
				}
			}
		}
	}
	bindingByID := make(map[documentv2.ID]indexedBinding, len(bindings))
	for index, binding := range bindings {
		if index&1023 == 0 {
			if err := contextFailure(ctx, "bindings"); err != nil {
				return nil, err
			}
		}
		path := fmt.Sprintf("bindings/%d", index)
		if binding.DocumentHash != documentHash {
			return nil, sourceError(CodeDocumentMismatch, path+"/document_hash", ErrDocumentMismatch)
		}
		if strings.TrimSpace(string(binding.AssetID)) == "" {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		if _, duplicate := bindingByID[binding.AssetID]; duplicate {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		asset, exists := assets[binding.AssetID]
		if !exists || asset.Kind != documentv2.AssetKindAudio {
			return nil, sourceError(CodeBindingInvalid, path+"/asset_id", ErrBindingInvalid)
		}
		if _, needed := required[binding.AssetID]; !needed {
			return nil, sourceError(CodeBindingExtra, path, ErrBindingExtra)
		}
		bindingByID[binding.AssetID] = indexedBinding{index: index, binding: binding}
	}
	assetIDs := make([]documentv2.ID, 0, len(required))
	for assetID := range required {
		if _, exists := bindingByID[assetID]; !exists {
			return nil, sourceError(CodeBindingMissing, "bindings", ErrBindingMissing)
		}
		assetIDs = append(assetIDs, assetID)
	}
	sort.Slice(assetIDs, func(i, j int) bool { return assetIDs[i] < assetIDs[j] })

	probeBuild, probeOptions, err := probeFingerprints()
	if err != nil {
		return nil, sourceError(CodeConfigInvalid, "probe_identity", errors.Join(ErrConfigInvalid, err))
	}
	entries := make(map[documentv2.ID]assetEntry, len(assetIDs))
	mediaBindings := make([]audiograph.MediaBinding, 0, len(assetIDs))
	receipts := make([]AssetReceipt, 0, len(assetIDs))
	var retained uint64
	for _, assetID := range assetIDs {
		selected := bindingByID[assetID]
		bindingIndex := selected.index
		if err := contextFailure(ctx, fmt.Sprintf("bindings/%d", bindingIndex)); err != nil {
			return nil, err
		}
		binding := selected.binding
		decoded, decodeErr := wavdecode.Decode(ctx, binding.EncodedWAV, normalized.Limits.WAV)
		if decodeErr != nil {
			if errors.Is(decodeErr, wavdecode.ErrCanceled) || errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, context.DeadlineExceeded) {
				return nil, sourceError(CodeCanceled, fmt.Sprintf("bindings/%d/decode", bindingIndex), errors.Join(ErrCanceled, decodeErr))
			}
			if errors.Is(decodeErr, wavdecode.ErrResourceLimit) {
				return nil, sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/decode", bindingIndex), errors.Join(ErrResourceLimit, decodeErr))
			}
			return nil, sourceError(CodeDecodeFailed, fmt.Sprintf("bindings/%d/decode", bindingIndex), errors.Join(ErrDecodeFailed, decodeErr))
		}
		if err := contextFailure(ctx, fmt.Sprintf("bindings/%d/decode", bindingIndex)); err != nil {
			return nil, err
		}
		asset := assets[assetID]
		if decoded.EncodedContentHash != asset.ContentHash {
			return nil, sourceError(CodeContentMismatch, fmt.Sprintf("bindings/%d/content_hash", bindingIndex), ErrContentMismatch)
		}
		if decoded.PCM == nil || decoded.FrameCount == 0 || decoded.FrameCount > math.MaxInt64 {
			return nil, sourceError(CodeSourceUnavailable, fmt.Sprintf("bindings/%d/frames", bindingIndex), ErrSourceUnavailable)
		}
		decodedDuration := documentv2.RationalTime{Value: int64(decoded.FrameCount), Rate: int64(decoded.SampleRate)}
		if asset.Duration == nil || !equalTime(*asset.Duration, decodedDuration) {
			return nil, sourceError(CodeDurationMismatch, fmt.Sprintf("bindings/%d/duration", bindingIndex), ErrDurationMismatch)
		}
		if decoded.DataBytes > math.MaxUint64-retained || retained+decoded.DataBytes > normalized.Limits.MaxRetainedBytes {
			return nil, sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/retained_bytes", bindingIndex), ErrResourceLimit)
		}
		retained += decoded.DataBytes
		manifest, manifestErr := makeProbeManifest(assetID, len(binding.EncodedWAV), decoded, probeBuild, probeOptions)
		if manifestErr != nil {
			return nil, sourceError(CodeDocumentInvalid, fmt.Sprintf("bindings/%d/probe", bindingIndex), errors.Join(ErrDocumentInvalid, manifestErr))
		}
		fingerprint, fingerprintErr := probe.Fingerprint(manifest)
		if fingerprintErr != nil {
			return nil, sourceError(CodeDocumentInvalid, fmt.Sprintf("bindings/%d/probe", bindingIndex), errors.Join(ErrDocumentInvalid, fingerprintErr))
		}
		decoderChannels := decoded.PCM.ChannelOrder()
		mediaBinding := audiograph.MediaBinding{
			AssetID: assetID, Manifest: manifest, StreamIndex: 0,
			DecoderChannels: decoderChannels, DecodeProfile: wavdecode.ProfileV1,
			SelectionProfile: SelectionProfileV1, RetimeProfile: RetimeProfileV1,
		}
		entry := assetEntry{index: assetIndexes[assetID], pcm: decoded.PCM, decoded: decoded, probe: manifest, fingerprint: fingerprint}
		entries[assetID] = entry
		mediaBindings = append(mediaBindings, cloneMediaBinding(mediaBinding))
		receipts = append(receipts, AssetReceipt{
			AssetID: assetID, AssetIndex: entry.index,
			EncodedContentHash: decoded.EncodedContentHash, ContentHashProfile: decoded.ContentHashProfile,
			SampleHash: decoded.SampleHash, SampleHashProfile: decoded.SampleHashProfile,
			DecodedSampleDomain: decoded.SampleDomain,
			SampleRate:          decoded.SampleRate, Channels: decoded.Channels, ChannelLayout: decoded.ChannelLayout,
			DecoderChannels: append([]string(nil), decoderChannels...), FrameCount: decoded.FrameCount,
			DecodeProfile: wavdecode.ProfileV1, SelectionProfile: SelectionProfileV1, RetimeProfile: RetimeProfileV1,
			SourceProfile: audiograph.SourceProfileV1, SampleDomain: audiograph.SourceSampleDomainProfileV1,
			ProbeImplementation: manifest.Probe.Implementation, ProbeBuildFingerprint: manifest.Probe.BuildFingerprint,
			ProbeOptionsFingerprint: manifest.Probe.OptionsFingerprint,
			ManifestFingerprint:     fingerprint,
		})
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return nil, err
	}
	return &Provider{
		documentHash: documentHash, limits: normalized.Limits,
		assets: entries, clips: clips, bindings: mediaBindings, receipts: receipts,
	}, nil
}

func (provider *Provider) Sample(ctx context.Context, request audiograph.SourceRequest) (audiograph.SourceSample, error) {
	if provider == nil {
		return audiograph.SourceSample{}, sourceError(CodeSourceUnavailable, "provider", ErrSourceUnavailable)
	}
	if ctx == nil {
		return audiograph.SourceSample{}, sourceError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "request"); err != nil {
		return audiograph.SourceSample{}, err
	}
	entry, clip, err := provider.validateRequest(request)
	if err != nil {
		return audiograph.SourceSample{}, err
	}
	if err := validateMappedTimes(request, clip); err != nil {
		return audiograph.SourceSample{}, err
	}
	channels, err := samplePCM(ctx, entry, request.SourceTime, provider.limits.MaxExactBits)
	if err != nil {
		return audiograph.SourceSample{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return audiograph.SourceSample{}, err
	}
	return audiograph.SourceSample{
		ContractVersion: audiograph.SourceContractVersion,
		Request:         cloneRequest(request),
		SampleDomain:    audiograph.SourceSampleDomainProfileV1,
		Channels:        channels,
	}, nil
}

func (provider *Provider) validateRequest(request audiograph.SourceRequest) (assetEntry, allowedClip, error) {
	if request.ContractVersion != audiograph.SourceContractVersion || request.DocumentHash != provider.documentHash ||
		request.SourceProfile != audiograph.SourceProfileV1 || request.SampleDomain != audiograph.SourceSampleDomainProfileV1 ||
		request.DecodeProfile != wavdecode.ProfileV1 || request.SelectionProfile != SelectionProfileV1 || request.RetimeProfile != RetimeProfileV1 ||
		request.AssetKind != documentv2.AssetKindAudio || request.StreamIndex != 0 ||
		request.TimelineIndex < 0 || request.TrackIndex < 0 || request.ClipIndex < 0 || request.AssetIndex < 0 ||
		request.TimelineTime.Validate() != nil || request.LocalTime.Validate() != nil || request.SourceTime.Validate() != nil ||
		request.ResourceSizeBytes <= 0 || !canonicalSHA256(request.ResourceContentHash) || !canonicalSHA256(request.ManifestFingerprint) {
		return assetEntry{}, allowedClip{}, sourceError(CodeRequestInvalid, "request", ErrRequestInvalid)
	}
	clip, exists := provider.clips[clipKey{timeline: request.TimelineID, track: request.TrackID, clip: request.ClipID}]
	if !exists || clip.timelineIndex != request.TimelineIndex || clip.trackIndex != request.TrackIndex || clip.clipIndex != request.ClipIndex ||
		clip.assetID != request.AssetID || clip.assetIndex != request.AssetIndex {
		return assetEntry{}, allowedClip{}, sourceError(CodeRequestMismatch, "request/scope", ErrRequestMismatch)
	}
	entry, exists := provider.assets[request.AssetID]
	if !exists || entry.pcm == nil || entry.index != request.AssetIndex {
		return assetEntry{}, allowedClip{}, sourceError(CodeSourceUnavailable, "request/asset", ErrSourceUnavailable)
	}
	if request.ManifestFingerprint != entry.fingerprint || request.ResourceContentHash != entry.decoded.EncodedContentHash ||
		request.ResourceSizeBytes != entry.probe.Resource.SizeBytes || request.SampleRate != int64(entry.decoded.SampleRate) ||
		request.ChannelLayout != entry.decoded.ChannelLayout || !equalStrings(request.DecoderChannels, entry.pcm.ChannelOrder()) {
		return assetEntry{}, allowedClip{}, sourceError(CodeRequestMismatch, "request/bindings", ErrRequestMismatch)
	}
	return entry, clip, nil
}

func validateMappedTimes(request audiograph.SourceRequest, clip allowedClip) error {
	timeline := timeRat(request.TimelineTime)
	start := timeRat(clip.timelineRange.Start)
	end := new(big.Rat).Add(start, timeRat(clip.timelineRange.Duration))
	if timeline.Cmp(start) < 0 || timeline.Cmp(end) >= 0 {
		return sourceError(CodeRequestMismatch, "request/timeline_time", ErrRequestMismatch)
	}
	local := new(big.Rat).Sub(timeline, start)
	if local.Cmp(timeRat(request.LocalTime)) != 0 {
		return sourceError(CodeRequestMismatch, "request/local_time", ErrRequestMismatch)
	}
	sourceOffset := new(big.Rat).Mul(local, new(big.Rat).Quo(timeRat(clip.sourceRange.Duration), timeRat(clip.timelineRange.Duration)))
	expectedSource := new(big.Rat).Add(timeRat(clip.sourceRange.Start), sourceOffset)
	if expectedSource.Cmp(timeRat(request.SourceTime)) != 0 {
		return sourceError(CodeRequestMismatch, "request/source_time", ErrRequestMismatch)
	}
	return nil
}

func samplePCM(ctx context.Context, entry assetEntry, sourceTime documentv2.RationalTime, maxExactBits int) ([]audiograph.Exact, error) {
	if sourceTime.Value < 0 || entry.decoded.FrameCount == 0 {
		return nil, sourceError(CodeSourceUnavailable, "request/source_time", ErrSourceUnavailable)
	}
	position := new(big.Rat).Mul(timeRat(sourceTime), new(big.Rat).SetInt64(int64(entry.decoded.SampleRate)))
	frameCount := new(big.Int).SetUint64(entry.decoded.FrameCount)
	if position.Sign() < 0 || position.Cmp(new(big.Rat).SetInt(frameCount)) >= 0 {
		return nil, sourceError(CodeSourceUnavailable, "request/source_time", ErrSourceUnavailable)
	}
	base, remainder := new(big.Int), new(big.Int)
	base.QuoRem(position.Num(), position.Denom(), remainder)
	if !base.IsUint64() {
		return nil, sourceError(CodeSourceUnavailable, "request/source_time", ErrSourceUnavailable)
	}
	baseFrame := base.Uint64()
	terminal := baseFrame+1 >= entry.decoded.FrameCount
	channels := make([]audiograph.Exact, int(entry.decoded.Channels))
	for channel := range channels {
		if err := contextFailure(ctx, fmt.Sprintf("sample/channels/%d", channel)); err != nil {
			return nil, err
		}
		left, sampleErr := entry.pcm.Sample(baseFrame, channel)
		if sampleErr != nil {
			return nil, sourceError(CodeSourceUnavailable, fmt.Sprintf("sample/channels/%d", channel), errors.Join(ErrSourceUnavailable, sampleErr))
		}
		var numerator, denominator big.Int
		if remainder.Sign() == 0 || terminal {
			numerator.SetInt64(int64(left))
			denominator.SetInt64(wavdecode.NominalFullScaleDenominator)
		} else {
			right, rightErr := entry.pcm.Sample(baseFrame+1, channel)
			if rightErr != nil {
				return nil, sourceError(CodeSourceUnavailable, fmt.Sprintf("sample/channels/%d", channel), errors.Join(ErrSourceUnavailable, rightErr))
			}
			var leftWeight, leftTerm, rightTerm big.Int
			leftWeight.Sub(position.Denom(), remainder)
			leftTerm.Mul(big.NewInt(int64(left)), &leftWeight)
			rightTerm.Mul(big.NewInt(int64(right)), remainder)
			numerator.Add(&leftTerm, &rightTerm)
			denominator.Mul(big.NewInt(wavdecode.NominalFullScaleDenominator), position.Denom())
		}
		exact, exactErr := audiograph.NewExact(&numerator, &denominator)
		if exactErr != nil {
			return nil, sourceError(CodeRequestInvalid, fmt.Sprintf("sample/channels/%d", channel), errors.Join(ErrRequestInvalid, exactErr))
		}
		if exact.Numerator().BitLen()+exact.Denominator().BitLen() > maxExactBits {
			return nil, sourceError(CodeExactLimit, fmt.Sprintf("sample/channels/%d", channel), ErrExactLimit)
		}
		channels[channel] = exact
	}
	return channels, nil
}

func makeProbeManifest(assetID documentv2.ID, encodedBytes int, decoded wavdecode.Result, buildFingerprint, optionsFingerprint string) (probe.ProbeManifest, error) {
	durationPTS := int64(decoded.FrameCount)
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: assetID, ContentHash: decoded.EncodedContentHash, SizeBytes: int64(encodedBytes)},
		Probe:         probe.ProbeIdentity{Implementation: ProbeImplementationV1, BuildFingerprint: buildFingerprint, OptionsFingerprint: optionsFingerprint},
		Container: probe.ContainerInfo{
			FormatName: "wav", StartTime: documentv2.RationalTime{Value: 0, Rate: 1},
			BitRate: int64(decoded.SampleRate) * int64(decoded.Channels) * 16, Seekable: true,
		},
		Duration: documentv2.RationalTime{Value: int64(decoded.FrameCount), Rate: int64(decoded.SampleRate)},
		Streams: []probe.Stream{{
			Index: 0, Kind: probe.StreamKindAudio, Codec: "pcm_s16le",
			TimeBase: documentv2.RationalTime{Value: 1, Rate: int64(decoded.SampleRate)},
			StartPTS: 0, DurationPTS: &durationPTS,
			Audio: &probe.AudioStream{
				SampleRate: int64(decoded.SampleRate), Channels: int(decoded.Channels),
				ChannelLayout: decoded.ChannelLayout, SampleFormat: "s16", BitsPerSample: 16,
				EncoderDelaySamples: 0, PaddingSamples: 0,
			},
		}},
	}
	if diagnostics := probe.Validate(manifest); probe.HasErrorDiagnostics(diagnostics) {
		return probe.ProbeManifest{}, fmt.Errorf("invalid generated ProbeManifest: %+v", diagnostics)
	}
	return manifest, nil
}

func probeFingerprints() (string, string, error) {
	build, err := canonicalDigest(struct {
		SourceDescriptor  ProfileDescriptor           `json:"source_descriptor"`
		DecoderDescriptor wavdecode.ProfileDescriptor `json:"decoder_descriptor"`
	}{SourceDescriptor: Descriptor(), DecoderDescriptor: wavdecode.Descriptor()})
	if err != nil {
		return "", "", err
	}
	options, err := canonicalDigest(struct {
		Profile       string `json:"profile"`
		StreamIndex   int    `json:"stream_index"`
		ChannelPolicy string `json:"channel_policy"`
	}{Profile: wavdecode.ProfileV1, StreamIndex: 0, ChannelPolicy: "mono-fc-or-stereo-fl-fr"})
	if err != nil {
		return "", "", err
	}
	return build, options, nil
}

func canonicalDigest(value any) (string, error) {
	canonical, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func hashBytes(ctx context.Context, value []byte, path string) (string, error) {
	digest := sha256.New()
	const stride = 32 << 10
	for offset := 0; offset < len(value); offset += stride {
		if err := contextFailure(ctx, path); err != nil {
			return "", err
		}
		end := offset + stride
		if end > len(value) {
			end = len(value)
		}
		_, _ = digest.Write(value[offset:end])
	}
	if err := contextFailure(ctx, path); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func (provider *Provider) Manifest() Manifest {
	if provider == nil {
		return Manifest{}
	}
	assets := make([]AssetReceipt, len(provider.receipts))
	for index, receipt := range provider.receipts {
		assets[index] = receipt
		assets[index].DecoderChannels = append([]string(nil), receipt.DecoderChannels...)
	}
	return Manifest{SchemaVersion: 1, Profile: ProfileV1, DocumentHash: provider.documentHash, Assets: assets}
}

func (provider *Provider) MediaBindings() []audiograph.MediaBinding {
	if provider == nil {
		return nil
	}
	result := make([]audiograph.MediaBinding, len(provider.bindings))
	for index, binding := range provider.bindings {
		result[index] = cloneMediaBinding(binding)
	}
	return result
}

func cloneMediaBinding(value audiograph.MediaBinding) audiograph.MediaBinding {
	value.DecoderChannels = append([]string(nil), value.DecoderChannels...)
	value.Manifest = cloneProbeManifest(value.Manifest)
	return value
}

func cloneProbeManifest(value probe.ProbeManifest) probe.ProbeManifest {
	result := value
	result.Streams = make([]probe.Stream, len(value.Streams))
	for index, stream := range value.Streams {
		result.Streams[index] = stream
		if stream.DurationPTS != nil {
			copyValue := *stream.DurationPTS
			result.Streams[index].DurationPTS = &copyValue
		}
		if stream.Audio != nil {
			copyValue := *stream.Audio
			result.Streams[index].Audio = &copyValue
		}
		if stream.Video != nil {
			copyValue := *stream.Video
			result.Streams[index].Video = &copyValue
		}
	}
	return result
}

func cloneRequest(value audiograph.SourceRequest) audiograph.SourceRequest {
	value.DecoderChannels = append([]string(nil), value.DecoderChannels...)
	return value
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return sourceError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return sourceError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[7:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func equalTime(left, right documentv2.RationalTime) bool {
	if left.Validate() != nil || right.Validate() != nil {
		return false
	}
	return timeRat(left).Cmp(timeRat(right)) == 0
}

func timeRat(value documentv2.RationalTime) *big.Rat {
	return new(big.Rat).SetFrac64(value.Value, value.Rate)
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
