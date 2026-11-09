package audiograph

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

// SourceRequest is the complete immutable identity of one decoded/selected/retimed source sample.
// DocumentHash scopes all document-local IDs. Profile fields are frozen from the asset's
// MediaBinding rather than supplied by the provider. DecoderChannels is owned by the request and
// must not be retained or mutated by a provider.
type SourceRequest struct {
	ContractVersion     int                     `json:"contract_version"`
	DocumentHash        string                  `json:"document_hash"`
	TimelineID          documentv2.ID           `json:"timeline_id"`
	TimelineIndex       int                     `json:"timeline_index"`
	TrackID             documentv2.ID           `json:"track_id"`
	TrackIndex          int                     `json:"track_index"`
	ClipID              documentv2.ID           `json:"clip_id"`
	ClipIndex           int                     `json:"clip_index"`
	AssetID             documentv2.ID           `json:"asset_id"`
	AssetIndex          int                     `json:"asset_index"`
	AssetKind           documentv2.AssetKind    `json:"asset_kind"`
	TimelineTime        documentv2.RationalTime `json:"timeline_time"`
	LocalTime           documentv2.RationalTime `json:"local_time"`
	SourceTime          documentv2.RationalTime `json:"source_time"`
	SourceProfile       string                  `json:"source_profile"`
	SampleDomain        string                  `json:"sample_domain"`
	DecodeProfile       string                  `json:"decode_profile"`
	SelectionProfile    string                  `json:"selection_profile"`
	RetimeProfile       string                  `json:"retime_profile"`
	ManifestFingerprint string                  `json:"manifest_fingerprint"`
	ResourceContentHash string                  `json:"resource_content_hash"`
	ResourceSizeBytes   int64                   `json:"resource_size_bytes"`
	StreamIndex         int                     `json:"stream_index"`
	SampleRate          int64                   `json:"sample_rate"`
	ChannelLayout       string                  `json:"channel_layout"`
	DecoderChannels     []string                `json:"decoder_channels"`
}

// SourceSample is a provider response. It must exactly echo the complete request and source sample
// domain; this turns a provider's resource/profile claim into a validated receipt rather than an
// unchecked side channel. Channels are in Request.DecoderChannels order and may exceed [-1,1].
// The provider transfers the returned slices for the duration of validation and must not mutate
// them concurrently; the graph immediately takes defensive copies.
type SourceSample struct {
	ContractVersion int           `json:"contract_version"`
	Request         SourceRequest `json:"request"`
	SampleDomain    string        `json:"sample_domain"`
	Channels        []Exact       `json:"channels"`
}

// SourceReceipt is the defensive, serializable provider record retained in an Evaluation. It is
// not a signature or proof that provider claims are truthful; a containing evidence manifest must
// bind it to trusted runtime identity/worker attestation when that guarantee is required.
type SourceReceipt struct {
	ContractVersion int           `json:"contract_version"`
	Request         SourceRequest `json:"request"`
	SampleDomain    string        `json:"sample_domain"`
	Channels        []Exact       `json:"channels"`
}

// SourceProvider supplies exact source-domain samples. Decoder/resampler implementations are
// separate versioned capabilities; this graph owns source-time mapping and all following DSP.
type SourceProvider interface {
	Sample(context.Context, SourceRequest) (SourceSample, error)
}

// SourceProviderFunc adapts a function to SourceProvider.
type SourceProviderFunc func(context.Context, SourceRequest) (SourceSample, error)

func (function SourceProviderFunc) Sample(ctx context.Context, request SourceRequest) (SourceSample, error) {
	if function == nil {
		return SourceSample{}, runtimeError(CodeInputInvalid, "source_provider", ErrInputInvalid)
	}
	return function(ctx, request)
}

