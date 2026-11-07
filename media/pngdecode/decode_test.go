package pngdecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	stdcolor "image/color"
	"image/png"
	"sync"
	"testing"

	cutlycolor "github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestDecodeStrictPNGSuccessAndBindings(t *testing.T) {
	encoded := strictPNG(t, []stdcolor.NRGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 128, G: 64, B: 32, A: 128},
	}, 2, 1, 2)
	before := append([]byte(nil), encoded...)
	result, err := Decode(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 2 || result.Height != 1 || result.SRGBRenderingIntent != 2 ||
		result.DecodeProfile != ProfileV1 || result.NormalizationProfile != cutlycolor.ProfileRGBA8SRGBV1 ||
		result.SurfaceProfile != cpucompositor.SurfaceProfileV1 || result.ContentHashProfile != ContentHashProfileV1 ||
		result.Surface == nil {
		t.Fatalf("unexpected result: %+v", result)
	}
	digest := sha256.Sum256(before)
	if want := "sha256:" + hex.EncodeToString(digest[:]); result.EncodedContentHash != want {
		t.Fatalf("content hash = %q, want %q", result.EncodedContentHash, want)
	}
	if hash, hashErr := result.Surface.CanonicalHash(); hashErr != nil || hash != result.PixelHash {
		t.Fatalf("surface hash = %q, %v; result = %q", hash, hashErr, result.PixelHash)
	}
	first, err := result.Surface.Pixel(0, 0)
	if err != nil || first != (cpucompositor.Pixel{R: 65535, A: 65535}) {
		t.Fatalf("first pixel = %+v, %v", first, err)
	}
	second, err := result.Surface.Pixel(1, 0)
	if err != nil || second != (cpucompositor.Pixel{R: 7101, G: 1687, B: 475, A: 32896}) {
		t.Fatalf("second pixel = %+v, %v", second, err)
	}

	// Result owns decoded/normalized storage; later caller mutation of encoded bytes cannot alter it.
	for index := range encoded {
		encoded[index] = 0
	}
	if hash, hashErr := result.Surface.CanonicalHash(); hashErr != nil || hash != result.PixelHash {
		t.Fatalf("hash after input mutation = %q, %v", hash, hashErr)
	}
}

func TestDecodeRejectsEveryOutOfProfileEnvelope(t *testing.T) {
	baseWithoutSRGB := encodedNRGBA(t, []stdcolor.NRGBA{{R: 1, G: 2, B: 3, A: 4}}, 1, 1)
	base := insertChunkAfter(t, baseWithoutSRGB, "IHDR", pngChunk("sRGB", []byte{0}))
	tests := []struct {
		name string
		data []byte
		is   error
	}{
		{name: "missing srgb", data: baseWithoutSRGB, is: ErrChunkInvalid},
		{name: "unknown ancillary", data: insertChunkAfter(t, base, "sRGB", pngChunk("tEXt", []byte("k\x00v"))), is: ErrProfileMismatch},
		{name: "icc", data: insertChunkAfter(t, base, "sRGB", pngChunk("iCCP", []byte("profile\x00\x00"))), is: ErrProfileMismatch},
		{name: "exif", data: insertChunkAfter(t, base, "sRGB", pngChunk("eXIf", []byte{1})), is: ErrProfileMismatch},
		{name: "apng", data: insertChunkAfter(t, base, "sRGB", pngChunk("acTL", make([]byte, 8))), is: ErrProfileMismatch},
		{name: "noncontiguous idat", data: insertChunkAfter(t, base, "IDAT", append(pngChunk("tEXt", []byte("x\x00y")), pngChunk("IDAT", nil)...)), is: ErrProfileMismatch},
		{name: "trailing idat chunk", data: insertChunkAfter(t, base, "IDAT", pngChunk("IDAT", []byte{1, 2, 3})), is: ErrDecodeFailed},
		{name: "trailing bytes in idat", data: appendIDATPayload(t, base, []byte{1, 2, 3}), is: ErrDecodeFailed},
		{name: "duplicate srgb", data: insertChunkAfter(t, base, "sRGB", pngChunk("sRGB", []byte{0})), is: ErrChunkInvalid},
		{name: "invalid intent", data: insertChunkAfter(t, baseWithoutSRGB, "IHDR", pngChunk("sRGB", []byte{4})), is: ErrProfileMismatch},
		{name: "srgb after idat", data: insertChunkAfter(t, baseWithoutSRGB, "IDAT", pngChunk("sRGB", []byte{0})), is: ErrChunkInvalid},
		{name: "trailing bytes", data: append(append([]byte(nil), base...), 0), is: ErrChunkInvalid},
		{name: "corrupt crc", data: corruptChunkCRC(t, base, "IDAT"), is: ErrChunkInvalid},
		{name: "interlaced", data: mutateIHDR(t, base, func(data []byte) { data[12] = 1 }), is: ErrProfileMismatch},
		{name: "sixteen bit", data: mutateIHDR(t, base, func(data []byte) { data[8] = 16 }), is: ErrProfileMismatch},
		{name: "bad signature", data: append([]byte{0}, base[1:]...), is: ErrInputInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Decode(context.Background(), test.data, Limits{})
			if !errors.Is(err, test.is) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.is)
			}
			if result != (Result{}) {
				t.Fatalf("failure exposed partial result: %+v", result)
			}
		})
	}
}

