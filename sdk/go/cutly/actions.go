package cutly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

var ErrReceiptInvalid = errors.New("cutly sdk: Agent receipt is invalid")

type ProjectPlanState string

const (
	ProjectPlanPending   ProjectPlanState = "pending"
	ProjectPlanApplied   ProjectPlanState = "applied"
	ProjectPlanDiscarded ProjectPlanState = "discarded"
)

type ProjectPlanSummaryV1 struct {
	TransactionID  string             `json:"transaction_id"`
	State          ProjectPlanState   `json:"state"`
	BaseRevision   videoedit.Revision `json:"base_revision"`
	NewRevision    videoedit.Revision `json:"new_revision"`
	BaseHash       string             `json:"base_hash"`
	CandidateHash  string             `json:"candidate_hash"`
	OperationCount uint64             `json:"operation_count"`
}

type ProjectSnapshotV1 struct {
	Document      DocumentV1             `json:"document"`
	DocumentHash  string                 `json:"document_hash"`
	StateSequence uint64                 `json:"state_sequence"`
	PendingPlans  []ProjectPlanSummaryV1 `json:"pending_plans"`
	HistoryCount  uint64                 `json:"history_count"`
}

type ProjectStatusV1 struct {
	DocumentID      videoedit.ID       `json:"document_id"`
	Revision        videoedit.Revision `json:"revision"`
	DocumentHash    string             `json:"document_hash"`
	StateSequence   uint64             `json:"state_sequence"`
	PendingPlans    uint64             `json:"pending_plans"`
	AppliedPlans    uint64             `json:"applied_plans"`
	DiscardedPlans  uint64             `json:"discarded_plans"`
	HistoryCount    uint64             `json:"history_count"`
	RecoveryPending bool               `json:"recovery_pending"`
}

type ProjectHistoryRecordV1 struct {
	Sequence             uint64             `json:"sequence"`
	BaseRevision         videoedit.Revision `json:"base_revision"`
	BaseDocumentHash     string             `json:"base_document_hash"`
	Transaction          TransactionV1      `json:"transaction"`
	Commit               CommitRecordV1     `json:"commit"`
	RevertsTransactionID string             `json:"reverts_transaction_id,omitempty"`
}

type ProjectInspectReceiptV1 struct {
	Profile  string            `json:"profile"`
	Snapshot ProjectSnapshotV1 `json:"snapshot"`
}

type ProjectStatusReceiptV1 struct {
	Profile string          `json:"profile"`
	Status  ProjectStatusV1 `json:"status"`
}

type ProjectHistoryReceiptV1 struct {
	Profile       string                   `json:"profile"`
	AfterSequence uint64                   `json:"after_sequence"`
	Records       []ProjectHistoryRecordV1 `json:"records"`
}

type ProjectMutationReceiptV1 struct {
	Profile              string                `json:"profile"`
	Action               string                `json:"action"`
	State                string                `json:"state"`
	TransactionID        string                `json:"transaction_id,omitempty"`
	RevertsTransactionID string                `json:"reverts_transaction_id,omitempty"`
	BaseDocumentHash     string                `json:"base_document_hash,omitempty"`
	NewDocumentHash      string                `json:"new_document_hash,omitempty"`
	Plan                 *ProjectPlanSummaryV1 `json:"plan,omitempty"`
	CommitSequence       *uint64               `json:"commit_sequence,omitempty"`
	CommitDigest         string                `json:"commit_digest,omitempty"`
}

type historyPayload struct {
	AfterSequence uint64 `json:"after_sequence,omitempty"`
	Limit         uint64 `json:"limit,omitempty"`
}

type planPayload struct {
	Transaction TransactionV1 `json:"transaction"`
}

type transactionPayload struct {
	TransactionID string `json:"transaction_id"`
}

type revisionPayload struct {
	TransactionID    string             `json:"transaction_id"`
	ExpectedRevision videoedit.Revision `json:"expected_revision"`
}

type renderPayload struct {
	Job RenderJobV1 `json:"job"`
}

type jobPayload struct {
	JobID string `json:"job_id"`
}

func (client *Client) InspectProject(ctx context.Context, requestID, projectID string) (Result, error) {
	return client.Invoke(ctx, requestID, ToolProject, "inspect", projectID, struct{}{})
}

func (client *Client) ProjectStatus(ctx context.Context, requestID, projectID string) (Result, error) {
	return client.Invoke(ctx, requestID, ToolProject, "status", projectID, struct{}{})
}

