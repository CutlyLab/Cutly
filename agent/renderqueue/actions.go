package renderqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

const (
	CodePayloadInvalid     = "RENDER.PAYLOAD_INVALID"
	CodeJobNotFound        = "RENDER.JOB_NOT_FOUND"
	CodeJobConflict        = "RENDER.JOB_CONFLICT"
	CodeTransitionRejected = "RENDER.TRANSITION_REJECTED"
)

type Coordinator interface {
	Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error)
	Snapshot(string) (scheduler.JobSnapshot, error)
	RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error)
}

type Service struct {
	coordinator Coordinator
}

func New(coordinator Coordinator) (*Service, error) {
	if coordinator == nil {
		return nil, errors.New("renderqueue: coordinator is required")
	}
	return &Service{coordinator: coordinator}, nil
}

func (service *Service) Routes() []router.Route {
	if service == nil {
		return nil
	}
	return []router.Route{
		{Tool: agentprotocol.ToolRender, Action: "render", Handler: router.ActionFunc(service.render)},
		{Tool: agentprotocol.ToolRender, Action: "status", Handler: router.ActionFunc(service.status)},
		{Tool: agentprotocol.ToolRender, Action: "cancel", Handler: router.ActionFunc(service.cancel)},
	}
}

type renderPayload struct {
	Job json.RawMessage `json:"job"`
}

type jobPayload struct {
	JobID string `json:"job_id"`
}

func (service *Service) render(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	var payload renderPayload
	if err := decodePayload(request.Payload, &payload); err != nil || len(payload.Job) == 0 {
		return reject(CodePayloadInvalid, "/payload", payloadError("render payload requires exactly one job", err)), nil
	}
	job, err := renderapi.DecodeRenderJob(payload.Job)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/job", err.Error()), nil
	}
	snapshot, created, err := service.coordinator.Submit(ctx, job)
	if err != nil {
		if outcome, ok := schedulerRejection(err); ok {
			return outcome, nil
		}
		return router.Outcome{}, err
	}
	receipt, err := snapshotReceipt(snapshot)
	if err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: string(snapshot.State), JobID: snapshot.Job.ID,
		Changed: created, Replayed: !created, RenderRequired: !terminal(snapshot.State),
		Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: queueNextActions(request.ProjectID, snapshot),
	}, nil
}

func (service *Service) status(_ context.Context, request agentprotocol.Request) (router.Outcome, error) {
	payload, outcome, ok := decodeJobPayload(request.Payload)
	if !ok {
		return outcome, nil
	}
	snapshot, err := service.coordinator.Snapshot(payload.JobID)
	if err != nil {
		if outcome, ok := schedulerRejection(err); ok {
			return outcome, nil
		}
		return router.Outcome{}, err
	}
	receipt, err := snapshotReceipt(snapshot)
	if err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: string(snapshot.State), JobID: snapshot.Job.ID,
		RenderRequired: !terminal(snapshot.State), Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: queueNextActions(request.ProjectID, snapshot),
	}, nil
}

func (service *Service) cancel(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	payload, outcome, ok := decodeJobPayload(request.Payload)
	if !ok {
		return outcome, nil
	}
	snapshot, changed, err := service.coordinator.RequestCancel(ctx, payload.JobID)
	if err != nil {
		if outcome, ok := schedulerRejection(err); ok {
			return outcome, nil
		}
		return router.Outcome{}, err
	}
	receipt, err := snapshotReceipt(snapshot)
	if err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: string(snapshot.State), JobID: snapshot.Job.ID,
		Changed: changed, RenderRequired: false, Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: []agentprotocol.NextAction{{
			Tool: agentprotocol.ToolRender, Action: "status", ProjectID: request.ProjectID, JobID: snapshot.Job.ID,
		}},
	}, nil
}

func decodeJobPayload(raw json.RawMessage) (jobPayload, router.Outcome, bool) {
	var payload jobPayload
	if err := decodePayload(raw, &payload); err != nil || !validJobID(payload.JobID) {
		return jobPayload{}, reject(CodePayloadInvalid, "/payload/job_id", payloadError("job_id is required", err)), false
	}
	return payload, router.Outcome{}, true
}

func decodePayload(raw json.RawMessage, target any) error {
	canonical, err := canonicaljson.CanonicalizeWithLimits(raw, requestLimits())
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

func requestLimits() canonicaljson.Limits {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = agentprotocol.MaxRequestBytes
	limits.MaxStringBytes = agentprotocol.MaxRequestBytes
	return limits
}

func validJobID(value string) bool {
	return strings.Trim(value, " \t\r\n") != "" && len(value) <= scheduler.MaxJobIDBytes
}

func schedulerRejection(err error) (router.Outcome, bool) {
	switch {
	case errors.Is(err, scheduler.ErrJobNotFound):
		return reject(CodeJobNotFound, "/payload/job_id", "render job was not found"), true
	case errors.Is(err, scheduler.ErrJobConflict), errors.Is(err, scheduler.ErrIdempotencyConflict):
		return reject(CodeJobConflict, "/payload/job", err.Error()), true
	case errors.Is(err, scheduler.ErrInvalidTransition), errors.Is(err, scheduler.ErrProtocolInvalid):
		return reject(CodeTransitionRejected, "/payload", err.Error()), true
	default:
		return router.Outcome{}, false
	}
}

func reject(code, path, message string) router.Outcome {
	return router.Outcome{
		OK: false, State: "rejected",
		Diagnostics: []videoedit.Diagnostic{{Code: code, Severity: videoedit.SeverityError, Path: path, Message: message}},
		NextActions: []agentprotocol.NextAction{},
	}
}

func snapshotReceipt(snapshot scheduler.JobSnapshot) (json.RawMessage, error) {
	encoded, err := canonicaljson.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("renderqueue: encode scheduler receipt: %w", err)
	}
	return encoded, nil
}

func queueNextActions(projectID string, snapshot scheduler.JobSnapshot) []agentprotocol.NextAction {
	if terminal(snapshot.State) {
		return []agentprotocol.NextAction{}
	}
	actions := []agentprotocol.NextAction{{
		Tool: agentprotocol.ToolRender, Action: "status", ProjectID: projectID, JobID: snapshot.Job.ID,
	}}
	if snapshot.State == renderapi.JobQueued || snapshot.State == renderapi.JobRunning {
		actions = append(actions, agentprotocol.NextAction{
			Tool: agentprotocol.ToolRender, Action: "cancel", ProjectID: projectID, JobID: snapshot.Job.ID,
		})
	}
	return actions
}

func terminal(state renderapi.JobState) bool {
	return state == renderapi.JobComplete || state == renderapi.JobFailed || state == renderapi.JobCanceled
}

func payloadError(fallback string, err error) string {
	if err == nil {
		return fallback
	}
	return fallback + ": " + err.Error()
}