func TestDecodeAcceptsOneZlibStreamSplitAcrossIDATChunks(t *testing.T) {
	base := strictPNG(t, []stdcolor.NRGBA{{R: 1, G: 2, B: 3, A: 4}}, 1, 1, 0)
	split := splitIDAT(t, base)
	want, err := Decode(context.Background(), base, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(context.Background(), split, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got.PixelHash != want.PixelHash || got.EncodedContentHash == want.EncodedContentHash {
		t.Fatalf("split result = %+v, base = %+v", got, want)
	}
}

func TestDecodeRejectsNonRGBAColorModel(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, 1, 1))
	gray.SetGray(0, 0, stdcolor.Gray{Y: 123})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, gray); err != nil {
		t.Fatal(err)
	}
	encoded := insertChunkAfter(t, buffer.Bytes(), "IHDR", pngChunk("sRGB", []byte{0}))
	if _, err := Decode(context.Background(), encoded, Limits{}); !errors.Is(err, ErrProfileMismatch) {
		t.Fatalf("gray error = %v", err)
	}
}

func TestDecodeLimitsAndCancellationFailBeforePublication(t *testing.T) {
	encoded := strictPNG(t, []stdcolor.NRGBA{{A: 255}}, 1, 1, 0)
	tests := []struct {
		name   string
		limits Limits
	}{
		{name: "encoded", limits: Limits{MaxWidth: 1, MaxHeight: 1, MaxEncodedBytes: uint64(len(encoded) - 1), MaxWorkingBytes: 20}},
		{name: "width", limits: Limits{MaxWidth: 0, MaxHeight: 1, MaxEncodedBytes: uint64(len(encoded)), MaxWorkingBytes: 20}},
		{name: "working", limits: Limits{MaxWidth: 1, MaxHeight: 1, MaxEncodedBytes: uint64(len(encoded)), MaxWorkingBytes: 19}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Decode(context.Background(), encoded, test.limits)
			if !errors.Is(err, ErrResourceLimit) || result != (Result{}) {
				t.Fatalf("result/error = %+v, %v", result, err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Decode(ctx, encoded, Limits{})
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) || result != (Result{}) {
		t.Fatalf("canceled result/error = %+v, %v", result, err)
	}
}

func TestDecodeWorkingLimitIncludesDecoderScanlines(t *testing.T) {
	encoded := strictPNG(t, []stdcolor.NRGBA{{A: 255}, {A: 254}}, 2, 1, 0)
	// Two pixels require 2*20 frame bytes plus two (filter byte + 2*RGBA8) rows.
	const exactWorkingBytes = 58
	limits := Limits{MaxWidth: 2, MaxHeight: 1, MaxEncodedBytes: uint64(len(encoded)), MaxWorkingBytes: exactWorkingBytes - 1}
	if result, err := Decode(context.Background(), encoded, limits); !errors.Is(err, ErrResourceLimit) || result != (Result{}) {
		t.Fatalf("under-budget result/error = %+v, %v", result, err)
	}
	limits.MaxWorkingBytes = exactWorkingBytes
	if _, err := Decode(context.Background(), encoded, limits); err != nil {
		t.Fatalf("exact-budget decode: %v", err)
	}
}

func TestDecodeConcurrentDeterminism(t *testing.T) {
	encoded := strictPNG(t, []stdcolor.NRGBA{
		{R: 1, G: 2, B: 3, A: 4}, {R: 5, G: 6, B: 7, A: 8},
		{R: 9, G: 10, B: 11, A: 12}, {R: 13, G: 14, B: 15, A: 16},
	}, 2, 2, 1)
	want, err := Decode(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, decodeErr := Decode(context.Background(), encoded, Limits{})
			if decodeErr != nil {
				errorsChannel <- decodeErr
				return
			}
			if got.PixelHash != want.PixelHash || got.EncodedContentHash != want.EncodedContentHash {
				errorsChannel <- errors.New("concurrent result differs")
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func TestContextReaderChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &contextBoundedReader{ctx: ctx, reader: bytes.NewReader(make([]byte, maxContextRead*2))}
	buffer := make([]byte, maxContextRead*4)
	if read, err := reader.Read(buffer); err != nil || read != maxContextRead {
		t.Fatalf("first read = %d, %v", read, err)
	}
	cancel()
	if read, err := reader.Read(buffer); read != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %d, %v", read, err)
	}
}

func TestValidationAndEncodedHashObserveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checksumIEEE(ctx, make([]byte, maxContextRead*2), "crc"); !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CRC error = %v", err)
	}
	if hash, err := hashEncoded(ctx, make([]byte, maxContextRead*2)); hash != "" || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled content hash = %q, %v", hash, err)
	}
	if hash, err := hashEncoded(context.Background(), []byte("abc")); err != nil || hash != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("content hash = %q, %v", hash, err)
	}
}

