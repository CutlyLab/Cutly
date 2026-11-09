// Package preparev2 separates host authorization from Document v2 wire identity.
// It does not launch a provider, retain a live Program, or publish media.
package preparev2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/internal/buildidentity"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

var ErrAuthorization = errors.New("preparev2: host authorization failed")

// ResourceVerifier is installed by trusted host code, never selected by a wire
// request. It must confine the locator to host-owned roots, reject symlinks, and
// hash and inspect/probe the same retained or privately snapshotted bytes. It
// returns observed identities, not a copy of unverified claims. URI-only assets
// must already have been materialized under that host policy. The document hash
// lets a host bind a materialization authorization to the original snapshot.
//
// Success here is not a retained resource handle. Reconstruction and execution
// must reverify content from their own retained handles before using it. The
// callback must be concurrency-safe, honor cancellation, and remain immutable
// for the lifetime of the Authority.
type ResourceVerifier func(context.Context, string, renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error)

// Config is a trusted host construction API, not a JSON request. No daemon or
// Agent route may populate it from document, manifest, request, or environment
// fields. Local development requires explicit opt-in; packaged provider identity
// comes from release-owned build policy and a measured host-selected executable.
type Config struct {
	ProviderExecutable    string
	AllowLocalDevelopment bool
	CompilerBuild         string
	EvaluatorBuild        string
	Backend               renderapi.BackendCapabilitiesV2
	MediaRuntime          renderapi.MediaRuntime
	VerifyResource        ResourceVerifier
}

type hostPins struct {
	CompilerBuild  string                          `json:"compiler_build"`
	EvaluatorBuild string                          `json:"evaluator_build"`
	Backend        renderapi.BackendCapabilitiesV2 `json:"backend"`
	MediaRuntime   renderapi.MediaRuntime          `json:"media_runtime"`
}

// Authority holds owned host choices. Provider measurement is an authorization
// pin, not protection against replacing a pathname before a later process launch.
// The worker launcher must bind the actual executable bytes again at execution.
type Authority struct {
	provider       string
	pins           string
	verifyResource ResourceVerifier
}

func New(ctx context.Context, config Config) (result *Authority, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = denied("host construction panic: %v", r)
		}
	}()
	policy, err := buildidentity.ResolveProviderPolicy()
	if err != nil {
		return nil, denied("provider policy: %v", err)
	}
	var identity buildidentity.ProviderExecutableIdentity
	if policy.Distribution() == "development" {
		if !config.AllowLocalDevelopment {
			return nil, denied("local provider measurement requires explicit host opt-in")
		}
		identity, err = policy.MeasureLocalDevelopmentExecutable(ctx, config.ProviderExecutable)
	} else {
		if config.AllowLocalDevelopment {
			return nil, denied("local development opt-in is forbidden for packaged policy")
		}
		identity, err = policy.MeasurePackagedExecutable(ctx, config.ProviderExecutable)
	}
	if err != nil {
		return nil, denied("provider measurement: %v", err)
	}
	if !identity.Valid() {
		return nil, denied("invalid provider identity")
	}
	return newWithProviderIdentity(ctx, config, identity)
}

// NewWithProviderBinding constructs authority inside an already-launched
// provider process. The binding proves only which executable bytes are running;
// the parent remains responsible for packaged-policy authorization before exec.
// Keeping this constructor typed around an opaque measured identity prevents a
// request or manifest digest string from becoming executable authority.
func NewWithProviderBinding(ctx context.Context, config Config, identity buildidentity.ProviderExecutableIdentity) (*Authority, error) {
	return newWithProviderIdentity(ctx, config, identity)
}

func newWithProviderIdentity(ctx context.Context, config Config, identity buildidentity.ProviderExecutableIdentity) (result *Authority, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = denied("host construction panic: %v", r)
		}
	}()
	if err = checkContext(ctx); err != nil {
		return nil, err
	}
	if !identity.Valid() {
		return nil, denied("invalid provider identity")
	}
	if !digest(config.CompilerBuild) || !digest(config.EvaluatorBuild) {
		return nil, denied("compiler and evaluator builds must be canonical digests")
	}
	pins := hostPins{config.CompilerBuild, config.EvaluatorBuild, config.Backend, config.MediaRuntime}
	if err = admitPins(pins); err != nil {
		return nil, err
	}
	raw, err := canonical(pins)
	if err != nil {
		return nil, err
	}
	if err = checkContext(ctx); err != nil {
		return nil, err
	}
	return &Authority{provider: identity.Digest(), pins: string(raw), verifyResource: config.VerifyResource}, nil
}

// Freeze verifies the original Document and derives execution identity using
// host-owned build/backend/media choices. It never trusts draft build pins.
func (a *Authority) Freeze(ctx context.Context, document documentv2.Document, plan renderplanv2.Plan, draft renderapi.ExecutionManifestV2) (result renderapi.ExecutionManifestV2, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = renderapi.ExecutionManifestV2{}
			err = denied("freeze panic: %v", r)
		}
	}()
	if err = a.ready(ctx); err != nil {
		return result, err
	}
	if err = renderplanv2.VerifyAgainstDocument(ctx, plan, document); err != nil {
		return result, err
	}
	var pins hostPins
	if err = json.Unmarshal([]byte(a.pins), &pins); err != nil {
		return result, denied("host pins: %v", err)
	}
	draft.CompilerBuild, draft.EvaluatorBuild, draft.ProviderBuild = pins.CompilerBuild, pins.EvaluatorBuild, a.provider
	draft.Backend, draft.MediaRuntime = pins.Backend, pins.MediaRuntime
	frozen, err := renderapi.FreezeExecutionManifestV2(ctx, plan, draft)
	if err != nil {
		return result, err
	}
	if err = a.verifyResources(ctx, frozen); err != nil {
		return result, err
	}
	if err = checkContext(ctx); err != nil {
		return result, err
	}
	return frozen, nil
}

