package local

import (
	"errors"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	DocumentName = "project.cutly.json"
	StateVersion = 1
)

var (
	ErrInvalid         = errors.New("local project: invalid project state")
	ErrBusy            = errors.New("local project: another process holds the project lock")
	ErrUnsupported     = errors.New("local project: platform locking is unsupported")
	ErrLimit           = errors.New("local project: resource limit exceeded")
	ErrCommitUnknown   = errors.New("local project: durable commit outcome is unknown; reopen and retry")
	ErrProjectExists   = errors.New("local project: project already exists")
	ErrProjectNotFound = errors.New("local project: project document does not exist")
	ErrServiceClosed   = errors.New("local project: service is closed")
)

const (
	CodePlanNotFound       = "PROJECT.PLAN_NOT_FOUND"
	CodePlanConflict       = "PROJECT.PLAN_CONFLICT"
	CodePlanDiscarded      = "PROJECT.PLAN_DISCARDED"
	CodePlanAlreadyApplied = "PROJECT.PLAN_ALREADY_APPLIED"
	CodeRevisionConflict   = "PROJECT.REVISION_CONFLICT"
	CodeBaseChanged        = "PROJECT.BASE_CHANGED"
	CodeHistoryNotFound    = "PROJECT.HISTORY_NOT_FOUND"
	CodeAlreadyReverted    = "PROJECT.ALREADY_REVERTED"
	CodeResourceLimit      = "PROJECT.RESOURCE_LIMIT"
)

type Limits struct {
	MaxDocumentBytes    uint64
	MaxTransactionBytes uint64
	MaxStateBytes       uint64
	MaxIntentBytes      uint64
	MaxIdentityBytes    uint64
	MaxPlans            uint64
	MaxHistory          uint64
	MaxOperations       uint64
}

var DefaultLimits = Limits{
	MaxDocumentBytes:    16 << 20,
	MaxTransactionBytes: 4 << 20,
	MaxStateBytes:       32 << 20,
	MaxIntentBytes:      64 << 20,
	MaxIdentityBytes:    4 << 10,
	MaxPlans:            4096,
	MaxHistory:          4096,
	MaxOperations:       65_536,
}

type Options struct {
	Limits Limits
}

type PlanState string

const (
	PlanPending   PlanState = "pending"
	PlanApplied   PlanState = "applied"
	PlanDiscarded PlanState = "discarded"
)

type PlanSummary struct {
	TransactionID  string             `json:"transaction_id"`
	State          PlanState          `json:"state"`
	BaseRevision   videoedit.Revision `json:"base_revision"`
	NewRevision    videoedit.Revision `json:"new_revision"`
	BaseHash       string             `json:"base_hash"`
	CandidateHash  string             `json:"candidate_hash"`
	OperationCount uint64             `json:"operation_count"`
}

type HistoryRecord struct {
	Sequence             uint64                 `json:"sequence"`
	BaseRevision         videoedit.Revision     `json:"base_revision"`
	BaseDocumentHash     string                 `json:"base_document_hash"`
	Transaction          videoedit.Transaction  `json:"transaction"`
	Commit               videoedit.CommitRecord `json:"commit"`
	RevertsTransactionID string                 `json:"reverts_transaction_id,omitempty"`
}

type Snapshot struct {
	Document      videoedit.Document `json:"document"`
	DocumentHash  string             `json:"document_hash"`
	StateSequence uint64             `json:"state_sequence"`
	PendingPlans  []PlanSummary      `json:"pending_plans"`
	HistoryCount  uint64             `json:"history_count"`
}

type Status struct {
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

type MutationResult struct {
	OK                   bool                   `json:"ok"`
	Action               string                 `json:"action"`
	State                string                 `json:"state"`
	TransactionID        string                 `json:"transaction_id,omitempty"`
	RevertsTransactionID string                 `json:"reverts_transaction_id,omitempty"`
	BaseRevision         videoedit.Revision     `json:"base_revision"`
	NewRevision          *videoedit.Revision    `json:"new_revision,omitempty"`
	BaseDocumentHash     string                 `json:"base_document_hash,omitempty"`
	NewDocumentHash      string                 `json:"new_document_hash,omitempty"`
	Changed              bool                   `json:"changed"`
	Replayed             bool                   `json:"replayed"`
	RenderRequired       bool                   `json:"render_required"`
	Diagnostics          []videoedit.Diagnostic `json:"diagnostics"`
	Plan                 *PlanSummary           `json:"plan,omitempty"`
	Commit               *HistoryRecord         `json:"commit,omitempty"`
}
