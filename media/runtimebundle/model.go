package runtimebundle

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
	"runtime"
)

const (
	SchemaVersion            = 1
	ManifestProfileV1        = "cutly.media-runtime-bundle.v1"
	ClosureProfileV1         = "cutly.media-runtime-closure.v1"
	SignatureAlgorithmV1     = "ed25519.v1"
	ManifestFileName         = "cutly-runtime-bundle.json"
	SignatureDomainV1        = "cutly.media-runtime-bundle.signature.ed25519.v1"
	ToolFFmpeg               = "ffmpeg"
	ToolFFprobe              = "ffprobe"
	DefaultMaxManifestBytes  = int64(8 << 20)
	DefaultMaxFiles          = 4096
	DefaultMaxClosureNodes   = 8192
	DefaultMaxTotalFileBytes = int64(16 << 30)
	MaxRelativePathBytes     = 1024
)

var (
	ErrConfiguration = errors.New("runtimebundle: invalid configuration")
	ErrManifest      = errors.New("runtimebundle: invalid manifest")
	ErrTrust         = errors.New("runtimebundle: untrusted signing key")
	ErrSignature     = errors.New("runtimebundle: signature verification failed")
	ErrPlatform      = errors.New("runtimebundle: platform mismatch")
	ErrClosure       = errors.New("runtimebundle: invalid bundle closure")
	ErrContent       = errors.New("runtimebundle: bundle content mismatch")
	ErrLimit         = errors.New("runtimebundle: resource limit exceeded")

	identityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// FileEntry is one signed member of the complete runtime closure. Tools is a
// strictly sorted set containing ffmpeg, ffprobe, or both.
type FileEntry struct {
	Path        string   `json:"path"`
	SizeBytes   int64    `json:"size_bytes"`
	Mode        uint32   `json:"mode"`
	ContentHash string   `json:"content_hash"`
	Tools       []string `json:"tools"`
}

// Manifest is the complete signed wire object. BundleFingerprint is derived
// from the canonical payload without signing metadata. Signature authenticates
// every other field, including that fingerprint and KeyID.
type Manifest struct {
	SchemaVersion        int         `json:"schema_version"`
	Profile              string      `json:"profile"`
	CanonicalJSONProfile string      `json:"canonical_json_profile"`
	Platform             string      `json:"platform"`
	Architecture         string      `json:"architecture"`
	FFmpegPath           string      `json:"ffmpeg_path"`
	FFprobePath          string      `json:"ffprobe_path"`
	Files                []FileEntry `json:"files"`
	BundleFingerprint    string      `json:"bundle_fingerprint"`
	KeyID                string      `json:"key_id"`
	SignatureAlgorithm   string      `json:"signature_algorithm"`
	Signature            string      `json:"signature"`
}

type manifestPayload struct {
	SchemaVersion        int         `json:"schema_version"`
	Profile              string      `json:"profile"`
	CanonicalJSONProfile string      `json:"canonical_json_profile"`
	Platform             string      `json:"platform"`
	Architecture         string      `json:"architecture"`
	FFmpegPath           string      `json:"ffmpeg_path"`
	FFprobePath          string      `json:"ffprobe_path"`
	Files                []FileEntry `json:"files"`
}

type unsignedManifest struct {
	SchemaVersion        int         `json:"schema_version"`
	Profile              string      `json:"profile"`
	CanonicalJSONProfile string      `json:"canonical_json_profile"`
	Platform             string      `json:"platform"`
	Architecture         string      `json:"architecture"`
	FFmpegPath           string      `json:"ffmpeg_path"`
	FFprobePath          string      `json:"ffprobe_path"`
	Files                []FileEntry `json:"files"`
	BundleFingerprint    string      `json:"bundle_fingerprint"`
	KeyID                string      `json:"key_id"`
	SignatureAlgorithm   string      `json:"signature_algorithm"`
}

type closureIdentity struct {
	SchemaVersion        int         `json:"schema_version"`
	Profile              string      `json:"profile"`
	CanonicalJSONProfile string      `json:"canonical_json_profile"`
	Platform             string      `json:"platform"`
	Architecture         string      `json:"architecture"`
	Tool                 string      `json:"tool"`
	ExecutablePath       string      `json:"executable_path"`
	Files                []FileEntry `json:"files"`
}

// Options are host-owned trust and admission policy. Zero platform and
// architecture select the current Go target. TrustedKeys is copied by New.
type Options struct {
	Platform          string
	Architecture      string
	ManifestName      string
	TrustedKeys       map[string]ed25519.PublicKey
	MaxManifestBytes  int64
	MaxFiles          int
	MaxTotalFileBytes int64
	MaxClosureNodes   int
}

// Executable binds one tool path to its signed binary bytes and complete
// dependency closure. Path is informational execution input; callers must keep
// the verified bundle root host-owned and non-writable by untrusted principals.
type Executable struct {
	Path               string `json:"path"`
	RelativePath       string `json:"relative_path"`
	BinaryContentHash  string `json:"binary_content_hash"`
	ClosureFingerprint string `json:"closure_fingerprint"`
}

// Resolution is published only after every signature, closure and byte check
// succeeds. It deliberately contains no partially verified state.
type Resolution struct {
	BundleRoot        string     `json:"bundle_root"`
	Manifest          Manifest   `json:"manifest"`
	BundleFingerprint string     `json:"bundle_fingerprint"`
	FFmpeg            Executable `json:"ffmpeg"`
	FFprobe           Executable `json:"ffprobe"`
}

// Resolver is immutable and safe for concurrent Resolve calls.
type Resolver struct {
	platform          string
	architecture      string
	manifestName      string
	trustedKeys       map[string]ed25519.PublicKey
	maxManifestBytes  int64
	maxFiles          int
	maxTotalFileBytes int64
	maxClosureNodes   int
}

func New(options Options) (*Resolver, error) {
	platform := options.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	architecture := options.Architecture
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	manifestName := options.ManifestName
	if manifestName == "" {
		manifestName = ManifestFileName
	}
	maxManifestBytes := options.MaxManifestBytes
	if maxManifestBytes == 0 {
		maxManifestBytes = DefaultMaxManifestBytes
	}
	maxFiles := options.MaxFiles
	if maxFiles == 0 {
		maxFiles = DefaultMaxFiles
	}
	maxTotalBytes := options.MaxTotalFileBytes
	if maxTotalBytes == 0 {
		maxTotalBytes = DefaultMaxTotalFileBytes
	}
	maxClosureNodes := options.MaxClosureNodes
	if maxClosureNodes == 0 {
		maxClosureNodes = DefaultMaxClosureNodes
	}
	if !identityPattern.MatchString(platform) || !identityPattern.MatchString(architecture) ||
		!validRelativePath(manifestName) || maxManifestBytes < 1 || maxManifestBytes > DefaultMaxManifestBytes ||
		maxFiles < 2 || maxFiles > DefaultMaxFiles || maxTotalBytes < 1 || maxTotalBytes > DefaultMaxTotalFileBytes ||
		maxClosureNodes < 3 || maxClosureNodes > DefaultMaxClosureNodes ||
		len(options.TrustedKeys) == 0 || len(options.TrustedKeys) > 256 {
		return nil, ErrConfiguration
	}
	keys := make(map[string]ed25519.PublicKey, len(options.TrustedKeys))
	for keyID, publicKey := range options.TrustedKeys {
		if !identityPattern.MatchString(keyID) || len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: invalid trusted key", ErrConfiguration)
		}
		keys[keyID] = append(ed25519.PublicKey(nil), publicKey...)
	}
	return &Resolver{
		platform: platform, architecture: architecture, manifestName: manifestName,
		trustedKeys: keys, maxManifestBytes: maxManifestBytes, maxFiles: maxFiles,
		maxTotalFileBytes: maxTotalBytes, maxClosureNodes: maxClosureNodes,
	}, nil
}
