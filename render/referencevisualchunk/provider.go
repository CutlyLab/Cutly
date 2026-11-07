package referencevisualchunk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/backendstage"
	"github.com/CutlyLab/Cutly/render/chunkplan"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

// Scope is the exact cache-visible request envelope authorized for one immutable Program.
// ImplementationFingerprint is a caller-declared build identity. It is not a signature or proof
// of loaded executable bytes.
type Scope struct {
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	TimelineID                   videoedit.ID           `json:"timeline_id"`
	Target                       videoedit.RenderTarget `json:"target"`
	FrameRange                   videoedit.TickRange    `json:"frame_range"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
	ImplementationFingerprint    string                 `json:"implementation_fingerprint"`
}

// Binding is the immutable integrity record returned by New. Its fingerprint covers every field
// except Fingerprint itself. It does not claim that PlanFingerprint or
// ExecutionManifestFingerprint was derived from the Program; that derivation belongs to a future
// durable Document v2 render protocol.
type Binding struct {
	SchemaVersion                int                    `json:"schema_version"`
	Profile                      string                 `json:"profile"`
	CanonicalJSONProfile         string                 `json:"canonical_json_profile"`
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	TimelineID                   videoedit.ID           `json:"timeline_id"`
	Target                       videoedit.RenderTarget `json:"target"`
	FrameRange                   videoedit.TickRange    `json:"frame_range"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
	ImplementationFingerprint    string                 `json:"implementation_fingerprint"`
	ProgramProfile               string                 `json:"program_profile"`
	DocumentHash                 string                 `json:"document_hash"`
	SourceManifestFingerprint    string                 `json:"source_manifest_fingerprint"`
	ProgramConfigFingerprint     string                 `json:"program_config_fingerprint"`
	ProfileDescriptorFingerprint string                 `json:"profile_descriptor_fingerprint"`
	Fingerprint                  string                 `json:"fingerprint"`
}

