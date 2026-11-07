// Package renderprepare exposes the narrow Agent action that turns a frozen
// semantic RenderPlan and host-authorized file bindings into an
// environment-specific ExecutionManifest.
package renderprepare

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

	"github.com/CutlyLab/Cutly/agent/ffmpeghost"
	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	ReceiptProfile = "cutly.agent-render-prepare.v1"

	CodePayloadInvalid  = "RENDER.PAYLOAD_INVALID"
	CodeProjectMismatch = "RENDER.PROJECT_MISMATCH"
	CodePrepareRejected = "RENDER.PREPARE_REJECTED"

	maxDiagnosticMessageBytes = 32 << 10
)

var ErrConfiguration = errors.New("renderprepare: invalid configuration")

// Preparer is deliberately narrower than a render backend. Implementations
// own every executable/runtime/output choice and accept only semantic input
// plus local file bindings.
type Preparer interface {
	PrepareManifest(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error)
}

type PreparerFunc func(context.Context, string, ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error)

func (function PreparerFunc) PrepareManifest(ctx context.Context, projectID string, request ffmpeghost.PrepareRequest) (renderapi.ExecutionManifest, error) {
	return function(ctx, projectID, request)
}

type Config struct {
	ProjectID string
	Preparer  Preparer
}

type Service struct {
	projectID string
	preparer  Preparer
}

// Receipt is the typed canonical result of prepare. The manifest itself binds
// the plan, host runtime, resources, output profile, and deterministic seed.
type Receipt struct {
	Profile           string                      `json:"profile"`
	ExecutionManifest renderapi.ExecutionManifest `json:"execution_manifest"`
}

func New(config Config) (*Service, error) {
	if !validProjectID(config.ProjectID) || nilLike(config.Preparer) {
		return nil, fmt.Errorf("%w: a fixed project ID and host preparer are required", ErrConfiguration)
	}
	return &Service{projectID: config.ProjectID, preparer: config.Preparer}, nil
}

func (service *Service) Routes() []router.Route {
	if service == nil {
		return nil
	}
	return []router.Route{{
		Tool: agentprotocol.ToolRender, Action: "prepare", Handler: router.ActionFunc(service.prepare),
	}}
}

type payload struct {
	Plan              json.RawMessage   `json:"plan"`
	Resources         []resourceBinding `json:"resources"`
	DeterministicSeed optionalInt64     `json:"deterministic_seed,omitempty"`
}

type resourceBinding struct {
	AssetID videoedit.ID `json:"asset_id"`
	Locator string       `json:"locator"`
}

type optionalInt64 struct {
	value int64
}

func (value *optionalInt64) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		return errors.New("deterministic_seed must be an integer when present")
	}
	var decoded int64
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value.value = decoded
	return nil
}

