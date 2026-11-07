package buildidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setProviderBuildState(t *testing.T, mode, digest string) {
	t.Helper()
	oldMode, oldDigest := providerDistributionMode, stampedProviderExecutableDigest
	providerDistributionMode, stampedProviderExecutableDigest = mode, digest
	t.Cleanup(func() { providerDistributionMode, stampedProviderExecutableDigest = oldMode, oldDigest })
}

func providerTestDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func providerTestPolicy(t *testing.T, mode, digest string) ProviderPolicy {
	t.Helper()
	setProviderBuildState(t, mode, digest)
	policy, err := ResolveProviderPolicy()
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func providerFixture(t *testing.T, data []byte) string {
	t.Helper()
	if !providerMeasurementSupported {
		t.Skip("native provider measurement unsupported on this runtime")
	}
	// macOS /var and /tmp aliases intentionally fail the all-ancestor rule.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provider-fixture")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireZeroProviderIdentity(t *testing.T, identity ProviderExecutableIdentity, err error) {
	t.Helper()
	if err == nil || identity != (ProviderExecutableIdentity{}) || identity.Valid() || identity.Digest() != "" {
		t.Fatalf("failure must return zero identity: identity=%+v error=%v", identity, err)
	}
}

func TestProviderPolicyReleaseInputs(t *testing.T) {
	pin := providerTestDigest([]byte("release provider, not controlling host"))
	for _, test := range []struct {
		name, mode, digest string
		want               error
	}{
		{"development", ProviderDistributionDevelopment, "", nil},
		{"packaged", ProviderDistributionPackaged, pin, nil},
		{"missing packaged pin", ProviderDistributionPackaged, "", ErrProviderPolicyUnconfigured},
		{"uppercase", ProviderDistributionPackaged, "sha256:" + strings.Repeat("A", 64), ErrProviderPolicyUnconfigured},
		{"bad hex", ProviderDistributionPackaged, "sha256:" + strings.Repeat("g", 64), ErrProviderPolicyUnconfigured},
		{"short", ProviderDistributionPackaged, "sha256:00", ErrProviderPolicyUnconfigured},
		{"bare hash", ProviderDistributionPackaged, strings.Repeat("a", 64), ErrProviderPolicyUnconfigured},
		{"whitespace", ProviderDistributionPackaged, " " + pin, ErrProviderPolicyUnconfigured},
		{"unknown", "future", pin, ErrProviderPolicyInvalid},
		{"mode whitespace", " packaged ", pin, ErrProviderPolicyInvalid},
		{"development stamp", ProviderDistributionDevelopment, pin, ErrProviderPolicyInvalid},
		{"development blank stamp", ProviderDistributionDevelopment, " ", ErrProviderPolicyInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			setProviderBuildState(t, test.mode, test.digest)
			policy, err := ResolveProviderPolicy()
			if test.want != nil {
				if !errors.Is(err, test.want) || policy != (ProviderPolicy{}) {
					t.Fatalf("policy=%+v err=%v", policy, err)
				}
				return
			}
			if err != nil || !policy.Valid() || policy.Distribution() != test.mode || policy.ExpectedExecutableDigest() != test.digest || policy.Source() != test.mode+"-build" {
				t.Fatalf("policy=%+v err=%v", policy, err)
			}
			// Existing values are immutable snapshots of the resolved host state.
			providerDistributionMode, stampedProviderExecutableDigest = "invalid", "invalid"
			if !policy.Valid() || policy.ExpectedExecutableDigest() != test.digest {
				t.Fatal("policy changed after resolution")
			}
		})
	}
	for _, value := range []any{ProviderPolicy{}, ProviderExecutableIdentity{}} {
		typeOf := reflect.TypeOf(value)
		for index := 0; index < typeOf.NumField(); index++ {
			if typeOf.Field(index).IsExported() {
				t.Fatalf("%v exposes mutable field", typeOf)
			}
		}
	}
}

func TestProviderEnvironmentCannotChangeHostPolicy(t *testing.T) {
	for _, mode := range []string{ProviderDistributionDevelopment, ProviderDistributionPackaged} {
		t.Run(mode, func(t *testing.T) {
			pin := ""
			if mode == ProviderDistributionPackaged {
				pin = providerTestDigest([]byte("pinned"))
			}
			setProviderBuildState(t, mode, pin)
			before, err := ResolveProviderPolicy()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"CUTLY_PROVIDER_DISTRIBUTION_MODE", "CUTLY_PROVIDER_EXECUTABLE_DIGEST", "PROVIDER_DISTRIBUTION_MODE", "PROVIDER_EXECUTABLE_DIGEST", "providerDistributionMode", "stampedProviderExecutableDigest"} {
				t.Setenv(name, "sha256:"+strings.Repeat("f", 64))
			}
			after, err := ResolveProviderPolicy()
			if err != nil || before != after {
				t.Fatalf("environment changed policy: %+v %v", after, err)
			}
		})
	}
}

