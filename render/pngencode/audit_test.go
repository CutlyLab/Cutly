package pngencode

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"hash/crc32"
	"io"
	"math"
	"testing"
)

func TestStoredZlibExactBlockBoundaries(t *testing.T) {
	if _, _, err := checkedStoredLayout(0); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("zero scanline layout error=%v", err)
	}
	if writer, err := newStoredZlibWriter(context.Background(), make([]byte, 11), 0); writer != nil || !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("zero writer=%v err=%v", writer, err)
	}
	tests := []struct {
		name       string
		rawBytes   int
		wantIDAT   uint64
		wantBlocks []int
	}{
		{name: "one", rawBytes: 1, wantIDAT: 12, wantBlocks: []int{1}},
		{name: "max-minus-one", rawBytes: 65534, wantIDAT: 65545, wantBlocks: []int{65534}},
		{name: "max", rawBytes: 65535, wantIDAT: 65546, wantBlocks: []int{65535}},
		{name: "max-plus-one", rawBytes: 65536, wantIDAT: 65552, wantBlocks: []int{65535, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockCount, idatBytes, err := checkedStoredLayout(uint64(test.rawBytes))
			if err != nil {
				t.Fatal(err)
			}
			if idatBytes != test.wantIDAT || blockCount != uint64(len(test.wantBlocks)) {
				t.Fatalf("blocks=%d idat=%d, want %d/%d", blockCount, idatBytes, len(test.wantBlocks), test.wantIDAT)
			}
			raw := make([]byte, test.rawBytes)
			for index := range raw {
				raw[index] = byte(index*131 + 17)
			}
			encoded := make([]byte, int(idatBytes))
			writer, err := newStoredZlibWriter(context.Background(), encoded, uint64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < len(raw); {
				end := offset + 7919
				if end > len(raw) {
					end = len(raw)
				}
				if err := writer.Write(raw[offset:end]); err != nil {
					t.Fatal(err)
				}
				offset = end
			}
			if err := writer.Finish(); err != nil {
				t.Fatal(err)
			}
			assertStoredStreamExact(t, encoded, raw, test.wantBlocks)
		})
	}
}

func TestCheckedStoredLayoutPNG31BitAnd32BitBoundaries(t *testing.T) {
	const largestScanline = uint64(32766 * 65535)
	blocks, idatBytes, err := checkedStoredLayout(largestScanline)
	if err != nil {
		t.Fatal(err)
	}
	if blocks != 32766 || idatBytes != math.MaxInt32-1 {
		t.Fatalf("largest blocks=%d idat=%d", blocks, idatBytes)
	}
	if _, _, err := checkedStoredLayout(largestScanline + 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("next scanline error=%v", err)
	}
	if _, _, err := checkedStoredLayout(math.MaxUint64); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("overflow scanline error=%v", err)
	}

	defaultScanlineUpper := DefaultMaxRawRGBABytes + DefaultMaxHeight
	defaultBlocks := (defaultScanlineUpper + StoredBlockMaxBytes - 1) / StoredBlockMaxBytes
	defaultArtifactUpper := ZlibFixedBytes + defaultScanlineUpper + defaultBlocks*5 + FixedArtifactBytes
	defaultWorkingUpper := defaultArtifactUpper + DefaultMaxWidth*4
	if defaultArtifactUpper > DefaultMaxArtifactBytes || defaultWorkingUpper > DefaultMaxWorkingBytes {
		t.Fatalf("default budgets do not cover their declared raw/dimension maxima: artifact=%d working=%d", defaultArtifactUpper, defaultWorkingUpper)
	}

	hardScanlineUpper := HardMaxRawRGBABytes + HardMaxDimension
	hardBlocks := (hardScanlineUpper + StoredBlockMaxBytes - 1) / StoredBlockMaxBytes
	hardArtifactUpper := ZlibFixedBytes + hardScanlineUpper + hardBlocks*5 + FixedArtifactBytes
	hardWorkingUpper := hardArtifactUpper + HardMaxDimension*4
	if hardArtifactUpper > math.MaxInt32 || hardWorkingUpper > math.MaxInt32 {
		t.Fatalf("actual hard raw/dimension path is not 32-bit representable: artifact=%d working=%d", hardArtifactUpper, hardWorkingUpper)
	}
}

