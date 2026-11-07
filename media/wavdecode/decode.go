package wavdecode

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	minimumEncodedBytes = 44
	contextStrideBytes  = 32 << 10
)

// PCM16 is an immutable, owned frame-major interleaved sample buffer. It exposes samples only by
// value and returns a copy of channel-order evidence, making a successful decode safe for
// concurrent reads.
type PCM16 struct {
	sampleRate uint32
	channels   uint16
	samples    []int16
}

// Result binds the encoded resource and decoded canonical sample bytes to the exact decoder
// profile. SampleHash intentionally excludes rate and channel order; Result carries those fields
// so an enclosing execution manifest can bind the complete claim.
type Result struct {
	SampleRate          uint32       `json:"sample_rate"`
	Channels            uint16       `json:"channels"`
	ChannelLayout       string       `json:"channel_layout"`
	DecoderChannelOrder ChannelOrder `json:"decoder_channel_order"`
	FrameCount          uint64       `json:"frame_count"`
	SampleCount         uint64       `json:"sample_count"`
	DataBytes           uint64       `json:"data_bytes"`
	EncodedContentHash  string       `json:"encoded_content_hash"`
	SampleHash          string       `json:"sample_hash"`
	DecodeProfile       string       `json:"decode_profile"`
	SampleDomain        string       `json:"sample_domain"`
	ContentHashProfile  string       `json:"content_hash_profile"`
	SampleHashProfile   string       `json:"sample_hash_profile"`
	PCM                 *PCM16       `json:"-"`
}

// Decode validates and decodes one complete strict WAV resource. Encoded is borrowed only for the
// call and must remain immutable until return.
func Decode(ctx context.Context, encoded []byte, limits Limits) (Result, error) {
	if ctx == nil {
		return Result{}, decodeError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return Result{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return Result{}, err
	}
	metadata, err := validateEnvelope(ctx, encoded, normalized)
	if err != nil {
		return Result{}, err
	}
	encodedHash, err := hashEncoded(ctx, encoded)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "samples/allocation"); err != nil {
		return Result{}, err
	}
	samples, err := allocateSamples(int(metadata.sampleCount))
	if err != nil {
		return Result{}, err
	}
	if err := decodeSamples(ctx, encoded[metadata.dataStart:metadata.dataEnd], samples); err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "samples/hash"); err != nil {
		return Result{}, err
	}
	sampleHash, err := hashSamples(ctx, samples)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return Result{}, err
	}
	buffer := &PCM16{sampleRate: metadata.sampleRate, channels: metadata.channels, samples: samples}
	return Result{
		SampleRate: metadata.sampleRate, Channels: metadata.channels,
		ChannelLayout: channelLayout(metadata.channels), DecoderChannelOrder: channelOrder(metadata.channels),
		FrameCount:  metadata.frameCount,
		SampleCount: metadata.sampleCount, DataBytes: metadata.dataBytes,
		EncodedContentHash: encodedHash, SampleHash: sampleHash,
		DecodeProfile: ProfileV1, SampleDomain: SampleDomainProfileV1,
		ContentHashProfile: ContentHashProfileV1, SampleHashProfile: SampleHashProfileV1,
		PCM: buffer,
	}, nil
}

type envelope struct {
	sampleRate  uint32
	channels    uint16
	frameCount  uint64
	sampleCount uint64
	dataBytes   uint64
	dataStart   int
	dataEnd     int
}

