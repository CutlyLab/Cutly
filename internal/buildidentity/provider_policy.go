package buildidentity

import (
	"context"
	"errors"
	"fmt"
)

const (
	ProviderDistributionDevelopment = "development"
	ProviderDistributionPackaged    = "packaged"

	// MaxProviderExecutableBytes bounds work before any measurement buffer is
	// allocated. It is a host policy limit, not a request-controlled allowance.
	MaxProviderExecutableBytes int64 = 512 * 1024 * 1024
)

var (
	ErrProviderPolicyUnconfigured     = errors.New("buildidentity: provider policy is not configured")
	ErrProviderPolicyInvalid          = errors.New("buildidentity: provider policy is invalid")
	ErrProviderExecutableInvalid      = errors.New("buildidentity: provider executable is invalid")
	ErrProviderExecutableChanged      = errors.New("buildidentity: provider executable changed during measurement")
	ErrProviderExecutableMismatch     = errors.New("buildidentity: provider executable does not match release pin")
	ErrProviderMeasurementUnsupported = errors.New("buildidentity: provider executable measurement is unsupported")
)

// Release inputs belong to the controlling HOST build, which selects a distinct
// provider artifact. They may be set with -ldflags -X. The digest is NOT a
// self-referential hash embedded in the provider binary being measured. Neither
// requests, environment variables, nor command-line trust flags set this policy.
var (
	providerDistributionMode        = ProviderDistributionDevelopment
	stampedProviderExecutableDigest string
)

// ProviderPolicy is an immutable, host-owned authorization policy. Its zero
// value is invalid; callers cannot construct a policy from a digest string.
// Resolving a development policy does not itself authorize any executable.
type ProviderPolicy struct {
	distribution     string
	executableDigest string
	source           string
}

func (p ProviderPolicy) Distribution() string             { return p.distribution }
func (p ProviderPolicy) ExpectedExecutableDigest() string { return p.executableDigest }
func (p ProviderPolicy) Source() string                   { return p.source }

// Valid reports whether this value was resolved as a configured host policy.
func (p ProviderPolicy) Valid() bool {
	switch p.distribution {
	case ProviderDistributionDevelopment:
		return p.source == "development-build" && p.executableDigest == ""
	case ProviderDistributionPackaged:
		return p.source == "packaged-build" && canonicalProviderSHA256(p.executableDigest)
	default:
		return false
	}
}

// ResolveProviderPolicy reads only linker/build-owned state and returns scalar
// values. Packaged hosts must pin one exact provider executable. Development
// hosts must have no packaged stamp; even a whitespace-only stamp is rejected.
func ResolveProviderPolicy() (ProviderPolicy, error) {
	switch providerDistributionMode {
	case ProviderDistributionDevelopment:
		if stampedProviderExecutableDigest != "" {
			return ProviderPolicy{}, fmt.Errorf("%w: executable stamp in development host", ErrProviderPolicyInvalid)
		}
		return ProviderPolicy{distribution: ProviderDistributionDevelopment, source: "development-build"}, nil
	case ProviderDistributionPackaged:
		if !canonicalProviderSHA256(stampedProviderExecutableDigest) {
			return ProviderPolicy{}, fmt.Errorf("%w: packaged host requires canonical sha256 executable pin", ErrProviderPolicyUnconfigured)
		}
		return ProviderPolicy{
			distribution:     ProviderDistributionPackaged,
			executableDigest: stampedProviderExecutableDigest,
			source:           "packaged-build",
		}, nil
	default:
		return ProviderPolicy{}, fmt.Errorf("%w: unknown distribution mode %q", ErrProviderPolicyInvalid, providerDistributionMode)
	}
}

// ProviderExecutableIdentity is opaque, immutable evidence of successful
// host-authorized measurement, not a caller-supplied digest assertion. Its zero
// value is invalid. It contains no path and grants no permission to launch one.
// Measurement closes the file: S6 execution must separately bind the launched
// bytes to these same measured bytes, rather than re-open an untrusted path.
type ProviderExecutableIdentity struct {
	digest string
}

func (i ProviderExecutableIdentity) Valid() bool { return canonicalProviderSHA256(i.digest) }

// Digest returns a canonical sha256 digest, or an empty string for zero identity.
func (i ProviderExecutableIdentity) Digest() string {
	if !i.Valid() {
		return ""
	}
	return i.digest
}

// MeasurePackagedExecutable measures a HOST-selected absolute executable path
// and authorizes it only if it matches this packaged host's release pin. Never
// pass a document/request-selected path. This does not launch the executable or
// make the pathname safe for a later launch (see ProviderExecutableIdentity).
func (p ProviderPolicy) MeasurePackagedExecutable(ctx context.Context, hostSelectedAbsolutePath string) (ProviderExecutableIdentity, error) {
	if !p.Valid() || p.distribution != ProviderDistributionPackaged {
		return ProviderExecutableIdentity{}, fmt.Errorf("%w: packaged measurement requires a packaged host policy", ErrProviderPolicyInvalid)
	}
	identity, err := measureProviderExecutable(ctx, hostSelectedAbsolutePath)
	if err != nil {
		return ProviderExecutableIdentity{}, err
	}
	if identity.digest != p.executableDigest {
		return ProviderExecutableIdentity{}, ErrProviderExecutableMismatch
	}
	return identity, nil
}

// MeasureLocalDevelopmentExecutable is the explicit local-development opt-in.
// It accepts only a HOST-selected absolute path, never request input, and only a
// resolved development policy. Measuring a digest is not packaged authorization
// and is not permission to later launch the pathname. No binary is executed.
func (p ProviderPolicy) MeasureLocalDevelopmentExecutable(ctx context.Context, hostSelectedAbsolutePath string) (ProviderExecutableIdentity, error) {
	if !p.Valid() || p.distribution != ProviderDistributionDevelopment {
		return ProviderExecutableIdentity{}, fmt.Errorf("%w: local measurement requires a development host policy", ErrProviderPolicyInvalid)
	}
	return measureProviderExecutable(ctx, hostSelectedAbsolutePath)
}

// MeasureProviderExecutableBinding measures a host-selected executable without
// granting release authorization. It exists for an already-launched child to
// bind its own bytes to a request that the packaged parent authorized before
// exec. The opaque result can be passed to trusted in-process constructors, but
// it must never replace ProviderPolicy.MeasurePackagedExecutable at a launch
// boundary.
func MeasureProviderExecutableBinding(ctx context.Context, hostSelectedAbsolutePath string) (ProviderExecutableIdentity, error) {
	return measureProviderExecutable(ctx, hostSelectedAbsolutePath)
}

func canonicalProviderSHA256(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, character := range value[7:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
