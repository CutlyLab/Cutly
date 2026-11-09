// Package chunkcachev2 provides a job-neutral, durable receipt index over a host-owned CAS.
// It never manufactures or replays job checkpoints. Receipts are host-validated producer
// claims, not executable attestation or authorization to read original media.
package chunkcachev2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	IdentityProfile = "cutly.cache.chunk-identity.document-v2.v2"
	ReceiptProfile  = "cutly.cache.chunk-receipt.document-v2.v2"
	IndexProfile    = "cutly.cache.chunk-index.document-v2.v2"
	MaxRecordBytes  = 32 << 10
)

var (
	ErrInvalid             = errors.New("chunkcachev2: invalid request")
	ErrMiss                = errors.New("chunkcachev2: cache miss")
	ErrCorrupt             = errors.New("chunkcachev2: corrupt entry")
	ErrCollision           = errors.New("chunkcachev2: key collision")
	ErrLimit               = errors.New("chunkcachev2: admission limit")
	ErrUnsupported         = errors.New("chunkcachev2: unsupported storage platform")
	ErrDurabilityUncertain = errors.New("chunkcachev2: durability uncertain; retry identical publication")
	invocationPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)
)

// Identity excludes only execution lifecycle and publication location. Document, plan,
// manifest, provider, runtime, layout and exact decoded composition range remain bound.
// It is a content identity, not authority. Construct it from a verified environment at
// the host boundary with NewIdentity; do not accept an untrusted identity as admission.
type Identity struct {
	SchemaVersion                uint32                 `json:"schema_version"`
	Profile                      string                 `json:"profile"`
	DocumentHash                 string                 `json:"document_hash"`
	PlanFingerprint              string                 `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                 `json:"execution_manifest_fingerprint"`
	ProviderExecutableDigest     string                 `json:"provider_executable_digest"`
	MediaRuntime                 api.MediaRuntime       `json:"media_runtime"`
	Output                       api.OutputSpecV2       `json:"output"`
	Target                       videoedit.RenderTarget `json:"target"`
	DeterministicSeed            int64                  `json:"deterministic_seed"`
	Domain                       string                 `json:"domain"`
	Range                        videoedit.TickRange    `json:"range"`
	UnitBytes                    int64                  `json:"unit_bytes"`
}

type Receipt struct {
	SchemaVersion      uint32   `json:"schema_version"`
	Profile            string   `json:"profile"`
	Identity           Identity `json:"identity"`
	ContentHash        string   `json:"content_hash"`
	SizeBytes          int64    `json:"size_bytes"`
	WorkerInvocationID string   `json:"worker_invocation_id"`
	Attempt            uint8    `json:"attempt"`
	ExecutableDigest   string   `json:"executable_digest"`
	Fingerprint        string   `json:"fingerprint"`
}

type record struct {
	SchemaVersion uint32       `json:"schema_version"`
	Profile       string       `json:"profile"`
	Key           cachekey.Key `json:"key"`
	Receipt       Receipt      `json:"receipt"`
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	return ctx.Err()
}
func recoverError(err *error) {
	if recover() != nil {
		*err = fmt.Errorf("%w: panic", ErrInvalid)
	}
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return "sha256:" + hex.EncodeToString(h[:]) }

// NewIdentity admits only a protocol partition within the verified native plan.
func NewIdentity(ctx context.Context, environment prepared.VerifiedEnvironmentV2, domain string, r videoedit.TickRange) (out Identity, err error) {
	defer func() {
		if recover() != nil {
			out = Identity{}
			err = ErrInvalid
		}
		if err != nil {
			out = Identity{}
		}
	}()
	if err = checkContext(ctx); err != nil {
		return out, err
	}
	if !environment.Valid() {
		return out, ErrInvalid
	}
	e, err := environment.Environment(ctx)
	if err != nil {
		return out, err
	}
	whole, partition := e.Plan.FrameRange, int64(64)
	if domain == api.AudioBlockProfileV2 {
		whole = e.Plan.SampleRange
		partition = 65536
	} else if domain != api.VideoSurfaceProfileV2 {
		return out, ErrInvalid
	}
	end, ee := r.EndExclusive()
	limit, le := whole.EndExclusive()
	if ee != nil || le != nil || r.Count <= 0 || r.First < whole.First || end > limit || (r.First-whole.First)%partition != 0 || r.Count != min(partition, limit-r.First) {
		return out, ErrInvalid
	}
	m := e.Manifest
	out = Identity{SchemaVersion: 2, Profile: IdentityProfile, DocumentHash: e.Plan.DocumentHash, PlanFingerprint: e.Plan.Fingerprint, ExecutionManifestFingerprint: m.Fingerprint, ProviderExecutableDigest: m.ProviderBuild, MediaRuntime: m.MediaRuntime, Output: m.Output, Target: e.Plan.Target, DeterministicSeed: m.DeterministicSeed, Domain: domain, Range: r}
	out.UnitBytes, err = unitBytes(out)
	if err == nil {
		err = validateIdentity(out)
	}
	if err == nil {
		err = ctx.Err()
	}
	return out, err
}

func unitBytes(v Identity) (int64, error) {
	if v.Domain == api.AudioBlockProfileV2 {
		if v.Target.Channels <= 0 || int64(v.Target.Channels) > math.MaxInt64/2 {
			return 0, ErrInvalid
		}
		return int64(v.Target.Channels) * 2, nil
	}
	if v.Domain != api.VideoSurfaceProfileV2 || v.Target.Width <= 0 || v.Target.Height <= 0 {
		return 0, ErrInvalid
	}
	w, h := int64(v.Target.Width), int64(v.Target.Height)
	if w > math.MaxInt64/h || w*h > math.MaxInt64/8 {
		return 0, ErrInvalid
	}
	return w * h * 8, nil
}
func validateIdentity(v Identity) error {
	// Bound every variable-size field before marshaling or scanning it.
	for _, s := range []string{v.Profile, v.DocumentHash, v.PlanFingerprint, v.ExecutionManifestFingerprint, v.ProviderExecutableDigest, v.Domain, string(v.Target.TimelineID), v.Target.PixelFormat, v.Target.ColorSpace, v.MediaRuntime.FFmpegBuild, v.MediaRuntime.TextShaperBuild, v.MediaRuntime.DecoderProfile, v.MediaRuntime.EncoderProfile, v.MediaRuntime.ColorProfile, v.Output.ReferenceProfile, v.Output.Format, v.Output.PixelFormat, v.Output.ColorSpace, v.Output.VideoCodec, v.Output.AudioCodec, v.Output.Intermediate.VideoSurfaceProfile, v.Output.Intermediate.AudioBlockProfile, v.Output.DecodedVideoProfile, v.Output.DecodedAudioProfile} {
		if len(s) > 1024 {
			return ErrLimit
		}
		if !utf8.ValidString(s) {
			return ErrInvalid
		}
	}
	if v.SchemaVersion != 2 || v.Profile != IdentityProfile || v.DeterministicSeed != 0 || v.Range.Validate() != nil || v.Range.Count <= 0 {
		return ErrInvalid
	}
	for _, s := range []string{v.DocumentHash, v.PlanFingerprint, v.ExecutionManifestFingerprint, v.ProviderExecutableDigest, v.MediaRuntime.FFmpegBuild} {
		if !cachekey.CanonicalSHA256(s) {
			return ErrInvalid
		}
	}
	if v.MediaRuntime.TextShaperBuild != "" && !cachekey.CanonicalSHA256(v.MediaRuntime.TextShaperBuild) {
		return ErrInvalid
	}
	if v.MediaRuntime.DecoderProfile == "" || v.MediaRuntime.EncoderProfile == "" || v.MediaRuntime.ColorProfile == "" {
		return ErrInvalid
	}
	t, o := v.Target, v.Output
	if t.TimelineID == "" || t.Width <= 0 || t.Height <= 0 || t.Channels <= 0 || int64(t.Width) > math.MaxInt32 || int64(t.Height) > math.MaxInt32 || int64(t.Channels) > math.MaxInt32 || t.FrameClock.Validate() != nil || t.SampleClock.Validate() != nil {
		return ErrInvalid
	}
	if int64(o.Width) != int64(t.Width) || int64(o.Height) != int64(t.Height) || int64(o.Channels) != int64(t.Channels) || o.FrameClock != t.FrameClock || o.SampleClock != t.SampleClock || o.SampleRate != t.SampleClock.SamplesPerSecond || o.PixelFormat != t.PixelFormat || o.ColorSpace != t.ColorSpace {
		return ErrInvalid
	}
	if o.ReferenceProfile != api.OutputReferenceProfileV2 || o.Intermediate.VideoSurfaceProfile != api.VideoSurfaceProfileV2 || o.Intermediate.AudioBlockProfile != api.AudioBlockProfileV2 || o.DecodedVideoProfile != api.DecodedVideoProfileRGBA8V1 || o.DecodedAudioProfile != api.DecodedAudioProfilePCM16LEV1 || o.Format == "" || o.VideoCodec == "" || o.AudioCodec == "" {
		return ErrInvalid
	}
	u, err := unitBytes(v)
	if err != nil || v.UnitBytes != u || v.Range.Count > math.MaxInt64/u {
		return ErrInvalid
	}
	maxCount := int64(64)
	if v.Domain == api.AudioBlockProfileV2 {
		maxCount = 65536
	}
	if v.Range.Count > maxCount {
		return ErrLimit
	}
	return nil
}

// Input returns the complete key preimage, including dispatch-stage binding for a DAG.
func Input(v Identity) (cachekey.Input, error) {
	if err := validateIdentity(v); err != nil {
		return cachekey.Input{}, err
	}
	raw, err := boundedMarshal(v)
	if err != nil {
		return cachekey.Input{}, err
	}
	stage := api.StageCompose
	kind := "video"
	if v.Domain == api.AudioBlockProfileV2 {
		stage = api.StageAudio
		kind = "audio"
	}
	binding, err := stagedag.StageBindingInput(stage)
	if err != nil {
		return cachekey.Input{}, err
	}
	out := cachekey.Input{SchemaVersion: 1, Profile: cachekey.ProfileV1, Namespace: "cutly.cache.chunk.document-v2.v2", ArtifactKind: "cutly.cache-artifact." + kind + "-chunk.document-v2.v2", PlanFingerprint: v.PlanFingerprint, ExecutionManifestFingerprint: v.ExecutionManifestFingerprint, StageProfile: "cutly.stage." + kind + "-chunk.document-v2.v2", Inputs: []cachekey.NormalizedInput{{Role: "chunk-identity", Ordinal: 0, ContentHash: digest(raw)}, binding}, OutputProfile: v.Domain}
	if out.Inputs[0].Role > out.Inputs[1].Role {
		out.Inputs[0], out.Inputs[1] = out.Inputs[1], out.Inputs[0]
	}
	r := &cachekey.IndexRange{Start: v.Range.First, Count: v.Range.Count}
	if kind == "video" {
		out.FrameRange = r
	} else {
		out.SampleRange = r
	}
	if err = cachekey.Validate(out); err != nil {
		return cachekey.Input{}, err
	}
	return out, nil
}
func Key(v Identity) (cachekey.Key, error) {
	input, err := Input(v)
	if err != nil {
		return cachekey.Key{}, err
	}
	return cachekey.New(input)
}

func NewReceipt(v Identity, hash string, size int64, invocation string, attempt uint8) (Receipt, error) {
	r := Receipt{SchemaVersion: 2, Profile: ReceiptProfile, Identity: v, ContentHash: hash, SizeBytes: size, WorkerInvocationID: invocation, Attempt: attempt, ExecutableDigest: v.ProviderExecutableDigest}
	if err := validateReceipt(r, false); err != nil {
		return Receipt{}, err
	}
	raw, err := boundedMarshal(r)
	if err != nil {
		return Receipt{}, err
	}
	r.Fingerprint = digest(raw)
	if _, err = boundedMarshal(r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}
func validateReceipt(r Receipt, fingerprint bool) error {
	if err := validateIdentity(r.Identity); err != nil {
		return err
	}
	if len(r.WorkerInvocationID) > 256 || !invocationPattern.MatchString(r.WorkerInvocationID) || r.SchemaVersion != 2 || r.Profile != ReceiptProfile || !cachekey.CanonicalSHA256(r.ContentHash) || r.SizeBytes != r.Identity.Range.Count*r.Identity.UnitBytes || r.ExecutableDigest != r.Identity.ProviderExecutableDigest || r.Attempt == 0 || r.Attempt > stagedag.MaxAttempts {
		return ErrInvalid
	}
	if fingerprint {
		want := r.Fingerprint
		r.Fingerprint = ""
		raw, err := boundedMarshal(r)
		if err != nil || !cachekey.CanonicalSHA256(want) || want != digest(raw) {
			return ErrInvalid
		}
	}
	return nil
}
func CanonicalReceipt(r Receipt) ([]byte, error) {
	if err := validateReceipt(r, true); err != nil {
		return nil, err
	}
	return boundedMarshal(r)
}
func DecodeReceipt(raw []byte) (Receipt, error) {
	var out Receipt
	if err := decode(raw, &out); err != nil {
		return Receipt{}, err
	}
	if err := validateReceipt(out, true); err != nil {
		return Receipt{}, err
	}
	return out, nil
}
func boundedMarshal(value any) ([]byte, error) {
	raw, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxRecordBytes {
		return nil, ErrLimit
	}
	return raw, nil
}
func decode(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > MaxRecordBytes {
		return ErrLimit
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{MaxInputBytes: MaxRecordBytes, MaxDepth: 16, MaxValues: 512, MaxStringBytes: MaxRecordBytes, MaxIntegerDigits: 20})
	if err != nil || !bytes.Equal(raw, canonical) {
		return ErrInvalid
	}
	if err = wirejson.Decode(raw, out); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	return nil
}
func decodeRecord(raw []byte) (record, error) {
	var r record
	if err := decode(raw, &r); err != nil {
		return record{}, err
	}
	if r.SchemaVersion != 2 || r.Profile != IndexProfile || validateReceipt(r.Receipt, true) != nil {
		return record{}, ErrCorrupt
	}
	k, err := Key(r.Receipt.Identity)
	if err != nil || k != r.Key {
		return record{}, ErrCorrupt
	}
	return r, nil
}
func encodeRecord(v Identity, r Receipt) (record, []byte, error) {
	if v != r.Identity || validateReceipt(r, true) != nil {
		return record{}, nil, ErrInvalid
	}
	k, err := Key(v)
	if err != nil {
		return record{}, nil, err
	}
	out := record{2, IndexProfile, k, r}
	raw, err := boundedMarshal(out)
	if err != nil {
		return record{}, nil, err
	}
	return out, raw, nil
}
