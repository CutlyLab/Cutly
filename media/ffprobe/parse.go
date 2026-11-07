package ffprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

type parseIdentity struct {
	assetID            videoedit.ID
	contentHash        string
	sizeBytes          int64
	buildFingerprint   string
	optionsFingerprint string
}

type rawOutput struct {
	Streams []rawStream `json:"streams"`
	Format  rawFormat   `json:"format"`
}

type rawFormat struct {
	FormatName string `json:"format_name"`
	StartTime  string `json:"start_time"`
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
}

type rawStream struct {
	Index              int                          `json:"index"`
	CodecName          string                       `json:"codec_name"`
	CodecType          string                       `json:"codec_type"`
	TimeBase           string                       `json:"time_base"`
	StartPTS           json.RawMessage              `json:"start_pts"`
	StartTime          string                       `json:"start_time"`
	DurationTS         json.RawMessage              `json:"duration_ts"`
	Duration           string                       `json:"duration"`
	Width              int                          `json:"width"`
	Height             int                          `json:"height"`
	PixelFormat        string                       `json:"pix_fmt"`
	SampleAspectRatio  string                       `json:"sample_aspect_ratio"`
	DisplayAspectRatio string                       `json:"display_aspect_ratio"`
	AverageFrameRate   string                       `json:"avg_frame_rate"`
	RealFrameRate      string                       `json:"r_frame_rate"`
	ColorRange         string                       `json:"color_range"`
	ColorSpace         string                       `json:"color_space"`
	ColorTransfer      string                       `json:"color_transfer"`
	ColorPrimaries     string                       `json:"color_primaries"`
	ChromaLocation     string                       `json:"chroma_location"`
	SampleFormat       string                       `json:"sample_fmt"`
	SampleRate         string                       `json:"sample_rate"`
	Channels           int                          `json:"channels"`
	ChannelLayout      string                       `json:"channel_layout"`
	BitsPerSample      int                          `json:"bits_per_sample"`
	BitsPerRawSample   string                       `json:"bits_per_raw_sample"`
	InitialPadding     json.RawMessage              `json:"initial_padding"`
	TrailingPadding    json.RawMessage              `json:"trailing_padding"`
	Tags               map[string]string            `json:"tags"`
	SideData           []map[string]json.RawMessage `json:"side_data_list"`
}

func parseOutput(raw []byte, identity parseIdentity) (probe.ProbeManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var output rawOutput
	if err := decoder.Decode(&output); err != nil {
		return probe.ProbeManifest{}, fmt.Errorf("%w: JSON: %v", ErrParse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return probe.ProbeManifest{}, fmt.Errorf("%w: multiple JSON values", ErrParse)
		}
		return probe.ProbeManifest{}, fmt.Errorf("%w: trailing JSON: %v", ErrParse, err)
	}

	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource: probe.ResourceIdentity{
			AssetID:     identity.assetID,
			ContentHash: identity.contentHash,
			SizeBytes:   identity.sizeBytes,
		},
		Probe: probe.ProbeIdentity{
			Implementation:     Implementation,
			BuildFingerprint:   identity.buildFingerprint,
			OptionsFingerprint: identity.optionsFingerprint,
		},
		Container: probe.ContainerInfo{FormatName: strings.TrimSpace(output.Format.FormatName), Seekable: true},
	}

	if output.Format.BitRate != "" && output.Format.BitRate != "N/A" {
		bitRate, err := strconv.ParseInt(output.Format.BitRate, 10, 64)
		if err != nil {
			return probe.ProbeManifest{}, fmt.Errorf("%w: format bit_rate %q: %v", ErrParse, output.Format.BitRate, err)
		}
		manifest.Container.BitRate = bitRate
	}

	for _, stream := range output.Streams {
		if stream.CodecType != string(probe.StreamKindVideo) && stream.CodecType != string(probe.StreamKindAudio) {
			continue
		}
		parsed, err := parseStream(stream)
		if err != nil {
			return probe.ProbeManifest{}, fmt.Errorf("%w: stream %d: %v", ErrParse, stream.Index, err)
		}
		manifest.Streams = append(manifest.Streams, parsed)
	}
	sort.SliceStable(manifest.Streams, func(i, j int) bool { return manifest.Streams[i].Index < manifest.Streams[j].Index })

	start, startPresent, err := parseOptionalDecimal(output.Format.StartTime)
	if err != nil {
		return probe.ProbeManifest{}, fmt.Errorf("%w: format start_time: %v", ErrParse, err)
	}
	duration, durationPresent, err := parseOptionalDecimal(output.Format.Duration)
	if err != nil {
		return probe.ProbeManifest{}, fmt.Errorf("%w: format duration: %v", ErrParse, err)
	}
	derivedStart, derivedDuration, derived := deriveBounds(manifest.Streams)
	if startPresent {
		manifest.Container.StartTime = start
	} else if derived {
		manifest.Container.StartTime = derivedStart
	} else {
		manifest.Container.StartTime = videoedit.RationalTime{Value: 0, Rate: 1}
	}
	if durationPresent {
		manifest.Duration = duration
	} else if derived {
		manifest.Duration = derivedDuration
	} else {
		return probe.ProbeManifest{}, fmt.Errorf("%w: no exact container or stream duration", ErrParse)
	}
	return manifest, nil
}

