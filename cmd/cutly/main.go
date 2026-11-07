package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

const (
	defaultEndpoint = "http://127.0.0.1:9817/v1/agent"
	exitFailure     = 1
	exitUsage       = 2
	exitRejected    = 3
)

type transportFactory func(string) (cutly.Transport, error)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, defaultTransport))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, factory transportFactory) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return 0
	case "version":
		_, _ = fmt.Fprintf(stdout, "cutly %s (agent protocol v%d)\n", cutly.Version, agentprotocol.ProtocolVersion)
		return 0
	case "canonicalize":
		return runCanonicalize(args[1:], stdin, stdout, stderr)
	case "init":
		return runInit(ctx, args[1:], stdin, stdout, stderr)
	case "call":
		return runCall(ctx, args[1:], stdin, stdout, stderr, factory)
	case "doctor":
		return runAction(ctx, agentprotocol.ToolProject, "capabilities", args[1:], stdin, stdout, stderr, factory)
	case "project", "edit", "render":
		if len(args) < 2 {
			_, _ = fmt.Fprintf(stderr, "cutly: %s action is required\n", args[0])
			return exitUsage
		}
		tool := map[string]agentprotocol.Tool{
			"project": agentprotocol.ToolProject,
			"edit":    agentprotocol.ToolEdit,
			"render":  agentprotocol.ToolRender,
		}[args[0]]
		return runAction(ctx, tool, args[1], args[2:], stdin, stdout, stderr, factory)
	default:
		_, _ = fmt.Fprintf(stderr, "cutly: unknown command %q\n", args[0])
		writeUsage(stderr)
		return exitUsage
	}
}

type initReceipt struct {
	Profile      string `json:"profile"`
	ProjectDir   string `json:"project_dir"`
	DocumentHash string `json:"document_hash"`
}

func runInit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectDir := flags.String("project-dir", "", "local Cutly project directory")
	documentPath := flags.String("document", "-", "Document v1 JSON file, or - for stdin")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*projectDir) == "" {
		return exitUsage
	}
	if ctx == nil {
		_, _ = io.WriteString(stderr, "cutly: initialize project: context is required\n")
		return exitFailure
	}
	if err := ctx.Err(); err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: initialize project: %v\n", err)
		return exitFailure
	}
	raw, err := readInput(*documentPath, stdin, cutly.MaxDocumentBytes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: read project document: %v\n", err)
		return exitFailure
	}
	document, err := cutly.DecodeDocumentV1(raw)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: invalid project document: %v\n", err)
		return exitFailure
	}
	absolute, err := filepath.Abs(*projectDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: resolve project directory: %v\n", err)
		return exitFailure
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: create project directory: %v\n", err)
		return exitFailure
	}
	project, err := cutly.CreateLocalProjectV1(ctx, absolute, document)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: initialize project: %v\n", err)
		return exitFailure
	}
	snapshot, err := project.Inspect(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: inspect initialized project: %v\n", err)
		return exitFailure
	}
	receipt, err := canonicaljson.Marshal(initReceipt{
		Profile: "cutly.cli-project-init.v1", ProjectDir: absolute, DocumentHash: snapshot.DocumentHash,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: encode init receipt: %v\n", err)
		return exitFailure
	}
	if _, err := stdout.Write(append(receipt, '\n')); err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: write init receipt: %v\n", err)
		return exitFailure
	}
	return 0
}

func runCanonicalize(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("canonicalize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "-", "JSON file, or - for stdin")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return exitUsage
	}
	raw, err := readInput(*input, stdin, canonicaljson.DefaultLimits.MaxInputBytes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: read JSON: %v\n", err)
		return exitFailure
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: canonicalize: %v\n", err)
		return exitFailure
	}
	if _, err := stdout.Write(append(canonical, '\n')); err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: write result: %v\n", err)
		return exitFailure
	}
	return 0
}

