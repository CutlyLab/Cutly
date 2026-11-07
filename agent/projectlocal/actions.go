package projectlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/project/local"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	CodePayloadInvalid              = "PROJECT.PAYLOAD_INVALID"
	CodeProjectNotFound             = "PROJECT.NOT_FOUND"
	CodeProjectBusy                 = "PROJECT.BUSY"
	CodeProjectInvalid              = "PROJECT.INVALID_STATE"
	CodeProjectUnsupported          = "PROJECT.PLATFORM_UNSUPPORTED"
	CodeProjectLimit                = "PROJECT.RESOURCE_LIMIT"
	CodeProjectCommitUnknown        = "PROJECT.COMMIT_UNKNOWN"
	CodeCanonicalProfileUnsupported = "PROJECT.CANONICAL_PROFILE_UNSUPPORTED"
	CodeResultTooLarge              = "PROJECT.RESULT_TOO_LARGE"

	maxReceiptBytes = agentprotocol.MaxResultBytes - (1 << 20)
)

var ErrProjectUnknown = errors.New("projectlocal: project is not registered")

type Resolver interface {
	ResolveProject(context.Context, string) (*local.Service, error)
}

type ResolverFunc func(context.Context, string) (*local.Service, error)

func (function ResolverFunc) ResolveProject(ctx context.Context, projectID string) (*local.Service, error) {
	return function(ctx, projectID)
}

// Registry is an immutable project-ID registry and is safe for concurrent use.
type Registry struct {
	projects map[string]*local.Service
}

func NewRegistry(projects map[string]*local.Service) (*Registry, error) {
	if len(projects) == 0 {
		return nil, errors.New("projectlocal: at least one project is required")
	}
	owned := make(map[string]*local.Service, len(projects))
	for projectID, project := range projects {
		if !validProjectID(projectID) || project == nil {
			return nil, fmt.Errorf("projectlocal: invalid project registration %q", projectID)
		}
		owned[projectID] = project
	}
	return &Registry{projects: owned}, nil
}

