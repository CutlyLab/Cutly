package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CutlyLab/Cutly/agent/httpapi"
	"github.com/CutlyLab/Cutly/agent/router"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

type config struct {
	listen          string
	shutdownTimeout time.Duration
	writeTimeout    time.Duration
	projectID       string
	projectDir      string
	render          renderConfig
	version         bool
}

const maxRenderInputRoots = 64

type renderConfig struct {
	enabled                             bool
	stateDir                            string
	outputDir                           string
	inputRoots                          stringList
	ffmpegExecutable                    string
	ffprobeExecutable                   string
	sdkBuildDigest                      string
	compilerBuild                       string
	evaluatorBuild                      string
	ffmpegRuntimeDependencyFingerprint  string
	ffprobeRuntimeDependencyFingerprint string
	stageMemoryBytes                    uint64
	stageDiskBytes                      uint64
	maxConcurrentJobs                   int
	workerPollInterval                  time.Duration
}

type stringList []string

var ErrHTTPHandlersActive = errors.New("cutlyd: HTTP handlers remain active after forced shutdown")

func (values *stringList) String() string {
	if values == nil {
		return ""
	}
	return strings.Join(*values, ",")
}

func (values *stringList) Set(value string) error {
	if values == nil || value == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func main() {
	configuration, err := parseConfig(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cutlyd: %v\n", err)
		os.Exit(2)
	}
	if configuration.version {
		_, _ = fmt.Fprintf(os.Stdout, "cutlyd %s\n", cutly.Version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "cutlyd: ", log.LstdFlags|log.LUTC)
	if err := run(ctx, configuration, logger); err != nil {
		logger.Printf("%v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *log.Logger) error {
	if ctx == nil || logger == nil {
		return errors.New("cutlyd: run context and logger are required")
	}
	application, err := buildApplication(ctx, configuration)
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	serveErr := serveWithRuntimeErrors(ctx, configuration, application.handler, logger, application.Errors())
	if errors.Is(serveErr, ErrHTTPHandlersActive) {
		// Closing backend directory handles or the scheduler journal while an
		// uncooperative handler still uses them is unsafe. main exits immediately
		// after this error, so let the OS reclaim process-owned resources instead
		// of racing application.Close against that handler.
		return serveErr
	}
	return errors.Join(serveErr, application.Close())
}

func parseConfig(args []string) (config, error) {
	flags := flag.NewFlagSet("cutlyd", flag.ContinueOnError)
	flags.SetOutput(new(strings.Builder))
	configuration := config{}
	flags.StringVar(&configuration.listen, "listen", "127.0.0.1:9817", "loopback HTTP listen address")
	flags.DurationVar(&configuration.shutdownTimeout, "shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
	flags.DurationVar(&configuration.writeTimeout, "write-timeout", 10*time.Minute, "maximum HTTP response write duration")
	flags.StringVar(&configuration.projectID, "project-id", "", "project identity advertised by the Agent API")
	flags.StringVar(&configuration.projectDir, "project-dir", "", "existing local Cutly project directory")
	flags.StringVar(&configuration.render.stateDir, "render-state-dir", "", "existing host-owned durable render state directory")
	flags.StringVar(&configuration.render.outputDir, "render-output-dir", "", "existing dedicated host-owned render publication directory")
	flags.Var(&configuration.render.inputRoots, "render-input-root", "existing input root allowed for local media (repeatable)")
	flags.StringVar(&configuration.render.ffmpegExecutable, "ffmpeg", "ffmpeg", "host-owned FFmpeg executable")
	flags.StringVar(&configuration.render.ffprobeExecutable, "ffprobe", "ffprobe", "host-owned FFprobe executable")
	flags.StringVar(&configuration.render.sdkBuildDigest, "sdk-build-digest", "", "sha256 identity of this Cutly SDK build")
	flags.StringVar(&configuration.render.compilerBuild, "compiler-build", "", "sha256 identity accepted for semantic compiler output")
	flags.StringVar(&configuration.render.evaluatorBuild, "evaluator-build", "", "sha256 identity accepted for expression evaluation")
	flags.StringVar(&configuration.render.ffmpegRuntimeDependencyFingerprint, "ffmpeg-runtime-dependency-fingerprint", "", "sha256 identity of the pinned FFmpeg dependency closure")
	flags.StringVar(&configuration.render.ffprobeRuntimeDependencyFingerprint, "ffprobe-runtime-dependency-fingerprint", "", "sha256 identity of the pinned FFprobe dependency closure")
	flags.Uint64Var(&configuration.render.stageMemoryBytes, "render-stage-memory-bytes", 512<<20, "per-render memory admission estimate")
	flags.Uint64Var(&configuration.render.stageDiskBytes, "render-stage-disk-bytes", 4<<30, "per-render disk admission estimate")
	flags.IntVar(&configuration.render.maxConcurrentJobs, "render-max-concurrent-jobs", 1, "maximum in-process render jobs")
	flags.DurationVar(&configuration.render.workerPollInterval, "render-worker-poll-interval", defaultWorkerPollInterval, "durable queue polling fallback interval")
	flags.BoolVar(&configuration.version, "version", false, "print version")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	renderFlags := map[string]struct{}{
		"render-state-dir": {}, "render-output-dir": {}, "render-input-root": {},
		"ffmpeg": {}, "ffprobe": {}, "sdk-build-digest": {}, "compiler-build": {}, "evaluator-build": {},
		"ffmpeg-runtime-dependency-fingerprint": {}, "ffprobe-runtime-dependency-fingerprint": {},
		"render-stage-memory-bytes": {}, "render-stage-disk-bytes": {}, "render-max-concurrent-jobs": {},
		"render-worker-poll-interval": {},
	}
	flags.Visit(func(value *flag.Flag) {
		if _, ok := renderFlags[value.Name]; ok {
			configuration.render.enabled = true
		}
	})
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if configuration.version {
		return configuration, nil
	}
	if configuration.shutdownTimeout <= 0 || configuration.writeTimeout <= 0 {
		return config{}, errors.New("shutdown-timeout and write-timeout must be positive")
	}
	if (configuration.projectID == "") != (configuration.projectDir == "") {
		return config{}, errors.New("project-id and project-dir must be configured together")
	}
	if err := validateLoopbackAddress(configuration.listen); err != nil {
		return config{}, err
	}
	if err := validateRenderConfig(configuration); err != nil {
		return config{}, err
	}
	return configuration, nil
}

func validateRenderConfig(configuration config) error {
	render := configuration.render
	if !render.enabled {
		return nil
	}
	if render.stateDir == "" {
		return errors.New("render-state-dir is required when render configuration is supplied")
	}
	if configuration.projectID == "" || configuration.projectDir == "" {
		return errors.New("rendering requires project-id and project-dir")
	}
	for _, field := range []struct{ name, value string }{
		{"project-dir", configuration.projectDir},
		{"render-state-dir", render.stateDir},
		{"render-output-dir", render.outputDir},
	} {
		if err := validateCleanAbsolutePath(field.value); err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if len(render.inputRoots) == 0 || len(render.inputRoots) > maxRenderInputRoots {
		return fmt.Errorf("render-input-root count must be within [1,%d]", maxRenderInputRoots)
	}
	for index, root := range render.inputRoots {
		if err := validateCleanAbsolutePath(root); err != nil {
			return fmt.Errorf("render-input-root %d: %w", index, err)
		}
	}
	if render.ffmpegExecutable == "" || render.ffprobeExecutable == "" {
		return errors.New("ffmpeg and ffprobe executables are required")
	}
	for _, field := range []struct{ name, value string }{
		{"sdk-build-digest", render.sdkBuildDigest},
		{"compiler-build", render.compilerBuild},
		{"evaluator-build", render.evaluatorBuild},
		{"ffmpeg-runtime-dependency-fingerprint", render.ffmpegRuntimeDependencyFingerprint},
		{"ffprobe-runtime-dependency-fingerprint", render.ffprobeRuntimeDependencyFingerprint},
	} {
		if !canonicalSHA256(field.value) {
			return fmt.Errorf("%s must be a canonical sha256 digest", field.name)
		}
	}
	if render.stageMemoryBytes == 0 || render.stageDiskBytes == 0 {
		return errors.New("render stage memory and disk estimates must be positive")
	}
	if render.maxConcurrentJobs < 1 || render.maxConcurrentJobs > 1024 {
		return errors.New("render-max-concurrent-jobs must be within [1,1024]")
	}
	if render.workerPollInterval < time.Millisecond || render.workerPollInterval > maximumWorkerPollInterval {
		return fmt.Errorf("render-worker-poll-interval must be within [1ms,%s]", maximumWorkerPollInterval)
	}
	return nil
}

func validateCleanAbsolutePath(value string) error {
	if value == "" || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) || filepath.Clean(value) != value || filepath.Dir(value) == value {
		return errors.New("must be a clean absolute non-volume-root path")
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func buildRouter(configuration config) (*router.Router, error) {
	routes, err := buildProjectRoutes(configuration)
	if err != nil {
		return nil, err
	}
	return router.New(routes...)
}

func serve(ctx context.Context, configuration config, handler httpapi.Handler, logger *log.Logger) error {
	return serveWithRuntimeErrors(ctx, configuration, handler, logger, nil)
}

func serveWithRuntimeErrors(ctx context.Context, configuration config, handler httpapi.Handler, logger *log.Logger, runtimeErrors <-chan error) error {
	if ctx == nil || handler == nil || logger == nil {
		return errors.New("server context, handler and logger are required")
	}
	listener, err := net.Listen("tcp", configuration.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", configuration.listen, err)
	}
	defer listener.Close()
	return serveOnListener(ctx, configuration, handler, logger, runtimeErrors, listener)
}

func serveOnListener(ctx context.Context, configuration config, handler httpapi.Handler, logger *log.Logger, runtimeErrors <-chan error, listener net.Listener) error {
	if ctx == nil || handler == nil || logger == nil || listener == nil {
		return errors.New("server context, handler, logger and listener are required")
	}
	tracked := newTrackedHTTPHandler(newMux(handler))
	server := &http.Server{
		Handler:           tracked,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      configuration.writeTimeout,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          logger,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	logger.Printf("listening on http://%s/v1/agent", listener.Addr())

	for {
		select {
		case err := <-serveErrors:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			// Serve may stop because its listener failed while already accepted
			// handlers are still active. Drain (or force-close) those handlers
			// before run closes render workers and durable state beneath them.
			return errors.Join(fmt.Errorf("HTTP server: %w", err), drainServer(server, tracked, configuration.shutdownTimeout))
		case runtimeErr, open := <-runtimeErrors:
			if !open {
				runtimeErrors = nil
				continue
			}
			if runtimeErr == nil {
				runtimeErr = errors.New("render worker stopped without an error")
			}
			return errors.Join(fmt.Errorf("runtime worker: %w", runtimeErr), shutdownServer(server, tracked, serveErrors, configuration.shutdownTimeout))
		case <-ctx.Done():
			return shutdownServer(server, tracked, serveErrors, configuration.shutdownTimeout)
		}
	}
}

func shutdownServer(server *http.Server, tracked *trackedHTTPHandler, serveErrors <-chan error, timeout time.Duration) error {
	if err := drainServer(server, tracked, timeout); err != nil {
		return err
	}
	if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP server after shutdown: %w", err)
	}
	return nil
}

func drainServer(server *http.Server, tracked *trackedHTTPHandler, timeout time.Duration) error {
	tracked.beginClose()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		_ = server.Close()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), timeout)
		idle := tracked.wait(waitCtx)
		waitCancel()
		if !idle {
			return errors.Join(fmt.Errorf("graceful shutdown: %w", shutdownErr), ErrHTTPHandlersActive)
		}
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	if !tracked.idle() {
		return ErrHTTPHandlersActive
	}
	return nil
}

type trackedHTTPHandler struct {
	delegate http.Handler

	mu      sync.Mutex
	active  int
	closing bool
	idleCh  chan struct{}
}

func newTrackedHTTPHandler(delegate http.Handler) *trackedHTTPHandler {
	return &trackedHTTPHandler{delegate: delegate}
}

func (handler *trackedHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler.mu.Lock()
	if handler.closing {
		handler.mu.Unlock()
		http.Error(writer, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if handler.active == 0 {
		handler.idleCh = make(chan struct{})
	}
	handler.active++
	handler.mu.Unlock()
	defer func() {
		handler.mu.Lock()
		handler.active--
		if handler.active == 0 {
			close(handler.idleCh)
		}
		handler.mu.Unlock()
	}()
	handler.delegate.ServeHTTP(writer, request)
}

func (handler *trackedHTTPHandler) beginClose() {
	handler.mu.Lock()
	handler.closing = true
	handler.mu.Unlock()
}

func (handler *trackedHTTPHandler) idle() bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.active == 0
}

func (handler *trackedHTTPHandler) wait(ctx context.Context) bool {
	handler.mu.Lock()
	if handler.active == 0 {
		handler.mu.Unlock()
		return true
	}
	idle := handler.idleCh
	handler.mu.Unlock()
	select {
	case <-idle:
		return true
	case <-ctx.Done():
		return false
	}
}

func newMux(handler httpapi.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/agent", httpapi.Server{Handler: handler})
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("listen must be host:port: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must be an explicit loopback address; put an authenticated reverse proxy in front for remote access")
	}
	return nil
}