func validateEnvelope(ctx context.Context, encoded []byte, limits Limits) (envelope, error) {
	if uint64(len(encoded)) > limits.MaxEncodedBytes {
		return envelope{}, decodeError(CodeResourceLimit, "encoded_bytes", ErrResourceLimit)
	}
	if len(encoded) < minimumEncodedBytes {
		return envelope{}, decodeError(CodeInputInvalid, "RIFF", ErrInputInvalid)
	}
	if string(encoded[0:4]) != "RIFF" {
		return envelope{}, decodeError(CodeInputInvalid, "RIFF/id", ErrInputInvalid)
	}
	if string(encoded[8:12]) != "WAVE" {
		return envelope{}, decodeError(CodeInputInvalid, "RIFF/form_type", ErrInputInvalid)
	}
	declaredRIFFBytes := uint64(binary.LittleEndian.Uint32(encoded[4:8]))
	if declaredRIFFBytes != uint64(len(encoded)-8) {
		return envelope{}, decodeError(CodeChunkInvalid, "RIFF/size", ErrChunkInvalid)
	}
	if err := contextFailure(ctx, "chunks/0"); err != nil {
		return envelope{}, err
	}
	fmtChunk, err := parseChunk(encoded, 12, "chunks/0")
	if err != nil {
		return envelope{}, err
	}
	if fmtChunk.id != "fmt " {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/id", ErrProfileMismatch)
	}
	if fmtChunk.size != 16 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/size", ErrProfileMismatch)
	}
	format := binary.LittleEndian.Uint16(encoded[fmtChunk.dataStart : fmtChunk.dataStart+2])
	channels := binary.LittleEndian.Uint16(encoded[fmtChunk.dataStart+2 : fmtChunk.dataStart+4])
	sampleRate := binary.LittleEndian.Uint32(encoded[fmtChunk.dataStart+4 : fmtChunk.dataStart+8])
	byteRate := binary.LittleEndian.Uint32(encoded[fmtChunk.dataStart+8 : fmtChunk.dataStart+12])
	blockAlign := binary.LittleEndian.Uint16(encoded[fmtChunk.dataStart+12 : fmtChunk.dataStart+14])
	bitsPerSample := binary.LittleEndian.Uint16(encoded[fmtChunk.dataStart+14 : fmtChunk.dataStart+16])
	if format != 1 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/audio_format", ErrProfileMismatch)
	}
	if channels != 1 && channels != 2 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/channels", ErrProfileMismatch)
	}
	if sampleRate == 0 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/sample_rate", ErrProfileMismatch)
	}
	if sampleRate > limits.MaxSampleRate {
		return envelope{}, decodeError(CodeResourceLimit, "chunks/0/fmt/sample_rate", ErrResourceLimit)
	}
	if bitsPerSample != 16 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/bits_per_sample", ErrProfileMismatch)
	}
	expectedBlockAlign := channels * 2
	if blockAlign != expectedBlockAlign {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/block_align", ErrProfileMismatch)
	}
	expectedByteRate := uint64(sampleRate) * uint64(expectedBlockAlign)
	if expectedByteRate > math.MaxUint32 || byteRate != uint32(expectedByteRate) {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/0/fmt/byte_rate", ErrProfileMismatch)
	}

	if err := contextFailure(ctx, "chunks/1"); err != nil {
		return envelope{}, err
	}
	dataChunk, err := parseChunk(encoded, fmtChunk.next, "chunks/1")
	if err != nil {
		return envelope{}, err
	}
	if dataChunk.id != "data" {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/1/id", ErrProfileMismatch)
	}
	if dataChunk.next != len(encoded) {
		if len(encoded)-dataChunk.next >= 8 {
			return envelope{}, decodeError(CodeProfileMismatch, "chunks/2", ErrProfileMismatch)
		}
		return envelope{}, decodeError(CodeChunkInvalid, "chunks/1/trailing_bytes", ErrChunkInvalid)
	}
	dataBytes := uint64(dataChunk.size)
	if dataBytes%uint64(blockAlign) != 0 {
		return envelope{}, decodeError(CodeProfileMismatch, "chunks/1/data/frame_alignment", ErrProfileMismatch)
	}
	frameCount := dataBytes / uint64(blockAlign)
	if frameCount > limits.MaxFrames {
		return envelope{}, decodeError(CodeResourceLimit, "chunks/1/data/frame_count", ErrResourceLimit)
	}
	if dataBytes > limits.MaxWorkingBytes {
		return envelope{}, decodeError(CodeResourceLimit, "chunks/1/data/working_bytes", ErrResourceLimit)
	}
	sampleCount := dataBytes / 2
	if sampleCount > uint64(maxInt()) {
		return envelope{}, decodeError(CodeResourceLimit, "chunks/1/data/sample_count", ErrResourceLimit)
	}
	return envelope{
		sampleRate: sampleRate, channels: channels,
		frameCount: frameCount, sampleCount: sampleCount, dataBytes: dataBytes,
		dataStart: dataChunk.dataStart, dataEnd: dataChunk.dataEnd,
	}, nil
}

