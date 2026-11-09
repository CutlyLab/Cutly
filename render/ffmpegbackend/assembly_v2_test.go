package ffmpegbackend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/color"
	decode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

type sourceFuncV2 func(context.Context, evidence.ChunkDescriptorV2) (io.ReadCloser, error)

func (s sourceFuncV2) Open(c context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
	return s(c, d)
}
func digestV2(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func signBindingV2(t *testing.T, r *evidence.BindingV2) {
	t.Helper()
	data, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]json.RawMessage
	if e = json.Unmarshal(data, &m); e != nil {
		t.Fatal(e)
	}
	delete(m, "fingerprint")
	data, e = json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	data, e = canonicaljson.Canonicalize(data)
	if e != nil {
		t.Fatal(e)
	}
	r.Fingerprint = digestV2(data)
}

type assemblyFixtureNativeV2 struct {
	b    *ChunkAssemblerV2
	r    evidence.BindingV2
	v    evidence.VerifiedBindingV2
	data map[cachekey.Key][]byte
	o    ChunkAssemblerOptionsV2
}

func assemblyNativeV2(t *testing.T, samples int64) *assemblyFixtureNativeV2 {
	t.Helper()
	ctx := context.Background()
	path, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg not installed")
	}
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	d := documentv2.Document{SchemaVersion: 2, ID: "assembly-document", Revision: 1, Timelines: []documentv2.Timeline{{ID: "main"}}}
	zero := videoedit.RationalTime{Value: 0, Rate: 1}
	timeline := documentv2.TimeRange{Start: zero, Duration: videoedit.RationalTime{Value: samples, Rate: 48000}}
	target := videoedit.RenderTarget{TimelineID: "main", Width: 16, Height: 16, Channels: 2, PixelFormat: "rgba16", ColorSpace: "linear-srgb", FrameClock: videoedit.FrameClock{Origin: zero, FramesPerSecond: videoedit.RationalTime{Value: 24, Rate: 1}}, SampleClock: videoedit.SampleClock{Origin: zero, SamplesPerSecond: 48000}}
	p, ds := renderplanv2.Resolve(ctx, d, timeline, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("resolve: %+v", ds)
	}
	sdk, worker, deps := digestV2([]byte("sdk")), digestV2([]byte("worker")), digestV2([]byte("dependency closure"))
	caps, e := ChunkAssemblerCapabilitiesV2(sdk)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := ChunkAssemblerRuntimeFingerprintV2(ctx, path, deps)
	if e != nil {
		t.Fatal(e)
	}
	output := api.OutputSpecV2{ReferenceProfile: api.OutputReferenceProfileV2, Format: "mp4", Width: 16, Height: 16, PixelFormat: "rgba16", ColorSpace: "linear-srgb", VideoCodec: "h264", AudioCodec: "aac", FrameClock: target.FrameClock, SampleClock: target.SampleClock, SampleRate: 48000, Channels: 2, Intermediate: api.IntermediateProfilesV2{VideoSurfaceProfile: api.VideoSurfaceProfileV2, AudioBlockProfile: api.AudioBlockProfileV2}, DecodedVideoProfile: api.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: api.DecodedAudioProfilePCM16LEV1}
	m, e := api.FreezeExecutionManifestV2(ctx, p, api.ExecutionManifestV2{CompilerBuild: sdk, EvaluatorBuild: sdk, ProviderBuild: worker, Backend: caps, MediaRuntime: api.MediaRuntime{FFmpegBuild: runtime, DecoderProfile: decode.AdapterSemanticProfileV1, EncoderProfile: ChunkAssemblerEncoderProfileV2, ColorProfile: color.ProfileLinearRGBA16ToRGBA8SRGBV1}, Output: output})
	if e != nil {
		t.Fatal(e)
	}
	o := ChunkAssemblerOptionsV2{Executable: path, OutputRoot: root, SDKBuildDigest: sdk, WorkerExecutableDigest: worker, RuntimeDependencyFingerprint: deps, MaxStagingBytes: 64 << 20, Plan: p, Manifest: m}
	b, e := NewChunkAssemblerV2(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = b.Close() })
	r := evidence.BindingV2{SchemaVersion: 2, Profile: evidence.AssemblyProfileV2, CanonicalJSONProfile: canonicaljson.Profile, JobID: "assembly-native", ArtifactURI: (&url.URL{Scheme: "file", Path: filepath.Join(root, "result.mp4")}).String(), DocumentHash: p.DocumentHash, PlanFingerprint: p.Fingerprint, ExecutionManifestFingerprint: m.Fingerprint, TimelineRange: timeline, FrameRange: p.FrameRange, SampleRange: p.SampleRange, Target: target, MediaRuntime: m.MediaRuntime, Output: m.Output, ProviderExecutableDigest: m.ProviderBuild, Aggregate: evidence.ChunkAggregateBindingV2{Profile: evidence.AggregateProfileV2, ResumeMode: evidence.ResumeModeV2}, Audio: []evidence.ChunkDescriptorV2{}, Video: []evidence.ChunkDescriptorV2{}}
	data := make(map[cachekey.Key][]byte)
	for _, domain := range []struct {
		name, prefix    string
		rg              videoedit.TickRange
		unit, partition int64
		unitName        evidence.EvidenceUnit
	}{{api.AudioBlockProfileV2, "audio", r.SampleRange, 4, 65536, evidence.EvidenceSamples}, {api.VideoSurfaceProfileV2, "video", r.FrameRange, 16 * 16 * 8, 64, evidence.EvidenceFrames}} {
		if domain.rg.Count == 0 {
			continue
		}
		sequence := evidence.DecodedSequence{Domain: domain.name, Unit: domain.unitName, UnitBytes: domain.unit, Range: domain.rg}
		var descriptors []evidence.ChunkDescriptorV2
		for first, i := domain.rg.First, 0; first < domain.rg.First+domain.rg.Count; i++ {
			n := domain.partition
			if n > domain.rg.First+domain.rg.Count-first {
				n = domain.rg.First + domain.rg.Count - first
			}
			payload := make([]byte, n*domain.unit)
			if domain.prefix == "video" {
				for off := 0; off < len(payload); off += 8 {
					binary.LittleEndian.PutUint16(payload[off:], 16000)
					binary.LittleEndian.PutUint16(payload[off+6:], 65535)
				}
			}
			key := cachekey.Key{SchemaVersion: 1, Profile: cachekey.ProfileV1, Namespace: "cutly.assembly-test.v2", ArtifactKind: "cutly.chunk." + domain.prefix + ".v2", Digest: digestV2([]byte(fmt.Sprintf("%s-%d", domain.prefix, i)))}
			desc := evidence.ChunkDescriptorV2{TaskID: fmt.Sprintf("%s-%05d", domain.prefix, i), Domain: domain.name, CacheKey: key, Range: videoedit.TickRange{First: first, Count: n}, ContentHash: digestV2(payload), SizeBytes: int64(len(payload)), UnitBytes: domain.unit}
			descriptors = append(descriptors, desc)
			data[key] = payload
			sequence.Chunks = append(sequence.Chunks, evidence.DecodedChunk{First: first, Count: n, ContentHash: desc.ContentHash})
			first += n
		}
		hash, e := evidence.ComputeDecodedRoot(sequence)
		if e != nil {
			t.Fatal(e)
		}
		aggregate := &evidence.ChunkAggregateRootV2{Domain: domain.name, UnitBytes: domain.unit, Range: domain.rg, ChunkCount: len(descriptors), RootHash: hash}
		if domain.prefix == "audio" {
			r.Audio = descriptors
			r.Aggregate.Audio = aggregate
		} else {
			r.Video = descriptors
			r.Aggregate.Video = aggregate
		}
	}
	signBindingV2(t, &r)
	v, e := evidence.VerifyBindingV2(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	return &assemblyFixtureNativeV2{b: b, r: r, v: v, data: data, o: o}
}
func (f *assemblyFixtureNativeV2) source() ChunkSourceV2 {
	return sourceFuncV2(func(ctx context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(f.data[d.CacheKey])), nil
	})
}
func assertEmptyV2(t *testing.T, f *assemblyFixtureNativeV2) {
	t.Helper()
	entries, e := os.ReadDir(f.o.OutputRoot)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) > 0 {
		t.Fatalf("failure left files: %v", entries)
	}
}

