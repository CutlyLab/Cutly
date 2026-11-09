package backendstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func assemblyFixtureV2(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func assemblyTestRequestV2(t *testing.T) (ChunkAssemblyRequestV2, api.RenderRequestV2) {
	t.Helper()
	r, err := DecodeChunkAssemblyRequestV2(assemblyFixtureV2(t, "document-v2-chunk-assembly-mixed.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := api.DecodeRenderRequestV2(assemblyFixtureV2(t, "document-v2-render-request-mixed.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r, source
}
func assemblyCloneTestV2(t *testing.T, r ChunkAssemblyRequestV2) ChunkAssemblyRequestV2 {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var result ChunkAssemblyRequestV2
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func assemblyResignTestV2(t *testing.T, r *ChunkAssemblyRequestV2) {
	t.Helper()
	hash, err := assemblyFingerprintV2(*r)
	if err != nil {
		t.Fatal(err)
	}
	r.Fingerprint = hash
}
func assemblyRawTestV2(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = assemblyRawCanonicalV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func assemblyObjectTestV2(t *testing.T, raw []byte) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestChunkAssemblyV2OwnedRoundTripAndFreeze(t *testing.T) {
	r, source := assemblyTestRequestV2(t)
	frozen, err := FreezeChunkAssemblyRequestV2(context.Background(), source.Plan, source.Manifest, r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frozen, r) {
		t.Fatal("freeze changed valid request")
	}
	r.Audio[0].ContentHash = source.Plan.DocumentHash
	r.Aggregate.Audio.RootHash = source.Plan.DocumentHash
	raw, err := CanonicalChunkAssemblyRequestV2(frozen)
	if err != nil {
		t.Fatal("freeze retained caller alias", err)
	}
	verified, err := VerifyChunkAssemblyRequestV2(frozen)
	if err != nil || !verified.Valid() {
		t.Fatal(err)
	}
	if verified.Fingerprint() != frozen.Fingerprint {
		t.Fatal("fingerprint accessor")
	}
	copy, err := verified.Request()
	if err != nil {
		t.Fatal(err)
	}
	copy.Video[0].TaskID = "changed"
	copy.Aggregate.Video.UnitBytes++
	owned, err := verified.Request()
	if err != nil || !reflect.DeepEqual(owned, frozen) {
		t.Fatal("request accessor retained mutable alias", err)
	}
	data, err := verified.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'x'
	again, err := verified.Canonical()
	if err != nil || !bytes.Equal(again, raw) {
		t.Fatal("canonical accessor retained mutable alias", err)
	}
	var zero VerifiedChunkAssemblyRequestV2
	if zero.Valid() || zero.Fingerprint() != "" {
		t.Fatal("zero handle identity")
	}
	if _, err := zero.Canonical(); err == nil {
		t.Fatal("zero canonical")
	}
	if _, err := zero.Request(); err == nil {
		t.Fatal("zero request")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if got, e := verified.Request(); e != nil || got.Fingerprint != frozen.Fingerprint {
					t.Error("concurrent owned read", e)
				}
			}
		}()
	}
	wg.Wait()
}

func TestChunkAssemblyV2IdentityBeforeGraphWork(t *testing.T) {
	r, s := assemblyTestRequestV2(t)
	// This graph is too large even to admit. Wrong fixed-size identity must win
	// before walking descriptors or the independent manifest/plan graph.
	r.Audio = make([]ChunkDescriptorV2, MaxChunkAssemblyDescriptorsV2+1)
	r.ProviderExecutableDigest = s.Plan.DocumentHash
	got, err := VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), r, s.Manifest, s.Plan)
	if !errors.Is(err, ErrChunkAssemblyInvalidV2) || errors.Is(err, ErrChunkAssemblyLimitV2) || got.Valid() {
		t.Fatalf("identity did not precede proportional work: %v", err)
	}
	frozen, err := FreezeChunkAssemblyRequestV2(context.Background(), s.Plan, s.Manifest, r)
	if !errors.Is(err, ErrChunkAssemblyInvalidV2) || frozen.Fingerprint != "" {
		t.Fatalf("freeze identity ordering: %v", err)
	}
	r.ProviderExecutableDigest = s.Manifest.ProviderBuild
	if _, err = VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), r, s.Manifest, s.Plan); !errors.Is(err, ErrChunkAssemblyLimitV2) {
		t.Fatalf("matching identity did not reach limit: %v", err)
	}
}

