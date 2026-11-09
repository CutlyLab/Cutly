// Package projectlocalv2 exposes the durable Document v2 project lifecycle to
// the independent v2 Agent router.
package projectlocalv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/agent/routerv2"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/transactionv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/project/localv2"
)

const (
	CodePayloadInvalid       = "PROJECT.V2.PAYLOAD_INVALID"
	CodeProjectNotFound      = "PROJECT.V2.NOT_FOUND"
	CodeProjectBusy          = "PROJECT.V2.BUSY"
	CodeProjectInvalid       = "PROJECT.V2.INVALID_STATE"
	CodeProjectUnsupported   = "PROJECT.V2.PLATFORM_UNSUPPORTED"
	CodeProjectLimit         = "PROJECT.V2.RESOURCE_LIMIT"
	CodeProjectConflict      = "PROJECT.V2.CONFLICT"
	CodePlanNotFound         = "PROJECT.V2.PLAN_NOT_FOUND"
	CodePlanDiscarded        = "PROJECT.V2.PLAN_DISCARDED"
	CodeProjectCommitUnknown = "PROJECT.V2.COMMIT_UNKNOWN"
)

var ErrProjectUnknown = errors.New("projectlocalv2: project is not registered")

type Resolver interface {
	ResolveProjectV2(context.Context, string) (*localv2.Service, error)
}

type Registry struct{ projects map[string]*localv2.Service }

func NewRegistry(projects map[string]*localv2.Service) (*Registry, error) {
	if len(projects) == 0 {
		return nil, errors.New("projectlocalv2: at least one project is required")
	}
	owned := make(map[string]*localv2.Service, len(projects))
	for id, project := range projects {
		if id == "" || project == nil {
			return nil, fmt.Errorf("projectlocalv2: invalid project registration %q", id)
		}
		owned[id] = project
	}
	return &Registry{projects: owned}, nil
}

