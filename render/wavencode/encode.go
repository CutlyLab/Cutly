package wavencode

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
)

type Artifact struct {
	valid bool
	bytes []byte
}

type Result struct {
	Profile                string    `json:"profile"`
	InputQuantization      string    `json:"input_quantization_profile"`
	InputSampleProfile     string    `json:"input_sample_profile"`
	ArtifactHashProfile    string    `json:"artifact_hash_profile"`
	SampleRate             int64     `json:"sample_rate"`
	FirstCompositionSample int64     `json:"first_composition_sample"`
	FrameCount             uint64    `json:"frame_count"`
	Channels               int       `json:"channels"`
	ChannelOrder           string    `json:"channel_order"`
	BitsPerSample          int       `json:"bits_per_sample"`
	DataBytes              uint64    `json:"data_bytes"`
	ArtifactBytes          uint64    `json:"artifact_bytes"`
	SourcePCMHash          string    `json:"source_pcm_hash"`
	ArtifactHash           string    `json:"artifact_hash"`
	Artifact               *Artifact `json:"-"`
}

func Encode(ctx context.Context, input pcm16le.Result, limits Limits) (Result, error) {
	if ctx == nil {
		return Result{}, encodeError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return Result{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return Result{}, err
	}
	if input.PCM == nil || input.Profile != pcm16le.ProfileV1 ||
		input.InputSampleProfile != audiograph.SampleDomainProfileV1 ||
		input.OutputSampleProfile != renderapi.DecodedAudioProfilePCM16LEV1 ||
		input.Channels != pcm16le.Channels || input.ChannelOrder != "FL,FR" ||
		input.BytesPerFrame != pcm16le.BytesPerFrame || input.SampleRate <= 0 {
		return Result{}, encodeError(CodeInputInvalid, "input", ErrInputInvalid)
	}
	if input.FrameCount != input.PCM.FrameCount() {
		return Result{}, encodeError(CodeInputMismatch, "input/frame_count", ErrInputMismatch)
	}
	if input.FrameCount > math.MaxInt64 {
		return Result{}, encodeError(CodeResourceLimit, "input/frame_count", ErrResourceLimit)
	}
	if err := (videoedit.TickRange{First: input.FirstCompositionSample, Count: int64(input.FrameCount)}).Validate(); err != nil {
		return Result{}, encodeError(CodeInputInvalid, "input/composition_sample_range", errors.Join(ErrInputInvalid, err))
	}
	layout, err := checkedLayout(input.FrameCount, input.SampleRate, normalized)
	if err != nil {
		return Result{}, err
	}
	pcmDigest := sha256.New()
	if err := input.PCM.WriteCanonical(&contextHashWriter{ctx: ctx, writer: pcmDigest}); err != nil {
		if canceled := contextFailure(ctx, "input/pcm_hash"); canceled != nil {
			return Result{}, canceled
		}
		return Result{}, encodeError(CodeInputInvalid, "input/pcm", errors.Join(ErrInputInvalid, err))
	}
	actualPCMHash := "sha256:" + hex.EncodeToString(pcmDigest.Sum(nil))
	if actualPCMHash != input.PCMHash {
		return Result{}, encodeError(CodeInputMismatch, "input/pcm_hash", ErrInputMismatch)
	}
	if err := contextFailure(ctx, "allocate"); err != nil {
		return Result{}, err
	}
	encoded, err := allocateBytes(int(layout.artifactBytes))
	if err != nil {
		return Result{}, err
	}
	writeHeader(encoded[:HeaderBytes], layout.sampleRate, uint32(layout.dataBytes), layout.byteRate)
	payloadWriter := &contextSliceWriter{ctx: ctx, destination: encoded[HeaderBytes:]}
	if err := input.PCM.WriteCanonical(payloadWriter); err != nil {
		if canceled := contextFailure(ctx, "payload"); canceled != nil {
			return Result{}, canceled
		}
		return Result{}, encodeError(CodeInputMismatch, "input/pcm_bytes", errors.Join(ErrInputMismatch, err))
	}
	if payloadWriter.offset != len(payloadWriter.destination) {
		return Result{}, encodeError(CodeInputMismatch, "input/pcm_bytes", ErrInputMismatch)
	}
	digest, err := hashBytes(ctx, encoded)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return Result{}, err
	}
	artifact := &Artifact{valid: true, bytes: encoded}
	return Result{
		Profile: ProfileV1, InputQuantization: pcm16le.ProfileV1,
		InputSampleProfile:  renderapi.DecodedAudioProfilePCM16LEV1,
		ArtifactHashProfile: ArtifactHashProfile,
		SampleRate:          input.SampleRate, FirstCompositionSample: input.FirstCompositionSample,
		FrameCount: input.FrameCount, Channels: pcm16le.Channels, ChannelOrder: "FL,FR",
		BitsPerSample: 16, DataBytes: layout.dataBytes, ArtifactBytes: layout.artifactBytes,
		SourcePCMHash: actualPCMHash, ArtifactHash: "sha256:" + hex.EncodeToString(digest),
		Artifact: artifact,
	}, nil
}