func TestIDATCRCIsIncrementalCancelableAndExact(t *testing.T) {
	value := make([]byte, ArtifactWriteChunkBytes*2+1)
	for index := range value {
		value[index] = byte(index*17 + 3)
	}
	got, err := checksumIEEEWithContext(context.Background(), value, "IDAT/crc")
	if err != nil {
		t.Fatal(err)
	}
	if want := crc32.ChecksumIEEE(value); got != want {
		t.Fatalf("crc=%08x want=%08x", got, want)
	}
	ctx := &stepCancelContext{cancelAfter: 2}
	if _, err := checksumIEEEWithContext(ctx, value, "IDAT/crc"); !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	} else {
		var encoded *Error
		if !errors.As(err, &encoded) || encoded.Path != "IDAT/crc" || encoded.Code != CodeCanceled {
			t.Fatalf("cancel diagnostic=%v", err)
		}
	}
}

func TestSingleWideRowCancellationAndExactWorkingBudget(t *testing.T) {
	input := outputFrame(t, HardMaxDimension, 1, nil)
	limits := DefaultLimits()
	limits.MaxWidth = HardMaxDimension
	limits.MaxRawRGBABytes = HardMaxDimension * 4
	limits.MaxArtifactBytes = 512 << 10
	limits.MaxWorkingBytes = 768 << 10
	ctx := &stepCancelContext{cancelAfter: 4}
	result, err := Encode(ctx, input, limits)
	if result != (Result{}) || !errors.Is(err, ErrCanceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var encoded *Error
	if !errors.As(err, &encoded) || encoded.Path != "input/pixel_hash" {
		t.Fatalf("cancel path=%v", err)
	}

	one := outputFrame(t, 1, 1, nil)
	oneLimits := DefaultLimits()
	oneLimits.MaxArtifactBytes = 86
	oneLimits.MaxWorkingBytes = 89
	if result, err := Encode(context.Background(), one, oneLimits); result != (Result{}) || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("working budget result=%+v err=%v", result, err)
	}
	oneLimits.MaxWorkingBytes = 90
	if result, err := Encode(context.Background(), one, oneLimits); err != nil || result.WorkingBytes != 90 {
		t.Fatalf("exact working budget result=%+v err=%v", result, err)
	}
}

func TestArtifactWriteToPreservesContentAgainstMutatingWriter(t *testing.T) {
	result, err := Encode(context.Background(), outputFrame(t, 8192, 2, nil), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want, err := result.Artifact.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	writer := &capturingMutatingWriter{}
	written, err := result.Artifact.WriteTo(writer)
	if err != nil || written != int64(len(want)) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if !bytes.Equal(writer.captured, want) {
		t.Fatal("writer did not receive the original complete artifact")
	}
	for _, retained := range writer.retained {
		for index := range retained {
			retained[index] ^= 0x5a
		}
	}
	again, err := result.Artifact.Bytes()
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("retained writer alias changed artifact: err=%v", err)
	}
}

