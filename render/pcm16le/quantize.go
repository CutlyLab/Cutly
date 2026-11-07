package pcm16le

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
)

// Block is one immutable, owned, headerless stereo PCM block.
type Block struct {
	valid bool
	bytes []byte
}

// Result binds the immutable PCM bytes to their exact placement and conversion profiles. It is
// render evidence material, not a container, signature, worker attestation or publication proof.
type Result struct {
	Profile                string `json:"profile"`
	InputSampleProfile     string `json:"input_sample_profile"`
	OutputSampleProfile    string `json:"output_sample_profile"`
	SampleRate             int64  `json:"sample_rate"`
	FirstCompositionSample int64  `json:"first_composition_sample"`
	FrameCount             uint64 `json:"frame_count"`
	Channels               int    `json:"channels"`
	ChannelOrder           string `json:"channel_order"`
	BytesPerFrame          int    `json:"bytes_per_frame"`
	PCMHash                string `json:"pcm_hash"`
	PCM                    *Block `json:"-"`
}

// Quantize converts one complete exact stereo block atomically.
func Quantize(
	ctx context.Context,
	sampleRate int64,
	firstCompositionSample int64,
	samples []audiograph.StereoSample,
	limits Limits,
) (Result, error) {
	if ctx == nil {
		return Result{}, quantizeError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return Result{}, err
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return Result{}, err
	}
	if sampleRate <= 0 {
		return Result{}, quantizeError(CodeInputInvalid, "sample_rate", ErrInputInvalid)
	}
	frameCount := uint64(len(samples))
	if frameCount > normalized.MaxFrames || frameCount > HardMaxFrames || frameCount > math.MaxUint64/BytesPerFrame {
		return Result{}, quantizeError(CodeResourceLimit, "samples", ErrResourceLimit)
	}
	sampleRange := videoedit.TickRange{First: firstCompositionSample, Count: int64(frameCount)}
	if err := sampleRange.Validate(); err != nil {
		return Result{}, quantizeError(CodeInputInvalid, "composition_sample_range", errors.Join(ErrInputInvalid, err))
	}
	byteCount := frameCount * BytesPerFrame
	if byteCount > normalized.MaxOutputBytes || byteCount > HardMaxOutputBytes || byteCount > uint64(maxInt()) {
		return Result{}, quantizeError(CodeResourceLimit, "output_bytes", ErrResourceLimit)
	}
	if err := contextFailure(ctx, "allocate"); err != nil {
		return Result{}, err
	}
	encoded, err := allocateBytes(int(byteCount))
	if err != nil {
		return Result{}, err
	}
	var quantizer exactQuantizer
	for index, sample := range samples {
		if index&1023 == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("samples/%d", index)); err != nil {
				return Result{}, err
			}
		}
		left, quantizeErr := quantizer.quantize(sample.Left, normalized.MaxExactBits)
		if quantizeErr != nil {
			return Result{}, quantizeError(classifyExactError(quantizeErr), fmt.Sprintf("samples/%d/left", index), quantizeErr)
		}
		right, quantizeErr := quantizer.quantize(sample.Right, normalized.MaxExactBits)
		if quantizeErr != nil {
			return Result{}, quantizeError(classifyExactError(quantizeErr), fmt.Sprintf("samples/%d/right", index), quantizeErr)
		}
		offset := index * BytesPerFrame
		binary.LittleEndian.PutUint16(encoded[offset:offset+2], uint16(left))
		binary.LittleEndian.PutUint16(encoded[offset+2:offset+4], uint16(right))
	}
	if err := contextFailure(ctx, "hash"); err != nil {
		return Result{}, err
	}
	digest, err := hashBytes(ctx, encoded)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return Result{}, err
	}
	block := &Block{valid: true, bytes: encoded}
	return Result{
		Profile: ProfileV1, InputSampleProfile: audiograph.SampleDomainProfileV1,
		OutputSampleProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		SampleRate:          sampleRate, FirstCompositionSample: firstCompositionSample,
		FrameCount: frameCount, Channels: Channels, ChannelOrder: "FL,FR",
		BytesPerFrame: BytesPerFrame,
		PCMHash:       "sha256:" + hex.EncodeToString(digest), PCM: block,
	}, nil
}

func hashBytes(ctx context.Context, encoded []byte) ([]byte, error) {
	hash := sha256.New()
	const chunkBytes = 64 << 10
	for offset := 0; offset < len(encoded); offset += chunkBytes {
		if err := contextFailure(ctx, "hash"); err != nil {
			return nil, err
		}
		end := offset + chunkBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = hash.Write(encoded[offset:end])
	}
	if err := contextFailure(ctx, "hash"); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if value.MaxFrames == 0 || value.MaxFrames > HardMaxFrames ||
		value.MaxOutputBytes < BytesPerFrame || value.MaxOutputBytes > HardMaxOutputBytes ||
		value.MaxExactBits <= 0 || value.MaxExactBits > HardMaxExactBits {
		return Limits{}, quantizeError(CodeInputInvalid, "limits", ErrInputInvalid)
	}
	return value, nil
}

func classifyExactError(err error) ErrorCode {
	if errors.Is(err, ErrResourceLimit) {
		return CodeResourceLimit
	}
	return CodeInputInvalid
}

