package pngencode

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/adler32"
	"hash/crc32"
	"io"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func TestEncodeCanonicalPNGAndStrictRoundTrip(t *testing.T) {
	input := outputFrame(t, 3, 1, []cpucompositor.Pixel{
		{R: 65535, A: 65535},
		{R: 8192, G: 4096, B: 2048, A: 32768},
		{},
	})
	result, err := Encode(context.Background(), input, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ProfileV1 || result.InputColorProfile != color.ProfileLinearRGBA16ToRGBA8SRGBV1 ||
		result.InputSurfaceProfile != cpucompositor.SurfaceProfileV1 ||
		result.InputPixelHashProfile != color.OutputRGBAHashProfileV1 || result.FilterProfile != FilterProfileV1 ||
		result.DeflateProfile != DeflateProfileV1 || result.RoundTripDecodeProfile != pngdecode.ProfileV1 ||
		result.ArtifactHashProfile != pngdecode.ContentHashProfileV1 || result.Width != 3 || result.Height != 1 ||
		result.Stride != 12 || result.SRGBRenderingIntent != 0 || result.RawRGBABytes != 12 ||
		result.ScanlineBytes != 13 || result.StoredBlockCount != 1 || result.IDATBytes != 24 ||
		result.ArtifactBytes != 94 || result.WorkingBytes != 106 ||
		result.SourcePixelHash != input.PixelHash || result.Artifact == nil ||
		result.Artifact.Size() != 94 {
		t.Fatalf("result=%+v", result)
	}
	encoded, err := result.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	chunks := parsePNG(t, encoded)
	if got, want := chunkTypes(chunks), []string{"IHDR", "sRGB", "IDAT", "IEND"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks=%v want=%v", got, want)
	}
	assertIHDR(t, chunks[0].data, 3, 1)
	if !bytes.Equal(chunks[1].data, []byte{0}) {
		t.Fatalf("sRGB=%x", chunks[1].data)
	}
	raw, blocks := inflateStoredOracle(t, chunks[2].data)
	wantPixels, err := input.Frame.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := append([]byte{0}, wantPixels...)
	if !bytes.Equal(raw, wantRaw) || !reflect.DeepEqual(blocks, []int{13}) {
		t.Fatalf("raw=%x want=%x blocks=%v", raw, wantRaw, blocks)
	}
	reader, err := zlib.NewReader(bytes.NewReader(chunks[2].data))
	if err != nil {
		t.Fatal(err)
	}
	standardRaw, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || !bytes.Equal(standardRaw, wantRaw) {
		t.Fatalf("stdlib raw=%x err=%v close=%v", standardRaw, err, closeErr)
	}
	artifactHash, err := result.Artifact.CanonicalHash()
	if err != nil || artifactHash != result.ArtifactHash {
		t.Fatalf("hash=%s result=%s err=%v", artifactHash, result.ArtifactHash, err)
	}
	decoded, err := pngdecode.Decode(context.Background(), encoded, pngdecode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Width != 3 || decoded.Height != 1 || decoded.SRGBRenderingIntent != 0 ||
		decoded.EncodedContentHash != result.ArtifactHash {
		t.Fatalf("decoded=%+v", decoded)
	}
	normalized, err := color.NormalizeRGBA8(context.Background(), color.RGBA8Input{
		Width: 3, Height: 1, Stride: 12, Metadata: color.MetadataRGBA8SRGBV1(), Pixels: wantPixels,
	}, color.Limits{MaxWidth: 3, MaxHeight: 1, MaxInputBytes: 12, MaxWorkingBytes: 48})
	if err != nil {
		t.Fatal(err)
	}
	normalizedHash, _ := normalized.CanonicalHash()
	if decoded.PixelHash != normalizedHash {
		t.Fatalf("decoded pixel hash=%s normalized=%s", decoded.PixelHash, normalizedHash)
	}
	encoded[0] = 0
	again, _ := result.Artifact.Bytes()
	if !bytes.Equal(again[:8], pngSignature[:]) {
		t.Fatal("Bytes exposed artifact backing")
	}
}

func TestStoredDeflateBoundaryAndOddGeometry(t *testing.T) {
	input := outputFrame(t, 8192, 2, nil)
	result, err := Encode(context.Background(), input, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if result.RawRGBABytes != 65536 || result.ScanlineBytes != 65538 || result.StoredBlockCount != 2 ||
		result.IDATBytes != 65554 || result.ArtifactBytes != 65624 || result.WorkingBytes != 98392 {
		t.Fatalf("result=%+v", result)
	}
	encoded, _ := result.Artifact.Bytes()
	chunks := parsePNG(t, encoded)
	raw, blocks := inflateStoredOracle(t, chunks[2].data)
	if len(raw) != 65538 || !reflect.DeepEqual(blocks, []int{65535, 3}) || raw[0] != 0 || raw[32769] != 0 {
		t.Fatalf("raw=%d blocks=%v filters=%d,%d", len(raw), blocks, raw[0], raw[32769])
	}
	if _, err := pngdecode.Decode(context.Background(), encoded, pngdecode.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeRejectsForgedInputLimitsAndCancellation(t *testing.T) {
	base := outputFrame(t, 1, 1, []cpucompositor.Pixel{{R: 123, G: 45, B: 6, A: 456}})
	mutations := []struct {
		name string
		edit func(*color.OutputResult)
		want error
	}{
		{name: "nil frame", edit: func(value *color.OutputResult) { value.Frame = nil }, want: ErrInputInvalid},
		{name: "surface profile", edit: func(value *color.OutputResult) { value.InputSurfaceProfile = "vendor" }, want: ErrInputInvalid},
		{name: "color profile", edit: func(value *color.OutputResult) { value.OutputProfile = "vendor" }, want: ErrInputInvalid},
		{name: "metadata", edit: func(value *color.OutputResult) { value.OutputMetadata.Transfer = "linear" }, want: ErrInputInvalid},
		{name: "hash profile", edit: func(value *color.OutputResult) { value.HashProfile = "vendor" }, want: ErrInputInvalid},
		{name: "hash shape", edit: func(value *color.OutputResult) { value.PixelHash = "sha256:deadbeef" }, want: ErrInputInvalid},
		{name: "hash value", edit: func(value *color.OutputResult) { value.PixelHash = "sha256:" + string(bytes.Repeat([]byte{'0'}, 64)) }, want: ErrInputMismatch},
		{name: "width", edit: func(value *color.OutputResult) { value.Width++ }, want: ErrInputMismatch},
		{name: "height", edit: func(value *color.OutputResult) { value.Height++ }, want: ErrInputMismatch},
		{name: "stride", edit: func(value *color.OutputResult) { value.Stride++ }, want: ErrInputMismatch},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			result, err := Encode(context.Background(), input, DefaultLimits())
			if result != (Result{}) || !errors.Is(err, test.want) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, test.want)
			}
		})
	}
	for _, limits := range []Limits{
		{MaxWidth: 1, MaxHeight: 1, MaxRawRGBABytes: 3, MaxArtifactBytes: 100},
		{MaxWidth: 1, MaxHeight: 1, MaxRawRGBABytes: 4, MaxArtifactBytes: 85},
		{MaxWidth: HardMaxDimension + 1, MaxHeight: 1, MaxRawRGBABytes: 4, MaxArtifactBytes: 100},
	} {
		if result, err := Encode(context.Background(), base, limits); result != (Result{}) ||
			(!errors.Is(err, ErrInputInvalid) && !errors.Is(err, ErrResourceLimit)) {
			t.Fatalf("limits=%+v result=%+v err=%v", limits, result, err)
		}
	}
	limitedWidth := DefaultLimits()
	limitedWidth.MaxWidth = 1
	wide := outputFrame(t, 2, 1, nil)
	if result, err := Encode(context.Background(), wide, limitedWidth); result != (Result{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("width result=%+v err=%v", result, err)
	}
	if _, err := checkedLayout(maxInt(), 1, 4, DefaultLimits()); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("overflow err=%v", err)
	}
	if result, err := Encode(nil, base, DefaultLimits()); result != (Result{}) || !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("nil context result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Encode(ctx, base, DefaultLimits()); result != (Result{}) || !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result=%+v err=%v", result, err)
	}
	for _, cancelAfter := range []int{2, 6, 10, 14} {
		ctx := &stepCancelContext{cancelAfter: cancelAfter}
		result, err := Encode(ctx, outputFrame(t, 8, 4, nil), DefaultLimits())
		if err != nil && (result != (Result{}) || !errors.Is(err, ErrCanceled)) {
			t.Fatalf("cancelAfter=%d result=%+v err=%v", cancelAfter, result, err)
		}
	}
}

