package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func (backend *Backend) verifyDecodedOutput(ctx context.Context, encoded *os.File, request renderapi.RenderRequest, contentHash string, size int64) (evidence.Manifest, error) {
	frameBytes, err := checkedProduct(int64(request.Manifest.Output.Width), int64(request.Manifest.Output.Height), 4)
	if err != nil {
		return evidence.Manifest{}, fmt.Errorf("%w: decoded frame size: %v", ErrCommand, err)
	}
	video, frameCount, err := backend.decodeSequence(ctx, decodedRequest{
		file: encoded,
		arguments: []string{
			"-map", "0:v:0", "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-",
		},
		unit: evidence.EvidenceFrames, unitBytes: frameBytes, first: request.Job.FrameRange.First,
		expectedCount: request.Job.FrameRange.Count, domain: request.Manifest.Output.DecodedVideoProfile,
	})
	if err != nil {
		return evidence.Manifest{}, err
	}
	audioBytes, err := checkedProduct(int64(request.Manifest.Output.Channels), 2)
	if err != nil {
		return evidence.Manifest{}, fmt.Errorf("%w: decoded audio sample size: %v", ErrCommand, err)
	}
	audio, sampleCount, err := backend.decodeSequence(ctx, decodedRequest{
		file: encoded,
		arguments: []string{
			"-map", "0:a:0", "-vn", "-f", "s16le", "-acodec", "pcm_s16le",
			"-ar", strconv.FormatInt(request.Manifest.Output.SampleRate, 10),
			"-ac", strconv.Itoa(request.Manifest.Output.Channels), "-",
		},
		unit: evidence.EvidenceSamples, unitBytes: audioBytes, first: request.Job.SampleRange.First,
		expectedCount: request.Job.SampleRange.Count,
		domain:        request.Manifest.Output.DecodedAudioProfile,
	})
	if err != nil {
		return evidence.Manifest{}, err
	}
	if frameCount != request.Job.FrameRange.Count || sampleCount != request.Job.SampleRange.Count {
		return evidence.Manifest{}, fmt.Errorf(
			"%w: decoded counts frames=%d/%d samples=%d/%d",
			ErrCommand, frameCount, request.Job.FrameRange.Count, sampleCount, request.Job.SampleRange.Count,
		)
	}
	frozen, err := evidence.FreezeForRender(request, evidence.Manifest{
		Artifact: evidence.ArtifactIdentity{
			ContentHash: contentHash, SizeBytes: size,
		},
		DecodedVideo: &video, DecodedAudio: &audio,
		Cache: evidence.CacheEvidence{Hit: false, Key: request.Manifest.Fingerprint},
	})
	if err != nil {
		return evidence.Manifest{}, fmt.Errorf("%w: freeze decoded evidence: %v", ErrCommand, err)
	}
	return frozen, nil
}

type decodedRequest struct {
	file          *os.File
	arguments     []string
	unit          evidence.EvidenceUnit
	unitBytes     int64
	first         int64
	expectedCount int64
	domain        string
}

func (backend *Backend) decodeSequence(ctx context.Context, request decodedRequest) (evidence.DecodedSequence, int64, error) {
	if request.file == nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: encoded artifact handle is nil", ErrCommand)
	}
	maxBytes, err := checkedProduct(request.expectedCount, request.unitBytes)
	if err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s limit: %v", ErrCommand, request.unit, err)
	}
	if _, err := request.file.Seek(0, io.SeekStart); err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: rewind encoded artifact: %v", ErrCommand, err)
	}
	arguments := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
		"-protocol_whitelist", "file,fd", "-i", "<encoded-output>",
	}
	arguments = append(arguments, request.arguments...)
	command := newCommandContext(ctx, backend.executable, arguments...)
	command.Env = append([]string(nil), backend.runtimeEnvironment...)
	if err := replaceCommandInputs(command, []commandFileEndpoint{attachCommandFile(command, request.file)}); err != nil {
		return evidence.DecodedSequence{}, 0, err
	}
	stderr := newBoundedBuffer(backend.maxStderrBytes)
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s pipe: %v", ErrCommand, request.unit, err)
	}
	if err := command.Start(); err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: start decoded %s evidence: %v", ErrCommand, request.unit, err)
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var byteCount int64
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			readErr = err
			break
		}
		count, err := stdout.Read(buffer)
		if count > 0 {
			if byteCount > math.MaxInt64-int64(count) || byteCount+int64(count) > maxBytes {
				readErr = videoedit.ErrTimeOverflow
				break
			}
			byteCount += int64(count)
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	var cancelErr error
	if readErr != nil && command.Cancel != nil {
		cancelErr = command.Cancel()
		if errors.Is(cancelErr, os.ErrProcessDone) {
			cancelErr = nil
		}
	}
	waitErr := command.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return evidence.DecodedSequence{}, 0, ctxErr
	}
	if readErr != nil || waitErr != nil || stderr.exceeded {
		return evidence.DecodedSequence{}, 0, fmt.Errorf(
			"%w: decode %s: %v: %s", ErrCommand, request.unit,
			errors.Join(readErr, cancelErr, waitErr), strings.TrimSpace(stderr.String()),
		)
	}
	if request.unitBytes <= 0 || byteCount == 0 || byteCount%request.unitBytes != 0 {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s bytes %d do not align to unit size %d", ErrCommand, request.unit, byteCount, request.unitBytes)
	}
	count := byteCount / request.unitBytes
	contentHash := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	sequence := evidence.DecodedSequence{
		Domain: request.domain, Unit: request.unit, UnitBytes: request.unitBytes,
		Range:  videoedit.TickRange{First: request.first, Count: count},
		Chunks: []evidence.DecodedChunk{{First: request.first, Count: count, ContentHash: contentHash}},
	}
	return sequence, count, nil
}

func checkedProduct(values ...int64) (int64, error) {
	result := int64(1)
	for _, value := range values {
		if value <= 0 || result > math.MaxInt64/value {
			return 0, videoedit.ErrTimeOverflow
		}
		result *= value
	}
	return result, nil
}
