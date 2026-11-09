package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
)

// Keep this neutral validator aligned with the frozen assembly protocol. The
// backendstage adapter additionally validates against the original request.
func bindingHeaderV2(r BindingV2) error {
	for _, s := range []string{r.ArtifactURI, r.JobID, string(r.Target.TimelineID), r.MediaRuntime.DecoderProfile, r.MediaRuntime.EncoderProfile, r.MediaRuntime.ColorProfile, r.Output.Format, r.Output.PixelFormat, r.Output.ColorSpace, r.Output.VideoCodec, r.Output.AudioCodec, r.Target.PixelFormat, r.Target.ColorSpace} {
		if len(s) > MaxEvidenceStringBytesV2 {
			return ErrEvidenceLimitV2
		}
	}
	if r.SchemaVersion != 2 || r.Profile != AssemblyProfileV2 || r.CanonicalJSONProfile != "cutly.json.canonical-integer.v1" || len(r.JobID) > 1024 || strings.TrimSpace(r.JobID) == "" {
		return invalidV2("assembly envelope/job")
	}
	for _, h := range []string{r.DocumentHash, r.PlanFingerprint, r.ExecutionManifestFingerprint, r.ProviderExecutableDigest, r.Fingerprint, r.MediaRuntime.FFmpegBuild} {
		if !cachekey.CanonicalSHA256(h) {
			return invalidV2("assembly digest")
		}
	}
	if _, err := api.EvidenceSidecarURI(r.ArtifactURI); err != nil {
		return invalidV2("artifact URI")
	}
	if r.DeterministicSeed != 0 || r.TimelineRange.Validate() != nil || r.TimelineRange.Duration.Value <= 0 || r.FrameRange.Validate() != nil || r.SampleRange.Validate() != nil || (r.FrameRange.Count == 0 && r.SampleRange.Count == 0) {
		return invalidV2("assembly seed/ranges")
	}
	t, o := r.Target, r.Output
	if len(t.TimelineID) > 1024 || strings.TrimSpace(string(t.TimelineID)) == "" || t.Width <= 0 || int64(t.Width) > math.MaxInt32 || t.Height <= 0 || int64(t.Height) > math.MaxInt32 || t.Channels <= 0 || int64(t.Channels) > math.MaxInt32 || t.FrameClock.Validate() != nil || t.SampleClock.Validate() != nil {
		return invalidV2("target")
	}
	if int64(o.Width) != int64(t.Width) || int64(o.Height) != int64(t.Height) || int64(o.Channels) != int64(t.Channels) || o.PixelFormat != t.PixelFormat || o.ColorSpace != t.ColorSpace || o.FrameClock != t.FrameClock || o.SampleClock != t.SampleClock || o.SampleRate != t.SampleClock.SamplesPerSecond {
		return invalidV2("output target layout/clocks")
	}
	if o.ReferenceProfile != api.OutputReferenceProfileV2 || o.Intermediate.VideoSurfaceProfile != api.VideoSurfaceProfileV2 || o.Intermediate.AudioBlockProfile != api.AudioBlockProfileV2 || o.DecodedVideoProfile != api.DecodedVideoProfileRGBA8V1 || o.DecodedAudioProfile != api.DecodedAudioProfilePCM16LEV1 {
		return invalidV2("output intermediate/final profiles")
	}
	for _, s := range []string{o.Format, o.PixelFormat, o.ColorSpace, o.VideoCodec, o.AudioCodec, r.MediaRuntime.DecoderProfile, r.MediaRuntime.EncoderProfile, r.MediaRuntime.ColorProfile} {
		if strings.TrimSpace(s) == "" {
			return invalidV2("output/runtime identity")
		}
	}
	if r.MediaRuntime.TextShaperBuild != "" && !cachekey.CanonicalSHA256(r.MediaRuntime.TextShaperBuild) {
		return invalidV2("text shaper digest")
	}
	frames, err := videoedit.CompileFrameTickRange(r.TimelineRange, t.FrameClock)
	if err != nil || frames != r.FrameRange {
		return invalidV2("frame projection")
	}
	samples, err := videoedit.CompileSampleTickRange(r.TimelineRange, t.SampleClock)
	if err != nil || samples != r.SampleRange {
		return invalidV2("sample projection")
	}
	if r.Aggregate.Profile != AggregateProfileV2 || r.Aggregate.ResumeMode != ResumeModeV2 || r.Audio == nil || r.Video == nil {
		return invalidV2("aggregate envelope/collections")
	}
	if len(r.Audio) > MaxEvidenceDescriptorsV2 || len(r.Video) > MaxEvidenceDescriptorsV2-len(r.Audio) {
		return ErrEvidenceLimitV2
	}
	return nil
}