func TestArtifactAccessorsHostileWriterAndConcurrency(t *testing.T) {
	result, err := Encode(context.Background(), outputFrame(t, 8192, 2, nil), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := result.Artifact.Bytes()
	var zero *Artifact
	if _, err := zero.Bytes(); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("nil bytes err=%v", err)
	}
	if _, err := (&Artifact{}).WriteTo(io.Discard); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("zero writer err=%v", err)
	}
	if _, err := result.Artifact.WriteTo(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer err=%v", err)
	}
	if _, err := result.Artifact.WriteTo(errorWriter{}); !errors.Is(err, errWriter) {
		t.Fatalf("error writer err=%v", err)
	}
	for _, writer := range []io.Writer{invalidCountWriter{-1, nil}, invalidCountWriter{len(want) + 1, nil}, invalidCountWriter{-1, errWriter}} {
		if written, err := result.Artifact.WriteTo(writer); written != 0 || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("writer=%T written=%d err=%v", writer, written, err)
		}
	}
	hostile := &mutatingRetainingWriter{}
	if written, err := result.Artifact.WriteTo(hostile); err != nil || written != int64(len(want)) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if len(hostile.retained) == 0 {
		t.Fatal("writer retained no chunk")
	}
	hostile.retained[0] ^= 0xff
	again, _ := result.Artifact.Bytes()
	if !bytes.Equal(again, want) {
		t.Fatal("writer mutated artifact")
	}
	const workers = 24
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			switch index % 3 {
			case 0:
				value, accessErr := result.Artifact.Bytes()
				if accessErr != nil || !bytes.Equal(value, want) {
					errorsOut <- errors.New("bytes mismatch")
				}
			case 1:
				value, accessErr := result.Artifact.CanonicalHash()
				if accessErr != nil || value != result.ArtifactHash {
					errorsOut <- errors.New("hash mismatch")
				}
			case 2:
				written, accessErr := result.Artifact.WriteTo(io.Discard)
				if accessErr != nil || written != int64(len(want)) {
					errorsOut <- errors.New("write mismatch")
				}
			}
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
}

