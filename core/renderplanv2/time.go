package renderplanv2

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
)

func positive(r documentv2.TimeRange) bool        { return r.Validate() == nil && r.Duration.Value > 0 }
func less(a, b documentv2.RationalTime) bool      { n, e := a.Compare(b); return e == nil && n < 0 }
func equalTime(a, b documentv2.RationalTime) bool { n, e := a.Compare(b); return e == nil && n == 0 }

func intersection(a, b documentv2.TimeRange) (documentv2.TimeRange, bool, error) {
	if err := a.Validate(); err != nil {
		return documentv2.TimeRange{}, false, err
	}
	if err := b.Validate(); err != nil {
		return documentv2.TimeRange{}, false, err
	}
	start := a.Start
	if less(start, b.Start) {
		start = b.Start
	}
	ae, _ := a.End()
	be, _ := b.End()
	end := ae
	if less(be, end) {
		end = be
	}
	if !less(start, end) {
		return documentv2.TimeRange{}, false, nil
	}
	start, err := start.Normalize()
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	duration, err := end.Sub(start)
	if err != nil {
		return documentv2.TimeRange{}, false, err
	}
	return documentv2.TimeRange{Start: start, Duration: duration}, true, nil
}
func projected(b ClipBinding, r documentv2.TimeRange) (documentv2.TimeRange, error) {
	clip := documentv2.Clip{TimelineRange: b.TimelineRange, SourceRange: b.SourceRange}
	start, err := evaluator.SourceTime(clip, r.Start)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	end, err := r.End()
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	sourceEnd, err := evaluator.SourceTime(clip, end)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	duration, err := sourceEnd.Sub(start)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	return documentv2.TimeRange{Start: start, Duration: duration}, nil
}
func temporal(b ClipBinding) bool {
	return b.AssetKind == documentv2.AssetKindVideo || b.AssetKind == documentv2.AssetKindAudio
}
func participant(b ClipBinding, active documentv2.TimeRange) (TransitionParticipant, error) {
	source, err := projected(b, active)
	if err != nil {
		return TransitionParticipant{}, err
	}
	p := TransitionParticipant{Binding: b, SampledTimelineRange: active, SampledSourceRange: source, Handles: []HandleSpan{}}
	if !temporal(b) {
		return p, nil
	}
	end, _ := active.End()
	clipEnd, _ := b.TimelineRange.End()
	add := func(kind HandleKind, start, finish documentv2.RationalTime) error {
		if !less(start, finish) {
			return nil
		}
		start, err := start.Normalize()
		if err != nil {
			return err
		}
		duration, err := finish.Sub(start)
		if err != nil {
			return err
		}
		r := documentv2.TimeRange{Start: start, Duration: duration}
		src, err := projected(b, r)
		if err != nil {
			return err
		}
		p.Handles = append(p.Handles, HandleSpan{Kind: kind, TimelineRange: r, SourceRange: src})
		return nil
	}
	if less(active.Start, b.TimelineRange.Start) {
		finish := end
		if less(b.TimelineRange.Start, finish) {
			finish = b.TimelineRange.Start
		}
		if err = add(HandleHead, active.Start, finish); err != nil {
			return TransitionParticipant{}, err
		}
	}
	if less(clipEnd, end) {
		start := active.Start
		if less(start, clipEnd) {
			start = clipEnd
		}
		if err = add(HandleTail, start, end); err != nil {
			return TransitionParticipant{}, err
		}
	}
	return p, nil
}
func transitionRange(cut, pre, post documentv2.RationalTime) (documentv2.TimeRange, error) {
	start, err := cut.Sub(pre)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	duration, err := pre.Add(post)
	if err != nil {
		return documentv2.TimeRange{}, err
	}
	r := documentv2.TimeRange{Start: start, Duration: duration}
	return r, r.Validate()
}
