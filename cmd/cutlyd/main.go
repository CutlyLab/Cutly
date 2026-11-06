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
	"strings"
	"syscall"
	"time"

	"github.com/CutlyLab/Cutly/agent/httpapi"
	"github.com/CutlyLab/Cutly/agent/projectlocal"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/project/local"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

type config struct {
	listen          string
	shutdownTimeout time.Duration
	projectID       string
	projectDir      string
	version         bool
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

	actionRouter, err := buildRouter(configuration)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cutlyd: initialize router: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "cutlyd: ", log.LstdFlags|log.LUTC)
	if err := serve(ctx, configuration, actionRouter, logger); err != nil {
		logger.Printf("%v", err)
		os.Exit(1)
	}
}

func parseConfig(args []string) (config, error) {
	flags := flag.NewFlagSet("cutlyd", flag.ContinueOnError)
	flags.SetOutput(new(strings.Builder))
	configuration := config{}
	flags.StringVar(&configuration.listen, "listen", "127.0.0.1:9817", "loopback HTTP listen address")
	flags.DurationVar(&configuration.shutdownTimeout, "shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
	flags.StringVar(&configuration.projectID, "project-id", "", "project identity advertised by the Agent API")
	flags.StringVar(&configuration.projectDir, "project-dir", "", "existing local Cutly project directory")
	flags.BoolVar(&configuration.version, "version", false, "print version")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if configuration.version {
		return configuration, nil
	}
	if configuration.shutdownTimeout <= 0 {
		return config{}, errors.New("shutdown-timeout must be positive")
	}
	if (configuration.projectID == "") != (configuration.projectDir == "") {
		return config{}, errors.New("project-id and project-dir must be configured together")
	}
	if err := validateLoopbackAddress(configuration.listen); err != nil {
		return config{}, err
	}
	return configuration, nil
}

func buildRouter(configuration config) (*router.Router, error) {
	if configuration.projectDir == "" {
		return router.New()
	}
	project, err := local.New(configuration.projectDir, local.Options{})
	if err != nil {
		return nil, fmt.Errorf("open local project: %w", err)
	}
	registry, err := projectlocal.NewRegistry(map[string]*local.Service{configuration.projectID: project})
	if err != nil {
		return nil, fmt.Errorf("register local project: %w", err)
	}
	projectActions, err := projectlocal.New(registry)
	if err != nil {
		return nil, fmt.Errorf("initialize local project actions: %w", err)
	}
	return router.New(projectActions.Routes()...)
}

func serve(ctx context.Context, configuration config, handler httpapi.Handler, logger *log.Logger) error {
	if ctx == nil || handler == nil || logger == nil {
		return errors.New("server context, handler and logger are required")
	}
	listener, err := net.Listen("tcp", configuration.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", configuration.listen, err)
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           newMux(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          logger,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	logger.Printf("listening on http://%s/v1/agent", listener.Addr())

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), configuration.shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server after shutdown: %w", err)
		}
		return nil
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
