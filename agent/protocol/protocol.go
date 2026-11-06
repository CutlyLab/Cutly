// Package agentprotocol defines the transport-neutral Agent/CLI envelope shared
// by Cutly project, edit and render tools. Business rejection is represented by
// ok=false plus diagnostics; transport errors remain outside this envelope.
package agentprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const (
	ProtocolVersion    = 1
	MediaType          = "application/cutly-agent+json"
	MaxRequestBytes    = 4 << 20
	MaxResultBytes     = 16 << 20
	MaxIdentityScalars = 4 << 10
	MaxIdentityBytes   = MaxIdentityScalars * 4
	MaxMessageScalars  = 64 << 10
	MaxMessageBytes    = MaxMessageScalars * 4
	MaxListItems       = 4 << 10
)

var (
	ErrRequestInvalid = errors.New("agent protocol: request is invalid")
	ErrResultInvalid  = errors.New("agent protocol: result is invalid")
)

type Tool string

const (
	ToolProject Tool = "cutly_project"
	ToolEdit    Tool = "cutly_edit"
	ToolRender  Tool = "cutly_render"
)

type Request struct {
	ProtocolVersion int             `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Tool            Tool            `json:"tool"`
	Action          string          `json:"action"`
	ProjectID       string          `json:"project_id,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

type NextAction struct {
	Tool      Tool                `json:"tool"`
	Action    string              `json:"action"`
	ProjectID string              `json:"project_id,omitempty"`
	Revision  *videoedit.Revision `json:"revision,omitempty"`
	JobID     string              `json:"job_id,omitempty"`
}

type Result struct {
	ProtocolVersion int                    `json:"protocol_version"`
	OK              bool                   `json:"ok"`
	RequestID       string                 `json:"request_id"`
	Tool            Tool                   `json:"tool"`
	Action          string                 `json:"action"`
	State           string                 `json:"state"`
	ProjectID       string                 `json:"project_id,omitempty"`
	TransactionID   string                 `json:"transaction_id,omitempty"`
	JobID           string                 `json:"job_id,omitempty"`
	BaseRevision    *videoedit.Revision    `json:"base_revision,omitempty"`
	NewRevision     *videoedit.Revision    `json:"new_revision,omitempty"`
	Changed         bool                   `json:"changed"`
	Replayed        bool                   `json:"replayed"`
	RenderRequired  bool                   `json:"render_required"`
	Diagnostics     []videoedit.Diagnostic `json:"diagnostics"`
	Diff            json.RawMessage        `json:"diff,omitempty"`
	Receipt         json.RawMessage        `json:"receipt,omitempty"`
	NextActions     []NextAction           `json:"next_actions"`
}

var actions = map[Tool]map[string]struct{}{
	ToolProject: actionSet("inspect", "status", "history", "capabilities"),
	ToolEdit:    actionSet("plan", "apply", "discard", "revert"),
	ToolRender:  actionSet("plan", "prepare", "preflight", "frame", "render", "status", "cancel"),
}

// ValidContentType reports whether value names an Agent JSON representation.
// The only permitted parameter is one optional, literal profile parameter for
// the canonical integer JSON profile. Raw-form checks intentionally reject
// duplicate parameters, trailing semicolons and RFC 2231 extended parameter
// spellings so every Cutly transport implements the same narrow contract.
func ValidContentType(value string) bool {
	parts := strings.Split(value, ";")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	if mediaType != MediaType && mediaType != "application/json" {
		return false
	}
	if len(parts) == 2 {
		parameter := strings.TrimSpace(parts[1])
		equals := strings.IndexByte(parameter, '=')
		if equals < 0 || !strings.EqualFold(strings.TrimSpace(parameter[:equals]), "profile") {
			return false
		}
		profile := strings.TrimSpace(parameter[equals+1:])
		if profile != canonicaljson.Profile && profile != `"`+canonicaljson.Profile+`"` {
			return false
		}
	}
	return true
}

func DecodeRequest(raw []byte) (Request, error) {
	if len(raw) == 0 || len(raw) > MaxRequestBytes {
		return Request{}, fmt.Errorf("%w: encoded length %d", ErrRequestInvalid, len(raw))
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil {
		return Request{}, fmt.Errorf("%w: canonical profile: %v", ErrRequestInvalid, err)
	}
	var request Request
	if err := wirejson.Decode(canonical, &request); err != nil {
		return Request{}, fmt.Errorf("%w: decode: %v", ErrRequestInvalid, err)
	}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	request.Payload = cloneRaw(request.Payload)
	return request, nil
}

func DecodeResult(raw []byte) (Result, error) {
	if len(raw) == 0 || len(raw) > MaxResultBytes {
		return Result{}, fmt.Errorf("%w: encoded length %d", ErrResultInvalid, len(raw))
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil {
		return Result{}, fmt.Errorf("%w: canonical profile: %v", ErrResultInvalid, err)
	}
	var result Result
	if err := wirejson.Decode(canonical, &result); err != nil {
		return Result{}, fmt.Errorf("%w: decode: %v", ErrResultInvalid, err)
	}
	if err := ValidateResult(result); err != nil {
		return Result{}, err
	}
	result.Diagnostics = cloneDiagnostics(result.Diagnostics)
	result.Diff = cloneRaw(result.Diff)
	result.Receipt = cloneRaw(result.Receipt)
	result.NextActions = cloneNextActions(result.NextActions)
	return result, nil
}

func CanonicalRequest(request Request) ([]byte, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	canonical, err := canonicaljson.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical encode: %v", ErrRequestInvalid, err)
	}
	if len(canonical) > MaxRequestBytes {
		return nil, fmt.Errorf("%w: encoded length %d", ErrRequestInvalid, len(canonical))
	}
	return canonical, nil
}

func CanonicalResult(result Result) ([]byte, error) {
	if err := ValidateResult(result); err != nil {
		return nil, err
	}
	canonical, err := canonicaljson.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical encode: %v", ErrResultInvalid, err)
	}
	if len(canonical) > MaxResultBytes {
		return nil, fmt.Errorf("%w: encoded length %d", ErrResultInvalid, len(canonical))
	}
	return canonical, nil
}

func ValidateResult(result Result) error {
	if result.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: protocol version %d", ErrResultInvalid, result.ProtocolVersion)
	}
	if !validIdentity(result.RequestID) || !validToolAction(result.Tool, result.Action) || !validIdentity(result.State) {
		return fmt.Errorf("%w: request identity, tool/action and state are required", ErrResultInvalid)
	}
	if result.Action != "capabilities" && result.ProjectID == "" {
		return fmt.Errorf("%w: project identity is required for action", ErrResultInvalid)
	}
	if result.ProjectID != "" && !validIdentity(result.ProjectID) {
		return fmt.Errorf("%w: project identity is invalid", ErrResultInvalid)
	}
	if result.TransactionID != "" && (result.Tool != ToolEdit || !validIdentity(result.TransactionID)) {
		return fmt.Errorf("%w: transaction identity is invalid for tool", ErrResultInvalid)
	}
	if result.JobID != "" && (result.Tool != ToolRender || !validIdentity(result.JobID)) {
		return fmt.Errorf("%w: job identity is invalid for tool", ErrResultInvalid)
	}
	if result.Diagnostics == nil || result.NextActions == nil || len(result.Diagnostics) > MaxListItems || len(result.NextActions) > MaxListItems {
		return fmt.Errorf("%w: diagnostics and next_actions must be present arrays", ErrResultInvalid)
	}
	if err := validateDiagnostics(result.Diagnostics); err != nil {
		return fmt.Errorf("%w: %v", ErrResultInvalid, err)
	}
	if result.OK && videoedit.HasErrorDiagnostics(result.Diagnostics) {
		return fmt.Errorf("%w: ok result contains an error diagnostic", ErrResultInvalid)
	}
	if !result.OK && !videoedit.HasErrorDiagnostics(result.Diagnostics) {
		return fmt.Errorf("%w: rejected result requires an error diagnostic", ErrResultInvalid)
	}
	for index, next := range result.NextActions {
		if !validToolAction(next.Tool, next.Action) {
			return fmt.Errorf("%w: next_actions/%d has invalid tool/action", ErrResultInvalid, index)
		}
		if next.Action != "capabilities" && next.ProjectID == "" {
			return fmt.Errorf("%w: next_actions/%d project identity is required", ErrResultInvalid, index)
		}
		if next.ProjectID != "" && !validIdentity(next.ProjectID) {
			return fmt.Errorf("%w: next_actions/%d project identity is invalid", ErrResultInvalid, index)
		}
		if next.JobID != "" && (next.Tool != ToolRender || !validIdentity(next.JobID)) {
			return fmt.Errorf("%w: next_actions/%d job identity is invalid", ErrResultInvalid, index)
		}
	}
	for name, raw := range map[string]json.RawMessage{"diff": result.Diff, "receipt": result.Receipt} {
		if len(raw) == 0 {
			continue
		}
		canonical, err := canonicaljson.CanonicalizeWithLimits(raw, objectLimits(MaxResultBytes))
		if err != nil || len(canonical) == 0 || canonical[0] != '{' {
			return fmt.Errorf("%w: %s must be a canonical-profile JSON object: %v", ErrResultInvalid, name, err)
		}
	}
	return nil
}

