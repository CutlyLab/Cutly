package textlayer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

func TestCanonicalProfileAndEmptyManifestFixtures(t *testing.T) {
	profile := textlayer.DescriptorCanonicalJSON()
	wantProfile, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read profile fixture: %v\nactual=%s", err, profile)
	}
	wantProfile = bytes.TrimSuffix(wantProfile, []byte("\n"))
	if !bytes.Equal(profile, wantProfile) {
		t.Fatalf("profile fixture drift\nactual=%s\nwant=%s", profile, wantProfile)
	}
	var decodedProfile textlayer.ProfileDescriptor
	if err := wirejson.Decode(wantProfile, &decodedProfile); err != nil || decodedProfile != textlayer.Descriptor() {
		t.Fatalf("strict profile decode: decoded=%+v err=%v", decodedProfile, err)
	}

	ctx := context.Background()
	_, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	document.Timelines[0].Tracks[2].Clips[0].Disabled = true
	provider, err := textlayer.Compile(ctx, document, nil, textlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	emptyManifest, err := videoedit.CanonicalJSON(provider.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	wantManifest, err := os.ReadFile("testdata/empty-manifest-v1.canonical.json")
	if err != nil {
		t.Fatalf("read manifest fixture: %v\nactual=%s", err, emptyManifest)
	}
	wantManifest = bytes.TrimSuffix(wantManifest, []byte("\n"))
	if !bytes.Equal(emptyManifest, wantManifest) {
		t.Fatalf("manifest fixture drift\nactual=%s\nwant=%s", emptyManifest, wantManifest)
	}
}

func TestNonEmptyManifestCanonicalFixtureStrictRoundtripAndFrameSerialization(t *testing.T) {
	ctx := context.Background()
	font, fontHash := buildProvider(t, ctx)
	document := textDocument(t, fontHash)
	documentHash := freezeHash(t, document)
	provider, err := textlayer.Compile(ctx, document, []textlayer.FontBinding{{
		DocumentHash: documentHash, FontAssetID: "asset-font", Provider: font,
	}}, textlayer.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	manifest := provider.Manifest()
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/manifest-v1.canonical.json")
	if err != nil {
		t.Fatalf("read non-empty manifest fixture: %v\nactual=%s", err, canonical)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(canonical, want) {
		t.Fatalf("non-empty manifest fixture drift\nactual=%s\nwant=%s", canonical, want)
	}
	digest := sha256.Sum256(canonical)
	if got, wantHash := "sha256:"+hex.EncodeToString(digest[:]), "sha256:b0b8746f802b97748509f27a85c6be03a7e4870da15327777c2fd7841e847250"; got != wantHash {
		t.Fatalf("manifest fixture hash=%q want=%q", got, wantHash)
	}
	var decoded textlayer.Manifest
	if err := wirejson.Decode(want, &decoded); err != nil {
		t.Fatalf("strict manifest decode: %v", err)
	}
	roundtrip, err := videoedit.CanonicalJSON(decoded)
	if err != nil || !bytes.Equal(roundtrip, want) {
		t.Fatalf("manifest roundtrip drift err=%v\nactual=%s", err, roundtrip)
	}
	hostile := bytes.Replace(want, []byte(`"schema_version":1`), []byte(`"unknown":true,"schema_version":1`), 1)
	if bytes.Equal(hostile, want) {
		t.Fatal("hostile replacement did not match")
	}
	if err := wirejson.Decode(hostile, &decoded); err == nil {
		t.Fatal("strict manifest decode accepted unknown field")
	}

	request := frameRequest(t, document, manifest.Assets[0], 0, documentv2.RationalTime{Value: 1, Rate: 1})
	frame, err := provider.Frame(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := videoedit.CanonicalJSON(frame)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{[]byte(`"receipt":`), []byte(`"request":`), []byte(`"assembly":`), []byte(`"raster":`), []byte(`"surface_identity":`)} {
		if !bytes.Contains(serialized, required) {
			t.Fatalf("Frame serialization omitted %s: %s", required, serialized)
		}
	}
	if bytes.Contains(serialized, []byte(`"surface":`)) {
		t.Fatalf("Frame serialization leaked live surface: %s", serialized)
	}
	var decodedFrame textlayer.Frame
	if err := wirejson.Decode(serialized, &decodedFrame); err != nil || decodedFrame.Surface != nil || decodedFrame.Receipt != frame.Receipt {
		t.Fatalf("strict Frame decode: decoded=%+v err=%v", decodedFrame.Receipt, err)
	}
	frameRoundtrip, err := videoedit.CanonicalJSON(decodedFrame)
	if err != nil || !bytes.Equal(frameRoundtrip, serialized) {
		t.Fatalf("Frame roundtrip drift err=%v\nactual=%s", err, frameRoundtrip)
	}
	hostileFrame := bytes.Replace(serialized, []byte(`"receipt":`), []byte(`"unknown":true,"receipt":`), 1)
	if bytes.Equal(hostileFrame, serialized) {
		t.Fatal("hostile Frame replacement did not match")
	}
	if err := wirejson.Decode(hostileFrame, &decodedFrame); err == nil {
		t.Fatal("strict Frame decode accepted unknown field")
	}
}
