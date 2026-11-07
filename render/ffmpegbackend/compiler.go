package ffmpegbackend

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

type commandSpec struct {
	Arguments  []string
	InputPaths []string
}

// videoFrameWindow is derived through the semantic evaluator before either export or exact-frame
// preview compiles FFmpeg filters. First is a composition-wide index; Offset is relative to the
// beginning of the resolved plan's frame range.
type videoFrameWindow struct {
	First      int64
	Offset     int64
	Count      int64
	Evaluation videoedit.FrameEvaluation
}

// compileCommand creates a deterministic argv vector. All variable filter values have already
// been reduced to integers or allowlisted tokens; file paths remain separate argv entries.
func compileCommand(request renderapi.RenderRequest, temporaryOutput string, profiles map[videoedit.ID]sourceProfile) (commandSpec, error) {
	if request.Job.FrameRange != request.Plan.FrameRange || request.Job.SampleRange != request.Plan.SampleRange {
		return commandSpec{}, fmt.Errorf("%w: hard-cut v1 renders complete plans only", ErrPreflight)
	}
	resources := append([]renderapi.MaterializedResource(nil), request.Manifest.Resources...)
	sort.Slice(resources, func(i, j int) bool { return resources[i].AssetID < resources[j].AssetID })
	inputIndex := make(map[videoedit.ID]int, len(resources))
	arguments := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-y"}
	inputs := make([]string, 0, len(resources))
	for index, resource := range resources {
		inputIndex[resource.AssetID] = index
		inputs = append(inputs, resource.Locator)
		arguments = append(arguments,
			"-protocol_whitelist", "file,fd",
			"-noautorotate",
			"-i", resource.Locator,
		)
	}

	window, err := evaluateVideoFrameWindow(request.Plan, request.Job.FrameRange.First, request.Job.FrameRange.Count)
	if err != nil {
		return commandSpec{}, err
	}
	videoFilters, err := compileVideoFilters(request.Plan, request.Manifest, inputIndex, profiles, window, request.Manifest.Output.PixelFormat)
	if err != nil {
		return commandSpec{}, err
	}
	audioFilters, err := compileAudioFilters(request, inputIndex, profiles)
	if err != nil {
		return commandSpec{}, err
	}
	filterComplex := strings.Join(append(videoFilters, audioFilters...), ";")
	frameRate := rationalArgument(request.Plan.Target.FrameClock.FramesPerSecond)
	videoCodec := request.Manifest.Output.VideoCodec
	if videoCodec == "h264" {
		videoCodec = "libx264"
	}
	arguments = append(arguments,
		"-filter_complex", filterComplex,
		"-map", "[vout]",
		"-map", "[aout]",
		"-frames:v", strconv.FormatInt(request.Job.FrameRange.Count, 10),
		"-c:v", videoCodec,
		"-pix_fmt", request.Manifest.Output.PixelFormat,
		"-r", frameRate,
		"-preset", "medium",
		"-threads", "1",
		"-c:a", request.Manifest.Output.AudioCodec,
		"-ar", strconv.FormatInt(request.Manifest.Output.SampleRate, 10),
		"-ac", strconv.Itoa(request.Manifest.Output.Channels),
		"-map_metadata", "-1",
		"-map_chapters", "-1",
		"-metadata", "creation_time=1970-01-01T00:00:00Z",
		"-fflags", "+bitexact",
		"-flags:v", "+bitexact",
		"-flags:a", "+bitexact",
		"-f", "mp4",
		temporaryOutput,
	)
	return commandSpec{Arguments: arguments, InputPaths: inputs}, nil
}

// compileExactFrameCommand uses the same evaluated window and video-filter compiler as MP4
// export. It emits one PNG directly from the render surface; it never renders or reopens an MP4.
func compileExactFrameCommand(request renderapi.ExactFrameRenderRequest, temporaryOutput string, profiles map[videoedit.ID]sourceProfile) (commandSpec, videoedit.FrameEvaluation, error) {
	resources := append([]renderapi.MaterializedResource(nil), request.Manifest.Resources...)
	sort.Slice(resources, func(i, j int) bool { return resources[i].AssetID < resources[j].AssetID })
	inputIndex := make(map[videoedit.ID]int, len(resources))
	arguments := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-y"}
	inputs := make([]string, 0, len(resources))
	for index, resource := range resources {
		inputIndex[resource.AssetID] = index
		inputs = append(inputs, resource.Locator)
		arguments = append(arguments,
			"-protocol_whitelist", "file,fd",
			"-noautorotate",
			"-i", resource.Locator,
		)
	}
	window, err := evaluateVideoFrameWindow(request.Plan, request.Frame.CompositionFrameIndex, 1)
	if err != nil {
		return commandSpec{}, videoedit.FrameEvaluation{}, err
	}
	videoFilters, err := compileVideoFilters(request.Plan, request.Manifest, inputIndex, profiles, window, "rgba")
	if err != nil {
		return commandSpec{}, videoedit.FrameEvaluation{}, err
	}
	arguments = append(arguments,
		"-filter_complex", strings.Join(videoFilters, ";"),
		"-map", "[vout]",
		"-an",
		"-frames:v", "1",
		"-c:v", "png",
		"-pix_fmt", "rgba",
		"-pred", "mixed",
		"-compression_level", "9",
		"-threads", "1",
		"-map_metadata", "-1",
		"-map_chapters", "-1",
		"-f", "image2pipe",
		temporaryOutput,
	)
	return commandSpec{Arguments: arguments, InputPaths: inputs}, window.Evaluation, nil
}

