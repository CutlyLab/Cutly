package wavencode

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/render/pcm16le"
)

type vectorCase struct {
	Name                   string      `json:"name"`
	SampleRate             int64       `json:"sample_rate"`
	FirstCompositionSample int64       `json:"first_composition_sample"`
	Inputs                 [][2]string `json:"inputs"`
	ExpectedPCMHex         string      `json:"expected_pcm_hex"`
	ExpectedPCMHash        string      `json:"expected_pcm_hash"`
	ExpectedWAVHex         string      `json:"expected_wav_hex"`
	ExpectedWAVHash        string      `json:"expected_wav_hash"`
}

type vectorFixture struct {
	Profile                  string       `json:"profile"`
	InputQuantizationProfile string       `json:"input_quantization_profile"`
	RoundTripDecodeProfile   string       `json:"round_trip_decode_profile"`
	Cases                    []vectorCase `json:"cases"`
}

func TestCanonicalEncodeVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/encode-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture vectorFixture
	if err := wirejson.Decode(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	canonical, err := videoedit.CanonicalJSON(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonical) || fixture.Profile != ProfileV1 ||
		fixture.InputQuantizationProfile != pcm16le.ProfileV1 ||
		fixture.RoundTripDecodeProfile != wavdecode.ProfileV1 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid/noncanonical fixture\ncanonical=%s", canonical)
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			pcm := quantize(t, vector.SampleRate, vector.FirstCompositionSample, vector.Inputs)
			pcmBytes, err := pcm.PCM.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(pcmBytes) != vector.ExpectedPCMHex || pcm.PCMHash != vector.ExpectedPCMHash {
				t.Fatalf("pcm hex=%s hash=%s", hex.EncodeToString(pcmBytes), pcm.PCMHash)
			}
			result, err := Encode(context.Background(), pcm, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := result.Artifact.Bytes()
			if hex.EncodeToString(encoded) != vector.ExpectedWAVHex || result.ArtifactHash != vector.ExpectedWAVHash {
				t.Fatalf("wav hex=%s hash=%s", hex.EncodeToString(encoded), result.ArtifactHash)
			}
			decoded, err := wavdecode.Decode(context.Background(), encoded, wavdecode.Limits{MaxSampleRate: HardMaxSampleRate})
			if err != nil || decoded.FrameCount != result.FrameCount || decoded.EncodedContentHash != result.ArtifactHash {
				t.Fatalf("decoded=%+v err=%v", decoded, err)
			}
		})
	}
}

func TestHeaderMatchesIndependentOracleAndRIFFBoundaries(t *testing.T) {
	for _, sampleRate := range []int64{1, 8_000, 48_000, 192_000, int64(HardMaxSampleRate)} {
		for _, frames := range []int{0, 1, 7} {
			values := make([][2]string, frames)
			for index := range values {
				values[index] = [2]string{"1/4", "-1/4"}
			}
			pcm := quantize(t, sampleRate, -4, values)
			result, err := Encode(context.Background(), pcm, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := result.Artifact.Bytes()
			payload, _ := pcm.PCM.Bytes()
			want := oracleStrictWAV(uint32(sampleRate), payload)
			if !bytes.Equal(actual, want) {
				t.Fatalf("rate=%d frames=%d\nactual=%x\nwant=%x", sampleRate, frames, actual, want)
			}
		}
	}

	limits := Limits{MaxDataBytes: HardMaxDataBytes, MaxArtifactBytes: HardMaxArtifactBytes}
	if HardMaxDataBytes+HeaderBytes != HardMaxArtifactBytes || HardMaxArtifactBytes != uint64(^uint32(0))+8 {
		t.Fatalf("hard limits data=%d artifact=%d", HardMaxDataBytes, HardMaxArtifactBytes)
	}
	if normalized, err := normalizeLimits(Limits{}); err != nil || normalized != DefaultLimits() {
		t.Fatalf("default limits=%+v err=%v", normalized, err)
	}
	for _, invalid := range []Limits{
		{MaxDataBytes: HardMaxDataBytes + 1, MaxArtifactBytes: HardMaxArtifactBytes},
		{MaxDataBytes: HardMaxDataBytes, MaxArtifactBytes: HardMaxArtifactBytes + 1},
		{MaxDataBytes: 1, MaxArtifactBytes: HeaderBytes - 1},
		{MaxDataBytes: 5, MaxArtifactBytes: HeaderBytes + 4},
	} {
		if _, err := normalizeLimits(invalid); err == nil {
			t.Fatalf("invalid hard limits accepted: %+v", invalid)
		}
	}
	maxFrames := HardMaxDataBytes / pcm16le.BytesPerFrame
	layout, err := checkedLayout(maxFrames, int64(HardMaxSampleRate), limits)
	if err != nil {
		t.Fatal(err)
	}
	if layout.dataBytes != 4_294_967_256 || layout.artifactBytes != 4_294_967_300 ||
		layout.dataBytes%pcm16le.BytesPerFrame != 0 {
		t.Fatalf("max layout=%+v", layout)
	}
	var header [HeaderBytes]byte
	writeHeader(header[:], layout.sampleRate, uint32(layout.dataBytes), layout.byteRate)
	if binary.LittleEndian.Uint32(header[4:8]) != uint32(layout.artifactBytes-8) ||
		binary.LittleEndian.Uint32(header[40:44]) != uint32(layout.dataBytes) {
		t.Fatalf("max header=%x", header)
	}
	if _, err := checkedLayout(maxFrames+1, int64(HardMaxSampleRate), limits); err == nil {
		t.Fatal("one frame past RIFF limit succeeded")
	}
	if _, err := checkedLayout(0, int64(HardMaxSampleRate)+1, limits); err == nil {
		t.Fatal("sample rate past decoder hard maximum succeeded")
	}
}

func oracleStrictWAV(sampleRate uint32, payload []byte) []byte {
	result := make([]byte, 0, HeaderBytes+len(payload))
	result = append(result, 'R', 'I', 'F', 'F')
	result = appendUint32LE(result, uint32(36+len(payload)))
	result = append(result, 'W', 'A', 'V', 'E', 'f', 'm', 't', ' ')
	result = appendUint32LE(result, 16)
	result = appendUint16LE(result, 1)
	result = appendUint16LE(result, 2)
	result = appendUint32LE(result, sampleRate)
	result = appendUint32LE(result, sampleRate*4)
	result = appendUint16LE(result, 4)
	result = appendUint16LE(result, 16)
	result = append(result, 'd', 'a', 't', 'a')
	result = appendUint32LE(result, uint32(len(payload)))
	return append(result, payload...)
}

func appendUint16LE(destination []byte, value uint16) []byte {
	return append(destination, byte(value), byte(value>>8))
}

func appendUint32LE(destination []byte, value uint32) []byte {
	return append(destination, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
}