type chunk struct {
	id        string
	size      uint32
	dataStart int
	dataEnd   int
	next      int
}

func parseChunk(encoded []byte, offset int, path string) (chunk, error) {
	if offset < 0 || offset > len(encoded) || len(encoded)-offset < 8 {
		return chunk{}, decodeError(CodeChunkInvalid, path, ErrChunkInvalid)
	}
	size := binary.LittleEndian.Uint32(encoded[offset+4 : offset+8])
	dataStart := uint64(offset) + 8
	dataEnd := dataStart + uint64(size)
	next := dataEnd + uint64(size&1)
	if dataEnd > uint64(len(encoded)) || next > uint64(len(encoded)) || next > uint64(maxInt()) {
		return chunk{}, decodeError(CodeChunkInvalid, path+"/size", ErrChunkInvalid)
	}
	if size&1 != 0 && encoded[int(dataEnd)] != 0 {
		return chunk{}, decodeError(CodeChunkInvalid, path+"/padding", ErrChunkInvalid)
	}
	return chunk{
		id: string(encoded[offset : offset+4]), size: size,
		dataStart: int(dataStart), dataEnd: int(dataEnd), next: int(next),
	}, nil
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxSampleRate == 0 {
		value.MaxSampleRate = defaults.MaxSampleRate
	}
	if value.MaxFrames == 0 {
		value.MaxFrames = defaults.MaxFrames
	}
	if value.MaxEncodedBytes == 0 {
		value.MaxEncodedBytes = defaults.MaxEncodedBytes
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if value.MaxSampleRate > HardMaxSampleRate || value.MaxFrames > HardMaxFrames ||
		value.MaxEncodedBytes < minimumEncodedBytes || value.MaxEncodedBytes > HardMaxEncodedBytes ||
		value.MaxWorkingBytes > HardMaxWorkingBytes {
		return Limits{}, decodeError(CodeResourceLimit, "limits", ErrResourceLimit)
	}
	return value, nil
}

func allocateSamples(count int) (samples []int16, err error) {
	defer func() {
		if recover() != nil {
			samples = nil
			err = decodeError(CodeAllocationFailed, "samples", ErrAllocationFailed)
		}
	}()
	return make([]int16, count), nil
}

func decodeSamples(ctx context.Context, encoded []byte, samples []int16) error {
	if len(encoded) != len(samples)*2 {
		return decodeError(CodeChunkInvalid, "samples/length", ErrChunkInvalid)
	}
	const samplesPerCheck = contextStrideBytes / 2
	for index := range samples {
		if index%samplesPerCheck == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("samples/%d", index)); err != nil {
				return err
			}
		}
		samples[index] = int16(binary.LittleEndian.Uint16(encoded[index*2 : index*2+2]))
	}
	return contextFailure(ctx, "samples")
}

