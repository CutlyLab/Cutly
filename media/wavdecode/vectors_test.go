package wavdecode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

type vectorFixture struct {
	SchemaVersion int          `json:"schema_version"`
	Profile       string       `json:"profile"`
	Vectors       []testVector `json:"vectors"`
}

type testVector struct {
	Name                string       `json:"name"`
	EncodedWAVBase64    string       `json:"encoded_wav_base64"`
	SampleRate          uint32       `json:"sample_rate"`
	Channels            uint16       `json:"channels"`
	ChannelLayout       string       `json:"channel_layout"`
	DecoderChannelOrder ChannelOrder `json:"decoder_channel_order"`
	DecoderChannels     []string     `json:"decoder_channels"`
	FrameCount          uint64       `json:"frame_count"`
	EncodedContentHash  string       `json:"encoded_content_hash"`
	SampleHash          string       `json:"sample_hash"`
	InterleavedSamples  []int16      `json:"interleaved_samples"`
}

func TestDecodeCanonicalVectors(t *testing.T) {
	encoded := strictWAV(t, 48_000, 2, []int16{-32768, 32767, 0, -1, 1, 12345})
	result, err := Decode(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	actualFixture := vectorFixture{
		SchemaVersion: 1,
		Profile:       ProfileV1,
		Vectors: []testVector{{
			Name: "stereo-extremes", EncodedWAVBase64: base64.StdEncoding.EncodeToString(encoded),
			SampleRate: result.SampleRate, Channels: result.Channels, ChannelLayout: result.ChannelLayout,
			DecoderChannelOrder: result.DecoderChannelOrder,
			DecoderChannels:     result.PCM.ChannelOrder(), FrameCount: result.FrameCount,
			EncodedContentHash: result.EncodedContentHash, SampleHash: result.SampleHash,
			InterleavedSamples: []int16{-32768, 32767, 0, -1, 1, 12345},
		}},
	}
	actual, err := json.Marshal(actualFixture)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/decode-v1.vectors.canonical.json")
	if err != nil {
		t.Fatalf("read vectors fixture: %v\nactual %s", err, actual)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(actual, want) {
		t.Fatalf("vector fixture mismatch\nactual %s\nwant   %s", actual, want)
	}

	var parsed vectorFixture
	if err := json.Unmarshal(want, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, vector := range parsed.Vectors {
		decoded, err := base64.StdEncoding.DecodeString(vector.EncodedWAVBase64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(context.Background(), decoded, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if got.EncodedContentHash != vector.EncodedContentHash || got.SampleHash != vector.SampleHash ||
			got.SampleRate != vector.SampleRate || got.Channels != vector.Channels || got.FrameCount != vector.FrameCount ||
			got.ChannelLayout != vector.ChannelLayout || got.DecoderChannelOrder != vector.DecoderChannelOrder ||
			!equalStrings(got.PCM.ChannelOrder(), vector.DecoderChannels) {
			t.Fatalf("vector %q mismatch: %+v", vector.Name, got)
		}
		for index, wantSample := range vector.InterleavedSamples {
			frame := uint64(index / int(vector.Channels))
			channel := index % int(vector.Channels)
			gotSample, err := got.PCM.Sample(frame, channel)
			if err != nil || gotSample != wantSample {
				t.Fatalf("vector %q sample %d = %d, %v; want %d", vector.Name, index, gotSample, err, wantSample)
			}
		}
	}
}
