package ffprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/probe"
)

const (
	// VideoScannerImplementation identifies the host-owned complete scanner.
	VideoScannerImplementation = "cutly.ffprobe.video-scan"
	VideoScannerProfileV1      = "cutly.ffprobe.video-scan.v1"
	// HardMaxVideoScanBytes bounds captured JSON independently of Probe options.
	HardMaxVideoScanBytes int64 = 64 << 20
)

var (
	ErrVideoScanUnsupported = errors.New("ffprobe: unsupported video scan semantics")
	ErrVideoScanLimit       = errors.New("ffprobe: video scan budget exceeded")
)

// VideoScanOptions bounds captured JSON and indexed frames. Zero selects 32 MiB
// and decode.DefaultMaxIndexedFrames. Frame admission precedes record allocation;
// byte admission precedes JSON decoding. These are not OS decoder-memory limits.
// Callers must supply a deadline and an OS boundary for untrusted codecs.
type VideoScanOptions struct {
	MaxOutputBytes int64 `json:"max_output_bytes"`
	MaxFrames      int   `json:"max_frames"`
}

// VideoScanResult owns its frame records. No result, including fingerprints, is
// returned on cancellation, panic, binding failure or private-descriptor cleanup
// failure. Index and semantics independently bind the exact supplied manifest.
type VideoScanResult struct {
	Index                     videodecode.FrameIndex
	IndexFingerprint          string
	Semantics                 videodecode.VideoSemanticsEvidence
	SemanticsFingerprint      string
	ScannerBuildFingerprint   string
	ScannerOptionsFingerprint string
}