func validateBindingV2(ctx context.Context, r BindingV2) error {
	if err := bindingHeaderV2(r); err != nil {
		return err
	}
	if err := graphAdmissionLimitsV2(ctx, r, 8<<20, 200000); err != nil {
		return err
	}
	w, h := int64(r.Target.Width), int64(r.Target.Height)
	if w > math.MaxInt64/h || w*h > math.MaxInt64/8 {
		return invalidV2("RGBA16 size overflow")
	}
	if err := intermediateV2(ctx, r.Audio, r.Aggregate.Audio, api.AudioBlockProfileV2, EvidenceSamples, r.SampleRange, int64(r.Target.Channels)*2, 65536, "audio"); err != nil {
		return err
	}
	if err := intermediateV2(ctx, r.Video, r.Aggregate.Video, api.VideoSurfaceProfileV2, EvidenceFrames, r.FrameRange, w*h*8, 64, "video"); err != nil {
		return err
	}
	keys := make(map[cachekey.Key]bool, len(r.Audio)+len(r.Video))
	var total int64
	for _, values := range [][]ChunkDescriptorV2{r.Audio, r.Video} {
		for _, d := range values {
			if err := ctx.Err(); err != nil {
				return err
			}
			if keys[d.CacheKey] || d.SizeBytes > math.MaxInt64-total {
				return invalidV2("duplicate cache key/total size overflow")
			}
			keys[d.CacheKey] = true
			total += d.SizeBytes
		}
	}
	want, err := bindingFingerprintV2(r)
	if err != nil {
		return err
	}
	if want != r.Fingerprint {
		return invalidV2("assembly fingerprint")
	}
	return ctx.Err()
}

func intermediateV2(ctx context.Context, ds []ChunkDescriptorV2, root *ChunkAggregateRootV2, domain string, unit EvidenceUnit, expected videoedit.TickRange, unitBytes, partition int64, prefix string) error {
	if expected.Count == 0 {
		if root != nil || len(ds) != 0 {
			return invalidV2("inactive intermediate domain")
		}
		return nil
	}
	count := expected.Count / partition
	if expected.Count%partition != 0 {
		count++
	}
	if count > MaxEvidenceDescriptorsV2 || int64(len(ds)) != count || root == nil || root.Domain != domain || root.UnitBytes != unitBytes || root.Range != expected || root.ChunkCount != len(ds) || !validDigest(root.RootHash) {
		return invalidV2("intermediate aggregate identity")
	}
	seq := DecodedSequence{Domain: domain, Unit: unit, UnitBytes: unitBytes, Range: expected, Chunks: make([]DecodedChunk, 0, len(ds))}
	next, remaining := expected.First, expected.Count
	for i, d := range ds {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := partition
		if remaining < n {
			n = remaining
		}
		if d.TaskID != fmt.Sprintf("%s-%05d", prefix, i) || d.Domain != domain || cachekey.ValidateKey(d.CacheKey) != nil || !validDigest(d.ContentHash) || d.Range.First != next || d.Range.Count != n || d.Range.Validate() != nil || d.UnitBytes != unitBytes || n > math.MaxInt64/unitBytes || d.SizeBytes != n*unitBytes {
			return invalidV2("intermediate descriptor identity/order/size")
		}
		seq.Chunks = append(seq.Chunks, DecodedChunk{First: next, Count: n, ContentHash: d.ContentHash})
		var err error
		next, err = d.Range.EndExclusive()
		if err != nil {
			return err
		}
		remaining -= n
	}
	actual, err := decodedRootV2(ctx, seq)
	if err != nil {
		return err
	}
	if remaining != 0 || root.RootHash != actual {
		return invalidV2("intermediate aggregate root")
	}
	return nil
}

