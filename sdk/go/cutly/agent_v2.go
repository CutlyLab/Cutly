package cutly

import (
	"context"
	"fmt"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

type AgentRequestV2 = protocolv2.Request
type AgentResultV2 = protocolv2.Result
type AgentToolV2 = protocolv2.Tool

const (
	AgentSchemaVersionV2  = protocolv2.SchemaVersion
	AgentRequestProfileV2 = protocolv2.RequestProfile
	AgentResultProfileV2  = protocolv2.ResultProfile
	AgentMediaTypeV2      = protocolv2.MediaType
	AgentToolProjectV2    = protocolv2.ToolProject
	AgentToolEditV2       = protocolv2.ToolEdit
	AgentToolRenderV2     = protocolv2.ToolRender
	FramePreviewProfileV2 = "cutly.preview-frame.document-v2.v2"
)

// NewAgentRequestV2 constructs and validates one independent Document v2
// Agent request. Caller-owned request IDs make exact retry behavior explicit.
func NewAgentRequestV2(ctx context.Context, requestID string, tool AgentToolV2, action, projectID string, payload any) (AgentRequestV2, error) {
	if payload == nil {
		payload = struct{}{}
	}
	raw, err := videoedit.CanonicalJSON(payload)
	if err != nil {
		return AgentRequestV2{}, fmt.Errorf("cutly sdk: encode Agent v2 payload: %w", err)
	}
	request := AgentRequestV2{
		SchemaVersion:  AgentSchemaVersionV2,
		Profile:        AgentRequestProfileV2,
		RequestID:      requestID,
		Tool:           tool,
		Action:         action,
		ProjectID:      projectID,
		PayloadProfile: protocolv2.PayloadProfile(tool, action),
		Payload:        append([]byte(nil), raw...),
	}
	if _, err := protocolv2.CanonicalRequest(ctx, request); err != nil {
		return AgentRequestV2{}, err
	}
	return request, nil
}

func DecodeAgentRequestV2(ctx context.Context, raw []byte) (AgentRequestV2, error) {
	return protocolv2.DecodeRequest(ctx, raw)
}

func CanonicalAgentResultV2(ctx context.Context, result AgentResultV2) ([]byte, error) {
	return protocolv2.CanonicalResult(ctx, result)
}

// AgentClientV2 is the strict Document v2 Agent client. It does not negotiate
// or fall back to the legacy v1 envelope.
type AgentClientV2 struct{ transport Transport }

func NewAgentClientV2(transport Transport) (*AgentClientV2, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: nil Agent v2 transport", ErrTransport)
	}
	return &AgentClientV2{transport: transport}, nil
}

func (client *AgentClientV2) Call(ctx context.Context, request AgentRequestV2) (AgentResultV2, error) {
	if client == nil || client.transport == nil || ctx == nil {
		return AgentResultV2{}, fmt.Errorf("%w: uninitialized Agent v2 client", ErrTransport)
	}
	canonical, err := protocolv2.CanonicalRequest(ctx, request)
	if err != nil {
		return AgentResultV2{}, err
	}
	response, err := client.transport.RoundTrip(ctx, append([]byte(nil), canonical...))
	if err != nil {
		return AgentResultV2{}, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	result, err := protocolv2.DecodeResult(ctx, response)
	if err != nil {
		return AgentResultV2{}, fmt.Errorf("%w: decode Agent v2 result: %v", ErrTransport, err)
	}
	if result.RequestID != request.RequestID || result.Tool != request.Tool || result.Action != request.Action || result.ProjectID != request.ProjectID {
		return AgentResultV2{}, fmt.Errorf("%w: Agent v2 response identity differs", ErrCorrelation)
	}
	return result, nil
}

func (client *AgentClientV2) Invoke(ctx context.Context, requestID string, tool AgentToolV2, action, projectID string, payload any) (AgentResultV2, error) {
	request, err := NewAgentRequestV2(ctx, requestID, tool, action, projectID, payload)
	if err != nil {
		return AgentResultV2{}, err
	}
	return client.Call(ctx, request)
}

func (client *AgentClientV2) InspectProject(ctx context.Context, requestID, projectID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolProjectV2, "inspect", projectID, struct{}{})
}

