package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

func fixtureV2(t *testing.T, provider string) (RequestV2, LaunchConfigV2) {
	t.Helper()
	ctx := context.Background()
	raw, err := os.ReadFile("../../conformance/fixtures/document-v2-render-request-gap-silence.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	req, err := api.DecodeRenderRequestV2(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../conformance/fixtures/document-v2-render-plan-gap.document.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	target := req.Plan.Target
	target.Width = 16
	target.Height = 16
	p, ds := renderplanv2.Resolve(ctx, d, req.Plan.RequestedRange, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	m := req.Manifest
	m.ProviderBuild = provider
	m.Output.Width = 16
	m.Output.Height = 16
	m.Visual = api.ProgramSelectionV2{}
	m.Audio = api.ProgramSelectionV2{}
	m, err = api.FreezeExecutionManifestV2(ctx, p, m)
	if err != nil {
		t.Fatal(err)
	}
	env, err := prepared.NewEnvironmentV2(ctx, d, p, m)
	if err != nil {
		t.Fatal(err)
	}
	r := RequestV2{SchemaVersion: 2, Profile: RequestProfileV2, InvocationID: "native-test", Attempt: 1, ProviderExecutableDigest: provider, Environment: env, Chunk: ChunkV2{Domain: "audio", Range: p.SampleRange, IntermediateProfile: api.AudioBlockProfileV2}}
	c := LaunchConfigV2{Profile: LaunchConfigProfileV2, AllowLocalDevelopment: true, CompilerBuild: m.CompilerBuild, EvaluatorBuild: m.EvaluatorBuild, Backend: m.Backend, MediaRuntime: m.MediaRuntime, Width: 16, Height: 16, Resources: []HostResourceV2{}}
	return r, c
}
func launchFileV2(t *testing.T, c LaunchConfigV2) *os.File {
	t.Helper()
	raw, err := CanonicalLaunchConfigV2(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "launch.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenLaunchConfigV2(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func buildChildV2(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "cutly-worker-v2")
	command := exec.Command("go", "build", "-o", path, "../../cmd/cutly-worker-v2")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, raw)
	}
	return path
}
func TestProductionChildV2(t *testing.T) {
	path := buildChildV2(t)
	provider, err := measureAuthorizedV2(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	request, config := fixtureV2(t, provider)
	if err := AuthorizeLaunchEnvironmentV2(context.Background(), config, path, request.Environment); err != nil {
		t.Fatal("parent host authorization", err)
	}
	runner, err := NewRunnerV2(ConfigV2{Process: Config{Executable: path, WorkingDirectory: filepath.Dir(path)}, LaunchConfig: launchFileV2(t, config), SpoolDirectory: t.TempDir(), MaxPayloadBytes: MaxStreamPayloadBytesV2})
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"audio", "video"} {
		t.Run(domain, func(t *testing.T) {
			r := request
			if domain == "video" {
				r.Chunk = ChunkV2{Domain: "video", Range: r.Environment.Plan.FrameRange, IntermediateProfile: api.VideoSurfaceProfileV2}
			}
			out, report, err := runner.Run(context.Background(), r)
			if err != nil {
				t.Fatalf("run: %v stderr=%s", err, report.Stderr)
			}
			if !out.OK || out.Receipt == nil || report.ExecutableHash != provider || report.Profile != RunnerProfileV2 || !report.DescendantIsolation {
				t.Fatalf("out=%+v report=%+v", out, report)
			}
			for _, b := range out.Payload {
				if b != 0 {
					t.Fatal("gap/silence not zero")
				}
			}
			out.Payload[0] = 255
			again, _, err := runner.Run(context.Background(), r)
			if err != nil || again.Payload[0] != 0 {
				t.Fatal("output ownership", err)
			}
		})
	}
	// A foreign digest must fail before canonicalizing a deliberately malformed document.
	foreign := request
	foreign.ProviderExecutableDigest = digestV2([]byte("foreign"))
	foreign.Environment.Document.Timelines = nil
	if out, report, err := runner.Run(context.Background(), foreign); err == nil || !reflect.DeepEqual(out, ResultV2{}) || report.ExecutableHash != "" {
		t.Fatal("foreign provider was admitted", err)
	}
	// A private but independently wrong host policy must fail; request cannot repair it.
	config.CompilerBuild = digestV2([]byte("other compiler"))
	wrong, err := NewRunnerV2(ConfigV2{Process: Config{Executable: path, WorkingDirectory: filepath.Dir(path)}, LaunchConfig: launchFileV2(t, config), SpoolDirectory: t.TempDir(), MaxPayloadBytes: MaxStreamPayloadBytesV2})
	if err != nil {
		t.Fatal(err)
	}
	out, report, err := wrong.Run(context.Background(), request)
	if err == nil && (out.OK || out.FailureClass == "") {
		t.Fatalf("foreign host policy succeeded %+v %+v", out, report)
	}
}

func TestProtocolV2StrictOwnedAndLimits(t *testing.T) {
	r, _ := fixtureV2(t, digestV2([]byte("provider")))
	raw, err := CanonicalRequestV2(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequestV2(context.Background(), raw, r.ProviderExecutableDigest)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Environment.Document.Timelines[0].ID = "mutated"
	again, err := DecodeRequestV2(context.Background(), raw, r.ProviderExecutableDigest)
	if err != nil || again.Environment.Document.Timelines[0].ID != "main" {
		t.Fatal("request alias", err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"null":      func(b []byte) []byte { return bytes.Replace(b, []byte(`"attempt":1`), []byte(`"attempt":null`), 1) },
		"duplicate": func(b []byte) []byte { return append([]byte(`{"attempt":1,`), b[1:]...) },
		"unknown":   func(b []byte) []byte { return append([]byte(`{"unknown":1,`), b[1:]...) },
		"fraction":  func(b []byte) []byte { return bytes.Replace(b, []byte(`"attempt":1`), []byte(`"attempt":1.0`), 1) },
		"trailing":  func(b []byte) []byte { return append(b, ' ') },
		"environment-null": func(b []byte) []byte {
			var w map[string]json.RawMessage
			json.Unmarshal(b, &w)
			w["environment"] = json.RawMessage(`null`)
			v, _ := json.Marshal(w)
			return v
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := DecodeRequestV2(context.Background(), mutate(bytes.Clone(raw)), r.ProviderExecutableDigest)
			if err == nil || !reflect.DeepEqual(out, RequestV2{}) {
				t.Fatal("invalid admitted", err)
			}
		})
	}
	bad := r
	bad.Chunk.Range.Count--
	if _, err = CanonicalRequestV2(context.Background(), bad); err == nil {
		t.Fatal("partial native partition admitted")
	}
	bad = r
	bad.Chunk.IntermediateProfile = "v1"
	if _, err = CanonicalRequestV2(context.Background(), bad); err == nil {
		t.Fatal("foreign intermediate admitted")
	}
	if _, err = DecodeRequestV2(context.Background(), raw, digestV2([]byte("foreign"))); err == nil {
		t.Fatal("foreign self admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = DecodeRequestV2(ctx, raw, r.ProviderExecutableDigest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	payload := []byte{1, 2, 3, 4}
	receipt := receiptV2(r, payload)
	result := ResultV2{SchemaVersion: 2, Profile: ResultProfileV2, InvocationID: r.InvocationID, Attempt: 1, OK: true, Receipt: &receipt, Payload: payload}
	encoded, err := CanonicalResultV2(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResultV2(encoded); err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"size_bytes":4`), []byte(`"size_bytes":5`), 1)
	if out, err := DecodeResultV2(encoded); err == nil || !reflect.DeepEqual(out, ResultV2{}) {
		t.Fatal("tamper admitted", err)
	}
	result.Payload = make([]byte, MaxPayloadBytesV2+1)
	if _, err = CanonicalResultV2(result); err == nil {
		t.Fatal("oversize result admitted")
	}
}

func TestProtocolV2AdmitsDescriptorStreamBeyondInlineLimit(t *testing.T) {
	ctx := context.Background()
	r, _ := fixtureV2(t, digestV2([]byte("provider")))
	target := r.Environment.Plan.Target
	target.Width = 1920
	target.Height = 1080
	p, diagnostics := renderplanv2.Resolve(ctx, r.Environment.Document, r.Environment.Plan.RequestedRange, target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	m := r.Environment.Manifest
	m.Output.Width = 1920
	m.Output.Height = 1080
	m.Visual = api.ProgramSelectionV2{}
	m.Audio = api.ProgramSelectionV2{}
	var err error
	m, err = api.FreezeExecutionManifestV2(ctx, p, m)
	if err != nil {
		t.Fatal(err)
	}
	r.Environment, err = prepared.NewEnvironmentV2(ctx, r.Environment.Document, p, m)
	if err != nil {
		t.Fatal(err)
	}
	r.Chunk = ChunkV2{Domain: "video", Range: p.FrameRange, IntermediateProfile: api.VideoSurfaceProfileV2}
	size, err := chunkSizeV2(r)
	if err != nil || size <= MaxPayloadBytesV2 || size > MaxStreamPayloadBytesV2 {
		t.Fatalf("stream size=%d err=%v", size, err)
	}
	if _, err = CanonicalRequestV2(ctx, r); err != nil {
		t.Fatal("large descriptor-stream request rejected", err)
	}
	called := false
	if _, err = ExecuteNativeV2(ctx, r, func(context.Context, prepared.EnvironmentV2) (NativeBundleV2, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrOutputLimit) || called {
		t.Fatalf("inline compatibility path err=%v reconstruct-called=%v", err, called)
	}
}

type faultBundleV2 struct {
	mode   string
	closed bool
}

func (b *faultBundleV2) Close() error {
	b.closed = true
	if b.mode == "close" {
		return errors.New("close")
	}
	if b.mode == "close-panic" {
		panic("close")
	}
	return nil
}
func (b *faultBundleV2) RenderSurfaceFrame(context.Context, int64) (referencevisual.SurfaceFrameResult, error) {
	panic("frame")
}
func (b *faultBundleV2) RenderPCM(context.Context, int64, int64) (pcm16le.Result, error) {
	if b.mode == "panic" {
		panic("opaque")
	}
	return pcm16le.Result{}, errors.New("native failure")
}
func TestExecuteNativeV2FailureZero(t *testing.T) {
	r, _ := fixtureV2(t, digestV2([]byte("provider")))
	for _, mode := range []string{"panic", "failure", "close", "close-panic", "construct-panic"} {
		t.Run(mode, func(t *testing.T) {
			b := &faultBundleV2{mode: mode}
			out, err := ExecuteNativeV2(context.Background(), r, func(context.Context, prepared.EnvironmentV2) (NativeBundleV2, error) {
				if mode == "construct-panic" {
					panic("construct")
				}
				return b, nil
			})
			if err == nil || !reflect.DeepEqual(out, ResultV2{}) || mode != "construct-panic" && !b.closed {
				t.Fatal("nonzero failure or leaked bundle", err)
			}
		})
	}
}

// Actual child harness modes exercise runner process failures and cancellation.
func TestWorkerV2ProcessHelper(t *testing.T) {
	mode := os.Getenv("CUTLY_WORKER_V2_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "hang":
		_ = os.WriteFile(os.Getenv("CUTLY_WORKER_V2_MARKER"), []byte("started"), 0600)
		for {
			time.Sleep(time.Second)
		}
	case "crash":
		os.Exit(7)
	case "panic":
		panic("child panic")
	case "null":
		io.WriteString(os.Stdout, `null`)
		os.Exit(0)
	case "limit":
		_, _ = io.CopyN(os.Stdout, zeroReaderV2{}, MaxResultBytesV2+1)
		os.Exit(0)
	case "serve":
		err := ServeV2(context.Background(), os.Stdin, os.Stdout, func(context.Context) (ReconstructV2, error) {
			return func(context.Context, prepared.EnvironmentV2) (NativeBundleV2, error) { panic("native") }, nil
		})
		if err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	}
	os.Exit(8)
}

type zeroReaderV2 struct{}

func (zeroReaderV2) Read(b []byte) (int, error) { clear(b); return len(b), nil }
func TestRunnerV2RealFailureAndCancel(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := measureAuthorizedV2(context.Background(), self)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := fixtureV2(t, provider)
	for _, mode := range []string{"crash", "panic", "null", "limit", "serve", "hang"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			runner, err := NewRunnerV2(ConfigV2{Process: Config{Executable: self, Arguments: []string{"-test.run=^TestWorkerV2ProcessHelper$"}, Environment: []string{"CUTLY_WORKER_V2_HELPER=" + mode, "CUTLY_WORKER_V2_MARKER=" + marker}, WorkingDirectory: filepath.Dir(self), Timeout: 5 * time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "hang" {
				go func() {
					for i := 0; i < 1000; i++ {
						if _, e := os.Stat(marker); e == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
					cancel()
				}()
			}
			started := time.Now()
			out, report, err := runner.Run(ctx, request)
			if mode == "serve" {
				if err != nil || out.OK || out.Payload != nil {
					t.Fatalf("panic child failure %v %+v", err, out)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(out, ResultV2{}) {
				t.Fatal("failure returned success", err)
			}
			if mode == "hang" && (!errors.Is(err, context.Canceled) || !report.Canceled || time.Since(started) >= 2*time.Second) {
				t.Fatalf("cancel took %s report=%+v err=%v", time.Since(started), report, err)
			}
		})
	}
	if _, err = NewRunnerV2(ConfigV2{Process: Config{Executable: self, WorkingDirectory: filepath.Dir(self), TerminationGrace: time.Second}}); err == nil {
		t.Fatal("long grace admitted")
	}
}
func TestLaunchDescriptorV2(t *testing.T) {
	r, c := fixtureV2(t, digestV2([]byte("provider")))
	_ = r
	f := launchFileV2(t, c)
	for i := 0; i < 2; i++ {
		got, err := ReadLaunchConfigV2(f)
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Fatal("descriptor decode", err)
		}
	}
	if err := os.Chmod(f.Name(), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLaunchConfigV2(f); err == nil {
		t.Fatal("public config admitted")
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f.Name(), symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLaunchConfigV2(symlink); err == nil {
		t.Fatal("symlink admitted")
	}
	if _, err := OpenLaunchConfigV2(strings.Repeat("x", 4)); err == nil {
		t.Fatal("relative path admitted")
	}
}
