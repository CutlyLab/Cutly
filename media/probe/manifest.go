// Package probe defines Cutly's versioned, backend-neutral media probe contract.
//
// Adapters such as FFprobe translate untrusted tool output into this manifest. Render planning and
// workers consume only a validated ProbeManifest, never tool-specific JSON.
package probe

import "github.com/CutlyLab/Cutly/core/videoedit"

const SchemaVersion = 1

// StreamKind is the media kind understood by the first native renderer.
type StreamKind string

const (
	StreamKindVideo StreamKind = "video"
	StreamKindAudio StreamKind = "audio"
)

// FrameRateMode describes whether video presentation timestamps follow a constant grid.
type FrameRateMode string

const (
	FrameRateUnknown FrameRateMode = "unknown"
	FrameRateCFR     FrameRateMode = "cfr"
	FrameRateVFR     FrameRateMode = "vfr"
)

// ColorRange describes encoded YUV/RGB range semantics.
type ColorRange string

const (
	ColorRangeUnknown ColorRange = "unknown"
	ColorRangeLimited ColorRange = "limited"
	ColorRangeFull    ColorRange = "full"
)

// ProbeManifest freezes media facts for one immutable resource and one pinned probe build/options
// profile. Its fingerprint is suitable for cache keys.
type ProbeManifest struct {
	SchemaVersion int                    `json:"schema_version"`
	Resource      ResourceIdentity       `json:"resource"`
	Probe         ProbeIdentity          `json:"probe"`
	Container     ContainerInfo          `json:"container"`
	Duration      videoedit.RationalTime `json:"duration"`
	Streams       []Stream               `json:"streams"`
}

// ResourceIdentity binds observations to immutable input bytes.
type ResourceIdentity struct {
	AssetID     videoedit.ID `json:"asset_id"`
	ContentHash string       `json:"content_hash"`
	SizeBytes   int64        `json:"size_bytes"`
}

// ProbeIdentity prevents results from being reused across different parser builds or options.
type ProbeIdentity struct {
	Implementation     string `json:"implementation"`
	BuildFingerprint   string `json:"build_fingerprint"`
	OptionsFingerprint string `json:"options_fingerprint"`
}

type ContainerInfo struct {
	FormatName string                 `json:"format_name"`
	StartTime  videoedit.RationalTime `json:"start_time"`
	BitRate    int64                  `json:"bit_rate,omitempty"`
	Seekable   bool                   `json:"seekable"`
}

// Stream carries exact container timestamps plus exactly one kind-specific payload.
type Stream struct {
	Index       int                    `json:"index"`
	Kind        StreamKind             `json:"kind"`
	Codec       string                 `json:"codec"`
	TimeBase    videoedit.RationalTime `json:"time_base"`
	StartPTS    int64                  `json:"start_pts"`
	DurationPTS *int64                 `json:"duration_pts,omitempty"`
	Video       *VideoStream           `json:"video,omitempty"`
	Audio       *AudioStream           `json:"audio,omitempty"`
}

type VideoStream struct {
	Width              int                     `json:"width"`
	Height             int                     `json:"height"`
	RotationDegrees    int                     `json:"rotation_degrees"`
	SampleAspectRatio  *Ratio                  `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio *Ratio                  `json:"display_aspect_ratio,omitempty"`
	PixelFormat        string                  `json:"pixel_format"`
	HasAlpha           bool                    `json:"has_alpha"`
	FrameRateMode      FrameRateMode           `json:"frame_rate_mode"`
	AverageFrameRate   *videoedit.RationalTime `json:"average_frame_rate,omitempty"`
	Keyframes          KeyframeSummary         `json:"keyframes"`
	Color              ColorMetadata           `json:"color"`
	HDR                *HDRMetadata            `json:"hdr,omitempty"`
}

type Ratio struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// KeyframeSummary records scan completeness without embedding an unbounded keyframe index. A later
// index artifact can be addressed by IndexHash.
type KeyframeSummary struct {
	Complete  bool   `json:"complete"`
	Count     int64  `json:"count"`
	FirstPTS  *int64 `json:"first_pts,omitempty"`
	LastPTS   *int64 `json:"last_pts,omitempty"`
	IndexHash string `json:"index_hash,omitempty"`
}

type ColorMetadata struct {
	Primaries      string     `json:"primaries,omitempty"`
	Transfer       string     `json:"transfer,omitempty"`
	Matrix         string     `json:"matrix,omitempty"`
	Range          ColorRange `json:"range"`
	ChromaLocation string     `json:"chroma_location,omitempty"`
}

type HDRMetadata struct {
	Format                    string `json:"format"`
	MasteringDisplay          string `json:"mastering_display,omitempty"`
	MaxContentLightLevel      int64  `json:"max_content_light_level,omitempty"`
	MaxFrameAverageLightLevel int64  `json:"max_frame_average_light_level,omitempty"`
}

type AudioStream struct {
	SampleRate          int64  `json:"sample_rate"`
	Channels            int    `json:"channels"`
	ChannelLayout       string `json:"channel_layout"`
	SampleFormat        string `json:"sample_format"`
	BitsPerSample       int    `json:"bits_per_sample,omitempty"`
	EncoderDelaySamples int64  `json:"encoder_delay_samples"`
	PaddingSamples      int64  `json:"padding_samples"`
}
