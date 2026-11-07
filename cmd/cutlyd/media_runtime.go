package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/CutlyLab/Cutly/internal/buildidentity"
	"github.com/CutlyLab/Cutly/media/runtimebundle"
)

var (
	ErrMediaRuntimePolicy   = errors.New("cutlyd: media runtime policy rejected")
	ErrMediaRuntimePin      = errors.New("cutlyd: media runtime bundle pin mismatch")
	ErrMediaRuntimeReplaced = errors.New("cutlyd: media runtime bundle changed during application construction")
)

type mediaRuntimeBundleResolver interface {
	Resolve(context.Context, string) (runtimebundle.Resolution, error)
}

type resolvedMediaRuntime struct {
	ffmpegExecutable                    string
	ffprobeExecutable                   string
	ffmpegRuntimeDependencyFingerprint  string
	ffprobeRuntimeDependencyFingerprint string

	bundleRoot                string
	expectedBundleFingerprint string
	resolver                  mediaRuntimeBundleResolver
	resolution                runtimebundle.Resolution
}

func configureParsedMediaRuntime(render *renderConfig) error {
	if render == nil || !render.enabled {
		return nil
	}
	policy, err := buildidentity.ResolveMediaRuntimePolicy()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMediaRuntimePolicy, err)
	}
	if render.mediaRuntimeBundleRoot == "" {
		if policy.Distribution != buildidentity.MediaRuntimeDistributionDevelopment {
			return fmt.Errorf("%w: packaged builds require a signed media runtime bundle", ErrMediaRuntimePolicy)
		}
		return nil
	}
	if policy.Distribution != buildidentity.MediaRuntimeDistributionPackaged {
		return fmt.Errorf(
			"%w: signed bundle use requires release-stamped keys and an exact bundle fingerprint",
			ErrMediaRuntimePolicy,
		)
	}
	resolver, err := runtimebundle.New(runtimebundle.Options{TrustedKeys: policy.TrustedKeys})
	if err != nil {
		return fmt.Errorf("%w: construct release resolver: %v", ErrMediaRuntimePolicy, err)
	}
	render.mediaRuntimeResolver = resolver
	render.expectedMediaRuntimeBundleFingerprint = policy.ExpectedBundleFingerprint
	return nil
}

func resolveMediaRuntime(ctx context.Context, render renderConfig) (resolvedMediaRuntime, error) {
	if ctx == nil {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: context is required", ErrMediaRuntimePolicy)
	}
	if err := ctx.Err(); err != nil {
		return resolvedMediaRuntime{}, err
	}
	if render.mediaRuntimeBundleRoot == "" {
		return resolveDevelopmentMediaRuntime(render)
	}
	if render.unsafeAllowUnverifiedMediaRuntime || render.legacyMediaRuntimeConfigured ||
		render.ffmpegExecutable != "" || render.ffprobeExecutable != "" ||
		render.ffmpegRuntimeDependencyFingerprint != "" || render.ffprobeRuntimeDependencyFingerprint != "" {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: signed and direct media runtime configuration are mutually exclusive", ErrMediaRuntimePolicy)
	}
	if err := validateCleanAbsolutePath(render.mediaRuntimeBundleRoot); err != nil {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: bundle root: %v", ErrMediaRuntimePolicy, err)
	}

	resolver := render.mediaRuntimeResolver
	expected := render.expectedMediaRuntimeBundleFingerprint
	if resolver == nil || expected == "" {
		policy, err := buildidentity.ResolveMediaRuntimePolicy()
		if err != nil || policy.Distribution != buildidentity.MediaRuntimeDistributionPackaged {
			return resolvedMediaRuntime{}, fmt.Errorf("%w: packaged signed-bundle trust is unavailable: %v", ErrMediaRuntimePolicy, err)
		}
		if resolver == nil {
			resolver, err = runtimebundle.New(runtimebundle.Options{TrustedKeys: policy.TrustedKeys})
			if err != nil {
				return resolvedMediaRuntime{}, fmt.Errorf("%w: construct release resolver: %v", ErrMediaRuntimePolicy, err)
			}
		}
		if expected == "" {
			expected = policy.ExpectedBundleFingerprint
		}
	}
	if !canonicalSHA256(expected) {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: expected bundle fingerprint is not canonical", ErrMediaRuntimePolicy)
	}
	resolution, err := resolver.Resolve(ctx, render.mediaRuntimeBundleRoot)
	if err != nil {
		return resolvedMediaRuntime{}, fmt.Errorf("resolve signed media runtime: %w", err)
	}
	if resolution.BundleFingerprint != expected {
		return resolvedMediaRuntime{}, fmt.Errorf(
			"%w: resolved %s, release expects %s",
			ErrMediaRuntimePin,
			resolution.BundleFingerprint,
			expected,
		)
	}
	return resolvedMediaRuntime{
		ffmpegExecutable:                    resolution.FFmpeg.Path,
		ffprobeExecutable:                   resolution.FFprobe.Path,
		ffmpegRuntimeDependencyFingerprint:  resolution.FFmpeg.ClosureFingerprint,
		ffprobeRuntimeDependencyFingerprint: resolution.FFprobe.ClosureFingerprint,
		bundleRoot:                          render.mediaRuntimeBundleRoot,
		expectedBundleFingerprint:           expected,
		resolver:                            resolver,
		resolution:                          resolution,
	}, nil
}