func parseStream(raw rawStream) (probe.Stream, error) {
	timeBase, err := parseFraction(raw.TimeBase, '/')
	if err != nil || timeBase.Value <= 0 {
		return probe.Stream{}, fmt.Errorf("invalid time_base %q", raw.TimeBase)
	}
	startPTSPointer, present, err := parseOptionalInt64(raw.StartPTS)
	if err != nil {
		return probe.Stream{}, fmt.Errorf("start_pts: %v", err)
	}
	startPTS := int64(0)
	if present {
		startPTS = *startPTSPointer
	}
	if !present {
		startTime, timePresent, timeErr := parseOptionalDecimal(raw.StartTime)
		if timeErr != nil {
			return probe.Stream{}, fmt.Errorf("start_time: %v", timeErr)
		}
		if timePresent {
			value, exact := timeToTicks(startTime, timeBase)
			if !exact {
				return probe.Stream{}, fmt.Errorf("start_time %s is not exact in time_base %s", raw.StartTime, raw.TimeBase)
			}
			startPTS = value
			present = true
		}
	}
	if !present {
		return probe.Stream{}, errors.New("start_pts and exact start_time are both unavailable")
	}
	durationPTS, _, err := parseOptionalInt64(raw.DurationTS)
	if err != nil {
		return probe.Stream{}, fmt.Errorf("duration_ts: %v", err)
	}
	if durationPTS == nil {
		duration, durationPresent, durationErr := parseOptionalDecimal(raw.Duration)
		if durationErr != nil {
			return probe.Stream{}, fmt.Errorf("duration: %v", durationErr)
		}
		if durationPresent {
			value, exact := timeToTicks(duration, timeBase)
			if exact {
				durationPTS = &value
			}
		}
	}

	stream := probe.Stream{
		Index:       raw.Index,
		Kind:        probe.StreamKind(raw.CodecType),
		Codec:       strings.TrimSpace(raw.CodecName),
		TimeBase:    timeBase,
		StartPTS:    startPTS,
		DurationPTS: durationPTS,
	}
	switch stream.Kind {
	case probe.StreamKindVideo:
		video, err := parseVideo(raw)
		if err != nil {
			return probe.Stream{}, err
		}
		stream.Video = &video
	case probe.StreamKindAudio:
		audio, err := parseAudio(raw)
		if err != nil {
			return probe.Stream{}, err
		}
		stream.Audio = &audio
	}
	return stream, nil
}

func parseVideo(raw rawStream) (probe.VideoStream, error) {
	averageRate, err := parseOptionalPositiveFraction(raw.AverageFrameRate)
	if err != nil {
		return probe.VideoStream{}, fmt.Errorf("average frame rate: %v", err)
	}
	sampleAspect, err := parseOptionalRatio(raw.SampleAspectRatio)
	if err != nil {
		return probe.VideoStream{}, fmt.Errorf("sample aspect ratio: %v", err)
	}
	displayAspect, err := parseOptionalRatio(raw.DisplayAspectRatio)
	if err != nil {
		return probe.VideoStream{}, fmt.Errorf("display aspect ratio: %v", err)
	}
	rotation, err := parseRotation(raw)
	if err != nil {
		return probe.VideoStream{}, err
	}
	hdr := parseHDR(raw.SideData)
	return probe.VideoStream{
		Width:              raw.Width,
		Height:             raw.Height,
		RotationDegrees:    rotation,
		SampleAspectRatio:  sampleAspect,
		DisplayAspectRatio: displayAspect,
		PixelFormat:        strings.TrimSpace(raw.PixelFormat),
		HasAlpha:           pixelFormatHasAlpha(raw.PixelFormat),
		// Container rate fields alone do not prove CFR or VFR. Adapter v2 applies a separate bounded
		// decoded-frame PTS scan after this structural parse and promotes the mode only on proof.
		FrameRateMode:    probe.FrameRateUnknown,
		AverageFrameRate: averageRate,
		Keyframes:        probe.KeyframeSummary{Complete: false, Count: 0},
		Color: probe.ColorMetadata{
			Primaries:      strings.TrimSpace(raw.ColorPrimaries),
			Transfer:       strings.TrimSpace(raw.ColorTransfer),
			Matrix:         strings.TrimSpace(raw.ColorSpace),
			Range:          mapColorRange(raw.ColorRange),
			ChromaLocation: strings.TrimSpace(raw.ChromaLocation),
		},
		HDR: hdr,
	}, nil
}

