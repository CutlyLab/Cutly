package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/media/color"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/prepared"
)

func TestNativePreviewSelectsAndDecodesExactFrame(t *testing.T) {
	environment := prepared.EnvironmentV2{Manifest: api.ExecutionManifestV2{Output: api.OutputSpecV2{Width: 2, Height: 1}}}
	first := []byte{
		0, 1, 0, 2, 0, 3, 0, 4,
		0, 5, 0, 6, 0, 7, 0, 8,
	}
	second := []byte{
		0, 9, 0, 10, 0, 11, 0, 12,
		0, 13, 0, 14, 0, 15, 0, 16,
	}
	selected, err := nativePreviewFrameBytesV2(bytes.NewReader(append(first, second...)), environment, 40, 41)
	if err != nil || !bytes.Equal(selected, second) {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	surface, err := nativePreviewSurfaceV2(environment, selected)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := surface.Pixel(1, 0)
	if err != nil || pixel != (cpucompositor.Pixel{R: 13, G: 14, B: 15, A: 16}) {
		t.Fatalf("pixel=%+v err=%v", pixel, err)
	}
	if _, err := nativePreviewFrameBytesV2(bytes.NewReader(first), environment, 40, 41); err == nil {
		t.Fatal("truncated frame group was accepted")
	}
}

func TestPublishNativePreviewIsContentAddressedAndIdempotent(t *testing.T) {
	surface, err := cpucompositor.NewSurfaceFromPixels(1, 1, []cpucompositor.Pixel{{R: 1, G: 2, B: 3, A: 4}})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := color.ConvertSurfaceToRGBA8(context.Background(), surface, color.DefaultOutputLimits())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := pngencode.Encode(context.Background(), converted, pngencode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := publishNativePreviewV2(root, encoded.Artifact, encoded.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishNativePreviewV2(root, encoded.Artifact, encoded.ArtifactHash)
	if err != nil || first != second || !strings.HasPrefix(first, "file://") {
		t.Fatalf("first=%q second=%q err=%v", first, second, err)
	}
	path := filepath.Join(root, ".cutly-preview-v2", strings.TrimPrefix(encoded.ArtifactHash, "sha256:")+".png")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != encoded.ArtifactHash {
		t.Fatal("published preview hash differs")
	}
}
