package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/agent/httpapi"
	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestConfigAllowsOnlyExplicitLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9817", "[::1]:9817", "localhost:9817"} {
		if _, err := parseConfig([]string{"--listen", address}); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{":9817", "0.0.0.0:9817", "[::]:9817", "example.com:9817", "bad"} {
		if _, err := parseConfig([]string{"--listen", address}); err == nil {
			t.Fatalf("%s accepted", address)
		}
	}
	for _, args := range [][]string{
		{"--project-id", "project"},
		{"--project-dir", t.TempDir()},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Fatalf("unpaired project config accepted: %v", args)
		}
	}
}

func TestRenderConfigIsExplicitCompleteAndBounded(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	complete := []string{
		"--project-id", "project", "--project-dir", root,
		"--render-state-dir", filepath.Join(root, "state"),
		"--render-output-dir", filepath.Join(root, "output"),
		"--render-input-root", filepath.Join(root, "input"),
		"--unsafe-allow-unverified-media-runtime",
		"--sdk-build-digest", digest,
		"--compiler-build", digest,
		"--evaluator-build", digest,
		"--ffmpeg-runtime-dependency-fingerprint", digest,
		"--ffprobe-runtime-dependency-fingerprint", digest,
	}
	configuration, err := parseConfig(complete)
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.render.enabled || len(configuration.render.inputRoots) != 1 || configuration.writeTimeout != 10*time.Minute {
		t.Fatalf("configuration = %+v", configuration)
	}
	tooManyInputs := append([]string{}, complete...)
	for index := 1; index < maxRenderInputRoots+1; index++ {
		tooManyInputs = append(tooManyInputs, "--render-input-root", filepath.Join(root, fmt.Sprintf("input-%d", index)))
	}

	for name, arguments := range map[string][]string{
		"render flag without state": {"--ffmpeg", "/bin/ffmpeg"},
		"render without project": {
			"--render-state-dir", filepath.Join(root, "state"),
		},
		"relative state":   append(append([]string{}, complete...), "--render-state-dir", "state"),
		"relative project": append(append([]string{}, complete...), "--project-dir", "project"),
		"missing input": {
			"--project-id", "project", "--project-dir", root,
			"--render-state-dir", filepath.Join(root, "state"),
			"--render-output-dir", filepath.Join(root, "output"),
			"--sdk-build-digest", digest, "--compiler-build", digest, "--evaluator-build", digest,
			"--ffmpeg-runtime-dependency-fingerprint", digest, "--ffprobe-runtime-dependency-fingerprint", digest,
		},
		"bad identity":     append(append([]string{}, complete...), "--compiler-build", "latest"),
		"zero memory":      append(append([]string{}, complete...), "--render-stage-memory-bytes", "0"),
		"too many workers": append(append([]string{}, complete...), "--render-max-concurrent-jobs", "1025"),
		"too many inputs":  tooManyInputs,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig(arguments); err == nil {
				t.Fatalf("invalid render configuration accepted: %v", arguments)
			}
		})
	}
}

func TestMuxHealthAndCapabilities(t *testing.T) {
	actionRouter, err := router.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := newMux(actionRouter)

	healthRequest := httptest.NewRequest(http.MethodGet, "http://cutly.invalid/healthz", nil)
	healthResponse := httptest.NewRecorder()
	mux.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health = %d %s", healthResponse.Code, healthResponse.Body.String())
	}

	agentRequest := httptest.NewRequest(http.MethodPost, "http://cutly.invalid/v1/agent", strings.NewReader(
		`{"protocol_version":1,"request_id":"doctor","tool":"cutly_project","action":"capabilities","payload":{}}`,
	))
	agentRequest.Header.Set("Content-Type", "application/json")
	agentResponse := httptest.NewRecorder()
	mux.ServeHTTP(agentResponse, agentRequest)
	if agentResponse.Code != http.StatusOK {
		t.Fatalf("agent = %d %s", agentResponse.Code, agentResponse.Body.String())
	}
	result, err := agentprotocol.DecodeResult(agentResponse.Body.Bytes())
	if err != nil || !result.OK {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil || receipt["profile"] != "cutly.agent-capabilities.v1" {
		t.Fatalf("receipt = %s, err = %v", result.Receipt, err)
	}
}

