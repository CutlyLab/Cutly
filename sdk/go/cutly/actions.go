package cutly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
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

// RenderPrepareResourceV1 binds one immutable plan resource to a host-local
// file. Backend, runtime and output choices are deliberately absent: cutlyd
// observes and freezes those trusted values.
type RenderPrepareResourceV1 struct {
	AssetID videoedit.ID `json:"asset_id"`
	Locator string       `json:"locator"`
}

type renderPreparePayload struct {
	Plan              RenderPlanV3              `json:"plan"`
	Resources         []RenderPrepareResourceV1 `json:"resources"`
	DeterministicSeed *int64                    `json:"deterministic_seed,omitempty"`
}

type renderPreflightPayload struct {
	Plan              RenderPlanV3        `json:"plan"`
	ExecutionManifest ExecutionManifestV1 `json:"execution_manifest"`
}

type renderFramePayload struct {
	Plan              RenderPlanV3        `json:"plan"`
	ExecutionManifest ExecutionManifestV1 `json:"execution_manifest"`
	Frame             ExactFrameRequestV1 `json:"frame"`
}

type jobPayload struct {
	JobID string `json:"job_id"`
}

const RenderPrepareReceiptProfileV1 = "cutly.agent-render-prepare.v1"

type RenderPrepareReceiptV1 struct {
	Profile           string              `json:"profile"`
	ExecutionManifest ExecutionManifestV1 `json:"execution_manifest"`
}