// ClipContribution is one active track signal after optional matrix, clip gain, and clip pan but
// before its route/bus processing. Entries retain selected timeline track-array order.
type ClipContribution struct {
	TrackID    documentv2.ID           `json:"track_id"`
	ClipID     documentv2.ID           `json:"clip_id"`
	AssetID    documentv2.ID           `json:"asset_id"`
	TrackIndex int                     `json:"track_index"`
	ClipIndex  int                     `json:"clip_index"`
	SourceTime documentv2.RationalTime `json:"source_time"`
	BusID      documentv2.ID           `json:"bus_id,omitempty"`
	Source     SourceReceipt           `json:"source"`
	Sample     StereoSample            `json:"sample"`
}

// BusEvaluation records the stable input order and exact signal around one bus. BusEvaluations in
// an Evaluation retain Document.AudioBuses order, independent of recursive traversal order.
type BusEvaluation struct {
	BusID        documentv2.ID   `json:"bus_id"`
	BusIndex     int             `json:"bus_index"`
	DirectTracks []documentv2.ID `json:"direct_tracks,omitempty"`
	ChildBuses   []documentv2.ID `json:"child_buses,omitempty"`
	Input        StereoSample    `json:"input"`
	Output       StereoSample    `json:"output"`
}

// Evaluation is one exact pre-quantization composition sample.
type Evaluation struct {
	Profile               string                  `json:"profile"`
	SampleDomain          string                  `json:"sample_domain"`
	SourceContractVersion int                     `json:"source_contract_version"`
	SourceProfile         string                  `json:"source_profile"`
	DocumentHash          string                  `json:"document_hash"`
	TimelineID            documentv2.ID           `json:"timeline_id"`
	TimelineTime          documentv2.RationalTime `json:"timeline_time"`
	ActiveClips           []ClipContribution      `json:"active_clips,omitempty"`
	Buses                 []BusEvaluation         `json:"buses,omitempty"`
	Output                StereoSample            `json:"output"`
}

type routedSignal struct {
	trackID documentv2.ID
	sample  StereoSample
}

func (track compiledTrack) activeClip(at *big.Rat) (compiledClip, bool) {
	next := sort.Search(len(track.clips), func(index int) bool {
		return track.clips[index].start.Cmp(at) > 0
	})
	if next == 0 {
		return compiledClip{}, false
	}
	candidate := track.clips[next-1]
	if candidate.end.Cmp(at) <= 0 {
		return compiledClip{}, false
	}
	return candidate, true
}

