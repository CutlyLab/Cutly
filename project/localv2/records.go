package localv2

import (
	"context"
	"fmt"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/transactionv2"
)

// EncodePlanRecord encodes the strict number-preserving v2 record shape. A
// record is not authorization: the store also recomputes against its snapshots.
func EncodePlanRecord(record PlanRecord) ([]byte, error) {
	if err := validatePlanRecord(record); err != nil {
		return nil, err
	}
	return canonical(record, DefaultLimits().MaxRecordBytes)
}
func DecodePlanRecord(raw []byte) (PlanRecord, error) {
	var r PlanRecord
	if err := decodeCanonical(raw, &r, DefaultLimits().MaxRecordBytes); err != nil {
		return PlanRecord{}, err
	}
	if err := validatePlanRecord(r); err != nil {
		return PlanRecord{}, err
	}
	return r, nil
}
func validatePlanRecord(r PlanRecord) error {
	if err := validateImpact(r.Impact); err != nil {
		return err
	}
	if r.Version != Version || !validHash(r.Digest) || !validHash(r.BaseDocumentHash) || !validHash(r.CandidateDocumentHash) {
		return ErrInvalid
	}
	if uint64(len(r.Transaction.Operations)) > DefaultLimits().MaxOperations || uint64(len(r.Inverse.Operations)) > DefaultLimits().MaxOperations {
		return ErrLimit
	}
	if ds := transactionv2.Validate(r.Transaction); documentv2.HasErrorDiagnostics(ds) {
		return ErrInvalid
	}
	if ds := transactionv2.Validate(r.Inverse); documentv2.HasErrorDiagnostics(ds) {
		return ErrInvalid
	}
	hash, err := transactionv2.Digest(r.Transaction)
	if err != nil || hash != r.Digest || r.Transaction.ExpectedDocumentHash != r.BaseDocumentHash {
		return ErrInvalid
	}
	if r.Inverse.DocumentID != r.Transaction.DocumentID || r.Transaction.ExpectedRevision == ^documentv2.Revision(0) || r.Inverse.ExpectedRevision != r.Transaction.ExpectedRevision+1 || r.Inverse.ExpectedDocumentHash != r.CandidateDocumentHash {
		return ErrInvalid
	}
	if r.RevertsTransactionID != "" && !identity(r.RevertsTransactionID, DefaultLimits().MaxIdentityBytes) {
		return ErrInvalid
	}
	return nil
}

// EncodeCommitRecord and DecodeCommitRecord check shape and intrinsic bindings;
// only a Service can verify the plan/base/candidate and complete history chain.
func EncodeCommitRecord(record CommitRecord) ([]byte, error) {
	if err := validateCommitRecord(record); err != nil {
		return nil, err
	}
	return canonical(record, DefaultLimits().MaxRecordBytes)
}
func DecodeCommitRecord(raw []byte) (CommitRecord, error) {
	var r CommitRecord
	if err := decodeCanonical(raw, &r, DefaultLimits().MaxRecordBytes); err != nil {
		return CommitRecord{}, err
	}
	if err := validateCommitRecord(r); err != nil {
		return CommitRecord{}, err
	}
	return r, nil
}
func validateCommitRecord(c CommitRecord) error {
	if err := validateImpact(c.Impact); err != nil {
		return err
	}
	if c.RevertsTransactionID != "" && !identity(c.RevertsTransactionID, DefaultLimits().MaxIdentityBytes) {
		return ErrInvalid
	}
	if c.Version != Version || c.Sequence == 0 || !validHash(c.PlanHash) || !validHash(c.Digest) || !validHash(c.BaseDocumentHash) || !validHash(c.NewDocumentHash) || !identity(c.TransactionID, DefaultLimits().MaxIdentityBytes) || !identity(c.IdempotencyKey, DefaultLimits().MaxIdentityBytes) || c.BaseRevision == ^documentv2.Revision(0) || c.NewRevision != c.BaseRevision+1 {
		return ErrInvalid
	}
	if ds := transactionv2.Validate(c.Inverse); documentv2.HasErrorDiagnostics(ds) {
		return ErrInvalid
	}
	if c.Inverse.ExpectedRevision != c.NewRevision || c.Inverse.ExpectedDocumentHash != c.NewDocumentHash {
		return ErrInvalid
	}
	return nil
}

func validateImpact(impact transactionv2.RenderImpact) error {
	if !impact.IdentityChanged || impact.RenderRequired != (impact.Visual || impact.Audio) || impact.TimelineIDs == nil || len(impact.TimelineIDs) > 2_000_000 {
		return ErrInvalid
	}
	var previous documentv2.ID
	for i, id := range impact.TimelineIDs {
		if !identity(string(id), DefaultLimits().MaxIdentityBytes) || i > 0 && id <= previous {
			return ErrInvalid
		}
		previous = id
	}
	return nil
}