func (client *Client) ProjectHistory(ctx context.Context, requestID, projectID string, afterSequence, limit uint64) (Result, error) {
	return client.Invoke(ctx, requestID, ToolProject, "history", projectID, historyPayload{AfterSequence: afterSequence, Limit: limit})
}

func (client *Client) PlanEditV1(ctx context.Context, requestID, projectID string, transaction TransactionV1) (Result, error) {
	return client.Invoke(ctx, requestID, ToolEdit, "plan", projectID, planPayload{Transaction: transaction})
}

func (client *Client) ApplyEditV1(
	ctx context.Context,
	requestID, projectID, transactionID string,
	expectedRevision videoedit.Revision,
) (Result, error) {
	return client.Invoke(ctx, requestID, ToolEdit, "apply", projectID, revisionPayload{
		TransactionID: transactionID, ExpectedRevision: expectedRevision,
	})
}

func (client *Client) DiscardEditV1(ctx context.Context, requestID, projectID, transactionID string) (Result, error) {
	return client.Invoke(ctx, requestID, ToolEdit, "discard", projectID, transactionPayload{TransactionID: transactionID})
}

func (client *Client) RevertEditV1(
	ctx context.Context,
	requestID, projectID, transactionID string,
	expectedRevision videoedit.Revision,
) (Result, error) {
	return client.Invoke(ctx, requestID, ToolEdit, "revert", projectID, revisionPayload{
		TransactionID: transactionID, ExpectedRevision: expectedRevision,
	})
}

func (client *Client) SubmitRender(ctx context.Context, requestID, projectID string, job RenderJobV1) (Result, error) {
	return client.Invoke(ctx, requestID, ToolRender, "render", projectID, renderPayload{Job: job})
}

func (client *Client) RenderStatus(ctx context.Context, requestID, projectID, jobID string) (Result, error) {
	return client.Invoke(ctx, requestID, ToolRender, "status", projectID, jobPayload{JobID: jobID})
}

func (client *Client) CancelRender(ctx context.Context, requestID, projectID, jobID string) (Result, error) {
	return client.Invoke(ctx, requestID, ToolRender, "cancel", projectID, jobPayload{JobID: jobID})
}

func DecodeProjectInspectReceipt(raw json.RawMessage) (ProjectInspectReceiptV1, error) {
	var receipt ProjectInspectReceiptV1
	if err := decodeReceipt(raw, "cutly.project-inspect.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return ProjectInspectReceiptV1{}, err
	}
	if diagnostics := videoedit.ValidateDocument(receipt.Snapshot.Document); videoedit.HasErrorDiagnostics(diagnostics) {
		return ProjectInspectReceiptV1{}, fmt.Errorf("%w: snapshot document: %s", ErrReceiptInvalid, diagnostics[0].Code)
	}
	return receipt, nil
}

func DecodeProjectStatusReceipt(raw json.RawMessage) (ProjectStatusReceiptV1, error) {
	var receipt ProjectStatusReceiptV1
	if err := decodeReceipt(raw, "cutly.project-status.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return ProjectStatusReceiptV1{}, err
	}
	return receipt, nil
}

func DecodeProjectHistoryReceipt(raw json.RawMessage) (ProjectHistoryReceiptV1, error) {
	var receipt ProjectHistoryReceiptV1
	if err := decodeReceipt(raw, "cutly.project-history.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return ProjectHistoryReceiptV1{}, err
	}
	if receipt.Records == nil {
		return ProjectHistoryReceiptV1{}, fmt.Errorf("%w: history records must be an array", ErrReceiptInvalid)
	}
	return receipt, nil
}

func DecodeProjectMutationReceipt(raw json.RawMessage) (ProjectMutationReceiptV1, error) {
	var receipt ProjectMutationReceiptV1
	if err := decodeReceipt(raw, "cutly.project-mutation.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return ProjectMutationReceiptV1{}, err
	}
	return receipt, nil
}

func decodeReceipt(raw json.RawMessage, profile string, target any, actual func() string) error {
	if len(raw) == 0 || len(raw) > agentprotocol.MaxResultBytes {
		return fmt.Errorf("%w: encoded length %d", ErrReceiptInvalid, len(raw))
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return fmt.Errorf("%w: receipt is not canonical: %v", ErrReceiptInvalid, err)
	}
	if err := wirejson.Decode(raw, target); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrReceiptInvalid, err)
	}
	if actual() != profile {
		return fmt.Errorf("%w: profile %q, want %q", ErrReceiptInvalid, actual(), profile)
	}
	return nil
}
