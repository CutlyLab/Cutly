package probe

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

const (
	CodeSchemaUnsupported  = "PROBE.SCHEMA_UNSUPPORTED"
	CodeResourceInvalid    = "PROBE.RESOURCE_INVALID"
	CodeFingerprintInvalid = "PROBE.FINGERPRINT_INVALID"
	CodeContainerInvalid   = "PROBE.CONTAINER_INVALID"
	CodeDurationInvalid    = "PROBE.DURATION_INVALID"
	CodeStreamOrderInvalid = "PROBE.STREAM_ORDER_INVALID"
	CodeStreamInvalid      = "PROBE.STREAM_INVALID"
	CodeTimeBaseInvalid    = "PROBE.TIME_BASE_INVALID"
	CodePTSInvalid         = "PROBE.PTS_INVALID"
	CodeVideoInvalid       = "PROBE.VIDEO_INVALID"
	CodeKeyframesInvalid   = "PROBE.KEYFRAMES_INVALID"
	CodeColorInvalid       = "PROBE.COLOR_INVALID"
	CodeHDRInvalid         = "PROBE.HDR_INVALID"
	CodeAudioInvalid       = "PROBE.AUDIO_INVALID"
)

type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path,omitempty"`
	Message  string   `json:"message"`
}

var digestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*:[0-9a-f]{16,}$`)

// Validate deterministically checks the semantic wire contract without touching input bytes.
func Validate(manifest ProbeManifest) []Diagnostic {
	var diagnostics []Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}

	if manifest.SchemaVersion != SchemaVersion {
		add(CodeSchemaUnsupported, "/schema_version", fmt.Sprintf("schema version %d is not supported; expected %d", manifest.SchemaVersion, SchemaVersion))
	}
	if strings.TrimSpace(string(manifest.Resource.AssetID)) == "" {
		add(CodeResourceInvalid, "/resource/asset_id", "asset_id is required")
	}
	if !validDigest(manifest.Resource.ContentHash) {
		add(CodeResourceInvalid, "/resource/content_hash", "content_hash must be an algorithm-prefixed lowercase hex digest")
	}
	if manifest.Resource.SizeBytes <= 0 {
		add(CodeResourceInvalid, "/resource/size_bytes", "size_bytes must be positive")
	}
	if strings.TrimSpace(manifest.Probe.Implementation) == "" {
		add(CodeFingerprintInvalid, "/probe/implementation", "probe implementation is required")
	}
	for _, value := range []struct {
		path   string
		digest string
	}{
		{path: "/probe/build_fingerprint", digest: manifest.Probe.BuildFingerprint},
		{path: "/probe/options_fingerprint", digest: manifest.Probe.OptionsFingerprint},
	} {
		if !validDigest(value.digest) {
			add(CodeFingerprintInvalid, value.path, "fingerprint must be an algorithm-prefixed lowercase hex digest")
		}
	}
	if strings.TrimSpace(manifest.Container.FormatName) == "" {
		add(CodeContainerInvalid, "/container/format_name", "container format_name is required")
	}
	if err := manifest.Container.StartTime.Validate(); err != nil {
		add(CodeContainerInvalid, "/container/start_time", err.Error())
	}
	if manifest.Container.BitRate < 0 {
		add(CodeContainerInvalid, "/container/bit_rate", "bit_rate must not be negative")
	}
	if err := validateNonNegativeTime(manifest.Duration); err != nil {
		add(CodeDurationInvalid, "/duration", err.Error())
	}
	if len(manifest.Streams) == 0 {
		add(CodeStreamInvalid, "/streams", "at least one video or audio stream is required")
	}

	seen := make(map[int]int, len(manifest.Streams))
	for index, stream := range manifest.Streams {
		path := fmt.Sprintf("/streams/%d", index)
		if stream.Index < 0 {
			add(CodeStreamInvalid, path+"/index", "stream index must not be negative")
		}
		if previous, exists := seen[stream.Index]; exists {
			add(CodeStreamOrderInvalid, path+"/index", fmt.Sprintf("stream index %d duplicates /streams/%d/index", stream.Index, previous))
		} else {
			seen[stream.Index] = index
		}
		if index > 0 && manifest.Streams[index-1].Index >= stream.Index {
			add(CodeStreamOrderInvalid, path+"/index", "streams must be strictly ordered by index")
		}
		validateStream(stream, path, add)
	}
	return diagnostics
}

func validateStream(stream Stream, path string, add func(string, string, string)) {
	if strings.TrimSpace(stream.Codec) == "" {
		add(CodeStreamInvalid, path+"/codec", "codec is required")
	}
	if err := stream.TimeBase.Validate(); err != nil || stream.TimeBase.Value <= 0 {
		message := "time_base must be valid and positive"
		if err != nil {
			message += ": " + err.Error()
		}
		add(CodeTimeBaseInvalid, path+"/time_base", message)
	}
	if stream.DurationPTS != nil && *stream.DurationPTS < 0 {
		add(CodePTSInvalid, path+"/duration_pts", "duration_pts must not be negative")
	}

	switch stream.Kind {
	case StreamKindVideo:
		if stream.Video == nil || stream.Audio != nil {
			add(CodeStreamInvalid, path, "video stream requires video payload and forbids audio payload")
			return
		}
		validateVideo(*stream.Video, path+"/video", add)
	case StreamKindAudio:
		if stream.Audio == nil || stream.Video != nil {
			add(CodeStreamInvalid, path, "audio stream requires audio payload and forbids video payload")
			return
		}
		validateAudio(*stream.Audio, path+"/audio", add)
	default:
		add(CodeStreamInvalid, path+"/kind", fmt.Sprintf("unsupported stream kind %q", stream.Kind))
	}
}

func validateVideo(video VideoStream, path string, add func(string, string, string)) {
	if video.Width <= 0 || video.Height <= 0 {
		add(CodeVideoInvalid, path, "width and height must be positive")
	}
	if video.RotationDegrees < -180 || video.RotationDegrees > 180 {
		add(CodeVideoInvalid, path+"/rotation_degrees", "rotation_degrees must be normalized to [-180,180]")
	}
	validateRatio(video.SampleAspectRatio, path+"/sample_aspect_ratio", add)
	validateRatio(video.DisplayAspectRatio, path+"/display_aspect_ratio", add)
	if strings.TrimSpace(video.PixelFormat) == "" {
		add(CodeVideoInvalid, path+"/pixel_format", "pixel_format is required")
	}
	switch video.FrameRateMode {
	case FrameRateUnknown:
		if video.AverageFrameRate != nil {
			if err := video.AverageFrameRate.Validate(); err != nil || video.AverageFrameRate.Value <= 0 {
				add(CodeVideoInvalid, path+"/average_frame_rate", "average_frame_rate must be valid and positive when present")
			}
		}
	case FrameRateCFR, FrameRateVFR:
		if video.AverageFrameRate == nil {
			add(CodeVideoInvalid, path+"/average_frame_rate", "known frame_rate_mode requires average_frame_rate")
		} else if err := video.AverageFrameRate.Validate(); err != nil || video.AverageFrameRate.Value <= 0 {
			add(CodeVideoInvalid, path+"/average_frame_rate", "average_frame_rate must be valid and positive")
		}
	default:
		add(CodeVideoInvalid, path+"/frame_rate_mode", fmt.Sprintf("unknown frame_rate_mode %q", video.FrameRateMode))
	}
	validateKeyframes(video.Keyframes, path+"/keyframes", add)
	validateColor(video.Color, path+"/color", add)
	if video.HDR != nil {
		validateHDR(*video.HDR, path+"/hdr", add)
	}
}

func validateRatio(ratio *Ratio, path string, add func(string, string, string)) {
	if ratio == nil {
		return
	}
	if ratio.Numerator <= 0 || ratio.Denominator <= 0 {
		add(CodeVideoInvalid, path, "ratio numerator and denominator must be positive")
	}
}

func validateKeyframes(value KeyframeSummary, path string, add func(string, string, string)) {
	if value.Count < 0 {
		add(CodeKeyframesInvalid, path+"/count", "keyframe count must not be negative")
	}
	if value.Count == 0 && (value.FirstPTS != nil || value.LastPTS != nil) {
		add(CodeKeyframesInvalid, path, "zero keyframe count cannot have first_pts or last_pts")
	}
	if value.Count > 0 {
		if value.FirstPTS == nil || value.LastPTS == nil {
			add(CodeKeyframesInvalid, path, "positive keyframe count requires first_pts and last_pts")
		} else if *value.FirstPTS > *value.LastPTS {
			add(CodeKeyframesInvalid, path, "first_pts must not exceed last_pts")
		}
	}
	if value.IndexHash != "" && !validDigest(value.IndexHash) {
		add(CodeKeyframesInvalid, path+"/index_hash", "index_hash must be an algorithm-prefixed lowercase hex digest")
	}
	if value.Complete && !validDigest(value.IndexHash) {
		add(CodeKeyframesInvalid, path+"/index_hash", "a complete keyframe scan requires index_hash")
	}
}

func validateColor(value ColorMetadata, path string, add func(string, string, string)) {
	switch value.Range {
	case ColorRangeUnknown, ColorRangeLimited, ColorRangeFull:
	default:
		add(CodeColorInvalid, path+"/range", fmt.Sprintf("unknown color range %q", value.Range))
	}
}

func validateHDR(value HDRMetadata, path string, add func(string, string, string)) {
	if strings.TrimSpace(value.Format) == "" {
		add(CodeHDRInvalid, path+"/format", "HDR format is required")
	}
	if value.MaxContentLightLevel < 0 {
		add(CodeHDRInvalid, path+"/max_content_light_level", "max content light level must not be negative")
	}
	if value.MaxFrameAverageLightLevel < 0 {
		add(CodeHDRInvalid, path+"/max_frame_average_light_level", "max frame-average light level must not be negative")
	}
}

func validateAudio(audio AudioStream, path string, add func(string, string, string)) {
	if audio.SampleRate <= 0 {
		add(CodeAudioInvalid, path+"/sample_rate", "sample_rate must be positive")
	}
	if audio.Channels <= 0 {
		add(CodeAudioInvalid, path+"/channels", "channels must be positive")
	}
	if strings.TrimSpace(audio.ChannelLayout) == "" {
		add(CodeAudioInvalid, path+"/channel_layout", "channel_layout is required")
	}
	if strings.TrimSpace(audio.SampleFormat) == "" {
		add(CodeAudioInvalid, path+"/sample_format", "sample_format is required")
	}
	if audio.BitsPerSample < 0 || audio.EncoderDelaySamples < 0 || audio.PaddingSamples < 0 {
		add(CodeAudioInvalid, path, "bits_per_sample, encoder_delay_samples and padding_samples must not be negative")
	}
}

func validateNonNegativeTime(value videoedit.RationalTime) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Value < 0 {
		return fmt.Errorf("time must not be negative")
	}
	return nil
}

func validDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func HasErrorDiagnostics(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}
