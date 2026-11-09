package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func (b *ChunkAssemblerV2) decodedV2(ctx context.Context, f *os.File, r evidence.BindingV2) (video, audio *evidence.DecodedSequence, err error) {
	if r.FrameRange.Count > 0 {
		s, n, e := b.decodeSequenceV2(ctx, decodedRequest{file: f, arguments: []string{"-map", "0:v:0", "-an", "-threads", "1", "-f", "rawvideo", "-pix_fmt", "rgba", "-"}, unit: evidence.EvidenceFrames, unitBytes: int64(r.Output.Width) * int64(r.Output.Height) * 4, first: r.FrameRange.First, expectedCount: r.FrameRange.Count, domain: r.Output.DecodedVideoProfile})
		if e != nil {
			return nil, nil, e
		}
		if n != r.FrameRange.Count {
			return nil, nil, fmt.Errorf("%w: decoded frame count", ErrCommand)
		}
		video = &s
	}
	if r.SampleRange.Count > 0 {
		audio, err = b.decodedAudioV2(ctx, f, r)
	}
	return video, audio, err
}

// AAC LC encodes 1024-sample frames. The canonical evidence projection hashes
// exactly the requested N samples, without synthesizing missing samples. Full
// decoder output is nevertheless consumed and must contain N..ceil(N/1024)*1024
// samples; an extra encoded frame or arbitrary appended segment is rejected.
// This bounded tail-padding projection is part of ChunkAssemblerEncoderProfileV2.
func (b *ChunkAssemblerV2) decodedAudioV2(ctx context.Context, f *os.File, r evidence.BindingV2) (*evidence.DecodedSequence, error) {
	n := r.SampleRange.Count
	unit := int64(r.Output.Channels) * 2
	wanted, err := checkedProduct(n, unit)
	if err != nil {
		return nil, err
	}
	padding := (1024 - n%1024) % 1024
	if n > int64(^uint64(0)>>1)-padding {
		return nil, ErrPreflight
	}
	maximum, err := checkedProduct(n+padding, unit)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-protocol_whitelist", "file,fd", "-i", "<artifact>", "-map", "0:a:0", "-vn", "-threads", "1", "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.FormatInt(r.Output.SampleRate, 10), "-ac", strconv.Itoa(int(r.Output.Channels)), "-"}
	cmd := newCommandContext(ctx, b.io.executable, args...)
	cmd.Env = append([]string(nil), b.io.runtimeEnvironment...)
	if err = replaceCommandInputs(cmd, []commandFileEndpoint{attachCommandFile(cmd, f)}); err != nil {
		return nil, err
	}
	stderr := newBoundedBuffer(b.io.maxStderrBytes)
	cmd.Stderr = stderr
	cmd, cleanup, err := b.superviseCommandV2(ctx, cmd)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		count, e := stdout.Read(buf)
		if count > 0 {
			if int64(count) > maximum-total {
				err = fmt.Errorf("%w: decoded AAC exceeds bounded padding", ErrCommand)
				break
			}
			take := int64(count)
			if take > wanted-total {
				take = wanted - total
			}
			if take > 0 {
				_, _ = hash.Write(buf[:take])
			}
			total += int64(count)
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			err = e
			break
		}
		if count == 0 {
			err = io.ErrNoProgress
			break
		}
	}
	if err != nil {
		_ = cmd.Cancel()
	}
	wait := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || wait != nil || stderr.exceeded || total < wanted || total%unit != 0 {
		return nil, fmt.Errorf("%w: decoded AAC count=%d/%d max=%d: %v %s", ErrCommand, total, wanted, maximum, errors.Join(err, wait), stderr.String())
	}
	return &evidence.DecodedSequence{Domain: r.Output.DecodedAudioProfile, Unit: evidence.EvidenceSamples, UnitBytes: unit, Range: r.SampleRange, Chunks: []evidence.DecodedChunk{{First: r.SampleRange.First, Count: n, ContentHash: "sha256:" + hex.EncodeToString(hash.Sum(nil))}}}, nil
}

func artifactV2(r evidence.BindingV2, m evidence.ManifestV2) (a api.RenderArtifact, err error) {
	uri, err := api.EvidenceSidecarURI(r.ArtifactURI)
	if err != nil {
		return a, err
	}
	return api.RenderArtifact{URI: r.ArtifactURI, ContentHash: m.Artifact.ContentHash, SizeBytes: m.Artifact.SizeBytes, ExecutionManifestFingerprint: r.ExecutionManifestFingerprint, EvidenceURI: uri, EvidenceFingerprint: m.Fingerprint, Duration: r.TimelineRange.Duration, FrameCount: r.FrameRange.Count, SampleCount: r.SampleRange.Count}, nil
}
