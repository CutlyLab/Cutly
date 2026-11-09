package resourceidentityv2_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	identity "github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

func externalBinding(kind documentv2.AssetKind, encoded []byte) identity.Binding {
	return identity.Binding{AssetID: "asset", Kind: kind, ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), SizeBytes: int64(len(encoded))}
}
func externalClaim(r identity.Receipt) renderapi.MaterializedResourceV2 {
	profile := renderapi.ImageMaterializationProfileV2
	if r.Resource.Kind == documentv2.AssetKindFont {
		profile = renderapi.FontMaterializationProfileV2
	}
	fingerprint := r.Fingerprint
	return renderapi.MaterializedResourceV2{AssetID: r.Resource.AssetID, Kind: r.Resource.Kind, ContentHash: r.Resource.ContentHash, SizeBytes: r.Resource.SizeBytes, MaterializationProfile: profile, InspectionFingerprint: &fingerprint}
}

func TestPublicSurfaceHeaderCannotOverwriteObservation(t *testing.T) {
	encoded, e := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489000000017352474200aece1ce90000000d49444154789c63f8cfc0d00000048101802c55ceb00000000049454e44ae426082")
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	expected := externalBinding(documentv2.AssetKindImage, encoded)
	actual, e := identity.InspectPNG(ctx, expected, encoded, pngdecode.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := actual.Receipt()
	if e != nil {
		t.Fatal(e)
	}
	first, e := actual.Surface()
	if e != nil {
		t.Fatal(e)
	}
	second, e := actual.Surface()
	if e != nil {
		t.Fatal(e)
	}
	if first == second {
		t.Fatal("getters share an overwriteable header")
	}
	replacement, e := cpucompositor.NewSurface(2, 1)
	if e != nil {
		t.Fatal(e)
	}
	*first = *replacement
	*second = cpucompositor.Surface{}
	assertOriginal := func() {
		t.Helper()
		surface, e := actual.Surface()
		if e != nil {
			t.Error(e)
			return
		}
		hash, e := surface.CanonicalHash()
		if e != nil || hash != receipt.PNG.PixelHash || surface.Width() != 1 || surface.Height() != 1 {
			t.Errorf("retained surface replaced: %s %v", hash, e)
		}
		if e = identity.Check(ctx, expected, externalClaim(receipt), actual); e != nil {
			t.Error(e)
		}
		if e = identity.Matches(ctx, expected, receipt, actual); e != nil {
			t.Error(e)
		}
	}
	assertOriginal()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 32; n++ {
				own, e := actual.Surface()
				if e != nil {
					t.Error(e)
					return
				}
				*own = *replacement
				*own = cpucompositor.Surface{}
				assertOriginal()
			}
		}()
	}
	wg.Wait()
	assertOriginal()
}

func TestPublicFontHeaderCannotOverwriteObservation(t *testing.T) {
	raw, e := os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	expected := externalBinding(documentv2.AssetKindFont, encoded)
	actual, e := identity.InspectFont(ctx, expected, encoded, sfntglyf.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := actual.Receipt()
	if e != nil {
		t.Fatal(e)
	}
	first, e := actual.Font()
	if e != nil {
		t.Fatal(e)
	}
	second, e := actual.Font()
	if e != nil {
		t.Fatal(e)
	}
	if first == second {
		t.Fatal("getters share an overwriteable header")
	}
	// An independently parsed nonzero font header is a valid caller replacement.
	replacement, _, e := sfntglyf.Parse(ctx, encoded, sfntglyf.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	*first = *replacement
	*second = sfntglyf.Font{}
	assertOriginal := func() {
		t.Helper()
		font, e := actual.Font()
		if e != nil {
			t.Error(e)
			return
		}
		if font.NumGlyphs() != receipt.Font.NumGlyphs || font.UnitsPerEm() != receipt.Font.UnitsPerEm {
			t.Error("retained font header replaced")
		}
		if gid, ok := font.NominalGlyph('A'); !ok || gid != 1 {
			t.Error("retained cmap replaced")
		}
		if metric, ok := font.HorizontalMetric(1); !ok || metric.AdvanceWidth != 500 {
			t.Error("retained metrics replaced")
		}
		if font.Receipt().ContentHash != expected.ContentHash {
			t.Error("retained font receipt replaced")
		}
		if e = identity.Check(ctx, expected, externalClaim(receipt), actual); e != nil {
			t.Error(e)
		}
		if e = identity.Matches(ctx, expected, receipt, actual); e != nil {
			t.Error(e)
		}
	}
	assertOriginal()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 32; n++ {
				own, e := actual.Font()
				if e != nil {
					t.Error(e)
					return
				}
				*own = *replacement
				*own = sfntglyf.Font{}
				assertOriginal()
			}
		}()
	}
	wg.Wait()
	assertOriginal()
}