func TestChunkAssemblyV2ResignedCrossBindings(t *testing.T) {
	base, source := assemblyTestRequestV2(t)
	for name, mutate := range map[string]func(*ChunkAssemblyRequestV2){
		"document":         func(r *ChunkAssemblyRequestV2) { r.DocumentHash = source.Manifest.CompilerBuild },
		"plan":             func(r *ChunkAssemblyRequestV2) { r.PlanFingerprint = source.Manifest.CompilerBuild },
		"manifest":         func(r *ChunkAssemblyRequestV2) { r.ExecutionManifestFingerprint = source.Manifest.CompilerBuild },
		"provider":         func(r *ChunkAssemblyRequestV2) { r.ProviderExecutableDigest = source.Manifest.CompilerBuild },
		"target-timeline":  func(r *ChunkAssemblyRequestV2) { r.Target.TimelineID = "other" },
		"runtime-ffmpeg":   func(r *ChunkAssemblyRequestV2) { r.MediaRuntime.FFmpegBuild = source.Manifest.CompilerBuild },
		"runtime-decoder":  func(r *ChunkAssemblyRequestV2) { r.MediaRuntime.DecoderProfile = "other.v2" },
		"runtime-encoder":  func(r *ChunkAssemblyRequestV2) { r.MediaRuntime.EncoderProfile = "other.v2" },
		"runtime-color":    func(r *ChunkAssemblyRequestV2) { r.MediaRuntime.ColorProfile = "other.v2" },
		"runtime-text":     func(r *ChunkAssemblyRequestV2) { r.MediaRuntime.TextShaperBuild = source.Manifest.CompilerBuild },
		"format":           func(r *ChunkAssemblyRequestV2) { r.Output.Format = "mov" },
		"video-codec":      func(r *ChunkAssemblyRequestV2) { r.Output.VideoCodec = "other" },
		"audio-codec":      func(r *ChunkAssemblyRequestV2) { r.Output.AudioCodec = "other" },
		"seed":             func(r *ChunkAssemblyRequestV2) { r.DeterministicSeed = 1 },
		"outside-timeline": func(r *ChunkAssemblyRequestV2) { r.TimelineRange.Start.Value = 99999 },
	} {
		t.Run(name, func(t *testing.T) {
			r := assemblyCloneTestV2(t, base)
			mutate(&r)
			assemblyResignTestV2(t, &r)
			got, err := VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), r, source.Manifest, source.Plan)
			if err == nil || got.Valid() {
				t.Fatal("accepted resigned foreign binding")
			}
			if got, err := FreezeChunkAssemblyRequestV2(context.Background(), source.Plan, source.Manifest, r); err == nil || got.Fingerprint != "" {
				t.Fatal("freeze repaired foreign binding")
			}
		})
	}
	// Cheap matching identity does not exempt either independent graph from
	// native V2 verification; their unchanged fingerprints cannot conceal drift.
	badPlan := source.Plan
	badPlan.Resources = append(badPlan.Resources, badPlan.Resources[0])
	if v, err := VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), base, source.Manifest, badPlan); err == nil || v.Valid() {
		t.Fatal("unverified independent plan accepted")
	}
	badManifest := source.Manifest
	badManifest.CompilerBuild = source.Plan.DocumentHash
	if v, err := VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), base, badManifest, source.Plan); err == nil || v.Valid() {
		t.Fatal("unverified independent manifest accepted")
	}
}