func TestAllPNGFiltersMatchIndependentVectors(t *testing.T) {
	previous := []byte{0, 10, 20, 30, 40, 50, 60, 70, 80}
	want := []byte{0, 15, 25, 35, 45, 65, 85, 105, 125}
	tests := []struct {
		name    string
		filter  byte
		encoded []byte
	}{
		{name: "none", filter: 0, encoded: []byte{15, 25, 35, 45, 65, 85, 105, 125}},
		{name: "sub", filter: 1, encoded: []byte{15, 25, 35, 45, 50, 60, 70, 80}},
		{name: "up", filter: 2, encoded: []byte{5, 5, 5, 5, 15, 25, 35, 45}},
		{name: "average", filter: 3, encoded: []byte{10, 15, 20, 25, 33, 43, 53, 63}},
		{name: "paeth", filter: 4, encoded: []byte{5, 5, 5, 5, 15, 25, 35, 45}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := append([]byte{test.filter}, test.encoded...)
			if err := unfilterRow(context.Background(), current, append([]byte(nil), previous...), 4, 7); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(current[1:], want[1:]) {
				t.Fatalf("decoded row = %v, want %v", current[1:], want[1:])
			}
		})
	}
	bad := append([]byte{5}, make([]byte, 8)...)
	if err := unfilterRow(context.Background(), bad, append([]byte(nil), previous...), 4, 0); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("bad filter error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := unfilterRow(ctx, append([]byte(nil), want...), append([]byte(nil), previous...), 4, 0); !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled filter error = %v", err)
	}
}

