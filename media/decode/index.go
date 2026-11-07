package videodecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	FrameIndexSchemaVersion = 1
	FrameIndexProfileV1     = "cutly.media.video-frame-index.v1"
	MaxFrameIndexBytes      = 64 << 20
	DefaultMaxIndexedFrames = 1 << 20
	HardMaxIndexedFrames    = 4 << 20
)

// FrameRecord is one exact display interval in stream time-base ticks.
type FrameRecord struct {
	PTS         int64 `json:"pts"`
	DurationPTS int64 `json:"duration_pts"`
	Keyframe    bool  `json:"keyframe"`
}

// FrameIndex is the complete ordered display-PTS index required for exact VFR
// selection. ProbeFingerprint binds all probe semantics and runtime identity.
type FrameIndex struct {
	SchemaVersion    int                    `json:"schema_version"`
	Profile          string                 `json:"profile"`
	ProbeFingerprint string                 `json:"probe_fingerprint"`
	ResourceHash     string                 `json:"resource_hash"`
	StreamIndex      int                    `json:"stream_index"`
	TimeBase         videoedit.RationalTime `json:"time_base"`
	Frames           []FrameRecord          `json:"frames"`
}

type Selection struct {
	FrameOrdinal int         `json:"frame_ordinal"`
	Frame        FrameRecord `json:"frame"`
	SeekPTS      int64       `json:"seek_pts"`
}

// NewFrameIndex freezes caller-observed complete frame intervals against one
// already-validated ProbeManifest. Discovery is intentionally outside this
// package: callers must obtain these records from a pinned, complete frame
// scan rather than synthesize CFR timestamps from average rate metadata.
func NewFrameIndex(ctx context.Context, manifest probe.ProbeManifest, streamIndex int, frames []FrameRecord, maximumFrames int) (FrameIndex, string, error) {
	if ctx == nil {
		return FrameIndex{}, "", fmt.Errorf("%w: context is required", ErrIndexInvalid)
	}
	manifestFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		return FrameIndex{}, "", fmt.Errorf("%w: invalid probe manifest: %v", ErrIndexInvalid, err)
	}
	var timeBase videoedit.RationalTime
	found := false
	for position := range manifest.Streams {
		if manifest.Streams[position].Index == streamIndex && manifest.Streams[position].Kind == probe.StreamKindVideo {
			timeBase = manifest.Streams[position].TimeBase
			found = true
			break
		}
	}
	if !found {
		return FrameIndex{}, "", fmt.Errorf("%w: video stream %d is absent", ErrIndexMismatch, streamIndex)
	}
	index := FrameIndex{
		SchemaVersion: FrameIndexSchemaVersion, Profile: FrameIndexProfileV1,
		ProbeFingerprint: manifestFingerprint, ResourceHash: manifest.Resource.ContentHash,
		StreamIndex: streamIndex, TimeBase: timeBase, Frames: append([]FrameRecord(nil), frames...),
	}
	if err := BindFrameIndex(ctx, manifest, index, maximumFrames); err != nil {
		return FrameIndex{}, "", err
	}
	fingerprint, err := FrameIndexFingerprint(index, maximumFrames)
	if err != nil {
		return FrameIndex{}, "", err
	}
	return index, fingerprint, nil
}

func DecodeFrameIndex(raw []byte, maximumFrames int) (FrameIndex, error) {
	if len(raw) == 0 || len(raw) > MaxFrameIndexBytes {
		return FrameIndex{}, fmt.Errorf("%w: encoded bytes %d exceed [1,%d]", ErrIndexInvalid, len(raw), MaxFrameIndexBytes)
	}
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = MaxFrameIndexBytes
	limits.MaxStringBytes = MaxFrameIndexBytes
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil {
		return FrameIndex{}, fmt.Errorf("%w: canonical JSON: %v", ErrIndexInvalid, err)
	}
	var index FrameIndex
	if err := wirejson.Decode(canonical, &index); err != nil {
		return FrameIndex{}, fmt.Errorf("%w: decode: %v", ErrIndexInvalid, err)
	}
	if err := ValidateFrameIndex(context.Background(), index, maximumFrames); err != nil {
		return FrameIndex{}, err
	}
	return cloneFrameIndex(index), nil
}

