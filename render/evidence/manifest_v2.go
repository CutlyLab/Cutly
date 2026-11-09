package evidence

import (
	"context"
	"errors"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
)

const (
	SchemaVersionV2    uint32 = 2
	ProfileV2                 = "cutly.evidence-manifest.document-v2.v2"
	AssemblyProfileV2         = "cutly.render.reference-chunk-assembly.document-v2.v2"
	AggregateProfileV2        = "cutly.render.reference-chunk-aggregate-binding.document-v2.v2"
	ResumeModeV2              = "pre-codec-chunks-resume-final-codec-restarts.document-v2.v2"
)

var (
	ErrEvidenceInvalidV2 = errors.New("evidence: invalid document-v2 manifest")
	ErrEvidenceLimitV2   = errors.New("evidence: document-v2 admission limit")
)

// These neutral wire types deliberately do not import backendstage. Their shape
// is identical to ChunkAssemblyRequestV2, so adapters can retain every request
// fact without translating through a V1 request or execution manifest.
type ChunkDescriptorV2 struct {
	TaskID      string              `json:"task_id"`
	Domain      string              `json:"domain"`
	CacheKey    cachekey.Key        `json:"cache_key"`
	Range       videoedit.TickRange `json:"range"`
	ContentHash string              `json:"content_hash"`
	SizeBytes   int64               `json:"size_bytes"`
	UnitBytes   int64               `json:"unit_bytes"`
}
type ChunkAggregateRootV2 struct {
	Domain     string              `json:"domain"`
	UnitBytes  int64               `json:"unit_bytes"`
	Range      videoedit.TickRange `json:"range"`
	ChunkCount int                 `json:"chunk_count"`
	RootHash   string              `json:"root_hash"`
}
type ChunkAggregateBindingV2 struct {
	Profile    string                `json:"profile"`
	ResumeMode string                `json:"resume_mode"`
	Video      *ChunkAggregateRootV2 `json:"video,omitempty"`
	Audio      *ChunkAggregateRootV2 `json:"audio,omitempty"`
}

// BindingV2 authenticates request identity, not authorization, executable
// provenance or possession of CAS bytes. Hosts must additionally verify the
// original request against its native Document-v2 plan and execution manifest.
type BindingV2 struct {
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

// ManifestV2 records intermediate RGBA16/PCM identities separately from final
// decoded RGBA8/PCM identities. Lossy encoding is never asserted to preserve
// intermediate roots. Encoded artifact hashes need not be repeatable.
// WorkerExecutableDigest identifies the independently measured assembler worker,
// not the pre-codec provider. It cannot establish provider executable provenance:
// hosts must verify the S6 provider worker's measured hash equals ProviderBuild
// (Assembly.ProviderExecutableDigest) before accepting its chunk receipts.
// The production FFmpeg backend currently enforces a narrower same-provider-
// worker deployment: its configured measured worker digest must equal that
// provider digest. This generic protocol permits independently measured roles;
// neither representation replaces the S6 parent's executable measurement.
type ManifestV2 struct {
	SchemaVersion          uint32           `json:"schema_version"`
	Profile                string           `json:"profile"`
	CanonicalJSONProfile   string           `json:"canonical_json_profile"`
	Assembly               BindingV2        `json:"assembly"`
	WorkerExecutableDigest string           `json:"worker_executable_digest"`
	Artifact               ArtifactIdentity `json:"artifact"`
	DecodedVideo           *DecodedSequence `json:"decoded_video,omitempty"`
	DecodedAudio           *DecodedSequence `json:"decoded_audio,omitempty"`
	Fingerprint            string           `json:"fingerprint"`
}

func invalidV2(message string) error { return fmt.Errorf("%w: %s", ErrEvidenceInvalidV2, message) }
func contextV2(ctx context.Context) error {
	if ctx == nil {
		return invalidV2("nil context")
	}
	return ctx.Err()
}
func recoverV2[T any](out *T, err *error) {
	if x := recover(); x != nil {
		*err = fmt.Errorf("%w: boundary panic: %v", ErrEvidenceInvalidV2, x)
	}
	if *err != nil {
		var zero T
		*out = zero
	}
}

// NewManifestV2 fills the envelope, missing decoded roots and fingerprint only.
// It never sorts, repairs coverage, changes request identities or overwrites a
// supplied wrong root. All returned collections are owned by the caller.
func NewManifestV2(ctx context.Context, binding BindingV2, workerDigest, artifactHash string, artifactSize int64, video, audio *DecodedSequence) (out ManifestV2, err error) {
	defer recoverV2(&out, &err)
	draft := ManifestV2{SchemaVersion: SchemaVersionV2, Profile: ProfileV2, CanonicalJSONProfile: "cutly.json.canonical-integer.v1", Assembly: binding, WorkerExecutableDigest: workerDigest, Artifact: ArtifactIdentity{URI: binding.ArtifactURI, ContentHash: artifactHash, SizeBytes: artifactSize}, DecodedVideo: video, DecodedAudio: audio}
	if err = admissionV2(ctx, draft); err != nil {
		return out, err
	}
	// Bound and validate all request facts before cloning or hashing decoded data.
	if err = validateBindingV2(ctx, binding); err != nil {
		return out, err
	}
	draft = cloneV2(draft)
	for _, seq := range []*DecodedSequence{draft.DecodedVideo, draft.DecodedAudio} {
		if seq == nil {
			continue
		}
		root, e := decodedRootV2(ctx, *seq)
		if e != nil {
			return out, e
		}
		if seq.RootHash != "" && seq.RootHash != root {
			return out, invalidV2("supplied decoded root")
		}
		seq.RootHash = root
	}
	if err = validateV2(ctx, draft); err != nil {
		return out, err
	}
	draft.Fingerprint, err = fingerprintV2(draft)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return draft, nil
}

func cloneV2(m ManifestV2) ManifestV2 {
	m.Assembly.Audio = append(make([]ChunkDescriptorV2, 0, len(m.Assembly.Audio)), m.Assembly.Audio...)
	m.Assembly.Video = append(make([]ChunkDescriptorV2, 0, len(m.Assembly.Video)), m.Assembly.Video...)
	if m.Assembly.Aggregate.Video != nil {
		x := *m.Assembly.Aggregate.Video
		m.Assembly.Aggregate.Video = &x
	}
	if m.Assembly.Aggregate.Audio != nil {
		x := *m.Assembly.Aggregate.Audio
		m.Assembly.Aggregate.Audio = &x
	}
	for _, p := range []**DecodedSequence{&m.DecodedVideo, &m.DecodedAudio} {
		if *p != nil {
			x := **p
			x.Chunks = append([]DecodedChunk(nil), x.Chunks...)
			*p = &x
		}
	}
	return m
}