// validateRecords loads only one bounded plan/base/candidate at a time. The
// index contains no full candidate documents. Checksums alone cannot validate
// a forged inverse: replay through the typed core checks semantics as well.
func (x *session) validateRecords(ctx context.Context, state diskState) error {
	saved := x.state
	x.state = state
	defer func() { x.state = saved }()
	for _, p := range state.Plans {
		r, err := x.readPlan(ctx, p, true)
		if err != nil {
			return err
		}
		if err := validatePlanRecord(r); err != nil {
			return err
		}
		if r.RevertsTransactionID != "" {
			h := findHistory(state, r.RevertsTransactionID)
			if h == nil {
				return ErrInvalid
			}
			if applied := findHistory(state, p.TransactionID); applied != nil && h.Sequence >= applied.Sequence {
				return ErrInvalid
			}
			target, err := x.readCommit(ctx, *h)
			if err != nil {
				return err
			}
			baseRaw, err := x.object(ctx, "documents", r.BaseDocumentHash, x.service.limits.MaxDocumentBytes)
			if err != nil {
				return err
			}
			base, err := decodeDocument(baseRaw, x.service.limits.MaxDocumentBytes)
			if err != nil {
				return err
			}
			inverse := target.Inverse
			inverse.ID = r.Transaction.ID
			inverse.IdempotencyKey = r.Transaction.IdempotencyKey
			expected, err := transactionv2.Create(base, inverse)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(expected, r.Transaction) {
				return fmt.Errorf("%w: revert does not use historical inverse", ErrInvalid)
			}
		}
	}
	var previous *CommitRecord
	for _, h := range state.History {
		c, err := x.readCommit(ctx, h)
		if err != nil {
			return err
		}
		if err := validateCommitRecord(c); err != nil {
			return err
		}
		if previous != nil && (c.BaseDocumentHash != previous.NewDocumentHash || c.BaseRevision != previous.NewRevision) {
			return fmt.Errorf("%w: discontinuous history", ErrInvalid)
		}
		copy := c
		previous = &copy
	}
	return nil
}

// Validate an exact logical transition, not just independent hash memberships.
// A forged, self-consistent root remains owner-controlled data, not authority.
func (x *session) validateTransition(ctx context.Context, old, next diskState, initial bool) error {
	if initial {
		if next.Sequence != 0 || len(next.Plans) != 0 || len(next.History) != 0 || next.LatestCommitHash != "" {
			return ErrInvalid
		}
		return nil
	}
	if old.Sequence == ^uint64(0) || next.Sequence != old.Sequence+1 || old.DocumentID != next.DocumentID {
		return ErrInvalid
	}
	if len(next.Plans) < len(old.Plans) || len(next.Plans) > len(old.Plans)+1 || len(next.History) < len(old.History) || len(next.History) > len(old.History)+1 {
		return ErrInvalid
	}
	for i := range old.History {
		if old.History[i] != next.History[i] {
			return ErrInvalid
		}
	}
	added := 0
	changes := 0
	var changed *PlanSummary
	for _, p := range next.Plans {
		before := findPlan(old, p.TransactionID)
		if before == nil {
			added++
			copy := p
			changed = &copy
			continue
		}
		if *before != p {
			copy := p
			copy.State = before.State
			if copy != *before || before.State != PlanPending || (p.State != PlanApplied && p.State != PlanDiscarded) {
				return ErrInvalid
			}
			changes++
			copy = p
			changed = &copy
		}
	}
	if added+changes != 1 {
		return ErrInvalid
	}
	committed := len(next.History) == len(old.History)+1
	if committed {
		h := next.History[len(next.History)-1]
		if changed == nil || changed.State != PlanApplied || h.TransactionID != changed.TransactionID || h.Sequence != next.Sequence || changed.BaseRevision != old.Revision || changed.BaseDocumentHash != old.DocumentHash || next.DocumentHash != changed.CandidateDocumentHash || next.Revision != changed.NewRevision || next.LatestCommitHash != h.RecordHash {
			return ErrInvalid
		}
		if added == 1 {
			saved := x.state
			x.state = next
			r, err := x.readPlan(ctx, *changed, true)
			x.state = saved
			if err != nil {
				return err
			}
			if r.RevertsTransactionID == "" {
				return ErrInvalid
			}
		}
	} else {
		if next.DocumentHash != old.DocumentHash || next.Revision != old.Revision || next.LatestCommitHash != old.LatestCommitHash {
			return ErrInvalid
		}
		if added == 1 && changed.State != PlanPending {
			return ErrInvalid
		}
		if changes == 1 && changed.State != PlanDiscarded {
			return ErrInvalid
		}
	}
	// Every old plan survives unchanged except the one admitted state change.
	for _, p := range old.Plans {
		if findPlan(next, p.TransactionID) == nil {
			return ErrInvalid
		}
	}
	return nil
}
