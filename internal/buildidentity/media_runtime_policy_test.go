package buildidentity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMediaRuntimePolicyLDFlagsSmoke(t *testing.T) {
	if os.Getenv("CUTLY_TEST_EXPECT_PACKAGED_MEDIA_RUNTIME") != "1" {
		t.Skip("set CUTLY_TEST_EXPECT_PACKAGED_MEDIA_RUNTIME=1 and pass the packaged media-runtime linker stamps")
	}
	policy, err := ResolveMediaRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := hex.DecodeString(os.Getenv("CUTLY_TEST_MEDIA_RUNTIME_PUBLIC_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Distribution != MediaRuntimeDistributionPackaged || policy.Source != "packaged-build" ||
		policy.ExpectedBundleFingerprint != mediaRuntimeDigest('a') || len(policy.TrustedKeys) != 1 ||
		!bytes.Equal(policy.TrustedKeys["test-release"], wantKey) {
		t.Fatalf("linker-stamped policy = %+v", policy)
	}
}

func TestPackagedMediaRuntimePolicyPinsKeysAndExactBundle(t *testing.T) {
	first := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	second := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	stamp := "release-2026=" + hex.EncodeToString(first) + ",release-2027=" + hex.EncodeToString(second)
	restore := setMediaRuntimeBuildState(t, MediaRuntimeDistributionPackaged, stamp, mediaRuntimeDigest('a'))
	defer restore()

	policy, err := ResolveMediaRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Distribution != MediaRuntimeDistributionPackaged || policy.Source != "packaged-build" ||
		policy.ExpectedBundleFingerprint != mediaRuntimeDigest('a') || len(policy.TrustedKeys) != 2 ||
		!bytes.Equal(policy.TrustedKeys["release-2026"], first) ||
		!bytes.Equal(policy.TrustedKeys["release-2027"], second) {
		t.Fatalf("policy = %+v", policy)
	}
	policy.TrustedKeys["release-2026"][0] ^= 0xff
	again, err := ResolveMediaRuntimePolicy()
	if err != nil || !bytes.Equal(again.TrustedKeys["release-2026"], first) {
		t.Fatalf("policy retained caller mutation: %+v, %v", again, err)
	}
}

func TestDevelopmentMediaRuntimePolicyHasNoRuntimeTrust(t *testing.T) {
	restore := setMediaRuntimeBuildState(t, MediaRuntimeDistributionDevelopment, "", "")
	defer restore()
	policy, err := ResolveMediaRuntimePolicy()
	if err != nil || policy.Distribution != MediaRuntimeDistributionDevelopment ||
		policy.Source != "development-build" || len(policy.TrustedKeys) != 0 ||
		policy.ExpectedBundleFingerprint != "" {
		t.Fatalf("development policy = %+v, %v", policy, err)
	}
}

func TestMediaRuntimePolicyFailsClosed(t *testing.T) {
	validKey := hex.EncodeToString(bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize))
	for _, test := range []struct {
		name, mode, keys, fingerprint string
		want                          error
	}{
		{name: "missing keys", mode: MediaRuntimeDistributionPackaged, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyUnconfigured},
		{name: "missing fingerprint", mode: MediaRuntimeDistributionPackaged, keys: "release=" + validKey, want: ErrMediaRuntimePolicyUnconfigured},
		{name: "uppercase fingerprint", mode: MediaRuntimeDistributionPackaged, keys: "release=" + validKey, fingerprint: "sha256:" + strings.Repeat("A", 64), want: ErrMediaRuntimePolicyUnconfigured},
		{name: "unknown mode", mode: "future", want: ErrMediaRuntimePolicyInvalid},
		{name: "mode whitespace", mode: " packaged ", keys: "release=" + validKey, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "orphan keys", mode: MediaRuntimeDistributionDevelopment, keys: "release=" + validKey, want: ErrMediaRuntimePolicyInvalid},
		{name: "orphan fingerprint", mode: MediaRuntimeDistributionDevelopment, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "unsorted keys", mode: MediaRuntimeDistributionPackaged, keys: "z=" + validKey + ",a=" + validKey, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "duplicate keys", mode: MediaRuntimeDistributionPackaged, keys: "a=" + validKey + ",a=" + validKey, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "bad key id", mode: MediaRuntimeDistributionPackaged, keys: "Bad=" + validKey, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "short key", mode: MediaRuntimeDistributionPackaged, keys: "a=00", fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyInvalid},
		{name: "whitespace", mode: MediaRuntimeDistributionPackaged, keys: " a=" + validKey, fingerprint: mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyUnconfigured},
		{name: "fingerprint whitespace", mode: MediaRuntimeDistributionPackaged, keys: "release=" + validKey, fingerprint: " " + mediaRuntimeDigest('a'), want: ErrMediaRuntimePolicyUnconfigured},
	} {
		t.Run(test.name, func(t *testing.T) {
			restore := setMediaRuntimeBuildState(t, test.mode, test.keys, test.fingerprint)
			defer restore()
			policy, err := ResolveMediaRuntimePolicy()
			if !errors.Is(err, test.want) || !reflect.DeepEqual(policy, MediaRuntimePolicy{}) {
				t.Fatalf("policy=%+v error=%v want=%v", policy, err, test.want)
			}
		})
	}
}

func setMediaRuntimeBuildState(t *testing.T, mode, keys, fingerprint string) func() {
	t.Helper()
	oldMode := mediaRuntimeDistributionMode
	oldKeys := stampedMediaRuntimeTrustedKeys
	oldFingerprint := stampedMediaRuntimeBundleFingerprint
	mediaRuntimeDistributionMode = mode
	stampedMediaRuntimeTrustedKeys = keys
	stampedMediaRuntimeBundleFingerprint = fingerprint
	return func() {
		mediaRuntimeDistributionMode = oldMode
		stampedMediaRuntimeTrustedKeys = oldKeys
		stampedMediaRuntimeBundleFingerprint = oldFingerprint
	}
}

func mediaRuntimeDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
