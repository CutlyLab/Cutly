package chunkplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

const (
	// FrameGroupCount and SampleBlockCount are protocol constants rather than
	// host tuning knobs. Changing either requires a new stage profile.
	FrameGroupCount  int64 = 64
	SampleBlockCount int64 = 65_536

	VideoStageProfileV1 = "cutly.stage.video-surface-frame-group.v1"
	AudioStageProfileV1 = "cutly.stage.audio-pcm-block.v1"

	VideoCacheNamespaceV1 = "cutly.cache.video-surface-chunk.v1"
	AudioCacheNamespaceV1 = "cutly.cache.audio-pcm-chunk.v1"

	VideoArtifactKindV1 = "cutly.cache-artifact.video-surface-chunk.v1"
	AudioArtifactKindV1 = "cutly.cache-artifact.audio-pcm-chunk.v1"

	VideoOutputProfileV1 = cpucompositor.SurfaceProfileV1
	AudioOutputProfileV1 = renderapi.DecodedAudioProfilePCM16LEV1

	VideoReceiptProfileV1 = "cutly.render.video-surface-chunk-receipt.v1"
	AudioReceiptProfileV1 = "cutly.render.audio-pcm-chunk-receipt.v1"

	DescriptorProfileV1 = "cutly.render.pre-codec-chunk-plan-descriptor.v1"
	MaxReceiptBytes     = 256 << 10
)

var (
	ErrConfiguration = errors.New("chunkplan: invalid configuration")
	ErrJob           = errors.New("chunkplan: invalid render job")
	ErrLimit         = errors.New("chunkplan: resource limit exceeded")
	ErrReceipt       = errors.New("chunkplan: invalid chunk receipt")
	ErrResume        = errors.New("chunkplan: invalid resume state")
	ErrAggregate     = errors.New("chunkplan: invalid chunk aggregation")
)

// Config fixes host admission estimates and the bounded task count. Chunk
// sizes are protocol constants and cannot be selected by callers.
type Config struct {
	VideoResources stagedag.ResourceEstimate
	AudioResources stagedag.ResourceEstimate
	Retry          stagedag.RetryPolicy
	MaxTasks       int
}

// ConfigForResources supplies protocol retry/task defaults while requiring
// callers to provide the only values for which no safe hidden default exists.
// Build ignores the estimate for an inactive domain.
func ConfigForResources(video, audio stagedag.ResourceEstimate) Config {
	return Config{
		VideoResources: video,
		AudioResources: audio,
		Retry:          stagedag.RetryPolicy{MaxAttempts: 2, RetryTransient: true, RetryResource: true},
		MaxTasks:       stagedag.MaxTasks,
	}
}

// ProfileDescriptor freezes what this phase proves and, importantly, what it
// does not claim yet.
type ProfileDescriptor struct {
	Profile                  string `json:"profile"`
	FrameGroupCount          int64  `json:"frame_group_count"`
	SampleBlockCount         int64  `json:"sample_block_count"`
	VideoStageProfile        string `json:"video_stage_profile"`
	AudioStageProfile        string `json:"audio_stage_profile"`
	VideoOutputProfile       string `json:"video_output_profile"`
	AudioOutputProfile       string `json:"audio_output_profile"`
	CacheSemantics           string `json:"cache_semantics"`
	ResumeSemantics          string `json:"resume_semantics"`
	AggregationSemantics     string `json:"aggregation_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	Profile:                  DescriptorProfileV1,
	FrameGroupCount:          FrameGroupCount,
	SampleBlockCount:         SampleBlockCount,
	VideoStageProfile:        VideoStageProfileV1,
	AudioStageProfile:        AudioStageProfileV1,
	VideoOutputProfile:       VideoOutputProfileV1,
	AudioOutputProfile:       AudioOutputProfileV1,
	CacheSemantics:           "Task cache preimages bind the immutable plan and execution manifest, exact composition range, dispatch stage and output profile while deliberately excluding JobID and publication URI. Equivalent jobs therefore share chunk keys; checkpoints remain bound to one job.",
	ResumeSemantics:          "A checkpoint is reusable only after stagedag binding validation, canonical chunk-receipt validation and an integrity-aware CAS availability check for the exact cache key, payload hash and byte size. Invalid, duplicate or missing state selects only that task for recomputation.",
	AggregationSemantics:     "Receipts must be in strictly contiguous composition order, share one plan, execution manifest, output profile and unit byte size, and cover the requested range exactly. The aggregate root reuses cutly.decoded-sequence.v1.",
	FailureSemantics:         "Nil context, invalid jobs, negative cache ranges, limit overflow, malformed receipts, cancellation and availability errors return no partial plan or aggregate. Invalid durable candidates are reported by stable rejection code and treated as missing work.",
	ExcludedResponsibilities: "Media decode, composition, audio DSP, worker execution, sandboxing, CAS publication, checkpoint persistence, leases, adaptive concurrency, codec encode, mux, final publication, attestation and production backend integration are outside this protocol phase.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

func DescriptorCanonicalJSON() []byte {
	raw, err := canonicaljson.Marshal(descriptorV1)
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), raw...)
}

func DescriptorHash() string {
	sum := sha256.Sum256(DescriptorCanonicalJSON())
	return "sha256:" + hex.EncodeToString(sum[:])
}