func TestChunkAssemblerV2RealEncodeRepeatRecovery(t *testing.T) {
	for _, samples := range []int64{48000, 48001, 1024, 65537} {
		t.Run(fmt.Sprint(samples), func(t *testing.T) {
			f := assemblyNativeV2(t, samples)
			ctx := context.Background()
			a, e := f.b.AssembleChunks(ctx, f.v, f.source(), nil)
			if e != nil {
				t.Fatal(e)
			}
			m, found, e := f.b.OpenChunkAssembly(ctx, f.v)
			if e != nil || !found {
				t.Fatalf("open %v %v", found, e)
			}
			data, e := io.ReadAll(m)
			if e != nil {
				t.Fatal(e)
			}
			if e = m.Close(); e != nil {
				t.Fatal(e)
			}
			if digestV2(data) != a.ContentHash {
				t.Fatal("snapshot hash")
			}
			sidePath := filepath.Join(f.o.OutputRoot, "result.mp4.evidence.json")
			raw, e := os.ReadFile(sidePath)
			if e != nil {
				t.Fatal(e)
			}
			first, e := evidence.DecodeManifestV2(raw)
			if e != nil {
				t.Fatal(e)
			}
			// Restart does not reopen/recompute pre-codec CAS when complete pair verifies.
			restarted, e := NewChunkAssemblerV2(ctx, f.o)
			if e != nil {
				t.Fatal(e)
			}
			defer restarted.Close()
			a2, e := restarted.AssembleChunks(ctx, f.v, sourceFuncV2(func(context.Context, evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
				t.Error("recovery read CAS")
				return nil, errors.New("unexpected CAS")
			}), nil)
			if e != nil || a2 != a {
				t.Fatalf("recovery mismatch %v", e)
			}
			if e = os.Remove(filepath.Join(f.o.OutputRoot, "result.mp4")); e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(sidePath); e != nil {
				t.Fatal(e)
			}
			if _, e = f.b.AssembleChunks(ctx, f.v, f.source(), nil); e != nil {
				t.Fatal(e)
			}
			raw, e = os.ReadFile(sidePath)
			if e != nil {
				t.Fatal(e)
			}
			second, e := evidence.DecodeManifestV2(raw)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(first.DecodedVideo, second.DecodedVideo) || !reflect.DeepEqual(first.DecodedAudio, second.DecodedAudio) {
				t.Fatal("nonrepeatable decoded roots")
			}
		})
	}
}

