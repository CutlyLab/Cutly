package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const MaxHistoryPage = 1024
const generatedIdentityReserve = 64

// Create initializes a native local project. Repeating Create with the exact
// same canonical document is idempotent and also completes state recovery;
// different content fails with ErrProjectExists.
func Create(ctx context.Context, dir string, document videoedit.Document, options Options) (*Service, error) {
	service, err := New(dir, options)
	if err != nil {
		return nil, err
	}
	if !documentAdmissionSize(document, service.limits.MaxDocumentBytes) {
		return nil, fmt.Errorf("%w: document exceeds configured encoded-size budget", ErrLimit)
	}
	if diagnostics := videoedit.ValidateDocument(document); videoedit.HasErrorDiagnostics(diagnostics) {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, diagnostics[0].Code)
	}
	if !documentIdentitiesAdmitted(document, service.limits.MaxIdentityBytes) {
		return nil, fmt.Errorf("%w: document identity exceeds configured bounds", ErrLimit)
	}
	canonical, err := videoedit.CanonicalJSON(document)
	if err != nil {
		return nil, err
	}
	if uint64(len(canonical)) > service.limits.MaxDocumentBytes {
		return nil, fmt.Errorf("%w: document exceeds %d bytes", ErrLimit, service.limits.MaxDocumentBytes)
	}
	wantedHash, err := videoedit.DocumentHash(document)
	if err != nil {
		return nil, err
	}
	err = service.withLockedRoot(ctx, func(root *os.Root) error {
		session := &projectSession{service: service, root: root}
		existingRaw, exists, readErr := session.readRegular(ctx, DocumentName, service.limits.MaxDocumentBytes)
		if readErr != nil {
			return readErr
		}
		if exists {
			if !bytes.Equal(existingRaw, canonical) {
				return ErrProjectExists
			}
			if err := session.recover(ctx); err != nil {
				return err
			}
			if session.documentHash != wantedHash {
				return ErrProjectExists
			}
			return nil
		}
		for _, name := range []string{stateName, intentName, markerName} {
			if _, present, checkErr := session.readRegular(ctx, name, service.limits.MaxIntentBytes); checkErr != nil {
				return checkErr
			} else if present {
				return fmt.Errorf("%w: metadata exists without a project document", ErrInvalid)
			}
		}
		if err := session.atomicWrite(ctx, DocumentName, canonical); err != nil {
			return err
		}
		if err := session.recover(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("%w: initialize state: %v", ErrCommitUnknown, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return service, nil
}

func (service *Service) Inspect(ctx context.Context) (Snapshot, error) {
	var result Snapshot
	err := service.withSession(ctx, func(session *projectSession) error {
		document, err := cloneDocument(session.document)
		if err != nil {
			return err
		}
		result = Snapshot{
			Document: document, DocumentHash: session.documentHash,
			StateSequence: session.state.Sequence, HistoryCount: uint64(len(session.state.History)),
			PendingPlans: pendingPlanSummaries(session.state.Plans),
		}
		return nil
	})
	return result, err
}

func (service *Service) Status(ctx context.Context) (Status, error) {
	var result Status
	err := service.withSession(ctx, func(session *projectSession) error {
		result = Status{
			DocumentID: session.document.ID, Revision: session.document.Revision,
			DocumentHash: session.documentHash, StateSequence: session.state.Sequence,
			HistoryCount: uint64(len(session.state.History)), RecoveryPending: false,
		}
		for _, plan := range session.state.Plans {
			switch plan.State {
			case PlanPending:
				result.PendingPlans++
			case PlanApplied:
				result.AppliedPlans++
			case PlanDiscarded:
				result.DiscardedPlans++
			}
		}
		return nil
	})
	return result, err
}

// History returns records after afterSequence, in commit order. A zero limit
// uses MaxHistoryPage; larger requests fail rather than silently truncating.
func (service *Service) History(ctx context.Context, afterSequence, limit uint64) ([]HistoryRecord, error) {
	if limit == 0 {
		limit = MaxHistoryPage
	}
	if limit > MaxHistoryPage {
		return nil, fmt.Errorf("%w: history page limit exceeds %d", ErrLimit, MaxHistoryPage)
	}
	result := make([]HistoryRecord, 0)
	err := service.withSession(ctx, func(session *projectSession) error {
		for _, record := range session.state.History {
			if record.Sequence <= afterSequence {
				continue
			}
			clone, err := cloneHistory(record)
			if err != nil {
				return err
			}
			result = append(result, clone)
			if uint64(len(result)) == limit {
				break
			}
		}
		return nil
	})
	return result, err
}

func (service *Service) Plan(ctx context.Context, transaction videoedit.Transaction) (MutationResult, error) {
	result := baseMutation("plan", transaction.ID)
	if service == nil {
		return MutationResult{}, ErrServiceClosed
	}
	if admission := admitTransaction(transaction, service.limits); admission != nil {
		result.Diagnostics = []videoedit.Diagnostic{*admission}
		return result, nil
	}
	digest, err := videoedit.TransactionDigest(transaction)
	if err != nil {
		return MutationResult{}, err
	}
	err = service.withSession(ctx, func(session *projectSession) error {
		result.BaseRevision = session.document.Revision
		result.BaseDocumentHash = session.documentHash
		if plan, ok := findPlan(session.state.Plans, transaction.ID); ok {
			return replayExistingPlan(&result, plan, digest, session.state.History)
		}
		if history, ok := findHistory(session.state.History, transaction.ID); ok {
			historyDigest, digestErr := videoedit.TransactionDigest(history.Transaction)
			if digestErr != nil {
				return digestErr
			}
			if historyDigest != digest {
				result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanConflict, "/id", "transaction ID is already committed with different content")}
				return nil
			}
			return mutationFromHistory(&result, "already_applied", history, true)
		}
		if uint64(len(session.state.Plans)) >= session.service.limits.MaxPlans {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/transaction", "project plan count reached its configured limit")}
			return nil
		}
		if session.state.Sequence == ^uint64(0) {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/state_sequence", "project state sequence cannot be incremented")}
			return nil
		}
		applied, applyErr := videoedit.Apply(session.document, transaction, ledgerFromHistory(session.state.History))
		if applyErr != nil {
			return applyErr
		}
		if videoedit.HasErrorDiagnostics(applied.Diagnostics) {
			result.Diagnostics = cloneDiagnostics(applied.Diagnostics)
			return nil
		}
		if applied.Replayed || applied.Commit == nil {
			return fmt.Errorf("%w: core returned an unbound plan replay", ErrInvalid)
		}
		candidateHash, hashErr := videoedit.DocumentHash(applied.Document)
		if hashErr != nil {
			return hashErr
		}
		state, cloneErr := cloneState(session.state)
		if cloneErr != nil {
			return cloneErr
		}
		state.Sequence++
		state.Plans = append(state.Plans, diskPlan{
			State: PlanPending, Transaction: transaction, Digest: digest,
			BaseHash: session.documentHash, Candidate: applied.Document,
			CandidateHash: candidateHash, Commit: *applied.Commit,
		})
		sortPlans(state.Plans)
		if err := session.writeState(ctx, state); err != nil {
			return err
		}
		summary := summarizePlan(state.Plans[planIndex(state.Plans, transaction.ID)])
		result.OK = true
		result.State = "planned"
		result.NewRevision = revisionPointer(summary.NewRevision)
		result.NewDocumentHash = summary.CandidateHash
		result.Plan = &summary
		return nil
	})
	return ownMutation(result), err
}

func (service *Service) Apply(ctx context.Context, transactionID string, expectedRevision videoedit.Revision) (MutationResult, error) {
	result := baseMutation("apply", transactionID)
	if service == nil {
		return MutationResult{}, ErrServiceClosed
	}
	if !validIdentity(transactionID, service.limits.MaxIdentityBytes) {
		result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanNotFound, "/transaction_id", "transaction ID is invalid")}
		return result, nil
	}
	err := service.withSession(ctx, func(session *projectSession) error {
		result.BaseRevision = session.document.Revision
		result.BaseDocumentHash = session.documentHash
		index := planIndex(session.state.Plans, transactionID)
		if index < 0 {
			if history, ok := findHistory(session.state.History, transactionID); ok {
				return mutationFromHistory(&result, "applied", history, true)
			}
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanNotFound, "/transaction_id", "reviewed transaction plan was not found")}
			return nil
		}
		plan := session.state.Plans[index]
		switch plan.State {
		case PlanApplied:
			history, ok := findHistory(session.state.History, transactionID)
			if !ok {
				return fmt.Errorf("%w: applied plan has no history record", ErrInvalid)
			}
			return mutationFromHistory(&result, "applied", history, true)
		case PlanDiscarded:
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanDiscarded, "/transaction_id", "reviewed transaction plan was discarded")}
			return nil
		}
		if expectedRevision != session.document.Revision {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeRevisionConflict, "/expected_revision", fmt.Sprintf("expected revision %d, current revision is %d", expectedRevision, session.document.Revision))}
			return nil
		}
		if plan.Transaction.ExpectedRevision != expectedRevision || plan.BaseHash != session.documentHash {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeBaseChanged, "/transaction_id", "reviewed plan base revision or hash no longer matches the project")}
			return nil
		}
		if uint64(len(session.state.History)) >= session.service.limits.MaxHistory {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/history", "project history reached its configured limit")}
			return nil
		}
		if session.state.Sequence == ^uint64(0) {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/state_sequence", "project state sequence cannot be incremented")}
			return nil
		}
		state, cloneErr := cloneState(session.state)
		if cloneErr != nil {
			return cloneErr
		}
		state.Sequence++
		state.Plans[index].State = PlanApplied
		history := HistoryRecord{
			Sequence: state.Sequence, BaseRevision: session.document.Revision,
			BaseDocumentHash: session.documentHash, Transaction: plan.Transaction, Commit: plan.Commit,
		}
		state.History = append(state.History, history)
		if err := session.commit(ctx, plan.Candidate, state); err != nil {
			return err
		}
		return mutationFromHistory(&result, "applied", history, false)
	})
	return ownMutation(result), err
}

