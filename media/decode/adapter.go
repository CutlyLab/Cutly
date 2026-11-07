package videodecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// OpenRequest binds a path to already-frozen probe, complete display-frame
// index and complete semantic-scan artifacts. Both fingerprints are mandatory
// so artifact transport cannot silently substitute another valid artifact.
type OpenRequest struct {
	Path                 string
	Manifest             probe.ProbeManifest
	Index                FrameIndex
	IndexFingerprint     string
	Semantics            VideoSemanticsEvidence
	SemanticsFingerprint string
}

type FrameReceipt struct {
	AdapterFingerprint   string   `json:"adapter_fingerprint"`
	ProbeFingerprint     string   `json:"probe_fingerprint"`
	IndexFingerprint     string   `json:"index_fingerprint"`
	SemanticsFingerprint string   `json:"semantics_fingerprint"`
	ResourceHash         string   `json:"resource_hash"`
	StreamIndex          int      `json:"stream_index"`
	FrameOrdinal         int      `json:"frame_ordinal"`
	DisplayPTS           int64    `json:"display_pts"`
	SeekPTS              int64    `json:"seek_pts"`
	Width                int      `json:"width"`
	Height               int      `json:"height"`
	SurfaceHash          string   `json:"surface_hash"`
	Profiles             Profiles `json:"profiles"`
}

type Profiles struct {
	Decode        string `json:"decode"`
	Selection     string `json:"selection"`
	Color         string `json:"color"`
	Normalization string `json:"normalization"`
}

type SourceIdentity struct {
	AdapterFingerprint   string   `json:"adapter_fingerprint"`
	ProbeFingerprint     string   `json:"probe_fingerprint"`
	IndexFingerprint     string   `json:"index_fingerprint"`
	SemanticsFingerprint string   `json:"semantics_fingerprint"`
	ResourceHash         string   `json:"resource_hash"`
	StreamIndex          int      `json:"stream_index"`
	Profiles             Profiles `json:"profiles"`
}

// Adapter is immutable after construction and safe to share. FFmpeg runtime
// output is only reproducible within its pinned build/dependency/profile
// identity; no cross-build pixel equivalence is claimed.
type Adapter struct {
	executable        string
	executableInfo    os.FileInfo
	binaryFingerprint string
	fingerprint       string
	identity          BuildIdentity
	options           Options
}

// Source owns one immutable, unlinked, descriptor-bound input snapshot.
// Frame calls are serialized because the inherited descriptor has a shared
// seek offset; returned compositor surfaces remain immutable and concurrent-
// read safe.
type Source struct {
	mu                   sync.Mutex
	frameGate            chan struct{}
	adapter              *Adapter
	snapshot             *sourceSnapshot
	manifest             probe.ProbeManifest
	index                FrameIndex
	keyframes            []int
	indexFingerprint     string
	semanticsFingerprint string
	stream               normalizedVideo
	closed               bool
}

type normalizedVideo struct {
	index         int
	timeBase      videoedit.RationalTime
	startPTS      int64
	inputWidth    int
	inputHeight   int
	outputWidth   int
	outputHeight  int
	squareWidth   int
	rotation      int
	matrix        string
	transfer      string
	primaries     string
	rangeName     string
	chroma        string
	maxAllocation int64
	maxPixels     int64
}