func TestPaethTieBreaking(t *testing.T) {
	tests := []struct {
		left, above, upperLeft byte
		want                   byte
	}{
		{left: 10, above: 20, upperLeft: 10, want: 20},
		{left: 20, above: 10, upperLeft: 10, want: 20},
		{left: 10, above: 10, upperLeft: 20, want: 10},
		{left: 0, above: 255, upperLeft: 0, want: 255},
	}
	for _, test := range tests {
		if got := paeth(test.left, test.above, test.upperLeft); got != test.want {
			t.Fatalf("paeth(%d,%d,%d) = %d, want %d", test.left, test.above, test.upperLeft, got, test.want)
		}
	}
}

func TestStrictDecoderMatchesRGBA8EncoderCorpus(t *testing.T) {
	state := uint32(0x9e3779b9)
	next := func() byte {
		state = state*1664525 + 1013904223
		return byte(state >> 24)
	}
	for iteration := 0; iteration < 100; iteration++ {
		width, height := iteration%17+1, iteration%11+1
		imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
		for index := range imageValue.Pix {
			imageValue.Pix[index] = next()
		}
		// Prevent the standard encoder from selecting opaque truecolor instead of profile RGBA8.
		imageValue.Pix[3] = byte(iteration % 255)
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, imageValue); err != nil {
			t.Fatal(err)
		}
		encoded := insertChunkAfter(t, buffer.Bytes(), "IHDR", pngChunk("sRGB", []byte{byte(iteration % 4)}))
		envelope, err := validateEnvelope(context.Background(), encoded, DefaultLimits())
		if err != nil {
			t.Fatalf("iteration %d envelope: %v", iteration, err)
		}
		decoded, err := decodeRGBA8(context.Background(), encoded, envelope)
		if err != nil {
			t.Fatalf("iteration %d decode: %v", iteration, err)
		}
		if !bytes.Equal(decoded, imageValue.Pix) {
			t.Fatalf("iteration %d decoded pixels differ", iteration)
		}
	}
}

func FuzzDecodeNeverPanicsOrPublishesOnError(f *testing.F) {
	f.Add([]byte{})
	f.Add(strictPNGForFuzz(f, []stdcolor.NRGBA{{R: 1, A: 255}}, 1, 1, 0))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 1<<20 {
			t.Skip()
		}
		result, err := Decode(context.Background(), encoded, Limits{
			MaxWidth: 64, MaxHeight: 64, MaxEncodedBytes: 1 << 20, MaxWorkingBytes: 64 * 64 * 20,
		})
		if err != nil && result != (Result{}) {
			t.Fatalf("error exposed partial result: %+v, %v", result, err)
		}
		if err == nil && (result.Surface == nil || result.PixelHash == "" || result.EncodedContentHash == "") {
			t.Fatalf("success omitted bindings: %+v", result)
		}
	})
}

func strictPNG(t *testing.T, pixels []stdcolor.NRGBA, width, height int, intent byte) []byte {
	t.Helper()
	return strictPNGForT(t, pixels, width, height, intent)
}

func strictPNGForFuzz(f *testing.F, pixels []stdcolor.NRGBA, width, height int, intent byte) []byte {
	f.Helper()
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for index, pixel := range pixels {
		imageValue.SetNRGBA(index%width, index/width, pixel)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		f.Fatal(err)
	}
	return insertChunkAfterF(f, buffer.Bytes(), "IHDR", pngChunk("sRGB", []byte{intent}))
}

func strictPNGForT(t *testing.T, pixels []stdcolor.NRGBA, width, height int, intent byte) []byte {
	t.Helper()
	return insertChunkAfter(t, encodedNRGBA(t, pixels, width, height), "IHDR", pngChunk("sRGB", []byte{intent}))
}

