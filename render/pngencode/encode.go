package pngencode

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"strings"

	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type Artifact struct {
	valid bool
	bytes []byte
}

type Result struct {
	Profile                string    `json:"profile"`
	InputColorProfile      string    `json:"input_color_profile"`
	InputSurfaceProfile    string    `json:"input_surface_profile"`
	InputPixelHashProfile  string    `json:"input_pixel_hash_profile"`
	FilterProfile          string    `json:"filter_profile"`
	DeflateProfile         string    `json:"deflate_profile"`
	RoundTripDecodeProfile string    `json:"round_trip_decode_profile"`
	ArtifactHashProfile    string    `json:"artifact_hash_profile"`
	Width                  int       `json:"width"`
	Height                 int       `json:"height"`
	Stride                 int       `json:"stride"`
	SRGBRenderingIntent    uint8     `json:"srgb_rendering_intent"`
	RawRGBABytes           uint64    `json:"raw_rgba_bytes"`
	ScanlineBytes          uint64    `json:"scanline_bytes"`
	StoredBlockCount       uint64    `json:"stored_block_count"`
	IDATBytes              uint64    `json:"idat_bytes"`
	ArtifactBytes          uint64    `json:"artifact_bytes"`
	WorkingBytes           uint64    `json:"working_bytes"`
	SourcePixelHash        string    `json:"source_pixel_hash"`
	ArtifactHash           string    `json:"artifact_hash"`
	Artifact               *Artifact `json:"-"`
}