// EvaluateAt evaluates an exact timeline time. The provider is called once for each active audio
// track, in timeline track-array order. On any failure it returns a zero Evaluation; callers never
// receive a partially mixed success.
func (program *Program) EvaluateAt(ctx context.Context, timelineID documentv2.ID, at documentv2.RationalTime, provider SourceProvider) (Evaluation, error) {
	if program == nil {
		return Evaluation{}, runtimeError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	if nilInterface(provider) {
		return Evaluation{}, runtimeError(CodeInputInvalid, "source_provider", ErrInputInvalid)
	}
	if err := checkContext(ctx, "context"); err != nil {
		return Evaluation{}, err
	}
	timeline, exists := program.timelines[timelineID]
	if !exists {
		return Evaluation{}, runtimeError(CodeProgramLookup, "timeline_id", ErrProgramLookup)
	}
	if err := at.Validate(); err != nil {
		return Evaluation{}, runtimeError(CodeInputInvalid, "timeline_time", ErrInputInvalid)
	}
	atBig := new(big.Rat).SetFrac64(at.Value, at.Rate)
	result := Evaluation{
		Profile: ProfileV1, SampleDomain: SampleDomainProfileV1,
		SourceContractVersion: SourceContractVersion, SourceProfile: SourceProfileV1,
		DocumentHash: program.documentHash,
		TimelineID:   timelineID, TimelineTime: at, Output: zeroStereo(),
	}
	direct := make([][]routedSignal, len(program.buses))
	legacySignals := make([]routedSignal, 0, len(timeline.tracks))

	for _, track := range timeline.tracks {
		if err := checkContext(ctx, "timeline.tracks"); err != nil {
			return Evaluation{}, err
		}
		clip, active := track.activeClip(atBig)
		if !active {
			continue
		}
		evaluated, err := program.evaluator.EvaluateClip(timelineID, clip.id, at)
		if err != nil {
			return Evaluation{}, runtimeError(CodeInputInvalid, fmt.Sprintf("clip[%s]", clip.id), err)
		}
		binding, exists := program.bindings[clip.assetID]
		if !exists {
			return Evaluation{}, runtimeError(CodeSourceBinding, fmt.Sprintf("clip[%s].asset", clip.id), ErrSourceBinding)
		}
		request := SourceRequest{
			ContractVersion: SourceContractVersion, DocumentHash: program.documentHash,
			TimelineID: timelineID, TimelineIndex: timeline.index,
			TrackID: clip.trackID, TrackIndex: clip.trackIndex,
			ClipID: clip.id, ClipIndex: clip.clipIndex,
			AssetID: clip.assetID, AssetIndex: binding.assetIndex, AssetKind: binding.assetKind,
			TimelineTime: at, LocalTime: evaluated.LocalTime, SourceTime: evaluated.SourceTime,
			SourceProfile: SourceProfileV1, SampleDomain: SourceSampleDomainProfileV1,
			DecodeProfile: binding.decodeProfile, SelectionProfile: binding.selectionProfile,
			RetimeProfile:       binding.retimeProfile,
			ManifestFingerprint: binding.manifestHash, StreamIndex: binding.streamIndex,
			ResourceContentHash: binding.manifest.Resource.ContentHash,
			ResourceSizeBytes:   binding.manifest.Resource.SizeBytes,
			SampleRate:          binding.audio.SampleRate, ChannelLayout: binding.audio.ChannelLayout,
			DecoderChannels: append([]string(nil), binding.decoderChannels...),
		}
		if err := checkContext(ctx, fmt.Sprintf("clip[%s].source", clip.id)); err != nil {
			return Evaluation{}, err
		}
		// Keep the authoritative request separate from the provider-owned slice backing arrays. A
		// hostile provider mutating its argument therefore cannot rewrite the expected receipt.
		source, providerErr := provider.Sample(ctx, cloneSourceRequest(request))
		if err := checkContext(ctx, fmt.Sprintf("clip[%s].source", clip.id)); err != nil {
			return Evaluation{}, err
		}
		if providerErr != nil {
			if errors.Is(providerErr, context.Canceled) || errors.Is(providerErr, context.DeadlineExceeded) {
				return Evaluation{}, runtimeError(CodeCanceled, fmt.Sprintf("clip[%s].source", clip.id), errors.Join(ErrCanceled, providerErr))
			}
			return Evaluation{}, runtimeError(CodeSourceProvider, fmt.Sprintf("clip[%s].source", clip.id), errors.Join(ErrSourceProvider, providerErr))
		}
		receipt, err := validateSourceSample(source, request, binding, program.limits.MaxExactBits)
		if err != nil {
			if errors.Is(err, ErrLimitExceeded) {
				return Evaluation{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("clip[%s].source", clip.id), err)
			}
			if errors.Is(err, ErrSourceMismatch) {
				return Evaluation{}, runtimeError(CodeSourceMismatch, fmt.Sprintf("clip[%s].source", clip.id), err)
			}
			return Evaluation{}, runtimeError(CodeSourceSampleInvalid, fmt.Sprintf("clip[%s].source", clip.id), err)
		}
		// DSP consumes only the validated defensive receipt. It must never read the provider-owned
		// response again after validation, otherwise evidence and rendered samples could diverge.
		stereo, err := sourceToStereo(receipt, evaluated.Properties.AudioChannelMatrix, clip.usesMatrix)
		if err != nil {
			return Evaluation{}, runtimeError(CodeSourceSampleInvalid, fmt.Sprintf("clip[%s].matrix", clip.id), err)
		}
		if err := validateStereoBits(stereo, program.limits.MaxExactBits); err != nil {
			return Evaluation{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("clip[%s].matrix", clip.id), err)
		}
		if evaluated.Properties.AudioGain == nil || evaluated.Properties.AudioPan == nil {
			return Evaluation{}, runtimeError(CodeInputInvalid, fmt.Sprintf("clip[%s].properties", clip.id), ErrInputInvalid)
		}
		gain, err := exactFromEvaluator(*evaluated.Properties.AudioGain)
		if err != nil {
			return Evaluation{}, err
		}
		pan, err := exactFromEvaluator(*evaluated.Properties.AudioPan)
		if err != nil {
			return Evaluation{}, err
		}
		stereo, err = gainStereo(stereo, gain)
		if err != nil {
			return Evaluation{}, err
		}
		if err := validateStereoBits(stereo, program.limits.MaxExactBits); err != nil {
			return Evaluation{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("clip[%s].gain", clip.id), err)
		}
		stereo, err = panStereo(stereo, pan)
		if err != nil {
			return Evaluation{}, err
		}
		if err := validateStereoBits(stereo, program.limits.MaxExactBits); err != nil {
			return Evaluation{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("clip[%s].properties", clip.id), err)
		}
		busID := documentv2.ID("")
		if track.busIndex >= 0 {
			busID = program.buses[track.busIndex].id
		}
		result.ActiveClips = append(result.ActiveClips, ClipContribution{
			TrackID: clip.trackID, ClipID: clip.id, AssetID: clip.assetID,
			TrackIndex: clip.trackIndex, ClipIndex: clip.clipIndex, SourceTime: evaluated.SourceTime,
			BusID: busID, Source: receipt, Sample: stereo,
		})
		signal := routedSignal{trackID: track.id, sample: stereo}
		if track.busIndex < 0 {
			legacySignals = append(legacySignals, signal)
		} else {
			direct[track.busIndex] = append(direct[track.busIndex], signal)
		}
	}

	if len(program.buses) == 0 {
		output := zeroStereo()
		for _, signal := range legacySignals {
			if err := checkContext(ctx, "legacy_master.sum"); err != nil {
				return Evaluation{}, err
			}
			var err error
			output, err = addStereo(output, signal.sample)
			if err != nil {
				return Evaluation{}, err
			}
			if err := validateStereoBits(output, program.limits.MaxExactBits); err != nil {
				return Evaluation{}, runtimeError(CodeLimitExceeded, "legacy_master.sum", err)
			}
		}
		result.Output = output
		return result, nil
	}
	if program.masterBus < 0 || program.masterBus >= len(program.buses) {
		return Evaluation{}, runtimeError(CodeProgramLookup, "master_bus", ErrProgramLookup)
	}

	busResults := make([]BusEvaluation, len(program.buses))
	busSignals := make([]StereoSample, len(program.buses))
	done := make([]bool, len(program.buses))
	var evaluateBus func(int) (StereoSample, error)
	evaluateBus = func(index int) (StereoSample, error) {
		if done[index] {
			return busSignals[index], nil
		}
		bus := program.buses[index]
		input := zeroStereo()
		directTrackIDs := make([]documentv2.ID, 0, len(direct[index]))
		for _, signal := range direct[index] {
			if err := checkContext(ctx, fmt.Sprintf("bus[%s].direct_sum", bus.id)); err != nil {
				return StereoSample{}, err
			}
			var err error
			input, err = addStereo(input, signal.sample)
			if err != nil {
				return StereoSample{}, err
			}
			if err := validateStereoBits(input, program.limits.MaxExactBits); err != nil {
				return StereoSample{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("bus[%s].direct_sum", bus.id), err)
			}
			directTrackIDs = append(directTrackIDs, signal.trackID)
		}
		childIDs := make([]documentv2.ID, 0, len(bus.children))
		for _, childIndex := range bus.children {
			if err := checkContext(ctx, fmt.Sprintf("bus[%s].child_sum", bus.id)); err != nil {
				return StereoSample{}, err
			}
			child, err := evaluateBus(childIndex)
			if err != nil {
				return StereoSample{}, err
			}
			input, err = addStereo(input, child)
			if err != nil {
				return StereoSample{}, err
			}
			if err := validateStereoBits(input, program.limits.MaxExactBits); err != nil {
				return StereoSample{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("bus[%s].child_sum", bus.id), err)
			}
			childIDs = append(childIDs, program.buses[childIndex].id)
		}
		output, err := gainStereo(input, bus.gain)
		if err != nil {
			return StereoSample{}, err
		}
		output, err = panStereo(output, bus.pan)
		if err != nil {
			return StereoSample{}, err
		}
		if index == program.masterBus && bus.limiter != nil {
			output, err = limitStereo(output, *bus.limiter)
			if err != nil {
				return StereoSample{}, err
			}
		}
		if err := validateStereoBits(output, program.limits.MaxExactBits); err != nil {
			return StereoSample{}, runtimeError(CodeLimitExceeded, fmt.Sprintf("bus[%s].output", bus.id), err)
		}
		busSignals[index] = output
		busResults[index] = BusEvaluation{
			BusID: bus.id, BusIndex: bus.index, DirectTracks: directTrackIDs, ChildBuses: childIDs,
			Input: input, Output: output,
		}
		done[index] = true
		return output, nil
	}
	output, err := evaluateBus(program.masterBus)
	if err != nil {
		return Evaluation{}, err
	}
	result.Buses = busResults
	result.Output = output
	return result, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func checkContext(ctx context.Context, path string) error {
	if ctx == nil {
		return runtimeError(CodeInputInvalid, path, ErrInputInvalid)
	}
	if err := ctx.Err(); err != nil {
		return runtimeError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func validateSourceSample(value SourceSample, expected SourceRequest, binding compiledBinding, maxBits int) (SourceReceipt, error) {
	if value.ContractVersion != SourceContractVersion || value.Request.ContractVersion != SourceContractVersion ||
		value.SampleDomain != SourceSampleDomainProfileV1 || !equalSourceRequest(value.Request, expected) {
		return SourceReceipt{}, ErrSourceMismatch
	}
	if len(value.Channels) != len(binding.decoderChannels) {
		return SourceReceipt{}, fmt.Errorf("%w: got %d channels, want %d", ErrSourceSampleInvalid, len(value.Channels), len(binding.decoderChannels))
	}
	for index, channel := range value.Channels {
		if !channel.Valid() {
			return SourceReceipt{}, fmt.Errorf("%w: channel %d is invalid", ErrSourceSampleInvalid, index)
		}
		if exactBits(channel) > maxBits {
			return SourceReceipt{}, fmt.Errorf("%w: channel %d exact representation exceeds %d bits", ErrLimitExceeded, index, maxBits)
		}
	}
	return SourceReceipt{
		ContractVersion: SourceContractVersion, Request: cloneSourceRequest(expected),
		SampleDomain: SourceSampleDomainProfileV1, Channels: cloneExactSlice(value.Channels),
	}, nil
}

func cloneExactSlice(values []Exact) []Exact {
	if values == nil {
		return nil
	}
	result := make([]Exact, len(values))
	for index := range values {
		result[index].numerator.Set(&values[index].numerator)
		result[index].denominator.Set(&values[index].denominator)
	}
	return result
}

func cloneSourceRequest(value SourceRequest) SourceRequest {
	result := value
	result.DecoderChannels = append([]string(nil), value.DecoderChannels...)
	return result
}

func equalSourceRequest(left, right SourceRequest) bool {
	return left.ContractVersion == right.ContractVersion && left.DocumentHash == right.DocumentHash &&
		left.TimelineID == right.TimelineID && left.TimelineIndex == right.TimelineIndex &&
		left.TrackID == right.TrackID && left.TrackIndex == right.TrackIndex &&
		left.ClipID == right.ClipID && left.ClipIndex == right.ClipIndex &&
		left.AssetID == right.AssetID && left.AssetIndex == right.AssetIndex && left.AssetKind == right.AssetKind &&
		left.TimelineTime == right.TimelineTime && left.LocalTime == right.LocalTime && left.SourceTime == right.SourceTime &&
		left.SourceProfile == right.SourceProfile && left.SampleDomain == right.SampleDomain &&
		left.DecodeProfile == right.DecodeProfile && left.SelectionProfile == right.SelectionProfile && left.RetimeProfile == right.RetimeProfile &&
		left.ManifestFingerprint == right.ManifestFingerprint && left.ResourceContentHash == right.ResourceContentHash &&
		left.ResourceSizeBytes == right.ResourceSizeBytes && left.StreamIndex == right.StreamIndex &&
		left.SampleRate == right.SampleRate && left.ChannelLayout == right.ChannelLayout &&
		equalStrings(left.DecoderChannels, right.DecoderChannels)
}

func exactBits(value Exact) int {
	if !value.Valid() {
		return 0
	}
	return value.numerator.BitLen() + value.denominator.BitLen()
}

func validateStereoBits(value StereoSample, maxBits int) error {
	if exactBits(value.Left) > maxBits || exactBits(value.Right) > maxBits {
		return fmt.Errorf("%w: exact stereo representation exceeds %d bits", ErrLimitExceeded, maxBits)
	}
	return nil
}

func sourceToStereo(value SourceReceipt, matrix *evaluator.AudioChannelMatrix, usesMatrix bool) (StereoSample, error) {
	if !usesMatrix {
		if matrix != nil {
			return StereoSample{}, fmt.Errorf("%w: unexpected compiled matrix", ErrSourceSampleInvalid)
		}
		switch len(value.Channels) {
		case 1:
			return StereoSample{Left: value.Channels[0], Right: value.Channels[0]}, nil
		case 2:
			return StereoSample{Left: value.Channels[0], Right: value.Channels[1]}, nil
		default:
			return StereoSample{}, fmt.Errorf("%w: direct input is neither mono nor stereo", ErrSourceSampleInvalid)
		}
	}
	if matrix == nil || len(matrix.LeftCoefficients) != len(value.Channels) || len(matrix.RightCoefficients) != len(value.Channels) {
		return StereoSample{}, fmt.Errorf("%w: compiled matrix shape does not match source", ErrSourceSampleInvalid)
	}
	left, right := Zero(), Zero()
	for index, sample := range value.Channels {
		leftCoefficient, err := exactFromEvaluator(matrix.LeftCoefficients[index])
		if err != nil {
			return StereoSample{}, err
		}
		rightCoefficient, err := exactFromEvaluator(matrix.RightCoefficients[index])
		if err != nil {
			return StereoSample{}, err
		}
		leftTerm, err := exactMul(sample, leftCoefficient)
		if err != nil {
			return StereoSample{}, err
		}
		rightTerm, err := exactMul(sample, rightCoefficient)
		if err != nil {
			return StereoSample{}, err
		}
		left, err = exactAdd(left, leftTerm)
		if err != nil {
			return StereoSample{}, err
		}
		right, err = exactAdd(right, rightTerm)
		if err != nil {
			return StereoSample{}, err
		}
	}
	return StereoSample{Left: left, Right: right}, nil
}

// SampleEvaluation couples a composition-wide sample index with its exact timeline evaluation.
type SampleEvaluation struct {
	CompositionSampleIndex int64      `json:"composition_sample_index"`
	Evaluation             Evaluation `json:"evaluation"`
}

// EvaluateSample maps one composition sample index through the shared exact SampleClock.
func (program *Program) EvaluateSample(ctx context.Context, timelineID documentv2.ID, clock videoedit.SampleClock, compositionSampleIndex int64, provider SourceProvider) (SampleEvaluation, error) {
	if program == nil {
		return SampleEvaluation{}, runtimeError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	if err := checkContext(ctx, "context"); err != nil {
		return SampleEvaluation{}, err
	}
	if nilInterface(provider) {
		return SampleEvaluation{}, runtimeError(CodeInputInvalid, "source_provider", ErrInputInvalid)
	}
	at, err := videoedit.SampleTimeAt(clock, compositionSampleIndex)
	if err != nil {
		if errors.Is(err, videoedit.ErrTimeOverflow) {
			return SampleEvaluation{}, runtimeError(CodeTimeOverflow, "sample_clock", ErrTimeOverflow)
		}
		return SampleEvaluation{}, runtimeError(CodeInputInvalid, "sample_clock", ErrInputInvalid)
	}
	evaluation, err := program.EvaluateAt(ctx, timelineID, at, provider)
	if err != nil {
		return SampleEvaluation{}, err
	}
	return SampleEvaluation{CompositionSampleIndex: compositionSampleIndex, Evaluation: evaluation}, nil
}

// BlockEvaluation is a bounded sequence of exact pre-quantization output samples.
type BlockEvaluation struct {
	Profile                string                `json:"profile"`
	SampleDomain           string                `json:"sample_domain"`
	SourceContractVersion  int                   `json:"source_contract_version"`
	SourceProfile          string                `json:"source_profile"`
	DocumentHash           string                `json:"document_hash"`
	TimelineID             documentv2.ID         `json:"timeline_id"`
	SampleClock            videoedit.SampleClock `json:"sample_clock"`
	CompositionSampleRange videoedit.TickRange   `json:"composition_sample_range"`
	Samples                []StereoSample        `json:"samples,omitempty"`
}

// EvaluateBlock evaluates [first, first+count) using the same single-sample path. It rejects
// overflow and configured memory limits before invoking the provider, and returns no partial block
// on any later failure.
func (program *Program) EvaluateBlock(ctx context.Context, timelineID documentv2.ID, clock videoedit.SampleClock, first, count int64, provider SourceProvider) (BlockEvaluation, error) {
	if program == nil {
		return BlockEvaluation{}, runtimeError(CodeProgramLookup, "program", ErrProgramLookup)
	}
	if err := checkContext(ctx, "context"); err != nil {
		return BlockEvaluation{}, err
	}
	if _, exists := program.timelines[timelineID]; !exists {
		return BlockEvaluation{}, runtimeError(CodeProgramLookup, "timeline_id", ErrProgramLookup)
	}
	if nilInterface(provider) {
		return BlockEvaluation{}, runtimeError(CodeInputInvalid, "source_provider", ErrInputInvalid)
	}
	rangeValue := videoedit.TickRange{First: first, Count: count}
	if err := rangeValue.Validate(); err != nil {
		return BlockEvaluation{}, runtimeError(CodeInputInvalid, "sample_range", ErrInputInvalid)
	}
	if count > program.limits.MaxBlockSamples {
		return BlockEvaluation{}, runtimeError(CodeLimitExceeded, "sample_range.count", ErrLimitExceeded)
	}
	maxInt := int64(^uint(0) >> 1)
	if count > maxInt {
		return BlockEvaluation{}, runtimeError(CodeLimitExceeded, "sample_range.count", ErrLimitExceeded)
	}
	if err := clock.Validate(); err != nil {
		return BlockEvaluation{}, runtimeError(CodeInputInvalid, "sample_clock", ErrInputInvalid)
	}
	result := BlockEvaluation{
		Profile: ProfileV1, SampleDomain: SampleDomainProfileV1,
		SourceContractVersion: SourceContractVersion, SourceProfile: SourceProfileV1,
		DocumentHash: program.documentHash, TimelineID: timelineID, SampleClock: clock,
		CompositionSampleRange: rangeValue,
	}
	if count == 0 {
		return result, nil
	}
	samples, allocationErr := allocateStereoSamples(int(count))
	if allocationErr != nil {
		return BlockEvaluation{}, allocationErr
	}
	result.Samples = samples
	for offset := int64(0); offset < count; offset++ {
		if err := checkContext(ctx, "sample_range"); err != nil {
			return BlockEvaluation{}, err
		}
		evaluated, err := program.EvaluateSample(ctx, timelineID, clock, first+offset, provider)
		if err != nil {
			return BlockEvaluation{}, err
		}
		result.Samples[offset] = evaluated.Evaluation.Output
	}
	if err := checkContext(ctx, "sample_range"); err != nil {
		return BlockEvaluation{}, err
	}
	return result, nil
}

func allocateStereoSamples(count int) (result []StereoSample, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = runtimeError(CodeLimitExceeded, "sample_range.count", ErrLimitExceeded)
		}
	}()
	return make([]StereoSample, count), nil
}
