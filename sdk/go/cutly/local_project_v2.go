package cutly

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/project/localv2"
)

type LocalProjectSnapshotV2 = localv2.Snapshot
type LocalProjectStatusV2 = localv2.Status
type LocalProjectPlanStateV2 = localv2.PlanState
type LocalProjectPlanSummaryV2 = localv2.PlanSummary
type LocalProjectCommitRecordV2 = localv2.CommitRecord
type LocalProjectMutationResultV2 = localv2.MutationResult

const (
	LocalProjectPlanPendingV2   = localv2.PlanPending
	LocalProjectPlanAppliedV2   = localv2.PlanApplied
	LocalProjectPlanDiscardedV2 = localv2.PlanDiscarded
	MaxLocalProjectHistoryV2    = localv2.MaxHistoryPage
)

var (
	ErrLocalProjectV2Invalid             = localv2.ErrInvalid
	ErrLocalProjectV2Limit               = localv2.ErrLimit
	ErrLocalProjectV2Busy                = localv2.ErrBusy
	ErrLocalProjectV2Unsupported         = localv2.ErrUnsupported
	ErrLocalProjectV2Exists              = localv2.ErrProjectExists
	ErrLocalProjectV2NotFound            = localv2.ErrProjectNotFound
	ErrLocalProjectV2Conflict            = localv2.ErrConflict
	ErrLocalProjectV2PlanNotFound        = localv2.ErrPlanNotFound
	ErrLocalProjectV2Discarded           = localv2.ErrDiscarded
	ErrLocalProjectV2CommitUnknown       = localv2.ErrCommitUnknown
	ErrLocalProjectV2DurabilityUncertain = localv2.ErrDurabilityUncertain
	ErrLocalProjectV2Unavailable         = errors.New("cutly sdk: local Document v2 project is unavailable")
)

// LocalProjectV2 is the public durable Document v2 project handle. Its methods
// delegate directly to the hardened v2 store and never translate through v1.
type LocalProjectV2 struct {
	service *localv2.Service
}

func CreateLocalProjectV2(ctx context.Context, dir string, document Document) (*LocalProjectV2, error) {
	service, err := localv2.Create(ctx, dir, document, localv2.Options{})
	if err != nil {
		return nil, err
	}
	return &LocalProjectV2{service: service}, nil
}

func OpenLocalProjectV2(dir string) (*LocalProjectV2, error) {
	service, err := localv2.New(dir, localv2.Options{})
	if err != nil {
		return nil, err
	}
	return &LocalProjectV2{service: service}, nil
}

func (project *LocalProjectV2) Inspect(ctx context.Context) (LocalProjectSnapshotV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectSnapshotV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Inspect(ctx)
}

func (project *LocalProjectV2) Status(ctx context.Context) (LocalProjectStatusV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectStatusV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Status(ctx)
}

func (project *LocalProjectV2) History(ctx context.Context, afterSequence, limit uint64) ([]LocalProjectCommitRecordV2, error) {
	if project == nil || project.service == nil {
		return nil, ErrLocalProjectV2Unavailable
	}
	return project.service.History(ctx, afterSequence, limit)
}

func (project *LocalProjectV2) Plan(ctx context.Context, transaction TransactionV2) (LocalProjectMutationResultV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectMutationResultV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Plan(ctx, transaction)
}

func (project *LocalProjectV2) Apply(ctx context.Context, transactionID string) (LocalProjectMutationResultV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectMutationResultV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Apply(ctx, transactionID)
}

func (project *LocalProjectV2) Discard(ctx context.Context, transactionID string) (LocalProjectMutationResultV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectMutationResultV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Discard(ctx, transactionID)
}

func (project *LocalProjectV2) Revert(ctx context.Context, targetTransactionID, transactionID, idempotencyKey string) (LocalProjectMutationResultV2, error) {
	if project == nil || project.service == nil {
		return LocalProjectMutationResultV2{}, ErrLocalProjectV2Unavailable
	}
	return project.service.Revert(ctx, targetTransactionID, transactionID, idempotencyKey)
}