type RenderPreflightReceiptV1 struct {
	Profile                      string `json:"profile"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	BackendID                    string `json:"backend_id"`
	BackendBuildFingerprint      string `json:"backend_build_fingerprint"`
}

type RenderFrameReceiptV1 struct {
	Profile  string               `json:"profile"`
	Request  ExactFrameRequestV1  `json:"request"`
	Artifact ExactFrameArtifactV1 `json:"artifact"`
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

// PrepareRender asks the trusted host to probe the supplied local resources
// and freeze the environment-specific ExecutionManifest. A nil seed omits the
// wire field and has the protocol-defined value zero.
func (client *Client) PrepareRender(
	ctx context.Context,
	requestID, projectID string,
	plan RenderPlanV3,
	resources []RenderPrepareResourceV1,
	deterministicSeed *int64,
) (Result, error) {
	ownedResources := append([]RenderPrepareResourceV1{}, resources...)
	var ownedSeed *int64
	if deterministicSeed != nil {
		seed := *deterministicSeed
		ownedSeed = &seed
	}
	return client.Invoke(ctx, requestID, ToolRender, "prepare", projectID, renderPreparePayload{
		Plan: plan, Resources: ownedResources, DeterministicSeed: ownedSeed,
	})
}

func (client *Client) PreflightRender(
	ctx context.Context,
	requestID, projectID string,
	plan RenderPlanV3,
	manifest ExecutionManifestV1,
) (Result, error) {
	return client.Invoke(ctx, requestID, ToolRender, "preflight", projectID, renderPreflightPayload{
		Plan: plan, ExecutionManifest: manifest,
	})
}

func (client *Client) RenderFrame(
	ctx context.Context,
	requestID, projectID string,
	plan RenderPlanV3,
	manifest ExecutionManifestV1,
	frame ExactFrameRequestV1,
) (Result, error) {
	return client.Invoke(ctx, requestID, ToolRender, "frame", projectID, renderFramePayload{
		Plan: plan, ExecutionManifest: manifest, Frame: frame,
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

func DecodeRenderPrepareReceipt(raw json.RawMessage) (RenderPrepareReceiptV1, error) {
	var receipt RenderPrepareReceiptV1
	if err := decodeReceipt(raw, RenderPrepareReceiptProfileV1, &receipt, func() string { return receipt.Profile }); err != nil {
		return RenderPrepareReceiptV1{}, err
	}
	encoded, err := canonicaljson.Marshal(receipt.ExecutionManifest)
	if err != nil {
		return RenderPrepareReceiptV1{}, fmt.Errorf("%w: execution manifest encode: %v", ErrReceiptInvalid, err)
	}
	verified, err := renderapi.DecodeExecutionManifest(encoded)
	if err != nil {
		return RenderPrepareReceiptV1{}, fmt.Errorf("%w: execution manifest: %v", ErrReceiptInvalid, err)
	}
	receipt.ExecutionManifest = verified
	return receipt, nil
}

func ValidateRenderPrepareReceipt(
	receipt RenderPrepareReceiptV1,
	plan RenderPlanV3,
	resources []RenderPrepareResourceV1,
	deterministicSeed *int64,
) error {
	if receipt.Profile != RenderPrepareReceiptProfileV1 {
		return fmt.Errorf("%w: prepare receipt profile", ErrReceiptInvalid)
	}
	expectedSeed := int64(0)
	if deterministicSeed != nil {
		expectedSeed = *deterministicSeed
	}
	if receipt.ExecutionManifest.DeterministicSeed != expectedSeed {
		return fmt.Errorf("%w: prepare deterministic seed binding", ErrReceiptInvalid)
	}
	refrozen, err := renderapi.FreezeExecutionManifest(plan, receipt.ExecutionManifest)
	var suppliedManifest, refrozenManifest []byte
	if err == nil {
		suppliedManifest, err = canonicaljson.Marshal(receipt.ExecutionManifest)
	}
	if err == nil {
		refrozenManifest, err = canonicaljson.Marshal(refrozen)
	}
	if err != nil || !bytes.Equal(suppliedManifest, refrozenManifest) {
		return fmt.Errorf("%w: prepare plan/manifest binding: %v", ErrReceiptInvalid, err)
	}

	wanted := make(map[videoedit.ID]string, len(resources))
	for _, resource := range resources {
		if resource.AssetID == "" || resource.Locator == "" {
			return fmt.Errorf("%w: prepare resource binding is incomplete", ErrReceiptInvalid)
		}
		if _, duplicate := wanted[resource.AssetID]; duplicate {
			return fmt.Errorf("%w: duplicate prepare resource binding", ErrReceiptInvalid)
		}
		wanted[resource.AssetID] = resource.Locator
	}
	if len(wanted) != len(receipt.ExecutionManifest.Resources) {
		return fmt.Errorf("%w: prepare resource binding count", ErrReceiptInvalid)
	}
	for _, materialized := range receipt.ExecutionManifest.Resources {
		locator, exists := wanted[materialized.AssetID]
		if !exists || materialized.LocatorScheme != renderapi.LocatorFile || materialized.Locator != locator {
			return fmt.Errorf("%w: prepare resource binding", ErrReceiptInvalid)
		}
		delete(wanted, materialized.AssetID)
	}
	if len(wanted) != 0 {
		return fmt.Errorf("%w: prepare resource binding is incomplete", ErrReceiptInvalid)
	}
	return nil
}

func DecodeRenderPreflightReceipt(raw json.RawMessage) (RenderPreflightReceiptV1, error) {
	var receipt RenderPreflightReceiptV1
	if err := decodeReceipt(raw, "cutly.agent-render-preflight.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return RenderPreflightReceiptV1{}, err
	}
	if !canonicalDigest(receipt.PlanFingerprint) || !canonicalDigest(receipt.ExecutionManifestFingerprint) ||
		!canonicalDigest(receipt.BackendBuildFingerprint) || strings.TrimSpace(receipt.BackendID) == "" {
		return RenderPreflightReceiptV1{}, fmt.Errorf("%w: preflight identities are incomplete", ErrReceiptInvalid)
	}
	return receipt, nil
}

func ValidateRenderPreflightReceipt(
	receipt RenderPreflightReceiptV1,
	plan RenderPlanV3,
	manifest ExecutionManifestV1,
) error {
	if receipt.Profile != "cutly.agent-render-preflight.v1" || receipt.PlanFingerprint != plan.Fingerprint ||
		receipt.ExecutionManifestFingerprint != manifest.Fingerprint ||
		receipt.BackendID != manifest.Backend.BackendID ||
		receipt.BackendBuildFingerprint != manifest.Backend.BuildFingerprint {
		return fmt.Errorf("%w: preflight receipt binding", ErrReceiptInvalid)
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
		return fmt.Errorf("%w: plan/manifest binding: %v", ErrReceiptInvalid, err)
	}
	return nil
}

func DecodeRenderFrameReceipt(raw json.RawMessage) (RenderFrameReceiptV1, error) {
	var receipt RenderFrameReceiptV1
	if err := decodeReceipt(raw, "cutly.agent-render-frame.v1", &receipt, func() string { return receipt.Profile }); err != nil {
		return RenderFrameReceiptV1{}, err
	}
	requestRaw, err := canonicaljson.Marshal(receipt.Request)
	if err != nil {
		return RenderFrameReceiptV1{}, fmt.Errorf("%w: frame request encode: %v", ErrReceiptInvalid, err)
	}
	verified, err := renderapi.DecodeExactFrameRequest(requestRaw)
	if err != nil || !reflect.DeepEqual(verified, receipt.Request) {
		return RenderFrameReceiptV1{}, fmt.Errorf("%w: frame request: %v", ErrReceiptInvalid, err)
	}
	return receipt, nil
}

func ValidateRenderFrameReceipt(
	receipt RenderFrameReceiptV1,
	plan RenderPlanV3,
	manifest ExecutionManifestV1,
) error {
	if receipt.Profile != "cutly.agent-render-frame.v1" {
		return fmt.Errorf("%w: frame receipt profile", ErrReceiptInvalid)
	}
	if err := renderapi.ValidateExactFrameArtifact(receipt.Artifact, renderapi.ExactFrameRenderRequest{
		Frame: receipt.Request, Plan: plan, Manifest: manifest,
	}); err != nil {
		return fmt.Errorf("%w: frame artifact: %v", ErrReceiptInvalid, err)
	}
	return nil
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

func canonicalDigest(value string) bool {
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