func TestProviderExecutableMeasurementAuthorization(t *testing.T) {
	data := bytes.Repeat([]byte("fixture never executed\n"), 10000)
	path := providerFixture(t, data)
	development := providerTestPolicy(t, ProviderDistributionDevelopment, "")
	identity, err := development.MeasureLocalDevelopmentExecutable(context.Background(), path)
	if err != nil || !identity.Valid() || identity.Digest() != providerTestDigest(data) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	packaged := providerTestPolicy(t, ProviderDistributionPackaged, providerTestDigest(data))
	matched, err := packaged.MeasurePackagedExecutable(context.Background(), path)
	if err != nil || matched != identity {
		t.Fatalf("matched=%+v err=%v", matched, err)
	}
	mismatch := providerTestPolicy(t, ProviderDistributionPackaged, providerTestDigest([]byte("other")))
	got, err := mismatch.MeasurePackagedExecutable(context.Background(), path)
	requireZeroProviderIdentity(t, got, err)
	if !errors.Is(err, ErrProviderExecutableMismatch) {
		t.Fatal(err)
	}
	for _, policy := range []ProviderPolicy{{}, packaged} {
		got, err := policy.MeasureLocalDevelopmentExecutable(context.Background(), path)
		requireZeroProviderIdentity(t, got, err)
	}
	for _, policy := range []ProviderPolicy{{}, development} {
		got, err := policy.MeasurePackagedExecutable(context.Background(), path)
		requireZeroProviderIdentity(t, got, err)
	}
}

func TestProviderExecutableMeasurementRejectsInvalidFiles(t *testing.T) {
	policy := providerTestPolicy(t, ProviderDistributionDevelopment, "")
	for _, name := range []string{"relative", "unclean", "missing", "empty", "oversize", "directory", "symlink", "parent symlink", "noexec", "nil context", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			path := providerFixture(t, []byte("fixture"))
			ctx := context.Background()
			switch name {
			case "relative":
				path = "provider-fixture"
			case "unclean":
				path = filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)
			case "missing":
				path += "missing"
			case "empty":
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(path, MaxProviderExecutableBytes+1); err != nil {
					t.Fatal(err)
				}
			case "directory":
				path = filepath.Dir(path)
			case "symlink":
				link := path + "-link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "parent symlink":
				link := filepath.Join(filepath.Dir(path), "alias")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "noexec":
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "nil context":
				ctx = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := policy.MeasureLocalDevelopmentExecutable(ctx, path)
			requireZeroProviderIdentity(t, got, err)
			if name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

// Err runs a deterministic mutation after the first buffer has been hashed.
// No global test hook, goroutine races, timing assumptions, or binary execution.
type providerMeasurementContext struct {
	context.Context
	calls, trigger int
	action         func()
	cancelled      bool
}

func (c *providerMeasurementContext) Err() error {
	c.calls++
	if c.calls == c.trigger {
		c.action()
	}
	if c.cancelled {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestProviderExecutableMeasurementDetectsMutationAndCancellation(t *testing.T) {
	policy := providerTestPolicy(t, ProviderDistributionDevelopment, "")
	for _, name := range []string{"grow", "shrink", "same-size-write", "replace", "chmod", "cancel"} {
		t.Run(name, func(t *testing.T) {
			path := providerFixture(t, bytes.Repeat([]byte("a"), 3*providerMeasurementBufferBytes))
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(strings.TrimPrefix(path, filepath.VolumeName(path)+string(filepath.Separator)), string(filepath.Separator))
			ctx := &providerMeasurementContext{Context: context.Background(), trigger: len(parts) + 2}
			ctx.action = func() {
				var err error
				switch name {
				case "grow":
					err = os.Truncate(path, before.Size()+1)
				case "shrink":
					err = os.Truncate(path, 1)
				case "same-size-write":
					err = os.WriteFile(path, bytes.Repeat([]byte("b"), int(before.Size())), 0o700)
					if err == nil {
						err = os.Chtimes(path, before.ModTime().Add(2000000000), before.ModTime().Add(2000000000))
					}
				case "replace":
					err = os.Rename(path, path+"-old")
					if err == nil {
						err = os.WriteFile(path, bytes.Repeat([]byte("b"), int(before.Size())), 0o700)
					}
				case "chmod":
					err = os.Chmod(path, 0o600)
				case "cancel":
					ctx.cancelled = true
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := policy.MeasureLocalDevelopmentExecutable(ctx, path)
			requireZeroProviderIdentity(t, got, err)
			if name == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrProviderExecutableChanged) {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderUnsupportedRuntimeFailsClosed(t *testing.T) {
	if providerMeasurementSupported {
		t.Skip("native measurement supported")
	}
	policy := providerTestPolicy(t, ProviderDistributionDevelopment, "")
	got, err := policy.MeasureLocalDevelopmentExecutable(context.Background(), "/provider")
	requireZeroProviderIdentity(t, got, err)
	if !errors.Is(err, ErrProviderMeasurementUnsupported) {
		t.Fatal(err)
	}
}
