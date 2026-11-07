package canonicaljson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCanonicalizeProfileVector(t *testing.T) {
	raw := []byte(" {\n  \"\\ue000\": -0, \"z\": [true, null, \"a\\/b\\u0001\"], \"\\ud83d\\ude00\": 9223372036854775808, \"a\": false } ")
	want := []byte("{\"a\":false,\"z\":[true,null,\"a/b\\u0001\"],\"😀\":9223372036854775808,\"\":0}")
	got, err := Canonicalize(raw)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	digest, digestBytes, err := Digest(raw)
	if err != nil || !bytes.Equal(digestBytes, want) || digest != "sha256:7d0a35b65a58803530602efb807548b4546cff965895757cff44ed4f1d589c0b" {
		t.Fatalf("digest = %s, bytes = %s, err = %v", digest, digestBytes, err)
	}
	again, err := Canonicalize(got)
	if err != nil || !bytes.Equal(again, got) {
		t.Fatalf("canonicalization is not idempotent: %s, %v", again, err)
	}
}

func TestCanonicalizeRejectsAmbiguity(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		code Code
	}{
		{name: "duplicate decoded name", raw: []byte(`{"a":1,"\u0061":2}`), code: CodeDuplicateMember},
		{name: "fraction", raw: []byte(`1.0`), code: CodeUnsupportedNumber},
		{name: "exponent", raw: []byte(`1e3`), code: CodeUnsupportedNumber},
		{name: "leading zero", raw: []byte(`01`), code: CodeInvalidSyntax},
		{name: "lone high surrogate", raw: []byte(`"\ud800"`), code: CodeInvalidUnicode},
		{name: "lone low surrogate", raw: []byte(`"\udc00"`), code: CodeInvalidUnicode},
		{name: "trailing comma", raw: []byte(`{"a":1,}`), code: CodeInvalidSyntax},
		{name: "invalid utf8", raw: []byte{'"', 0xff, '"'}, code: CodeInvalidUnicode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Canonicalize(test.raw); !IsCode(err, test.code) {
				t.Fatalf("error = %v, want code %s", err, test.code)
			}
		})
	}
}

func TestCanonicalizeLimitsArePrepublication(t *testing.T) {
	limits := DefaultLimits
	limits.MaxDepth = 2
	if got, err := CanonicalizeWithLimits([]byte(`[[[0]]]`), limits); !IsCode(err, CodeResourceLimit) || got != nil {
		t.Fatalf("depth result = %q, %v", got, err)
	}

	limits = DefaultLimits
	limits.MaxValues = 2
	if got, err := CanonicalizeWithLimits([]byte(`[0,1]`), limits); !IsCode(err, CodeResourceLimit) || got != nil {
		t.Fatalf("value result = %q, %v", got, err)
	}

	limits = DefaultLimits
	limits.MaxStringBytes = 3
	if got, err := CanonicalizeWithLimits([]byte(`{"ab":"cd"}`), limits); !IsCode(err, CodeResourceLimit) || got != nil {
		t.Fatalf("string result = %q, %v", got, err)
	}

	limits = DefaultLimits
	limits.MaxIntegerDigits = 3
	if got, err := CanonicalizeWithLimits([]byte(`1000`), limits); !IsCode(err, CodeResourceLimit) || got != nil {
		t.Fatalf("integer result = %q, %v", got, err)
	}
}

func TestMarshalAndDigest(t *testing.T) {
	value := struct {
		B int    `json:"b"`
		A string `json:"a"`
	}{B: 2, A: "x"}
	canonical, err := Marshal(value)
	if err != nil || string(canonical) != `{"a":"x","b":2}` {
		t.Fatalf("Marshal = %s, %v", canonical, err)
	}
	if _, err := Marshal(struct {
		Value float64 `json:"value"`
	}{Value: 1.5}); !IsCode(err, CodeUnsupportedNumber) {
		t.Fatalf("float error = %v", err)
	}

	digest, got, err := Digest([]byte(` {"b":2,"a":"x"} `))
	if err != nil || !bytes.Equal(got, canonical) {
		t.Fatalf("Digest canonical = %s, %v", got, err)
	}
	sum := sha256.Sum256(canonical)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if digest != want || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %s, want %s", digest, want)
	}
}

func TestNestedContainersAtExactDepth(t *testing.T) {
	limits := DefaultLimits
	limits.MaxDepth = 3
	if got, err := CanonicalizeWithLimits([]byte(`{"a":[{"b":0}]}`), limits); err != nil || string(got) != `{"a":[{"b":0}]}` {
		t.Fatalf("exact depth = %s, %v", got, err)
	}
}

func TestGoMatchesCrossLanguageFixture(t *testing.T) {
	raw, err := os.ReadFile("../../conformance/fixtures/canonical-json-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	trimmed := bytes.TrimSpace(raw)
	fixtureCanonical, err := Canonicalize(trimmed)
	if err != nil || !bytes.Equal(fixtureCanonical, trimmed) {
		t.Fatalf("fixture is not canonical: %v", err)
	}
	var fixture struct {
		Profile string `json:"profile"`
		Cases   []struct {
			Name      string `json:"name"`
			Input     string `json:"input"`
			Canonical string `json:"canonical"`
			Digest    string `json:"digest"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(trimmed, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Profile != Profile {
		t.Fatalf("profile = %q, want %q", fixture.Profile, Profile)
	}
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			digest, canonical, err := Digest([]byte(vector.Input))
			if err != nil || string(canonical) != vector.Canonical || digest != vector.Digest {
				t.Fatalf("canonical = %q, digest = %q, err = %v", canonical, digest, err)
			}
		})
	}
}

func FuzzCanonicalize(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`null`),
		[]byte(`{"b":2,"a":1}`),
		[]byte(`{"a":1,"\u0061":2}`),
		[]byte(`[-0,9223372036854775808,"\ud83d\ude00"]`),
		{'"', 0xff, '"'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		canonical, err := Canonicalize(raw)
		if err != nil {
			if canonical != nil {
				t.Fatalf("failure published bytes %q", canonical)
			}
			return
		}
		again, err := Canonicalize(canonical)
		if err != nil {
			t.Fatalf("canonical bytes rejected: %q: %v", canonical, err)
		}
		if !bytes.Equal(again, canonical) {
			t.Fatalf("not idempotent: first %q, second %q", canonical, again)
		}
	})
}