func TestChunkAssemblerV2UntrustedChunksFailureZero(t *testing.T) {
	for _, mode := range []string{"short", "extra", "tamper", "panic-open", "panic-read", "close-error", "panic-close", "enospc", "cancel", "out-of-order"} {
		t.Run(mode, func(t *testing.T) {
			f := assemblyNativeV2(t, 48000)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			source := sourceFuncV2(func(c context.Context, d evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
				calls++
				payload := append([]byte(nil), f.data[d.CacheKey]...)
				switch mode {
				case "short":
					payload = payload[:len(payload)-1]
				case "extra":
					payload = append(payload, 0)
				case "tamper":
					payload[0] ^= 1
				case "panic-open":
					panic("open")
				case "cancel":
					cancel()
				case "out-of-order":
					payload = f.data[f.r.Video[0].CacheKey]
				}
				return &faultReaderV2{Reader: bytes.NewReader(payload), mode: mode}, nil
			})
			a, e := f.b.AssembleChunks(ctx, f.v, source, nil)
			if e == nil || a != (api.RenderArtifact{}) {
				t.Fatalf("failure-zero: %v %+v", e, a)
			}
			if calls != 1 {
				t.Fatalf("opened later descriptor after error: %d", calls)
			}
			assertEmptyV2(t, f)
		})
	}
}