func parseAudio(raw rawStream) (probe.AudioStream, error) {
	sampleRate, err := strconv.ParseInt(raw.SampleRate, 10, 64)
	if err != nil {
		return probe.AudioStream{}, fmt.Errorf("sample_rate %q: %v", raw.SampleRate, err)
	}
	bits := raw.BitsPerSample
	if bits == 0 && raw.BitsPerRawSample != "" && raw.BitsPerRawSample != "N/A" {
		value, parseErr := strconv.ParseInt(raw.BitsPerRawSample, 10, 32)
		if parseErr != nil {
			return probe.AudioStream{}, fmt.Errorf("bits_per_raw_sample %q: %v", raw.BitsPerRawSample, parseErr)
		}
		bits = int(value)
	}
	delay, _, err := parseOptionalInt64(raw.InitialPadding)
	if err != nil {
		return probe.AudioStream{}, fmt.Errorf("initial_padding: %v", err)
	}
	padding, _, err := parseOptionalInt64(raw.TrailingPadding)
	if err != nil {
		return probe.AudioStream{}, fmt.Errorf("trailing_padding: %v", err)
	}
	layout := strings.TrimSpace(raw.ChannelLayout)
	if layout == "" {
		layout = "unspecified"
	}
	return probe.AudioStream{
		SampleRate:          sampleRate,
		Channels:            raw.Channels,
		ChannelLayout:       layout,
		SampleFormat:        strings.TrimSpace(raw.SampleFormat),
		BitsPerSample:       bits,
		EncoderDelaySamples: valueOrZero(delay),
		PaddingSamples:      valueOrZero(padding),
	}, nil
}

func parseRotation(raw rawStream) (int, error) {
	var value int64
	var present bool
	if raw.Tags != nil && strings.TrimSpace(raw.Tags["rotate"]) != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(raw.Tags["rotate"]), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("rotation tag: %v", err)
		}
		value, present = parsed, true
	}
	for _, sideData := range raw.SideData {
		if rotation, ok := sideData["rotation"]; ok {
			parsed, found, err := parseOptionalInt64(rotation)
			if err != nil {
				return 0, fmt.Errorf("rotation side data: %v", err)
			}
			if found {
				value, present = *parsed, true
			}
		}
	}
	if !present {
		return 0, nil
	}
	value %= 360
	if value > 180 {
		value -= 360
	}
	if value < -180 {
		value += 360
	}
	return int(value), nil
}

func parseHDR(values []map[string]json.RawMessage) *probe.HDRMetadata {
	var formats []string
	result := probe.HDRMetadata{}
	for _, value := range values {
		typeName := rawString(value["side_data_type"])
		lower := strings.ToLower(typeName)
		if strings.Contains(lower, "mastering display") {
			formats = append(formats, "mastering-display")
			if raw, err := json.Marshal(value); err == nil {
				result.MasteringDisplay = string(raw)
			}
		}
		if strings.Contains(lower, "content light") {
			formats = append(formats, "content-light")
			if parsed, ok, _ := parseOptionalInt64(value["max_content"]); ok {
				result.MaxContentLightLevel = *parsed
			}
			if parsed, ok, _ := parseOptionalInt64(value["max_average"]); ok {
				result.MaxFrameAverageLightLevel = *parsed
			}
		}
		if strings.Contains(lower, "dovi") || strings.Contains(lower, "dolby vision") {
			formats = append(formats, "dolby-vision")
		}
		if strings.Contains(lower, "hdr10+") {
			formats = append(formats, "hdr10+")
		}
	}
	if len(formats) == 0 {
		return nil
	}
	sort.Strings(formats)
	result.Format = strings.Join(formats, "+")
	return &result
}