func TestChunkAssemblyV2ResignedSemanticAttacks(t *testing.T) {
	base, _ := assemblyTestRequestV2(t)
	for name, mutate := range map[string]func(*ChunkAssemblyRequestV2){
		"reorder":          func(r *ChunkAssemblyRequestV2) { r.Audio[0], r.Audio[1] = r.Audio[1], r.Audio[0] },
		"duplicate":        func(r *ChunkAssemblyRequestV2) { r.Audio[1] = r.Audio[0] },
		"key-repeat":       func(r *ChunkAssemblyRequestV2) { r.Audio[1].CacheKey = r.Audio[0].CacheKey },
		"key-cross-domain": func(r *ChunkAssemblyRequestV2) { r.Video[0].CacheKey = r.Audio[0].CacheKey },
		"task-id":          func(r *ChunkAssemblyRequestV2) { r.Audio[0].TaskID = "video-00000" },
		"key-profile":      func(r *ChunkAssemblyRequestV2) { r.Audio[0].CacheKey.Profile = "foreign.v2" },
		"key-namespace":    func(r *ChunkAssemblyRequestV2) { r.Audio[0].CacheKey.Namespace = "../foreign" },
		"domain":           func(r *ChunkAssemblyRequestV2) { r.Video[0].Domain = api.DecodedVideoProfileRGBA8V1 },
		"gap":              func(r *ChunkAssemblyRequestV2) { r.Audio[1].Range.First++ },
		"overlap":          func(r *ChunkAssemblyRequestV2) { r.Audio[1].Range.First-- },
		"partial-coverage": func(r *ChunkAssemblyRequestV2) { r.Audio = r.Audio[:len(r.Audio)-1] },
		"noncanonical-partition": func(r *ChunkAssemblyRequestV2) {
			r.Audio[0].Range.Count--
			r.Audio[0].SizeBytes -= r.Audio[0].UnitBytes
			r.Audio[1].Range.First--
			r.Audio[1].Range.Count++
			r.Audio[1].SizeBytes += r.Audio[1].UnitBytes
		},
		"size":              func(r *ChunkAssemblyRequestV2) { r.Audio[0].SizeBytes++ },
		"unit":              func(r *ChunkAssemblyRequestV2) { r.Audio[0].UnitBytes++ },
		"hash":              func(r *ChunkAssemblyRequestV2) { r.Audio[0].ContentHash = r.DocumentHash },
		"uppercase-hash":    func(r *ChunkAssemblyRequestV2) { r.Audio[0].ContentHash = strings.ToUpper(r.Audio[0].ContentHash) },
		"missing-root":      func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio = nil },
		"root":              func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.RootHash = r.DocumentHash },
		"root-domain":       func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.Domain = api.VideoSurfaceProfileV2 },
		"root-unit":         func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.UnitBytes++ },
		"root-first":        func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.Range.First++ },
		"root-count":        func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.Range.Count++ },
		"root-chunks":       func(r *ChunkAssemblyRequestV2) { r.Aggregate.Audio.ChunkCount++ },
		"aggregate-profile": func(r *ChunkAssemblyRequestV2) { r.Aggregate.Profile = ReferenceChunkAggregateBindingV1 },
		"resume-mode":       func(r *ChunkAssemblyRequestV2) { r.Aggregate.ResumeMode = ReferenceChunkResumeModeV1 },
		"null-array":        func(r *ChunkAssemblyRequestV2) { r.Audio = nil },
		"frame-projection":  func(r *ChunkAssemblyRequestV2) { r.FrameRange.First++ },
		"sample-projection": func(r *ChunkAssemblyRequestV2) { r.SampleRange.First++ },
		"intermediate-video": func(r *ChunkAssemblyRequestV2) {
			r.Output.Intermediate.VideoSurfaceProfile = api.DecodedVideoProfileRGBA8V1
		},
		"intermediate-audio": func(r *ChunkAssemblyRequestV2) { r.Output.Intermediate.AudioBlockProfile = "float32" },
		"final-video":        func(r *ChunkAssemblyRequestV2) { r.Output.DecodedVideoProfile = api.VideoSurfaceProfileV2 },
		"final-audio":        func(r *ChunkAssemblyRequestV2) { r.Output.DecodedAudioProfile = "float32" },
		"width":              func(r *ChunkAssemblyRequestV2) { r.Output.Width++ },
		"height":             func(r *ChunkAssemblyRequestV2) { r.Output.Height++ },
		"channels":           func(r *ChunkAssemblyRequestV2) { r.Output.Channels++ },
		"rate":               func(r *ChunkAssemblyRequestV2) { r.Output.SampleRate++ },
		"frame-clock":        func(r *ChunkAssemblyRequestV2) { r.Output.FrameClock.FramesPerSecond.Value++ },
		"sample-clock":       func(r *ChunkAssemblyRequestV2) { r.Output.SampleClock.Origin.Value++ },
		"pixel":              func(r *ChunkAssemblyRequestV2) { r.Output.PixelFormat = "other" },
		"color":              func(r *ChunkAssemblyRequestV2) { r.Output.ColorSpace = "other" },
		"size-overflow": func(r *ChunkAssemblyRequestV2) {
			r.Audio[0].UnitBytes = math.MaxInt64
			r.Aggregate.Audio.UnitBytes = math.MaxInt64
		},
		"end-overflow": func(r *ChunkAssemblyRequestV2) { r.Audio[0].Range.First = math.MaxInt64 },
		"unit-overflow": func(r *ChunkAssemblyRequestV2) {
			r.Target.Width = math.MaxInt32
			r.Target.Height = math.MaxInt32
			r.Output.Width = math.MaxInt32
			r.Output.Height = math.MaxInt32
		},
		"artifact-remote":    func(r *ChunkAssemblyRequestV2) { r.ArtifactURI = "https://example.invalid/out.mp4" },
		"artifact-traversal": func(r *ChunkAssemblyRequestV2) { r.ArtifactURI = "file:///controlled/../escape.mp4" },
		"empty-work":         func(r *ChunkAssemblyRequestV2) { r.FrameRange.Count = 0; r.SampleRange.Count = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			r := assemblyCloneTestV2(t, base)
			mutate(&r)
			assemblyResignTestV2(t, &r)
			if v, err := VerifyChunkAssemblyRequestV2(r); err == nil || v.Valid() {
				t.Fatal("accepted resigned semantic drift")
			}
			if raw, err := CanonicalChunkAssemblyRequestV2(r); err == nil || raw != nil {
				t.Fatal("canonical returned partial bytes")
			}
			raw := assemblyRawTestV2(t, r)
			if got, err := DecodeChunkAssemblyRequestV2(raw); err == nil || !reflect.DeepEqual(got, ChunkAssemblyRequestV2{}) {
				t.Fatal("decode accepted invalid or partial value")
			}
		})
	}
}

