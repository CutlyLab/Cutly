package reconstructv2

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

func fontAdapterFixture(t testing.TB) (resourceidentityv2.Binding, *resourceidentityv2.Inspected, []byte) {
	t.Helper()
	raw, e := os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := hex.DecodeString(strings.TrimSpace(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	binding := resourceidentityv2.Binding{AssetID: "font", Kind: documentv2.AssetKindFont, ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), SizeBytes: int64(len(encoded))}
	actual, e := resourceidentityv2.InspectFont(context.Background(), binding, encoded, sfntglyf.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	return binding, actual, encoded
}
func fontAdapterDocumentHash() string { return "sha256:" + strings.Repeat("a", 64) }

func TestFontAdapterActualLineageMatchesNativeChain(t *testing.T) {
	expected, actual, encoded := fontAdapterFixture(t)
	ctx := context.Background()
	limits := defaultFontBuildLimits()
	binding, e := buildFontBinding(ctx, fontAdapterDocumentHash(), expected, actual, limits)
	if e != nil {
		t.Fatal(e)
	}
	if binding.DocumentHash != fontAdapterDocumentHash() || binding.FontAssetID != expected.AssetID || binding.Provider == nil {
		t.Fatal("missing authorization edge")
	}
	// Build an independent native chain as an oracle; no synthetic receipts.
	font, _, e := sfntglyf.Parse(ctx, encoded, sfntglyf.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	outlines, _, e := sfntglyf.DecodeOutlineFont(ctx, font, limits.Outline)
	if e != nil {
		t.Fatal(e)
	}
	geometry, _, e := sfntglyf.ValidateOutlineGeometry(ctx, outlines, limits.Geometry)
	if e != nil {
		t.Fatal(e)
	}
	set, _, e := sfntglyf.MaterializeOutlineSet(ctx, geometry, limits.OutlineSet)
	if e != nil {
		t.Fatal(e)
	}
	graph, _, e := sfntglyf.BuildLayoutGraph(ctx, set, limits.Layout)
	if e != nil {
		t.Fatal(e)
	}
	provider, want, e := sfntglyf.BindShapeProvider(ctx, graph, limits.Provider)
	if e != nil {
		t.Fatal(e)
	}
	if got := binding.Provider.Receipt(); got != want || got.FontContentHash != expected.ContentHash || got.EncodedBytes != uint64(expected.SizeBytes) {
		t.Fatal("native chain mismatch")
	}
	// Mutating caller bytes and an inspected getter's exported header cannot alter
	// the already-built chain or another reconstruction from the actual inspection.
	clear(encoded)
	getter, e := actual.Font()
	if e != nil {
		t.Fatal(e)
	}
	*getter = sfntglyf.Font{}
	input := sfntglyf.ShapeInput{Text: "A", Language: "en", Script: "Latn", Direction: sfntglyf.ShapeDirection("ltr")}
	shaped, sr, e := binding.Provider.Shape(ctx, input, sfntglyf.DefaultShapeLimits())
	if e != nil || shaped == nil {
		t.Fatal("actual provider unusable", e)
	}
	_, oracle, e := provider.Shape(ctx, input, sfntglyf.DefaultShapeLimits())
	if e != nil || !reflect.DeepEqual(sr, oracle) {
		t.Fatal("shaped identity mismatch", e)
	}
	again, e := buildFontBinding(ctx, fontAdapterDocumentHash(), expected, actual, limits)
	if e != nil || again.Provider.Receipt() != want {
		t.Fatal("ownership/reconstruction repeat", e)
	}
	if want.ProviderHash != "sha256:7ca8fba6f9a0c2f013ae2840cd34120a815316994f8d9ea6b80938819e7bfc74" || sr.OutputHash != "sha256:c020d20e6ecf9e63f20f04246d1f769e423bc2e86412e4c483649a5d8b73cb22" {
		t.Fatal("pinned native provider/shape identity changed")
	}
}

func TestFontAdapterBindingMismatchAndZeroFailure(t *testing.T) {
	expected, actual, _ := fontAdapterFixture(t)
	ctx := context.Background()
	limits := defaultFontBuildLimits()
	for name, mutate := range map[string]func(*resourceidentityv2.Binding){
		"asset": func(b *resourceidentityv2.Binding) { b.AssetID = "other" },
		"kind":  func(b *resourceidentityv2.Binding) { b.Kind = documentv2.AssetKindImage },
		"hash":  func(b *resourceidentityv2.Binding) { b.ContentHash = "sha256:" + strings.Repeat("b", 64) },
		"size":  func(b *resourceidentityv2.Binding) { b.SizeBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			b := expected
			mutate(&b)
			out, e := buildFontBinding(ctx, fontAdapterDocumentHash(), b, actual, limits)
			if e == nil || out != (textlayer.FontBinding{}) {
				t.Fatal("mismatch exposed binding", e)
			}
		})
	}
	for _, a := range []*resourceidentityv2.Inspected{nil, {}} {
		out, e := buildFontBinding(ctx, fontAdapterDocumentHash(), expected, a, limits)
		if e == nil || out != (textlayer.FontBinding{}) {
			t.Fatal("non-observation accepted")
		}
	}
	for _, hash := range []string{"", "document", "sha256:" + strings.Repeat("A", 64)} {
		out, e := buildFontBinding(ctx, hash, expected, actual, limits)
		if e == nil || out != (textlayer.FontBinding{}) {
			t.Fatal("invalid document hash accepted")
		}
	}
}

func TestFontAdapterLimitsAdmissionEnvelope(t *testing.T) {
	defaults := defaultFontBuildLimits()
	working, retained, e := fontBuildEnvelope(defaults)
	if e != nil || working != 96<<20 || retained != 96<<20 {
		t.Fatalf("default envelope %d %d %v", working, retained, e)
	}
	// Every native scalar must remain explicit: zero must never activate a larger
	// upstream default; unsupported widening/overflow is rejected before native work.
	v := reflect.ValueOf(defaults)
	for i := 0; i < v.NumField(); i++ {
		stage := v.Field(i)
		for j := 0; j < stage.NumField(); j++ {
			name := v.Type().Field(i).Name + "/" + stage.Type().Field(j).Name
			t.Run(name, func(t *testing.T) {
				for _, value := range []uint64{0, stage.Field(j).Uint() + 1, math.MaxUint64} {
					limits := defaults
					field := reflect.ValueOf(&limits).Elem().Field(i).Field(j)
					if field.Kind() == reflect.Uint32 && value > math.MaxUint32 {
						value = math.MaxUint32
					}
					field.SetUint(value)
					if w, r, e := fontBuildEnvelope(limits); !errors.Is(e, ErrLimit) || w != 0 || r != 0 {
						t.Fatalf("invalid limit admitted %d: %d %d %v", value, w, r, e)
					}
				}
			})
		}
	}
	expected, actual, _ := fontAdapterFixture(t)
	for name, mutate := range map[string]func(*fontBuildLimits){
		"outline":  func(l *fontBuildLimits) { l.Outline.MaxWorkingBytes = 1 },
		"geometry": func(l *fontBuildLimits) { l.Geometry.MaxWorkingBytes = 1 },
		"set":      func(l *fontBuildLimits) { l.OutlineSet.MaxWorkingBytes = 1 },
		"layout":   func(l *fontBuildLimits) { l.Layout.MaxWorkingBytes = 1 },
		"provider": func(l *fontBuildLimits) { l.Provider.MaxWorkingBytes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			limits := defaults
			mutate(&limits)
			out, e := buildFontBinding(context.Background(), fontAdapterDocumentHash(), expected, actual, limits)
			if e == nil || out != (textlayer.FontBinding{}) {
				t.Fatal("native limit failure not zero")
			}
		})
	}
	smaller := defaults
	smaller.Outline.MaxWorkingBytes /= 2
	w, r, e := fontBuildEnvelope(smaller)
	if e != nil || w != working-(8<<20) || r != retained {
		t.Fatal("reported policy lowering", e)
	}
	if out, e := buildFontBinding(context.Background(), fontAdapterDocumentHash(), expected, actual, smaller); e != nil || out.Provider == nil {
		t.Fatal("small admitted native limits", e)
	}
}

type fontAdapterContext struct {
	context.Context
	calls, stop, panicAt int
}

func (c *fontAdapterContext) Err() error {
	c.calls++
	if c.calls == c.panicAt {
		panic("font adapter context")
	}
	if c.stop > 0 && c.calls >= c.stop {
		return context.Canceled
	}
	return nil
}
func TestFontAdapterCancellationPanicFailureZero(t *testing.T) {
	expected, actual, _ := fontAdapterFixture(t)
	l := defaultFontBuildLimits()
	doc := fontAdapterDocumentHash()
	if out, e := buildFontBinding(nil, doc, expected, actual, l); !errors.Is(e, ErrContext) || out != (textlayer.FontBinding{}) {
		t.Fatal(e)
	}
	full := &fontAdapterContext{Context: context.Background()}
	if out, e := buildFontBinding(full, doc, expected, actual, l); e != nil || out.Provider == nil {
		t.Fatal(e)
	}
	for _, n := range []int{1, 2, full.calls / 4, full.calls / 2, 3 * full.calls / 4, full.calls} {
		c := &fontAdapterContext{Context: context.Background(), stop: n}
		out, e := buildFontBinding(c, doc, expected, actual, l)
		if !errors.Is(e, context.Canceled) || out != (textlayer.FontBinding{}) {
			t.Fatalf("cancel %d/%d %v", n, full.calls, e)
		}
		c = &fontAdapterContext{Context: context.Background(), panicAt: n}
		out, e = buildFontBinding(c, doc, expected, actual, l)
		if e == nil || out != (textlayer.FontBinding{}) {
			t.Fatalf("panic %d/%d %v", n, full.calls, e)
		}
	}
}

// A genuine checksum-correct core inspection is still not geometry authority.
// Mutate only an upstream declared/unverified bbox and recompute sfnt checksums;
// the real geometry stage must reject it rather than laundering the core receipt.
func TestFontAdapterRejectsCoreOnlyUnverifiedGeometry(t *testing.T) {
	_, _, encoded := fontAdapterFixture(t)
	n := int(binary.BigEndian.Uint16(encoded[4:6]))
	headRecord := -1
	for i := 0; i < n; i++ {
		at := 12 + i*16
		if string(encoded[at:at+4]) == "head" {
			headRecord = at
			break
		}
	}
	if headRecord < 0 {
		t.Fatal("missing head")
	}
	head := int(binary.BigEndian.Uint32(encoded[headRecord+8 : headRecord+12]))
	binary.BigEndian.PutUint16(encoded[head+40:head+42], 1)
	binary.BigEndian.PutUint32(encoded[head+8:head+12], 0)
	checksum := func(b []byte) uint32 {
		var sum uint32
		for at := 0; at < len(b); at += 4 {
			var word [4]byte
			copy(word[:], b[at:min(at+4, len(b))])
			sum += binary.BigEndian.Uint32(word[:])
		}
		return sum
	}
	length := int(binary.BigEndian.Uint32(encoded[headRecord+12 : headRecord+16]))
	binary.BigEndian.PutUint32(encoded[headRecord+4:headRecord+8], checksum(encoded[head:head+length]))
	binary.BigEndian.PutUint32(encoded[head+8:head+12], uint32(0xb1b0afba)-checksum(encoded))
	expected := resourceidentityv2.Binding{AssetID: "font", Kind: documentv2.AssetKindFont, ContentHash: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), SizeBytes: int64(len(encoded))}
	actual, e := resourceidentityv2.InspectFont(context.Background(), expected, encoded, sfntglyf.Limits{})
	if e != nil {
		t.Fatal("core inspection must be genuinely accepted", e)
	}
	out, e := buildFontBinding(context.Background(), fontAdapterDocumentHash(), expected, actual, defaultFontBuildLimits())
	if e == nil || out != (textlayer.FontBinding{}) {
		t.Fatal("unverified core bbox became shape authority", e)
	}
}

func TestFontAdapterConcurrentIndependentChains(t *testing.T) {
	expected, actual, _ := fontAdapterFixture(t)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := buildFontBinding(context.Background(), fontAdapterDocumentHash(), expected, actual, defaultFontBuildLimits())
			if e != nil || out.Provider == nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
}
