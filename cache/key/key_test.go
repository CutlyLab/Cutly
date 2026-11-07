package key

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestStageKeyV1CanonicalFixture(t *testing.T) {
	raw := readFixture(t, "stage-key-v1.input.canonical.json")
	input, err := DecodeInput(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	canonicalJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), canonicalJSON) {
		t.Fatalf("input JSON fixture is not canonical\nwant %s\n got %s", canonicalJSON, bytes.TrimSpace(raw))
	}
	canonical, err := CanonicalBinary(input)
	if err != nil {
		t.Fatal(err)
	}
	wantHex := strings.TrimSpace(string(readFixture(t, "stage-key-v1.canonical.hex")))
	if got := hex.EncodeToString(canonical); got != wantHex {
		t.Fatalf("canonical binary drift\nwant %s\n got %s", wantHex, got)
	}
	value, err := New(input)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := strings.TrimSpace(string(readFixture(t, "stage-key-v1.digest")))
	if value.Digest != wantDigest {
		t.Fatalf("digest drift: want %s got %s", wantDigest, value.Digest)
	}
	if err := Verify(input, value); err != nil {
		t.Fatal(err)
	}
}

func TestStageKeyBindsEverySemanticFieldButNotJobOrArtifactLocation(t *testing.T) {
	input := fixtureInput()
	base, err := New(input)
	if err != nil {
		t.Fatal(err)
	}
	type volatileRequest struct {
		JobID       string
		ArtifactURI string
		Input       Input
	}
	first := volatileRequest{JobID: "job-a", ArtifactURI: "file:///one/output.mp4", Input: input}
	second := volatileRequest{JobID: "job-b", ArtifactURI: "s3://bucket/two.mp4", Input: input}
	firstKey, _ := New(first.Input)
	secondKey, _ := New(second.Input)
	if firstKey != secondKey || firstKey != base {
		t.Fatalf("volatile scheduling identity changed cache key: %+v %+v", firstKey, secondKey)
	}

	mutations := []struct {
		name   string
		mutate func(*Input)
	}{
		{name: "namespace", mutate: func(value *Input) { value.Namespace = "cutly.cache.render-frame.v2" }},
		{name: "artifact kind", mutate: func(value *Input) { value.ArtifactKind = "cutly.cache-artifact.rgba-frame-group.v2" }},
		{name: "plan", mutate: func(value *Input) { value.PlanFingerprint = digest('1') }},
		{name: "execution", mutate: func(value *Input) { value.ExecutionManifestFingerprint = digest('2') }},
		{name: "stage profile", mutate: func(value *Input) { value.StageProfile = "cutly.compose.cpu-linear.v2" }},
		{name: "input hash", mutate: func(value *Input) { value.Inputs[0].ContentHash = digest('3') }},
		{name: "input role", mutate: func(value *Input) { value.Inputs[2].Role = "glyph" }},
		{name: "frame range", mutate: func(value *Input) { value.FrameRange.Count++ }},
		{name: "sample range", mutate: func(value *Input) { value.SampleRange.Start++ }},
		{name: "output profile", mutate: func(value *Input) { value.OutputProfile = "cutly.surface.rgba16-linear-bt709.v2" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneInput(input)
			test.mutate(&changed)
			got, err := New(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest == base.Digest {
				t.Fatalf("mutation did not change digest: %+v", changed)
			}
		})
	}
}

func TestStageKeyStrictDecodeAndCanonicalOrdering(t *testing.T) {
	raw := bytes.TrimSpace(readFixture(t, "stage-key-v1.input.canonical.json"))
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unknown", raw: append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)},
		{name: "duplicate", raw: append([]byte(`{"schema_version":1,`), raw[1:]...)},
		{name: "case alias", raw: bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"Schema_Version"`), 1)},
		{name: "uppercase digest", raw: bytes.Replace(raw, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("A", 64)), 1)},
		{name: "missing required zero ordinal", raw: bytes.Replace(raw, []byte(`"ordinal":0,`), nil, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeInput(test.raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	input := fixtureInput()
	input.Inputs[0], input.Inputs[1] = input.Inputs[1], input.Inputs[0]
	if _, err := New(input); !errors.Is(err, ErrCanonical) {
		t.Fatalf("unsorted input error = %v", err)
	}
	input = fixtureInput()
	input.Inputs[1].Ordinal = 2
	if _, err := New(input); !errors.Is(err, ErrCanonical) {
		t.Fatalf("ordinal gap error = %v", err)
	}
	input = fixtureInput()
	input.Inputs = nil
	if _, err := New(input); !errors.Is(err, ErrCanonical) {
		t.Fatalf("implicit null inputs error = %v", err)
	}
	input.Inputs = []NormalizedInput{}
	if _, err := New(input); err != nil {
		t.Fatalf("explicit empty normalized input array rejected: %v", err)
	}
}

func TestStageKeyNormalizedInputLimitNNPlusOne(t *testing.T) {
	input := fixtureInput()
	input.Inputs = make([]NormalizedInput, MaxNormalizedInputs)
	for index := range input.Inputs {
		input.Inputs[index] = NormalizedInput{Role: "source", Ordinal: uint32(index), ContentHash: digest(byte('a' + index%6))}
	}
	if _, err := New(input); err != nil {
		t.Fatalf("N=%d rejected: %v", MaxNormalizedInputs, err)
	}
	input.Inputs = append(input.Inputs, NormalizedInput{Role: "source", Ordinal: MaxNormalizedInputs, ContentHash: digest('f')})
	if _, err := New(input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("N+1 error = %v, want ErrInvalid", err)
	}
}

func fixtureInput() Input {
	return Input{
		SchemaVersion:                SchemaVersion,
		Profile:                      ProfileV1,
		Namespace:                    "cutly.cache.render-chunk.v1",
		ArtifactKind:                 "cutly.cache-artifact.rgba-frame-group.v1",
		PlanFingerprint:              digest('a'),
		ExecutionManifestFingerprint: digest('b'),
		StageProfile:                 "cutly.compose.cpu-linear.v1",
		Inputs: []NormalizedInput{
			{Role: "asset", Ordinal: 0, ContentHash: digest('c')},
			{Role: "asset", Ordinal: 1, ContentHash: digest('d')},
			{Role: "font", Ordinal: 0, ContentHash: digest('e')},
		},
		FrameRange:    &IndexRange{Start: 120, Count: 48},
		SampleRange:   &IndexRange{Start: 192000, Count: 76800},
		OutputProfile: "cutly.surface.rgba16-linear-bt709.v1",
	}
}

func cloneInput(input Input) Input {
	result := input
	result.Inputs = append([]NormalizedInput(nil), input.Inputs...)
	if input.FrameRange != nil {
		frame := *input.FrameRange
		result.FrameRange = &frame
	}
	if input.SampleRange != nil {
		sample := *input.SampleRange
		result.SampleRange = &sample
	}
	return result
}

func digest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func ExampleNew() {
	value, _ := New(fixtureInput())
	fmt.Println(strings.HasPrefix(value.Digest, "sha256:"))
	// Output: true
}