// ScanVideo scans every decoded frame and all reported stream/frame side data of
// one global video stream, using a private content-verified inherited descriptor.
// It does not modify manifest or change Probe's legacy summaries/fingerprints.
// VFR and negative PTS are accepted only with explicit ordered non-overlapping
// exact intervals, matching start/end and keyframe summary. Missing duration is
// rejected (never inferred from average FPS, adjacent PTS, or container duration).
// V1 accepts explicit static SDR BT.709/sRGB-family YUV, progressive frames, and
// absent or exact pure quarter-turn display matrices. Unknown side data, HDR,
// flips, dynamic color/geometry and incomplete facts fail closed.
func (adapter *Adapter) ScanVideo(ctx context.Context, request Request, manifest probe.ProbeManifest, streamIndex int, options VideoScanOptions) (result VideoScanResult, returnErr error) {
	var input *materializedInput
	defer finishVideoScan(&result, &returnErr, &input)
	if adapter == nil || adapter.executable == "" {
		return result, fmt.Errorf("%w: uninitialized adapter", ErrExecutable)
	}
	if ctx == nil {
		return result, fmt.Errorf("%w: nil context", ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	options, err := normalizeVideoScanOptions(options)
	if err != nil {
		return result, err
	}
	// Freeze all nested caller values before any blocking work.
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return result, fmt.Errorf("%w: manifest: %v", ErrInput, err)
	}
	manifest, _, err = probe.Decode(manifestBytes)
	if err != nil {
		return result, err
	}
	if _, err := probe.Fingerprint(manifest); err != nil {
		return result, err
	}
	stream, err := scanVideoStream(manifest, streamIndex)
	if err != nil {
		return result, err
	}
	if request.AssetID != manifest.Resource.AssetID || manifest.Probe.Implementation != Implementation ||
		manifest.Probe.BuildFingerprint != adapter.buildFingerprint || manifest.Probe.OptionsFingerprint != adapter.optionsFingerprint ||
		(request.ExpectedContentHash != "" && request.ExpectedContentHash != manifest.Resource.ContentHash) {
		return result, fmt.Errorf("%w: scan request/probe identity mismatch", ErrInput)
	}
	verifyExecutable := func() error {
		current, _, err := hashRegularFile(ctx, adapter.executable)
		if err != nil {
			return errors.Join(fmt.Errorf("%w: runtime revalidation", ErrExecutable), err)
		}
		if current != adapter.binaryFingerprint {
			return fmt.Errorf("%w: executable changed", ErrExecutable)
		}
		return nil
	}
	if err := verifyExecutable(); err != nil {
		return result, err
	}
	input, err = materializeLocalInput(ctx, request.Path, manifest.Resource.ContentHash)
	if err != nil {
		return result, err
	}
	if input.sizeBytes != manifest.Resource.SizeBytes {
		return result, fmt.Errorf("%w: resource size", ErrInput)
	}
	// A value copy changes only this command's capture budget, not adapter identity.
	runner := *adapter
	runner.options.MaxOutputBytes = options.MaxOutputBytes
	arguments := func(o Options, path string) []string { return videoScanArguments(o, path, streamIndex) }
	if err := verifyExecutable(); err != nil {
		return result, err
	}
	raw, err := runner.runProbeCommandChecked(ctx, input, arguments, true)
	if err != nil {
		return result, err
	}
	if err := verifyExecutable(); err != nil {
		return result, err
	}
	frames, observations, err := parseVideoScan(ctx, raw, stream, options)
	if err != nil {
		return result, err
	}
	hash, size, err := input.verify(ctx)
	if err != nil {
		return result, err
	}
	if hash != manifest.Resource.ContentHash || size != manifest.Resource.SizeBytes {
		return result, ErrContentChanged
	}
	result.Index, result.IndexFingerprint, err = videodecode.NewFrameIndex(ctx, manifest, streamIndex, frames, options.MaxFrames)
	if err != nil {
		return result, err
	}
	// NewFrameIndex also calls BindFrameIndex, proving interval and keyframe closure.
	result.ScannerBuildFingerprint, err = fingerprintJSON(struct {
		Profile    string `json:"profile"`
		ProbeBuild string `json:"probe_build"`
	}{VideoScannerProfileV1, adapter.buildFingerprint})
	if err != nil {
		return result, err
	}
	result.ScannerOptionsFingerprint, err = fingerprintJSON(struct {
		Profile      string           `json:"profile"`
		ProbeOptions string           `json:"probe_options"`
		Options      VideoScanOptions `json:"options"`
		Arguments    []string         `json:"arguments"`
	}{VideoScannerProfileV1, adapter.optionsFingerprint, options, arguments(runner.options, "<private-content-addressed-input>")})
	if err != nil {
		return result, err
	}
	result.Semantics, result.SemanticsFingerprint, err = videodecode.NewVideoSemanticsEvidence(ctx, manifest, streamIndex,
		VideoScannerImplementation, result.ScannerBuildFingerprint, result.ScannerOptionsFingerprint, observations)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

// This deferred boundary owns both panic conversion and descriptor cleanup; a
// cleanup error suppresses every already-computed output just like a scan error.
func finishVideoScan(result *VideoScanResult, returnErr *error, input **materializedInput) {
	if recovered := recover(); recovered != nil {
		*returnErr = errors.Join(*returnErr, fmt.Errorf("%w: scanner panic: %v", ErrParse, recovered))
	}
	if err := (*input).Close(); err != nil {
		*returnErr = errors.Join(*returnErr, fmt.Errorf("%w: %v", ErrCleanup, err))
	}
	if *returnErr != nil {
		*result = VideoScanResult{}
	}
}

func normalizeVideoScanOptions(options VideoScanOptions) (VideoScanOptions, error) {
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = defaultMaxOutputBytes
	}
	if options.MaxFrames == 0 {
		options.MaxFrames = videodecode.DefaultMaxIndexedFrames
	}
	if options.MaxOutputBytes < 1024 || options.MaxOutputBytes > HardMaxVideoScanBytes || options.MaxFrames < 1 || options.MaxFrames > videodecode.HardMaxIndexedFrames {
		return VideoScanOptions{}, fmt.Errorf("%w: invalid byte/frame limits", ErrVideoScanLimit)
	}
	return options, nil
}

func videoScanArguments(options Options, path string, streamIndex int) []string {
	return []string{"-v", "error", "-protocol_whitelist", "fd", "-fd", "3",
		"-probesize", strconv.FormatInt(options.ProbeSizeBytes, 10), "-analyzeduration", strconv.FormatInt(options.AnalyzeDurationMicros, 10),
		"-select_streams", strconv.Itoa(streamIndex), "-show_streams", "-show_frames", "-print_format", "json", path}
}

func scanVideoStream(manifest probe.ProbeManifest, index int) (probe.Stream, error) {
	for _, stream := range manifest.Streams {
		if stream.Index != index || stream.Kind != probe.StreamKindVideo || stream.Video == nil {
			continue
		}
		v := stream.Video
		pixels := map[string]bool{"yuv420p": true, "yuv422p": true, "yuv444p": true, "yuv420p10le": true, "yuv422p10le": true, "yuv444p10le": true}
		if v.HDR != nil || v.HasAlpha || !pixels[v.PixelFormat] || v.Color.Primaries != "bt709" || v.Color.Matrix != "bt709" ||
			(v.Color.Transfer != "bt709" && v.Color.Transfer != "iec61966-2-1") ||
			(v.Color.Range != probe.ColorRangeLimited && v.Color.Range != probe.ColorRangeFull) ||
			v.Color.ChromaLocation == "" || v.Color.ChromaLocation == "unspecified" || v.SampleAspectRatio == nil ||
			v.RotationDegrees%90 != 0 {
			return probe.Stream{}, fmt.Errorf("%w: explicit SDR color/pixels/aspect/quarter-turn required", ErrVideoScanUnsupported)
		}
		return stream, nil
	}
	return probe.Stream{}, fmt.Errorf("%w: video stream %d absent", ErrInput, index)
}

type scanFrame struct {
	MediaType      string                       `json:"media_type"`
	StreamIndex    *int                         `json:"stream_index"`
	PTS            json.RawMessage              `json:"pts"`
	BestEffort     json.RawMessage              `json:"best_effort_timestamp"`
	Duration       json.RawMessage              `json:"duration"`
	PacketDuration json.RawMessage              `json:"pkt_duration"`
	Keyframe       json.RawMessage              `json:"key_frame"`
	Interlaced     json.RawMessage              `json:"interlaced_frame"`
	TopFieldFirst  json.RawMessage              `json:"top_field_first"`
	Repeat         json.RawMessage              `json:"repeat_pict"`
	Width          int                          `json:"width"`
	Height         int                          `json:"height"`
	PixelFormat    string                       `json:"pix_fmt"`
	SampleAspect   string                       `json:"sample_aspect_ratio"`
	Primaries      string                       `json:"color_primaries"`
	Transfer       string                       `json:"color_transfer"`
	Matrix         string                       `json:"color_space"`
	Range          string                       `json:"color_range"`
	Chroma         string                       `json:"chroma_location"`
	SideData       []map[string]json.RawMessage `json:"side_data_list"`
	CropTop        int                          `json:"crop_top"`
	CropBottom     int                          `json:"crop_bottom"`
	CropLeft       int                          `json:"crop_left"`
	CropRight      int                          `json:"crop_right"`
	DecodeErrors   int                          `json:"decode_error_flags"`
}

// parseVideoScan streams array elements rather than decoding an unbounded frame
// slice. Each record is admitted before decoding/allocating it. The command's
// byte cap also bounds individual objects, strings, side data and ignored keys.
func parseVideoScan(ctx context.Context, raw []byte, stream probe.Stream, options VideoScanOptions) ([]videodecode.FrameRecord, videodecode.VideoSemanticsObservations, error) {
	fail := func(err error) ([]videodecode.FrameRecord, videodecode.VideoSemanticsObservations, error) {
		return nil, videodecode.VideoSemanticsObservations{}, err
	}
	if int64(len(raw)) > options.MaxOutputBytes {
		return fail(ErrVideoScanLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fail(fmt.Errorf("%w: scan root", ErrParse))
	}
	seen := map[string]bool{}
	frames := []videodecode.FrameRecord{}
	header := false
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || (key != "frames" && key != "streams") {
			return fail(fmt.Errorf("%w: scan root field %v", ErrParse, token))
		}
		seen[key] = true
		token, err = decoder.Token()
		if err != nil || token != json.Delim('[') {
			return fail(fmt.Errorf("%w: scan %s array", ErrParse, key))
		}
		count := 0
		for decoder.More() {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			if key == "frames" && len(frames) >= options.MaxFrames {
				return fail(ErrVideoScanLimit)
			}
			if key == "streams" && count >= 1 {
				return fail(fmt.Errorf("%w: extra stream", ErrParse))
			}
			var object json.RawMessage
			if err := decoder.Decode(&object); err != nil {
				return fail(fmt.Errorf("%w: scan object: %v", ErrParse, err))
			}
			if err := wirejson.Validate(object); err != nil {
				return fail(fmt.Errorf("%w: scan object: %v", ErrParse, err))
			}
			if key == "frames" {
				var frame scanFrame
				if err := json.Unmarshal(object, &frame); err != nil {
					return fail(fmt.Errorf("%w: frame: %v", ErrParse, err))
				}
				record, err := observeScanFrame(frame, stream)
				if err != nil {
					return fail(err)
				}
				// Explicit growth never allocates capacity beyond the admitted frame cap.
				if len(frames) == cap(frames) {
					capacity := cap(frames)*2 + 1
					if capacity > options.MaxFrames {
						capacity = options.MaxFrames
					}
					grown := make([]videodecode.FrameRecord, len(frames), capacity)
					copy(grown, frames)
					frames = grown
				}
				frames = append(frames, record)
			} else {
				if err := observeScanHeader(object, stream); err != nil {
					return fail(err)
				}
				header = true
			}
			count++
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
			return fail(fmt.Errorf("%w: scan array end", ErrParse))
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return fail(fmt.Errorf("%w: scan root end", ErrParse))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fail(fmt.Errorf("%w: trailing scan JSON", ErrParse))
	}
	if !header || !seen["frames"] || len(frames) == 0 {
		return fail(fmt.Errorf("%w: incomplete scan", ErrParse))
	}
	// Assertions are emitted only after all frame and header checks succeeded and
	// the entire JSON document closed. Index binding happens before publication.
	return frames, videodecode.VideoSemanticsObservations{ScanComplete: true, ProgressiveOnly: true, StaticColorOnly: true, RotationOnlyMatrix: true, RotationDegrees: stream.Video.RotationDegrees}, nil
}

func scanInteger(raw json.RawMessage, name string) (int64, error) {
	value, present, err := parseOptionalInt64(raw)
	if err != nil || !present {
		return 0, fmt.Errorf("%w: missing/invalid %s", ErrVideoScanUnsupported, name)
	}
	return *value, nil
}

func observeScanFrame(frame scanFrame, stream probe.Stream) (videodecode.FrameRecord, error) {
	fail := func(why string) (videodecode.FrameRecord, error) {
		return videodecode.FrameRecord{}, fmt.Errorf("%w: %s", ErrVideoScanUnsupported, why)
	}
	if frame.MediaType != "video" || frame.StreamIndex == nil || *frame.StreamIndex != stream.Index {
		return fail("frame stream identity")
	}
	if frame.CropTop != 0 || frame.CropBottom != 0 || frame.CropLeft != 0 || frame.CropRight != 0 || frame.DecodeErrors != 0 {
		return fail("cropped or damaged frame")
	}
	pts, err := scanInteger(frame.PTS, "pts")
	if err != nil {
		return videodecode.FrameRecord{}, err
	}
	if len(frame.BestEffort) != 0 {
		best, err := scanInteger(frame.BestEffort, "best_effort_timestamp")
		if err != nil || best != pts {
			return fail("ambiguous display timestamp")
		}
	}
	durationRaw := frame.Duration
	if len(durationRaw) == 0 {
		durationRaw = frame.PacketDuration
	}
	duration, err := scanInteger(durationRaw, "duration/pkt_duration")
	if err != nil || duration <= 0 {
		return fail("explicit positive duration required")
	}
	key, err := scanInteger(frame.Keyframe, "key_frame")
	if err != nil || (key != 0 && key != 1) {
		return fail("keyframe flag")
	}
	for name, raw := range map[string]json.RawMessage{"interlaced_frame": frame.Interlaced, "top_field_first": frame.TopFieldFirst, "repeat_pict": frame.Repeat} {
		value, err := scanInteger(raw, name)
		if err != nil || value != 0 {
			return fail("non-progressive or unknown " + name)
		}
	}
	v := stream.Video
	aspect, err := parseOptionalRatio(frame.SampleAspect)
	if err != nil || !reflect.DeepEqual(aspect, v.SampleAspectRatio) || frame.Width != v.Width || frame.Height != v.Height || frame.PixelFormat != v.PixelFormat ||
		frame.Primaries != v.Color.Primaries || frame.Transfer != v.Color.Transfer || frame.Matrix != v.Color.Matrix ||
		frame.Range == "" || mapColorRange(frame.Range) != v.Color.Range || frame.Chroma != v.Color.ChromaLocation {
		return fail("dynamic or unknown frame geometry/color")
	}
	if err := observeScanSideData(frame.SideData, v.RotationDegrees, false); err != nil {
		return videodecode.FrameRecord{}, err
	}
	return videodecode.FrameRecord{PTS: pts, DurationPTS: duration, Keyframe: key == 1}, nil
}

func observeScanHeader(object []byte, stream probe.Stream) error {
	var raw rawStream
	if err := json.Unmarshal(object, &raw); err != nil {
		return fmt.Errorf("%w: stream header: %v", ErrParse, err)
	}
	parsed, err := parseStream(raw)
	if err != nil {
		return fmt.Errorf("%w: stream header: %v", ErrParse, err)
	}
	expected := stream
	video := *stream.Video
	video.FrameRateMode = probe.FrameRateUnknown
	video.Keyframes = probe.KeyframeSummary{Complete: false, Count: 0}
	expected.Video = &video
	if !reflect.DeepEqual(parsed, expected) {
		return fmt.Errorf("%w: scanned stream header differs from probe", ErrInput)
	}
	var fields struct {
		FieldOrder string `json:"field_order"`
	}
	if err := json.Unmarshal(object, &fields); err != nil {
		return err
	}
	if fields.FieldOrder != "progressive" && fields.FieldOrder != "unknown" && fields.FieldOrder != "" {
		return fmt.Errorf("%w: interlaced header", ErrVideoScanUnsupported)
	}
	if tag := strings.TrimSpace(raw.Tags["rotate"]); tag != "" {
		// A scalar tag alone cannot rule out reflection/shear; require matrix proof.
		matrix := false
		for _, side := range raw.SideData {
			if kind := rawString(side["side_data_type"]); kind == "Display Matrix" || kind == "3x3 displaymatrix" {
				matrix = true
			}
		}
		if !matrix {
			return fmt.Errorf("%w: unproven rotation tag", ErrVideoScanUnsupported)
		}
	}
	return observeScanSideData(raw.SideData, stream.Video.RotationDegrees, true)
}

func observeScanSideData(sides []map[string]json.RawMessage, rotation int, header bool) error {
	matrices := 0
	for _, side := range sides {
		kind := rawString(side["side_data_type"])
		switch kind {
		case "H.26[45] User Data Unregistered SEI message":
			// FFprobe's opaque user-data marker (e.g. encoder build text) is not a
			// color/display assertion. No other unrecognized side data is accepted.
		case "Display Matrix", "3x3 displaymatrix":
			matrices++
			if matrices > 1 {
				return fmt.Errorf("%w: duplicate display matrices", ErrVideoScanUnsupported)
			}
			if err := observeDisplayMatrix(side, rotation); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown/HDR/dynamic side data %q", ErrVideoScanUnsupported, kind)
		}
	}
	if header && rotation != 0 && matrices != 1 {
		return fmt.Errorf("%w: missing display matrix", ErrVideoScanUnsupported)
	}
	return nil
}

func observeDisplayMatrix(side map[string]json.RawMessage, rotation int) error {
	fail := func() error {
		return fmt.Errorf("%w: display matrix is not an exact pure quarter-turn", ErrVideoScanUnsupported)
	}
	reported, err := scanInteger(side["rotation"], "matrix rotation")
	if err != nil || reported != int64(rotation) {
		return fail()
	}
	lines := strings.Split(strings.TrimSpace(rawString(side["displaymatrix"])), "\n")
	if len(lines) != 3 {
		return fail()
	}
	var matrix [9]int64
	for row, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != fmt.Sprintf("%08d:", row) {
			return fail()
		}
		for col := 0; col < 3; col++ {
			n, err := strconv.ParseInt(fields[col+1], 10, 64)
			if err != nil {
				return fail()
			}
			matrix[row*3+col] = n
		}
	}
	expected := [9]int64{65536, 0, 0, 0, 65536, 0, 0, 0, 1073741824}
	switch rotation {
	case 0:
	case 90:
		expected = [9]int64{0, -65536, 0, 65536, 0, 0, 0, 0, 1073741824}
	case -90:
		expected = [9]int64{0, 65536, 0, -65536, 0, 0, 0, 0, 1073741824}
	case 180, -180:
		expected = [9]int64{-65536, 0, 0, 0, -65536, 0, 0, 0, 1073741824}
	default:
		return fail()
	}
	if matrix != expected {
		return fail()
	}
	return nil
}
