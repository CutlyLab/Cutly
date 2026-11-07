package ffprobe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
)

type rawFrameOutput struct {
	Frames []rawFrame `json:"frames"`
}

type rawFrame struct {
	StreamIndex         int             `json:"stream_index"`
	BestEffortTimestamp json.RawMessage `json:"best_effort_timestamp"`
	PTS                 json.RawMessage `json:"pts"`
	PacketDuration      json.RawMessage `json:"pkt_duration"`
	KeyFrame            json.RawMessage `json:"key_frame"`
}

type frameObservation struct {
	pts         []int64
	durations   []*int64
	keyframePTS []int64
	complete    bool
}

func manifestHasVideo(manifest probe.ProbeManifest) bool {
	for _, stream := range manifest.Streams {
		if stream.Kind == probe.StreamKindVideo {
			return true
		}
	}
	return false
}

// applyFrameScan promotes unknown container-rate metadata only when a complete decoded-frame scan
// proves the exact presentation grid. A successful command is not itself proof: every selected
// frame must carry a usable display timestamp and keyframe flag, and CFR additionally requires
// exact start/end closure against the stream clock.
func applyFrameScan(manifest *probe.ProbeManifest, raw []byte) error {
	if manifest == nil {
		return fmt.Errorf("%w: nil manifest for frame scan", ErrParse)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var output rawFrameOutput
	if err := decoder.Decode(&output); err != nil {
		return fmt.Errorf("%w: frame scan JSON: %v", ErrParse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: frame scan has multiple JSON values", ErrParse)
		}
		return fmt.Errorf("%w: trailing frame scan JSON: %v", ErrParse, err)
	}

	videoStreams := make(map[int]*probe.Stream)
	observations := make(map[int]*frameObservation)
	for index := range manifest.Streams {
		stream := &manifest.Streams[index]
		if stream.Kind == probe.StreamKindVideo && stream.Video != nil {
			stream.Video.FrameRateMode = probe.FrameRateUnknown
			stream.Video.Keyframes = probe.KeyframeSummary{}
			videoStreams[stream.Index] = stream
			observations[stream.Index] = &frameObservation{complete: true}
		}
	}
	for _, frame := range output.Frames {
		observation, selected := observations[frame.StreamIndex]
		if !selected {
			continue
		}
		pts, present, err := parseOptionalInt64(frame.BestEffortTimestamp)
		if err != nil {
			return fmt.Errorf("%w: frame stream %d best_effort_timestamp: %v", ErrParse, frame.StreamIndex, err)
		}
		if !present {
			pts, present, err = parseOptionalInt64(frame.PTS)
			if err != nil {
				return fmt.Errorf("%w: frame stream %d pts: %v", ErrParse, frame.StreamIndex, err)
			}
		}
		key, keyPresent, err := parseOptionalInt64(frame.KeyFrame)
		if err != nil || (keyPresent && *key != 0 && *key != 1) {
			return fmt.Errorf("%w: frame stream %d key_frame is invalid", ErrParse, frame.StreamIndex)
		}
		duration, durationPresent, err := parseOptionalInt64(frame.PacketDuration)
		if err != nil {
			return fmt.Errorf("%w: frame stream %d pkt_duration: %v", ErrParse, frame.StreamIndex, err)
		}
		if !present || !keyPresent {
			observation.complete = false
			continue
		}
		observation.pts = append(observation.pts, *pts)
		if durationPresent && *duration > 0 {
			value := *duration
			observation.durations = append(observation.durations, &value)
		} else {
			observation.durations = append(observation.durations, nil)
		}
		if *key == 1 {
			observation.keyframePTS = append(observation.keyframePTS, *pts)
		}
	}

	for streamIndex, stream := range videoStreams {
		observation := observations[streamIndex]
		if observation == nil || !observation.complete || len(observation.pts) == 0 {
			continue
		}
		ordered := true
		for index := 1; index < len(observation.pts); index++ {
			if observation.pts[index] <= observation.pts[index-1] {
				ordered = false
				break
			}
		}
		if !ordered {
			continue
		}

		stream.Video.Keyframes = completeKeyframeSummary(streamIndex, observation.keyframePTS)
		if stream.DurationPTS == nil || stream.Video.AverageFrameRate == nil || observation.pts[0] != stream.StartPTS {
			continue
		}
		streamEnd, ok := checkedAdd(stream.StartPTS, *stream.DurationPTS)
		if !ok {
			continue
		}
		step, exact := exactFrameStep(*stream.Video.AverageFrameRate, stream.TimeBase)
		constant := exact && step > 0
		if constant {
			for index := 1; index < len(observation.pts); index++ {
				if observation.pts[index]-observation.pts[index-1] != step {
					constant = false
					break
				}
			}
		}
		if constant {
			lastEnd, addOK := checkedAdd(observation.pts[len(observation.pts)-1], step)
			if addOK && lastEnd == streamEnd {
				stream.Video.FrameRateMode = probe.FrameRateCFR
				continue
			}
		}

		// VFR is asserted only when the complete scan also proves the final frame interval.
		// Missing/zero packet duration is common in weak container metadata and remains unknown.
		lastDuration := observation.durations[len(observation.durations)-1]
		if lastDuration == nil {
			continue
		}
		lastEnd, addOK := checkedAdd(observation.pts[len(observation.pts)-1], *lastDuration)
		if addOK && lastEnd == streamEnd {
			stream.Video.FrameRateMode = probe.FrameRateVFR
		}
	}
	return nil
}

func exactFrameStep(rate, timeBase videoedit.RationalTime) (int64, bool) {
	if err := rate.Validate(); err != nil || rate.Value <= 0 {
		return 0, false
	}
	if err := timeBase.Validate(); err != nil || timeBase.Value <= 0 {
		return 0, false
	}
	// (rate.Rate/rate.Value seconds per frame) / (timeBase.Value/timeBase.Rate seconds per tick).
	step := new(big.Rat).Mul(
		new(big.Rat).SetFrac64(rate.Rate, rate.Value),
		new(big.Rat).SetFrac64(timeBase.Rate, timeBase.Value),
	)
	if !step.IsInt() || !step.Num().IsInt64() {
		return 0, false
	}
	return step.Num().Int64(), true
}

func completeKeyframeSummary(streamIndex int, values []int64) probe.KeyframeSummary {
	copyValues := append([]int64(nil), values...)
	canonical, _ := videoedit.CanonicalJSON(struct {
		Profile     string  `json:"profile"`
		StreamIndex int     `json:"stream_index"`
		PTS         []int64 `json:"pts"`
	}{Profile: "cutly.keyframe-index.v1", StreamIndex: streamIndex, PTS: copyValues})
	digest := sha256.Sum256(canonical)
	result := probe.KeyframeSummary{
		Complete:  true,
		Count:     lenToInt64(len(copyValues)),
		IndexHash: "sha256:" + hex.EncodeToString(digest[:]),
	}
	if len(copyValues) > 0 {
		first, last := copyValues[0], copyValues[len(copyValues)-1]
		result.FirstPTS = &first
		result.LastPTS = &last
	}
	return result
}

func lenToInt64(value int) int64 {
	if uint64(value) > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func checkedAdd(left, right int64) (int64, bool) {
	if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
		return 0, false
	}
	return left + right, true
}