func New(ctx context.Context, executable string, identity BuildIdentity, options Options) (*Adapter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrExecutable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProcessPlatform(); err != nil {
		return nil, err
	}
	if err := validateBuildIdentity(identity); err != nil {
		return nil, err
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveExecutable(executable)
	if err != nil {
		return nil, err
	}
	setupCtx, cancelSetup := context.WithTimeout(ctx, time.Duration(options.MaxDecodeMillis)*time.Millisecond)
	defer cancelSetup()
	binaryFingerprint, _, executableInfo, err := hashRegularFile(setupCtx, resolved, maxExecutableBytes)
	if err != nil {
		if setupErr := setupCtx.Err(); setupErr != nil {
			return nil, setupErr
		}
		return nil, fmt.Errorf("%w: hash executable: %v", ErrExecutable, err)
	}
	versionReport, err := runIdentityCommand(setupCtx, resolved, 64<<10, "-cpuflags", "0", "-version")
	if err != nil || !bytes.Contains([]byte(versionReport), []byte("ffmpeg version")) {
		if setupErr := setupCtx.Err(); setupErr != nil {
			return nil, setupErr
		}
		return nil, fmt.Errorf("%w: bounded FFmpeg version report required", ErrExecutable)
	}
	filterReport, err := runIdentityCommand(setupCtx, resolved, 4<<20, "-hide_banner", "-filters")
	if err != nil {
		if setupErr := setupCtx.Err(); setupErr != nil {
			return nil, setupErr
		}
		return nil, fmt.Errorf("%w: filter inventory: %v", ErrExecutable, err)
	}
	for _, required := range []string{"select", "zscale", "format", "transpose", "hflip", "vflip", "setsar"} {
		if !reportHasToken(filterReport, required) {
			return nil, fmt.Errorf("%w: required filter %q is absent", ErrExecutable, required)
		}
	}
	pixelReport, err := runIdentityCommand(setupCtx, resolved, 4<<20, "-hide_banner", "-pix_fmts")
	if err != nil || !reportHasToken(pixelReport, "gbrp16le") || !reportHasToken(pixelReport, "rgba64le") {
		if setupErr := setupCtx.Err(); setupErr != nil {
			return nil, setupErr
		}
		return nil, fmt.Errorf("%w: gbrp16le/rgba64le pixel formats are absent", ErrExecutable)
	}
	muxerReport, err := runIdentityCommand(setupCtx, resolved, 4<<20, "-hide_banner", "-muxers")
	if err != nil || !reportHasToken(muxerReport, "rawvideo") {
		if setupErr := setupCtx.Err(); setupErr != nil {
			return nil, setupErr
		}
		return nil, fmt.Errorf("%w: rawvideo muxer is absent", ErrExecutable)
	}
	filterFingerprint, _ := fingerprintJSON(filterReport)
	pixelFingerprint, _ := fingerprintJSON(pixelReport)
	muxerFingerprint, _ := fingerprintJSON(muxerReport)
	fingerprint, err := fingerprintJSON(struct {
		Implementation       string         `json:"implementation"`
		Binary               string         `json:"binary"`
		Version              string         `json:"version"`
		FilterInventory      string         `json:"filter_inventory"`
		PixelFormatInventory string         `json:"pixel_format_inventory"`
		MuxerInventory       string         `json:"muxer_inventory"`
		Identity             BuildIdentity  `json:"identity"`
		Options              Options        `json:"options"`
		Environment          []string       `json:"environment"`
		EnvProfile           string         `json:"environment_profile"`
		ArgumentProfile      []string       `json:"argument_profile"`
		Runtime              RuntimeProfile `json:"runtime"`
	}{
		Implementation: Implementation, Binary: binaryFingerprint,
		Version: versionReport, FilterInventory: filterFingerprint, PixelFormatInventory: pixelFingerprint, MuxerInventory: muxerFingerprint,
		Identity: identity, Options: options,
		Environment: controlledEnvironment(), EnvProfile: EnvironmentProfileV1,
		ArgumentProfile: decodeArguments(normalizedVideo{index: 7, timeBase: videoedit.RationalTime{Value: 1, Rate: 1000}, startPTS: 0, squareWidth: 16, inputHeight: 9, rotation: 0, matrix: "bt709", transfer: "bt709", primaries: "bt709", rangeName: "limited", chroma: "left", maxAllocation: options.MaxFrameBytes, maxPixels: 144}, Selection{Frame: FrameRecord{PTS: 42}, SeekPTS: 0}, "<descriptor-3>"),
		Runtime:         currentRuntimeProfile(identity.RuntimeCPUFingerprint),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: fingerprint: %v", ErrExecutable, err)
	}
	if err := verifyExecutableIdentity(setupCtx, resolved, executableInfo, binaryFingerprint); err != nil {
		return nil, err
	}
	return &Adapter{executable: resolved, executableInfo: executableInfo, binaryFingerprint: binaryFingerprint, fingerprint: fingerprint, identity: identity, options: options}, nil
}

func runIdentityCommand(ctx context.Context, executable string, limit int64, arguments ...string) (string, error) {
	stdout, stderr := newBoundedBuffer(limit), newBoundedBuffer(limit)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = controlledEnvironment()
	command.Stdout, command.Stderr = stdout, stderr
	hardenCommand(command)
	err := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil || stdout.exceeded || stderr.exceeded {
		return "", fmt.Errorf("command failed or exceeded output limit: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	// Inventory commands have historically used stdout, while some builds emit
	// headings on stderr. Both streams are identity-bound and searched.
	return strings.TrimSpace(stdout.String() + "\n" + stderr.String()), nil
}

func reportHasToken(report, token string) bool {
	for _, field := range strings.Fields(report) {
		if field == token {
			return true
		}
	}
	return false
}

func (adapter *Adapter) Fingerprint() string {
	if adapter == nil {
		return ""
	}
	return adapter.fingerprint
}

func (adapter *Adapter) Open(ctx context.Context, request OpenRequest) (_ *Source, returnErr error) {
	if adapter == nil || adapter.fingerprint == "" {
		return nil, fmt.Errorf("%w: uninitialized adapter", ErrExecutable)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := adapter.verifyExecutable(ctx); err != nil {
		return nil, err
	}
	if !fingerprintPattern.MatchString(request.IndexFingerprint) {
		return nil, fmt.Errorf("%w: canonical index fingerprint is required", ErrIndexInvalid)
	}
	if err := BindFrameIndex(ctx, request.Manifest, request.Index, adapter.options.MaxIndexedFrames); err != nil {
		return nil, err
	}
	actualIndexFingerprint, err := FrameIndexFingerprint(request.Index, adapter.options.MaxIndexedFrames)
	if err != nil {
		return nil, err
	}
	if actualIndexFingerprint != request.IndexFingerprint {
		return nil, fmt.Errorf("%w: index fingerprint", ErrIndexMismatch)
	}
	if !fingerprintPattern.MatchString(request.SemanticsFingerprint) {
		return nil, fmt.Errorf("%w: canonical semantics fingerprint is required", ErrInvalid)
	}
	actualSemanticsFingerprint, err := VideoSemanticsEvidenceFingerprint(ctx, request.Manifest, request.Semantics)
	if err != nil {
		return nil, err
	}
	if actualSemanticsFingerprint != request.SemanticsFingerprint || request.Semantics.StreamIndex != request.Index.StreamIndex {
		return nil, fmt.Errorf("%w: video semantic evidence", ErrIndexMismatch)
	}
	manifest, err := cloneManifest(request.Manifest)
	if err != nil {
		return nil, err
	}
	stream, err := normalizeVideoStream(manifest, request.Index.StreamIndex, adapter.options)
	if err != nil {
		return nil, err
	}
	snapshot, err := materializeLocalInput(ctx, request.Path, manifest.Resource.ContentHash, manifest.Resource.SizeBytes, adapter.options.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, snapshot.close())
		}
	}()
	return &Source{
		adapter: adapter, snapshot: snapshot, manifest: manifest,
		index: cloneFrameIndex(request.Index), keyframes: keyframeOrdinals(request.Index),
		indexFingerprint: actualIndexFingerprint, semanticsFingerprint: actualSemanticsFingerprint, stream: stream,
		frameGate: make(chan struct{}, 1),
	}, nil
}

func (source *Source) Frame(ctx context.Context, sourceTime videoedit.RationalTime) (*cpucompositor.Surface, FrameReceipt, error) {
	if source == nil {
		return nil, FrameReceipt{}, ErrClosed
	}
	if ctx == nil {
		return nil, FrameReceipt{}, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if source.frameGate == nil {
		return nil, FrameReceipt{}, ErrClosed
	}
	select {
	case source.frameGate <- struct{}{}:
		defer func() { <-source.frameGate }()
	case <-ctx.Done():
		return nil, FrameReceipt{}, ctx.Err()
	}
	source.mu.Lock()
	if source.closed || source.snapshot == nil {
		source.mu.Unlock()
		return nil, FrameReceipt{}, ErrClosed
	}
	source.mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(source.adapter.options.MaxDecodeMillis)*time.Millisecond)
	defer cancel()
	if err := source.adapter.verifyExecutable(runCtx); err != nil {
		return nil, FrameReceipt{}, err
	}
	selection, err := selectFrameValidated(source.index, sourceTime, source.keyframes)
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	input, extraFiles, err := source.snapshot.inheritedBinding()
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	stdout, err := newRGBA64PixelWriter(source.stream.outputWidth, source.stream.outputHeight, source.adapter.options)
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	stderr := newBoundedBuffer(source.adapter.options.MaxStderrBytes)
	arguments := decodeArguments(source.stream, selection, input)
	command := exec.CommandContext(runCtx, source.adapter.executable, arguments...)
	command.Env = controlledEnvironment()
	command.ExtraFiles = extraFiles
	command.Stdout, command.Stderr = stdout, stderr
	hardenCommand(command)
	runErr := command.Run()
	if ctxErr := runCtx.Err(); ctxErr != nil {
		return nil, FrameReceipt{}, ctxErr
	}
	if err := source.adapter.verifyExecutable(runCtx); err != nil {
		return nil, FrameReceipt{}, err
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, FrameReceipt{}, fmt.Errorf("%w: stdout=%t stderr=%t", ErrResourceLimit, stdout.exceeded, stderr.exceeded)
	}
	if runErr != nil {
		return nil, FrameReceipt{}, fmt.Errorf("%w: %v: %s", ErrCommand, runErr, strings.TrimSpace(stderr.String()))
	}
	surface, err := stdout.Surface(source.stream.outputWidth, source.stream.outputHeight, source.adapter.options)
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	surfaceHash, err := surfaceHashWithContext(runCtx, surface)
	if err != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, FrameReceipt{}, ctxErr
		}
		return nil, FrameReceipt{}, fmt.Errorf("%w: surface hash: %v", ErrOutput, err)
	}
	receipt := FrameReceipt{
		AdapterFingerprint:   source.adapter.fingerprint,
		ProbeFingerprint:     source.index.ProbeFingerprint,
		IndexFingerprint:     source.indexFingerprint,
		SemanticsFingerprint: source.semanticsFingerprint,
		ResourceHash:         source.index.ResourceHash, StreamIndex: source.index.StreamIndex,
		FrameOrdinal: selection.FrameOrdinal, DisplayPTS: selection.Frame.PTS, SeekPTS: selection.SeekPTS,
		Width: source.stream.outputWidth, Height: source.stream.outputHeight, SurfaceHash: surfaceHash,
		Profiles: Profiles{Decode: AdapterSemanticProfileV1, Selection: SelectionSemanticProfileV1, Color: ColorSemanticProfileV1, Normalization: NormalizationSemanticProfileV1},
	}
	return surface, receipt, nil
}

func (source *Source) Close() error {
	if source == nil {
		return nil
	}
	if source.frameGate == nil {
		source.mu.Lock()
		source.closed = true
		source.mu.Unlock()
		return nil
	}
	source.frameGate <- struct{}{}
	defer func() { <-source.frameGate }()
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil
	}
	source.closed = true
	err := source.snapshot.close()
	source.snapshot = nil
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCleanup, err)
	}
	return nil
}