func TestCanonicalProfileAndVectorFixtures(t *testing.T) {
	want, err := videoedit.CanonicalJSON(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("%v\ncanonical=%s", err, want)
	}
	var fixture ProfileDescriptor
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatalf("%v\ncanonical=%s", err, want)
	}
	got, err := videoedit.CanonicalJSON(fixture)
	if err != nil || !reflect.DeepEqual(fixture, Descriptor()) || !bytes.Equal(bytes.TrimSpace(raw), got) || !bytes.Equal(got, want) {
		t.Fatalf("profile fixture differs\nwant=%s\ngot=%s err=%v", want, bytes.TrimSpace(raw), err)
	}

	result, err := Encode(context.Background(), outputFrame(t, 1, 1, []cpucompositor.Pixel{{R: 65535, A: 65535}}), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := result.Artifact.Bytes()
	actual := vectorFixture{SourcePixelHash: result.SourcePixelHash, ArtifactHash: result.ArtifactHash, ArtifactHex: hex.EncodeToString(encoded)}
	vectorRaw, err := os.ReadFile("testdata/vector-v1.json")
	if err != nil {
		canonical, _ := json.Marshal(actual)
		t.Fatalf("%v\nvector=%s", err, canonical)
	}
	var vector vectorFixture
	if err := wirejson.Decode(vectorRaw, &vector); err != nil {
		t.Fatal(err)
	}
	if vector != actual {
		t.Fatalf("vector differs\nwant=%+v\ngot=%+v", actual, vector)
	}
}

func FuzzEncodeNeverPanicsOrPublishesPartial(f *testing.F) {
	f.Add(uint16(65535), uint16(0), uint16(32768), uint16(65535))
	f.Fuzz(func(t *testing.T, red, green, blue, alpha uint16) {
		if red > alpha {
			red = alpha
		}
		if green > alpha {
			green = alpha
		}
		if blue > alpha {
			blue = alpha
		}
		input := outputFrame(t, 1, 1, []cpucompositor.Pixel{{R: red, G: green, B: blue, A: alpha}})
		result, err := Encode(context.Background(), input, DefaultLimits())
		if err != nil {
			if result != (Result{}) {
				t.Fatalf("partial result=%+v err=%v", result, err)
			}
			return
		}
		encoded, accessErr := result.Artifact.Bytes()
		if accessErr != nil {
			t.Fatal(accessErr)
		}
		decoded, decodeErr := pngdecode.Decode(context.Background(), encoded, pngdecode.DefaultLimits())
		if decodeErr != nil || decoded.Width != 1 || decoded.Height != 1 || decoded.EncodedContentHash != result.ArtifactHash {
			t.Fatalf("decoded=%+v decodeErr=%v result=%+v", decoded, decodeErr, result)
		}
	})
}

type vectorFixture struct {
	SourcePixelHash string `json:"source_pixel_hash"`
	ArtifactHash    string `json:"artifact_hash"`
	ArtifactHex     string `json:"artifact_hex"`
}

type parsedChunk struct {
	typeName string
	data     []byte
}