func evaluateVideoFrameWindow(plan videoedit.RenderPlan, first, count int64) (videoFrameWindow, error) {
	if count <= 0 {
		return videoFrameWindow{}, fmt.Errorf("%w: video frame window must be positive", ErrPreflight)
	}
	rangeValue := videoedit.TickRange{First: first, Count: count}
	end, err := rangeValue.EndExclusive()
	if err != nil {
		return videoFrameWindow{}, fmt.Errorf("%w: video frame window: %v", ErrPreflight, err)
	}
	planEnd, err := plan.FrameRange.EndExclusive()
	if err != nil || first < plan.FrameRange.First || end > planEnd {
		return videoFrameWindow{}, fmt.Errorf("%w: video frame window is outside the plan", ErrPreflight)
	}
	verified, err := videoedit.VerifyRenderPlan(plan)
	if err != nil {
		return videoFrameWindow{}, fmt.Errorf("%w: verify plan for video window: %v", ErrPreflight, err)
	}
	evaluation, err := videoedit.EvaluateVerifiedFrame(verified, first)
	if err != nil {
		return videoFrameWindow{}, fmt.Errorf("%w: evaluate first video frame: %v", ErrPreflight, err)
	}
	if len(evaluation.Video) != 1 {
		return videoFrameWindow{}, fmt.Errorf("%w: hard-cut video window requires exactly one evaluated layer, got %d", ErrPreflight, len(evaluation.Video))
	}
	if count > 1 {
		last, evaluateErr := videoedit.EvaluateVerifiedFrame(verified, end-1)
		if evaluateErr != nil || len(last.Video) != 1 {
			return videoFrameWindow{}, fmt.Errorf("%w: evaluate last video frame: %v", ErrPreflight, evaluateErr)
		}
	}
	return videoFrameWindow{First: first, Offset: first - plan.FrameRange.First, Count: count, Evaluation: evaluation}, nil
}

func compileVideoFilters(plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest, inputIndex map[videoedit.ID]int, profiles map[videoedit.ID]sourceProfile, window videoFrameWindow, pixelFormat string) ([]string, error) {
	filters := make([]string, 0, len(plan.Video)+2)
	for index, clip := range plan.Video {
		input, exists := inputIndex[clip.AssetID]
		if !exists {
			return nil, fmt.Errorf("%w: video clip %q has no input", ErrPreflight, clip.ClipID)
		}
		profile, exists := profiles[clip.AssetID]
		if !exists || profile.video == nil {
			return nil, fmt.Errorf("%w: video clip %q has no verified stream profile", ErrPreflight, clip.ClipID)
		}
		timeBase, startPTS, endPTS, err := exactTickBounds(clip.VisibleSourceRange, profile.video.timeBase)
		if err != nil {
			return nil, fmt.Errorf("%w: video clip %q source range: %v", ErrPreflight, clip.ClipID, err)
		}
		filters = append(filters, fmt.Sprintf(
			"[%d:%d]settb=expr=%s,trim=start_pts=%d:end_pts=%d,setpts=PTS-STARTPTS,scale=w=%d:h=%d:flags=bicubic,setsar=1,format=pix_fmts=yuv420p[v%d]",
			input,
			profile.video.index,
			rationalArgument(timeBase),
			startPTS,
			endPTS,
			manifest.Output.Width,
			manifest.Output.Height,
			index,
		))
	}
	filters = append(filters, concatFilter("v", len(plan.Video), "vcat", "v=1:a=0"))
	trim := "trim=end_frame=" + strconv.FormatInt(window.Count, 10)
	if window.Offset != 0 {
		trim = "trim=start_frame=" + strconv.FormatInt(window.Offset, 10) + ":end_frame=" + strconv.FormatInt(window.Offset+window.Count, 10)
	}
	filters = append(filters, fmt.Sprintf(
		"[vcat]fps=fps=%s:start_time=0:round=near,%s,setpts=PTS-STARTPTS,format=pix_fmts=%s[vout]",
		rationalArgument(plan.Target.FrameClock.FramesPerSecond),
		trim,
		pixelFormat,
	))
	return filters, nil
}