func CanonicalFrameIndex(index FrameIndex, maximumFrames int) ([]byte, error) {
	if err := ValidateFrameIndex(context.Background(), index, maximumFrames); err != nil {
		return nil, err
	}
	canonical, err := canonicaljson.Marshal(index)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %v", ErrIndexInvalid, err)
	}
	if len(canonical) > MaxFrameIndexBytes {
		return nil, fmt.Errorf("%w: canonical bytes %d exceed %d", ErrIndexInvalid, len(canonical), MaxFrameIndexBytes)
	}
	return canonical, nil
}

func FrameIndexFingerprint(index FrameIndex, maximumFrames int) (string, error) {
	canonical, err := CanonicalFrameIndex(index, maximumFrames)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func ValidateFrameIndex(ctx context.Context, index FrameIndex, maximumFrames int) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrIndexInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	maximumFrames, err := normalizeMaximumFrames(maximumFrames)
	if err != nil {
		return err
	}
	if index.SchemaVersion != FrameIndexSchemaVersion || index.Profile != FrameIndexProfileV1 ||
		!canonicalSHA256(index.ProbeFingerprint) || !canonicalSHA256(index.ResourceHash) || index.StreamIndex < 0 ||
		index.TimeBase.Validate() != nil || index.TimeBase.Value <= 0 || index.Frames == nil || len(index.Frames) == 0 ||
		len(index.Frames) > maximumFrames {
		return fmt.Errorf("%w: header or frame count", ErrIndexInvalid)
	}
	foundKeyframe := false
	for position, frame := range index.Frames {
		if position&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if frame.DurationPTS <= 0 || frame.PTS > math.MaxInt64-frame.DurationPTS {
			return fmt.Errorf("%w: frame %d interval", ErrIndexInvalid, position)
		}
		if position > 0 {
			previous := index.Frames[position-1]
			if frame.PTS <= previous.PTS || frame.PTS < previous.PTS+previous.DurationPTS {
				return fmt.Errorf("%w: frame %d is unordered or overlaps", ErrIndexInvalid, position)
			}
		}
		if frame.Keyframe {
			foundKeyframe = true
		} else if !foundKeyframe {
			return fmt.Errorf("%w: frame %d has no preceding keyframe", ErrIndexInvalid, position)
		}
	}
	return nil
}

func BindFrameIndex(ctx context.Context, manifest probe.ProbeManifest, index FrameIndex, maximumFrames int) error {
	if err := ValidateFrameIndex(ctx, index, maximumFrames); err != nil {
		return err
	}
	manifestFingerprint, err := probe.Fingerprint(manifest)
	if err != nil {
		return fmt.Errorf("%w: invalid probe manifest: %v", ErrIndexMismatch, err)
	}
	if index.ProbeFingerprint != manifestFingerprint || index.ResourceHash != manifest.Resource.ContentHash {
		return fmt.Errorf("%w: probe or resource fingerprint", ErrIndexMismatch)
	}
	var stream *probe.Stream
	for position := range manifest.Streams {
		candidate := &manifest.Streams[position]
		if candidate.Index == index.StreamIndex {
			stream = candidate
			break
		}
	}
	if stream == nil || stream.Kind != probe.StreamKindVideo || stream.Video == nil || stream.TimeBase != index.TimeBase {
		return fmt.Errorf("%w: stream identity or time base", ErrIndexMismatch)
	}
	if stream.DurationPTS == nil || *stream.DurationPTS < 0 || index.Frames[0].PTS != stream.StartPTS ||
		stream.StartPTS > math.MaxInt64-*stream.DurationPTS {
		return fmt.Errorf("%w: index does not start at the bounded stream interval", ErrIndexMismatch)
	}
	last := index.Frames[len(index.Frames)-1]
	if last.PTS+last.DurationPTS != stream.StartPTS+*stream.DurationPTS {
		return fmt.Errorf("%w: index does not close the bounded stream interval", ErrIndexMismatch)
	}
	if !stream.Video.Keyframes.Complete {
		return fmt.Errorf("%w: probe keyframe scan is incomplete", ErrIndexMismatch)
	}
	if stream.Video.Keyframes.Count <= 0 || stream.Video.Keyframes.Count > int64(len(index.Frames)) {
		return fmt.Errorf("%w: probe keyframe count exceeds frame index", ErrIndexMismatch)
	}
	keyframes := make([]int64, 0, int(stream.Video.Keyframes.Count))
	for position, frame := range index.Frames {
		if position&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if frame.Keyframe {
			keyframes = append(keyframes, frame.PTS)
		}
	}
	summary := keyframeSummary(index.StreamIndex, keyframes)
	if summary.Complete != stream.Video.Keyframes.Complete || summary.Count != stream.Video.Keyframes.Count ||
		!equalOptionalInt64(summary.FirstPTS, stream.Video.Keyframes.FirstPTS) ||
		!equalOptionalInt64(summary.LastPTS, stream.Video.Keyframes.LastPTS) || summary.IndexHash != stream.Video.Keyframes.IndexHash {
		return fmt.Errorf("%w: keyframe summary", ErrIndexMismatch)
	}
	return nil
}