func parsePNG(t testing.TB, encoded []byte) []parsedChunk {
	t.Helper()
	if len(encoded) < 8 || !bytes.Equal(encoded[:8], pngSignature[:]) {
		t.Fatalf("signature=%x", encoded)
	}
	offset := 8
	var chunks []parsedChunk
	for offset < len(encoded) {
		if len(encoded)-offset < 12 {
			t.Fatalf("truncated chunk at %d", offset)
		}
		length := int(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		if length > len(encoded)-offset-12 {
			t.Fatalf("chunk length=%d remaining=%d", length, len(encoded)-offset)
		}
		typeName := string(encoded[offset+4 : offset+8])
		data := append([]byte(nil), encoded[offset+8:offset+8+length]...)
		wantCRC := binary.BigEndian.Uint32(encoded[offset+8+length : offset+12+length])
		gotCRC := crc32.ChecksumIEEE(encoded[offset+4 : offset+8+length])
		if gotCRC != wantCRC {
			t.Fatalf("%s crc=%08x want=%08x", typeName, gotCRC, wantCRC)
		}
		chunks = append(chunks, parsedChunk{typeName: typeName, data: data})
		offset += length + 12
	}
	if offset != len(encoded) {
		t.Fatalf("offset=%d len=%d", offset, len(encoded))
	}
	return chunks
}

func chunkTypes(chunks []parsedChunk) []string {
	result := make([]string, len(chunks))
	for index := range chunks {
		result[index] = chunks[index].typeName
	}
	return result
}

func assertIHDR(t testing.TB, data []byte, width, height uint32) {
	t.Helper()
	if len(data) != 13 || binary.BigEndian.Uint32(data[:4]) != width || binary.BigEndian.Uint32(data[4:8]) != height ||
		!bytes.Equal(data[8:], []byte{8, 6, 0, 0, 0}) {
		t.Fatalf("IHDR=%x", data)
	}
}

func inflateStoredOracle(t testing.TB, value []byte) ([]byte, []int) {
	t.Helper()
	if len(value) < 11 || value[0] != 0x78 || value[1] != 0x01 {
		t.Fatalf("zlib header=%x", value)
	}
	offset := 2
	var raw []byte
	var blocks []int
	for {
		if len(value)-offset < 5 {
			t.Fatalf("truncated block at %d", offset)
		}
		header := value[offset]
		if header != 0 && header != 1 {
			t.Fatalf("stored header=%02x", header)
		}
		final := header == 1
		length := int(binary.LittleEndian.Uint16(value[offset+1 : offset+3]))
		complement := binary.LittleEndian.Uint16(value[offset+3 : offset+5])
		if complement != ^uint16(length) || length > len(value)-offset-5-4 {
			t.Fatalf("LEN=%d NLEN=%04x remaining=%d", length, complement, len(value)-offset)
		}
		offset += 5
		raw = append(raw, value[offset:offset+length]...)
		blocks = append(blocks, length)
		offset += length
		if final {
			break
		}
	}
	if len(value)-offset != 4 {
		t.Fatalf("adler bytes=%d", len(value)-offset)
	}
	want := binary.BigEndian.Uint32(value[offset:])
	if got := adler32.Checksum(raw); got != want {
		t.Fatalf("adler=%08x want=%08x", got, want)
	}
	return raw, blocks
}

func outputFrame(t testing.TB, width, height int, pixels []cpucompositor.Pixel) color.OutputResult {
	t.Helper()
	var surface *cpucompositor.Surface
	var err error
	if pixels == nil {
		surface, err = cpucompositor.NewSurfaceWithLimits(width, height, cpucompositor.Limits{
			MaxWidth: width, MaxHeight: height, MaxBytes: uint64(width * height * int(cpucompositor.BytesPerPixel)),
		})
	} else {
		surface, err = cpucompositor.NewSurfaceFromPixelsWithLimits(width, height, pixels, cpucompositor.Limits{
			MaxWidth: width, MaxHeight: height, MaxBytes: uint64(width * height * int(cpucompositor.BytesPerPixel)),
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	result, err := color.ConvertSurfaceToRGBA8(context.Background(), surface, color.OutputLimits{
		MaxWidth: width, MaxHeight: height, MaxOutputBytes: uint64(width * height * 4),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) { return len(value) / 2, nil }

var errWriter = errors.New("writer failed")

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errWriter }

type invalidCountWriter struct {
	count int
	err   error
}

func (writer invalidCountWriter) Write([]byte) (int, error) { return writer.count, writer.err }

type mutatingRetainingWriter struct {
	retained []byte
}

func (writer *mutatingRetainingWriter) Write(value []byte) (int, error) {
	if len(value) > 0 {
		value[0] ^= 0xff
	}
	writer.retained = value
	return len(value), nil
}

type stepCancelContext struct {
	calls       int
	cancelAfter int
}

func (*stepCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*stepCancelContext) Done() <-chan struct{}       { return nil }
func (*stepCancelContext) Value(any) any               { return nil }
func (ctx *stepCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAfter {
		return context.Canceled
	}
	return nil
}