func compileAudioFilters(request renderapi.RenderRequest, inputIndex map[videoedit.ID]int, profiles map[videoedit.ID]sourceProfile) ([]string, error) {
	filters := make([]string, 0, len(request.Plan.Audio)+2)
	layout := "mono"
	if request.Manifest.Output.Channels == 2 {
		layout = "stereo"
	}
	for index, clip := range request.Plan.Audio {
		input, exists := inputIndex[clip.AssetID]
		if !exists {
			return nil, fmt.Errorf("%w: audio clip %q has no input", ErrPreflight, clip.ClipID)
		}
		profile, exists := profiles[clip.AssetID]
		if !exists || profile.audio == nil {
			return nil, fmt.Errorf("%w: audio clip %q has no verified stream profile", ErrPreflight, clip.ClipID)
		}
		timeBase, startPTS, endPTS, err := exactTickBounds(clip.VisibleSourceRange, profile.audio.timeBase)
		if err != nil {
			return nil, fmt.Errorf("%w: audio clip %q source range: %v", ErrPreflight, clip.ClipID, err)
		}
		filters = append(filters, fmt.Sprintf(
			"[%d:%d]asettb=expr=%s,atrim=start_pts=%d:end_pts=%d,asetpts=PTS-STARTPTS,aresample=%d:async=0:first_pts=0,aformat=sample_rates=%d:channel_layouts=%s[a%d]",
			input,
			profile.audio.index,
			rationalArgument(timeBase),
			startPTS,
			endPTS,
			request.Manifest.Output.SampleRate,
			request.Manifest.Output.SampleRate,
			layout,
			index,
		))
	}
	filters = append(filters, concatFilter("a", len(request.Plan.Audio), "acat", "v=0:a=1"))
	filters = append(filters, fmt.Sprintf(
		"[acat]atrim=start_sample=0:end_sample=%d,asetpts=PTS-STARTPTS[aout]",
		request.Job.SampleRange.Count,
	))
	return filters, nil
}

func concatFilter(prefix string, count int, output, mode string) string {
	if count == 1 {
		identity := "null"
		if prefix == "a" {
			identity = "anull"
		}
		return fmt.Sprintf("[%s0]%s[%s]", prefix, identity, output)
	}
	var builder strings.Builder
	for index := 0; index < count; index++ {
		fmt.Fprintf(&builder, "[%s%d]", prefix, index)
	}
	fmt.Fprintf(&builder, "concat=n=%d:%s[%s]", count, mode, output)
	return builder.String()
}

func rationalArgument(value videoedit.RationalTime) string {
	return strconv.FormatInt(value.Value, 10) + "/" + strconv.FormatInt(value.Rate, 10)
}

// exactTickBounds selects an exact FFmpeg filter time base for a source range, avoiding decimal
// time strings and their platform/version-dependent rounding.
func exactTickBounds(value videoedit.TimeRange, timeBase videoedit.RationalTime) (base videoedit.RationalTime, start, end int64, err error) {
	if err := value.Validate(); err != nil {
		return videoedit.RationalTime{}, 0, 0, err
	}
	if err := timeBase.Validate(); err != nil || timeBase.Value <= 0 {
		return videoedit.RationalTime{}, 0, 0, videoedit.ErrInvalidRate
	}
	startTime := new(big.Rat).SetFrac64(value.Start.Value, value.Start.Rate)
	duration := new(big.Rat).SetFrac64(value.Duration.Value, value.Duration.Rate)
	baseTime := new(big.Rat).SetFrac64(timeBase.Value, timeBase.Rate)
	startTicks := new(big.Rat).Quo(startTime, baseTime)
	endTicks := new(big.Rat).Quo(new(big.Rat).Add(startTime, duration), baseTime)
	if !startTicks.IsInt() || !endTicks.IsInt() {
		return videoedit.RationalTime{}, 0, 0, videoedit.ErrInexactRescale
	}
	if !startTicks.Num().IsInt64() || !endTicks.Num().IsInt64() {
		return videoedit.RationalTime{}, 0, 0, videoedit.ErrTimeOverflow
	}
	return timeBase, startTicks.Num().Int64(), endTicks.Num().Int64(), nil
}