func resolveDevelopmentMediaRuntime(render renderConfig) (resolvedMediaRuntime, error) {
	policy, err := buildidentity.ResolveMediaRuntimePolicy()
	if err != nil {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: %v", ErrMediaRuntimePolicy, err)
	}
	if policy.Distribution != buildidentity.MediaRuntimeDistributionDevelopment ||
		!render.unsafeAllowUnverifiedMediaRuntime {
		return resolvedMediaRuntime{}, fmt.Errorf(
			"%w: direct executable paths are allowed only behind the explicit development gate",
			ErrMediaRuntimePolicy,
		)
	}
	if render.mediaRuntimeResolver != nil || render.expectedMediaRuntimeBundleFingerprint != "" ||
		render.ffmpegExecutable == "" || render.ffprobeExecutable == "" ||
		!canonicalSHA256(render.ffmpegRuntimeDependencyFingerprint) ||
		!canonicalSHA256(render.ffprobeRuntimeDependencyFingerprint) {
		return resolvedMediaRuntime{}, fmt.Errorf("%w: incomplete direct development runtime", ErrMediaRuntimePolicy)
	}
	return resolvedMediaRuntime{
		ffmpegExecutable:                    render.ffmpegExecutable,
		ffprobeExecutable:                   render.ffprobeExecutable,
		ffmpegRuntimeDependencyFingerprint:  render.ffmpegRuntimeDependencyFingerprint,
		ffprobeRuntimeDependencyFingerprint: render.ffprobeRuntimeDependencyFingerprint,
	}, nil
}

// Revalidate catches bundle mutation during constructor work. It narrows but
// cannot eliminate the portable path-execution TOCTOU window; packaged hosts
// must still mount or code-sign the bundle as an immutable deployment asset.
func (runtime resolvedMediaRuntime) Revalidate(ctx context.Context) error {
	if runtime.resolver == nil {
		return nil
	}
	current, err := runtime.resolver.Resolve(ctx, runtime.bundleRoot)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMediaRuntimeReplaced, err)
	}
	if current.BundleFingerprint != runtime.expectedBundleFingerprint ||
		!reflect.DeepEqual(current, runtime.resolution) {
		return ErrMediaRuntimeReplaced
	}
	return nil
}

func validateMediaRuntimeRootSeparation(bundleRoot, projectRoot, stateRoot, outputRoot string, inputRoots []string) error {
	if bundleRoot == "" {
		return nil
	}
	physicalBundle, err := filepath.EvalSymlinks(bundleRoot)
	if err != nil {
		return fmt.Errorf("cutlyd: resolve media runtime bundle root: %w", err)
	}
	if filepath.Clean(physicalBundle) != bundleRoot {
		return errors.New("cutlyd: media runtime bundle root must be a canonical physical path")
	}
	others := make([]struct {
		name string
		path string
	}, 0, len(inputRoots)+3)
	others = append(others,
		struct{ name, path string }{"project", projectRoot},
		struct{ name, path string }{"render state", stateRoot},
		struct{ name, path string }{"render output", outputRoot},
	)
	for index, root := range inputRoots {
		others = append(others, struct{ name, path string }{fmt.Sprintf("render input %d", index), root})
	}
	for _, other := range others {
		resolved, resolveErr := filepath.EvalSymlinks(other.path)
		if resolveErr != nil {
			return fmt.Errorf("cutlyd: resolve %s root: %w", other.name, resolveErr)
		}
		overlap, compareErr := directoriesOverlap(physicalBundle, filepath.Clean(resolved))
		if compareErr != nil {
			return fmt.Errorf("cutlyd: compare media runtime and %s roots: %w", other.name, compareErr)
		}
		if overlap {
			return fmt.Errorf("cutlyd: media runtime bundle root must be disjoint from %s root", other.name)
		}
	}
	return nil
}