func (service *Service) Discard(ctx context.Context, transactionID string) (MutationResult, error) {
	result := baseMutation("discard", transactionID)
	if service == nil {
		return MutationResult{}, ErrServiceClosed
	}
	if !validIdentity(transactionID, service.limits.MaxIdentityBytes) {
		result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanNotFound, "/transaction_id", "transaction ID is invalid")}
		return result, nil
	}
	err := service.withSession(ctx, func(session *projectSession) error {
		result.BaseRevision = session.document.Revision
		result.BaseDocumentHash = session.documentHash
		index := planIndex(session.state.Plans, transactionID)
		if index < 0 {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanNotFound, "/transaction_id", "reviewed transaction plan was not found")}
			return nil
		}
		plan := session.state.Plans[index]
		if plan.State == PlanApplied {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanAlreadyApplied, "/transaction_id", "committed transaction cannot be discarded")}
			return nil
		}
		if plan.State == PlanDiscarded {
			result.OK, result.State, result.Replayed = true, "discarded", true
			result.NewRevision = revisionPointer(session.document.Revision)
			result.NewDocumentHash = session.documentHash
			summary := summarizePlan(plan)
			result.Plan = &summary
			return nil
		}
		if session.state.Sequence == ^uint64(0) {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/state_sequence", "project state sequence cannot be incremented")}
			return nil
		}
		state, cloneErr := cloneState(session.state)
		if cloneErr != nil {
			return cloneErr
		}
		state.Sequence++
		state.Plans[index].State = PlanDiscarded
		if err := session.writeState(ctx, state); err != nil {
			return err
		}
		result.OK, result.State, result.Changed = true, "discarded", true
		result.NewRevision = revisionPointer(session.document.Revision)
		result.NewDocumentHash = session.documentHash
		summary := summarizePlan(state.Plans[index])
		result.Plan = &summary
		return nil
	})
	return ownMutation(result), err
}