func (source *Source) Identity() (SourceIdentity, error) {
	if source == nil {
		return SourceIdentity{}, ErrClosed
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed || source.adapter == nil {
		return SourceIdentity{}, ErrClosed
	}
	return SourceIdentity{
		AdapterFingerprint:   source.adapter.fingerprint,
		ProbeFingerprint:     source.index.ProbeFingerprint,
		IndexFingerprint:     source.indexFingerprint,
		SemanticsFingerprint: source.semanticsFingerprint,
		ResourceHash:         source.index.ResourceHash,
		StreamIndex:          source.index.StreamIndex,
		Profiles:             Profiles{Decode: AdapterSemanticProfileV1, Selection: SelectionSemanticProfileV1, Color: ColorSemanticProfileV1, Normalization: NormalizationSemanticProfileV1},
	}, nil
}

func decodeArguments(stream normalizedVideo, selection Selection, input string) []string {
	arguments := []string{
		"-v", "error", "-nostdin", "-hide_banner",
		"-cpuflags", "0",
		"-max_alloc", strconv.FormatInt(stream.maxAllocation, 10), "-cpucount", "1", "-filter_threads", "1",
		"-threads", "1", "-max_pixels", strconv.FormatInt(stream.maxPixels, 10), "-copyts", "-noautorotate",
	}
	if seek := conservativeSeek(stream.timeBase, selection.SeekPTS, stream.startPTS); seek != "" {
		arguments = append(arguments, "-ss", seek)
	}
	arguments = append(arguments,
		"-protocol_whitelist", "fd", "-fd", "3", "-i", input,
		"-map", "0:"+strconv.Itoa(stream.index), "-an", "-sn", "-dn",
		"-vf", filterGraph(stream, selection.Frame.PTS),
		"-frames:v", "1", "-fps_mode", "passthrough", "-pix_fmt", "rgba64le", "-f", "rawvideo", "pipe:1",
	)
	return arguments
}

func filterGraph(stream normalizedVideo, pts int64) string {
	filters := []string{
		"select=eq(pts\\," + strconv.FormatInt(pts, 10) + ")",
		fmt.Sprintf("zscale=matrixin=%s:transferin=%s:primariesin=%s:rangein=%s:chromalin=%s:matrix=gbr:transfer=linear:primaries=bt709:range=full:w=%d:h=%d:filter=bilinear:dither=none:agamma=0",
			stream.matrix, stream.transfer, stream.primaries, stream.rangeName, stream.chroma, stream.squareWidth, stream.inputHeight),
		"format=gbrp16le",
		"format=rgba64le",
	}
	switch stream.rotation {
	case 90:
		filters = append(filters, "transpose=cclock")
	case -90:
		filters = append(filters, "transpose=clock")
	case 180, -180:
		filters = append(filters, "hflip", "vflip")
	}
	filters = append(filters, "setsar=1")
	return strings.Join(filters, ",")
}

// conservativeSeek floors positive keyframe time to microseconds, so the
// demuxer hint can never begin after the indexed keyframe. Exact selection is
// still enforced by the display-PTS filter after decode-forward.
func conservativeSeek(timeBase videoedit.RationalTime, pts, startPTS int64) string {
	if pts <= startPTS || timeBase.Value <= 0 || timeBase.Rate <= 0 {
		return ""
	}
	delta := new(big.Int).Sub(big.NewInt(pts), big.NewInt(startPTS))
	numerator := new(big.Int).Mul(delta, big.NewInt(timeBase.Value))
	numerator.Mul(numerator, big.NewInt(1_000_000))
	micros := new(big.Int).Quo(numerator, big.NewInt(timeBase.Rate))
	if !micros.IsInt64() || micros.Sign() <= 0 {
		return ""
	}
	value := micros.Int64()
	return fmt.Sprintf("%d.%06d", value/1_000_000, value%1_000_000)
}

func normalizeVideoStream(manifest probe.ProbeManifest, streamIndex int, options Options) (normalizedVideo, error) {
	var stream *probe.Stream
	for position := range manifest.Streams {
		if manifest.Streams[position].Index == streamIndex {
			stream = &manifest.Streams[position]
			break
		}
	}
	if stream == nil || stream.Kind != probe.StreamKindVideo || stream.Video == nil {
		return normalizedVideo{}, fmt.Errorf("%w: video stream %d", ErrUnsupported, streamIndex)
	}
	video := stream.Video
	if video.HDR != nil || video.HasAlpha {
		return normalizedVideo{}, fmt.Errorf("%w: HDR and alpha video are outside profile v1", ErrUnsupported)
	}
	allowedPixels := map[string]bool{
		"yuv420p": true, "yuv422p": true, "yuv444p": true,
		"yuv420p10le": true, "yuv422p10le": true, "yuv444p10le": true,
	}
	if !allowedPixels[video.PixelFormat] || video.SampleAspectRatio == nil ||
		video.SampleAspectRatio.Numerator <= 0 || video.SampleAspectRatio.Denominator <= 0 {
		return normalizedVideo{}, fmt.Errorf("%w: pixel format or explicit sample aspect ratio", ErrUnsupported)
	}
	if video.Color.Primaries != "bt709" || video.Color.Matrix != "bt709" {
		return normalizedVideo{}, fmt.Errorf("%w: explicit BT.709 primaries/matrix required", ErrUnsupported)
	}
	transfer := map[string]string{"bt709": "bt709", "iec61966-2-1": "iec61966-2-1"}[video.Color.Transfer]
	rangeName := map[probe.ColorRange]string{probe.ColorRangeLimited: "limited", probe.ColorRangeFull: "full"}[video.Color.Range]
	chroma := map[string]string{"left": "left", "center": "center", "topleft": "topleft"}[video.Color.ChromaLocation]
	if transfer == "" || rangeName == "" || chroma == "" {
		return normalizedVideo{}, fmt.Errorf("%w: explicit transfer/range/chroma metadata required", ErrUnsupported)
	}
	if video.RotationDegrees != 0 && video.RotationDegrees != 90 && video.RotationDegrees != -90 &&
		video.RotationDegrees != 180 && video.RotationDegrees != -180 {
		return normalizedVideo{}, fmt.Errorf("%w: rotation must be a quarter turn", ErrUnsupported)
	}
	squareWidth, err := roundedDisplayWidth(video.Width, *video.SampleAspectRatio)
	if err != nil {
		return normalizedVideo{}, err
	}
	width, height := squareWidth, video.Height
	if video.RotationDegrees == 90 || video.RotationDegrees == -90 {
		width, height = height, width
	}
	if width > options.MaxWidth || height > options.MaxHeight || video.Width > options.MaxWidth || squareWidth > options.MaxWidth || video.Height > options.MaxHeight {
		return normalizedVideo{}, fmt.Errorf("%w: normalized dimensions %dx%d", ErrResourceLimit, width, height)
	}
	if _, err := checkedDecodedFrameBytes(width, height, options.MaxFrameBytes); err != nil {
		return normalizedVideo{}, err
	}
	maxPixels, err := checkedFrameBytes(video.Width, video.Height, 1, math.MaxInt64)
	if err != nil {
		return normalizedVideo{}, err
	}
	return normalizedVideo{
		index: stream.Index, timeBase: stream.TimeBase, startPTS: stream.StartPTS, inputWidth: video.Width, inputHeight: video.Height,
		squareWidth: squareWidth, outputWidth: width, outputHeight: height, rotation: video.RotationDegrees,
		matrix: "bt709", transfer: transfer, primaries: "bt709", rangeName: rangeName, chroma: chroma,
		maxAllocation: options.MaxFrameBytes, maxPixels: maxPixels,
	}, nil
}

func roundedDisplayWidth(width int, ratio probe.Ratio) (int, error) {
	if width <= 0 || ratio.Numerator <= 0 || ratio.Denominator <= 0 {
		return 0, fmt.Errorf("%w: invalid sample aspect ratio", ErrUnsupported)
	}
	numerator := new(big.Int).Mul(big.NewInt(int64(width)), big.NewInt(ratio.Numerator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, big.NewInt(ratio.Denominator), remainder)
	doubleRemainder := new(big.Int).Lsh(remainder, 1)
	denominator := big.NewInt(ratio.Denominator)
	if doubleRemainder.Cmp(denominator) > 0 || doubleRemainder.Cmp(denominator) == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 || quotient.Int64() > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: normalized width", ErrResourceLimit)
	}
	return int(quotient.Int64()), nil
}

func decodeOpaqueRGBA64LE(raw []byte, width, height int, options Options) (*cpucompositor.Surface, error) {
	writer, err := newRGBA64PixelWriter(width, height, options)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(raw); err != nil {
		return nil, err
	}
	return writer.Surface(width, height, options)
}

// checkedDecodedFrameBytes treats MaxFrameBytes as a live decoded-frame
// working-set budget. One scratch Pixel slice and one immutable Surface coexist
// during construction, so each may consume at most half of the budget.
func checkedDecodedFrameBytes(width, height int, maximumWorkingSet int64) (int64, error) {
	frameBytes, err := checkedFrameBytes(width, height, 8, math.MaxInt64)
	if err != nil {
		return 0, err
	}
	if maximumWorkingSet < 16 || frameBytes > maximumWorkingSet/2 {
		return 0, fmt.Errorf("%w: decoded frame working set", ErrResourceLimit)
	}
	return frameBytes, nil
}

func checkedFrameBytes(width, height int, bytesPerPixel, maximum int64) (int64, error) {
	if width <= 0 || height <= 0 || bytesPerPixel <= 0 || maximum <= 0 {
		return 0, fmt.Errorf("%w: invalid frame dimensions", ErrResourceLimit)
	}
	value := new(big.Int).Mul(big.NewInt(int64(width)), big.NewInt(int64(height)))
	value.Mul(value, big.NewInt(bytesPerPixel))
	if !value.IsInt64() || value.Sign() <= 0 || value.Int64() > maximum {
		return 0, fmt.Errorf("%w: decoded frame bytes", ErrResourceLimit)
	}
	return value.Int64(), nil
}

type contextCheckedWriter struct {
	ctx        context.Context
	writer     io.Writer
	sinceCheck int
}

func (writer *contextCheckedWriter) Write(data []byte) (int, error) {
	writer.sinceCheck += len(data)
	if writer.sinceCheck >= 32<<10 {
		if err := writer.ctx.Err(); err != nil {
			return 0, err
		}
		writer.sinceCheck = 0
	}
	return writer.writer.Write(data)
}

func surfaceHashWithContext(ctx context.Context, surface *cpucompositor.Surface) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hasher := sha256.New()
	writer := &contextCheckedWriter{ctx: ctx, writer: hasher}
	if err := surface.WriteCanonical(writer); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func (adapter *Adapter) verifyExecutable(ctx context.Context) error {
	if adapter == nil || adapter.executableInfo == nil {
		return fmt.Errorf("%w: missing frozen executable identity", ErrExecutable)
	}
	return verifyExecutableIdentity(ctx, adapter.executable, adapter.executableInfo, adapter.binaryFingerprint)
}

func verifyExecutableIdentity(ctx context.Context, path string, frozen os.FileInfo, expectedFingerprint string) error {
	if err := validateTrustedExecutablePath(path); err != nil {
		return err
	}
	current, _, currentInfo, err := hashRegularFile(ctx, path, maxExecutableBytes)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: verify executable: %v", ErrExecutable, err)
	}
	if frozen == nil || !os.SameFile(frozen, currentInfo) || current != expectedFingerprint {
		return fmt.Errorf("%w: executable changed after initialization", ErrExecutable)
	}
	return nil
}