func (client *AgentClientV2) ProjectStatus(ctx context.Context, requestID, projectID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolProjectV2, "status", projectID, struct{}{})
}

func (client *AgentClientV2) ProjectHistory(ctx context.Context, requestID, projectID string, afterSequence, limit uint64) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolProjectV2, "history", projectID, struct {
		AfterSequence uint64 `json:"after_sequence,omitempty"`
		Limit         uint64 `json:"limit,omitempty"`
	}{afterSequence, limit})
}

func (client *AgentClientV2) PlanEdit(ctx context.Context, requestID, projectID string, transaction TransactionV2) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolEditV2, "plan", projectID, struct {
		Transaction TransactionV2 `json:"transaction"`
	}{transaction})
}

func (client *AgentClientV2) ApplyEdit(ctx context.Context, requestID, projectID, transactionID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolEditV2, "apply", projectID, map[string]string{"transaction_id": transactionID})
}

func (client *AgentClientV2) DiscardEdit(ctx context.Context, requestID, projectID, transactionID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolEditV2, "discard", projectID, map[string]string{"transaction_id": transactionID})
}

func (client *AgentClientV2) RevertEdit(ctx context.Context, requestID, projectID, targetTransactionID, transactionID, idempotencyKey string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolEditV2, "revert", projectID, map[string]string{
		"target_transaction_id": targetTransactionID,
		"transaction_id":        transactionID,
		"idempotency_key":       idempotencyKey,
	})
}

func (client *AgentClientV2) RenderCapabilities(ctx context.Context, requestID, projectID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolProjectV2, "capabilities", projectID, struct{}{})
}

func (client *AgentClientV2) PlanRender(ctx context.Context, requestID, projectID string, renderRange TimeRange, target RenderTargetV3) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "plan", projectID, struct {
		Range  TimeRange      `json:"range"`
		Target RenderTargetV3 `json:"target"`
	}{renderRange, target})
}

func (client *AgentClientV2) PrepareRender(ctx context.Context, requestID, projectID string, environment PreparedEnvironmentV2) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "prepare", projectID, struct {
		Environment PreparedEnvironmentV2 `json:"environment"`
	}{environment})
}

func (client *AgentClientV2) PreflightRender(ctx context.Context, requestID, projectID, planFingerprint, executionManifestFingerprint string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "preflight", projectID, struct {
		PlanFingerprint              string `json:"plan_fingerprint"`
		ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	}{planFingerprint, executionManifestFingerprint})
}

func (client *AgentClientV2) RenderFrame(ctx context.Context, requestID, projectID, planFingerprint, executionManifestFingerprint string, compositionFrameIndex int64) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "frame", projectID, struct {
		Profile                      string `json:"profile"`
		PlanFingerprint              string `json:"plan_fingerprint"`
		ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
		CompositionFrameIndex        int64  `json:"composition_frame_index"`
	}{FramePreviewProfileV2, planFingerprint, executionManifestFingerprint, compositionFrameIndex})
}

func (client *AgentClientV2) SubmitRender(ctx context.Context, requestID, projectID string, job RenderJobV2) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "submit", projectID, struct {
		Job RenderJobV2 `json:"job"`
	}{job})
}

func (client *AgentClientV2) RenderStatus(ctx context.Context, requestID, projectID, jobID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "status", projectID, map[string]string{"job_id": jobID})
}

func (client *AgentClientV2) RenderHistory(ctx context.Context, requestID, projectID, jobID string, afterSequence, limit uint64) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "history", projectID, struct {
		JobID         string `json:"job_id"`
		AfterSequence uint64 `json:"after_sequence,omitempty"`
		Limit         uint64 `json:"limit,omitempty"`
	}{jobID, afterSequence, limit})
}

func (client *AgentClientV2) CancelRender(ctx context.Context, requestID, projectID, jobID string) (AgentResultV2, error) {
	return client.Invoke(ctx, requestID, AgentToolRenderV2, "cancel", projectID, map[string]string{"job_id": jobID})
}
