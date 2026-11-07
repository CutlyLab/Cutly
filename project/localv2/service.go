package localv2

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/transactionv2"
)

// New opens an initialized v2 project and resolves any durable intent. It never
// silently adopts a bare document, a v1 root, or partially missing metadata.
func New(dir string, options Options) (*Service, error) {
	s, err := configure(dir, options)
	if err != nil {
		return nil, err
	}
	err = s.locked(context.Background(), false, func(x *session) error { return x.load(context.Background()) })
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Create initializes an empty explicit v2 project directory. Identical retries
// are idempotent; legacy metadata or any unowned project document is rejected.
// The host creates and owns dir; Create does not grant arbitrary path authority.
func Create(ctx context.Context, dir string, document documentv2.Document, options Options) (*Service, error) {
	s, err := configure(dir, options)
	if err != nil {
		return nil, err
	}
	raw, err := freezeDocument(document, s.limits.MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	if !identity(string(document.ID), s.limits.MaxIdentityBytes) {
		return nil, ErrLimit
	}
	err = s.locked(ctx, true, func(x *session) error {
		if _, exists, err := x.read(ctx, intentName, 4096); err != nil {
			return err
		} else if exists {
			if err := x.load(ctx); err != nil {
				return err
			}
		}
		if _, exists, err := x.read(ctx, stateName, s.limits.MaxStateBytes); err != nil {
			return err
		} else if exists {
			if err := x.load(ctx); err != nil {
				return err
			}
			if x.state.DocumentHash != digest(raw) {
				return ErrProjectExists
			}
			return nil
		}
		if _, exists, err := x.read(ctx, DocumentName, s.limits.MaxDocumentBytes); err != nil {
			return err
		} else if exists {
			return ErrProjectExists
		}
		next := diskState{Version: Version, DocumentID: document.ID, Revision: document.Revision, DocumentHash: digest(raw), Plans: []PlanSummary{}, History: []historyRef{}}
		return x.transition(ctx, next, raw)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) with(ctx context.Context, fn func(*session) error) error {
	return s.locked(ctx, false, func(x *session) error {
		if err := x.load(ctx); err != nil {
			return err
		}
		return fn(x)
	})
}

func (s *Service) Inspect(ctx context.Context) (Snapshot, error) {
	var out Snapshot
	err := s.with(ctx, func(x *session) error {
		raw, err := x.object(ctx, "documents", x.state.DocumentHash, s.limits.MaxDocumentBytes)
		if err != nil {
			return err
		}
		doc, err := decodeDocument(raw, s.limits.MaxDocumentBytes)
		if err != nil {
			return err
		}
		out = Snapshot{Document: doc, DocumentHash: x.state.DocumentHash, StateSequence: x.state.Sequence, Plans: append([]PlanSummary{}, x.state.Plans...), HistoryCount: uint64(len(x.state.History))}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	var out Status
	err := s.with(ctx, func(x *session) error {
		out = Status{DocumentID: x.state.DocumentID, Revision: x.state.Revision, DocumentHash: x.state.DocumentHash, StateSequence: x.state.Sequence, HistoryCount: uint64(len(x.state.History))}
		for _, p := range x.state.Plans {
			switch p.State {
			case PlanPending:
				out.PendingPlans++
			case PlanApplied:
				out.AppliedPlans++
			case PlanDiscarded:
				out.DiscardedPlans++
			}
		}
		return nil
	})
	if err != nil {
		return Status{}, err
	}
	return out, nil
}

// History returns owned, verified immutable commit records after a state
// sequence, in commit order. A zero limit selects MaxHistoryPage.
func (s *Service) History(ctx context.Context, afterSequence, limit uint64) ([]CommitRecord, error) {
	if limit == 0 {
		limit = MaxHistoryPage
	}
	if limit > MaxHistoryPage {
		return nil, ErrLimit
	}
	out := []CommitRecord{}
	err := s.with(ctx, func(x *session) error {
		var pageBytes uint64
		for _, h := range x.state.History {
			if h.Sequence <= afterSequence {
				continue
			}
			name, err := objectName("commits", h.RecordHash)
			if err != nil {
				return err
			}
			info, err := x.root.Lstat(name)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() < 0 {
				return ErrInvalid
			}
			if uint64(info.Size()) > x.service.limits.MaxRecordBytes || pageBytes+uint64(info.Size()) > DefaultLimits().MaxDocumentBytes {
				return ErrLimit
			}
			pageBytes += uint64(info.Size())
			record, err := x.readCommit(ctx, h)
			if err != nil {
				return err
			}
			out = append(out, record)
			if uint64(len(out)) == limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Plan(ctx context.Context, tx transactionv2.Transaction) (MutationResult, error) {
	var out MutationResult
	if s == nil {
		return out, ErrInvalid
	}
	// Resource/operation admission before encoding or invoking the core.
	owned, txDigest, err := s.freezeTransaction(tx)
	if err != nil {
		return out, err
	}
	err = s.with(ctx, func(x *session) error { var e error; out, e = x.plan(ctx, owned, txDigest, ""); return e })
	if err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

func (s *Service) freezeTransaction(tx transactionv2.Transaction) (transactionv2.Transaction, string, error) {
	l := s.limits
	if uint64(len(tx.Operations)) > l.MaxOperations || !identity(tx.ID, l.MaxIdentityBytes) || !identity(tx.IdempotencyKey, l.MaxIdentityBytes) {
		return transactionv2.Transaction{}, "", ErrLimit
	}
	raw, err := canonical(tx, l.MaxTransactionBytes)
	if err != nil {
		return transactionv2.Transaction{}, "", err
	}
	owned, err := transactionv2.Decode(raw)
	if err != nil {
		return transactionv2.Transaction{}, "", err
	}
	hash, err := transactionv2.Digest(owned)
	return owned, hash, err
}

func (x *session) currentDocument(ctx context.Context) (documentv2.Document, []byte, error) {
	raw, err := x.object(ctx, "documents", x.state.DocumentHash, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return documentv2.Document{}, nil, err
	}
	doc, err := decodeDocument(raw, x.service.limits.MaxDocumentBytes)
	return doc, raw, err
}

func (x *session) plan(ctx context.Context, tx transactionv2.Transaction, txDigest, reverts string) (MutationResult, error) {
	out := MutationResult{Action: "plan", TransactionID: tx.ID}
	// Transaction IDs and replay keys each bind one exact digest, including
	// discarded plans. Neither allows payload replacement or key rebinding.
	for _, p := range x.state.Plans {
		if p.TransactionID == tx.ID || p.IdempotencyKey == tx.IdempotencyKey {
			if p.Digest != txDigest {
				return out, ErrConflict
			}
			if p.TransactionID != tx.ID {
				return out, ErrConflict
			}
			if p.State == PlanDiscarded {
				return out, ErrDiscarded
			}
			if _, err := x.readPlan(ctx, p, true); err != nil {
				return out, err
			}
			copy := p
			out.OK = true
			out.Replayed = true
			out.Plan = &copy
			if h := findHistory(x.state, p.TransactionID); h != nil {
				c, err := x.readCommit(ctx, *h)
				if err != nil {
					return out, err
				}
				out.Commit = &c
			}
			return out, nil
		}
	}
	if uint64(len(x.state.Plans)) >= x.service.limits.MaxPlans || x.state.Sequence == ^uint64(0) {
		return out, ErrLimit
	}
	document, docRaw, err := x.currentDocument(ctx)
	if err != nil {
		return out, err
	}
	applied, err := transactionv2.Apply(ctx, document, tx)
	if err != nil {
		return out, err
	}
	if documentv2.HasErrorDiagnostics(applied.Diagnostics) {
		out.Diagnostics = applied.Diagnostics
		return out, nil
	}
	candidate, err := freezeDocument(applied.Document, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return out, err
	}
	if applied.DocumentHash != digest(candidate) || applied.TransactionDigest != txDigest {
		return out, ErrInvalid
	}
	if _, _, err := x.service.freezeTransaction(applied.Inverse); err != nil {
		return out, err
	}
	record := PlanRecord{Version: Version, Transaction: tx, Digest: txDigest, BaseDocumentHash: x.state.DocumentHash, CandidateDocumentHash: applied.DocumentHash, Inverse: applied.Inverse, Impact: applied.Impact, RevertsTransactionID: reverts}
	encoded, err := canonical(record, x.service.limits.MaxRecordBytes)
	if err != nil {
		return out, err
	}
	if _, err := x.put(ctx, "documents", candidate); err != nil {
		return out, err
	}
	hash, err := x.put(ctx, "plans", encoded)
	if err != nil {
		return out, err
	}
	summary := PlanSummary{TransactionID: tx.ID, IdempotencyKey: tx.IdempotencyKey, Digest: txDigest, RecordHash: hash, State: PlanPending, BaseRevision: tx.ExpectedRevision, NewRevision: applied.Document.Revision, BaseDocumentHash: record.BaseDocumentHash, CandidateDocumentHash: record.CandidateDocumentHash}
	next := cloneState(x.state)
	next.Sequence++
	next.Plans = append(next.Plans, summary)
	sort.Slice(next.Plans, func(i, j int) bool { return next.Plans[i].TransactionID < next.Plans[j].TransactionID })
	if x.staging {
		// Revert exposes neither a pending plan nor an extra sequence before
		// its single commit point. Immutable staged objects are harmless.
		next.Sequence = x.state.Sequence
		x.state = next
	} else if err := x.transition(ctx, next, docRaw); err != nil {
		return out, err
	}
	out.OK = true
	out.Plan = &summary
	return out, nil
}

// Apply accepts only an ID of a previously persisted plan. It cannot accept or
// substitute a transaction payload. Candidate and inverse are recomputed from
// the persisted base using the typed core before revision/hash CAS publication.
func (s *Service) Apply(ctx context.Context, transactionID string) (MutationResult, error) {
	var out MutationResult
	err := s.with(ctx, func(x *session) error { var err error; out, err = x.apply(ctx, transactionID); return err })
	if err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

func (x *session) apply(ctx context.Context, id string) (MutationResult, error) {
	out := MutationResult{Action: "apply", TransactionID: id}
	p := findPlan(x.state, id)
	if p == nil {
		return out, ErrPlanNotFound
	}
	if p.State == PlanDiscarded {
		return out, ErrDiscarded
	}
	record, err := x.readPlan(ctx, *p, true)
	if err != nil {
		return out, err
	}
	if p.State == PlanApplied {
		h := findHistory(x.state, id)
		if h == nil {
			return out, ErrInvalid
		}
		c, err := x.readCommit(ctx, *h)
		if err != nil {
			return out, err
		}
		out.OK = true
		out.Replayed = true
		out.Commit = &c
		return out, nil
	}
	if p.BaseRevision != x.state.Revision || p.BaseDocumentHash != x.state.DocumentHash {
		return out, ErrConflict
	}
	if uint64(len(x.state.History)) >= x.service.limits.MaxHistory || x.state.Sequence == ^uint64(0) {
		return out, ErrLimit
	}
	candidate, err := x.object(ctx, "documents", record.CandidateDocumentHash, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return out, err
	}
	next := cloneState(x.state)
	next.Sequence++
	next.Revision = p.NewRevision
	next.DocumentHash = p.CandidateDocumentHash
	findPlan(next, id).State = PlanApplied
	c := CommitRecord{Version: Version, Sequence: next.Sequence, PlanHash: p.RecordHash, TransactionID: id, IdempotencyKey: p.IdempotencyKey, Digest: p.Digest, BaseRevision: p.BaseRevision, NewRevision: p.NewRevision, BaseDocumentHash: p.BaseDocumentHash, NewDocumentHash: p.CandidateDocumentHash, Inverse: record.Inverse, Impact: record.Impact, RevertsTransactionID: record.RevertsTransactionID}
	raw, err := canonical(c, x.service.limits.MaxRecordBytes)
	if err != nil {
		return out, err
	}
	hash, err := x.put(ctx, "commits", raw)
	if err != nil {
		return out, err
	}
	next.LatestCommitHash = hash
	next.History = append(next.History, historyRef{TransactionID: id, IdempotencyKey: p.IdempotencyKey, Digest: p.Digest, RecordHash: hash, Sequence: next.Sequence})
	if err := x.transition(ctx, next, candidate); err != nil {
		return out, err
	}
	out.OK = true
	out.Changed = true
	out.Commit = &c
	return out, nil
}

func (s *Service) Discard(ctx context.Context, id string) (MutationResult, error) {
	var out MutationResult
	err := s.with(ctx, func(x *session) error {
		out = MutationResult{Action: "discard", TransactionID: id}
		p := findPlan(x.state, id)
		if p == nil {
			return ErrPlanNotFound
		}
		if p.State == PlanApplied {
			return ErrConflict
		}
		if _, err := x.readPlan(ctx, *p, true); err != nil {
			return err
		}
		if p.State == PlanDiscarded {
			out.OK = true
			out.Replayed = true
			return nil
		}
		if x.state.Sequence == ^uint64(0) {
			return ErrLimit
		}
		_, doc, err := x.currentDocument(ctx)
		if err != nil {
			return err
		}
		next := cloneState(x.state)
		next.Sequence++
		findPlan(next, id).State = PlanDiscarded
		if err := x.transition(ctx, next, doc); err != nil {
			return err
		}
		out.OK = true
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

// Revert plans and commits a new typed inverse against the current document.
// It keeps the historical inverse's postimage predicates, so later unrelated
// changes survive and a later edit of the same target conflicts. The supplied
// new transaction ID/key make retries idempotent across process restarts.
func (s *Service) Revert(ctx context.Context, targetID, transactionID, idempotencyKey string) (MutationResult, error) {
	var out MutationResult
	err := s.with(ctx, func(x *session) error {
		if !identity(transactionID, s.limits.MaxIdentityBytes) || !identity(idempotencyKey, s.limits.MaxIdentityBytes) {
			return ErrLimit
		}
		if p := findPlan(x.state, transactionID); p != nil {
			record, err := x.readPlan(ctx, *p, true)
			if err != nil {
				return err
			}
			if record.RevertsTransactionID != targetID || p.IdempotencyKey != idempotencyKey {
				return ErrConflict
			}
			out, err = x.apply(ctx, transactionID)
			out.Action = "revert"
			return err
		}
		h := findHistory(x.state, targetID)
		if h == nil {
			return ErrPlanNotFound
		}
		target, err := x.readCommit(ctx, *h)
		if err != nil {
			return err
		}
		document, _, err := x.currentDocument(ctx)
		if err != nil {
			return err
		}
		inverse := target.Inverse
		inverse.ID = transactionID
		inverse.IdempotencyKey = idempotencyKey
		tx, err := transactionv2.Create(document, inverse)
		if err != nil {
			return err
		}
		owned, hash, err := s.freezeTransaction(tx)
		if err != nil {
			return err
		}
		x.staging = true
		planned, err := x.plan(ctx, owned, hash, targetID)
		x.staging = false
		if err != nil {
			return err
		}
		if !planned.OK {
			out = planned
			out.Action = "revert"
			return nil
		}
		out, err = x.apply(ctx, transactionID)
		out.Action = "revert"
		return err
	})
	if err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

func cloneState(s diskState) diskState {
	s.Plans = append([]PlanSummary{}, s.Plans...)
	s.History = append([]historyRef{}, s.History...)
	return s
}

func (x *session) readPlan(ctx context.Context, p PlanSummary, recompute bool) (PlanRecord, error) {
	raw, err := x.object(ctx, "plans", p.RecordHash, x.service.limits.MaxRecordBytes)
	if err != nil {
		return PlanRecord{}, err
	}
	var r PlanRecord
	if err := decodeCanonical(raw, &r, x.service.limits.MaxRecordBytes); err != nil {
		return r, err
	}
	tx, hash, err := x.service.freezeTransaction(r.Transaction)
	if err != nil {
		return r, err
	}
	if r.Version != Version || hash != p.Digest || r.Digest != hash || tx.ID != p.TransactionID || tx.IdempotencyKey != p.IdempotencyKey || tx.DocumentID != x.state.DocumentID && x.state.DocumentID != "" || tx.ExpectedRevision != p.BaseRevision || tx.ExpectedDocumentHash != p.BaseDocumentHash || r.BaseDocumentHash != p.BaseDocumentHash || r.CandidateDocumentHash != p.CandidateDocumentHash {
		return r, ErrInvalid
	}
	if recompute {
		if err := x.recompute(ctx, r, p); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (x *session) recompute(ctx context.Context, r PlanRecord, p PlanSummary) error {
	base, err := x.object(ctx, "documents", r.BaseDocumentHash, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	doc, err := decodeDocument(base, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	applied, err := transactionv2.Apply(ctx, doc, r.Transaction)
	if err != nil {
		return err
	}
	if documentv2.HasErrorDiagnostics(applied.Diagnostics) {
		return fmt.Errorf("%w: persisted transaction cannot reproduce candidate", ErrInvalid)
	}
	candidate, err := freezeDocument(applied.Document, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	if digest(candidate) != r.CandidateDocumentHash || applied.Document.Revision != p.NewRevision || applied.TransactionDigest != r.Digest {
		return ErrInvalid
	}
	actual, err := x.object(ctx, "documents", r.CandidateDocumentHash, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, candidate) {
		return ErrInvalid
	}
	inverse, err := canonical(applied.Inverse, x.service.limits.MaxTransactionBytes)
	if err != nil {
		return err
	}
	recorded, err := canonical(r.Inverse, x.service.limits.MaxTransactionBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(inverse, recorded) || !reflect.DeepEqual(applied.Impact, r.Impact) {
		return fmt.Errorf("%w: persisted inverse or impact changed", ErrInvalid)
	}
	return nil
}

func (x *session) readCommit(ctx context.Context, h historyRef) (CommitRecord, error) {
	raw, err := x.object(ctx, "commits", h.RecordHash, x.service.limits.MaxRecordBytes)
	if err != nil {
		return CommitRecord{}, err
	}
	var c CommitRecord
	if err := decodeCanonical(raw, &c, x.service.limits.MaxRecordBytes); err != nil {
		return c, err
	}
	p := findPlan(x.state, h.TransactionID)
	if p == nil {
		return c, ErrInvalid
	}
	r, err := x.readPlan(ctx, *p, true)
	if err != nil {
		return c, err
	}
	expected := CommitRecord{Version: Version, Sequence: h.Sequence, PlanHash: p.RecordHash, TransactionID: p.TransactionID, IdempotencyKey: p.IdempotencyKey, Digest: p.Digest, BaseRevision: p.BaseRevision, NewRevision: p.NewRevision, BaseDocumentHash: p.BaseDocumentHash, NewDocumentHash: p.CandidateDocumentHash, Inverse: r.Inverse, Impact: r.Impact, RevertsTransactionID: r.RevertsTransactionID}
	if !reflect.DeepEqual(c, expected) || c.TransactionID != h.TransactionID || c.IdempotencyKey != h.IdempotencyKey || c.Digest != h.Digest {
		return c, ErrInvalid
	}
	return c, nil
}