// Encode serializes one immutable color-output frame into the strict deterministic PNG profile.
// It returns a zero Result on every failure.
func Encode(ctx context.Context, input color.OutputResult, limits Limits) (Result, error) {
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
	if input.Frame == nil || input.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		input.OutputProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		input.OutputMetadata != color.MetadataRGBA8SRGBOutputV1() ||
		input.HashProfile != color.OutputRGBAHashProfileV1 || !canonicalSHA256(input.PixelHash) {
		return Result{}, encodeError(CodeInputInvalid, "input", ErrInputInvalid)
	}
	view, err := input.Frame.View()
	if err != nil {
		return Result{}, encodeError(CodeInputInvalid, "input/frame", errors.Join(ErrInputInvalid, err))
	}
	if input.Width != view.Width() || input.Height != view.Height() || input.Stride != view.Stride() {
		return Result{}, encodeError(CodeInputMismatch, "input/geometry", ErrInputMismatch)
	}
	frameHash, err := input.Frame.PixelHash()
	if err != nil {
		return Result{}, encodeError(CodeInputInvalid, "input/frame/hash", errors.Join(ErrInputInvalid, err))
	}
	if !canonicalSHA256(frameHash) || frameHash != input.PixelHash {
		return Result{}, encodeError(CodeInputMismatch, "input/pixel_hash", ErrInputMismatch)
	}
	layout, err := checkedLayout(input.Width, input.Height, input.Stride, normalized)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "allocate"); err != nil {
		return Result{}, err
	}
	encoded, err := allocateBytes(int(layout.artifactBytes), "artifact")
	if err != nil {
		return Result{}, err
	}
	row, err := allocateBytes(layout.stride, "row")
	if err != nil {
		return Result{}, err
	}

	offset := 0
	copy(encoded[offset:], pngSignature[:])
	offset += len(pngSignature)
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(layout.width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(layout.height))
	ihdr[8], ihdr[9] = 8, 6
	// compression, filter, interlace remain canonical zero.
	offset = writeChunk(encoded, offset, "IHDR", ihdr[:])
	offset = writeChunk(encoded, offset, "sRGB", []byte{SRGBRenderingIntent})

	idatChunkOffset := offset
	binary.BigEndian.PutUint32(encoded[offset:offset+4], uint32(layout.idatBytes))
	copy(encoded[offset+4:offset+8], "IDAT")
	idatStart := offset + 8
	idatEnd := idatStart + int(layout.idatBytes)
	stream, err := newStoredZlibWriter(ctx, encoded[idatStart:idatEnd], layout.scanlineBytes)
	if err != nil {
		return Result{}, err
	}
	pixelDigest := sha256.New()
	var filterNone [1]byte
	for y := 0; y < layout.height; y++ {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, encodeError(CodeCanceled, fmt.Sprintf("rows/%d", y), errors.Join(ErrCanceled, contextErr))
		}
		if err := view.CopyRow(y, row); err != nil {
			return Result{}, encodeError(CodeInputInvalid, fmt.Sprintf("input/rows/%d", y), errors.Join(ErrInputInvalid, err))
		}
		if err := hashWithContext(ctx, pixelDigest, row, "input/pixel_hash"); err != nil {
			return Result{}, err
		}
		if err := stream.Write(filterNone[:]); err != nil {
			return Result{}, err
		}
		if err := stream.Write(row); err != nil {
			return Result{}, err
		}
	}
	if err := stream.Finish(); err != nil {
		return Result{}, err
	}
	actualPixelHash := "sha256:" + hex.EncodeToString(pixelDigest.Sum(nil))
	if actualPixelHash != input.PixelHash || actualPixelHash != frameHash {
		return Result{}, encodeError(CodeInputMismatch, "input/pixel_hash", ErrInputMismatch)
	}
	idatCRC, err := checksumIEEEWithContext(ctx, encoded[idatChunkOffset+4:idatEnd], "IDAT/crc")
	if err != nil {
		return Result{}, err
	}
	binary.BigEndian.PutUint32(encoded[idatEnd:idatEnd+4], idatCRC)
	offset = idatEnd + 4
	offset = writeChunk(encoded, offset, "IEND", nil)
	if offset != len(encoded) {
		return Result{}, encodeError(CodeInputMismatch, "artifact/layout", ErrInputMismatch)
	}
	artifactDigest, err := hashBytes(ctx, encoded, "artifact_hash")
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return Result{}, err
	}
	artifactHash := "sha256:" + hex.EncodeToString(artifactDigest)
	artifact := &Artifact{valid: true, bytes: encoded}
	return Result{
		Profile: ProfileV1, InputColorProfile: color.ProfileLinearRGBA16ToRGBA8SRGBV1,
		InputSurfaceProfile:   cpucompositor.SurfaceProfileV1,
		InputPixelHashProfile: color.OutputRGBAHashProfileV1,
		FilterProfile:         FilterProfileV1, DeflateProfile: DeflateProfileV1,
		RoundTripDecodeProfile: pngdecode.ProfileV1, ArtifactHashProfile: pngdecode.ContentHashProfileV1,
		Width: layout.width, Height: layout.height, Stride: layout.stride,
		SRGBRenderingIntent: SRGBRenderingIntent, RawRGBABytes: layout.rawRGBABytes,
		ScanlineBytes: layout.scanlineBytes, StoredBlockCount: layout.blockCount,
		IDATBytes: layout.idatBytes, ArtifactBytes: layout.artifactBytes,
		WorkingBytes:    layout.workingBytes,
		SourcePixelHash: actualPixelHash, ArtifactHash: artifactHash, Artifact: artifact,
	}, nil
}

type layout struct {
	width, height int
	stride        int
	rawRGBABytes  uint64
	scanlineBytes uint64
	blockCount    uint64
	idatBytes     uint64
	artifactBytes uint64
	workingBytes  uint64
}