// AuthorizedRequest owns a verified request after comparison against this host's
// independent authority. It grants no filesystem handle or publication right.
// Its validity is a point-in-time check; resource/executable bytes must be rebound
// at execution and after restart. The zero value is invalid.
type AuthorizedRequest struct {
	verified renderapi.VerifiedRenderRequestV2
}

func (r AuthorizedRequest) Valid() bool                                 { return r.verified.Valid() }
func (r AuthorizedRequest) Request() (renderapi.RenderRequestV2, error) { return r.verified.Request() }
func (r AuthorizedRequest) Canonical() ([]byte, error)                  { return r.verified.Canonical() }

func (a *Authority) Authorize(ctx context.Context, document documentv2.Document, request renderapi.RenderRequestV2) (result AuthorizedRequest, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = AuthorizedRequest{}
			err = denied("authorization panic: %v", r)
		}
	}()
	if err = a.ready(ctx); err != nil {
		return result, err
	}
	verified, err := renderapi.VerifyRenderRequestV2(request)
	if err != nil {
		return result, err
	}
	owned, err := verified.Request()
	if err != nil {
		return result, err
	}
	if err = renderplanv2.VerifyAgainstDocument(ctx, owned.Plan, document); err != nil {
		return result, err
	}
	if err = a.comparePins(owned.Manifest); err != nil {
		return result, err
	}
	if err = a.verifyResources(ctx, owned.Manifest); err != nil {
		return result, err
	}
	if err = checkContext(ctx); err != nil {
		return result, err
	}
	return AuthorizedRequest{verified: verified}, nil
}

func (a *Authority) comparePins(manifest renderapi.ExecutionManifestV2) error {
	if manifest.ProviderBuild != a.provider {
		return denied("provider executable identity mismatch")
	}
	pins, err := canonical(hostPins{manifest.CompilerBuild, manifest.EvaluatorBuild, manifest.Backend, manifest.MediaRuntime})
	if err != nil || !bytes.Equal(pins, []byte(a.pins)) {
		return denied("host build/backend/media identity mismatch")
	}
	return nil
}

func (a *Authority) verifyResources(ctx context.Context, manifest renderapi.ExecutionManifestV2) error {
	if len(manifest.Resources) != 0 && a.verifyResource == nil {
		return denied("no host resource verifier")
	}
	for _, resource := range manifest.Resources {
		if err := checkContext(ctx); err != nil {
			return err
		}
		expected, err := canonical(resource)
		if err != nil {
			return err
		}
		// Deep-copy pointer members before calling even trusted host callbacks.
		var claim renderapi.MaterializedResourceV2
		if err := json.Unmarshal(expected, &claim); err != nil {
			return err
		}
		observed, err := a.verifyResource(ctx, manifest.DocumentHash, claim)
		if err != nil {
			return denied("resource %q: %v", resource.AssetID, err)
		}
		// Bound callback data before marshaling; a callback cannot bypass admission.
		if err := admitResource(observed); err != nil {
			return err
		}
		actual, err := canonical(observed)
		if err != nil || !bytes.Equal(actual, expected) {
			return denied("resource %q identity mismatch", resource.AssetID)
		}
	}
	return checkContext(ctx)
}

func (a *Authority) ready(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if a == nil || !digest(a.provider) || a.pins == "" {
		return denied("invalid host authority")
	}
	return nil
}
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return denied("nil context")
	}
	return ctx.Err()
}
func denied(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrAuthorization, fmt.Sprintf(format, args...))
}
func digest(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, denied("encode: %v", err)
	}
	return canonicaljson.Canonicalize(raw)
}
func admitPins(p hostPins) error {
	if len(p.Backend.TrackKinds) > 16384 || len(p.Backend.AssetKinds) > 16384 || len(p.Backend.Features) > 16384 {
		return denied("host capability limit")
	}
	for _, s := range []string{p.Backend.BackendID, p.Backend.BuildFingerprint, p.MediaRuntime.FFmpegBuild, p.MediaRuntime.DecoderProfile, p.MediaRuntime.EncoderProfile, p.MediaRuntime.TextShaperBuild, p.MediaRuntime.ColorProfile} {
		if len(s) > 4096 {
			return denied("host identity length")
		}
	}
	for _, s := range p.Backend.TrackKinds {
		if len(s) > 128 {
			return denied("track kind length")
		}
	}
	for _, s := range p.Backend.AssetKinds {
		if len(s) > 128 {
			return denied("asset kind length")
		}
	}
	for _, s := range p.Backend.Features {
		if len(s) > 256 {
			return denied("feature length")
		}
	}
	return nil
}
func admitResource(r renderapi.MaterializedResourceV2) error {
	for _, s := range []string{string(r.AssetID), string(r.Kind), r.ContentHash, string(r.LocatorScheme), r.Locator, r.MaterializationProfile} {
		if len(s) > 1<<20 {
			return denied("observed resource length")
		}
	}
	for _, s := range []*string{r.InspectionFingerprint, r.ProbeFingerprint} {
		if s != nil && len(*s) > 71 {
			return denied("observed receipt length")
		}
	}
	return nil
}