// exactQuantizer reuses all arithmetic storage across the block. Exact's accessors still return
// defensive component copies, but no operation below retains them or allocates per arithmetic
// step. A fresh quantizer must not be shared between goroutines.
type exactQuantizer struct {
	negativeDenominator big.Int
	scaled              big.Int
	absolute            big.Int
	quotient            big.Int
	remainder           big.Int
	twiceRemainder      big.Int
}

func (quantizer *exactQuantizer) quantize(value audiograph.Exact, maxBits int) (int16, error) {
	if !value.Valid() {
		return 0, ErrInputInvalid
	}
	numerator := value.Numerator()
	denominator := value.Denominator()
	if numerator.BitLen()+denominator.BitLen() > maxBits {
		return 0, ErrResourceLimit
	}
	quantizer.negativeDenominator.Neg(denominator)
	if numerator.Cmp(&quantizer.negativeDenominator) <= 0 {
		return math.MinInt16, nil
	}
	if numerator.Cmp(denominator) >= 0 {
		return math.MaxInt16, nil
	}
	quantizer.scaled.Lsh(numerator, 15)
	negative := quantizer.scaled.Sign() < 0
	quantizer.absolute.Abs(&quantizer.scaled)
	quantizer.quotient.QuoRem(&quantizer.absolute, denominator, &quantizer.remainder)
	quantizer.twiceRemainder.Lsh(&quantizer.remainder, 1)
	comparison := quantizer.twiceRemainder.Cmp(denominator)
	if comparison > 0 || (comparison == 0 && quantizer.quotient.Bit(0) == 1) {
		quantizer.quotient.Add(&quantizer.quotient, bigOne)
	}
	if negative {
		quantizer.quotient.Neg(&quantizer.quotient)
	}
	result := quantizer.quotient.Int64()
	if result < math.MinInt16 {
		return math.MinInt16, nil
	}
	if result > math.MaxInt16 {
		return math.MaxInt16, nil
	}
	return int16(result), nil
}

var bigOne = big.NewInt(1)

// roundRatioTiesEven is retained as a small independently testable arithmetic primitive. The
// production block path uses exactQuantizer's allocation-reusing equivalent above.
func roundRatioTiesEven(numerator, denominator *big.Int) *big.Int {
	sign := numerator.Sign()
	absNumerator := new(big.Int).Abs(new(big.Int).Set(numerator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(absNumerator, denominator, remainder)
	twiceRemainder := new(big.Int).Lsh(remainder, 1)
	comparison := twiceRemainder.Cmp(denominator)
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if sign < 0 {
		quotient.Neg(quotient)
	}
	return quotient
}

func allocateBytes(count int) (result []byte, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = quantizeError(CodeAllocationFailed, "output_bytes", ErrAllocationFailed)
		}
	}()
	return make([]byte, count), nil
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return quantizeError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return quantizeError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func maxInt() int { return int(^uint(0) >> 1) }

func (block *Block) FrameCount() uint64 {
	if !validBlock(block) {
		return 0
	}
	return uint64(len(block.bytes) / BytesPerFrame)
}

func (block *Block) Sample(frame uint64, channel int) (int16, error) {
	if !validBlock(block) {
		return 0, quantizeError(CodeOutputInvalid, "pcm", ErrOutputInvalid)
	}
	if frame >= block.FrameCount() || channel < 0 || channel >= Channels {
		return 0, quantizeError(CodeBounds, "pcm.sample", ErrBounds)
	}
	offset := int(frame)*BytesPerFrame + channel*BytesPerSample
	return int16(binary.LittleEndian.Uint16(block.bytes[offset : offset+2])), nil
}

// Bytes returns a defensive copy of the complete canonical PCM bytes.
func (block *Block) Bytes() ([]byte, error) {
	if !validBlock(block) {
		return nil, quantizeError(CodeOutputInvalid, "pcm", ErrOutputInvalid)
	}
	return append([]byte(nil), block.bytes...), nil
}

func (block *Block) WriteCanonical(writer io.Writer) error {
	if !validBlock(block) || writer == nil {
		return quantizeError(CodeOutputInvalid, "pcm", ErrOutputInvalid)
	}
	const chunkBytes = 64 << 10
	var scratch [chunkBytes]byte
	for offset := 0; offset < len(block.bytes); offset += chunkBytes {
		end := offset + chunkBytes
		if end > len(block.bytes) {
			end = len(block.bytes)
		}
		chunk := scratch[:end-offset]
		copy(chunk, block.bytes[offset:end])
		written, err := writer.Write(chunk)
		if written < 0 || written > len(chunk) {
			return fmt.Errorf("pcm16le: write canonical bytes at %d: %w", offset, io.ErrShortWrite)
		}
		if err != nil {
			return fmt.Errorf("pcm16le: write canonical bytes at %d: %w", offset, err)
		}
		if written != len(chunk) {
			return fmt.Errorf("pcm16le: write canonical bytes at %d: %w", offset, io.ErrShortWrite)
		}
	}
	return nil
}

func (block *Block) CanonicalHash() (string, error) {
	if !validBlock(block) {
		return "", quantizeError(CodeOutputInvalid, "pcm", ErrOutputInvalid)
	}
	digest := sha256.Sum256(block.bytes)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validBlock(block *Block) bool {
	return block != nil && block.valid && len(block.bytes)%BytesPerFrame == 0
}
