package backendstage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

const (
	ChunkAssemblySchemaVersionV2   = 2
	ChunkAssemblyProfileV2         = "cutly.render.reference-chunk-assembly.document-v2.v2"
	ChunkAggregateBindingProfileV2 = "cutly.render.reference-chunk-aggregate-binding.document-v2.v2"
	ChunkAssemblyResumeModeV2      = "pre-codec-chunks-resume-final-codec-restarts.document-v2.v2"
	// V2 freezes these partitions independently of the V1 scheduler/provider.
	ChunkAssemblyFrameGroupCountV2  int64 = 64
	ChunkAssemblySampleBlockCountV2 int64 = 65536
)

var ErrChunkAssemblyInvalidV2 = errors.New("backendstage: invalid document-v2 chunk assembly request")
var ErrChunkAssemblyLimitV2 = errors.New("backendstage: document-v2 chunk assembly admission limit")

// Descriptor wire semantics and the decoded-sequence binary root algorithm are
// unchanged. Neither alias imports a V1 request, plan, provider or scheduler.
type ChunkDescriptorV2 = ChunkDescriptor
type ChunkAggregateRootV2 = ChunkAggregateRoot

type ChunkAggregateBindingV2 struct {
	Profile    string                `json:"profile"`
	ResumeMode string                `json:"resume_mode"`
	Video      *ChunkAggregateRootV2 `json:"video,omitempty"`
	Audio      *ChunkAggregateRootV2 `json:"audio,omitempty"`
}