func (service *Service) prepare(ctx context.Context, request agentprotocol.Request) (router.Outcome, error) {
	if ctx == nil {
		return router.Outcome{}, errors.New("renderprepare: context is required")
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	if service == nil || nilLike(service.preparer) {
		return reject(CodePrepareRejected, "/host", "host render preparation is unavailable"), nil
	}
	if request.ProjectID != service.projectID {
		return reject(CodeProjectMismatch, "/project_id", "request project does not match the configured project scope"), nil
	}

	var input payload
	if err := decodePayload(request.Payload, &input); err != nil || len(input.Plan) == 0 || input.Resources == nil || len(input.Resources) > agentprotocol.MaxListItems {
		return reject(CodePayloadInvalid, "/payload", payloadError("prepare requires plan and a bounded resources array", err)), nil
	}
	plan, err := videoedit.DecodeRenderPlan(input.Plan)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/plan", boundedMessage(err)), nil
	}
	expectedLocators, err := validateResourceMapping(plan, input.Resources)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/resources", "resources must map exactly once to every planned asset"), nil
	}
	preparerPlan, err := clonePlan(plan)
	if err != nil {
		return reject(CodePayloadInvalid, "/payload/plan", boundedMessage(err)), nil
	}
	resources := make([]ffmpeghost.PrepareResource, len(input.Resources))
	for index, resource := range input.Resources {
		resources[index] = ffmpeghost.PrepareResource{AssetID: resource.AssetID, Locator: resource.Locator}
	}
	seed := input.DeterministicSeed.value

	manifest, err := safePrepare(ctx, service.preparer, request.ProjectID, ffmpeghost.PrepareRequest{
		Plan: preparerPlan, Resources: resources, DeterministicSeed: seed,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return router.Outcome{}, ctxErr
		}
		return reject(CodePrepareRejected, "/host", "host could not prepare the execution manifest"), nil
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	frozen, err := verifyPreparedManifest(plan, manifest, seed, expectedLocators)
	if err != nil {
		return reject(CodePrepareRejected, "/host", "host returned an invalid execution manifest"), nil
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	receipt, err := encodeReceipt(Receipt{Profile: ReceiptProfile, ExecutionManifest: frozen})
	if err != nil {
		return router.Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return router.Outcome{}, err
	}
	return router.Outcome{
		OK: true, State: "prepared", RenderRequired: true,
		Diagnostics: []videoedit.Diagnostic{}, Receipt: receipt,
		NextActions: []agentprotocol.NextAction{{
			Tool: agentprotocol.ToolRender, Action: "preflight", ProjectID: request.ProjectID,
		}},
	}, nil
}

func verifyPreparedManifest(plan videoedit.RenderPlan, supplied renderapi.ExecutionManifest, seed int64, expectedLocators map[videoedit.ID]string) (renderapi.ExecutionManifest, error) {
	raw, err := canonicaljson.Marshal(supplied)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	decoded, err := renderapi.DecodeExecutionManifest(raw)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	if decoded.DeterministicSeed != seed {
		return renderapi.ExecutionManifest{}, errors.New("deterministic seed mismatch")
	}
	if len(decoded.Resources) != len(expectedLocators) {
		return renderapi.ExecutionManifest{}, errors.New("resource locator mapping mismatch")
	}
	for _, resource := range decoded.Resources {
		locator, exists := expectedLocators[resource.AssetID]
		if !exists || resource.LocatorScheme != renderapi.LocatorFile || resource.Locator != locator {
			return renderapi.ExecutionManifest{}, errors.New("resource locator mapping mismatch")
		}
	}
	refrozen, err := renderapi.FreezeExecutionManifest(plan, decoded)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	left, err := canonicaljson.Marshal(decoded)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	right, err := canonicaljson.Marshal(refrozen)
	if err != nil {
		return renderapi.ExecutionManifest{}, err
	}
	if !bytes.Equal(left, right) {
		return renderapi.ExecutionManifest{}, errors.New("manifest is not the exact frozen value for the supplied plan")
	}
	return decoded, nil
}

func validateResourceMapping(plan videoedit.RenderPlan, resources []resourceBinding) (map[videoedit.ID]string, error) {
	planned := make(map[videoedit.ID]struct{}, len(plan.Resources))
	for _, resource := range plan.Resources {
		planned[resource.AssetID] = struct{}{}
	}
	locators := make(map[videoedit.ID]string, len(resources))
	for _, resource := range resources {
		if _, duplicate := locators[resource.AssetID]; duplicate {
			return nil, errors.New("duplicate resource mapping")
		}
		if _, exists := planned[resource.AssetID]; !exists {
			return nil, errors.New("unknown resource mapping")
		}
		locators[resource.AssetID] = resource.Locator
	}
	if len(locators) != len(planned) {
		return nil, errors.New("incomplete resource mapping")
	}
	return locators, nil
}

func clonePlan(plan videoedit.RenderPlan) (videoedit.RenderPlan, error) {
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

func encodeReceipt(value Receipt) (json.RawMessage, error) {
	encoded, err := canonicaljson.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("renderprepare: encode receipt: %w", err)
	}
	if len(encoded) > agentprotocol.MaxResultBytes-(1<<20) {
		return nil, errors.New("renderprepare: receipt exceeds Agent result budget")
	}
	return encoded, nil
}

func reject(code, path, message string) router.Outcome {
	return router.Outcome{
		OK: false, State: "rejected",
		Diagnostics: []videoedit.Diagnostic{{
			Code: code, Severity: videoedit.SeverityError, Path: path,
			Message: boundedMessage(errors.New(message)),
		}},
		NextActions: []agentprotocol.NextAction{},
	}
}

func payloadError(fallback string, err error) string {
	if err == nil {
		return fallback
	}
	return fallback + ": " + err.Error()
}

func boundedMessage(err error) string {
	const fallback = "render prepare failed"
	if err == nil {
		return fallback
	}
	message := strings.ToValidUTF8(strings.TrimSpace(err.Error()), "\uFFFD")
	if message == "" {
		return fallback
	}
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

func safePrepare(ctx context.Context, preparer Preparer, projectID string, request ffmpeghost.PrepareRequest) (_ renderapi.ExecutionManifest, returnErr error) {
	defer func() {
		if recover() != nil {
			returnErr = errors.New("renderprepare: host preparer panicked")
		}
	}()
	return preparer.PrepareManifest(ctx, projectID, request)
}

func validProjectID(value string) bool {
	return value != "" && len(value) <= agentprotocol.MaxIdentityBytes && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= agentprotocol.MaxIdentityScalars && strings.Trim(value, " \t\r\n") != ""
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
