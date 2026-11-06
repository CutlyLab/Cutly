// Package renderplan exposes the Agent action that compiles the current
// project snapshot into a backend-neutral, frozen RenderPlan.
package renderplan

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
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	ReceiptProfile = "cutly.agent-render-plan.v1"

	CodePayloadInvalid  = "RENDER.PAYLOAD_INVALID"
	CodeProjectMismatch = "RENDER.PROJECT_MISMATCH"
	CodePlanRejected    = "RENDER.PLAN_REJECTED"
)

var (
	ErrConfiguration   = errors.New("renderplan: invalid configuration")
	errReceiptTooLarge = errors.New("renderplan: receipt exceeds Agent result budget")
)

// DocumentResolver returns the current document for a fixed registered
// project. The Service clones and validates it before compiling a plan.
type DocumentResolver interface {
	ResolveDocument(context.Context, string) (videoedit.Document, error)
}

type DocumentResolverFunc func(context.Context, string) (videoedit.Document, error)

func (function DocumentResolverFunc) ResolveDocument(ctx context.Context, projectID string) (videoedit.Document, error) {
	return function(ctx, projectID)
}

type Config struct {
	ProjectID string
	Resolver  DocumentResolver
}

type Service struct {
	projectID string
	resolver  DocumentResolver
}

type Receipt struct {
	Profile string               `json:"profile"`
	Plan    videoedit.RenderPlan `json:"plan"`
}

func New(config Config) (*Service, error) {
	if !validProjectID(config.ProjectID) || nilLike(config.Resolver) {
		return nil, fmt.Errorf("%w: a fixed project ID and document resolver are required", ErrConfiguration)
	}
	return &Service{projectID: config.ProjectID, resolver: config.Resolver}, nil
}

func (service *Service) Routes() []router.Route {
	if service == nil {
		return nil
	}
	return []router.Route{{
		Tool: agentprotocol.ToolRender, Action: "plan", Handler: router.ActionFunc(service.plan),
	}}
}

type payload struct {
	Range  json.RawMessage `json:"range"`
	Target json.RawMessage `json:"target"`
}