// ChunkAssemblyRequestV2 is an identity contract, not proof of chunk bytes,
// executable provenance, resource authority or publication authorization.
// No original media locator is exposed. Collections are ordered and non-null.
type ChunkAssemblyRequestV2 struct {
	SchemaVersion                uint32                  `json:"schema_version"`
	Profile                      string                  `json:"profile"`
	CanonicalJSONProfile         string                  `json:"canonical_json_profile"`
	JobID                        string                  `json:"job_id"`
	ArtifactURI                  string                  `json:"artifact_uri"`
	DocumentHash                 string                  `json:"document_hash"`
	PlanFingerprint              string                  `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                  `json:"execution_manifest_fingerprint"`
	TimelineRange                documentv2.TimeRange    `json:"timeline_range"`
	FrameRange                   videoedit.TickRange     `json:"frame_range"`
	SampleRange                  videoedit.TickRange     `json:"sample_range"`
	Target                       videoedit.RenderTarget  `json:"target"`
	MediaRuntime                 api.MediaRuntime        `json:"media_runtime"`
	Output                       api.OutputSpecV2        `json:"output"`
	DeterministicSeed            int64                   `json:"deterministic_seed"`
	ProviderExecutableDigest     string                  `json:"provider_executable_digest"`
	Aggregate                    ChunkAggregateBindingV2 `json:"aggregate"`
	Audio                        []ChunkDescriptorV2     `json:"audio"`
	Video                        []ChunkDescriptorV2     `json:"video"`
	Fingerprint                  string                  `json:"fingerprint"`
}

// ChunkSourceV2 must open only admitted descriptors, in request order, and
// verify complete payload hash/size consumption on Close. This protocol adds no
// implementation; a host must supply and independently validate the source.
type ChunkSourceV2 interface {
	Open(context.Context, ChunkDescriptorV2) (io.ReadCloser, error)
}

// All assembly/recovery/publication operations bind the same verified request.
// A standalone verified request is not necessarily plan-bound: hosts must call
// VerifyChunkAssemblyRequestV2AgainstPlan before invoking a backend.
type ChunkAssemblerBackendV2 interface {
	ID() string
	Capabilities(context.Context) (api.BackendCapabilitiesV2, error)
	AssembleChunks(context.Context, VerifiedChunkAssemblyRequestV2, ChunkSourceV2, api.EventSink) (api.RenderArtifact, error)
	InspectChunkAssembly(context.Context, VerifiedChunkAssemblyRequestV2) (api.RenderArtifact, bool, error)
	OpenChunkAssembly(context.Context, VerifiedChunkAssemblyRequestV2) (PublishedMedia, bool, error)
}

func assemblyInvalidV2(message string) error {
	return fmt.Errorf("%w: %s", ErrChunkAssemblyInvalidV2, message)
}

// Header checks precede scans, cloning, hashing or other descriptor-proportional
// work. In particular a foreign provider cannot trigger work on its chunk graph.
func assemblyIdentityV2(r ChunkAssemblyRequestV2, m api.ExecutionManifestV2, p renderplanv2.Plan) error {
	if r.DocumentHash != p.DocumentHash || r.DocumentHash != m.DocumentHash || r.PlanFingerprint != p.Fingerprint || r.PlanFingerprint != m.PlanFingerprint || r.ExecutionManifestFingerprint != m.Fingerprint || r.ProviderExecutableDigest != m.ProviderBuild || r.Target != p.Target || r.Output != m.Output || r.MediaRuntime != m.MediaRuntime || r.DeterministicSeed != m.DeterministicSeed {
		return assemblyInvalidV2("document/plan/manifest/provider/target/output/runtime/seed binding")
	}
	if err := assemblyTimelineSubsetV2(r.TimelineRange, p.RequestedRange); err != nil {
		return err
	}
	for _, pair := range [][2]videoedit.TickRange{{r.FrameRange, p.FrameRange}, {r.SampleRange, p.SampleRange}} {
		end, e := pair[0].EndExclusive()
		limit, le := pair[1].EndExclusive()
		if e != nil || le != nil || pair[0].First < pair[1].First || end > limit {
			return assemblyInvalidV2("range outside plan")
		}
	}
	return nil
}

func assemblyTimelineSubsetV2(r, p documentv2.TimeRange) error {
	if r.Validate() != nil || p.Validate() != nil {
		return assemblyInvalidV2("timeline range")
	}
	end, e := r.End()
	limit, le := p.End()
	if e != nil || le != nil {
		return assemblyInvalidV2("timeline end overflow")
	}
	start, se := r.Start.Compare(p.Start)
	stop, ee := end.Compare(limit)
	if se != nil || ee != nil || start < 0 || stop > 0 {
		return assemblyInvalidV2("timeline outside plan")
	}
	return nil
}

func assemblyHeaderV2(r ChunkAssemblyRequestV2) error {
	// Bound URI/runtime/output strings before parsers or whitespace scans.
	for _, s := range []string{r.ArtifactURI, r.MediaRuntime.DecoderProfile, r.MediaRuntime.EncoderProfile, r.MediaRuntime.ColorProfile, r.Output.Format, r.Output.PixelFormat, r.Output.ColorSpace, r.Output.VideoCodec, r.Output.AudioCodec, r.Target.PixelFormat, r.Target.ColorSpace} {
		if len(s) > MaxChunkAssemblyStringBytesV2 {
			return ErrChunkAssemblyLimitV2
		}
	}
	if r.SchemaVersion != 2 || r.Profile != ChunkAssemblyProfileV2 || r.CanonicalJSONProfile != "cutly.json.canonical-integer.v1" || len(r.JobID) > 1024 || strings.TrimSpace(r.JobID) == "" {
		return assemblyInvalidV2("envelope/job identity")
	}
	for _, hash := range []string{r.DocumentHash, r.PlanFingerprint, r.ExecutionManifestFingerprint, r.ProviderExecutableDigest, r.Fingerprint, r.MediaRuntime.FFmpegBuild} {
		if !cachekey.CanonicalSHA256(hash) {
			return assemblyInvalidV2("digest")
		}
	}
	if _, err := api.EvidenceSidecarURI(r.ArtifactURI); err != nil {
		return assemblyInvalidV2("publication URI")
	}
	if r.DeterministicSeed != 0 || r.TimelineRange.Validate() != nil || r.TimelineRange.Duration.Value <= 0 || r.FrameRange.Validate() != nil || r.SampleRange.Validate() != nil || (r.FrameRange.Count == 0 && r.SampleRange.Count == 0) {
		return assemblyInvalidV2("seed/ranges")
	}
	t, o := r.Target, r.Output
	if len(t.TimelineID) > 1024 || strings.TrimSpace(string(t.TimelineID)) == "" || t.Width <= 0 || int64(t.Width) > math.MaxInt32 || t.Height <= 0 || int64(t.Height) > math.MaxInt32 || t.Channels <= 0 || int64(t.Channels) > math.MaxInt32 || t.FrameClock.Validate() != nil || t.SampleClock.Validate() != nil {
		return assemblyInvalidV2("target")
	}
	if int64(o.Width) != int64(t.Width) || int64(o.Height) != int64(t.Height) || int64(o.Channels) != int64(t.Channels) || o.PixelFormat != t.PixelFormat || o.ColorSpace != t.ColorSpace || o.FrameClock != t.FrameClock || o.SampleClock != t.SampleClock || o.SampleRate != t.SampleClock.SamplesPerSecond {
		return assemblyInvalidV2("output target layout/clocks")
	}
	if o.ReferenceProfile != api.OutputReferenceProfileV2 || o.Intermediate.VideoSurfaceProfile != api.VideoSurfaceProfileV2 || o.Intermediate.AudioBlockProfile != api.AudioBlockProfileV2 || o.DecodedVideoProfile != api.DecodedVideoProfileRGBA8V1 || o.DecodedAudioProfile != api.DecodedAudioProfilePCM16LEV1 {
		return assemblyInvalidV2("intermediate/final profiles")
	}
	for _, s := range []string{o.Format, o.PixelFormat, o.ColorSpace, o.VideoCodec, o.AudioCodec, r.MediaRuntime.DecoderProfile, r.MediaRuntime.EncoderProfile, r.MediaRuntime.ColorProfile} {
		if strings.TrimSpace(s) == "" {
			return assemblyInvalidV2("output/runtime identity")
		}
	}
	if r.MediaRuntime.TextShaperBuild != "" && !cachekey.CanonicalSHA256(r.MediaRuntime.TextShaperBuild) {
		return assemblyInvalidV2("text shaper digest")
	}
	frames, e := videoedit.CompileFrameTickRange(r.TimelineRange, t.FrameClock)
	if e != nil || frames != r.FrameRange {
		return assemblyInvalidV2("frame projection")
	}
	samples, e := videoedit.CompileSampleTickRange(r.TimelineRange, t.SampleClock)
	if e != nil || samples != r.SampleRange {
		return assemblyInvalidV2("sample projection")
	}
	if r.Aggregate.Profile != ChunkAggregateBindingProfileV2 || r.Aggregate.ResumeMode != ChunkAssemblyResumeModeV2 || r.Audio == nil || r.Video == nil {
		return assemblyInvalidV2("aggregate profile/collections")
	}
	if len(r.Audio) > MaxChunkAssemblyDescriptorsV2 || len(r.Video) > MaxChunkAssemblyDescriptorsV2-len(r.Audio) {
		return ErrChunkAssemblyLimitV2
	}
	return nil
}

func assemblyDomainV2(ctx context.Context, values []ChunkDescriptorV2, root *ChunkAggregateRootV2, domain string, expected videoedit.TickRange, unitBytes, partition int64, prefix string, unit evidence.EvidenceUnit) error {
	if expected.Count == 0 {
		if root != nil || len(values) != 0 {
			return assemblyInvalidV2("inactive domain payload")
		}
		return nil
	}
	count := expected.Count / partition
	if expected.Count%partition != 0 {
		count++
	}
	if count > MaxChunkAssemblyDescriptorsV2 || int64(len(values)) != count || root == nil || root.Domain != domain || root.Range != expected || root.UnitBytes != unitBytes || root.ChunkCount != len(values) || !cachekey.CanonicalSHA256(root.RootHash) {
		return assemblyInvalidV2("aggregate domain/layout/range/count")
	}
	sequence := evidence.DecodedSequence{Domain: domain, Unit: unit, UnitBytes: unitBytes, Range: expected, Chunks: make([]evidence.DecodedChunk, 0, len(values))}
	next, remaining := expected.First, expected.Count
	seen := make(map[cachekey.Key]bool, len(values))
	var total int64
	for i, d := range values {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := partition
		if remaining < n {
			n = remaining
		}
		if d.TaskID != fmt.Sprintf("%s-%05d", prefix, i) || d.Domain != domain || cachekey.ValidateKey(d.CacheKey) != nil || seen[d.CacheKey] || !cachekey.CanonicalSHA256(d.ContentHash) || d.Range.First != next || d.Range.Count != n || d.Range.Validate() != nil || d.UnitBytes != unitBytes || unitBytes <= 0 || n > math.MaxInt64/unitBytes || d.SizeBytes != n*unitBytes || d.SizeBytes > math.MaxInt64-total {
			return assemblyInvalidV2("descriptor order/identity/coverage/size")
		}
		seen[d.CacheKey] = true
		total += d.SizeBytes
		sequence.Chunks = append(sequence.Chunks, evidence.DecodedChunk{First: d.Range.First, Count: n, ContentHash: d.ContentHash})
		var err error
		next, err = d.Range.EndExclusive()
		if err != nil {
			return err
		}
		remaining -= n
	}
	if remaining != 0 {
		return assemblyInvalidV2("incomplete coverage")
	}
	// Reuse only the truly identical decoded-domain binary root contract.
	actual, err := evidence.ComputeDecodedRoot(sequence)
	if err != nil || actual != root.RootHash {
		return assemblyInvalidV2("aggregate root")
	}
	return ctx.Err()
}

func assemblyValidateV2(ctx context.Context, r ChunkAssemblyRequestV2) error {
	if err := assemblyHeaderV2(r); err != nil {
		return err
	}
	w, h := int64(r.Target.Width), int64(r.Target.Height)
	if w > math.MaxInt64/h || w*h > math.MaxInt64/8 {
		return assemblyInvalidV2("RGBA16 unit overflow")
	}
	if err := assemblyDomainV2(ctx, r.Audio, r.Aggregate.Audio, api.AudioBlockProfileV2, r.SampleRange, int64(r.Target.Channels)*2, ChunkAssemblySampleBlockCountV2, "audio", evidence.EvidenceSamples); err != nil {
		return err
	}
	if err := assemblyDomainV2(ctx, r.Video, r.Aggregate.Video, api.VideoSurfaceProfileV2, r.FrameRange, w*h*8, ChunkAssemblyFrameGroupCountV2, "video", evidence.EvidenceFrames); err != nil {
		return err
	}
	// A CAS identity cannot represent two different ordered domain payloads.
	keys := make(map[cachekey.Key]bool, len(r.Audio))
	var total int64
	for _, d := range r.Audio {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.SizeBytes > math.MaxInt64-total {
			return assemblyInvalidV2("total payload size overflow")
		}
		total += d.SizeBytes
		keys[d.CacheKey] = true
	}
	for _, d := range r.Video {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.SizeBytes > math.MaxInt64-total {
			return assemblyInvalidV2("total payload size overflow")
		}
		total += d.SizeBytes
		if keys[d.CacheKey] {
			return assemblyInvalidV2("cross-domain duplicate cache key")
		}
	}
	want, err := assemblyFingerprintV2(r)
	if err != nil || want != r.Fingerprint {
		return assemblyInvalidV2("fingerprint")
	}
	return ctx.Err()
}