type wavLayout struct {
	sampleRate    uint32
	byteRate      uint32
	dataBytes     uint64
	artifactBytes uint64
}

func checkedLayout(frameCount uint64, sampleRate int64, limits Limits) (wavLayout, error) {
	if sampleRate <= 0 {
		return wavLayout{}, encodeError(CodeInputInvalid, "input/sample_rate", ErrInputInvalid)
	}
	if sampleRate > int64(HardMaxSampleRate) {
		return wavLayout{}, encodeError(CodeResourceLimit, "input/sample_rate", ErrResourceLimit)
	}
	if frameCount > math.MaxUint64/pcm16le.BytesPerFrame {
		return wavLayout{}, encodeError(CodeResourceLimit, "input/frame_count", ErrResourceLimit)
	}
	dataBytes := frameCount * pcm16le.BytesPerFrame
	artifactBytes := dataBytes + HeaderBytes
	if dataBytes > HardMaxDataBytes || dataBytes > limits.MaxDataBytes || artifactBytes < dataBytes ||
		artifactBytes > HardMaxArtifactBytes || artifactBytes > limits.MaxArtifactBytes ||
		artifactBytes > uint64(maxInt()) {
		return wavLayout{}, encodeError(CodeResourceLimit, "artifact_bytes", ErrResourceLimit)
	}
	byteRate := uint64(sampleRate) * pcm16le.BytesPerFrame
	if byteRate > math.MaxUint32 {
		return wavLayout{}, encodeError(CodeResourceLimit, "input/sample_rate", ErrResourceLimit)
	}
	return wavLayout{
		sampleRate: uint32(sampleRate), byteRate: uint32(byteRate),
		dataBytes: dataBytes, artifactBytes: artifactBytes,
	}, nil
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxDataBytes == 0 {
		value.MaxDataBytes = defaults.MaxDataBytes
	}
	if value.MaxArtifactBytes == 0 {
		value.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if value.MaxDataBytes > HardMaxDataBytes || value.MaxArtifactBytes < HeaderBytes ||
		value.MaxArtifactBytes > HardMaxArtifactBytes || value.MaxDataBytes > value.MaxArtifactBytes-HeaderBytes {
		return Limits{}, encodeError(CodeInputInvalid, "limits", ErrInputInvalid)
	}
	return value, nil
}

type contextHashWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *contextHashWriter) Write(value []byte) (int, error) {
	if writer == nil || writer.ctx == nil || writer.writer == nil {
		return 0, io.ErrClosedPipe
	}
	const chunkBytes = 64 << 10
	written := 0
	for written < len(value) {
		if err := contextFailure(writer.ctx, "input/pcm_hash"); err != nil {
			return written, err
		}
		end := written + chunkBytes
		if end > len(value) {
			end = len(value)
		}
		start := written
		count, err := writer.writer.Write(value[start:end])
		if count < 0 || count > end-start {
			return written, io.ErrShortWrite
		}
		written += count
		if err != nil {
			return written, err
		}
		if count != end-start {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func writeHeader(header []byte, sampleRate, dataBytes, byteRate uint32) {
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], pcm16le.Channels)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], byteRate)
	binary.LittleEndian.PutUint16(header[32:34], pcm16le.BytesPerFrame)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
}

