package videodecode

import (
	"fmt"
	"regexp"
	"runtime"
)

const (
	Implementation = "cutly.ffmpeg-video-decode"

	AdapterSemanticProfileV1       = "cutly.decode.ffmpeg-zscale-rgba64.v1"
	SelectionSemanticProfileV1     = "cutly.selection.display-pts-keyframe-forward.v1"
	ColorSemanticProfileV1         = "cutly.color.bt709-linear-rgba64le.v1"
	NormalizationSemanticProfileV1 = "cutly.geometry.square-sar-ffprobe-ccw-quarter-turn.v1"
	EnvironmentProfileV1           = "cutly.ffmpeg.environment.minimal.v1"
	RuntimeProfileV1               = "cutly.ffmpeg.runtime.cpu-feature-bound.v1"
	ExecutableTrustProfileV1       = "cutly.ffmpeg.executable.single-owner-path.v1"

	defaultMaxInputBytes  = int64(8 << 30)
	defaultMaxFrameBytes  = int64(512 << 20)
	defaultMaxStderrBytes = int64(1 << 20)
	defaultDecodeMillis   = int64(30_000)
	hardMaxInputBytes     = int64(1 << 40)
	hardMaxFrameBytes     = int64(1 << 30)
	hardMaxStderrBytes    = int64(16 << 20)
	hardDecodeMillis      = int64(10 * 60 * 1000)
	maxExecutableBytes    = int64(1 << 30)
)

var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// SemanticProfile is deliberately concrete. Changing frame selection, color
// conversion, geometry normalization, or the process boundary requires a new
// profile and therefore a different cache identity.
type SemanticProfile struct {
	Adapter       string `json:"adapter"`
	Selection     string `json:"selection"`
	Color         string `json:"color"`
	Normalization string `json:"normalization"`
}

// BuildIdentity is supplied by a controlled runtime resolver. The dependency
// fingerprint must cover FFmpeg's dynamic-library/filter closure (including
// libzimg), and the CPU fingerprint must cover the normalized feature set
// visible to libzimg's automatic dispatch. Version text alone proves neither.
type BuildIdentity struct {
	SemanticProfile              SemanticProfile `json:"semantic_profile"`
	RuntimeDependencyFingerprint string          `json:"runtime_dependency_fingerprint"`
	RuntimeCPUFingerprint        string          `json:"runtime_cpu_fingerprint"`
}

// RuntimeProfile is derived by the adapter, not accepted from media input. It
// prevents one cache identity from crossing OS/architecture or CPU-feature
// implementations. FFmpeg receives -cpuflags 0, but zscale/libzimg selects
// ZIMG_CPU_AUTO_64B independently; the controlled resolver must therefore
// bind a normalized CPU-feature fingerprint explicitly.
type RuntimeProfile struct {
	Profile         string `json:"profile"`
	GOOS            string `json:"goos"`
	GOARCH          string `json:"goarch"`
	FFmpegCPUFlags  string `json:"ffmpeg_cpu_flags"`
	ZimgCPUDispatch string `json:"zimg_cpu_dispatch"`
	CPUFingerprint  string `json:"cpu_fingerprint"`
	ExecutableTrust string `json:"executable_trust"`
}

func currentRuntimeProfile(cpuFingerprint string) RuntimeProfile {
	return RuntimeProfile{
		Profile: RuntimeProfileV1, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		FFmpegCPUFlags: "0", ZimgCPUDispatch: "ZIMG_CPU_AUTO_64B",
		CPUFingerprint: cpuFingerprint, ExecutableTrust: ExecutableTrustProfileV1,
	}
}

func DefaultBuildIdentity(runtimeDependencyFingerprint, runtimeCPUFingerprint string) BuildIdentity {
	return BuildIdentity{
		SemanticProfile: SemanticProfile{
			Adapter:       AdapterSemanticProfileV1,
			Selection:     SelectionSemanticProfileV1,
			Color:         ColorSemanticProfileV1,
			Normalization: NormalizationSemanticProfileV1,
		},
		RuntimeDependencyFingerprint: runtimeDependencyFingerprint,
		RuntimeCPUFingerprint:        runtimeCPUFingerprint,
	}
}

// Options are immutable adapter policy and part of its fingerprint. Zero
// values choose conservative defaults. MaxFrameBytes is the aggregate live Go
// storage budget for decoded-frame scratch plus the immutable Surface; FFmpeg
// child RSS is governed separately by the process worker/sandbox.
type Options struct {
	MaxInputBytes    int64 `json:"max_input_bytes"`
	MaxFrameBytes    int64 `json:"max_frame_bytes"`
	MaxStderrBytes   int64 `json:"max_stderr_bytes"`
	MaxDecodeMillis  int64 `json:"max_decode_millis"`
	MaxIndexedFrames int   `json:"max_indexed_frames"`
	MaxWidth         int   `json:"max_width"`
	MaxHeight        int   `json:"max_height"`
}

func normalizeOptions(value Options) (Options, error) {
	if value.MaxInputBytes == 0 {
		value.MaxInputBytes = defaultMaxInputBytes
	}
	if value.MaxFrameBytes == 0 {
		value.MaxFrameBytes = defaultMaxFrameBytes
	}
	if value.MaxStderrBytes == 0 {
		value.MaxStderrBytes = defaultMaxStderrBytes
	}
	if value.MaxDecodeMillis == 0 {
		value.MaxDecodeMillis = defaultDecodeMillis
	}
	if value.MaxIndexedFrames == 0 {
		value.MaxIndexedFrames = DefaultMaxIndexedFrames
	}
	if value.MaxWidth == 0 {
		value.MaxWidth = 16384
	}
	if value.MaxHeight == 0 {
		value.MaxHeight = 16384
	}
	if value.MaxInputBytes <= 0 || value.MaxInputBytes > hardMaxInputBytes ||
		value.MaxFrameBytes <= 0 || value.MaxFrameBytes > hardMaxFrameBytes ||
		value.MaxStderrBytes < 256 || value.MaxStderrBytes > hardMaxStderrBytes ||
		value.MaxDecodeMillis <= 0 || value.MaxDecodeMillis > hardDecodeMillis ||
		value.MaxWidth <= 0 || value.MaxWidth > 65536 || value.MaxHeight <= 0 || value.MaxHeight > 65536 {
		return Options{}, fmt.Errorf("%w: invalid decoder resource policy", ErrResourceLimit)
	}
	if _, err := normalizeMaximumFrames(value.MaxIndexedFrames); err != nil {
		return Options{}, err
	}
	return value, nil
}

func validateBuildIdentity(value BuildIdentity) error {
	want := DefaultBuildIdentity(value.RuntimeDependencyFingerprint, value.RuntimeCPUFingerprint)
	if value.SemanticProfile != want.SemanticProfile {
		return fmt.Errorf("%w: unsupported semantic profile", ErrExecutable)
	}
	if !fingerprintPattern.MatchString(value.RuntimeDependencyFingerprint) {
		return fmt.Errorf("%w: runtime dependency fingerprint must be canonical SHA-256", ErrExecutable)
	}
	if !fingerprintPattern.MatchString(value.RuntimeCPUFingerprint) {
		return fmt.Errorf("%w: runtime CPU fingerprint must be canonical SHA-256", ErrExecutable)
	}
	return nil
}