// Same immutable binary root algorithm as V1, with bounded, cancellable V2
// validation. It never sorts supplied chunks or relaxes V1's byte contract.
func decodedRootV2(ctx context.Context, s DecodedSequence) (string, error) {
	if len(s.Domain) == 0 || len(s.Domain) > 256 || strings.TrimSpace(s.Domain) == "" || (s.Unit != EvidenceFrames && s.Unit != EvidenceSamples) || s.UnitBytes <= 0 || s.Range.Validate() != nil || s.Range.Count <= 0 || len(s.Chunks) == 0 {
		return "", invalidV2("decoded sequence shape")
	}
	if len(s.Chunks) > MaxEvidenceDescriptorsV2 {
		return "", ErrEvidenceLimitV2
	}
	if s.Range.Count > math.MaxInt64/s.UnitBytes {
		return "", invalidV2("decoded payload overflow")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("cutly.decoded-sequence.v1\x00"))
	writeRootString(hash, s.Domain)
	writeRootString(hash, string(s.Unit))
	writeRootInt64(hash, s.UnitBytes)
	writeRootInt64(hash, s.Range.First)
	writeRootInt64(hash, s.Range.Count)
	next := s.Range.First
	for _, c := range s.Chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if c.First != next || c.Count <= 0 || !validDigest(c.ContentHash) {
			return "", invalidV2("decoded chunks order/hash")
		}
		var err error
		next, err = (videoedit.TickRange{First: c.First, Count: c.Count}).EndExclusive()
		if err != nil {
			return "", invalidV2("decoded chunk overflow")
		}
		writeRootInt64(hash, c.First)
		writeRootInt64(hash, c.Count)
		raw, _ := hex.DecodeString(c.ContentHash[7:])
		_, _ = hash.Write(raw)
	}
	end, err := s.Range.EndExclusive()
	if err != nil || next != end {
		return "", invalidV2("decoded coverage")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), ctx.Err()
}

func validateV2(ctx context.Context, m ManifestV2) error {
	if err := validateBindingV2(ctx, m.Assembly); err != nil {
		return err
	}
	o := m.Assembly.Output
	for _, item := range []struct {
		s      *DecodedSequence
		unit   EvidenceUnit
		r      videoedit.TickRange
		domain string
		bytes  int64
	}{
		{m.DecodedVideo, EvidenceFrames, m.Assembly.FrameRange, o.DecodedVideoProfile, int64(o.Width) * int64(o.Height) * 4},
		{m.DecodedAudio, EvidenceSamples, m.Assembly.SampleRange, o.DecodedAudioProfile, int64(o.Channels) * 2},
	} {
		if item.r.Count == 0 {
			if item.s != nil {
				return invalidV2("inactive decoded domain")
			}
			continue
		}
		if item.s == nil || item.s.Domain != item.domain || item.s.Unit != item.unit || item.s.Range != item.r || item.s.UnitBytes != item.bytes {
			return invalidV2("decoded layout/range")
		}
		root, err := decodedRootV2(ctx, *item.s)
		if err != nil {
			return err
		}
		if item.s.RootHash != root {
			return invalidV2("decoded root")
		}
	}
	return ctx.Err()
}