type contextSliceWriter struct {
	ctx         context.Context
	destination []byte
	offset      int
}

func (writer *contextSliceWriter) Write(value []byte) (int, error) {
	if writer == nil || writer.ctx == nil || writer.offset < 0 || writer.offset > len(writer.destination) ||
		len(value) > len(writer.destination)-writer.offset {
		return 0, io.ErrShortWrite
	}
	const chunkBytes = 64 << 10
	written := 0
	for written < len(value) {
		if err := contextFailure(writer.ctx, "payload"); err != nil {
			return written, err
		}
		end := written + chunkBytes
		if end > len(value) {
			end = len(value)
		}
		copy(writer.destination[writer.offset+written:writer.offset+end], value[written:end])
		written = end
	}
	writer.offset += written
	return written, nil
}

func hashBytes(ctx context.Context, value []byte) ([]byte, error) {
	digest := sha256.New()
	const chunkBytes = 64 << 10
	for offset := 0; offset < len(value); offset += chunkBytes {
		if err := contextFailure(ctx, "artifact_hash"); err != nil {
			return nil, err
		}
		end := offset + chunkBytes
		if end > len(value) {
			end = len(value)
		}
		_, _ = digest.Write(value[offset:end])
	}
	if err := contextFailure(ctx, "artifact_hash"); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func allocateBytes(count int) (result []byte, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = encodeError(CodeAllocationFailed, "artifact", ErrAllocationFailed)
		}
	}()
	return make([]byte, count), nil
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return encodeError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return encodeError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func maxInt() int { return int(^uint(0) >> 1) }

func validArtifact(artifact *Artifact) bool {
	return artifact != nil && artifact.valid && len(artifact.bytes) >= HeaderBytes
}

func (artifact *Artifact) Bytes() ([]byte, error) {
	if !validArtifact(artifact) {
		return nil, encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	return append([]byte(nil), artifact.bytes...), nil
}

func (artifact *Artifact) Size() uint64 {
	if !validArtifact(artifact) {
		return 0
	}
	return uint64(len(artifact.bytes))
}

func (artifact *Artifact) WriteTo(writer io.Writer) (int64, error) {
	if !validArtifact(artifact) || writer == nil {
		return 0, encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	// Never expose the artifact's backing array to an arbitrary writer. Besides
	// violating Artifact's immutable contract, a writer is otherwise able to
	// retain the slice and mutate the artifact after WriteTo returns.
	const chunkBytes = 64 << 10
	var scratch [chunkBytes]byte
	var total int64
	for offset := 0; offset < len(artifact.bytes); {
		end := offset + chunkBytes
		if end > len(artifact.bytes) {
			end = len(artifact.bytes)
		}
		chunk := scratch[:end-offset]
		copy(chunk, artifact.bytes[offset:end])
		written, err := writer.Write(chunk)
		if written < 0 || written > len(chunk) {
			return total, fmt.Errorf("wavencode: write artifact: invalid count %d: %w", written, errors.Join(io.ErrShortWrite, err))
		}
		total += int64(written)
		if err != nil {
			return total, fmt.Errorf("wavencode: write artifact: %w", err)
		}
		if written != len(chunk) {
			return total, fmt.Errorf("wavencode: write artifact: %w", io.ErrShortWrite)
		}
		offset = end
	}
	return total, nil
}

func (artifact *Artifact) CanonicalHash() (string, error) {
	if !validArtifact(artifact) {
		return "", encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	digest := sha256.Sum256(artifact.bytes)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