func (service *Service) Revert(ctx context.Context, transactionID string, expectedRevision videoedit.Revision) (MutationResult, error) {
	result := baseMutation("revert", "")
	result.RevertsTransactionID = transactionID
	if service == nil {
		return MutationResult{}, ErrServiceClosed
	}
	if !validIdentity(transactionID, service.limits.MaxIdentityBytes) {
		result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeHistoryNotFound, "/transaction_id", "history transaction ID is invalid")}
		return result, nil
	}
	err := service.withSession(ctx, func(session *projectSession) error {
		result.BaseRevision = session.document.Revision
		result.BaseDocumentHash = session.documentHash
		if reverted, ok := findRevert(session.state.History, transactionID); ok {
			if expectedRevision == reverted.BaseRevision {
				return mutationFromHistory(&result, "reverted", reverted, true)
			}
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeAlreadyReverted, "/transaction_id", "transaction was already reverted; revert the revert commit to redo it")}
			return nil
		}
		target, ok := findHistory(session.state.History, transactionID)
		if !ok {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeHistoryNotFound, "/transaction_id", "committed transaction was not found")}
			return nil
		}
		if expectedRevision != session.document.Revision {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeRevisionConflict, "/expected_revision", fmt.Sprintf("expected revision %d, current revision is %d", expectedRevision, session.document.Revision))}
			return nil
		}
		if uint64(len(session.state.History)) >= session.service.limits.MaxHistory {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/history", "project history reached its configured limit")}
			return nil
		}
		if session.state.Sequence == ^uint64(0) {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/state_sequence", "project state sequence cannot be incremented")}
			return nil
		}
		transaction := target.Commit.Inverse
		identity := revertIdentity(target.Commit.Digest, expectedRevision)
		transaction.ID = "revert:" + identity
		transaction.IdempotencyKey = "revert:" + identity
		transaction.ExpectedRevision = expectedRevision
		if uint64(len(transaction.Operations)) > session.service.limits.MaxOperations {
			result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodeResourceLimit, "/history/inverse/operations", "inverse operation count exceeds the project limit")}
			return nil
		}
		applied, applyErr := videoedit.Apply(session.document, transaction, ledgerFromHistory(session.state.History))
		if applyErr != nil {
			return applyErr
		}
		if videoedit.HasErrorDiagnostics(applied.Diagnostics) {
			result.TransactionID = transaction.ID
			result.Diagnostics = cloneDiagnostics(applied.Diagnostics)
			return nil
		}
		if applied.Commit == nil || applied.Replayed {
			return fmt.Errorf("%w: core returned an unbound revert", ErrInvalid)
		}
		state, cloneErr := cloneState(session.state)
		if cloneErr != nil {
			return cloneErr
		}
		state.Sequence++
		history := HistoryRecord{
			Sequence: state.Sequence, BaseRevision: session.document.Revision,
			BaseDocumentHash: session.documentHash, Transaction: transaction,
			Commit: *applied.Commit, RevertsTransactionID: transactionID,
		}
		state.History = append(state.History, history)
		if err := session.commit(ctx, applied.Document, state); err != nil {
			return err
		}
		result.TransactionID = transaction.ID
		return mutationFromHistory(&result, "reverted", history, false)
	})
	return ownMutation(result), err
}