func SelectFrame(index FrameIndex, sourceTime videoedit.RationalTime) (Selection, error) {
	if err := ValidateFrameIndex(context.Background(), index, len(index.Frames)); err != nil {
		return Selection{}, err
	}
	return selectFrameValidated(index, sourceTime, keyframeOrdinals(index))
}

func selectFrameValidated(index FrameIndex, sourceTime videoedit.RationalTime, keyframes []int) (Selection, error) {
	if err := sourceTime.Validate(); err != nil {
		return Selection{}, fmt.Errorf("%w: source time: %v", ErrInvalid, err)
	}
	left, right := 0, len(index.Frames)
	for left < right {
		middle := left + (right-left)/2
		frame := index.Frames[middle]
		if compareTimeToPTS(sourceTime, frame.PTS+frame.DurationPTS, index.TimeBase) >= 0 {
			left = middle + 1
		} else {
			right = middle
		}
	}
	if left >= len(index.Frames) {
		return Selection{}, ErrFrameUnavailable
	}
	frame := index.Frames[left]
	if compareTimeToPTS(sourceTime, frame.PTS, index.TimeBase) < 0 {
		return Selection{}, ErrFrameUnavailable
	}
	keyframePosition := sort.Search(len(keyframes), func(position int) bool { return keyframes[position] > left }) - 1
	if keyframePosition < 0 {
		return Selection{}, fmt.Errorf("%w: no preceding keyframe", ErrIndexInvalid)
	}
	seek := keyframes[keyframePosition]
	return Selection{FrameOrdinal: left, Frame: frame, SeekPTS: index.Frames[seek].PTS}, nil
}

func keyframeOrdinals(index FrameIndex) []int {
	result := make([]int, 0)
	for position, frame := range index.Frames {
		if frame.Keyframe {
			result = append(result, position)
		}
	}
	return result
}

func compareTimeToPTS(value videoedit.RationalTime, pts int64, timeBase videoedit.RationalTime) int {
	left := new(big.Int).Mul(big.NewInt(value.Value), big.NewInt(timeBase.Rate))
	right := new(big.Int).Mul(big.NewInt(pts), big.NewInt(timeBase.Value))
	right.Mul(right, big.NewInt(value.Rate))
	return left.Cmp(right)
}

func keyframeSummary(streamIndex int, values []int64) probe.KeyframeSummary {
	canonical, _ := videoedit.CanonicalJSON(struct {
		Profile     string  `json:"profile"`
		StreamIndex int     `json:"stream_index"`
		PTS         []int64 `json:"pts"`
	}{Profile: "cutly.keyframe-index.v1", StreamIndex: streamIndex, PTS: append([]int64(nil), values...)})
	digest := sha256.Sum256(canonical)
	result := probe.KeyframeSummary{Complete: true, Count: int64(len(values)), IndexHash: "sha256:" + hex.EncodeToString(digest[:])}
	if len(values) > 0 {
		first, last := values[0], values[len(values)-1]
		result.FirstPTS, result.LastPTS = &first, &last
	}
	return result
}

func cloneFrameIndex(value FrameIndex) FrameIndex {
	value.Frames = append([]FrameRecord(nil), value.Frames...)
	return value
}

func equalOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func normalizeMaximumFrames(value int) (int, error) {
	if value == 0 {
		value = DefaultMaxIndexedFrames
	}
	if value <= 0 || value > HardMaxIndexedFrames {
		return 0, fmt.Errorf("%w: max indexed frames %d", ErrResourceLimit, value)
	}
	return value, nil
}

func canonicalSHA256(value string) bool {
	if len(value) != 71 || !bytes.HasPrefix([]byte(value), []byte("sha256:")) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}