func (registry *Registry) ResolveProjectV2(ctx context.Context, projectID string) (*localv2.Service, error) {
	if ctx == nil {
		return nil, errors.New("projectlocalv2: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry == nil || registry.projects[projectID] == nil {
		return nil, ErrProjectUnknown
	}
	return registry.projects[projectID], nil
}

type Service struct{ resolver Resolver }

func New(resolver Resolver) (*Service, error) {
	if resolver == nil {
		return nil, errors.New("projectlocalv2: resolver is required")
	}
	return &Service{resolver: resolver}, nil
}

func (service *Service) Routes() []routerv2.Route {
	if service == nil {
		return nil
	}
	return []routerv2.Route{
		{Tool: protocolv2.ToolProject, Action: "inspect", Handler: routerv2.ActionFunc(service.inspect)},
		{Tool: protocolv2.ToolProject, Action: "status", Handler: routerv2.ActionFunc(service.status)},
		{Tool: protocolv2.ToolProject, Action: "history", Handler: routerv2.ActionFunc(service.history)},
		{Tool: protocolv2.ToolEdit, Action: "plan", Handler: routerv2.ActionFunc(service.plan)},
		{Tool: protocolv2.ToolEdit, Action: "apply", Handler: routerv2.ActionFunc(service.apply)},
		{Tool: protocolv2.ToolEdit, Action: "discard", Handler: routerv2.ActionFunc(service.discard)},
		{Tool: protocolv2.ToolEdit, Action: "revert", Handler: routerv2.ActionFunc(service.revert)},
		{Tool: protocolv2.ToolRender, Action: "plan", Handler: routerv2.ActionFunc(service.renderPlan)},
	}
}

type emptyPayload struct{}
type historyPayload struct {
	AfterSequence uint64 `json:"after_sequence,omitempty"`
	Limit         uint64 `json:"limit,omitempty"`
}
type planPayload struct {
	Transaction json.RawMessage `json:"transaction"`
}
type transactionPayload struct {
	TransactionID string `json:"transaction_id"`
}
type revertPayload struct {
	TargetTransactionID string `json:"target_transaction_id"`
	TransactionID       string `json:"transaction_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}
type renderPlanPayload struct {
	Range  documentv2.TimeRange   `json:"range"`
	Target videoedit.RenderTarget `json:"target"`
}

func (service *Service) inspect(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	if err := decodePayload(request.Payload, &emptyPayload{}); err != nil {
		return reject(CodePayloadInvalid, "/payload", err.Error()), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	snapshot, err := project.Inspect(ctx)
	if err != nil {
		return failure(err)
	}
	return success("inspected", "cutly.project-inspect.document-v2.v2", snapshot)
}

func (service *Service) status(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	if err := decodePayload(request.Payload, &emptyPayload{}); err != nil {
		return reject(CodePayloadInvalid, "/payload", err.Error()), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	status, err := project.Status(ctx)
	if err != nil {
		return failure(err)
	}
	return success("available", "cutly.project-status.document-v2.v2", status)
}

func (service *Service) history(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload historyPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return reject(CodePayloadInvalid, "/payload", err.Error()), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	records, err := project.History(ctx, payload.AfterSequence, payload.Limit)
	if err != nil {
		return failure(err)
	}
	return success("available", "cutly.project-history.document-v2.v2", struct {
		AfterSequence uint64                 `json:"after_sequence"`
		Records       []localv2.CommitRecord `json:"records"`
	}{payload.AfterSequence, records})
}

func (service *Service) plan(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload planPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return reject(CodePayloadInvalid, "/payload", err.Error()), nil
	}
	tx, err := transactionv2.Decode(payload.Transaction)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/transaction", err.Error()), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	result, err := project.Plan(ctx, tx)
	if err != nil {
		return failure(err)
	}
	return mutation("planned", result)
}

func (service *Service) apply(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload transactionPayload
	if err := decodePayload(request.Payload, &payload); err != nil || payload.TransactionID == "" {
		return reject(CodePayloadInvalid, "/payload/transaction_id", "transaction_id is required"), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	result, err := project.Apply(ctx, payload.TransactionID)
	if err != nil {
		return failure(err)
	}
	return mutation("applied", result)
}

func (service *Service) discard(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload transactionPayload
	if err := decodePayload(request.Payload, &payload); err != nil || payload.TransactionID == "" {
		return reject(CodePayloadInvalid, "/payload/transaction_id", "transaction_id is required"), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	result, err := project.Discard(ctx, payload.TransactionID)
	if err != nil {
		return failure(err)
	}
	return mutation("discarded", result)
}

func (service *Service) revert(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload revertPayload
	if err := decodePayload(request.Payload, &payload); err != nil || payload.TargetTransactionID == "" || payload.TransactionID == "" || payload.IdempotencyKey == "" {
		return reject(CodePayloadInvalid, "/payload", "target_transaction_id, transaction_id and idempotency_key are required"), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	result, err := project.Revert(ctx, payload.TargetTransactionID, payload.TransactionID, payload.IdempotencyKey)
	if err != nil {
		return failure(err)
	}
	return mutation("reverted", result)
}

func (service *Service) renderPlan(ctx context.Context, request protocolv2.Request) (routerv2.Outcome, error) {
	var payload renderPlanPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return reject(CodePayloadInvalid, "/payload", err.Error()), nil
	}
	project, outcome, err := service.resolve(ctx, request.ProjectID)
	if err != nil || outcome != nil {
		return outcomeOrError(outcome, err)
	}
	snapshot, err := project.Inspect(ctx)
	if err != nil {
		return failure(err)
	}
	plan, diagnostics := renderplanv2.Resolve(ctx, snapshot.Document, payload.Range, payload.Target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return routerv2.Outcome{State: "rejected", Diagnostics: diagnostics, Receipt: json.RawMessage(`{}`)}, nil
	}
	receipt, err := videoedit.CanonicalJSON(struct {
		Profile string            `json:"profile"`
		Plan    renderplanv2.Plan `json:"plan"`
	}{"cutly.render-plan.document-v2.v2", plan})
	if err != nil {
		return routerv2.Outcome{}, err
	}
	return routerv2.Outcome{OK: true, State: "planned", Diagnostics: diagnostics, Receipt: receipt}, nil
}

func (service *Service) resolve(ctx context.Context, projectID string) (*localv2.Service, *routerv2.Outcome, error) {
	project, err := service.resolver.ResolveProjectV2(ctx, projectID)
	if errors.Is(err, ErrProjectUnknown) {
		outcome := reject(CodeProjectNotFound, "/project_id", "project is not registered")
		return nil, &outcome, nil
	}
	return project, nil, err
}

func decodePayload(raw json.RawMessage, target any) error {
	return wirejson.Decode(raw, target)
}

func success(state, profile string, value any) (routerv2.Outcome, error) {
	receipt, err := videoedit.CanonicalJSON(struct {
		Profile string `json:"profile"`
		Value   any    `json:"value"`
	}{profile, value})
	if err != nil {
		return routerv2.Outcome{}, err
	}
	return routerv2.Outcome{OK: true, State: state, Diagnostics: []documentv2.Diagnostic{}, Receipt: receipt}, nil
}

func mutation(state string, result localv2.MutationResult) (routerv2.Outcome, error) {
	receipt, err := videoedit.CanonicalJSON(struct {
		Profile string                 `json:"profile"`
		Result  localv2.MutationResult `json:"result"`
	}{"cutly.project-mutation.document-v2.v2", result})
	if err != nil {
		return routerv2.Outcome{}, err
	}
	diagnostics := append([]documentv2.Diagnostic(nil), result.Diagnostics...)
	if !result.OK && !documentv2.HasErrorDiagnostics(diagnostics) {
		diagnostics = append(diagnostics, documentv2.Diagnostic{Code: CodeProjectInvalid, Severity: documentv2.SeverityError, Path: "/receipt", Message: "project mutation was rejected"})
	}
	return routerv2.Outcome{OK: result.OK, State: state, Diagnostics: diagnostics, Receipt: receipt}, nil
}

func failure(err error) (routerv2.Outcome, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return routerv2.Outcome{}, err
	}
	code := CodeProjectInvalid
	switch {
	case errors.Is(err, localv2.ErrBusy):
		code = CodeProjectBusy
	case errors.Is(err, localv2.ErrUnsupported):
		code = CodeProjectUnsupported
	case errors.Is(err, localv2.ErrLimit):
		code = CodeProjectLimit
	case errors.Is(err, localv2.ErrConflict):
		code = CodeProjectConflict
	case errors.Is(err, localv2.ErrPlanNotFound):
		code = CodePlanNotFound
	case errors.Is(err, localv2.ErrDiscarded):
		code = CodePlanDiscarded
	case errors.Is(err, localv2.ErrCommitUnknown), errors.Is(err, localv2.ErrDurabilityUncertain):
		code = CodeProjectCommitUnknown
	}
	return reject(code, "", err.Error()), nil
}

func reject(code, path, message string) routerv2.Outcome {
	return routerv2.Outcome{State: "rejected", Diagnostics: []documentv2.Diagnostic{{Code: code, Severity: documentv2.SeverityError, Path: path, Message: message}}, Receipt: json.RawMessage(`{}`)}
}

func outcomeOrError(outcome *routerv2.Outcome, err error) (routerv2.Outcome, error) {
	if err != nil {
		return routerv2.Outcome{}, err
	}
	return *outcome, nil
}