func replayExistingPlan(result *MutationResult, plan diskPlan, digest string, history []HistoryRecord) error {
	if plan.Digest != digest {
		result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanConflict, "/id", "transaction ID already names a different reviewed plan")}
		return nil
	}
	summary := summarizePlan(plan)
	result.Plan = &summary
	result.NewRevision = revisionPointer(summary.NewRevision)
	result.NewDocumentHash = summary.CandidateHash
	result.Replayed = true
	switch plan.State {
	case PlanPending:
		result.OK, result.State = true, "planned"
	case PlanDiscarded:
		result.State = "discarded"
		result.Diagnostics = []videoedit.Diagnostic{errorDiagnostic(CodePlanDiscarded, "/id", "transaction ID names a discarded plan")}
	case PlanApplied:
		record, ok := findHistory(history, plan.Transaction.ID)
		if !ok {
			return fmt.Errorf("%w: applied plan has no history record", ErrInvalid)
		}
		return mutationFromHistory(result, "already_applied", record, true)
	}
	return nil
}

func mutationFromHistory(result *MutationResult, state string, record HistoryRecord, replayed bool) error {
	clone, err := cloneHistory(record)
	if err != nil {
		return err
	}
	result.OK = true
	result.State = state
	result.TransactionID = record.Transaction.ID
	result.BaseRevision = record.BaseRevision
	result.NewRevision = revisionPointer(record.Commit.Revision)
	result.BaseDocumentHash = record.BaseDocumentHash
	result.NewDocumentHash = record.Commit.DocumentHash
	result.Changed = !replayed
	result.Replayed = replayed
	result.RenderRequired = !replayed
	result.RevertsTransactionID = record.RevertsTransactionID
	result.Commit = &clone
	return nil
}

