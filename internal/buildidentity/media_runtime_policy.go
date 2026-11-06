package buildidentity

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	MediaRuntimeDistributionDevelopment = "development"
	MediaRuntimeDistributionPackaged    = "packaged"
)

var (
	ErrMediaRuntimePolicyUnconfigured = errors.New("buildidentity: media runtime policy is not configured")
	ErrMediaRuntimePolicyInvalid      = errors.New("buildidentity: media runtime policy is invalid")
)

var mediaRuntimeKeyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// These values are intentionally unexported release inputs. Production builds
// may set them with -ldflags -X; documents, Agent requests, command-line flags,
// and runtime environment variables cannot replace them.
var (
	mediaRuntimeDistributionMode         = MediaRuntimeDistributionDevelopment
	stampedMediaRuntimeTrustedKeys       string
	stampedMediaRuntimeBundleFingerprint string
)

// MediaRuntimePolicy is the immutable release-owned trust policy for one exact
// signed FFmpeg/FFprobe bundle. TrustedKeys is a defensive copy.
type MediaRuntimePolicy struct {
	Distribution              string
	TrustedKeys               map[string]ed25519.PublicKey
	ExpectedBundleFingerprint string
	Source                    string
}

// ResolveMediaRuntimePolicy reads only compile-time state. In particular, it
// never accepts an environment or command-line trust key. Development builds
// return an empty policy so callers can expose a separately gated local path;
// packaged builds require both a canonical key set and one exact bundle pin.
func ResolveMediaRuntimePolicy() (MediaRuntimePolicy, error) {
	switch mediaRuntimeDistributionMode {
	case MediaRuntimeDistributionDevelopment:
		if strings.TrimSpace(stampedMediaRuntimeTrustedKeys) != "" ||
			strings.TrimSpace(stampedMediaRuntimeBundleFingerprint) != "" {
			return MediaRuntimePolicy{}, fmt.Errorf(
				"%w: packaged media runtime stamps are present without distribution mode %q",
				ErrMediaRuntimePolicyInvalid,
				MediaRuntimeDistributionPackaged,
			)
		}
		return MediaRuntimePolicy{
			Distribution: MediaRuntimeDistributionDevelopment,
			Source:       "development-build",
		}, nil
	case MediaRuntimeDistributionPackaged:
		keys, err := decodeMediaRuntimeTrustedKeys(stampedMediaRuntimeTrustedKeys)
		if err != nil {
			return MediaRuntimePolicy{}, err
		}
		fingerprint := stampedMediaRuntimeBundleFingerprint
		if !canonicalMediaRuntimeSHA256(fingerprint) {
			return MediaRuntimePolicy{}, fmt.Errorf(
				"%w: packaged bundle fingerprint must be canonical sha256",
				ErrMediaRuntimePolicyUnconfigured,
			)
		}
		return MediaRuntimePolicy{
			Distribution:              MediaRuntimeDistributionPackaged,
			TrustedKeys:               keys,
			ExpectedBundleFingerprint: fingerprint,
			Source:                    "packaged-build",
		}, nil
	default:
		return MediaRuntimePolicy{}, fmt.Errorf(
			"%w: unknown distribution mode %q",
			ErrMediaRuntimePolicyInvalid,
			mediaRuntimeDistributionMode,
		)
	}
}

// The linker stamp is a comma-separated, strictly key-ID-sorted sequence of
// key_id=64-lowercase-hex-public-key records. Public keys are release trust
// anchors, not secrets; private signing keys never enter the build.
func decodeMediaRuntimeTrustedKeys(stamp string) (map[string]ed25519.PublicKey, error) {
	if stamp == "" || strings.TrimSpace(stamp) != stamp {
		return nil, fmt.Errorf("%w: packaged trusted key stamp is empty or noncanonical", ErrMediaRuntimePolicyUnconfigured)
	}
	records := strings.Split(stamp, ",")
	if len(records) == 0 || len(records) > 256 {
		return nil, fmt.Errorf("%w: packaged trusted key count", ErrMediaRuntimePolicyInvalid)
	}
	keys := make(map[string]ed25519.PublicKey, len(records))
	prior := ""
	for index, record := range records {
		keyID, encoded, found := strings.Cut(record, "=")
		if !found || !mediaRuntimeKeyIDPattern.MatchString(keyID) ||
			(index > 0 && keyID <= prior) || len(encoded) != ed25519.PublicKeySize*2 ||
			encoded != strings.ToLower(encoded) {
			return nil, fmt.Errorf("%w: packaged trusted key %d", ErrMediaRuntimePolicyInvalid, index)
		}
		publicKey, err := hex.DecodeString(encoded)
		if err != nil || len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: packaged trusted key %d", ErrMediaRuntimePolicyInvalid, index)
		}
		keys[keyID] = ed25519.PublicKey(append([]byte(nil), publicKey...))
		prior = keyID
	}
	return keys, nil
}

func canonicalMediaRuntimeSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
