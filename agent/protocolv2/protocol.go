// Package protocolv2 defines the independent Document v2 Agent envelope. It is
// intentionally not wire-compatible with agent/protocol v1.
package protocolv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/transactionv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
)

const (
	SchemaVersion   = 2
	RequestProfile  = "cutly.agent-request.document-v2.v2"
	ResultProfile   = "cutly.agent-result.document-v2.v2"
	MediaType       = "application/cutly-agent-document-v2+json"
	MaxRequestBytes = 160 << 20
	MaxResultBytes  = 192 << 20
)

var (
	ErrRequestInvalid = errors.New("agent protocolv2: request is invalid")
	ErrResultInvalid  = errors.New("agent protocolv2: result is invalid")
)

type Tool string

const (
	ToolProject Tool = "cutly_project"
	ToolEdit    Tool = "cutly_edit"
	ToolRender  Tool = "cutly_render"
)

type Request struct {
	SchemaVersion  int             `json:"schema_version"`
	Profile        string          `json:"profile"`
	RequestID      string          `json:"request_id"`
	Tool           Tool            `json:"tool"`
	Action         string          `json:"action"`
	ProjectID      string          `json:"project_id"`
	PayloadProfile string          `json:"payload_profile"`
	Payload        json.RawMessage `json:"payload"`
}

type Result struct {
	SchemaVersion int                     `json:"schema_version"`
	Profile       string                  `json:"profile"`
	OK            bool                    `json:"ok"`
	RequestID     string                  `json:"request_id"`
	Tool          Tool                    `json:"tool"`
	Action        string                  `json:"action"`
	ProjectID     string                  `json:"project_id"`
	State         string                  `json:"state"`
	Diagnostics   []documentv2.Diagnostic `json:"diagnostics"`
	Receipt       json.RawMessage         `json:"receipt"`
}

var actions = map[Tool]map[string]bool{
	ToolProject: {"inspect": true, "status": true, "history": true, "capabilities": true},
	ToolEdit:    {"plan": true, "apply": true, "discard": true, "revert": true},
	ToolRender:  {"plan": true, "prepare": true, "preflight": true, "frame": true, "submit": true, "status": true, "history": true, "cancel": true},
}

func PayloadProfile(tool Tool, action string) string {
	if !actions[tool][action] {
		return ""
	}
	return "cutly.agent-payload." + strings.TrimPrefix(string(tool), "cutly_") + "." + action + ".document-v2.v2"
}

func DecodeRequest(ctx context.Context, raw []byte) (Request, error) {
	if ctx == nil || len(raw) == 0 || len(raw) > MaxRequestBytes {
		return Request{}, fmt.Errorf("%w: input/context", ErrRequestInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	canonical, err := canonicalRaw(raw, MaxRequestBytes)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Request{}, fmt.Errorf("%w: exact canonical JSON required: %v", ErrRequestInvalid, err)
	}
	var request Request
	if err = wirejson.Decode(raw, &request); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrRequestInvalid, err)
	}
	if err = validateRequest(ctx, request); err != nil {
		return Request{}, err
	}
	request.Payload = append(json.RawMessage(nil), request.Payload...)
	return request, nil
}

func CanonicalRequest(ctx context.Context, request Request) ([]byte, error) {
	if err := validateRequest(ctx, request); err != nil {
		return nil, err
	}
	raw, err := videoedit.CanonicalJSON(request)
	if err != nil || len(raw) > MaxRequestBytes {
		return nil, fmt.Errorf("%w: encode/limit: %v", ErrRequestInvalid, err)
	}
	return raw, nil
}

func DecodeResult(ctx context.Context, raw []byte) (Result, error) {
	if ctx == nil || len(raw) == 0 || len(raw) > MaxResultBytes {
		return Result{}, fmt.Errorf("%w: input/context", ErrResultInvalid)
	}
	canonical, err := canonicalRaw(raw, MaxResultBytes)
	if err != nil || !bytes.Equal(raw, canonical) {
		return Result{}, fmt.Errorf("%w: exact canonical JSON required: %v", ErrResultInvalid, err)
	}
	var result Result
	if err = wirejson.Decode(raw, &result); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrResultInvalid, err)
	}
	if err = validateResult(ctx, result); err != nil {
		return Result{}, err
	}
	ownedDiagnostics := make([]documentv2.Diagnostic, len(result.Diagnostics))
	copy(ownedDiagnostics, result.Diagnostics)
	result.Diagnostics = ownedDiagnostics
	result.Receipt = append(json.RawMessage(nil), result.Receipt...)
	return result, nil
}

func CanonicalResult(ctx context.Context, result Result) ([]byte, error) {
	if err := validateResult(ctx, result); err != nil {
		return nil, err
	}
	raw, err := videoedit.CanonicalJSON(result)
	if err != nil || len(raw) > MaxResultBytes {
		return nil, fmt.Errorf("%w: encode/limit: %v", ErrResultInvalid, err)
	}
	return raw, nil
}