type faultReaderV2 struct {
	*bytes.Reader
	mode string
}

func (r *faultReaderV2) Read(p []byte) (int, error) {
	switch r.mode {
	case "panic-read":
		panic("read")
	case "enospc":
		return 0, syscall.ENOSPC
	}
	return r.Reader.Read(p)
}
func (r *faultReaderV2) Close() error {
	switch r.mode {
	case "panic-close":
		panic("close")
	case "close-error":
		return errors.New("close failed")
	}
	return nil
}

func TestChunkAssemblerV2PublicationFaultsAndOwnership(t *testing.T) {
	for _, mode := range []string{"sync-journal", "sync-evidence", "sync-pair", "sink-panic", "sink-error", "collision-media", "collision-side", "replace-owned", "replace-owned-success", "mutate-media-success", "mutate-side-success"} {
		t.Run(mode, func(t *testing.T) {
			f := assemblyNativeV2(t, 48000)
			root := f.o.OutputRoot
			var preserved string
			syncs := 0
			original := f.b.io.syncDirectory
			f.b.io.syncDirectory = func() error {
				syncs++
				if mode == "sync-journal" && syncs == 1 {
					return syscall.EIO
				}
				if mode == "sync-evidence" && syncs == 3 {
					return syscall.ENOSPC
				}
				if mode == "sync-pair" && syncs == 4 {
					return syscall.EIO
				}
				return original()
			}
			sink := api.EventSinkFunc(func(c context.Context, event api.ProgressEvent) error {
				if event.State == api.JobComplete {
					switch mode {
					case "mutate-media-success":
						return os.WriteFile(filepath.Join(root, "result.mp4"), []byte("in-place tamper"), 0600)
					case "mutate-side-success":
						return os.WriteFile(filepath.Join(root, "result.mp4.evidence.json"), []byte("in-place tamper"), 0600)
					case "sink-panic":
						panic("sink")
					case "sink-error":
						return errors.New("sink failed")
					case "replace-owned", "replace-owned-success":
						preserved = filepath.Join(root, "result.mp4")
						if e := os.Remove(preserved); e != nil {
							return e
						}
						return os.WriteFile(preserved, []byte("foreign"), 0600)
					}
				}
				if event.Stage == api.StagePublish && event.State == api.JobRunning {
					switch mode {
					case "collision-media":
						preserved = filepath.Join(root, "result.mp4")
					case "collision-side":
						preserved = filepath.Join(root, "result.mp4.evidence.json")
					}
					if preserved != "" {
						return os.WriteFile(preserved, []byte("foreign"), 0600)
					}
				}
				return nil
			})
			if mode == "replace-owned" { // Force failure after replacement to exercise owned-only rollback.
				sink = api.EventSinkFunc(func(c context.Context, event api.ProgressEvent) error {
					if event.State == api.JobComplete {
						preserved = filepath.Join(root, "result.mp4")
						if e := os.Remove(preserved); e != nil {
							return e
						}
						if e := os.WriteFile(preserved, []byte("foreign"), 0600); e != nil {
							return e
						}
						return errors.New("replacement fault")
					}
					return nil
				})
			}
			a, e := f.b.AssembleChunks(context.Background(), f.v, f.source(), sink)
			if e == nil || a != (api.RenderArtifact{}) {
				t.Fatalf("fault exposed artifact: %v", e)
			}
			if preserved != "" {
				data, e := os.ReadFile(preserved)
				if e != nil || string(data) != "foreign" {
					t.Fatalf("foreign collision removed %q %v", data, e)
				}
				if e = os.Remove(preserved); e != nil {
					t.Fatal(e)
				}
			}
			assertEmptyV2(t, f)
		})
	}
}