func ledgerFromHistory(history []HistoryRecord) videoedit.IdempotencyLedger {
	result := make(videoedit.IdempotencyLedger, len(history))
	for _, record := range history {
		result[record.Commit.IdempotencyKey] = record.Commit
	}
	return result
}

func pendingPlanSummaries(plans []diskPlan) []PlanSummary {
	result := make([]PlanSummary, 0)
	for _, plan := range plans {
		if plan.State == PlanPending {
			result = append(result, summarizePlan(plan))
		}
	}
	return result
}

func summarizePlan(plan diskPlan) PlanSummary {
	return PlanSummary{
		TransactionID: plan.Transaction.ID, State: plan.State,
		BaseRevision: plan.Transaction.ExpectedRevision, NewRevision: plan.Commit.Revision,
		BaseHash: plan.BaseHash, CandidateHash: plan.CandidateHash,
		OperationCount: uint64(len(plan.Transaction.Operations)),
	}
}

func findPlan(plans []diskPlan, transactionID string) (diskPlan, bool) {
	index := planIndex(plans, transactionID)
	if index < 0 {
		return diskPlan{}, false
	}
	return plans[index], true
}

func planIndex(plans []diskPlan, transactionID string) int {
	left, right := 0, len(plans)
	for left < right {
		middle := left + (right-left)/2
		if plans[middle].Transaction.ID < transactionID {
			left = middle + 1
		} else {
			right = middle
		}
	}
	if left < len(plans) && plans[left].Transaction.ID == transactionID {
		return left
	}
	return -1
}

func findHistory(history []HistoryRecord, transactionID string) (HistoryRecord, bool) {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Transaction.ID == transactionID {
			return history[index], true
		}
	}
	return HistoryRecord{}, false
}

func findRevert(history []HistoryRecord, transactionID string) (HistoryRecord, bool) {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].RevertsTransactionID == transactionID {
			return history[index], true
		}
	}
	return HistoryRecord{}, false
}

func cloneState(value diskState) (diskState, error) {
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return diskState{}, err
	}
	var result diskState
	if err := wirejson.Decode(raw, &result); err != nil {
		return diskState{}, err
	}
	return result, nil
}

func cloneDocument(value videoedit.Document) (videoedit.Document, error) {
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return videoedit.Document{}, err
	}
	var result videoedit.Document
	if err := wirejson.Decode(raw, &result); err != nil {
		return videoedit.Document{}, err
	}
	return result, nil
}