func validateRequest(request Request) error {
	if request.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: protocol version %d", ErrRequestInvalid, request.ProtocolVersion)
	}
	if !validIdentity(request.RequestID) || !validToolAction(request.Tool, request.Action) {
		return fmt.Errorf("%w: request identity and supported tool/action are required", ErrRequestInvalid)
	}
	if request.ProjectID != "" && !validIdentity(request.ProjectID) {
		return fmt.Errorf("%w: project identity is invalid", ErrRequestInvalid)
	}
	if request.Action != "capabilities" && request.ProjectID == "" {
		return fmt.Errorf("%w: project_id is required for action %q", ErrRequestInvalid, request.Action)
	}
	canonical, err := canonicaljson.CanonicalizeWithLimits(request.Payload, objectLimits(MaxRequestBytes))
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return fmt.Errorf("%w: payload must be a canonical-profile JSON object: %v", ErrRequestInvalid, err)
	}
	return nil
}

func validateDiagnostics(diagnostics []videoedit.Diagnostic) error {
	for index, diagnostic := range diagnostics {
		if !validIdentity(diagnostic.Code) || !validRequiredText(diagnostic.Message, MaxMessageBytes, MaxMessageScalars) {
			return fmt.Errorf("diagnostic %d code/message is invalid", index)
		}
		if !validOptionalText(diagnostic.Path, MaxIdentityBytes, MaxIdentityScalars) {
			return fmt.Errorf("diagnostic %d path is invalid", index)
		}
		switch diagnostic.Severity {
		case videoedit.SeverityInfo, videoedit.SeverityWarning, videoedit.SeverityError:
		default:
			return fmt.Errorf("diagnostic %d severity is invalid", index)
		}
		if len(diagnostic.Fixes) > MaxListItems {
			return fmt.Errorf("diagnostic %d has too many candidate fixes", index)
		}
		for fixIndex, fix := range diagnostic.Fixes {
			if !validRequiredText(fix.Description, MaxMessageBytes, MaxMessageScalars) || len(fix.SideEffects) > MaxListItems {
				return fmt.Errorf("diagnostic %d fix %d is invalid", index, fixIndex)
			}
			if len(fix.Operation) != 0 {
				canonical, err := canonicaljson.CanonicalizeWithLimits(fix.Operation, objectLimits(MaxResultBytes))
				if err != nil || len(canonical) == 0 || canonical[0] != '{' {
					return fmt.Errorf("diagnostic %d fix %d operation must be a canonical-profile JSON object", index, fixIndex)
				}
			}
			for effectIndex, effect := range fix.SideEffects {
				if !validOptionalText(effect, MaxMessageBytes, MaxMessageScalars) {
					return fmt.Errorf("diagnostic %d fix %d side effect %d is invalid", index, fixIndex, effectIndex)
				}
			}
		}
	}
	return nil
}

