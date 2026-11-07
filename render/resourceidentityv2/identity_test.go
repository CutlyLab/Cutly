package resourceidentityv2

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

const tinyPNGHex = "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489000000017352474200aece1ce90000000d49444154789c63f8cfc0d00000048101802c55ceb00000000049454e44ae426082"

func pngBytes(t testing.TB) []byte {
	t.Helper()
	b, e := hex.DecodeString(tinyPNGHex)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func fontBytes(t testing.TB) []byte {
	t.Helper()
	raw, e := os.ReadFile("../../font/sfntglyf/testdata/minimal-font-v1.hex")
	if e != nil {
		t.Fatal(e)
	}
	b, e := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func wavBytes() []byte {
	b := make([]byte, 52)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 44)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 4)
	binary.LittleEndian.PutUint32(b[28:], 8)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 8)
	for i, v := range []int16{-32768, 0, 32767, 16384} {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}
func binding(kind documentv2.AssetKind, b []byte) Binding {
	return Binding{AssetID: "asset", Kind: kind, ContentHash: digest(b), SizeBytes: int64(len(b))}
}
func pngInspection(t testing.TB) (*Inspected, Receipt) {
	t.Helper()
	b := pngBytes(t)
	i, e := InspectPNG(context.Background(), binding(documentv2.AssetKindImage, b), b, pngdecode.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := i.Receipt()
	if e != nil {
		t.Fatal(e)
	}
	return i, r
}
func fontInspection(t testing.TB) (*Inspected, Receipt) {
	t.Helper()
	b := fontBytes(t)
	i, e := InspectFont(context.Background(), binding(documentv2.AssetKindFont, b), b, sfntglyf.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := i.Receipt()
	if e != nil {
		t.Fatal(e)
	}
	return i, r
}
func resign(t testing.TB, r Receipt) Receipt {
	t.Helper()
	r.Fingerprint = ""
	fp, e := Fingerprint(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	r.Fingerprint = fp
	return r
}
func staticClaim(r Receipt) renderapi.MaterializedResourceV2 {
	p := renderapi.ImageMaterializationProfileV2
	if r.Resource.Kind == documentv2.AssetKindFont {
		p = renderapi.FontMaterializationProfileV2
	}
	fp := r.Fingerprint
	return renderapi.MaterializedResourceV2{AssetID: r.Resource.AssetID, Kind: r.Resource.Kind, ContentHash: r.Resource.ContentHash, SizeBytes: r.Resource.SizeBytes, MaterializationProfile: p, InspectionFingerprint: &fp}
}
func TestFixedConcreteIdentitiesAndCanonicalRoundTrip(t *testing.T) {
	_, png := pngInspection(t)
	_, font := fontInspection(t)
	for _, r := range []Receipt{png, font} {
		v, e := Verify(context.Background(), r)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := v.CanonicalJSON()
		if e != nil {
			t.Fatal(e)
		}
		decoded, e := Decode(context.Background(), append([]byte(" \n"), raw...))
		if e != nil {
			t.Fatal(e)
		}
		got, e := decoded.Receipt()
		if e != nil {
			t.Fatal(e)
		}
		same, e := Equal(context.Background(), r, got)
		if e != nil || !same {
			t.Fatal("round trip", e)
		}
		var body map[string]json.RawMessage
		if e = json.Unmarshal(raw, &body); e != nil {
			t.Fatal(e)
		}
		delete(body, "fingerprint")
		preimage, e := canonicaljson.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		if digest(preimage) != r.Fingerprint {
			t.Fatal("fingerprint must omit member")
		}
		want := map[documentv2.AssetKind][3]string{
			documentv2.AssetKindImage: {"sha256:c83c0e69fcb743a959f59f3668a1e64bb441a9e694535444dfb340e96d9ef464", "sha256:4e718e38ad76247a3a443196f27b3505724831995783ac9970a5395f6814f32c", "sha256:4badaaf459dd3c98ec43eab3da93494c6db9f5dca24a58e0c8b576ec874cf1f8"},
			documentv2.AssetKindFont:  {"sha256:8ac0599a24e305fa552d72868651d8920fb55ce82d7856c769818d50774e1368", "sha256:32db45644d8c5bf0c61537ecafbc0f9192d9653ecf3130d304f8b566fe6707c8", "sha256:c74c4e561355e1470975be067d26459aee7ab8d70505cf225341355e02773aa3"},
		}[r.Resource.Kind]
		if r.Fingerprint != want[0] || r.Inspector.DescriptorFingerprint != want[1] || digest(raw) != want[2] {
			t.Fatalf("fixed identity changed: %s %s %s", r.Fingerprint, r.Inspector.DescriptorFingerprint, digest(raw))
		}
	}
	if png.PNG.Width != 1 || png.PNG.Height != 1 || png.PNG.SRGBRenderingIntent != 0 {
		t.Fatal(png)
	}
	if font.Font.WorkUnits != 2988 || font.Font.WorkingBytes != 65568 || font.Font.RetainedBytes != 18492 || font.Font.OpaqueLayoutTables == nil {
		t.Fatal(font)
	}
}

func TestStrictRawShape(t *testing.T) {
	_, p := pngInspection(t)
	_, f := fontInspection(t)
	pv, _ := Verify(context.Background(), p)
	pr, _ := pv.CanonicalJSON()
	fv, _ := Verify(context.Background(), f)
	fr, _ := fv.CanonicalJSON()
	for name, raw := range map[string][]byte{
		"unknown":           bytes.Replace(pr, []byte(`"schema_version":2`), []byte(`"extra":0,"schema_version":2`), 1),
		"case":              bytes.Replace(pr, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1),
		"duplicate":         bytes.Replace(pr, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1),
		"escaped duplicate": bytes.Replace(pr, []byte(`"schema_version":2`), []byte(`"schema_version":2,"`+string(rune(92))+`u0073chema_version":2`), 1),
		"missing zero":      bytes.Replace(pr, []byte(`"srgb_rendering_intent":0,`), nil, 1),
		"null zero":         bytes.Replace(pr, []byte(`"srgb_rendering_intent":0`), []byte(`"srgb_rendering_intent":null`), 1),
		"empty payload":     bytes.Replace(pr, []byte(`"schema_version":2`), []byte(`"font":{},"schema_version":2`), 1),
		"null payload":      bytes.Replace(pr, []byte(`"schema_version":2`), []byte(`"font":null,"schema_version":2`), 1),
		"fraction":          bytes.Replace(pr, []byte(`"width":1`), []byte(`"width":1.0`), 1),
		"exponent":          bytes.Replace(pr, []byte(`"width":1`), []byte(`"width":1e0`), 1),
		"trailing":          append(append([]byte{}, pr...), []byte(` {}`)...),
		"null layout":       bytes.Replace(fr, []byte(`"opaque_layout_tables":[]`), []byte(`"opaque_layout_tables":null`), 1),
		"missing layout":    bytes.Replace(fr, []byte(`"opaque_layout_tables":[],`), nil, 1),
		"unknown nested":    bytes.Replace(fr, []byte(`"tag":"cmap"`), []byte(`"tag":"cmap","live_font":{}`), 1),
		"root null":         []byte(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			v, e := Decode(context.Background(), raw)
			if e == nil || v.receipt != nil {
				t.Fatalf("accepted invalid shape: %s", raw)
			}
		})
	}
}

func TestTypedAdmissionAndNoEmptyAliases(t *testing.T) {
	_, p := pngInspection(t)
	_, f := fontInspection(t)
	for name, mutate := range map[string]func(*Receipt){
		"empty profile":       func(r *Receipt) { r.Profile = "" },
		"empty optional font": func(r *Receipt) { r.Font = &sfntglyf.Receipt{} },
		"missing png":         func(r *Receipt) { r.PNG = nil },
		"inspector":           func(r *Receipt) { r.Inspector.DescriptorFingerprint = digest(nil) },
		"bad utf8":            func(r *Receipt) { r.Resource.AssetID = documentv2.ID("\xff") },
		"oversize string":     func(r *Receipt) { r.Resource.AssetID = documentv2.ID(strings.Repeat("x", MaxAssetIDBytes+1)) },
	} {
		t.Run(name, func(t *testing.T) {
			r := cloneReceipt(p)
			mutate(&r)
			if _, e := Verify(context.Background(), r); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*sfntglyf.Receipt){
		"nil set":             func(f *sfntglyf.Receipt) { f.OpaqueLayoutTables = nil },
		"nil tables":          func(f *sfntglyf.Receipt) { f.Tables = nil },
		"unknown table":       func(f *sfntglyf.Receipt) { f.Tables[0].Tag = "CFF " },
		"counter":             func(f *sfntglyf.Receipt) { f.WorkUnits++ },
		"working counter":     func(f *sfntglyf.Receipt) { f.WorkingBytes++ },
		"retained counter":    func(f *sfntglyf.Receipt) { f.RetainedBytes++ },
		"count":               func(f *sfntglyf.Receipt) { f.TableCount++ },
		"duplicate table":     func(f *sfntglyf.Receipt) { f.Tables[1] = f.Tables[0] },
		"bounds":              func(f *sfntglyf.Receipt) { f.Tables[0].Offset = 0xfffffff0 },
		"opaque authority":    func(f *sfntglyf.Receipt) { f.GlyfTableStatus = "outline_validated" },
		"oversize collection": func(f *sfntglyf.Receipt) { f.Tables = make([]sfntglyf.TableReceipt, sfntglyf.HardMaxTables+1) },
	} {
		t.Run(name, func(t *testing.T) {
			r := cloneReceipt(f)
			mutate(r.Font)
			r.Fingerprint = ""
			if _, e := Fingerprint(context.Background(), r); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, e := Decode(context.Background(), make([]byte, MaxReceiptBytes+1)); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
}

func TestBindingsActualObservationAndResignedClaims(t *testing.T) {
	i, r := pngInspection(t)
	ctx := context.Background()
	claim := staticClaim(r)
	if e := Check(ctx, r.Resource, claim, i); e != nil {
		t.Fatal(e)
	}
	if e := Matches(ctx, r.Resource, r, i); e != nil {
		t.Fatal(e)
	}
	// A valid, newly signed claim is not proof it was observed.
	for name, mutate := range map[string]func(*Receipt){
		"pixel":    func(r *Receipt) { r.PNG.PixelHash = digest(nil) },
		"geometry": func(r *Receipt) { r.PNG.Width = 2 },
		"intent":   func(r *Receipt) { r.PNG.SRGBRenderingIntent = 1 },
		"asset":    func(r *Receipt) { r.Resource.AssetID = "replacement" },
		"size":     func(r *Receipt) { r.Resource.SizeBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneReceipt(r)
			mutate(&bad)
			if _, e := Verify(ctx, bad); e == nil {
				t.Fatal("unsigned tamper accepted")
			}
			bad = resign(t, bad)
			if _, e := Verify(ctx, bad); e != nil {
				t.Fatal(e)
			}
			if e := Matches(ctx, r.Resource, bad, i); !errors.Is(e, ErrMismatch) {
				t.Fatal("resigned claim trusted", e)
			}
			if e := Check(ctx, r.Resource, staticClaim(bad), i); !errors.Is(e, ErrMismatch) {
				t.Fatal("claim trusted", e)
			}
		})
	}
	for _, change := range []func(*Binding){func(b *Binding) { b.AssetID = "other" }, func(b *Binding) { b.Kind = documentv2.AssetKindFont }, func(b *Binding) { b.ContentHash = digest(nil) }, func(b *Binding) { b.SizeBytes++ }} {
		b := r.Resource
		change(&b)
		if e := Check(ctx, b, claim, i); e == nil {
			t.Fatal("expected substitution accepted")
		}
	}
	empty := ""
	claim.ProbeFingerprint = &empty
	if e := Check(ctx, r.Resource, claim, i); e == nil {
		t.Fatal("empty optional alias")
	}
	if e := Check(ctx, r.Resource, staticClaim(r), &Inspected{}); e == nil {
		t.Fatal("zero actual")
	}
	b := r.Resource
	b.Kind = documentv2.AssetKindVideo
	if e := Check(ctx, b, claim, i); !errors.Is(e, ErrUnsupported) {
		t.Fatal("general video", e)
	}
	fi, fr := fontInspection(t)
	for _, tag := range []string{"table hash", "metric"} {
		bad := cloneReceipt(fr)
		if tag == "table hash" {
			bad.Font.Tables[0].ContentHash = digest(nil)
		} else {
			bad.Font.HheaAscender++
		}
		bad = resign(t, bad)
		if e := Matches(ctx, fr.Resource, bad, fi); !errors.Is(e, ErrMismatch) {
			t.Fatal(e)
		}
	}
}

func TestHostLimitsDoNotChangeIdentity(t *testing.T) {
	b := pngBytes(t)
	expected := binding(documentv2.AssetKindImage, b)
	one, r := pngInspection(t)
	l := pngdecode.DefaultLimits()
	l.MaxWidth = 2
	l.MaxHeight = 2
	l.MaxWorkingBytes = 100
	l.MaxEncodedBytes = uint64(len(b))
	two, e := InspectPNG(context.Background(), expected, b, l)
	if e != nil {
		t.Fatal(e)
	}
	r2, _ := two.Receipt()
	if same, e := Equal(context.Background(), r, r2); e != nil || !same {
		t.Fatal("PNG host limits change identity", e)
	}
	fb := fontBytes(t)
	fl := sfntglyf.DefaultLimits()
	fl.MaxEncodedBytes = uint64(len(fb))
	fl.MaxWorkingBytes = 65568
	fl.MaxRetainedBytes = 18492
	fl.MaxWorkUnits = 2988
	fl.MaxTables = 7
	fl.MaxGlyphs = 2
	fl.MaxCmapRecords = 1
	fl.MaxCmapMappings = 2
	_, fr := fontInspection(t)
	ft, e := InspectFont(context.Background(), fr.Resource, fb, fl)
	if e != nil {
		t.Fatal(e)
	}
	fr2, _ := ft.Receipt()
	if same, e := Equal(context.Background(), fr, fr2); e != nil || !same {
		t.Fatal("font host limits change identity", e)
	}
	if one == two {
		t.Fatal("expected independently owned observations")
	}
}

func TestAudioUsesNativeProbeIdentityAndDefensiveOwnership(t *testing.T) {
	b := wavBytes()
	expected := binding(documentv2.AssetKindAudio, b)
	ctx := context.Background()
	actual, e := InspectAudio(ctx, expected, b, wavdecode.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	p, e := actual.Probe()
	if e != nil {
		t.Fatal(e)
	}
	native, e := wavsource.Inspect(ctx, expected.AssetID, expected.ContentHash, expected.SizeBytes, b, wavdecode.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	fp, e := probe.Fingerprint(native)
	if e != nil {
		t.Fatal(e)
	}
	got, e := probe.Fingerprint(p)
	if e != nil || fp != got || !reflect.DeepEqual(native, p) {
		t.Fatal("native probe parity", e)
	}
	if fp != "sha256:884b9a3a11692a118eff1a2287692562f2002191a0f7265cda671a75952c6b03" || expected.ContentHash != "sha256:3d6daf78b01ac83eb59aee746425f120d857b79fc07171e7840a97d7e4a4ee98" {
		t.Fatalf("fixed WAV identity changed: %s %s", fp, expected.ContentHash)
	}
	claim := renderapi.MaterializedResourceV2{AssetID: expected.AssetID, Kind: expected.Kind, ContentHash: expected.ContentHash, SizeBytes: expected.SizeBytes, MaterializationProfile: renderapi.AVMaterializationProfileV2, ProbeFingerprint: &fp}
	if e = Check(ctx, expected, claim, actual); e != nil {
		t.Fatal(e)
	}
	p.Streams[0].Audio.SampleRate = 999
	*p.Streams[0].DurationPTS = 999
	p.Streams[0].Codec = "bad"
	clear(b)
	again, _ := actual.Probe()
	if got, e = probe.Fingerprint(again); e != nil || got != fp {
		t.Fatal("probe ownership", e)
	}
	bad := digest(nil)
	claim.ProbeFingerprint = &bad
	if e = Check(ctx, expected, claim, actual); !errors.Is(e, ErrMismatch) {
		t.Fatal(e)
	}
	if _, e = actual.Receipt(); e == nil {
		t.Fatal("invented audio receipt")
	}
}

func TestImmutableResourcesAndOwnedMetadata(t *testing.T) {
	ctx := context.Background()
	b := pngBytes(t)
	i, e := InspectPNG(ctx, binding(documentv2.AssetKindImage, b), b, pngdecode.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	r, _ := i.Receipt()
	v, e := Verify(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := v.CanonicalJSON()
	r.PNG.PixelHash = "bad"
	r.Resource.AssetID = "bad"
	clear(raw)
	clear(b)
	original, _ := i.Receipt()
	owned, _ := v.Receipt()
	if original.PNG.PixelHash != owned.PNG.PixelHash {
		t.Fatal("receipt alias")
	}
	surface, _ := i.Surface()
	if hash, e := surface.CanonicalHash(); e != nil || hash != original.PNG.PixelHash {
		t.Fatal("surface alias", e)
	}
	fb := fontBytes(t)
	fi, e := InspectFont(ctx, binding(documentv2.AssetKindFont, fb), fb, sfntglyf.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	fr, _ := fi.Receipt()
	clear(fb)
	font, _ := fi.Font()
	if gid, ok := font.NominalGlyph('A'); !ok || gid != 1 {
		t.Fatal("font encoded alias")
	}
	fr.Font.Tables[0].ContentHash = "bad"
	fontReceipt := font.Receipt()
	fontReceipt.Tables[0].ContentHash = "bad"
	fresh, _ := fi.Receipt()
	if fresh.Font.Tables[0].ContentHash == "bad" {
		t.Fatal("font metadata alias")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 8; n++ {
				pr, e := i.Receipt()
				if e != nil {
					t.Error(e)
				}
				if e = Matches(ctx, pr.Resource, pr, i); e != nil {
					t.Error(e)
				}
				_, _ = surface.Pixel(0, 0)
				_, _ = font.NominalGlyph('A')
				_, _ = fi.Receipt()
			}
		}()
	}
	wg.Wait()
	if _, e := (Verified{}).Receipt(); e == nil {
		t.Fatal("zero verified")
	}
	if _, e := (*Inspected)(nil).Probe(); e == nil {
		t.Fatal("nil inspected")
	}
}

type stepContext struct {
	context.Context
	calls   int
	stop    int
	panicAt int
}

func (s *stepContext) Err() error {
	s.calls++
	if s.calls == s.panicAt {
		panic("context panic")
	}
	if s.stop > 0 && s.calls >= s.stop {
		return context.Canceled
	}
	return nil
}
func (s *stepContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func TestFailureZeroCancellationPanicAndAdmission(t *testing.T) {
	ctx := context.Background()
	png := pngBytes(t)
	font := fontBytes(t)
	wav := wavBytes()
	inspectors := []struct {
		name string
		data []byte
		kind documentv2.AssetKind
		run  func(context.Context, Binding, []byte) (*Inspected, error)
	}{
		{"png", png, documentv2.AssetKindImage, func(c context.Context, b Binding, p []byte) (*Inspected, error) {
			return InspectPNG(c, b, p, pngdecode.Limits{})
		}},
		{"font", font, documentv2.AssetKindFont, func(c context.Context, b Binding, p []byte) (*Inspected, error) {
			return InspectFont(c, b, p, sfntglyf.Limits{})
		}},
		{"audio", wav, documentv2.AssetKindAudio, func(c context.Context, b Binding, p []byte) (*Inspected, error) {
			return InspectAudio(c, b, p, wavdecode.Limits{})
		}},
	}
	for _, in := range inspectors {
		t.Run(in.name, func(t *testing.T) {
			expected := binding(in.kind, in.data)
			for _, badctx := range []context.Context{nil, &stepContext{Context: ctx, stop: 1}, &stepContext{Context: ctx, panicAt: 1}} {
				out, e := in.run(badctx, expected, in.data)
				if e == nil || out != nil {
					t.Fatal("failure nonzero", e)
				}
			}
			successful := &stepContext{Context: ctx}
			out, e := in.run(successful, expected, in.data)
			if e != nil || out == nil {
				t.Fatal(e)
			}
			for _, stop := range []int{2, successful.calls / 2, successful.calls} {
				c := &stepContext{Context: ctx, stop: stop}
				out, e := in.run(c, expected, in.data)
				if !errors.Is(e, context.Canceled) || out != nil {
					t.Fatalf("cancel at %d/%d: %v", stop, successful.calls, e)
				}
				out, e = in.run(&stepContext{Context: ctx, panicAt: stop}, expected, in.data)
				if e == nil || out != nil {
					t.Fatalf("panic at %d/%d exposed a result: %v", stop, successful.calls, e)
				}
			}
			for _, mutate := range []func(*Binding){func(b *Binding) { b.ContentHash = digest(nil) }, func(b *Binding) { b.SizeBytes++ }, func(b *Binding) { b.AssetID = " " }} {
				b := expected
				mutate(&b)
				out, e := in.run(ctx, b, in.data)
				if e == nil || out != nil {
					t.Fatal("bad binding accepted", e)
				}
			}
			malformed := []byte("not a resource")
			out, e = in.run(ctx, binding(in.kind, malformed), malformed)
			if e == nil || out != nil {
				t.Fatal("malformed accepted")
			}
		})
	}
	_, r := pngInspection(t)
	for _, badContext := range []func() context.Context{func() context.Context { return nil }, func() context.Context { return &stepContext{Context: ctx, stop: 1} }, func() context.Context { return &stepContext{Context: ctx, panicAt: 1} }} {
		if v, e := Verify(badContext(), r); e == nil || v.receipt != nil {
			t.Fatal("verify failure zero", e)
		}
		if fp, e := Fingerprint(badContext(), r); e == nil || fp != "" {
			t.Fatal("fingerprint failure zero", e)
		}
	}
	for _, run := range []func() (*Inspected, error){
		func() (*Inspected, error) {
			return InspectPNG(ctx, binding(documentv2.AssetKindImage, png), png, pngdecode.Limits{MaxEncodedBytes: 1})
		},
		func() (*Inspected, error) {
			return InspectFont(ctx, binding(documentv2.AssetKindFont, font), font, sfntglyf.Limits{MaxEncodedBytes: 1})
		},
		func() (*Inspected, error) {
			return InspectAudio(ctx, binding(documentv2.AssetKindAudio, wav), wav, wavdecode.Limits{MaxEncodedBytes: 1})
		},
		func() (*Inspected, error) {
			l := pngdecode.DefaultLimits()
			l.MaxWorkingBytes = 29
			return InspectPNG(ctx, binding(documentv2.AssetKindImage, png), png, l)
		},
		func() (*Inspected, error) {
			return InspectFont(ctx, binding(documentv2.AssetKindFont, font), font, sfntglyf.Limits{MaxWorkingBytes: 1})
		},
	} {
		if out, e := run(); e == nil || out != nil {
			t.Fatal("allocation policy accepted", e)
		}
	}
	// A large malformed input with wrong authorization hash must never reach
	// decoder/parser or trigger a proportional copy. Caller allocation is excluded.
	huge := make([]byte, 8<<20)
	expected := binding(documentv2.AssetKindFont, huge)
	expected.ContentHash = digest(nil)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out, e := InspectFont(ctx, expected, huge, sfntglyf.Limits{})
	runtime.ReadMemStats(&after)
	if !errors.Is(e, ErrMismatch) || out != nil {
		t.Fatal("hash before parser", e)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 512<<10 {
		t.Fatalf("prehash allocated proportional storage: %d", delta)
	}
}
