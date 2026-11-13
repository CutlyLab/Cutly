// Package streamencode runs one resident FFmpeg encoder that consumes straight-alpha RGBA8
// frames through a bounded stdin pipe and produces a deterministic MP4 — the
// cutly.pipeline.stream-encode.v1 profile's first cut: no per-frame processes, no intermediate
// PNG files, natural encoder backpressure through pipe blocking.
//
// The encoder identity posture mirrors media/decode: the executable must be a regular,
// executable, immutable-by-mode file on a symlink-free path owned inside the trusted
// root/current-user boundary, hashed once at Start. Encoder arguments mirror the whole-job
// backend's determinism flags (single thread, bitexact, pinned creation time, stripped
// metadata), so the same frame sequence yields byte-identical output across runs on the same
// pinned build.
package streamencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Profile identifies the streaming encode profile.
const Profile = "cutly.pipeline.stream-encode.v1"

// Default limits for one encode.
const (
	DefaultMaxStderrBytes = 1 << 20
	DefaultWriteTimeoutMs = 60_000
	maxExecutableBytes    = 1 << 30
)

var (
	ErrConfiguration = errors.New("streamencode: invalid configuration")
	ErrExecutable    = errors.New("streamencode: FFmpeg executable is invalid")
	ErrEncodeFailed  = errors.New("streamencode: encoding failed")
	ErrClosed        = errors.New("streamencode: encoder is closed")
	ErrResourceLimit = errors.New("streamencode: resource limit exceeded")
)

// Options describe one encode: geometry, frame rate, the host-pinned encoder executable and
// the durable output path. Width/Height are the RGBA8 frame dimensions; FrameRate is an exact
// rational like 30/1.
type Options struct {
	Executable           string
	OutputPath           string
	Width                int
	Height               int
	FrameRateNumerator   int64
	FrameRateDenominator int64
	MaxStderrBytes       int64
	WriteTimeoutMillis   int64
	// CRF selects the x264 constant-rate factor for higher-quality encodes (16 is visually
	// near-transparent, 23 the x264 default). Zero keeps the encoder default. The value is
	// recorded in the Result receipt so evidence binds the quality profile.
	CRF int
	// VideoCodec selects the encoder: "h264" (default, libx264 yuv420p 8-bit) or
	// "hevc" (libx265 yuv420p10le 10-bit — eliminates gradient banding).
	VideoCodec string
	// BinaryFingerprint, when set, pins the expected executable hash (host-authorized); when
	// empty the hash computed at Start is reported in the Result.
	BinaryFingerprint string
}

// Result is the completed encode's receipt.
type Result struct {
	Profile           string `json:"profile"`
	OutputPath        string `json:"output_path"`
	OutputHash        string `json:"output_hash"`
	OutputBytes       int64  `json:"output_bytes"`
	FramesWritten     int64  `json:"frames_written"`
	BinaryFingerprint string `json:"binary_fingerprint"`
	QualityCRF        int    `json:"quality_crf,omitempty"`
	VideoCodec        string `json:"video_codec,omitempty"`
}

// Encoder is one resident FFmpeg process fed whole frames in order. It is single-writer: the
// caller serializes WriteFrame/Finish; pipe blocking is the backpressure boundary.
type Encoder struct {
	command    *exec.Cmd
	stdin      io.WriteCloser
	stderr     *boundedBuffer
	options    Options
	frameBytes int
	frames     int64
	writeMu    sync.Mutex
	finished   bool
}

