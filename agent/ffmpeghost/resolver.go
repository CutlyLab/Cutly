package ffmpeghost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/renderexec"
	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

var ErrConfiguration = errors.New("ffmpeghost: invalid configuration")

// Backend is the host-owned FFmpeg backend surface required during Agent
// resolution. The concrete ffmpegbackend.Backend implements this interface.
type Backend interface {
	renderapi.RenderBackend
	renderapi.ExactFrameBackend
	ValidateInputLocator(string) error
	FFmpegRuntimeFingerprint(context.Context) (string, error)
}

// FreshProber observes the current immutable bytes selected by a file locator.
// Implementations are expected to be host-created ffprobe adapters, never
// values selected by an Agent payload.
type FreshProber interface {
	Probe(context.Context, cutlyffprobe.Request) (probe.ProbeManifest, []probe.Diagnostic, error)
}

// ProbeRegistry durably registers immutable probe evidence for later queued
// execution. It matches proberegistry.Store without importing its concrete
// storage implementation.
type ProbeRegistry interface {
	Put(context.Context, probe.ProbeManifest) (probe.ProbeManifest, string, bool, error)
}

type Config struct {
	ProjectID              string
	Backend                Backend
	Prober                 FreshProber
	Registry               ProbeRegistry
	ExpectedCompilerBuild  string
	ExpectedEvaluatorBuild string
}

// Resolver is immutable after construction and safe for concurrent calls when
// its backend, prober, and registry implementations are safe for concurrent use.
type Resolver struct {
	projectID              string
	backend                Backend
	prober                 FreshProber
	registry               ProbeRegistry
	expectedCompilerBuild  string
	expectedEvaluatorBuild string
}

var _ renderexec.Resolver = (*Resolver)(nil)

func New(config Config) (*Resolver, error) {
	if !validProjectID(config.ProjectID) {
		return nil, fmt.Errorf("%w: a valid fixed project ID is required", ErrConfiguration)
	}
	if nilLike(config.Backend) || nilLike(config.Prober) || nilLike(config.Registry) {
		return nil, fmt.Errorf("%w: backend, fresh prober, and registry are required", ErrConfiguration)
	}
	if !validSHA256(config.ExpectedCompilerBuild) || !validSHA256(config.ExpectedEvaluatorBuild) {
		return nil, fmt.Errorf("%w: expected compiler and evaluator builds must be canonical sha256 digests", ErrConfiguration)
	}
	return &Resolver{
		projectID: config.ProjectID, backend: config.Backend, prober: config.Prober, registry: config.Registry,
		expectedCompilerBuild: config.ExpectedCompilerBuild, expectedEvaluatorBuild: config.ExpectedEvaluatorBuild,
	}, nil
}