func TestChunkAssemblerV2RejectsAuthorityDrift(t *testing.T) {
	f := assemblyNativeV2(t, 48000)
	for _, field := range []string{"provider", "runtime", "output-root", "budget", "zero"} {
		t.Run(field, func(t *testing.T) {
			r := f.r
			v := f.v
			switch field {
			case "provider":
				r.ProviderExecutableDigest = digestV2([]byte("foreign-provider"))
				signBindingV2(t, &r)
				var e error
				v, e = evidence.VerifyBindingV2(context.Background(), r)
				if e != nil {
					t.Fatal(e)
				}
			case "runtime":
				r.MediaRuntime.FFmpegBuild = digestV2([]byte("foreign-runtime"))
				signBindingV2(t, &r)
				var e error
				v, e = evidence.VerifyBindingV2(context.Background(), r)
				if e != nil {
					t.Fatal(e)
				}
			case "output-root":
				r.ArtifactURI = "file:///tmp/escape.mp4"
				signBindingV2(t, &r)
				var e error
				v, e = evidence.VerifyBindingV2(context.Background(), r)
				if e != nil {
					t.Fatal(e)
				}
			case "budget":
				old := f.b.maxStagingBytes
				f.b.maxStagingBytes = 1
				defer func() { f.b.maxStagingBytes = old }()
			case "zero":
				v = evidence.VerifiedBindingV2{}
			}
			opened := false
			a, e := f.b.AssembleChunks(context.Background(), v, sourceFuncV2(func(context.Context, evidence.ChunkDescriptorV2) (io.ReadCloser, error) {
				opened = true
				return nil, nil
			}), nil)
			if e == nil || opened || a != (api.RenderArtifact{}) {
				t.Fatalf("authority fail %v opened=%v", e, opened)
			}
			assertEmptyV2(t, f)
		})
	}
	o := f.o
	o.WorkerExecutableDigest = digestV2([]byte("not-provider"))
	if b, e := NewChunkAssemblerV2(context.Background(), o); e == nil {
		b.Close()
		t.Fatal("accepted unrelated worker")
	}
}

// Test helpers below use controlled local scripts only; no external runtime is fetched.
func commandShimV2(t *testing.T, b *ChunkAssemblerV2, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg-test-shim")
	if e := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0700); e != nil {
		t.Fatal(e)
	}
	b.io.executable = p
	hash, e := executableFingerprint(context.Background(), p, b.io.maxStderrBytes, b.io.runtimeDependencyFingerprint, b.io.runtimeEnvironment)
	if e != nil {
		t.Fatal(e)
	}
	b.io.runtimeFingerprint = hash
	b.manifest.MediaRuntime.FFmpegBuild = hash
}

func TestChunkAssemblerV2DecodedAudioProjectionBounds(t *testing.T) {
	f := assemblyNativeV2(t, 48001)
	dummy, e := f.b.anonymousV2()
	if e != nil {
		t.Fatal(e)
	}
	defer dummy.Close()
	for _, n := range []int64{48000, 48001, 48128, 49152} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			commandShimV2(t, f.b, fmt.Sprintf("if [ \"$1\" = -version ]; then printf 'ffmpeg version shim\\n'; exit 0; fi\ndd if=/dev/zero bs=4 count=%d 2>/dev/null", n))
			seq, e := f.b.decodedAudioV2(context.Background(), dummy, f.r)
			valid := n >= 48001 && n <= 48128
			if valid {
				if e != nil || seq.Range.Count != 48001 {
					t.Fatalf("valid padding %v", e)
				}
			} else if e == nil {
				t.Fatal("hid short or extra decoded frame")
			}
		})
	}
}

