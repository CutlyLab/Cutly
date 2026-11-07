package pngdecode

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"

	cutlycolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

const maxContextRead = 32 << 10

// Result binds the immutable encoded bytes and the normalized pixel surface to the exact ingest
// profiles used. PixelHash intentionally excludes geometry and profiles; Result carries those
// fields so a containing execution/evidence manifest can bind the complete claim.
type Result struct {
	Width                int                    `json:"width"`
	Height               int                    `json:"height"`
	SRGBRenderingIntent  uint8                  `json:"srgb_rendering_intent"`
	EncodedContentHash   string                 `json:"encoded_content_hash"`
	PixelHash            string                 `json:"pixel_hash"`
	DecodeProfile        string                 `json:"decode_profile"`
	NormalizationProfile string                 `json:"normalization_profile"`
	SurfaceProfile       string                 `json:"surface_profile"`
	ContentHashProfile   string                 `json:"content_hash_profile"`
	Surface              *cpucompositor.Surface `json:"-"`
}

// Decode validates and losslessly decodes one complete encoded PNG, then converts its exact
// straight sRGB RGBA8 samples into Cutly's linear-light premultiplied working surface. Encoded is
// borrowed for the duration of the call and never retained. It must remain immutable until Decode
// returns; immutable input may be shared by concurrent calls.
func Decode(ctx context.Context, encoded []byte, limits Limits) (Result, error) {
	if ctx == nil {
		return Result{}, decodeError(CodeContextInvalid, "context", ErrContextInvalid)
	}
	if err := contextFailure(ctx, "context"); err != nil {
		return Result{}, err
	}
	normalizedLimits, err := normalizeLimits(limits)
	if err != nil {
		return Result{}, err
	}
	envelope, err := validateEnvelope(ctx, encoded, normalizedLimits)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "decode"); err != nil {
		return Result{}, err
	}
	rgba, err := decodeRGBA8(ctx, encoded, envelope)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "decode"); err != nil {
		return Result{}, err
	}

	normalized, err := cutlycolor.NormalizeRGBA8(ctx, cutlycolor.RGBA8Input{
		Width: envelope.width, Height: envelope.height, Stride: envelope.width * 4,
		Metadata: cutlycolor.MetadataRGBA8SRGBV1(), Pixels: rgba,
	}, cutlycolor.Limits{
		MaxWidth: envelope.width, MaxHeight: envelope.height,
		MaxInputBytes: envelope.rgbaBytes, MaxWorkingBytes: envelope.surfaceBytes * 2,
	})
	if err != nil {
		if canceled := contextFailure(ctx, "normalize"); canceled != nil {
			return Result{}, canceled
		}
		if errors.Is(err, cutlycolor.ErrCanceled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, decodeError(CodeCanceled, "normalize", errors.Join(ErrCanceled, err))
		}
		return Result{}, decodeError(CodeNormalizeFailed, "normalize", errors.Join(ErrNormalizeFailed, err))
	}
	if err := contextFailure(ctx, "hash"); err != nil {
		return Result{}, err
	}
	pixelHash, err := normalized.CanonicalHash()
	if err != nil {
		return Result{}, decodeError(CodeSurfaceHashFailed, "hash", errors.Join(ErrSurfaceHashFailed, err))
	}
	if err := contextFailure(ctx, "encoded_hash"); err != nil {
		return Result{}, err
	}
	encodedContentHash, err := hashEncoded(ctx, encoded)
	if err != nil {
		return Result{}, err
	}
	if err := contextFailure(ctx, "publish"); err != nil {
		return Result{}, err
	}
	return Result{
		Width: envelope.width, Height: envelope.height,
		SRGBRenderingIntent: envelope.intent,
		EncodedContentHash:  encodedContentHash, PixelHash: pixelHash,
		DecodeProfile: ProfileV1, NormalizationProfile: cutlycolor.ProfileRGBA8SRGBV1,
		SurfaceProfile: cpucompositor.SurfaceProfileV1, ContentHashProfile: ContentHashProfileV1,
		Surface: normalized,
	}, nil
}