func cloneHistory(value HistoryRecord) (HistoryRecord, error) {
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return HistoryRecord{}, err
	}
	var result HistoryRecord
	if err := wirejson.Decode(raw, &result); err != nil {
		return HistoryRecord{}, err
	}
	return result, nil
}

func ownMutation(value MutationResult) MutationResult {
	if value.Diagnostics == nil {
		value.Diagnostics = []videoedit.Diagnostic{}
	} else {
		value.Diagnostics = cloneDiagnostics(value.Diagnostics)
	}
	if value.Plan != nil {
		copy := *value.Plan
		value.Plan = &copy
	}
	if value.Commit != nil {
		copy, err := cloneHistory(*value.Commit)
		if err == nil {
			value.Commit = &copy
		}
	}
	return value
}

func cloneDiagnostics(values []videoedit.Diagnostic) []videoedit.Diagnostic {
	if values == nil {
		return nil
	}
	raw, err := videoedit.CanonicalJSON(values)
	if err != nil {
		return append([]videoedit.Diagnostic(nil), values...)
	}
	var result []videoedit.Diagnostic
	if err := json.Unmarshal(raw, &result); err != nil {
		return append([]videoedit.Diagnostic(nil), values...)
	}
	return result
}

func baseMutation(action, transactionID string) MutationResult {
	return MutationResult{
		Action: action, State: "rejected", TransactionID: transactionID,
		Diagnostics: []videoedit.Diagnostic{},
	}
}

func errorDiagnostic(code, path, message string) videoedit.Diagnostic {
	return videoedit.Diagnostic{Code: code, Severity: videoedit.SeverityError, Path: path, Message: message}
}

func revisionPointer(value videoedit.Revision) *videoedit.Revision {
	copy := value
	return &copy
}

func admitTransaction(transaction videoedit.Transaction, limits Limits) *videoedit.Diagnostic {
	if uint64(len(transaction.Operations)) > limits.MaxOperations {
		diagnostic := errorDiagnostic(CodeResourceLimit, "/operations", "transaction operation count exceeds the project limit")
		return &diagnostic
	}
	remaining := limits.MaxTransactionBytes
	if limits.MaxIdentityBytes <= generatedIdentityReserve ||
		uint64(len(transaction.ID)) > limits.MaxIdentityBytes-generatedIdentityReserve ||
		uint64(len(transaction.IdempotencyKey)) > limits.MaxIdentityBytes-generatedIdentityReserve {
		diagnostic := errorDiagnostic(CodeResourceLimit, "/transaction", "transaction identity leaves no room for deterministic inverse identities")
		return &diagnostic
	}
	charge := func(size uint64) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	chargeString := func(value string) bool {
		if !utf8.ValidString(value) || uint64(len(value)) > limits.MaxIdentityBytes && len(value) != 0 {
			return false
		}
		length := uint64(len(value))
		if length > (^uint64(0)-2)/6 {
			return false
		}
		return charge(2 + 6*length)
	}
	if !charge(256) || !chargeString(transaction.ID) || !chargeString(transaction.IdempotencyKey) {
		diagnostic := errorDiagnostic(CodeResourceLimit, "/transaction", "transaction identity or encoded size exceeds the project limit")
		return &diagnostic
	}
	chargeAnnotation := func(annotation videoedit.Annotation) bool {
		return charge(128) && chargeString(string(annotation.ID)) && chargeString(annotation.Namespace) &&
			chargeString(annotation.Key) && charge(uint64(len(annotation.Value)))
	}
	for index, operation := range transaction.Operations {
		if !charge(512) || !chargeString(operation.ID) || !chargeString(string(operation.ClipID)) ||
			!chargeString(string(operation.TrackID)) || !chargeString(string(operation.TargetTrackID)) ||
			!chargeString(string(operation.NewClipID)) || !chargeString(operation.Property) ||
			!charge(uint64(len(operation.Value))) {
			diagnostic := errorDiagnostic(CodeResourceLimit, fmt.Sprintf("/operations/%d", index), "operation identity or encoded size exceeds the project limit")
			return &diagnostic
		}
		if operation.Clip == nil {
			continue
		}
		clip := operation.Clip
		if !charge(512) || !chargeString(string(clip.ID)) || !chargeString(string(clip.AssetID)) {
			diagnostic := errorDiagnostic(CodeResourceLimit, fmt.Sprintf("/operations/%d/clip", index), "clip identity or encoded size exceeds the project limit")
			return &diagnostic
		}
		for key, value := range clip.Properties {
			if !chargeString(key) || !charge(uint64(len(value))) {
				diagnostic := errorDiagnostic(CodeResourceLimit, fmt.Sprintf("/operations/%d/clip/properties", index), "clip properties exceed the project limit")
				return &diagnostic
			}
		}
		for _, annotation := range clip.Annotations {
			if !chargeAnnotation(annotation) {
				diagnostic := errorDiagnostic(CodeResourceLimit, fmt.Sprintf("/operations/%d/clip/annotations", index), "clip annotations exceed the project limit")
				return &diagnostic
			}
		}
	}
	encoded, err := videoedit.CanonicalJSON(transaction)
	if err != nil {
		diagnostic := errorDiagnostic(videoedit.CodeOperationInvalid, "/transaction", err.Error())
		return &diagnostic
	}
	if uint64(len(encoded)) > limits.MaxTransactionBytes {
		diagnostic := errorDiagnostic(CodeResourceLimit, "/transaction", "encoded transaction exceeds the project limit")
		return &diagnostic
	}
	return nil
}