// Visit every populated object (including array records), not just the root or
// a hand-picked sample. Re-run the traversal on a fresh object per mutation.
func assemblyWalkObjectsV2(value any, path string, visit func(string, map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(path, v)
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key != "__unknown" {
				assemblyWalkObjectsV2(v[key], path+"/"+key, visit)
			}
		}
	case []any:
		for i, child := range v {
			assemblyWalkObjectsV2(child, fmtIndexV2(path, i), visit)
		}
	}
}
func fmtIndexV2(path string, index int) string { return fmt.Sprintf("%s/%d", path, index) }

func TestChunkAssemblyV2AllNestedWireFields(t *testing.T) {
	base := assemblyFixtureV2(t, "document-v2-chunk-assembly-mixed.canonical.json")
	object := assemblyObjectTestV2(t, base)
	paths := []string{}
	assemblyWalkObjectsV2(object, "$", func(path string, _ map[string]any) { paths = append(paths, path) })
	for _, path := range paths {
		for _, attack := range []string{"unknown", "duplicate"} {
			t.Run(path+"/"+attack, func(t *testing.T) {
				obj := assemblyObjectTestV2(t, base)
				var raw []byte
				assemblyWalkObjectsV2(obj, "$", func(p string, m map[string]any) {
					if p != path {
						return
					}
					if attack == "unknown" {
						m["__unknown"] = true
					} else {
						raw = assemblyRawTestV2(t, m)
					}
				})
				if attack == "unknown" {
					raw = assemblyRawTestV2(t, obj)
				} else {
					// Substitute a duplicate into the precise nested object serialization;
					// duplicate keys are rejected even when the values are identical.
					duplicate := append([]byte(`{"__duplicate":0,"__duplicate":0,`), raw[1:]...)
					raw = bytes.Replace(base, raw, duplicate, 1)
				}
				got, err := DecodeChunkAssemblyRequestV2(raw)
				if err == nil || !reflect.DeepEqual(got, ChunkAssemblyRequestV2{}) {
					t.Fatal("nested ambiguity accepted")
				}
			})
		}
	}
	// Every scalar, required object and array member must be present and non-null.
	// Omitempty fields are also non-null when present, enforced by wirejson.
	for _, path := range paths {
		obj := assemblyObjectTestV2(t, base)
		var keys []string
		assemblyWalkObjectsV2(obj, "$", func(p string, m map[string]any) {
			if p == path {
				for key := range m {
					keys = append(keys, key)
				}
			}
		})
		for _, key := range keys {
			for _, attack := range []string{"null", "tamper"} {
				t.Run(path+"/"+key+"/"+attack, func(t *testing.T) {
					obj := assemblyObjectTestV2(t, base)
					assemblyWalkObjectsV2(obj, "$", func(p string, m map[string]any) {
						if p != path {
							return
						}
						if attack == "null" {
							m[key] = nil
							return
						}
						switch v := m[key].(type) {
						case string:
							m[key] = v + "x"
						case json.Number:
							m[key] = json.Number("99999999")
						case map[string]any:
							m[key] = map[string]any{}
						case []any:
							m[key] = []any{}
						default:
							t.Fatalf("untested scalar %T", v)
						}
					})
					raw := assemblyRawTestV2(t, obj)
					if got, err := DecodeChunkAssemblyRequestV2(raw); err == nil || got.Fingerprint != "" {
						t.Fatal("unbound nested field accepted")
					}
				})
			}
		}
	}
	for _, raw := range [][]byte{append(append([]byte(nil), base...), '\n'), append(append([]byte(nil), base...), base...), []byte(`{"schema_version":2.0}`), bytes.Replace(base, []byte(`"schema_version":2`), []byte(`"Schema_Version":2`), 1), bytes.Replace(base, []byte(`"size_bytes":262144`), []byte(`"size_bytes":9223372036854775808`), 1)} {
		if _, err := DecodeChunkAssemblyRequestV2(raw); err == nil {
			t.Fatal("accepted noncanonical/overflow/case alias")
		}
	}
	if _, err := DecodeChunkAssemblyRequestV2(assemblyFixtureV2(t, "document-v2-render-request-mixed.canonical.json")); err == nil {
		t.Fatal("accepted foreign request")
	}
}