type envelope struct {
	width           int
	height          int
	intent          uint8
	rgbaBytes       uint64
	surfaceBytes    uint64
	firstIDATOffset int
}

func validateEnvelope(ctx context.Context, encoded []byte, limits Limits) (envelope, error) {
	if uint64(len(encoded)) > limits.MaxEncodedBytes {
		return envelope{}, decodeError(CodeResourceLimit, "encoded_bytes", ErrResourceLimit)
	}
	if len(encoded) < len(pngSignature) || !bytes.Equal(encoded[:len(pngSignature)], pngSignature[:]) {
		return envelope{}, decodeError(CodeInputInvalid, "signature", ErrInputInvalid)
	}

	var result envelope
	seenIHDR, seenSRGB, seenIDAT, seenIEND := false, false, false, false
	offset := len(pngSignature)
	chunkIndex := 0
	for offset < len(encoded) {
		if err := contextFailure(ctx, fmt.Sprintf("chunks/%d", chunkIndex)); err != nil {
			return envelope{}, err
		}
		if len(encoded)-offset < 12 {
			return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d", chunkIndex), ErrChunkInvalid)
		}
		length := uint64(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		if length > uint64(len(encoded)-offset-12) {
			return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/length", chunkIndex), ErrChunkInvalid)
		}
		typeStart := offset + 4
		dataStart := typeStart + 4
		dataEnd := dataStart + int(length)
		chunkEnd := dataEnd + 4
		chunkTypeBytes := encoded[typeStart:dataStart]
		if !validChunkType(chunkTypeBytes) {
			return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/type", chunkIndex), ErrChunkInvalid)
		}
		chunkType := string(chunkTypeBytes)
		expectedCRC := binary.BigEndian.Uint32(encoded[dataEnd:chunkEnd])
		actualCRC, err := checksumIEEE(ctx, encoded[typeStart:dataEnd], fmt.Sprintf("chunks/%d/crc", chunkIndex))
		if err != nil {
			return envelope{}, err
		}
		if expectedCRC != actualCRC {
			return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/crc", chunkIndex), ErrChunkInvalid)
		}

		switch chunkType {
		case "IHDR":
			if chunkIndex != 0 || seenIHDR || length != 13 {
				return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/IHDR", chunkIndex), ErrChunkInvalid)
			}
			width := binary.BigEndian.Uint32(encoded[dataStart : dataStart+4])
			height := binary.BigEndian.Uint32(encoded[dataStart+4 : dataStart+8])
			if width == 0 || height == 0 || uint64(width) > uint64(maxInt()) || uint64(height) > uint64(maxInt()) {
				return envelope{}, decodeError(CodeInputInvalid, "IHDR/dimensions", ErrInputInvalid)
			}
			if int(width) > limits.MaxWidth || int(height) > limits.MaxHeight {
				return envelope{}, decodeError(CodeResourceLimit, "IHDR/dimensions", ErrResourceLimit)
			}
			if encoded[dataStart+8] != 8 || encoded[dataStart+9] != 6 || encoded[dataStart+10] != 0 ||
				encoded[dataStart+11] != 0 || encoded[dataStart+12] != 0 {
				return envelope{}, decodeError(CodeProfileMismatch, "IHDR/profile", ErrProfileMismatch)
			}
			pixelCount := uint64(width) * uint64(height)
			frameBytes, ok := checkedMultiply(pixelCount, workingFrameBytesPerPixel)
			if !ok {
				return envelope{}, decodeError(CodeResourceLimit, "IHDR/working_bytes", ErrResourceLimit)
			}
			rowPayloadBytes, ok := checkedMultiply(uint64(width), 4)
			if !ok || rowPayloadBytes == math.MaxUint64 {
				return envelope{}, decodeError(CodeResourceLimit, "IHDR/working_bytes", ErrResourceLimit)
			}
			decoderRowBytes, ok := checkedMultiply(rowPayloadBytes+1, 2)
			if !ok {
				return envelope{}, decodeError(CodeResourceLimit, "IHDR/working_bytes", ErrResourceLimit)
			}
			requiredWorkingBytes, ok := checkedAdd(frameBytes, decoderRowBytes)
			if !ok || requiredWorkingBytes > limits.MaxWorkingBytes {
				return envelope{}, decodeError(CodeResourceLimit, "IHDR/working_bytes", ErrResourceLimit)
			}
			result.width, result.height = int(width), int(height)
			result.rgbaBytes = pixelCount * 4
			result.surfaceBytes = pixelCount * cpucompositor.BytesPerPixel
			seenIHDR = true
		case "sRGB":
			if !seenIHDR || seenSRGB || seenIDAT || length != 1 {
				return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/sRGB", chunkIndex), ErrChunkInvalid)
			}
			if encoded[dataStart] > 3 {
				return envelope{}, decodeError(CodeProfileMismatch, "sRGB/rendering_intent", ErrProfileMismatch)
			}
			result.intent = encoded[dataStart]
			seenSRGB = true
		case "IDAT":
			if !seenIHDR || !seenSRGB || seenIEND {
				return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/IDAT", chunkIndex), ErrChunkInvalid)
			}
			if !seenIDAT {
				result.firstIDATOffset = offset
			}
			seenIDAT = true
		case "IEND":
			if !seenIHDR || !seenSRGB || !seenIDAT || seenIEND || length != 0 {
				return envelope{}, decodeError(CodeChunkInvalid, fmt.Sprintf("chunks/%d/IEND", chunkIndex), ErrChunkInvalid)
			}
			seenIEND = true
			if chunkEnd != len(encoded) {
				return envelope{}, decodeError(CodeChunkInvalid, "IEND/trailing_bytes", ErrChunkInvalid)
			}
		default:
			return envelope{}, decodeError(CodeProfileMismatch, fmt.Sprintf("chunks/%d/%s", chunkIndex, chunkType), ErrProfileMismatch)
		}

		offset = chunkEnd
		chunkIndex++
		if seenIEND {
			break
		}
	}
	if !seenIHDR || !seenSRGB || !seenIDAT || !seenIEND || offset != len(encoded) {
		return envelope{}, decodeError(CodeChunkInvalid, "chunks", ErrChunkInvalid)
	}
	return result, nil
}

// decodeRGBA8 decodes the profile's one supported PNG pixel model directly. Owning the five PNG
// filters here avoids a host image library silently changing alpha or color-model conversion and
// lets cancellation be checked on decompressed rows as well as compressed input reads.
func decodeRGBA8(ctx context.Context, encoded []byte, expected envelope) ([]byte, error) {
	payload := &idatPayloadReader{ctx: ctx, encoded: encoded, offset: expected.firstIDATOffset}
	buffered := bufio.NewReaderSize(payload, maxContextRead)
	inflated, err := zlib.NewReader(buffered)
	if err != nil {
		if canceled := contextFailure(ctx, "IDAT/zlib"); canceled != nil {
			return nil, canceled
		}
		return nil, decodeError(CodeDecodeFailed, "IDAT/zlib", errors.Join(ErrDecodeFailed, err))
	}
	closed := false
	defer func() {
		if !closed {
			_ = inflated.Close()
		}
	}()
	rowBytes := expected.width*4 + 1
	pixels, current, previous, err := allocateDecodeBuffers(int(expected.rgbaBytes), rowBytes)
	if err != nil {
		return nil, err
	}
	decoded := &contextBoundedReader{ctx: ctx, reader: inflated}
	for y := 0; y < expected.height; y++ {
		if _, err := io.ReadFull(decoded, current); err != nil {
			if canceled := contextFailure(ctx, fmt.Sprintf("rows/%d", y)); canceled != nil {
				return nil, canceled
			}
			return nil, decodeError(CodeDecodeFailed, fmt.Sprintf("rows/%d", y), errors.Join(ErrDecodeFailed, err))
		}
		if err := unfilterRow(ctx, current, previous, 4, y); err != nil {
			return nil, err
		}
		copy(pixels[y*expected.width*4:(y+1)*expected.width*4], current[1:])
		current, previous = previous, current
	}
	var extra [1]byte
	read, readErr := decoded.Read(extra[:])
	closeErr := inflated.Close()
	closed = true
	if read != 0 || readErr != io.EOF || closeErr != nil {
		if canceled := contextFailure(ctx, "IDAT/inflated_bytes"); canceled != nil {
			return nil, canceled
		}
		cause := readErr
		if cause == nil {
			cause = closeErr
		}
		if cause == nil {
			cause = ErrDecodeFailed
		}
		return nil, decodeError(CodeDecodeFailed, "IDAT/inflated_bytes", errors.Join(ErrDecodeFailed, cause))
	}
	if _, trailingErr := buffered.ReadByte(); trailingErr != io.EOF {
		if canceled := contextFailure(ctx, "IDAT/trailing_compressed_bytes"); canceled != nil {
			return nil, canceled
		}
		if trailingErr == nil {
			trailingErr = ErrDecodeFailed
		}
		return nil, decodeError(CodeDecodeFailed, "IDAT/trailing_compressed_bytes", errors.Join(ErrDecodeFailed, trailingErr))
	}
	if err := contextFailure(ctx, "IDAT"); err != nil {
		return nil, err
	}
	return pixels, nil
}

func allocateDecodeBuffers(pixelBytes, rowBytes int) (pixels, current, previous []byte, err error) {
	defer func() {
		if recover() != nil {
			pixels, current, previous = nil, nil, nil
			err = decodeError(CodeAllocationFailed, "decode_buffers", ErrAllocationFailed)
		}
	}()
	return make([]byte, pixelBytes), make([]byte, rowBytes), make([]byte, rowBytes), nil
}

func unfilterRow(ctx context.Context, current, previous []byte, bytesPerPixel, row int) error {
	if len(current) != len(previous) || len(current) <= 1 || bytesPerPixel <= 0 || bytesPerPixel > len(current)-1 {
		return decodeError(CodeDecodeFailed, fmt.Sprintf("rows/%d/layout", row), ErrDecodeFailed)
	}
	data, prior := current[1:], previous[1:]
	filter := current[0]
	for index := range data {
		if index%maxContextRead == 0 {
			if err := contextFailure(ctx, fmt.Sprintf("rows/%d/filter", row)); err != nil {
				return err
			}
		}
		switch filter {
		case 0:
			// None.
		case 1:
			if index >= bytesPerPixel {
				data[index] += data[index-bytesPerPixel]
			}
		case 2:
			data[index] += prior[index]
		case 3:
			left := byte(0)
			if index >= bytesPerPixel {
				left = data[index-bytesPerPixel]
			}
			data[index] += byte((int(left) + int(prior[index])) / 2)
		case 4:
			left, upperLeft := byte(0), byte(0)
			if index >= bytesPerPixel {
				left = data[index-bytesPerPixel]
				upperLeft = prior[index-bytesPerPixel]
			}
			data[index] += paeth(left, prior[index], upperLeft)
		default:
			return decodeError(CodeDecodeFailed, fmt.Sprintf("rows/%d/filter", row), ErrDecodeFailed)
		}
	}
	return contextFailure(ctx, fmt.Sprintf("rows/%d/filter", row))
}

func paeth(left, above, upperLeft byte) byte {
	predict := int(left) + int(above) - int(upperLeft)
	leftDistance := abs(predict - int(left))
	aboveDistance := abs(predict - int(above))
	upperLeftDistance := abs(predict - int(upperLeft))
	if leftDistance <= aboveDistance && leftDistance <= upperLeftDistance {
		return left
	}
	if aboveDistance <= upperLeftDistance {
		return above
	}
	return upperLeft
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type idatPayloadReader struct {
	ctx     context.Context
	encoded []byte
	offset  int
	dataAt  int
	dataEnd int
	ended   bool
}

func (value *idatPayloadReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if err := value.ctx.Err(); err != nil {
		return 0, err
	}
	for value.dataAt == value.dataEnd {
		if value.ended || value.offset+12 > len(value.encoded) {
			return 0, io.EOF
		}
		length := int(binary.BigEndian.Uint32(value.encoded[value.offset : value.offset+4]))
		kind := string(value.encoded[value.offset+4 : value.offset+8])
		if kind == "IEND" {
			value.ended = true
			return 0, io.EOF
		}
		if kind != "IDAT" {
			return 0, ErrChunkInvalid
		}
		value.dataAt = value.offset + 8
		value.dataEnd = value.dataAt + length
		value.offset = value.dataEnd + 4
		if err := value.ctx.Err(); err != nil {
			return 0, err
		}
	}
	read := copy(buffer, value.encoded[value.dataAt:value.dataEnd])
	value.dataAt += read
	return read, nil
}

func (value *idatPayloadReader) ReadByte() (byte, error) {
	var one [1]byte
	read, err := value.Read(one[:])
	if read == 1 {
		return one[0], nil
	}
	return 0, err
}

func normalizeLimits(value Limits) (Limits, error) {
	if value == (Limits{}) {
		value = DefaultLimits()
	}
	if value.MaxWidth <= 0 || value.MaxHeight <= 0 || value.MaxEncodedBytes < 8 ||
		value.MaxEncodedBytes > HardMaxEncodedBytes || value.MaxWorkingBytes < minimumWorkingBytes ||
		value.MaxWorkingBytes > HardMaxWorkingBytes {
		return Limits{}, decodeError(CodeResourceLimit, "limits", ErrResourceLimit)
	}
	return value, nil
}

func validChunkType(value []byte) bool {
	if len(value) != 4 {
		return false
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z')) {
			return false
		}
	}
	return true
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return decodeError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return decodeError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func checksumIEEE(ctx context.Context, value []byte, path string) (uint32, error) {
	hash := crc32.NewIEEE()
	for offset := 0; offset < len(value); {
		if err := contextFailure(ctx, path); err != nil {
			return 0, err
		}
		end := offset + maxContextRead
		if end > len(value) {
			end = len(value)
		}
		_, _ = hash.Write(value[offset:end])
		offset = end
	}
	if err := contextFailure(ctx, path); err != nil {
		return 0, err
	}
	return hash.Sum32(), nil
}

func hashEncoded(ctx context.Context, encoded []byte) (string, error) {
	hash := sha256.New()
	for offset := 0; offset < len(encoded); {
		if err := contextFailure(ctx, "encoded_hash"); err != nil {
			return "", err
		}
		end := offset + maxContextRead
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = hash.Write(encoded[offset:end])
		offset = end
	}
	if err := contextFailure(ctx, "encoded_hash"); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func checkedMultiply(left, right uint64) (uint64, bool) {
	if right != 0 && left > math.MaxUint64/right {
		return 0, false
	}
	return left * right, true
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}

type contextBoundedReader struct {
	ctx    context.Context
	reader io.Reader
}

func (value *contextBoundedReader) Read(buffer []byte) (int, error) {
	if err := value.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) > maxContextRead {
		buffer = buffer[:maxContextRead]
	}
	read, err := value.reader.Read(buffer)
	if read == 0 {
		if canceled := value.ctx.Err(); canceled != nil {
			return 0, canceled
		}
	}
	return read, err
}

func maxInt() int { return int(^uint(0) >> 1) }
