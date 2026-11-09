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
	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

const (
	defaultEndpointV1 = "http://127.0.0.1:9817/v1/agent"
	defaultEndpointV2 = "http://127.0.0.1:9817/v2/agent"
	exitFailure       = 1
	exitUsage         = 2
	exitRejected      = 3
)

type transportFactory func(string, string) (cutly.Transport, error)

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
		_, _ = fmt.Fprintf(stdout, "cutly %s (Document v2 Agent schema %d; legacy Agent protocol v%d)\n", cutly.Version, protocolv2.SchemaVersion, agentprotocol.ProtocolVersion)
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
	documentPath := flags.String("document", "-", "Document JSON file, or - for stdin")
	profile := flags.String("profile", "document-v2", "project profile: document-v2 or legacy-v1")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*projectDir) == "" {
		return exitUsage
	}
	if *profile != "document-v2" && *profile != "legacy-v1" {
		_, _ = fmt.Fprintf(stderr, "cutly: unsupported project profile %q\n", *profile)
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
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: resolve real project directory: %v\n", err)
		return exitFailure
	}
	var receiptProfile, documentHash string
	if *profile == "legacy-v1" {
		document, decodeErr := cutly.DecodeDocumentV1(raw)
		if decodeErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: invalid legacy project document: %v\n", decodeErr)
			return exitFailure
		}
		project, createErr := cutly.CreateLocalProjectV1(ctx, resolved, document)
		if createErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: initialize project: %v\n", createErr)
			return exitFailure
		}
		snapshot, inspectErr := project.Inspect(ctx)
		if inspectErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: inspect initialized project: %v\n", inspectErr)
			return exitFailure
		}
		receiptProfile, documentHash = "cutly.cli-project-init.v1", snapshot.DocumentHash
	} else {
		document, decodeErr := cutly.DecodeDocument(raw)
		if decodeErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: invalid Document v2 project: %v\n", decodeErr)
			return exitFailure
		}
		project, createErr := cutly.CreateLocalProjectV2(ctx, resolved, document)
		if createErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: initialize project: %v\n", createErr)
			return exitFailure
		}
		snapshot, inspectErr := project.Inspect(ctx)
		if inspectErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: inspect initialized project: %v\n", inspectErr)
			return exitFailure
		}
		receiptProfile, documentHash = "cutly.cli-project-init.document-v2.v2", snapshot.DocumentHash
	}
	receipt, err := canonicaljson.Marshal(initReceipt{
		Profile: receiptProfile, ProjectDir: resolved, DocumentHash: documentHash,
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
	profile := flags.String("profile", "document-v2", "Agent profile: document-v2 or legacy-v1")
	endpoint := flags.String("endpoint", "", "Cutly Agent HTTP endpoint")
	requestPath := flags.String("request", "-", "canonical Agent request file, or - for stdin")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *timeout <= 0 || (*profile != "document-v2" && *profile != "legacy-v1") {
		return exitUsage
	}
	if *endpoint == "" {
		*endpoint = endpointDefault(*profile)
	}
	maximum := agentprotocol.MaxRequestBytes
	if *profile == "document-v2" {
		maximum = protocolv2.MaxRequestBytes
	}
	raw, err := readInput(*requestPath, stdin, maximum)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: read request: %v\n", err)
		return exitFailure
	}
	if *profile == "document-v2" {
		request, decodeErr := protocolv2.DecodeRequest(ctx, raw)
		if decodeErr != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: invalid Document v2 request: %v\n", decodeErr)
			return exitFailure
		}
		return executeV2(ctx, request, *endpoint, *timeout, stdout, stderr, factory)
	}
	request, err := agentprotocol.DecodeRequest(raw)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: invalid legacy request: %v\n", err)
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
	profile := flags.String("profile", "document-v2", "Agent profile: document-v2 or legacy-v1")
	endpoint := flags.String("endpoint", "", "Cutly Agent HTTP endpoint")
	projectID := flags.String("project", "", "project identity")
	requestID := flags.String("request-id", "", "stable request identity; generated when omitted")
	payloadPath := flags.String("payload", "", "payload JSON file, or - for stdin; default {}")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *timeout <= 0 || (*profile != "document-v2" && *profile != "legacy-v1") {
		return exitUsage
	}
	if *endpoint == "" {
		*endpoint = endpointDefault(*profile)
	}
	payload := json.RawMessage(`{}`)
	if *payloadPath != "" {
		maximum := agentprotocol.MaxRequestBytes
		if *profile == "document-v2" {
			maximum = protocolv2.MaxRequestBytes
		}
		raw, err := readInput(*payloadPath, stdin, maximum)
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
	if *profile == "document-v2" {
		v2Action := action
		if tool == agentprotocol.ToolRender && action == "render" {
			v2Action = "submit"
		}
		v2ProjectID := *projectID
		if tool == agentprotocol.ToolProject && action == "capabilities" && v2ProjectID == "" {
			v2ProjectID = "capabilities"
		}
		request, err := cutly.NewAgentRequestV2(ctx, *requestID, cutly.AgentToolV2(tool), v2Action, v2ProjectID, payload)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cutly: invalid Document v2 action request: %v\n", err)
			return exitFailure
		}
		return executeV2(ctx, request, *endpoint, *timeout, stdout, stderr, factory)
	}
	legacyAction := action
	if tool == agentprotocol.ToolRender && action == "submit" {
		legacyAction = "render"
	}
	request, err := cutly.NewRequest(*requestID, tool, legacyAction, *projectID, payload)
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
	transport, err := factory(endpoint, "legacy-v1")
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

func executeV2(ctx context.Context, request protocolv2.Request, endpoint string, timeout time.Duration, stdout, stderr io.Writer, factory transportFactory) int {
	transport, err := factory(endpoint, "document-v2")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cutly: configure transport: %v\n", err)
		return exitFailure
	}
	client, err := cutly.NewAgentClientV2(transport)
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
	encoded, err := cutly.CanonicalAgentResultV2(requestCtx, result)
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

func defaultTransport(endpoint, profile string) (cutly.Transport, error) {
	if profile == "document-v2" {
		return cutly.NewAgentHTTPTransportV2(endpoint, nil)
	}
	return cutly.NewHTTPTransport(endpoint, nil)
}

func endpointDefault(profile string) string {
	if configured := strings.TrimSpace(os.Getenv("CUTLY_ENDPOINT")); configured != "" {
		return configured
	}
	if profile == "document-v2" {
		return defaultEndpointV2
	}
	return defaultEndpointV1
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
  cutly init --project-dir DIR [--document FILE|-] [--profile document-v2|legacy-v1]
  cutly canonicalize [--input FILE|-]
  cutly call --request FILE|- [--profile document-v2|legacy-v1] [--endpoint URL]
  cutly doctor [--profile document-v2|legacy-v1] [--endpoint URL] [--request-id ID]
  cutly project ACTION --project ID [--payload FILE|-] [--profile document-v2|legacy-v1]
  cutly edit ACTION --project ID [--payload FILE|-] [--profile document-v2|legacy-v1]
  cutly render ACTION --project ID [--payload FILE|-] [--profile document-v2|legacy-v1]

Project actions: inspect, status, history, capabilities
Edit actions:    plan, apply, discard, revert
Render actions:  plan, prepare, preflight, frame, submit, status, history, cancel

Exit codes: 0 success, 1 transport/input failure, 2 usage, 3 business rejection.
`)
}