func transactionIdentitiesAdmitted(transaction videoedit.Transaction, maximum uint64) bool {
	if !validIdentity(transaction.ID, maximum) || !validIdentity(transaction.IdempotencyKey, maximum) {
		return false
	}
	optional := func(value string) bool {
		return value == "" || validIdentity(value, maximum)
	}
	for _, operation := range transaction.Operations {
		if !validIdentity(operation.ID, maximum) || !optional(string(operation.ClipID)) ||
			!optional(string(operation.TrackID)) || !optional(string(operation.TargetTrackID)) ||
			!optional(string(operation.NewClipID)) || !optional(operation.Property) {
			return false
		}
		if operation.Clip != nil && (!validIdentity(string(operation.Clip.ID), maximum) || !validIdentity(string(operation.Clip.AssetID), maximum)) {
			return false
		}
	}
	return true
}

func documentIdentitiesAdmitted(document videoedit.Document, maximum uint64) bool {
	if !validIdentity(string(document.ID), maximum) {
		return false
	}
	mapKeys := func(values map[string]json.RawMessage) bool {
		for key := range values {
			if !validIdentity(key, maximum) {
				return false
			}
		}
		return true
	}
	annotation := func(value videoedit.Annotation) bool {
		return validIdentity(string(value.ID), maximum) && validIdentity(value.Namespace, maximum) &&
			(value.Key == "" || validIdentity(value.Key, maximum))
	}
	if !mapKeys(document.Metadata) {
		return false
	}
	for _, value := range document.Annotations {
		if !annotation(value) {
			return false
		}
	}
	for _, asset := range document.Assets {
		if !validIdentity(string(asset.ID), maximum) || !utf8.ValidString(string(asset.Kind)) ||
			!utf8.ValidString(asset.URI) || !utf8.ValidString(asset.ContentHash) || !mapKeys(asset.Metadata) {
			return false
		}
	}
	for _, timeline := range document.Timelines {
		if !validIdentity(string(timeline.ID), maximum) || !utf8.ValidString(timeline.Name) || !mapKeys(timeline.Metadata) {
			return false
		}
		for _, value := range timeline.Annotations {
			if !annotation(value) {
				return false
			}
		}
		for _, track := range timeline.Tracks {
			if !validIdentity(string(track.ID), maximum) || !utf8.ValidString(string(track.Kind)) ||
				!utf8.ValidString(track.Name) || !mapKeys(track.Metadata) {
				return false
			}
			for _, value := range track.Annotations {
				if !annotation(value) {
					return false
				}
			}
			for _, clip := range track.Clips {
				if !validIdentity(string(clip.ID), maximum) || !validIdentity(string(clip.AssetID), maximum) || !mapKeys(clip.Properties) {
					return false
				}
				for _, value := range clip.Annotations {
					if !annotation(value) {
						return false
					}
				}
			}
		}
	}
	return true
}

