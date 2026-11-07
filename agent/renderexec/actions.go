package renderexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	CodePayloadInvalid       = "RENDER.PAYLOAD_INVALID"
	CodeBackendUnavailable   = "RENDER.BACKEND_UNAVAILABLE"
	CodeBackendMismatch      = "RENDER.BACKEND_MISMATCH"
	CodePreflightRejected    = "RENDER.PREFLIGHT_REJECTED"
	CodeFrameUnsupported     = "RENDER.FRAME_UNSUPPORTED"
	CodeFrameFailed          = "RENDER.FRAME_FAILED"
	CodeFrameArtifactInvalid = "RENDER.FRAME_ARTIFACT_INVALID"

	maxDiagnosticMessageBytes = 32 << 10
)

var ErrBackendUnavailable = errors.New("renderexec: backend is unavailable")

// Resolver selects a host-owned backend for one already-frozen manifest. It
// must not use Agent payload fields to construct executable paths or runtime
// identities. Service verifies the returned backend against the manifest.
type Resolver interface {
	ResolveBackend(context.Context, string, renderapi.ExecutionManifest) (renderapi.RenderBackend, error)
}

type ResolverFunc func(context.Context, string, renderapi.ExecutionManifest) (renderapi.RenderBackend, error)

func (function ResolverFunc) ResolveBackend(ctx context.Context, projectID string, manifest renderapi.ExecutionManifest) (renderapi.RenderBackend, error) {
	return function(ctx, projectID, manifest)
}

// Recorder durably registers an exact preflight environment for later queued
// execution. Implementations must be idempotent for the same plan/manifest
// fingerprints and fail closed on collisions or uncertain durability.
type Recorder interface {
	RecordPrepared(context.Context, string, videoedit.RenderPlan, renderapi.ExecutionManifest) error
}

type RecorderFunc func(context.Context, string, videoedit.RenderPlan, renderapi.ExecutionManifest) error

func (function RecorderFunc) RecordPrepared(ctx context.Context, projectID string, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) error {
	return function(ctx, projectID, plan, manifest)
}

type Service struct {
	resolver Resolver
	recorder Recorder
}

func New(resolver Resolver) (*Service, error) {
	return newService(resolver, nil)
}

func NewWithRecorder(resolver Resolver, recorder Recorder) (*Service, error) {
	if recorder == nil {
		return nil, errors.New("renderexec: prepared recorder is required")
	}
	return newService(resolver, recorder)
}

func newService(resolver Resolver, recorder Recorder) (*Service, error) {
	if resolver == nil {
		return nil, errors.New("renderexec: backend resolver is required")
	}
	return &Service{resolver: resolver, recorder: recorder}, nil
}

func (service *Service) Routes() []router.Route {
	if service == nil {
		return nil
	}
	return []router.Route{
		{Tool: agentprotocol.ToolRender, Action: "preflight", Handler: router.ActionFunc(service.preflight)},
		{Tool: agentprotocol.ToolRender, Action: "frame", Handler: router.ActionFunc(service.frame)},
	}
}

type preflightPayload struct {
	Plan              json.RawMessage `json:"plan"`
	ExecutionManifest json.RawMessage `json:"execution_manifest"`
}

type framePayload struct {
	Plan              json.RawMessage `json:"plan"`
	ExecutionManifest json.RawMessage `json:"execution_manifest"`
	Frame             json.RawMessage `json:"frame"`
}