type bindingPreimage struct {
	SchemaVersion                int                    `json:"schema_version"`
	Profile                      string                 `json:"profile"`
	CanonicalJSONProfile         string                 `json:"canonical_json_profile"`
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	TimelineID                   videoedit.ID           `json:"timeline_id"`
	Target                       videoedit.RenderTarget `json:"target"`
	FrameRange                   videoedit.TickRange    `json:"frame_range"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
	ImplementationFingerprint    string                 `json:"implementation_fingerprint"`
	ProgramProfile               string                 `json:"program_profile"`
	DocumentHash                 string                 `json:"document_hash"`
	SourceManifestFingerprint    string                 `json:"source_manifest_fingerprint"`
	ProgramConfigFingerprint     string                 `json:"program_config_fingerprint"`
	ProfileDescriptorFingerprint string                 `json:"profile_descriptor_fingerprint"`
}

// Provider owns one immutable Program binding and is safe for concurrent arbitrary-order calls.
type Provider struct {
	program   *referencevisual.Program
	binding   Binding
	resources stagedag.ResourceEstimate
}

var _ backendstage.SurfaceProvider = (*Provider)(nil)

// New freezes one exact host-bound surface-provider scope. The caller must incorporate Binding's
// fingerprint into its own versioned execution identity before treating produced chunks as
// reusable across processes or jobs.
func New(ctx context.Context, program *referencevisual.Program, scope Scope) (provider *Provider, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			provider = nil
			returnErr = fmt.Errorf("%w: constructor panic\n%s", ErrInvariant, debug.Stack())
		}
		if returnErr != nil {
			provider = nil
		}
	}()
	if ctx == nil || program == nil {
		return nil, fmt.Errorf("%w: context and Program are required", ErrConfiguration)
	}
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	config := program.Config()
	manifest := program.SourceManifest()
	if config.Width != scope.Target.Width || config.Height != scope.Target.Height || program.DocumentHash() == "" ||
		manifest.DocumentHash != program.DocumentHash() || (program.Profile() != referencevisual.ProfileV1 && program.Profile() != referencevisual.ProfileV2) {
		return nil, fmt.Errorf("%w: Program identity or geometry does not match scope", ErrConfiguration)
	}
	manifestFingerprint, err := canonicalFingerprint(manifest)
	if err != nil {
		return nil, errors.Join(ErrBinding, err)
	}
	configFingerprint, err := canonicalFingerprint(config)
	if err != nil {
		return nil, errors.Join(ErrBinding, err)
	}
	binding := Binding{
		SchemaVersion: SchemaVersion, Profile: ProfileV1, CanonicalJSONProfile: canonicaljson.Profile,
		PlanFingerprint: scope.PlanFingerprint, ExecutionManifestFingerprint: scope.ExecutionManifestFingerprint,
		TimelineID: scope.TimelineID, Target: scope.Target, FrameRange: scope.FrameRange,
		DeterministicSeed: scope.DeterministicSeed, ImplementationFingerprint: scope.ImplementationFingerprint,
		ProgramProfile: program.Profile(), DocumentHash: program.DocumentHash(),
		SourceManifestFingerprint: manifestFingerprint, ProgramConfigFingerprint: configFingerprint,
		ProfileDescriptorFingerprint: DescriptorHash(),
	}
	fingerprint, err := bindingFingerprint(binding)
	if err != nil {
		return nil, err
	}
	binding.Fingerprint = fingerprint
	if err := validateBindingShape(binding); err != nil {
		return nil, err
	}
	resources, err := videoResources(config, scope.FrameRange)
	if err != nil {
		return nil, err
	}
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	return &Provider{program: program, binding: binding, resources: resources}, nil
}

func (provider *Provider) Binding() Binding {
	if provider == nil {
		return Binding{}
	}
	return provider.binding
}

func (provider *Provider) BindingFingerprint() string {
	if provider == nil {
		return ""
	}
	return provider.binding.Fingerprint
}

// VideoResources returns the conservative task estimate required by this provider. The value can
// be passed directly to backendstage.ReferenceChunkConfig.VideoResources.
func (provider *Provider) VideoResources() stagedag.ResourceEstimate {
	if provider == nil {
		return stagedag.ResourceEstimate{}
	}
	return provider.resources
}

// ValidateVideoResources rejects estimates below the provider's conservative requirement.
func (provider *Provider) ValidateVideoResources(value stagedag.ResourceEstimate) error {
	if provider == nil || provider.program == nil || provider.resources.MemoryBytes == 0 || provider.resources.DiskBytes == 0 {
		return fmt.Errorf("%w: Provider is unavailable", ErrConfiguration)
	}
	if value.MemoryBytes < provider.resources.MemoryBytes || value.DiskBytes < provider.resources.DiskBytes {
		return fmt.Errorf("%w: memory=%d/%d disk=%d/%d", ErrResourceLimit,
			value.MemoryBytes, provider.resources.MemoryBytes, value.DiskBytes, provider.resources.DiskBytes)
	}
	return nil
}

func (provider *Provider) RenderSurface(ctx context.Context, request backendstage.ReferenceChunkRequest, frame int64) (surface *cpucompositor.Surface, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			surface = nil
			returnErr = fmt.Errorf("%w: render panic\n%s", ErrInvariant, debug.Stack())
		}
		if returnErr != nil {
			surface = nil
		}
	}()
	if provider == nil || provider.program == nil || ctx == nil {
		return nil, fmt.Errorf("%w: initialized Provider and context are required", ErrConfiguration)
	}
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	binding := provider.binding
	if request.PlanFingerprint != binding.PlanFingerprint ||
		request.ExecutionManifestFingerprint != binding.ExecutionManifestFingerprint ||
		request.TimelineID != binding.TimelineID || request.Target != binding.Target ||
		request.DeterministicSeed != binding.DeterministicSeed {
		return nil, ErrRequestMismatch
	}
	contains, err := binding.FrameRange.Contains(frame)
	if err != nil || !contains {
		return nil, errors.Join(ErrFrameRange, err)
	}
	result, err := provider.program.RenderSurfaceFrame(ctx, documentv2.ID(binding.TimelineID), binding.Target.FrameClock, frame)
	if err != nil {
		if canceled := contextFailure(ctx); canceled != nil {
			return nil, canceled
		}
		// A dependency can cooperatively cancel even when the parent context has not yet exposed
		// the same terminal state (for example a provider-specific cancellation boundary). Preserve
		// the adapter's stable cancellation category instead of misclassifying it as a render fault.
		if errors.Is(err, referencevisual.ErrCanceled) {
			return nil, errors.Join(ErrCanceled, err)
		}
		return nil, errors.Join(ErrRender, err)
	}
	if result.FrameClock != binding.Target.FrameClock || result.CompositionFrameIndex != frame ||
		result.Result.TimelineID != documentv2.ID(binding.TimelineID) || result.Result.Surface == nil ||
		result.Result.Width != binding.Target.Width || result.Result.Height != binding.Target.Height {
		return nil, ErrInvariant
	}
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	hash, err := result.Result.Surface.CanonicalHashContext(ctx)
	if canceled := contextFailure(ctx); canceled != nil {
		return nil, canceled
	}
	if err != nil || hash != result.Result.PixelHash {
		return nil, errors.Join(ErrInvariant, err)
	}
	return result.Result.Surface, nil
}

func CanonicalBinding(value Binding) ([]byte, error) {
	if err := validateBindingShape(value); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrBinding, err)
	}
	if len(raw) > MaxBindingBytes {
		return nil, ErrResourceLimit
	}
	return raw, nil
}

func DecodeBinding(raw []byte) (Binding, error) {
	if len(raw) == 0 || len(raw) > MaxBindingBytes {
		return Binding{}, ErrResourceLimit
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return Binding{}, errors.Join(ErrBinding, err)
	}
	var value Binding
	if err := wirejson.Decode(raw, &value); err != nil {
		return Binding{}, errors.Join(ErrBinding, err)
	}
	if err := validateBindingShape(value); err != nil {
		return Binding{}, err
	}
	return value, nil
}

func validateScope(value Scope) error {
	if !cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) ||
		!cachekey.CanonicalSHA256(value.ImplementationFingerprint) || !validID(value.TimelineID) ||
		value.Target.TimelineID != value.TimelineID || value.Target.Width <= 0 || value.Target.Height <= 0 ||
		value.Target.FrameClock.Validate() != nil || value.Target.SampleClock.Validate() != nil || value.Target.Channels <= 0 ||
		!utf8.ValidString(value.Target.PixelFormat) || !utf8.ValidString(value.Target.ColorSpace) ||
		strings.TrimSpace(value.Target.PixelFormat) == "" || strings.TrimSpace(value.Target.ColorSpace) == "" ||
		len(value.Target.PixelFormat) > 256 || len(value.Target.ColorSpace) > 256 ||
		value.FrameRange.Validate() != nil || value.FrameRange.First < 0 || value.FrameRange.Count <= 0 {
		return ErrConfiguration
	}
	return nil
}

func validateBindingShape(value Binding) error {
	if value.SchemaVersion != SchemaVersion || value.Profile != ProfileV1 || value.CanonicalJSONProfile != canonicaljson.Profile ||
		!cachekey.CanonicalSHA256(value.PlanFingerprint) || !cachekey.CanonicalSHA256(value.ExecutionManifestFingerprint) ||
		!cachekey.CanonicalSHA256(value.ImplementationFingerprint) || !cachekey.CanonicalSHA256(value.DocumentHash) ||
		!cachekey.CanonicalSHA256(value.SourceManifestFingerprint) || !cachekey.CanonicalSHA256(value.ProgramConfigFingerprint) ||
		value.ProfileDescriptorFingerprint != DescriptorHash() || !cachekey.CanonicalSHA256(value.Fingerprint) ||
		(value.ProgramProfile != referencevisual.ProfileV1 && value.ProgramProfile != referencevisual.ProfileV2) {
		return ErrBinding
	}
	if err := validateScope(Scope{
		PlanFingerprint: value.PlanFingerprint, ExecutionManifestFingerprint: value.ExecutionManifestFingerprint,
		TimelineID: value.TimelineID, Target: value.Target, FrameRange: value.FrameRange,
		DeterministicSeed: value.DeterministicSeed, ImplementationFingerprint: value.ImplementationFingerprint,
	}); err != nil {
		return errors.Join(ErrBinding, err)
	}
	want, err := bindingFingerprint(value)
	if err != nil || want != value.Fingerprint {
		return errors.Join(ErrBinding, err)
	}
	return nil
}

func bindingFingerprint(value Binding) (string, error) {
	preimage := bindingPreimage{
		SchemaVersion: value.SchemaVersion, Profile: value.Profile, CanonicalJSONProfile: value.CanonicalJSONProfile,
		PlanFingerprint: value.PlanFingerprint, ExecutionManifestFingerprint: value.ExecutionManifestFingerprint,
		TimelineID: value.TimelineID, Target: value.Target, FrameRange: value.FrameRange,
		DeterministicSeed: value.DeterministicSeed, ImplementationFingerprint: value.ImplementationFingerprint,
		ProgramProfile: value.ProgramProfile, DocumentHash: value.DocumentHash,
		SourceManifestFingerprint: value.SourceManifestFingerprint, ProgramConfigFingerprint: value.ProgramConfigFingerprint,
		ProfileDescriptorFingerprint: value.ProfileDescriptorFingerprint,
	}
	raw, err := canonicaljson.Marshal(preimage)
	if err != nil {
		return "", errors.Join(ErrBinding, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalFingerprint(value any) (string, error) {
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func videoResources(config referencevisual.Config, frameRange videoedit.TickRange) (stagedag.ResourceEstimate, error) {
	frames := frameRange.Count
	if frames > chunkplan.FrameGroupCount {
		frames = chunkplan.FrameGroupCount
	}
	return resourceEstimate(uint64(config.Width), uint64(config.Height), uint64(frames), config.PipelineLimits.MaxRuntimeWorkingBytes)
}

func resourceEstimate(width, height, frames, runtimeBytes uint64) (stagedag.ResourceEstimate, error) {
	if width == 0 || height == 0 || frames == 0 || frames > uint64(chunkplan.FrameGroupCount) || runtimeBytes == 0 {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	pixels, ok := checkedMul(width, height)
	if !ok {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	unit, ok := checkedMul(pixels, cpucompositor.BytesPerPixel)
	if !ok {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	payload, ok := checkedMul(unit, frames)
	if !ok || payload > math.MaxInt64 {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	doubled, ok := checkedMul(payload, 2)
	if !ok {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	disk, ok := checkedAdd(doubled, cas.PublicationOverheadBytes)
	if !ok {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	memory, ok := checkedAdd(runtimeBytes, providerFixedMemoryBytes)
	if !ok || memory < runtimeBytes || disk == 0 {
		return stagedag.ResourceEstimate{}, ErrResourceLimit
	}
	return stagedag.ResourceEstimate{MemoryBytes: memory, DiskBytes: disk}, nil
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}

func validID(value videoedit.ID) bool {
	text := string(value)
	return len(text) > 0 && len(text) <= 256 && utf8.ValidString(text) && strings.TrimSpace(text) != ""
}

func contextFailure(ctx context.Context) error {
	if ctx == nil {
		return ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCanceled, err)
	}
	return nil
}