func TestChunkAssemblerV2EncoderFaultCancelAndStderr(t *testing.T) {
	for _, mode := range []string{"exit", "broken-pipe", "stderr", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := assemblyNativeV2(t, 48000)
			body := "if [ \"$1\" = -version ]; then printf 'ffmpeg version shim\\n'; exit 0; fi\n"
			switch mode {
			case "exit":
				body += "exit 9"
			case "broken-pipe":
				body += "printf 'Broken pipe\\n' >&2; exit 1"
			case "stderr":
				body += "dd if=/dev/zero bs=4096 count=512 >&2 2>/dev/null; exit 0"
			case "cancel":
				body += "while :; do :; done"
			}
			commandShimV2(t, f.b, body)
			// Re-freeze host authority and binding after pinning the controlled local shim.
			m, e := api.FreezeExecutionManifestV2(context.Background(), f.o.Plan, f.b.manifest)
			if e != nil {
				t.Fatal(e)
			}
			f.b.manifest = m
			f.r.MediaRuntime = m.MediaRuntime
			f.r.ExecutionManifestFingerprint = m.Fingerprint
			signBindingV2(t, &f.r)
			f.v, e = evidence.VerifyBindingV2(context.Background(), f.r)
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if mode == "cancel" {
				ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
				defer cancel()
			}
			a, e := f.b.AssembleChunks(ctx, f.v, f.source(), nil)
			if e == nil || a != (api.RenderArtifact{}) {
				t.Fatalf("encoder fault %v", e)
			}
			if mode == "cancel" && !errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("lost cancellation %v", e)
			}
			if mode == "stderr" && !strings.Contains(e.Error(), "stderr-overflow=true") {
				t.Fatalf("stderr cap not enforced %v", e)
			}
			assertEmptyV2(t, f)
		})
	}
}

func TestChunkAssemblerV2RecoveryRejectsTamperingWithoutDeletion(t *testing.T) {
	for _, mode := range []string{"media", "evidence", "forged-root", "missing-media", "missing-evidence", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			f := assemblyNativeV2(t, 48000)
			if _, e := f.b.AssembleChunks(context.Background(), f.v, f.source(), nil); e != nil {
				t.Fatal(e)
			}
			mp := filepath.Join(f.o.OutputRoot, "result.mp4")
			ep := mp + ".evidence.json"
			switch mode {
			case "media":
				if e := os.WriteFile(mp, []byte("tampered"), 0600); e != nil {
					t.Fatal(e)
				}
			case "evidence":
				if e := os.WriteFile(ep, []byte("{}"), 0600); e != nil {
					t.Fatal(e)
				}
			case "missing-media":
				if e := os.Remove(mp); e != nil {
					t.Fatal(e)
				}
			case "missing-evidence":
				if e := os.Remove(ep); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Remove(mp); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(ep, mp); e != nil {
					t.Fatal(e)
				}
			case "forged-root":
				data, e := os.ReadFile(ep)
				if e != nil {
					t.Fatal(e)
				}
				m, e := evidence.DecodeManifestV2(data)
				if e != nil {
					t.Fatal(e)
				}
				m.DecodedVideo.Chunks[0].ContentHash = digestV2([]byte("fabricated decoded frame"))
				m.DecodedVideo.RootHash = ""
				m, e = evidence.NewManifestV2(context.Background(), m.Assembly, m.WorkerExecutableDigest, m.Artifact.ContentHash, m.Artifact.SizeBytes, m.DecodedVideo, m.DecodedAudio)
				if e != nil {
					t.Fatal(e)
				}
				data, e = evidence.CanonicalManifestV2(m)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(ep, data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadDir(f.o.OutputRoot)
			if e != nil {
				t.Fatal(e)
			}
			a, found, e := f.b.InspectChunkAssembly(context.Background(), f.v)
			if e == nil || found || a != (api.RenderArtifact{}) {
				t.Fatalf("accepted tamper %v", e)
			}
			after, e := os.ReadDir(f.o.OutputRoot)
			if e != nil {
				t.Fatal(e)
			}
			if len(before) != len(after) {
				t.Fatal("recovery deleted unproven remnants")
			}
		})
	}
}
