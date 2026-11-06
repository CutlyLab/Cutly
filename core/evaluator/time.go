package evaluator

import (
	"math/big"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// ClipLocalTime maps absolute timeline time onto a clip-relative clock. It intentionally accepts
// negative values and values at/after clip duration: transition head/tail handles use those exact
// offsets while keyframe evaluation applies endpoint clamping.
func ClipLocalTime(clip documentv2.Clip, timelineTime documentv2.RationalTime) (documentv2.RationalTime, error) {
	if err := clip.TimelineRange.Validate(); err != nil || clip.TimelineRange.Duration.Value <= 0 {
		return documentv2.RationalTime{}, evaluationError(CodeInputInvalid, "clip.timeline_range", ErrInputInvalid)
	}
	if err := timelineTime.Validate(); err != nil {
		return documentv2.RationalTime{}, evaluationError(CodeInputInvalid, "timeline_time", ErrInputInvalid)
	}
	result, err := timelineTime.Sub(clip.TimelineRange.Start)
	if err != nil {
		return documentv2.RationalTime{}, evaluationError(CodeTimeOverflow, "clip.local_time", ErrTimeOverflow)
	}
	return result, nil
}

// SourceTime applies the Document v2 exact linear retime equation. Like ClipLocalTime it permits
// transition handle positions outside the visible SourceRange; document/media preflight is
// responsible for proving those positions exist.
func SourceTime(clip documentv2.Clip, timelineTime documentv2.RationalTime) (documentv2.RationalTime, error) {
	if err := clip.SourceRange.Validate(); err != nil || clip.SourceRange.Duration.Value <= 0 {
		return documentv2.RationalTime{}, evaluationError(CodeInputInvalid, "clip.source_range", ErrInputInvalid)
	}
	local, err := ClipLocalTime(clip, timelineTime)
	if err != nil {
		return documentv2.RationalTime{}, err
	}
	localValue := new(big.Rat).SetFrac64(local.Value, local.Rate)
	sourceDuration := new(big.Rat).SetFrac64(clip.SourceRange.Duration.Value, clip.SourceRange.Duration.Rate)
	timelineDuration := new(big.Rat).SetFrac64(clip.TimelineRange.Duration.Value, clip.TimelineRange.Duration.Rate)
	offset := new(big.Rat).Mul(localValue, new(big.Rat).Quo(sourceDuration, timelineDuration))
	result := new(big.Rat).Add(
		new(big.Rat).SetFrac64(clip.SourceRange.Start.Value, clip.SourceRange.Start.Rate),
		offset,
	)
	return timeFromBig(result, "clip.source_time")
}

func timeFromBig(value *big.Rat, path string) (documentv2.RationalTime, error) {
	if value == nil || !value.Num().IsInt64() || !value.Denom().IsInt64() {
		return documentv2.RationalTime{}, evaluationError(CodeTimeOverflow, path, ErrTimeOverflow)
	}
	result := documentv2.RationalTime{Value: value.Num().Int64(), Rate: value.Denom().Int64()}
	if err := result.Validate(); err != nil {
		return documentv2.RationalTime{}, evaluationError(CodeTimeOverflow, path, ErrTimeOverflow)
	}
	return result, nil
}

func timeBig(value documentv2.RationalTime) (*big.Rat, error) {
	if err := value.Validate(); err != nil {
		return nil, evaluationError(CodeInputInvalid, "time", ErrInputInvalid)
	}
	return new(big.Rat).SetFrac64(value.Value, value.Rate), nil
}