func resolveExecutable(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "ffmpeg"
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExecutable, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path: %v", ErrExecutable, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve executable: %v", ErrExecutable, err)
	}
	path = resolved
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%w: executable regular file required", ErrExecutable)
	}
	if err := validateTrustedExecutablePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func validateTrustedExecutablePath(path string) error {
	for current, leaf := filepath.Clean(path), true; ; current, leaf = filepath.Dir(current), false {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: executable path must be symlink-free", ErrExecutable)
		}
		if !trustedPathOwner(info) {
			return fmt.Errorf("%w: executable path owner is outside the trusted root/current-user boundary", ErrExecutable)
		}
		if leaf {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o222 != 0 {
				return fmt.Errorf("%w: executable must be regular, executable, and immutable by mode", ErrExecutable)
			}
		} else {
			if !info.IsDir() {
				return fmt.Errorf("%w: executable ancestor is not a directory", ErrExecutable)
			}
			if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("%w: executable ancestor is group/world writable", ErrExecutable)
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func hashRegularFile(ctx context.Context, path string, maximum int64) (string, int64, os.FileInfo, error) {
	file, info, err := openRegularNoFollow(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return "", 0, nil, fmt.Errorf("regular file exceeds budget")
	}
	hasher, buffer := sha256.New(), make([]byte, 128<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, nil, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			size += int64(read)
			if size > maximum {
				return "", 0, nil, ErrResourceLimit
			}
			_, _ = hasher.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, nil, readErr
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", 0, nil, fmt.Errorf("regular file changed while hashing")
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), size, info, nil
}

func cloneManifest(value probe.ProbeManifest) (probe.ProbeManifest, error) {
	canonical, err := canonicaljson.Marshal(value)
	if err != nil {
		return probe.ProbeManifest{}, fmt.Errorf("%w: canonical manifest: %v", ErrInvalid, err)
	}
	copyValue, diagnostics, err := probe.Decode(canonical)
	if err != nil || probe.HasErrorDiagnostics(diagnostics) {
		return probe.ProbeManifest{}, fmt.Errorf("%w: invalid probe manifest", ErrInvalid)
	}
	return copyValue, nil
}

func fingerprintJSON(value any) (string, error) {
	canonical, err := canonicaljson.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func controlledEnvironment() []string { return []string{"LANG=C", "LC_ALL=C", "TZ=UTC"} }