func deriveBounds(streams []probe.Stream) (videoedit.RationalTime, videoedit.RationalTime, bool) {
	var minimum, maximum *big.Rat
	for _, stream := range streams {
		if stream.DurationPTS == nil {
			continue
		}
		start := ticksAsBigRat(stream.StartPTS, stream.TimeBase)
		end := new(big.Rat).Add(start, ticksAsBigRat(*stream.DurationPTS, stream.TimeBase))
		if minimum == nil || start.Cmp(minimum) < 0 {
			minimum = start
		}
		if maximum == nil || end.Cmp(maximum) > 0 {
			maximum = end
		}
	}
	if minimum == nil || maximum == nil || maximum.Cmp(minimum) < 0 {
		return videoedit.RationalTime{}, videoedit.RationalTime{}, false
	}
	start, err := rationalTimeFromBig(minimum)
	if err != nil {
		return videoedit.RationalTime{}, videoedit.RationalTime{}, false
	}
	duration, err := rationalTimeFromBig(new(big.Rat).Sub(maximum, minimum))
	if err != nil {
		return videoedit.RationalTime{}, videoedit.RationalTime{}, false
	}
	return start, duration, true
}

func parseOptionalDecimal(value string) (videoedit.RationalTime, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" {
		return videoedit.RationalTime{}, false, nil
	}
	rational, ok := new(big.Rat).SetString(value)
	if !ok {
		return videoedit.RationalTime{}, false, fmt.Errorf("invalid exact decimal %q", value)
	}
	parsed, err := rationalTimeFromBig(rational)
	return parsed, true, err
}

func parseFraction(value string, separator byte) (videoedit.RationalTime, error) {
	parts := strings.Split(strings.TrimSpace(value), string(separator))
	if len(parts) != 2 {
		return videoedit.RationalTime{}, fmt.Errorf("invalid fraction %q", value)
	}
	numerator, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return videoedit.RationalTime{}, err
	}
	denominator, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || denominator == 0 {
		return videoedit.RationalTime{}, fmt.Errorf("invalid denominator %q", parts[1])
	}
	return videoedit.NewRationalTime(numerator, denominator)
}

func parseOptionalPositiveFraction(value string) (*videoedit.RationalTime, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" || value == "0/0" {
		return nil, nil
	}
	parsed, err := parseFraction(value, '/')
	if err != nil {
		return nil, err
	}
	if parsed.Value <= 0 {
		return nil, nil
	}
	return &parsed, nil
}

func parseOptionalRatio(value string) (*probe.Ratio, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" || value == "0:1" || value == "0/1" {
		return nil, nil
	}
	separator := byte(':')
	if strings.Contains(value, "/") {
		separator = '/'
	}
	parsed, err := parseFraction(value, separator)
	if err != nil || parsed.Value <= 0 {
		return nil, fmt.Errorf("invalid ratio %q", value)
	}
	return &probe.Ratio{Numerator: parsed.Value, Denominator: parsed.Rate}, nil
}

func parseOptionalInt64(raw json.RawMessage) (*int64, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, nil
	}
	value := string(trimmed)
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, false, err
		}
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" {
		return nil, false, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, false, err
	}
	return &parsed, true, nil
}

func timeToTicks(value, timeBase videoedit.RationalTime) (int64, bool) {
	time := new(big.Rat).SetFrac64(value.Value, value.Rate)
	base := new(big.Rat).SetFrac64(timeBase.Value, timeBase.Rate)
	ticks := new(big.Rat).Quo(time, base)
	if !ticks.IsInt() || !ticks.Num().IsInt64() {
		return 0, false
	}
	return ticks.Num().Int64(), true
}

func ticksAsBigRat(ticks int64, timeBase videoedit.RationalTime) *big.Rat {
	return new(big.Rat).Mul(new(big.Rat).SetInt64(ticks), new(big.Rat).SetFrac64(timeBase.Value, timeBase.Rate))
}

func rationalTimeFromBig(value *big.Rat) (videoedit.RationalTime, error) {
	if value == nil || !value.Num().IsInt64() || !value.Denom().IsInt64() {
		return videoedit.RationalTime{}, videoedit.ErrTimeOverflow
	}
	return videoedit.NewRationalTime(value.Num().Int64(), value.Denom().Int64())
}

func mapColorRange(value string) probe.ColorRange {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "mpeg", "limited":
		return probe.ColorRangeLimited
	case "pc", "jpeg", "full":
		return probe.ColorRangeFull
	default:
		return probe.ColorRangeUnknown
	}
}

func pixelFormatHasAlpha(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "yuva") || strings.HasPrefix(value, "gbrap") ||
		strings.HasPrefix(value, "ya") || value == "rgba" || value == "argb" || value == "bgra" || value == "abgr"
}

func rawString(value json.RawMessage) string {
	var parsed string
	if len(value) > 0 && json.Unmarshal(value, &parsed) == nil {
		return parsed
	}
	return strings.Trim(string(bytes.TrimSpace(value)), `"`)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