func (registry *Registry) ResolveProject(ctx context.Context, projectID string) (*local.Service, error) {
	if ctx == nil {
		return nil, errors.New("projectlocal: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, ErrProjectUnknown
	}
	project := registry.projects[projectID]
	if project == nil {
		return nil, ErrProjectUnknown
	}
	return project, nil
}

type Service struct {
	resolver Resolver
}

func New(resolver Resolver) (*Service, error) {
	if resolver == nil {
		return nil, errors.New("projectlocal: resolver is required")
	}
	return &Service{resolver: resolver}, nil
}

func (service *Service) Routes() []router.Route {
	if service == nil {
		return nil
	}
	return []router.Route{
		{Tool: agentprotocol.ToolProject, Action: "inspect", Handler: router.ActionFunc(service.inspect)},
		{Tool: agentprotocol.ToolProject, Action: "status", Handler: router.ActionFunc(service.status)},
		{Tool: agentprotocol.ToolProject, Action: "history", Handler: router.ActionFunc(service.history)},
		{Tool: agentprotocol.ToolEdit, Action: "plan", Handler: router.ActionFunc(service.plan)},
		{Tool: agentprotocol.ToolEdit, Action: "apply", Handler: router.ActionFunc(service.apply)},
		{Tool: agentprotocol.ToolEdit, Action: "discard", Handler: router.ActionFunc(service.discard)},
		{Tool: agentprotocol.ToolEdit, Action: "revert", Handler: router.ActionFunc(service.revert)},
	}
}

type emptyPayload struct{}

type historyPayload struct {
	AfterSequence uint64 `json:"after_sequence,omitempty"`
	Limit         uint64 `json:"limit,omitempty"`
}

type planPayload struct {
	Transaction videoedit.Transaction `json:"transaction"`
}

type transactionPayload struct {
	TransactionID string `json:"transaction_id"`
}

type revisionPayload struct {
	TransactionID    string             `json:"transaction_id"`
	ExpectedRevision videoedit.Revision `json:"expected_revision"`
}

type inspectReceipt struct {
	Profile  string         `json:"profile"`
	Snapshot local.Snapshot `json:"snapshot"`
}

type statusReceipt struct {
	Profile string       `json:"profile"`
	Status  local.Status `json:"status"`
}

type historyReceipt struct {
	Profile       string                `json:"profile"`
	AfterSequence uint64                `json:"after_sequence"`
	Records       []local.HistoryRecord `json:"records"`
}

type mutationReceipt struct {
	Profile              string             `json:"profile"`
	Action               string             `json:"action"`
	State                string             `json:"state"`
	TransactionID        string             `json:"transaction_id,omitempty"`
	RevertsTransactionID string             `json:"reverts_transaction_id,omitempty"`
	BaseDocumentHash     string             `json:"base_document_hash,omitempty"`
	NewDocumentHash      string             `json:"new_document_hash,omitempty"`
	Plan                 *local.PlanSummary `json:"plan,omitempty"`
	CommitSequence       *uint64            `json:"commit_sequence,omitempty"`
	CommitDigest         string             `json:"commit_digest,omitempty"`
}

func (service *Service) inspect(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	if err := decodePayload(request.Payload, &emptyPayload{}); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	snapshot, err := project.Inspect(ctx)
	if err != nil {
		return serviceFailure(err)
	}
	receipt, err := encodeReceipt(inspectReceipt{Profile: "cutly.project-inspect.v1", Snapshot: snapshot})
	if err != nil {
		return readReceiptFailure(err), nil
	}
	revision := snapshot.Document.Revision
	return router.Outcome{
		OK: true, State: "inspected", BaseRevision: &revision,
		Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: projectNextActions(request.ProjectID, revision),
	}, nil
}

func (service *Service) status(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	if err := decodePayload(request.Payload, &emptyPayload{}); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	status, err := project.Status(ctx)
	if err != nil {
		return serviceFailure(err)
	}
	receipt, err := encodeReceipt(statusReceipt{Profile: "cutly.project-status.v1", Status: status})
	if err != nil {
		return readReceiptFailure(err), nil
	}
	revision := status.Revision
	return router.Outcome{
		OK: true, State: "available", BaseRevision: &revision,
		Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: projectNextActions(request.ProjectID, revision),
	}, nil
}

func (service *Service) history(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload historyPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	records, err := project.History(ctx, payload.AfterSequence, payload.Limit)
	if err != nil {
		return serviceFailure(err)
	}
	receipt, err := encodeReceipt(historyReceipt{
		Profile: "cutly.project-history.v1", AfterSequence: payload.AfterSequence, Records: records,
	})
	if err != nil {
		return readReceiptFailure(err), nil
	}
	return router.Outcome{
		OK: true, State: "available", Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: []agentprotocol.NextAction{{Tool: agentprotocol.ToolProject, Action: "status", ProjectID: request.ProjectID}},
	}, nil
}

func (service *Service) plan(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload planPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	if outcome, compatible, err := ensureAgentCompatible(ctx, project); err != nil || !compatible {
		return outcome, err
	}
	result, err := project.Plan(ctx, payload.Transaction)
	return mutationOutcome(request.ProjectID, result, err)
}

func (service *Service) apply(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload revisionPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	if outcome, compatible, err := ensureAgentCompatible(ctx, project); err != nil || !compatible {
		return outcome, err
	}
	result, err := project.Apply(ctx, payload.TransactionID, payload.ExpectedRevision)
	return mutationOutcome(request.ProjectID, result, err)
}

func (service *Service) discard(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload transactionPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	if outcome, compatible, err := ensureAgentCompatible(ctx, project); err != nil || !compatible {
		return outcome, err
	}
	result, err := project.Discard(ctx, payload.TransactionID)
	return mutationOutcome(request.ProjectID, result, err)
}

func (service *Service) revert(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload revisionPayload
	if err := decodePayload(request.Payload, &payload); err != nil {
		return payloadRejection(err), nil
	}
	project, rejection, err := service.resolve(ctx, request.ProjectID)
	if err != nil || rejection != nil {
		return outcomeOrError(rejection, err)
	}
	if outcome, compatible, err := ensureAgentCompatible(ctx, project); err != nil || !compatible {
		return outcome, err
	}
	result, err := project.Revert(ctx, payload.TransactionID, payload.ExpectedRevision)
	return mutationOutcome(request.ProjectID, result, err)
}

func (service *Service) resolve(ctx context.Context, projectID string) (*local.Service, *router.Outcome, error) {
	if service == nil || service.resolver == nil {
		return nil, nil, errors.New("projectlocal: service is uninitialized")
	}
	project, err := service.resolver.ResolveProject(ctx, projectID)
	if err == nil && project != nil {
		return project, nil, nil
	}
	if err == nil || errors.Is(err, ErrProjectUnknown) || errors.Is(err, local.ErrProjectNotFound) {
		outcome := reject(CodeProjectNotFound, "/project_id", "project is not registered or does not exist")
		return nil, &outcome, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	return nil, nil, err
}

func ensureAgentCompatible(ctx context.Context, project *local.Service) (router.Outcome, bool, error) {
	snapshot, err := project.Inspect(ctx)
	if err != nil {
		outcome, mapped, mapErr := mapServiceError(err)
		if mapped {
			return outcome, false, nil
		}
		return router.Outcome{}, false, mapErr
	}
	if _, err := canonicaljson.Marshal(snapshot); err != nil {
		return reject(
			CodeCanonicalProfileUnsupported, "/project_id",
			"project contains values outside the Cutly Agent integer-only canonical JSON profile",
		), false, nil
	}
	return router.Outcome{}, true, nil
}

func mutationOutcome(projectID string, result local.MutationResult, err error) (router.Outcome, error) {
	if err != nil {
		return serviceFailure(err)
	}
	receiptValue := mutationReceipt{
		Profile: "cutly.project-mutation.v1", Action: result.Action, State: result.State,
		TransactionID: result.TransactionID, RevertsTransactionID: result.RevertsTransactionID,
		BaseDocumentHash: result.BaseDocumentHash, NewDocumentHash: result.NewDocumentHash,
		Plan: result.Plan,
	}
	if result.Commit != nil {
		sequence := result.Commit.Sequence
		receiptValue.CommitSequence = &sequence
		receiptValue.CommitDigest = result.Commit.Commit.Digest
	}
	receipt, encodeErr := encodeReceipt(receiptValue)
	if encodeErr != nil {
		// The operation may already be durable. Return a transport error so a
		// caller retries the idempotent request instead of observing a false
		// business rejection.
		return router.Outcome{}, fmt.Errorf("projectlocal: encode durable mutation result: %w", encodeErr)
	}
	outcome := router.Outcome{
		OK: result.OK, State: result.State, TransactionID: result.TransactionID,
		BaseRevision: revisionPointer(result.BaseRevision), NewRevision: cloneRevision(result.NewRevision),
		Changed: result.Changed, Replayed: result.Replayed, RenderRequired: result.RenderRequired,
		Diagnostics: result.Diagnostics, Receipt: receipt,
		NextActions: mutationNextActions(projectID, result),
	}
	return outcome, nil
}

func mutationNextActions(projectID string, result local.MutationResult) []agentprotocol.NextAction {
	if result.OK && result.State == "planned" && result.NewRevision != nil {
		return []agentprotocol.NextAction{
			{Tool: agentprotocol.ToolEdit, Action: "apply", ProjectID: projectID, Revision: revisionPointer(result.BaseRevision)},
			{Tool: agentprotocol.ToolEdit, Action: "discard", ProjectID: projectID},
		}
	}
	actions := []agentprotocol.NextAction{
		{Tool: agentprotocol.ToolProject, Action: "status", ProjectID: projectID},
		{Tool: agentprotocol.ToolProject, Action: "history", ProjectID: projectID},
	}
	if result.OK && result.RenderRequired {
		actions = append(actions, agentprotocol.NextAction{Tool: agentprotocol.ToolRender, Action: "preflight", ProjectID: projectID})
	}
	return actions
}

func projectNextActions(projectID string, revision videoedit.Revision) []agentprotocol.NextAction {
	return []agentprotocol.NextAction{
		{Tool: agentprotocol.ToolEdit, Action: "plan", ProjectID: projectID, Revision: revisionPointer(revision)},
		{Tool: agentprotocol.ToolProject, Action: "history", ProjectID: projectID},
		{Tool: agentprotocol.ToolProject, Action: "status", ProjectID: projectID},
	}
}

func serviceFailure(err error) (router.Outcome, error) {
	outcome, mapped, returned := mapServiceError(err)
	if mapped {
		return outcome, nil
	}
	return router.Outcome{}, returned
}

func mapServiceError(err error) (router.Outcome, bool, error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return router.Outcome{}, false, err
	case errors.Is(err, local.ErrProjectNotFound):
		return reject(CodeProjectNotFound, "/project_id", "project does not exist"), true, nil
	case errors.Is(err, local.ErrBusy):
		return reject(CodeProjectBusy, "/project_id", "project is busy; retry with the same request"), true, nil
	case errors.Is(err, local.ErrUnsupported):
		return reject(CodeProjectUnsupported, "/project_id", "project locking is unsupported on this platform"), true, nil
	case errors.Is(err, local.ErrLimit):
		return reject(CodeProjectLimit, "/payload", "project resource limit was exceeded"), true, nil
	case errors.Is(err, local.ErrCommitUnknown):
		return reject(CodeProjectCommitUnknown, "/payload", "commit outcome is unknown; inspect the project and retry idempotently"), true, nil
	case errors.Is(err, local.ErrInvalid), errors.Is(err, local.ErrServiceClosed):
		return reject(CodeProjectInvalid, "/project_id", "project state is invalid or unavailable"), true, nil
	default:
		return router.Outcome{}, false, err
	}
}

func decodePayload(raw json.RawMessage, target any) error {
	if err := wirejson.Decode(raw, target); err != nil {
		return err
	}
	return nil
}

func encodeReceipt(value any) (json.RawMessage, error) {
	encoded, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxReceiptBytes {
		return nil, fmt.Errorf("receipt bytes %d exceed %d", len(encoded), maxReceiptBytes)
	}
	return encoded, nil
}

func readReceiptFailure(err error) router.Outcome {
	code := CodeCanonicalProfileUnsupported
	message := "project cannot be represented by the Cutly Agent integer-only canonical JSON profile"
	if strings.Contains(err.Error(), "exceed") {
		code = CodeResultTooLarge
		message = "project result exceeds the Agent response limit"
	}
	return reject(code, "/project_id", message)
}

func payloadRejection(err error) router.Outcome {
	return reject(CodePayloadInvalid, "/payload", "payload does not match the action contract: "+err.Error())
}

func reject(code, path, message string) router.Outcome {
	return router.Outcome{
		OK: false, State: "rejected",
		Diagnostics: []videoedit.Diagnostic{{Code: code, Severity: videoedit.SeverityError, Path: path, Message: message}},
		NextActions: []agentprotocol.NextAction{},
	}
}

func outcomeOrError(outcome *router.Outcome, err error) (router.Outcome, error) {
	if err != nil {
		return router.Outcome{}, err
	}
	return *outcome, nil
}

func revisionPointer(value videoedit.Revision) *videoedit.Revision {
	copy := value
	return &copy
}

func cloneRevision(value *videoedit.Revision) *videoedit.Revision {
	if value == nil {
		return nil
	}
	return revisionPointer(*value)
}

func validProjectID(value string) bool {
	return value != "" && len(value) <= agentprotocol.MaxIdentityBytes && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= agentprotocol.MaxIdentityScalars && strings.Trim(value, " \t\r\n") != ""
}