func encodedNRGBA(t *testing.T, pixels []stdcolor.NRGBA, width, height int) []byte {
	t.Helper()
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for index, pixel := range pixels {
		imageValue.SetNRGBA(index%width, index/width, pixel)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func pngChunk(kind string, data []byte) []byte {
	result := make([]byte, 12+len(data))
	binary.BigEndian.PutUint32(result[:4], uint32(len(data)))
	copy(result[4:8], kind)
	copy(result[8:8+len(data)], data)
	binary.BigEndian.PutUint32(result[8+len(data):], crc32.ChecksumIEEE(result[4:8+len(data)]))
	return result
}

func insertChunkAfter(t *testing.T, encoded []byte, after string, chunk []byte) []byte {
	t.Helper()
	return insertChunkAfterCommon(t, encoded, after, chunk)
}

func insertChunkAfterF(f *testing.F, encoded []byte, after string, chunk []byte) []byte {
	f.Helper()
	return insertChunkAfterCommon(f, encoded, after, chunk)
}

type fatalHelper interface {
	Helper()
	Fatalf(string, ...any)
}

func insertChunkAfterCommon(test fatalHelper, encoded []byte, after string, chunk []byte) []byte {
	test.Helper()
	offset := len(pngSignature)
	for offset+12 <= len(encoded) {
		length := int(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		end := offset + 12 + length
		if end > len(encoded) {
			test.Fatalf("invalid test PNG")
		}
		if string(encoded[offset+4:offset+8]) == after {
			result := make([]byte, 0, len(encoded)+len(chunk))
			result = append(result, encoded[:end]...)
			result = append(result, chunk...)
			result = append(result, encoded[end:]...)
			return result
		}
		offset = end
	}
	test.Fatalf("chunk %s not found", after)
	return nil
}

func corruptChunkCRC(t *testing.T, encoded []byte, kind string) []byte {
	t.Helper()
	result := append([]byte(nil), encoded...)
	offset := len(pngSignature)
	for offset+12 <= len(result) {
		length := int(binary.BigEndian.Uint32(result[offset : offset+4]))
		end := offset + 12 + length
		if string(result[offset+4:offset+8]) == kind {
			result[end-1] ^= 0xff
			return result
		}
		offset = end
	}
	t.Fatalf("chunk %s not found", kind)
	return nil
}

func mutateIHDR(t *testing.T, encoded []byte, mutate func([]byte)) []byte {
	t.Helper()
	result := append([]byte(nil), encoded...)
	if string(result[12:16]) != "IHDR" {
		t.Fatal("test PNG lacks first IHDR")
	}
	length := int(binary.BigEndian.Uint32(result[8:12]))
	data := result[16 : 16+length]
	mutate(data)
	binary.BigEndian.PutUint32(result[16+length:20+length], crc32.ChecksumIEEE(result[12:16+length]))
	return result
}

func appendIDATPayload(t *testing.T, encoded, suffix []byte) []byte {
	t.Helper()
	offset := len(pngSignature)
	for offset+12 <= len(encoded) {
		length := int(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		end := offset + 12 + length
		if string(encoded[offset+4:offset+8]) == "IDAT" {
			data := append([]byte(nil), encoded[offset+8:offset+8+length]...)
			data = append(data, suffix...)
			replacement := pngChunk("IDAT", data)
			result := make([]byte, 0, len(encoded)+len(suffix))
			result = append(result, encoded[:offset]...)
			result = append(result, replacement...)
			result = append(result, encoded[end:]...)
			return result
		}
		offset = end
	}
	t.Fatal("IDAT not found")
	return nil
}

func splitIDAT(t *testing.T, encoded []byte) []byte {
	t.Helper()
	offset := len(pngSignature)
	for offset+12 <= len(encoded) {
		length := int(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		end := offset + 12 + length
		if string(encoded[offset+4:offset+8]) == "IDAT" {
			if length < 2 {
				t.Fatal("IDAT too short to split")
			}
			data := encoded[offset+8 : offset+8+length]
			middle := len(data) / 2
			first, second := pngChunk("IDAT", data[:middle]), pngChunk("IDAT", data[middle:])
			result := make([]byte, 0, len(encoded)+12)
			result = append(result, encoded[:offset]...)
			result = append(result, first...)
			result = append(result, second...)
			result = append(result, encoded[end:]...)
			return result
		}
		offset = end
	}
	t.Fatal("IDAT not found")
	return nil
}