func (service *Service) plan(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	if ctx == nil {
		return router.Outcome{}, errors.New("renderplan: context is required")
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	if service == nil || nilLike(service.resolver) {
		return reject(CodePlanRejected, "/host", "host render planning is unavailable"), nil
	}
	if request.ProjectID != service.projectID {
		return reject(CodeProjectMismatch, "/project_id", "request project does not match the configured project scope"), nil
	}

	var input payload
	if err := decodePayload(request.Payload, &input); err != nil || len(input.Range) == 0 || len(input.Target) == 0 {
		return reject(CodePayloadInvalid, "/payload", "render plan requires exactly range and target objects"), nil
	}
	var renderRange videoedit.TimeRange
	if err := decodeObject(input.Range, &renderRange); err != nil {
		return reject(CodePayloadInvalid, "/payload/range", "range must match the frozen TimeRange contract"), nil
	}
	var target videoedit.RenderTarget
	if err := decodeObject(input.Target, &target); err != nil {
		return reject(CodePayloadInvalid, "/payload/target", "target must match the frozen RenderTarget contract"), nil
	}

	document, err := safeResolve(ctx, service.resolver, request.ProjectID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return router.Outcome{}, ctxErr
		}
		if isContextError(err) {
			return router.Outcome{}, err
		}
		return reject(CodePlanRejected, "/host", "host could not resolve the current project snapshot"), nil
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	document, err = cloneDocument(document)
	if err != nil {
		return reject(CodePlanRejected, "/host", "host returned an invalid project snapshot"), nil
	}
	plan, diagnostics := videoedit.Resolve(document, renderRange, target)
	diagnostics, diagnosticsOK := ownDiagnostics(diagnostics)
	if !diagnosticsOK {
		return reject(CodePlanRejected, "/host", "host render planning produced an oversized diagnostic result"), nil
	}
	if videoedit.HasErrorDiagnostics(diagnostics) {
		return router.Outcome{
			OK: false, State: "rejected", Diagnostics: diagnostics,
			NextActions: []agentprotocol.NextAction{},
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	plan, err = freezePlan(plan)
	if err != nil {
		return reject(CodePlanRejected, "/host", "host could not freeze the semantic render plan"), nil
	}
	receipt, err := encodeReceipt(Receipt{Profile: ReceiptProfile, Plan: plan})
	if err != nil {
		if errors.Is(err, errReceiptTooLarge) {
			return reject(CodePlanRejected, "/host", "frozen render plan exceeds the Agent result budget"), nil
		}
		return router.Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: "planned", RenderRequired: true,
		BaseRevision: &plan.Revision, Diagnostics: diagnostics, Receipt: receipt,
		NextActions: []agentprotocol.NextAction{{
			Tool: agentprotocol.ToolRender, Action: "prepare", ProjectID: request.ProjectID,
		}},
	}, nil
}

func cloneDocument(document videoedit.Document) (videoedit.Document, error) {
	raw, err := canonicaljson.Marshal(document)
	if err != nil {
		return videoedit.Document{}, err
	}
	var clone videoedit.Document
	if err := wirejson.Decode(raw, &clone); err != nil {
		return videoedit.Document{}, err
	}
	return clone, nil
}

func freezePlan(plan videoedit.RenderPlan) (videoedit.RenderPlan, error) {
	raw, err := canonicaljson.Marshal(plan)
	if err != nil {
		return videoedit.RenderPlan{}, err
	}
	return videoedit.DecodeRenderPlan(raw)
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

func decodeObject(raw json.RawMessage, target any) error {
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("value must be an object")
	}
	return wirejson.Decode(raw, target)
}

func encodeReceipt(value Receipt) (json.RawMessage, error) {
	encoded, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("renderplan: encode receipt: %w", err)
	}
	if len(encoded) > agentprotocol.MaxResultBytes-(1<<20) {
		return nil, errReceiptTooLarge
	}
	return encoded, nil
}

func safeResolve(ctx context.Context, resolver DocumentResolver, projectID string) (_ videoedit.Document, returnErr error) {
	defer func() {
		if recover() != nil {
			returnErr = errors.New("renderplan: document resolver panicked")
		}
	}()
	return resolver.ResolveDocument(ctx, projectID)
}

func ownDiagnostics(input []videoedit.Diagnostic) ([]videoedit.Diagnostic, bool) {
	if len(input) > agentprotocol.MaxListItems {
		return nil, false
	}
	if len(input) == 0 {
		return []videoedit.Diagnostic{}, true
	}
	result := make([]videoedit.Diagnostic, len(input))
	for index, diagnostic := range input {
		result[index] = diagnostic
		result[index].Fixes = append([]videoedit.CandidateFix(nil), diagnostic.Fixes...)
		for fixIndex := range result[index].Fixes {
			result[index].Fixes[fixIndex].Operation = append(json.RawMessage(nil), diagnostic.Fixes[fixIndex].Operation...)
			result[index].Fixes[fixIndex].SideEffects = append([]string(nil), diagnostic.Fixes[fixIndex].SideEffects...)
		}
	}
	encoded, err := canonicaljson.Marshal(result)
	if err != nil || len(encoded) > 1<<20 {
		return nil, false
	}
	ok := !videoedit.HasErrorDiagnostics(result)
	state := "planned"
	if !ok {
		state = "rejected"
	}
	if err := agentprotocol.ValidateResult(agentprotocol.Result{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		OK:              ok,
		RequestID:       "diagnostic-validation",
		Tool:            agentprotocol.ToolRender,
		Action:          "plan",
		State:           state,
		ProjectID:       "diagnostic-validation",
		Diagnostics:     result,
		NextActions:     []agentprotocol.NextAction{},
	}); err != nil {
		return nil, false
	}
	return result, true
}

func reject(code, path, message string) router.Outcome {
	return router.Outcome{
		OK: false, State: "rejected",
		Diagnostics: []videoedit.Diagnostic{{
			Code: code, Severity: videoedit.SeverityError, Path: path, Message: message,
		}},
		NextActions: []agentprotocol.NextAction{},
	}
}

func validProjectID(value string) bool {
	return value != "" && len(value) <= agentprotocol.MaxIdentityBytes && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= agentprotocol.MaxIdentityScalars && strings.Trim(value, " \t\r\n") != ""
}

func isContextError(err error) (result bool) {
	defer func() {
		if recover() != nil {
			result = false
		}
	}()
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