// ResolveBackend proves that a frozen execution manifest selects this exact
// host runtime and that every named source still has the probe evidence frozen
// into the manifest. Failure messages intentionally omit callback errors and
// file paths because Agent diagnostics cross a trust boundary.
func (resolver *Resolver) ResolveBackend(
	ctx context.Context,
	projectID string,
	manifest renderapi.ExecutionManifest,
) (backend renderapi.RenderBackend, returnErr error) {
	defer func() {
		if recover() != nil {
			backend = nil
			returnErr = unavailable("host resolver callback panicked")
		}
	}()
	if resolver == nil || nilLike(resolver.backend) || nilLike(resolver.prober) || nilLike(resolver.registry) {
		return nil, unavailable("host resolver is unavailable")
	}
	if ctx == nil {
		return nil, unavailable("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if projectID != resolver.projectID {
		return nil, unavailable("project scope mismatch")
	}

	frozen, err := cloneExecutionManifest(manifest)
	if err != nil {
		return nil, unavailable("execution manifest is invalid")
	}
	if frozen.CompilerBuild != resolver.expectedCompilerBuild || frozen.EvaluatorBuild != resolver.expectedEvaluatorBuild {
		return nil, unavailable("compiler or evaluator build mismatch")
	}

	capabilities, err := resolver.backend.Capabilities(ctx)
	if err := callbackError(ctx, err); err != nil {
		return nil, err
	}
	capabilities = videoedit.CanonicalBackendCapabilities(capabilities)
	wanted := videoedit.CanonicalBackendCapabilities(frozen.Backend)
	if resolver.backend.ID() != wanted.BackendID || !reflect.DeepEqual(capabilities, wanted) {
		return nil, unavailable("backend identity or capabilities mismatch")
	}
	runtimeFingerprint, err := resolver.backend.FFmpegRuntimeFingerprint(ctx)
	if err := callbackError(ctx, err); err != nil {
		return nil, err
	}
	if runtimeFingerprint != frozen.MediaRuntime.FFmpegBuild {
		return nil, unavailable("FFmpeg runtime identity mismatch")
	}

	for _, resource := range frozen.Resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resource.LocatorScheme != renderapi.LocatorFile {
			return nil, unavailable("resource locator scheme is unsupported")
		}
		if err := resolver.backend.ValidateInputLocator(resource.Locator); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, unavailable("resource locator is not authorized")
		}

		observed, diagnostics, err := resolver.prober.Probe(ctx, cutlyffprobe.Request{
			AssetID: resource.AssetID, Path: resource.Locator, ExpectedContentHash: resource.ContentHash,
		})
		if err := callbackError(ctx, err); err != nil {
			return nil, err
		}
		if probe.HasErrorDiagnostics(diagnostics) {
			return nil, unavailable("fresh probe rejected the resource")
		}
		observed, err = cloneProbeManifest(observed)
		if err != nil {
			return nil, unavailable("fresh probe returned an invalid manifest")
		}
		if observed.Resource.AssetID != resource.AssetID ||
			observed.Resource.ContentHash != resource.ContentHash ||
			observed.Resource.SizeBytes != resource.SizeBytes {
			return nil, unavailable("fresh probe resource identity mismatch")
		}
		fingerprint, err := probe.Fingerprint(observed)
		if err != nil || fingerprint != resource.ProbeFingerprint {
			return nil, unavailable("fresh probe fingerprint mismatch")
		}

		registryInput, err := cloneProbeManifest(observed)
		if err != nil {
			return nil, unavailable("fresh probe manifest cannot be registered")
		}
		stored, storedFingerprint, _, err := resolver.registry.Put(ctx, registryInput)
		if err := callbackError(ctx, err); err != nil {
			return nil, err
		}
		stored, err = cloneProbeManifest(stored)
		if err != nil || storedFingerprint != fingerprint || !equalProbeManifests(observed, stored) {
			return nil, unavailable("probe registry verification failed")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resolver.backend, nil
}

func cloneExecutionManifest(manifest renderapi.ExecutionManifest) (renderapi.ExecutionManifest, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	return renderapi.DecodeExecutionManifest(raw)
}

func cloneProbeManifest(manifest probe.ProbeManifest) (probe.ProbeManifest, error) {
	raw, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		return probe.ProbeManifest{}, err
	}
	cloned, diagnostics, err := probe.Decode(raw)
	if err != nil || probe.HasErrorDiagnostics(diagnostics) {
		if err == nil {
			err = errors.New("probe manifest has error diagnostics")
		}
		return probe.ProbeManifest{}, err
	}
	return cloned, nil
}

func equalProbeManifests(left, right probe.ProbeManifest) bool {
	leftRaw, leftErr := videoedit.CanonicalJSON(left)
	rightRaw, rightErr := videoedit.CanonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func callbackError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return unavailable("host runtime callback failed")
	}
	return nil
}

func unavailable(reason string) error {
	return fmt.Errorf("%w: %s", renderexec.ErrBackendUnavailable, reason)
}

func validProjectID(value string) bool {
	return value != "" && len(value) <= agentprotocol.MaxIdentityBytes && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= agentprotocol.MaxIdentityScalars && strings.Trim(value, " \t\r\n") != ""
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	hexValue := strings.TrimPrefix(value, "sha256:")
	if hexValue != strings.ToLower(hexValue) {
		return false
	}
	_, err := hex.DecodeString(hexValue)
	return err == nil
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
