package ffmpegbackend

import (
	"context"
	"fmt"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// ProbeResolver loads a validated immutable ProbeManifest by its manifest fingerprint. The
// scheduler typically backs this with a content-addressed store.
type ProbeResolver interface {
	ResolveProbe(context.Context, string) (probe.ProbeManifest, error)
}

type ProbeResolverFunc func(context.Context, string) (probe.ProbeManifest, error)

func (function ProbeResolverFunc) ResolveProbe(ctx context.Context, fingerprint string) (probe.ProbeManifest, error) {
	return function(ctx, fingerprint)
}

type sourceProfile struct {
	video *streamProfile
	audio *streamProfile
}

type streamProfile struct {
	index       int
	timeBase    videoedit.RationalTime
	startPTS    int64
	durationPTS int64
}

func (backend *Backend) resolveSourceProfiles(ctx context.Context, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) (map[videoedit.ID]sourceProfile, []videoedit.Diagnostic) {
	profiles := make(map[videoedit.ID]sourceProfile, len(manifest.Resources))
	var diagnostics []videoedit.Diagnostic
	add := func(path, message string) {
		diagnostics = append(diagnostics, videoedit.Diagnostic{Code: CodeProbeUnsupported, Severity: videoedit.SeverityError, Path: path, Message: message})
	}
	if backend == nil || backend.probeResolver == nil {
		add("/resources", "probe resolver is unavailable")
		return profiles, diagnostics
	}
	videoAssets := make(map[videoedit.ID][]videoedit.PlannedClip)
	audioAssets := make(map[videoedit.ID][]videoedit.PlannedClip)
	for _, clip := range plan.Video {
		videoAssets[clip.AssetID] = append(videoAssets[clip.AssetID], clip)
	}
	for _, clip := range plan.Audio {
		audioAssets[clip.AssetID] = append(audioAssets[clip.AssetID], clip)
	}
	for index, resource := range manifest.Resources {
		path := fmt.Sprintf("/resources/%d/probe_fingerprint", index)
		manifestValue, err := backend.probeResolver.ResolveProbe(ctx, resource.ProbeFingerprint)
		if err != nil {
			add(path, fmt.Sprintf("resolve probe manifest: %v", err))
			continue
		}
		fingerprint, err := probe.Fingerprint(manifestValue)
		if err != nil || fingerprint != resource.ProbeFingerprint {
			add(path, "resolved probe manifest fingerprint does not match the execution manifest")
			continue
		}
		if manifestValue.Resource.AssetID != resource.AssetID || manifestValue.Resource.ContentHash != resource.ContentHash || manifestValue.Resource.SizeBytes != resource.SizeBytes {
			add(path, "probe manifest resource identity does not match materialized bytes")
			continue
		}
		if !safeContainerFormat(manifestValue.Container.FormatName) || !manifestValue.Container.Seekable {
			add(path, fmt.Sprintf("container %q is not in the hard-cut v1 safe demuxer profile", manifestValue.Container.FormatName))
			continue
		}
		profile := sourceProfile{}
		var videos, audios []probe.Stream
		for _, stream := range manifestValue.Streams {
			switch stream.Kind {
			case probe.StreamKindVideo:
				videos = append(videos, stream)
			case probe.StreamKindAudio:
				audios = append(audios, stream)
			}
		}
		if clips := videoAssets[resource.AssetID]; len(clips) > 0 {
			if len(videos) != 1 {
				add(path, fmt.Sprintf("video asset requires exactly one video stream, got %d", len(videos)))
			} else if err := validateVideoProfile(videos[0], clips); err != nil {
				add(path, err.Error())
			} else {
				profile.video = profileFromStream(videos[0])
			}
		}
		if clips := audioAssets[resource.AssetID]; len(clips) > 0 {
			if len(audios) != 1 {
				add(path, fmt.Sprintf("audio asset requires exactly one audio stream, got %d", len(audios)))
			} else if err := validateAudioProfile(audios[0], clips); err != nil {
				add(path, err.Error())
			} else {
				profile.audio = profileFromStream(audios[0])
			}
		}
		profiles[resource.AssetID] = profile
	}
	return profiles, diagnostics
}

func safeContainerFormat(value string) bool {
	seen := false
	for _, name := range strings.Split(value, ",") {
		switch strings.TrimSpace(name) {
		case "mov", "mp4", "m4a", "3gp", "3g2", "mj2", "matroska", "webm", "wav", "flac", "ogg":
			seen = true
		default:
			return false
		}
	}
	return seen
}

func validateVideoProfile(stream probe.Stream, clips []videoedit.PlannedClip) error {
	if stream.Video == nil || stream.DurationPTS == nil {
		return fmt.Errorf("video stream requires exact duration evidence")
	}
	video := stream.Video
	if stream.StartPTS != 0 {
		return fmt.Errorf("hard-cut v1 rejects non-zero or negative video start PTS")
	}
	if video.FrameRateMode != probe.FrameRateCFR || video.AverageFrameRate == nil {
		return fmt.Errorf("hard-cut v1 requires a proven CFR video stream")
	}
	if video.RotationDegrees != 0 || video.HDR != nil || video.HasAlpha {
		return fmt.Errorf("hard-cut v1 requires unrotated opaque SDR video")
	}
	if video.Color.Primaries != "bt709" || video.Color.Transfer != "bt709" || video.Color.Matrix != "bt709" || video.Color.Range != probe.ColorRangeLimited {
		return fmt.Errorf("hard-cut v1 requires explicit limited-range BT.709 source metadata")
	}
	if ratio := video.SampleAspectRatio; ratio != nil && (ratio.Numerator != ratio.Denominator) {
		return fmt.Errorf("hard-cut v1 requires square-pixel video")
	}
	frameDuration, err := (videoedit.RationalTime{
		Value: video.AverageFrameRate.Rate,
		Rate:  video.AverageFrameRate.Value,
	}).Normalize()
	if err != nil || frameDuration.Value <= 0 {
		return fmt.Errorf("hard-cut v1 requires an exact positive CFR frame duration")
	}
	return validateClipBounds(stream, clips, frameDuration, "CFR frame")
}

func validateAudioProfile(stream probe.Stream, clips []videoedit.PlannedClip) error {
	if stream.Audio == nil || stream.DurationPTS == nil {
		return fmt.Errorf("audio stream requires exact duration evidence")
	}
	if stream.StartPTS != 0 {
		return fmt.Errorf("hard-cut v1 rejects non-zero or negative audio start PTS")
	}
	if stream.Audio.EncoderDelaySamples != 0 || stream.Audio.PaddingSamples != 0 {
		return fmt.Errorf("hard-cut v1 rejects source encoder delay or padding")
	}
	if stream.Audio.SampleRate <= 0 {
		return fmt.Errorf("hard-cut v1 requires a positive audio sample rate")
	}
	return validateClipBounds(
		stream,
		clips,
		videoedit.RationalTime{Value: 1, Rate: stream.Audio.SampleRate},
		"audio sample",
	)
}

func validateClipBounds(stream probe.Stream, clips []videoedit.PlannedClip, mediaUnit videoedit.RationalTime, unitName string) error {
	for _, clip := range clips {
		_, start, end, err := exactTickBounds(clip.VisibleSourceRange, stream.TimeBase)
		if err != nil {
			return fmt.Errorf("clip %q source range is not exact in stream time base: %v", clip.ClipID, err)
		}
		if start < stream.StartPTS || end > stream.StartPTS+*stream.DurationPTS {
			return fmt.Errorf("clip %q source range is outside the probed stream", clip.ClipID)
		}
		if _, _, _, err := exactTickBounds(clip.VisibleSourceRange, mediaUnit); err != nil {
			return fmt.Errorf("clip %q source range is not exact on the %s grid: %v", clip.ClipID, unitName, err)
		}
	}
	return nil
}

func profileFromStream(stream probe.Stream) *streamProfile {
	return &streamProfile{index: stream.Index, timeBase: stream.TimeBase, startPTS: stream.StartPTS, durationPTS: *stream.DurationPTS}
}