func FuzzStoredZlibWriterRoundTrip(f *testing.F) {
	f.Add([]byte{1}, uint16(1))
	f.Add(make([]byte, 65534), uint16(7919))
	f.Add(make([]byte, 65535), uint16(32768))
	f.Add(make([]byte, 65536), uint16(65535))
	f.Fuzz(func(t *testing.T, raw []byte, split uint16) {
		if len(raw) > 131072 {
			raw = raw[:131072]
		}
		if len(raw) == 0 {
			if _, _, err := checkedStoredLayout(0); !errors.Is(err, ErrInputInvalid) {
				t.Fatalf("empty error=%v", err)
			}
			return
		}
		_, idatBytes, err := checkedStoredLayout(uint64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		encoded := make([]byte, int(idatBytes))
		writer, err := newStoredZlibWriter(context.Background(), encoded, uint64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		chunkSize := int(split)
		if chunkSize == 0 {
			chunkSize = 1
		}
		for offset := 0; offset < len(raw); {
			end := offset + chunkSize
			if end > len(raw) {
				end = len(raw)
			}
			if err := writer.Write(raw[offset:end]); err != nil {
				t.Fatal(err)
			}
			offset = end
		}
		if err := writer.Finish(); err != nil {
			t.Fatal(err)
		}
		reader, err := zlib.NewReader(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		inflated, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(inflated, raw) {
			t.Fatalf("inflated=%d raw=%d readErr=%v closeErr=%v", len(inflated), len(raw), readErr, closeErr)
		}
	})
}

func FuzzCheckedStoredLayoutNeverOverflows(f *testing.F) {
	f.Add(uint64(0))
	f.Add(uint64(1))
	f.Add(uint64(65535))
	f.Add(uint64(65536))
	f.Add(uint64(32766 * 65535))
	f.Add(uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, scanlineBytes uint64) {
		blocks, idatBytes, err := checkedStoredLayout(scanlineBytes)
		if err != nil {
			if blocks != 0 || idatBytes != 0 {
				t.Fatalf("failed layout returned blocks=%d idat=%d", blocks, idatBytes)
			}
			return
		}
		wantBlocks := scanlineBytes / StoredBlockMaxBytes
		if scanlineBytes%StoredBlockMaxBytes != 0 {
			wantBlocks++
		}
		if blocks != wantBlocks || idatBytes != ZlibFixedBytes+scanlineBytes+blocks*5 ||
			idatBytes > HardMaxIDATBytes {
			t.Fatalf("scanline=%d blocks=%d idat=%d", scanlineBytes, blocks, idatBytes)
		}
	})
}

func assertStoredStreamExact(t testing.TB, encoded, raw []byte, wantBlocks []int) {
	t.Helper()
	if len(encoded) < 11 || !bytes.Equal(encoded[:2], []byte{0x78, 0x01}) {
		t.Fatalf("zlib header=%x", encoded)
	}
	offset := 2
	rawOffset := 0
	for blockIndex, wantLength := range wantBlocks {
		if offset+5 > len(encoded)-4 {
			t.Fatalf("truncated block %d", blockIndex)
		}
		wantHeader := byte(0)
		if blockIndex == len(wantBlocks)-1 {
			wantHeader = 1
		}
		if encoded[offset] != wantHeader {
			t.Fatalf("block %d header=%02x want=%02x", blockIndex, encoded[offset], wantHeader)
		}
		length := int(binary.LittleEndian.Uint16(encoded[offset+1 : offset+3]))
		complement := binary.LittleEndian.Uint16(encoded[offset+3 : offset+5])
		if length != wantLength || complement != ^uint16(length) {
			t.Fatalf("block %d LEN=%d NLEN=%04x want=%d/%04x", blockIndex, length, complement, wantLength, ^uint16(wantLength))
		}
		offset += 5
		if offset+length > len(encoded)-4 || !bytes.Equal(encoded[offset:offset+length], raw[rawOffset:rawOffset+length]) {
			t.Fatalf("block %d payload mismatch", blockIndex)
		}
		offset += length
		rawOffset += length
	}
	if rawOffset != len(raw) || offset+4 != len(encoded) {
		t.Fatalf("rawOffset=%d/%d offset=%d/%d", rawOffset, len(raw), offset, len(encoded))
	}
	if got, want := binary.BigEndian.Uint32(encoded[offset:]), adler32.Checksum(raw); got != want {
		t.Fatalf("adler=%08x want=%08x", got, want)
	}
	reader, err := zlib.NewReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	inflated, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(inflated, raw) {
		t.Fatalf("stdlib inflated=%d readErr=%v closeErr=%v", len(inflated), readErr, closeErr)
	}
}

type capturingMutatingWriter struct {
	captured []byte
	retained [][]byte
}

func (writer *capturingMutatingWriter) Write(value []byte) (int, error) {
	writer.captured = append(writer.captured, value...)
	writer.retained = append(writer.retained, value)
	for index := range value {
		value[index] ^= 0xa5
	}
	return len(value), nil
}