func validToolAction(tool Tool, action string) bool {
	available, exists := actions[tool]
	_, supported := available[action]
	return exists && supported
}

func validIdentity(value string) bool {
	return validRequiredText(value, MaxIdentityBytes, MaxIdentityScalars) && strings.Trim(value, " \t\r\n") != ""
}

func validRequiredText(value string, maximumBytes, maximumScalars int) bool {
	return value != "" && validOptionalText(value, maximumBytes, maximumScalars)
}

func validOptionalText(value string, maximumBytes, maximumScalars int) bool {
	return len(value) <= maximumBytes && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximumScalars
}

func objectLimits(maximum int) canonicaljson.Limits {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maximum
	limits.MaxStringBytes = maximum
	return limits
}

func actionSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneNextActions(values []NextAction) []NextAction {
	if values == nil {
		return nil
	}
	result := make([]NextAction, len(values))
	copy(result, values)
	return result
}

func cloneDiagnostics(values []videoedit.Diagnostic) []videoedit.Diagnostic {
	if values == nil {
		return nil
	}
	result := make([]videoedit.Diagnostic, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Fixes = append([]videoedit.CandidateFix(nil), value.Fixes...)
		for fixIndex := range result[index].Fixes {
			result[index].Fixes[fixIndex].Operation = cloneRaw(value.Fixes[fixIndex].Operation)
			result[index].Fixes[fixIndex].SideEffects = append([]string(nil), value.Fixes[fixIndex].SideEffects...)
		}
	}
	return result
}