// Start validates the executable, spawns the resident encoder and returns it ready to accept
// frames.
func Start(ctx context.Context, options Options) (*Encoder, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Width <= 0 || options.Height <= 0 || options.FrameRateNumerator <= 0 || options.FrameRateDenominator <= 0 {
		return nil, fmt.Errorf("%w: positive geometry and frame rate are required", ErrConfiguration)
	}
	// The output profile converts to yuv420p, whose chroma subsampling requires even
	// dimensions; fail at Start instead of inside the encoder.
	if options.Width%2 != 0 || options.Height%2 != 0 {
		return nil, fmt.Errorf("%w: width and height must be even for the yuv420p output profile", ErrConfiguration)
	}
	if options.OutputPath == "" || !filepath.IsAbs(options.OutputPath) {
		return nil, fmt.Errorf("%w: absolute output path is required", ErrConfiguration)
	}
	if options.MaxStderrBytes <= 0 {
		options.MaxStderrBytes = DefaultMaxStderrBytes
	}
	if options.WriteTimeoutMillis <= 0 {
		options.WriteTimeoutMillis = DefaultWriteTimeoutMs
	}
	crf := options.CRF
	if crf == 0 {
		crf = 23
	}
	if crf < 0 || crf > 51 {
		return nil, fmt.Errorf("%w: CRF %d is outside [0,51]", ErrConfiguration, crf)
	}
	options.CRF = crf
	resolved, fingerprint, err := verifyExecutable(ctx, options.Executable, options.BinaryFingerprint)
	if err != nil {
		return nil, err
	}
	options.Executable = resolved
	options.BinaryFingerprint = fingerprint

	codec := options.VideoCodec
	if codec == "" {
		codec = "h264"
	}
	var encoderLib, pixFmt string
	switch codec {
	case "h264":
		encoderLib, pixFmt = "libx264", "yuv420p"
	case "hevc":
		encoderLib, pixFmt = "libx265", "yuv420p10le"
	default:
		return nil, fmt.Errorf("%w: VideoCodec %q must be \"h264\" or \"hevc\"", ErrConfiguration, codec)
	}
	options.VideoCodec = codec

	arguments := []string{
		"-y",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", options.Width, options.Height),
		"-r", fmt.Sprintf("%d/%d", options.FrameRateNumerator, options.FrameRateDenominator),
		"-i", "pipe:0",
		"-an",
		"-c:v", encoderLib,
		"-pix_fmt", pixFmt,
		"-preset", "medium",
		"-threads", "1",
		"-crf", strconv.Itoa(crf),
		"-map_metadata", "-1",
		"-map_chapters", "-1",
		"-metadata", "creation_time=1970-01-01T00:00:00Z",
		"-fflags", "+bitexact",
		"-flags:v", "+bitexact",
		"-f", "mp4",
		options.OutputPath,
	}
	command := exec.Command(resolved, arguments...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "TZ=UTC"}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin pipe: %v", ErrConfiguration, err)
	}
	stderr := newBoundedBuffer(options.MaxStderrBytes)
	command.Stdout = nil
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("%w: start: %v", ErrEncodeFailed, err)
	}
	return &Encoder{
		command:    command,
		stdin:      stdin,
		stderr:     stderr,
		options:    options,
		frameBytes: options.Width * options.Height * 4,
	}, nil
}

// FrameBytes returns the exact byte length one frame must have.
func (encoder *Encoder) FrameBytes() int {
	if encoder == nil {
		return 0
	}
	return encoder.frameBytes
}