func validateRequest(ctx context.Context, request Request) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrRequestInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.SchemaVersion != SchemaVersion || request.Profile != RequestProfile || !validIdentity(request.RequestID) || !validIdentity(request.ProjectID) || !actions[request.Tool][request.Action] {
		return fmt.Errorf("%w: header", ErrRequestInvalid)
	}
	if request.PayloadProfile != PayloadProfile(request.Tool, request.Action) {
		return fmt.Errorf("%w: payload profile", ErrRequestInvalid)
	}
	canonical, err := canonicalRaw(request.Payload, MaxRequestBytes)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return fmt.Errorf("%w: payload object: %v", ErrRequestInvalid, err)
	}
	switch {
	case request.Tool == ToolProject && (request.Action == "inspect" || request.Action == "status" || request.Action == "capabilities"):
		if err = wirejson.Decode(request.Payload, &struct{}{}); err != nil {
			return fmt.Errorf("%w: empty project payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolProject && request.Action == "history":
		var payload struct {
			AfterSequence uint64 `json:"after_sequence,omitempty"`
			Limit         uint64 `json:"limit,omitempty"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil {
			return fmt.Errorf("%w: project history payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolEdit && request.Action == "plan":
		var payload struct {
			Transaction json.RawMessage `json:"transaction"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil {
			return fmt.Errorf("%w: edit plan payload: %v", ErrRequestInvalid, err)
		}
		if _, err = transactionv2.Decode(payload.Transaction); err != nil {
			return fmt.Errorf("%w: transaction: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolEdit && (request.Action == "apply" || request.Action == "discard"):
		var payload struct {
			TransactionID string `json:"transaction_id"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || !validIdentity(payload.TransactionID) {
			return fmt.Errorf("%w: edit transaction payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolEdit && request.Action == "revert":
		var payload struct {
			TargetTransactionID string `json:"target_transaction_id"`
			TransactionID       string `json:"transaction_id"`
			IdempotencyKey      string `json:"idempotency_key"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || !validIdentity(payload.TargetTransactionID) || !validIdentity(payload.TransactionID) || !validIdentity(payload.IdempotencyKey) {
			return fmt.Errorf("%w: edit revert payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "plan":
		var payload struct {
			Range  documentv2.TimeRange   `json:"range"`
			Target videoedit.RenderTarget `json:"target"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil {
			return fmt.Errorf("%w: render plan payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "prepare":
		var payload struct {
			Environment json.RawMessage `json:"environment"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil {
			return fmt.Errorf("%w: render prepare payload: %v", ErrRequestInvalid, err)
		}
		if _, err = prepared.DecodeEnvironmentV2(ctx, payload.Environment); err != nil {
			return fmt.Errorf("%w: prepared environment: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "preflight":
		var payload struct {
			PlanFingerprint              string `json:"plan_fingerprint"`
			ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || !cachekey.CanonicalSHA256(payload.PlanFingerprint) || !cachekey.CanonicalSHA256(payload.ExecutionManifestFingerprint) {
			return fmt.Errorf("%w: render preflight payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "frame":
		var payload struct {
			Profile                      string `json:"profile"`
			PlanFingerprint              string `json:"plan_fingerprint"`
			ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
			CompositionFrameIndex        int64  `json:"composition_frame_index"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || payload.Profile != "cutly.preview-frame.document-v2.v2" || !cachekey.CanonicalSHA256(payload.PlanFingerprint) || !cachekey.CanonicalSHA256(payload.ExecutionManifestFingerprint) {
			return fmt.Errorf("%w: render frame payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "submit":
		var payload struct {
			Job json.RawMessage `json:"job"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil {
			return fmt.Errorf("%w: render submit payload: %v", ErrRequestInvalid, err)
		}
		if _, err = renderapi.DecodeRenderJob(payload.Job); err != nil {
			return fmt.Errorf("%w: render job: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && (request.Action == "status" || request.Action == "cancel"):
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || !validIdentity(payload.JobID) {
			return fmt.Errorf("%w: render job payload: %v", ErrRequestInvalid, err)
		}
	case request.Tool == ToolRender && request.Action == "history":
		var payload struct {
			JobID         string `json:"job_id"`
			AfterSequence uint64 `json:"after_sequence,omitempty"`
			Limit         uint64 `json:"limit,omitempty"`
		}
		if err = wirejson.Decode(request.Payload, &payload); err != nil || !validIdentity(payload.JobID) {
			return fmt.Errorf("%w: render history payload: %v", ErrRequestInvalid, err)
		}
	}
	return nil
}

func validateResult(ctx context.Context, result Result) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrResultInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if result.SchemaVersion != SchemaVersion || result.Profile != ResultProfile || !validIdentity(result.RequestID) || !validIdentity(result.ProjectID) || !validIdentity(result.State) || !actions[result.Tool][result.Action] || result.Diagnostics == nil {
		return fmt.Errorf("%w: header", ErrResultInvalid)
	}
	hasError := documentv2.HasErrorDiagnostics(result.Diagnostics)
	if result.OK == hasError {
		return fmt.Errorf("%w: ok/diagnostics contradiction", ErrResultInvalid)
	}
	canonical, err := canonicalRaw(result.Receipt, MaxResultBytes)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return fmt.Errorf("%w: receipt object: %v", ErrResultInvalid, err)
	}
	return nil
}

func canonicalRaw(raw []byte, maximum int) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return nil, errors.New("encoded limit")
	}
	if err := wirejson.Validate(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return videoedit.CanonicalJSON(value)
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && strings.TrimSpace(value) == value
}