func documentAdmissionSize(document videoedit.Document, maximum uint64) bool {
	remaining := maximum
	charge := func(size uint64) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	chargeString := func(value string) bool {
		if !utf8.ValidString(value) {
			return false
		}
		size := uint64(2)
		for _, runeValue := range value {
			switch {
			case runeValue == '"' || runeValue == '\\' || runeValue == '\b' || runeValue == '\f' || runeValue == '\n' || runeValue == '\r' || runeValue == '\t':
				size += 2
			case runeValue < 0x20 || runeValue == '<' || runeValue == '>' || runeValue == '&' || runeValue == '\u2028' || runeValue == '\u2029':
				size += 6
			default:
				size += uint64(utf8.RuneLen(runeValue))
			}
			if size > remaining {
				return false
			}
		}
		return charge(size)
	}
	chargeRawMap := func(values map[string]json.RawMessage) bool {
		if !charge(2 + uint64(len(values))*2) {
			return false
		}
		for key, value := range values {
			if !chargeString(key) || !charge(uint64(len(value))) {
				return false
			}
		}
		return true
	}
	chargeAnnotation := func(value videoedit.Annotation) bool {
		return charge(192+uint64(len(value.Value))) && chargeString(string(value.ID)) &&
			chargeString(value.Namespace) && chargeString(value.Key)
	}
	if !charge(512) || !chargeString(string(document.ID)) || !chargeRawMap(document.Metadata) ||
		!charge(uint64(len(document.Assets)+len(document.Timelines)+len(document.Annotations))*32) {
		return false
	}
	for _, value := range document.Annotations {
		if !chargeAnnotation(value) {
			return false
		}
	}
	for _, asset := range document.Assets {
		if !charge(384) || !chargeString(string(asset.ID)) || !chargeString(string(asset.Kind)) ||
			!chargeString(asset.URI) || !chargeString(asset.ContentHash) || !chargeRawMap(asset.Metadata) {
			return false
		}
	}
	for _, timeline := range document.Timelines {
		if !charge(384) || !chargeString(string(timeline.ID)) || !chargeString(timeline.Name) ||
			!chargeRawMap(timeline.Metadata) || !charge(uint64(len(timeline.Tracks)+len(timeline.Annotations))*32) {
			return false
		}
		for _, value := range timeline.Annotations {
			if !chargeAnnotation(value) {
				return false
			}
		}
		for _, track := range timeline.Tracks {
			if !charge(384) || !chargeString(string(track.ID)) || !chargeString(string(track.Kind)) ||
				!chargeString(track.Name) || !chargeRawMap(track.Metadata) ||
				!charge(uint64(len(track.Clips)+len(track.Annotations))*32) {
				return false
			}
			for _, value := range track.Annotations {
				if !chargeAnnotation(value) {
					return false
				}
			}
			for _, clip := range track.Clips {
				if !charge(512) || !chargeString(string(clip.ID)) || !chargeString(string(clip.AssetID)) ||
					!chargeRawMap(clip.Properties) || !charge(uint64(len(clip.Annotations))*32) {
					return false
				}
				for _, value := range clip.Annotations {
					if !chargeAnnotation(value) {
						return false
					}
				}
			}
		}
	}
	return true
}

func validIdentity(value string, maximum uint64) bool {
	return utf8.ValidString(value) && uint64(len(value)) <= maximum && strings.TrimSpace(value) != ""
}

func revertIdentity(commitDigest string, revision videoedit.Revision) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", commitDigest, revision)))
	return hex.EncodeToString(digest[:16])
}