type assemblyPanicContextV2 struct{ context.Context }

func (assemblyPanicContextV2) Err() error { panic("host context panic") }

type assemblyStepContextV2 struct {
	context.Context
	calls int
	after int
}

func (c *assemblyStepContextV2) Err() error {
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}

func TestChunkAssemblyV2CancellationPanicAndLimits(t *testing.T) {
	base, s := assemblyTestRequestV2(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for name, ctx := range map[string]context.Context{"nil": nil, "canceled": canceled, "deadline": deadline, "panic": assemblyPanicContextV2{context.Background()}, "midwalk": &assemblyStepContextV2{Context: context.Background(), after: 100}, "late": &assemblyStepContextV2{Context: context.Background(), after: 900}} {
		t.Run(name, func(t *testing.T) {
			out, err := VerifyChunkAssemblyRequestV2AgainstPlan(ctx, base, s.Manifest, s.Plan)
			if err == nil || out.Valid() {
				t.Fatal("failure did not return zero", err)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if name == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			frozen, err := FreezeChunkAssemblyRequestV2(ctx, s.Plan, s.Manifest, base)
			if err == nil || !reflect.DeepEqual(frozen, ChunkAssemblyRequestV2{}) {
				t.Fatal("freeze failure not zero", err)
			}
		})
	}
	for name, mutate := range map[string]func(*ChunkAssemblyRequestV2){"count": func(r *ChunkAssemblyRequestV2) { r.Audio = make([]ChunkDescriptorV2, MaxChunkAssemblyDescriptorsV2+1) }, "string": func(r *ChunkAssemblyRequestV2) {
		r.MediaRuntime.EncoderProfile = strings.Repeat("x", MaxChunkAssemblyStringBytesV2+1)
	}, "bytes": func(r *ChunkAssemblyRequestV2) {
		r.Video = make([]ChunkDescriptorV2, MaxChunkAssemblyDescriptorsV2)
		for i := range r.Video {
			r.Video[i] = r.Audio[0]
		}
		r.Audio = []ChunkDescriptorV2{}
	}} {
		t.Run(name, func(t *testing.T) {
			r := assemblyCloneTestV2(t, base)
			mutate(&r)
			if out, err := VerifyChunkAssemblyRequestV2(r); !errors.Is(err, ErrChunkAssemblyLimitV2) || out.Valid() {
				t.Fatalf("limit not enforced failure-zero: %v", err)
			}
		})
	}
	if got, err := DecodeChunkAssemblyRequestV2(bytes.Repeat([]byte(" "), MaxChunkAssemblyBytesV2+1)); !errors.Is(err, ErrChunkAssemblyLimitV2) || got.Fingerprint != "" {
		t.Fatal("raw byte bound", err)
	}
	deep := []byte(strings.Repeat("[", MaxChunkAssemblyDepthV2+1) + "0" + strings.Repeat("]", MaxChunkAssemblyDepthV2+1))
	if _, err := DecodeChunkAssemblyRequestV2(deep); err == nil {
		t.Fatal("depth bound")
	}
	if _, err := canonicaljson.Canonicalize([]byte(`{"a":1,"a":1}`)); err == nil {
		t.Fatal("canonical parser lost duplicate check")
	}
}

func TestChunkAssemblyV2InactiveDomainAndArithmetic(t *testing.T) {
	base, _ := assemblyTestRequestV2(t)
	for _, name := range []string{"zero-frame", "zero-sample"} {
		r, err := DecodeChunkAssemblyRequestV2(assemblyFixtureV2(t, "document-v2-chunk-assembly-"+name+".canonical.json"))
		if err != nil {
			t.Fatal(err)
		}
		if name == "zero-frame" {
			r.Aggregate.Video = base.Aggregate.Video
		} else {
			r.Aggregate.Audio = base.Aggregate.Audio
		}
		assemblyResignTestV2(t, &r)
		if got, err := VerifyChunkAssemblyRequestV2(r); err == nil || got.Valid() {
			t.Fatal("inactive domain accepted an aggregate")
		}
	}
	// Reach the checked multiplication itself with valid domain/order/range/root
	// metadata, rather than being rejected earlier by a stale fingerprint.
	r := assemblyCloneTestV2(t, base)
	unit := int64(math.MaxInt64/ChunkAssemblySampleBlockCountV2 + 1)
	r.Aggregate.Audio.UnitBytes = unit
	for i := range r.Audio {
		r.Audio[i].UnitBytes = unit
	}
	if err := assemblyDomainV2(context.Background(), r.Audio, r.Aggregate.Audio, api.AudioBlockProfileV2, r.SampleRange, unit, ChunkAssemblySampleBlockCountV2, "audio", evidence.EvidenceSamples); err == nil {
		t.Fatal("payload multiplication overflow accepted")
	}
}

func FuzzChunkAssemblyRequestV2(f *testing.F) {
	for _, name := range []string{"mixed", "zero-frame", "fractional"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "document-v2-chunk-assembly-"+name+".canonical.json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(`{"schema_version":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := DecodeChunkAssemblyRequestV2(raw)
		if err != nil {
			if !reflect.DeepEqual(r, ChunkAssemblyRequestV2{}) {
				t.Fatal("partial failure")
			}
			return
		}
		canonical, err := CanonicalChunkAssemblyRequestV2(r)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatal("accepted noncanonical bytes", err)
		}
	})
}