type preflightReceipt struct {
	Profile                      string `json:"profile"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	BackendID                    string `json:"backend_id"`
	BackendBuildFingerprint      string `json:"backend_build_fingerprint"`
}

type frameReceipt struct {
	Profile  string                       `json:"profile"`
	Request  renderapi.ExactFrameRequest  `json:"request"`
	Artifact renderapi.ExactFrameArtifact `json:"artifact"`
}

func (service *Service) preflight(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload preflightPayload
	if err := decodePayload(request.Payload, &payload); err != nil || len(payload.Plan) == 0 || len(payload.ExecutionManifest) == 0 {
		return reject(CodePayloadInvalid, "/payload", payloadError("preflight requires plan and execution_manifest", err)), nil
	}
	prepared, outcome, err := service.prepare(ctx, request.ProjectID, payload.Plan, payload.ExecutionManifest)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	diagnostics, err := safePreflight(ctx, prepared.backend, prepared.plan, prepared.manifest)
	if err != nil {
		return reject(CodePreflightRejected, "/backend", boundedMessage(err)), nil
	}
	diagnostics = nonNilDiagnostics(diagnostics)
	ready := !videoedit.HasErrorDiagnostics(diagnostics)
	if ready && service.recorder != nil {
		if err := ctx.Err(); err != nil {
			return router.Outcome{}, err
		}
		if err := safeRecord(ctx, service.recorder, request.ProjectID, prepared.plan, prepared.manifest); err != nil {
			return router.Outcome{}, err
		}
	}
	receipt, err := encodeReceipt(preflightReceipt{
		Profile: "cutly.agent-render-preflight.v1", PlanFingerprint: prepared.plan.Fingerprint,
		ExecutionManifestFingerprint: prepared.manifest.Fingerprint,
		BackendID:                    prepared.manifest.Backend.BackendID,
		BackendBuildFingerprint:      prepared.manifest.Backend.BuildFingerprint,
	})
	if err != nil {
		return router.Outcome{}, err
	}
	state := "ready"
	next := []agentprotocol.NextAction{{Tool: agentprotocol.ToolRender, Action: "frame", ProjectID: request.ProjectID}}
	if !ready {
		state = "rejected"
		next = []agentprotocol.NextAction{{Tool: agentprotocol.ToolProject, Action: "status", ProjectID: request.ProjectID}}
	}
	return router.Outcome{
		OK: ready, State: state, RenderRequired: ready, Diagnostics: diagnostics,
		Receipt: receipt, NextActions: next,
	}, nil
}

func (service *Service) frame(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload framePayload
	if err := decodePayload(request.Payload, &payload); err != nil || len(payload.Plan) == 0 || len(payload.ExecutionManifest) == 0 || len(payload.Frame) == 0 {
		return reject(CodePayloadInvalid, "/payload", payloadError("frame requires plan, execution_manifest and frame", err)), nil
	}
	prepared, outcome, err := service.prepare(ctx, request.ProjectID, payload.Plan, payload.ExecutionManifest)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	frame, err := renderapi.DecodeExactFrameRequest(payload.Frame)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/frame", boundedMessage(err)), nil
	}
	frameRequest := renderapi.ExactFrameRenderRequest{Frame: frame, Plan: prepared.plan, Manifest: prepared.manifest}
	if err := renderapi.ValidateExactFrameRenderRequest(frameRequest); err != nil {
		return reject(CodePayloadInvalid, "/payload/frame", boundedMessage(err)), nil
	}
	diagnostics, err := safePreflight(ctx, prepared.backend, prepared.plan, prepared.manifest)
	if err != nil {
		return reject(CodePreflightRejected, "/backend", boundedMessage(err)), nil
	}
	diagnostics = nonNilDiagnostics(diagnostics)
	if videoedit.HasErrorDiagnostics(diagnostics) {
		return router.Outcome{
			OK: false, State: "rejected", RenderRequired: false, Diagnostics: diagnostics,
			NextActions: []agentprotocol.NextAction{{Tool: agentprotocol.ToolProject, Action: "status", ProjectID: request.ProjectID}},
		}, nil
	}
	backend, supported := prepared.backend.(renderapi.ExactFrameBackend)
	if !supported {
		return reject(CodeFrameUnsupported, "/backend", "selected backend does not implement exact-frame rendering"), nil
	}
	artifact, err := safeRenderFrame(ctx, backend, frameRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return router.Outcome{}, ctxErr
		}
		return reject(CodeFrameFailed, "/backend", boundedMessage(err)), nil
	}
	if err := renderapi.ValidateExactFrameArtifact(artifact, frameRequest); err != nil {
		return reject(CodeFrameArtifactInvalid, "/artifact", boundedMessage(err)), nil
	}
	receipt, err := encodeReceipt(frameReceipt{Profile: "cutly.agent-render-frame.v1", Request: frame, Artifact: artifact})
	if err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: "complete", Changed: true, RenderRequired: true,
		Diagnostics: diagnostics, Receipt: receipt,
		NextActions: []agentprotocol.NextAction{{Tool: agentprotocol.ToolRender, Action: "render", ProjectID: request.ProjectID}},
	}, nil
}

type preparedBackend struct {
	plan     videoedit.RenderPlan
	manifest renderapi.ExecutionManifest
	backend  renderapi.RenderBackend
}

func (service *Service) prepare(ctx context.Context, projectID string, planRaw, manifestRaw json.RawMessage) (preparedBackend, *router.Outcome, error) {
	if ctx == nil {
		return preparedBackend{}, nil, errors.New("renderexec: context is required")
	}
	if err := ctx.Err(); err != nil {
		return preparedBackend{}, nil, err
	}
	plan, err := videoedit.DecodeRenderPlan(planRaw)
	if err != nil {
		outcome := reject(CodePayloadInvalid, "/payload/plan", boundedMessage(err))
		return preparedBackend{}, &outcome, nil
	}
	manifest, err := renderapi.DecodeExecutionManifest(manifestRaw)
	if err != nil {
		outcome := reject(CodePayloadInvalid, "/payload/execution_manifest", boundedMessage(err))
		return preparedBackend{}, &outcome, nil
	}
	refrozen, err := renderapi.FreezeExecutionManifest(plan, manifest)
	var suppliedManifest, refrozenManifest []byte
	if err == nil {
		suppliedManifest, err = canonicaljson.Marshal(manifest)
	}
	if err == nil {
		refrozenManifest, err = canonicaljson.Marshal(refrozen)
	}
	if err != nil || !bytes.Equal(suppliedManifest, refrozenManifest) {
		if err == nil {
			err = errors.New("execution manifest is not the exact frozen environment for the supplied plan")
		}
		outcome := reject(CodePayloadInvalid, "/payload/execution_manifest", boundedMessage(err))
		return preparedBackend{}, &outcome, nil
	}
	backend, err := safeResolve(ctx, service.resolver, projectID, manifest)
	if err != nil {
		if errors.Is(err, ErrBackendUnavailable) {
			outcome := reject(CodeBackendUnavailable, "/backend", boundedMessage(err))
			return preparedBackend{}, &outcome, nil
		}
		return preparedBackend{}, nil, err
	}
	if backend == nil {
		outcome := reject(CodeBackendUnavailable, "/backend", "backend resolver returned no backend")
		return preparedBackend{}, &outcome, nil
	}
	capabilities, err := safeCapabilities(ctx, backend)
	if err != nil {
		outcome := reject(CodeBackendUnavailable, "/backend", boundedMessage(err))
		return preparedBackend{}, &outcome, nil
	}
	capabilities = videoedit.CanonicalBackendCapabilities(capabilities)
	wanted := videoedit.CanonicalBackendCapabilities(manifest.Backend)
	if backend.ID() != wanted.BackendID || !reflect.DeepEqual(capabilities, wanted) {
		outcome := reject(CodeBackendMismatch, "/backend", "live backend identity/capabilities do not match the frozen execution manifest")
		return preparedBackend{}, &outcome, nil
	}
	return preparedBackend{plan: plan, manifest: manifest, backend: backend}, nil, nil
}

func decodePayload(raw json.RawMessage, target any) error {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = agentprotocol.MaxRequestBytes
	limits.MaxStringBytes = agentprotocol.MaxRequestBytes
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	if err != nil {
		return err
	}
	if len(canonical) == 0 || canonical[0] != '{' {
		return errors.New("payload must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("payload contains multiple values")
		}
		return err
	}
	return nil
}

func encodeReceipt(value any) (json.RawMessage, error) {
	encoded, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("renderexec: encode receipt: %w", err)
	}
	if len(encoded) > agentprotocol.MaxResultBytes-(1<<20) {
		return nil, errors.New("renderexec: receipt exceeds Agent result budget")
	}
	return encoded, nil
}

func reject(code, path, message string) router.Outcome {
	return router.Outcome{
		OK: false, State: "rejected",
		Diagnostics: []videoedit.Diagnostic{{Code: code, Severity: videoedit.SeverityError, Path: path, Message: boundedMessage(errors.New(message))}},
		NextActions: []agentprotocol.NextAction{},
	}
}

func outcomeOrError(outcome *router.Outcome, err error) (router.Outcome, error) {
	if outcome != nil {
		return *outcome, nil
	}
	return router.Outcome{}, err
}

func payloadError(fallback string, err error) string {
	if err == nil {
		return fallback
	}
	return fallback + ": " + err.Error()
}

func nonNilDiagnostics(values []videoedit.Diagnostic) []videoedit.Diagnostic {
	if values == nil {
		return []videoedit.Diagnostic{}
	}
	result := make([]videoedit.Diagnostic, len(values))
	copy(result, values)
	return result
}

func boundedMessage(err error) string {
	const fallback = "render action failed"
	if err == nil {
		return fallback
	}
	trimmed := strings.TrimSpace(err.Error())
	if trimmed == "" {
		return fallback
	}
	message := strings.ToValidUTF8(trimmed, "\uFFFD")
	if len(message) <= maxDiagnosticMessageBytes {
		return message
	}
	message = message[:maxDiagnosticMessageBytes-len("...")]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return fallback
	}
	return message + "..."
}

func safeResolve(ctx context.Context, resolver Resolver, projectID string, manifest renderapi.ExecutionManifest) (_ renderapi.RenderBackend, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("renderexec: backend resolver panic: %v", recovered)
		}
	}()
	return resolver.ResolveBackend(ctx, projectID, manifest)
}

func safeCapabilities(ctx context.Context, backend renderapi.RenderBackend) (_ videoedit.BackendCapabilities, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("renderexec: backend capabilities panic: %v", recovered)
		}
	}()
	return backend.Capabilities(ctx)
}

func safePreflight(ctx context.Context, backend renderapi.RenderBackend, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) (_ []videoedit.Diagnostic, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("renderexec: backend preflight panic: %v", recovered)
		}
	}()
	return backend.Preflight(ctx, plan, manifest), nil
}

func safeRenderFrame(ctx context.Context, backend renderapi.ExactFrameBackend, request renderapi.ExactFrameRenderRequest) (_ renderapi.ExactFrameArtifact, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("renderexec: backend frame panic: %v", recovered)
		}
	}()
	return backend.RenderFrame(ctx, request, nil)
}

func safeRecord(ctx context.Context, recorder Recorder, projectID string, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) (returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = fmt.Errorf("renderexec: prepared recorder panic: %v", recovered)
		}
	}()
	return recorder.RecordPrepared(ctx, projectID, plan, manifest)
}