func checkedLayout(width, height, stride int, limits Limits) (layout, error) {
	if width <= 0 || height <= 0 || stride <= 0 {
		return layout{}, encodeError(CodeInputInvalid, "input/geometry", ErrInputInvalid)
	}
	if width > limits.MaxWidth || height > limits.MaxHeight || width > HardMaxDimension || height > HardMaxDimension {
		return layout{}, encodeError(CodeResourceLimit, "input/dimensions", ErrResourceLimit)
	}
	if width > math.MaxInt32 || height > math.MaxInt32 || width > maxInt()/int(color.BytesPerOutputRGBA8Pixel) {
		return layout{}, encodeError(CodeResourceLimit, "input/dimensions", ErrResourceLimit)
	}
	expectedStride := width * int(color.BytesPerOutputRGBA8Pixel)
	if stride != expectedStride {
		return layout{}, encodeError(CodeInputMismatch, "input/stride", ErrInputMismatch)
	}
	unsignedWidth, unsignedHeight := uint64(width), uint64(height)
	if unsignedHeight != 0 && unsignedWidth > math.MaxUint64/unsignedHeight {
		return layout{}, encodeError(CodeResourceLimit, "input/pixel_count", ErrResourceLimit)
	}
	pixelCount := unsignedWidth * unsignedHeight
	if pixelCount > math.MaxUint64/color.BytesPerOutputRGBA8Pixel {
		return layout{}, encodeError(CodeResourceLimit, "input/raw_rgba_bytes", ErrResourceLimit)
	}
	rawRGBABytes := pixelCount * color.BytesPerOutputRGBA8Pixel
	if rawRGBABytes > limits.MaxRawRGBABytes || rawRGBABytes > HardMaxRawRGBABytes {
		return layout{}, encodeError(CodeResourceLimit, "input/raw_rgba_bytes", ErrResourceLimit)
	}
	if rawRGBABytes > math.MaxUint64-unsignedHeight {
		return layout{}, encodeError(CodeResourceLimit, "scanline_bytes", ErrResourceLimit)
	}
	scanlineBytes := rawRGBABytes + unsignedHeight
	blockCount, idatBytes, err := checkedStoredLayout(scanlineBytes)
	if err != nil {
		return layout{}, err
	}
	if idatBytes > math.MaxUint64-FixedArtifactBytes {
		return layout{}, encodeError(CodeResourceLimit, "artifact_bytes", ErrResourceLimit)
	}
	artifactBytes := idatBytes + FixedArtifactBytes
	if artifactBytes > limits.MaxArtifactBytes || artifactBytes > HardMaxArtifactBytes || artifactBytes > uint64(maxInt()) {
		return layout{}, encodeError(CodeResourceLimit, "artifact_bytes", ErrResourceLimit)
	}
	if artifactBytes > math.MaxUint64-uint64(expectedStride) {
		return layout{}, encodeError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	workingBytes := artifactBytes + uint64(expectedStride)
	if workingBytes > limits.MaxWorkingBytes || workingBytes > HardMaxWorkingBytes {
		return layout{}, encodeError(CodeResourceLimit, "working_bytes", ErrResourceLimit)
	}
	return layout{
		width: width, height: height, stride: expectedStride,
		rawRGBABytes: rawRGBABytes, scanlineBytes: scanlineBytes,
		blockCount: blockCount, idatBytes: idatBytes, artifactBytes: artifactBytes,
		workingBytes: workingBytes,
	}, nil
}

func checkedStoredLayout(scanlineBytes uint64) (uint64, uint64, error) {
	if scanlineBytes == 0 {
		return 0, 0, encodeError(CodeInputInvalid, "scanline_bytes", ErrInputInvalid)
	}
	blockCount := scanlineBytes / StoredBlockMaxBytes
	if scanlineBytes%StoredBlockMaxBytes != 0 {
		blockCount++
	}
	if scanlineBytes > math.MaxUint64-ZlibFixedBytes ||
		blockCount > (math.MaxUint64-ZlibFixedBytes-scanlineBytes)/5 {
		return 0, 0, encodeError(CodeResourceLimit, "idat_bytes", ErrResourceLimit)
	}
	idatBytes := ZlibFixedBytes + scanlineBytes + blockCount*5
	if idatBytes > HardMaxIDATBytes {
		return 0, 0, encodeError(CodeResourceLimit, "idat_bytes", ErrResourceLimit)
	}
	return blockCount, idatBytes, nil
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxWidth == 0 {
		value.MaxWidth = defaults.MaxWidth
	}
	if value.MaxHeight == 0 {
		value.MaxHeight = defaults.MaxHeight
	}
	if value.MaxRawRGBABytes == 0 {
		value.MaxRawRGBABytes = defaults.MaxRawRGBABytes
	}
	if value.MaxArtifactBytes == 0 {
		value.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if value.MaxWidth <= 0 || value.MaxHeight <= 0 || value.MaxWidth > HardMaxDimension || value.MaxHeight > HardMaxDimension ||
		value.MaxRawRGBABytes < color.BytesPerOutputRGBA8Pixel || value.MaxRawRGBABytes > HardMaxRawRGBABytes ||
		value.MaxArtifactBytes < MinimumArtifactBytes || value.MaxArtifactBytes > HardMaxArtifactBytes ||
		value.MaxWorkingBytes < MinimumWorkingBytes || value.MaxWorkingBytes > HardMaxWorkingBytes {
		return Limits{}, encodeError(CodeResourceLimit, "limits", ErrResourceLimit)
	}
	return value, nil
}

type storedZlibWriter struct {
	ctx            context.Context
	destination    []byte
	offset         int
	rawRemaining   uint64
	blockRemaining int
	adler          uint32
	adlerA         uint32
	adlerB         uint32
	finished       bool
}

func newStoredZlibWriter(ctx context.Context, destination []byte, rawBytes uint64) (*storedZlibWriter, error) {
	if ctx == nil || rawBytes == 0 || len(destination) < int(ZlibFixedBytes+5) {
		return nil, encodeError(CodeInputInvalid, "zlib", ErrInputInvalid)
	}
	destination[0], destination[1] = 0x78, 0x01
	return &storedZlibWriter{
		ctx: ctx, destination: destination, offset: 2, rawRemaining: rawBytes,
		adlerA: 1,
	}, nil
}

func (writer *storedZlibWriter) startBlock() error {
	if writer == nil || writer.finished || writer.rawRemaining == 0 || writer.blockRemaining != 0 {
		return encodeError(CodeInputMismatch, "zlib/block", ErrInputMismatch)
	}
	if err := contextFailure(writer.ctx, "zlib/block"); err != nil {
		return err
	}
	blockLength := uint64(StoredBlockMaxBytes)
	if writer.rawRemaining < blockLength {
		blockLength = writer.rawRemaining
	}
	if writer.offset > len(writer.destination)-5 {
		return encodeError(CodeInputMismatch, "zlib/layout", ErrInputMismatch)
	}
	if blockLength == writer.rawRemaining {
		writer.destination[writer.offset] = 0x01
	} else {
		writer.destination[writer.offset] = 0x00
	}
	binary.LittleEndian.PutUint16(writer.destination[writer.offset+1:writer.offset+3], uint16(blockLength))
	binary.LittleEndian.PutUint16(writer.destination[writer.offset+3:writer.offset+5], ^uint16(blockLength))
	writer.offset += 5
	writer.blockRemaining = int(blockLength)
	return nil
}

func (writer *storedZlibWriter) Write(value []byte) error {
	if writer == nil || writer.finished || uint64(len(value)) > writer.rawRemaining {
		return encodeError(CodeInputMismatch, "zlib/input", ErrInputMismatch)
	}
	for len(value) > 0 {
		if writer.blockRemaining == 0 {
			if err := writer.startBlock(); err != nil {
				return err
			}
		}
		count := len(value)
		if count > writer.blockRemaining {
			count = writer.blockRemaining
		}
		if writer.offset > len(writer.destination)-count {
			return encodeError(CodeInputMismatch, "zlib/layout", ErrInputMismatch)
		}
		chunk := value[:count]
		copy(writer.destination[writer.offset:writer.offset+count], chunk)
		writer.updateAdler(chunk)
		writer.offset += count
		writer.blockRemaining -= count
		writer.rawRemaining -= uint64(count)
		value = value[count:]
	}
	return nil
}

func (writer *storedZlibWriter) updateAdler(value []byte) {
	// Keep the state locally so the byte contract does not depend on hash.Hash write behavior.
	const modulus = 65521
	for len(value) > 0 {
		count := len(value)
		if count > 5552 {
			count = 5552
		}
		for _, current := range value[:count] {
			writer.adlerA += uint32(current)
			writer.adlerB += writer.adlerA
		}
		writer.adlerA %= modulus
		writer.adlerB %= modulus
		value = value[count:]
	}
}

func (writer *storedZlibWriter) Finish() error {
	if writer == nil || writer.finished || writer.rawRemaining != 0 || writer.blockRemaining != 0 ||
		writer.offset > len(writer.destination)-4 {
		return encodeError(CodeInputMismatch, "zlib/finish", ErrInputMismatch)
	}
	if err := contextFailure(writer.ctx, "zlib/finish"); err != nil {
		return err
	}
	writer.adler = writer.adlerB<<16 | writer.adlerA
	binary.BigEndian.PutUint32(writer.destination[writer.offset:writer.offset+4], writer.adler)
	writer.offset += 4
	writer.finished = true
	if writer.offset != len(writer.destination) {
		return encodeError(CodeInputMismatch, "zlib/layout", ErrInputMismatch)
	}
	return nil
}

func writeChunk(destination []byte, offset int, chunkType string, data []byte) int {
	binary.BigEndian.PutUint32(destination[offset:offset+4], uint32(len(data)))
	copy(destination[offset+4:offset+8], chunkType)
	copy(destination[offset+8:offset+8+len(data)], data)
	end := offset + 8 + len(data)
	binary.BigEndian.PutUint32(destination[end:end+4], crc32.ChecksumIEEE(destination[offset+4:end]))
	return end + 4
}

func hashWithContext(ctx context.Context, digest io.Writer, value []byte, path string) error {
	for offset := 0; offset < len(value); {
		if err := contextFailure(ctx, path); err != nil {
			return err
		}
		end := offset + ArtifactWriteChunkBytes
		if end > len(value) {
			end = len(value)
		}
		written, err := digest.Write(value[offset:end])
		if err != nil || written != end-offset {
			return encodeError(CodeInputMismatch, path, errors.Join(ErrInputMismatch, err, io.ErrShortWrite))
		}
		offset = end
	}
	return nil
}

func hashBytes(ctx context.Context, value []byte, path string) ([]byte, error) {
	digest := sha256.New()
	if err := hashWithContext(ctx, digest, value, path); err != nil {
		return nil, err
	}
	if err := contextFailure(ctx, path); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func checksumIEEEWithContext(ctx context.Context, value []byte, path string) (uint32, error) {
	var checksum uint32
	for offset := 0; offset < len(value); {
		if err := contextFailure(ctx, path); err != nil {
			return 0, err
		}
		end := offset + ArtifactWriteChunkBytes
		if end > len(value) {
			end = len(value)
		}
		checksum = crc32.Update(checksum, crc32.IEEETable, value[offset:end])
		offset = end
	}
	if err := contextFailure(ctx, path); err != nil {
		return 0, err
	}
	return checksum, nil
}

func allocateBytes(count int, path string) (result []byte, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = encodeError(CodeAllocationFailed, path, ErrAllocationFailed)
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

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func maxInt() int { return int(^uint(0) >> 1) }

func validArtifact(artifact *Artifact) bool {
	return artifact != nil && artifact.valid && len(artifact.bytes) >= int(MinimumArtifactBytes) &&
		len(artifact.bytes) <= maxInt()
}

func (artifact *Artifact) Bytes() ([]byte, error) {
	if !validArtifact(artifact) {
		return nil, encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	result, err := allocateBytes(len(artifact.bytes), "artifact/bytes")
	if err != nil {
		return nil, err
	}
	copy(result, artifact.bytes)
	return result, nil
}

func (artifact *Artifact) Size() uint64 {
	if !validArtifact(artifact) {
		return 0
	}
	return uint64(len(artifact.bytes))
}

func (artifact *Artifact) CanonicalHash() (string, error) {
	if !validArtifact(artifact) {
		return "", encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	digest := sha256.Sum256(artifact.bytes)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (artifact *Artifact) WriteTo(writer io.Writer) (int64, error) {
	if !validArtifact(artifact) || writer == nil {
		return 0, encodeError(CodeArtifactInvalid, "artifact", ErrArtifactInvalid)
	}
	var scratch [ArtifactWriteChunkBytes]byte
	var total int64
	for offset := 0; offset < len(artifact.bytes); {
		end := offset + len(scratch)
		if end > len(artifact.bytes) {
			end = len(artifact.bytes)
		}
		chunk := scratch[:end-offset]
		copy(chunk, artifact.bytes[offset:end])
		written, err := writer.Write(chunk)
		if written < 0 || written > len(chunk) {
			return total, fmt.Errorf("pngencode: write artifact: invalid count %d: %w", written, errors.Join(io.ErrShortWrite, err))
		}
		total += int64(written)
		if err != nil {
			return total, fmt.Errorf("pngencode: write artifact: %w", err)
		}
		if written != len(chunk) {
			return total, fmt.Errorf("pngencode: write artifact: %w", io.ErrShortWrite)
		}
		offset = end
	}
	return total, nil
}