func runCall(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, factory transportFactory) int {
	flags := flag.NewFlagSet("call", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", endpointDefault(), "Cutly Agent HTTP endpoint")
	requestPath := flags.String("request", "-", "canonical Agent request file, or - for stdin")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *timeout <= 0 {
		return exitUsage
	}
	raw, err := readInput(*requestPath, stdin, agentprotocol.MaxRequestBytes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: read request: %v\n", err)
		return exitFailure
	}
	request, err := agentprotocol.DecodeRequest(raw)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: invalid request: %v\n", err)
		return exitFailure
	}
	return execute(ctx, request, *endpoint, *timeout, stdout, stderr, factory)
}

func runAction(
	ctx context.Context,
	tool agentprotocol.Tool,
	action string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	factory transportFactory,
) int {
	flags := flag.NewFlagSet(string(tool)+" "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", endpointDefault(), "Cutly Agent HTTP endpoint")
	projectID := flags.String("project", "", "project identity")
	requestID := flags.String("request-id", "", "stable request identity; generated when omitted")
	payloadPath := flags.String("payload", "", "payload JSON file, or - for stdin; default {}")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *timeout <= 0 {
		return exitUsage
	}
	payload := json.RawMessage(`{}`)
	if *payloadPath != "" {
		raw, err := readInput(*payloadPath, stdin, agentprotocol.MaxRequestBytes)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: read payload: %v\n", err)
			return exitFailure
		}
		payload = raw
	}
	if *requestID == "" {
		generated, err := newRequestID()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: create request identity: %v\n", err)
			return exitFailure
		}
		*requestID = generated
	}
	request, err := cutly.NewRequest(*requestID, tool, action, *projectID, payload)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: invalid action request: %v\n", err)
		return exitFailure
	}
	return execute(ctx, request, *endpoint, *timeout, stdout, stderr, factory)
}

func execute(
	ctx context.Context,
	request agentprotocol.Request,
	endpoint string,
	timeout time.Duration,
	stdout, stderr io.Writer,
	factory transportFactory,
) int {
	transport, err := factory(endpoint)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: configure transport: %v\n", err)
		return exitFailure
	}
	client, err := cutly.NewClient(transport)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: configure client: %v\n", err)
		return exitFailure
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := client.Call(requestCtx, request)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: call failed: %v\n", err)
		return exitFailure
	}
	encoded, err := agentprotocol.CanonicalResult(result)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: encode result: %v\n", err)
		return exitFailure
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: write result: %v\n", err)
		return exitFailure
	}
	if !result.OK {
		return exitRejected
	}
	return 0
}

func defaultTransport(endpoint string) (cutly.Transport, error) {
	return cutly.NewHTTPTransport(endpoint, nil)
}

func endpointDefault() string {
	if configured := strings.TrimSpace(os.Getenv("CUTLY_ENDPOINT")); configured != "" {
		return configured
	}
	return defaultEndpoint
}

func readInput(path string, stdin io.Reader, maximum int) ([]byte, error) {
	var reader io.Reader = stdin
	var file *os.File
	if path != "-" {
		opened, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		file = opened
		reader = opened
		defer file.Close()
	}
	raw, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, fmt.Errorf("input exceeds %d bytes", maximum)
	}
	return raw, nil
}

func newRequestID() (string, error) {
	var entropy [16]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", err
	}
	return "cli-" + hex.EncodeToString(entropy[:]), nil
}

func writeUsage(writer io.Writer) {
	_, _ = io.WriteString(writer, `Usage:
  cutly version
  cutly init --project-dir DIR [--document FILE|-]
  cutly canonicalize [--input FILE|-]
  cutly call --request FILE|- [--endpoint URL]
  cutly doctor [--endpoint URL] [--request-id ID]
  cutly project ACTION --project ID [--payload FILE|-]
  cutly edit ACTION --project ID [--payload FILE|-]
  cutly render ACTION --project ID [--payload FILE|-]

Project actions: inspect, status, history, capabilities
Edit actions:    plan, apply, discard, revert
Render actions:  plan, prepare, preflight, frame, render, status, cancel

Exit codes: 0 success, 1 transport/input failure, 2 usage, 3 business rejection.
`)
}
