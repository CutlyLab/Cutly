// Package localv2 provides a standalone, durable Document v2 editing store.
// It does not adopt v1 project metadata or invoke a renderer.
package localv2

import (
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/transactionv2"
)

const (
	DocumentName   = "project.cutly.json"
	Version        = 2
	MaxHistoryPage = 1024
)

var (
	ErrInvalid             = errors.New("localv2: invalid project or record")
	ErrLimit               = errors.New("localv2: resource limit exceeded")
	ErrBusy                = errors.New("localv2: project locked")
	ErrUnsupported         = errors.New("localv2: safe filesystem locking is unsupported")
	ErrProjectExists       = errors.New("localv2: project already exists or has legacy metadata")
	ErrProjectNotFound     = errors.New("localv2: initialized v2 project not found")
	ErrConflict            = errors.New("localv2: revision, hash, identity or postimage conflict")
	ErrPlanNotFound        = errors.New("localv2: persisted transaction not found")
	ErrDiscarded           = errors.New("localv2: transaction discarded")
	ErrCommitUnknown       = errors.New("localv2: durable intent published; reopen to determine commit outcome")
	ErrDurabilityUncertain = errors.New("localv2: publication durability is uncertain")
	ErrPanic               = errors.New("localv2: operation panicked")
)

// Limits are hard ceilings; zero Options selects DefaultLimits. Partial limits
// are rejected. Limits cannot exceed the defaults, keeping wire admission bounded.
type Limits struct {
	MaxDocumentBytes    uint64
	MaxTransactionBytes uint64
	MaxRecordBytes      uint64
	MaxStateBytes       uint64
	MaxPlans            uint64
	MaxHistory          uint64
	MaxOperations       uint64
	MaxIdentityBytes    uint64
}

// DefaultLimits returns a copy; callers cannot widen the package's hard ceilings.
// Transaction operation and identity ceilings match transactionv2.
func DefaultLimits() Limits {
	return Limits{128 << 20, 16 << 20, 40 << 20, 8 << 20, 4096, 4096, transactionv2.MaxOperations, transactionv2.MaxIDBytes}
}

type Options struct{ Limits Limits }

type PlanState string

const (
	PlanPending   PlanState = "pending"
	PlanApplied   PlanState = "applied"
	PlanDiscarded PlanState = "discarded"
)

// PlanRecord is immutable. Hashes are sha256 of number-preserving canonical
// bytes, not signatures or authorization against an owner who controls the root.
type PlanRecord struct {
	Version               int                        `json:"version"`
	Transaction           transactionv2.Transaction  `json:"transaction"`
	Digest                string                     `json:"digest"`
	BaseDocumentHash      string                     `json:"base_document_hash"`
	CandidateDocumentHash string                     `json:"candidate_document_hash"`
	Inverse               transactionv2.Transaction  `json:"inverse"`
	Impact                transactionv2.RenderImpact `json:"impact"`
	RevertsTransactionID  string                     `json:"reverts_transaction_id,omitempty"`
}

type PlanSummary struct {
	TransactionID         string              `json:"transaction_id"`
	IdempotencyKey        string              `json:"idempotency_key"`
	Digest                string              `json:"digest"`
	RecordHash            string              `json:"record_hash"`
	State                 PlanState           `json:"state"`
	BaseRevision          documentv2.Revision `json:"base_revision"`
	NewRevision           documentv2.Revision `json:"new_revision"`
	BaseDocumentHash      string              `json:"base_document_hash"`
	CandidateDocumentHash string              `json:"candidate_document_hash"`
}

// CommitRecord is an immutable history entry. Revert appends another entry and
// never edits the target record or restores an old whole-document snapshot.
type CommitRecord struct {
	Version              int                        `json:"version"`
	Sequence             uint64                     `json:"sequence"`
	PlanHash             string                     `json:"plan_hash"`
	TransactionID        string                     `json:"transaction_id"`
	IdempotencyKey       string                     `json:"idempotency_key"`
	Digest               string                     `json:"digest"`
	BaseRevision         documentv2.Revision        `json:"base_revision"`
	NewRevision          documentv2.Revision        `json:"new_revision"`
	BaseDocumentHash     string                     `json:"base_document_hash"`
	NewDocumentHash      string                     `json:"new_document_hash"`
	Inverse              transactionv2.Transaction  `json:"inverse"`
	Impact               transactionv2.RenderImpact `json:"impact"`
	RevertsTransactionID string                     `json:"reverts_transaction_id,omitempty"`
}

type Snapshot struct {
	Document      documentv2.Document `json:"document"`
	DocumentHash  string              `json:"document_hash"`
	StateSequence uint64              `json:"state_sequence"`
	Plans         []PlanSummary       `json:"plans"`
	HistoryCount  uint64              `json:"history_count"`
}

type Status struct {
	DocumentID     documentv2.ID       `json:"document_id"`
	Revision       documentv2.Revision `json:"revision"`
	DocumentHash   string              `json:"document_hash"`
	StateSequence  uint64              `json:"state_sequence"`
	PendingPlans   uint64              `json:"pending_plans"`
	AppliedPlans   uint64              `json:"applied_plans"`
	DiscardedPlans uint64              `json:"discarded_plans"`
	HistoryCount   uint64              `json:"history_count"`
}

type MutationResult struct {
	OK            bool                    `json:"ok"`
	Action        string                  `json:"action"`
	TransactionID string                  `json:"transaction_id"`
	Changed       bool                    `json:"changed"`
	Replayed      bool                    `json:"replayed"`
	Diagnostics   []documentv2.Diagnostic `json:"diagnostics,omitempty"`
	Plan          *PlanSummary            `json:"plan,omitempty"`
	Commit        *CommitRecord           `json:"commit,omitempty"`
}

type diskState struct {
	Version          int                 `json:"version"`
	Sequence         uint64              `json:"sequence"`
	DocumentID       documentv2.ID       `json:"document_id"`
	Revision         documentv2.Revision `json:"revision"`
	DocumentHash     string              `json:"document_hash"`
	LatestCommitHash string              `json:"latest_commit_hash,omitempty"`
	Plans            []PlanSummary       `json:"plans"`
	History          []historyRef        `json:"history"`
}

type historyRef struct {
	TransactionID  string `json:"transaction_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"digest"`
	RecordHash     string `json:"record_hash"`
	Sequence       uint64 `json:"sequence"`
}

type commitIntent struct {
	Version         int    `json:"version"`
	OldStateHash    string `json:"old_state_hash"`
	NewStateHash    string `json:"new_state_hash"`
	OldDocumentHash string `json:"old_document_hash"`
	NewDocumentHash string `json:"new_document_hash"`
	OldCommitHash   string `json:"old_commit_hash"`
	NewCommitHash   string `json:"new_commit_hash"`
}