func hashEncoded(ctx context.Context, encoded []byte) (string, error) {
	digest := sha256.New()
	for offset := 0; offset < len(encoded); offset += contextStrideBytes {
		if err := contextFailure(ctx, "encoded_hash"); err != nil {
			return "", err
		}
		end := offset + contextStrideBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = digest.Write(encoded[offset:end])
	}
	if err := contextFailure(ctx, "encoded_hash"); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func hashSamples(ctx context.Context, samples []int16) (string, error) {
	digest := sha256.New()
	if err := writeCanonical(ctx, digest, samples); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeCanonical(ctx context.Context, writer io.Writer, samples []int16) error {
	if writer == nil {
		return decodeError(CodeInputInvalid, "writer", ErrInputInvalid)
	}
	const sampleBatch = contextStrideBytes / 2
	var buffer [contextStrideBytes]byte
	for offset := 0; offset < len(samples); offset += sampleBatch {
		if ctx != nil {
			if err := contextFailure(ctx, "sample_hash"); err != nil {
				return err
			}
		}
		end := offset + sampleBatch
		if end > len(samples) {
			end = len(samples)
		}
		encoded := buffer[:(end-offset)*2]
		for index, sample := range samples[offset:end] {
			binary.BigEndian.PutUint16(encoded[index*2:index*2+2], uint16(sample))
		}
		written, err := writer.Write(encoded)
		if err != nil {
			return fmt.Errorf("wavdecode: write canonical samples at %d: %w", offset, err)
		}
		if written != len(encoded) {
			return fmt.Errorf("wavdecode: write canonical samples at %d: %w", offset, io.ErrShortWrite)
		}
	}
	if ctx != nil {
		return contextFailure(ctx, "sample_hash")
	}
	return nil
}

func contextFailure(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return decodeError(CodeCanceled, path, errors.Join(ErrCanceled, ctx.Err()))
	default:
		return nil
	}
}

func maxInt() int { return int(^uint(0) >> 1) }

func channelLayout(channels uint16) string {
	if channels == 1 {
		return "mono"
	}
	return "stereo"
}

func channelOrder(channels uint16) ChannelOrder {
	if channels == 1 {
		return ChannelOrderMonoFC
	}
	if channels == 2 {
		return ChannelOrderStereoFLFR
	}
	return ""
}

func (buffer *PCM16) SampleRate() uint32 {
	if buffer == nil {
		return 0
	}
	return buffer.sampleRate
}

func (buffer *PCM16) Channels() uint16 {
	if buffer == nil {
		return 0
	}
	return buffer.channels
}

func (buffer *PCM16) ChannelLayout() string {
	if buffer == nil {
		return ""
	}
	return channelLayout(buffer.channels)
}

// ChannelOrder returns fresh semantic evidence for the profile-defined decoder order.
func (buffer *PCM16) ChannelOrder() []string {
	if buffer == nil {
		return nil
	}
	if buffer.channels == 1 {
		return []string{"FC"}
	}
	if buffer.channels == 2 {
		return []string{"FL", "FR"}
	}
	return nil
}

func (buffer *PCM16) FrameCount() uint64 {
	if !validPCM(buffer) {
		return 0
	}
	return uint64(len(buffer.samples)) / uint64(buffer.channels)
}

// Sample returns one signed sample by value.
func (buffer *PCM16) Sample(frame uint64, channel int) (int16, error) {
	if !validPCM(buffer) || channel < 0 || channel >= int(buffer.channels) || frame >= buffer.FrameCount() {
		return 0, decodeError(CodeBounds, "sample", ErrBounds)
	}
	return buffer.samples[int(frame)*int(buffer.channels)+channel], nil
}

// WriteCanonical serializes samples only as signed two's-complement big-endian int16 words in
// frame-major interleaved channel order. Rate and channel order are bound separately by Result.
func (buffer *PCM16) WriteCanonical(writer io.Writer) error {
	if !validPCM(buffer) {
		return decodeError(CodeInputInvalid, "pcm", ErrInputInvalid)
	}
	return writeCanonical(nil, writer, buffer.samples)
}

func (buffer *PCM16) CanonicalHash() (string, error) {
	if !validPCM(buffer) {
		return "", decodeError(CodeInputInvalid, "pcm", ErrInputInvalid)
	}
	digest := sha256.New()
	if err := buffer.WriteCanonical(digest); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func validPCM(buffer *PCM16) bool {
	return buffer != nil && buffer.sampleRate > 0 && (buffer.channels == 1 || buffer.channels == 2) && len(buffer.samples)%int(buffer.channels) == 0
}