func TestServeRequiresDependencies(t *testing.T) {
	if err := serve(nil, config{}, nil, nil); err == nil {
		t.Fatal("nil dependencies accepted")
	}
	if err := validateLoopbackAddress("127.0.0.1"); err == nil {
		t.Fatal("missing port accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx // Context behavior is covered by integration builds; tests do not bind sockets.
}

func TestApplicationWithoutRenderIsIdempotentlyClosable(t *testing.T) {
	application, err := buildApplication(context.Background(), config{})
	if err != nil {
		t.Fatal(err)
	}
	if application.handler == nil || application.Errors() != nil {
		t.Fatalf("application = %+v", application)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRenderApplicationOwnsAndReopensDurableState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor FFmpeg profile is Unix-only")
	}
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobe, ffprobeErr := exec.LookPath("ffprobe")
	if ffmpegErr != nil || ffprobeErr != nil {
		t.Skip("FFmpeg and FFprobe are required for the production wiring smoke test")
	}
	root := t.TempDir()
	configuration := config{
		projectID: "project", projectDir: filepath.Join(root, "project"),
		render: renderConfig{
			enabled: true, stateDir: filepath.Join(root, "state"), outputDir: filepath.Join(root, "output"),
			inputRoots: stringList{filepath.Join(root, "input")}, ffmpegExecutable: ffmpeg, ffprobeExecutable: ffprobe,
			unsafeAllowUnverifiedMediaRuntime: true,
			sdkBuildDigest:                    testSHA256('a'), compilerBuild: testSHA256('b'), evaluatorBuild: testSHA256('c'),
			ffmpegRuntimeDependencyFingerprint: testSHA256('d'), ffprobeRuntimeDependencyFingerprint: testSHA256('e'),
			stageMemoryBytes: 64 << 20, stageDiskBytes: 256 << 20, maxConcurrentJobs: 1,
			workerPollInterval: 10 * time.Millisecond,
		},
	}
	for _, directory := range []string{
		configuration.projectDir, configuration.render.stateDir, configuration.render.outputDir, configuration.render.inputRoots[0],
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	failedInitialization := configuration
	failedInitialization.render.stageMemoryBytes = 0
	if failed, err := buildApplication(context.Background(), failedInitialization); err == nil {
		if failed != nil {
			_ = failed.Close()
		}
		t.Fatal("invalid late-stage configuration initialized")
	}

	first, err := buildApplication(context.Background(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	assertRenderRoutesAndAdmission(t, first)
	if _, err := buildApplication(context.Background(), configuration); !errors.Is(err, ErrRenderStateAlreadyOwned) {
		_ = first.Close()
		t.Fatalf("second owner = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	otherProject := filepath.Join(root, "other-project")
	if err := os.Mkdir(otherProject, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignConfiguration := configuration
	foreignConfiguration.projectID = "other-project"
	foreignConfiguration.projectDir = otherProject
	if foreign, err := buildApplication(context.Background(), foreignConfiguration); !errors.Is(err, ErrRenderStateProjectMismatch) {
		if foreign != nil {
			_ = foreign.Close()
		}
		t.Fatalf("foreign project reused render state: %v", err)
	}
	reopened, err := buildApplication(context.Background(), configuration)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRenderRoutesAndAdmission(t *testing.T, application *application) {
	t.Helper()
	capabilities, err := application.handler.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "capabilities",
		Tool: agentprotocol.ToolProject, Action: "capabilities", Payload: json.RawMessage(`{}`),
	})
	if err != nil || !capabilities.OK {
		t.Fatalf("capabilities=%+v error=%v", capabilities, err)
	}
	var receipt struct {
		Actions []struct {
			Tool   agentprotocol.Tool `json:"tool"`
			Action string             `json:"action"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(capabilities.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"cutly_render/plan": false, "cutly_render/prepare": false, "cutly_render/preflight": false, "cutly_render/frame": false, "cutly_render/render": false,
		"cutly_render/status": false, "cutly_render/history": false, "cutly_render/cancel": false,
	}
	for _, action := range receipt.Actions {
		key := string(action.Tool) + "/" + action.Action
		if _, exists := wanted[key]; exists {
			wanted[key] = true
		}
	}
	for action, found := range wanted {
		if !found {
			t.Fatalf("render capability %s is missing: %s", action, capabilities.Receipt)
		}
	}

	foreign, err := application.handler.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "foreign-status",
		Tool: agentprotocol.ToolRender, Action: "status", ProjectID: "another-project",
		Payload: json.RawMessage(`{"job_id":"job-unprepared"}`),
	})
	if err != nil || foreign.OK || len(foreign.Diagnostics) != 1 || foreign.Diagnostics[0].Code != "RENDER.PROJECT_MISMATCH" {
		t.Fatalf("foreign status=%+v error=%v", foreign, err)
	}

	job := renderapi.RenderJob{
		SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "job-unprepared", IdempotencyKey: "job-unprepared-once",
		PlanFingerprint: testSHA256('1'), ExecutionManifestFingerprint: testSHA256('2'),
		TimelineRange: videoedit.TimeRange{
			Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		},
		FrameRange: videoedit.TickRange{First: 0, Count: 30}, SampleRange: videoedit.TickRange{First: 0, Count: 48_000},
		ArtifactURI: "file:///host-owned/unprepared.mp4",
	}
	payload, err := json.Marshal(map[string]any{"job": job})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := application.handler.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "unprepared-submit",
		Tool: agentprotocol.ToolRender, Action: "render", ProjectID: "project", Payload: payload,
	})
	if err != nil || rejected.OK || len(rejected.Diagnostics) != 1 || rejected.Diagnostics[0].Code != "RENDER.JOB_NOT_PREPARED" {
		t.Fatalf("unprepared submit=%+v error=%v", rejected, err)
	}
	missing, err := application.handler.HandleAgent(context.Background(), agentprotocol.Request{
		ProtocolVersion: agentprotocol.ProtocolVersion, RequestID: "unprepared-status",
		Tool: agentprotocol.ToolRender, Action: "status", ProjectID: "project",
		Payload: json.RawMessage(`{"job_id":"job-unprepared"}`),
	})
	if err != nil || missing.OK || len(missing.Diagnostics) != 1 || missing.Diagnostics[0].Code != "RENDER.JOB_NOT_FOUND" {
		t.Fatalf("unprepared status=%+v error=%v", missing, err)
	}
}

func testSHA256(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}

func TestServeStopsOnRuntimeWorkerFailure(t *testing.T) {
	actionRouter, err := router.New()
	if err != nil {
		t.Fatal(err)
	}
	runtimeErrors := make(chan error, 1)
	runtimeErrors <- errors.New("worker failed")
	close(runtimeErrors)
	err = serveOnListener(context.Background(), config{
		listen: "127.0.0.1:0", shutdownTimeout: time.Second, writeTimeout: time.Second,
	}, actionRouter, log.New(&strings.Builder{}, "", 0), runtimeErrors, newBlockingListener())
	if err == nil || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("runtime failure = %v", err)
	}
}

func TestServeDrainsActiveHandlerAfterListenerFailure(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	listener := newFaultListener(serverConnection)
	started := make(chan struct{})
	release := make(chan struct{})
	handler := httpapi.HandlerFunc(func(_ context.Context, request agentprotocol.Request) (agentprotocol.Result, error) {
		close(started)
		<-release
		return agentprotocol.Result{
			ProtocolVersion: agentprotocol.ProtocolVersion, OK: true, RequestID: request.RequestID,
			Tool: request.Tool, Action: request.Action, State: "complete",
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []agentprotocol.NextAction{},
		}, nil
	})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveOnListener(context.Background(), config{
			shutdownTimeout: time.Second, writeTimeout: time.Second,
		}, handler, log.New(&strings.Builder{}, "", 0), nil, listener)
	}()
	go func() {
		defer clientConnection.Close()
		body := `{"protocol_version":1,"request_id":"active","tool":"cutly_project","action":"capabilities","payload":{}}`
		_, _ = fmt.Fprintf(clientConnection,
			"POST /v1/agent HTTP/1.1\r\nHost: cutly.invalid\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		_, _ = io.Copy(io.Discard, clientConnection)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	close(listener.fail)
	select {
	case err := <-serveDone:
		t.Fatalf("server returned before active handler drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-serveDone:
		if err == nil || !strings.Contains(err.Error(), "injected listener failure") {
			t.Fatalf("serve error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish after handler release")
	}
}

func TestServeReportsUnsafeShutdownWhenHandlerIgnoresCancellation(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	listener := newFaultListener(serverConnection)
	started := make(chan struct{})
	release := make(chan struct{})
	exited := make(chan struct{})
	handler := httpapi.HandlerFunc(func(_ context.Context, request agentprotocol.Request) (agentprotocol.Result, error) {
		close(started)
		<-release
		close(exited)
		return agentprotocol.Result{
			ProtocolVersion: agentprotocol.ProtocolVersion, OK: true, RequestID: request.RequestID,
			Tool: request.Tool, Action: request.Action, State: "complete",
			Diagnostics: []videoedit.Diagnostic{}, NextActions: []agentprotocol.NextAction{},
		}, nil
	})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveOnListener(context.Background(), config{
			shutdownTimeout: 25 * time.Millisecond, writeTimeout: time.Second,
		}, handler, log.New(&strings.Builder{}, "", 0), nil, listener)
	}()
	go writeAgentRequest(clientConnection)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	close(listener.fail)
	select {
	case err := <-serveDone:
		if !errors.Is(err, ErrHTTPHandlersActive) {
			t.Fatalf("forced shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("forced shutdown did not return")
	}
	close(release)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("released handler did not exit")
	}
}

func writeAgentRequest(connection net.Conn) {
	defer connection.Close()
	body := `{"protocol_version":1,"request_id":"active","tool":"cutly_project","action":"capabilities","payload":{}}`
	_, _ = fmt.Fprintf(connection,
		"POST /v1/agent HTTP/1.1\r\nHost: cutly.invalid\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	_, _ = io.Copy(io.Discard, connection)
}

type blockingListener struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{closed: make(chan struct{})}
}

func (listener *blockingListener) Accept() (net.Conn, error) {
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *blockingListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*blockingListener) Addr() net.Addr { return staticAddress("127.0.0.1:0") }

type staticAddress string

func (address staticAddress) Network() string { return "tcp" }
func (address staticAddress) String() string  { return string(address) }

type faultListener struct {
	connection net.Conn
	fail       chan struct{}
	closed     chan struct{}
	mu         sync.Mutex
	accepted   bool
	once       sync.Once
}

func newFaultListener(connection net.Conn) *faultListener {
	return &faultListener{connection: connection, fail: make(chan struct{}), closed: make(chan struct{})}
}

func (listener *faultListener) Accept() (net.Conn, error) {
	listener.mu.Lock()
	if !listener.accepted {
		listener.accepted = true
		connection := listener.connection
		listener.mu.Unlock()
		return connection, nil
	}
	listener.mu.Unlock()
	select {
	case <-listener.fail:
		return nil, errors.New("injected listener failure")
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *faultListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*faultListener) Addr() net.Addr { return staticAddress("127.0.0.1:0") }