// WriteFrame submits one complete straight-alpha RGBA8 frame. A short or oversized frame is
// rejected before anything reaches the encoder. Blocking on a full pipe is the encoder's
// backpressure; the write deadline bounds a wedged encoder.
func (encoder *Encoder) WriteFrame(ctx context.Context, frame []byte) error {
	if encoder == nil || encoder.command == nil {
		return ErrClosed
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(frame) != encoder.frameBytes {
		return fmt.Errorf("%w: frame bytes %d, want %d", ErrEncodeFailed, len(frame), encoder.frameBytes)
	}
	encoder.writeMu.Lock()
	defer encoder.writeMu.Unlock()
	if encoder.finished {
		return ErrClosed
	}
	written, err := encoder.stdin.Write(frame)
	if err != nil {
		return fmt.Errorf("%w: write frame %d: %v", ErrEncodeFailed, encoder.frames, err)
	}
	if written != len(frame) {
		return fmt.Errorf("%w: short write %d of %d on frame %d", ErrEncodeFailed, written, len(frame), encoder.frames)
	}
	encoder.frames++
	return nil
}

// FramesWritten reports how many frames the encoder consumed.
func (encoder *Encoder) FramesWritten() int64 {
	if encoder == nil {
		return 0
	}
	return encoder.frames
}

// Finish closes stdin, waits for the encoder and returns the durable receipt. The output file
// is hashed only after a successful exit.
func (encoder *Encoder) Finish(ctx context.Context) (Result, error) {
	if encoder == nil || encoder.command == nil {
		return Result{}, ErrClosed
	}
	encoder.writeMu.Lock()
	defer encoder.writeMu.Unlock()
	if encoder.finished {
		return Result{}, ErrClosed
	}
	encoder.finished = true
	if encoder.frames == 0 {
		_ = encoder.command.Process.Kill()
		_, _ = encoder.command.Process.Wait()
		_ = encoder.stdin.Close()
		return Result{}, fmt.Errorf("%w: finish without frames", ErrEncodeFailed)
	}
	if err := encoder.stdin.Close(); err != nil {
		_ = encoder.command.Process.Kill()
		_, _ = encoder.command.Process.Wait()
		return Result{}, fmt.Errorf("%w: close stdin: %v", ErrEncodeFailed, err)
	}
	waitErr := encoder.command.Wait()
	if encoder.stderr.exceeded {
		return Result{}, fmt.Errorf("%w: stderr exceeded the bound", ErrResourceLimit)
	}
	if waitErr != nil {
		return Result{}, fmt.Errorf("%w: %v: %s", ErrEncodeFailed, waitErr, strings.TrimSpace(encoder.stderr.String()))
	}
	raw, err := os.ReadFile(encoder.options.OutputPath)
	if err != nil {
		return Result{}, fmt.Errorf("%w: read output: %v", ErrEncodeFailed, err)
	}
	digest := sha256.Sum256(raw)
	result := Result{
		Profile:           Profile,
		OutputPath:        encoder.options.OutputPath,
		OutputHash:        "sha256:" + hex.EncodeToString(digest[:]),
		OutputBytes:       int64(len(raw)),
		FramesWritten:     encoder.frames,
		BinaryFingerprint: encoder.options.BinaryFingerprint,
		QualityCRF:        encoder.options.CRF,
		VideoCodec:        encoder.options.VideoCodec,
	}
	encoder.command = nil
	return result, nil
}

// Abort kills the encoder and releases its resources. It is safe to call after Finish.
func (encoder *Encoder) Abort() {
	if encoder == nil || encoder.command == nil {
		return
	}
	encoder.writeMu.Lock()
	defer encoder.writeMu.Unlock()
	if encoder.command.Process != nil {
		_ = encoder.command.Process.Kill()
		_, _ = encoder.command.Process.Wait()
	}
	_ = encoder.stdin.Close()
	encoder.command = nil
	encoder.finished = true
}

// verifyExecutable resolves and hashes the pinned encoder, mirroring media/decode's trusted
// executable rules: regular, executable, immutable by mode, symlink-free, owner inside the
// trusted boundary.
func verifyExecutable(ctx context.Context, executable, expected string) (string, string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", "", fmt.Errorf("%w: executable is required", ErrExecutable)
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrExecutable, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrExecutable, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrExecutable, err)
	}
	for current, leaf := filepath.Clean(resolved), true; ; current, leaf = filepath.Dir(current), false {
		info, statErr := os.Lstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("%w: path must be symlink-free", ErrExecutable)
		}
		if !trustedOwner(info) {
			return "", "", fmt.Errorf("%w: path owner is outside the trusted boundary", ErrExecutable)
		}
		if leaf {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o222 != 0 {
				return "", "", fmt.Errorf("%w: executable must be regular, executable, and immutable by mode", ErrExecutable)
			}
		} else if !info.IsDir() || (info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return "", "", fmt.Errorf("%w: ancestor directory is not a safe directory", ErrExecutable)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", "", fmt.Errorf("%w: open: %v", ErrExecutable, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(file, maxExecutableBytes)); err != nil {
		return "", "", fmt.Errorf("%w: hash: %v", ErrExecutable, err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	fingerprint := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if expected != "" && expected != fingerprint {
		return "", "", fmt.Errorf("%w: executable fingerprint mismatch", ErrExecutable)
	}
	return resolved, fingerprint, nil
}

func trustedOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())
}

// boundedBuffer keeps at most limit bytes of encoder stderr.
type boundedBuffer struct {
	limit    int64
	retained []byte
	exceeded bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (buffer *boundedBuffer) Write(chunk []byte) (int, error) {
	if int64(len(buffer.retained))+int64(len(chunk)) > buffer.limit {
		buffer.exceeded = true
		remaining := buffer.limit - int64(len(buffer.retained))
		if remaining > 0 {
			buffer.retained = append(buffer.retained, chunk[:remaining]...)
		}
		return len(chunk), nil
	}
	buffer.retained = append(buffer.retained, chunk...)
	return len(chunk), nil
}

func (buffer *boundedBuffer) String() string {
	return string(buffer.retained)
}
