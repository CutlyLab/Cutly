package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	metadataDirectory = ".cutly"
	stateName         = ".cutly/state.json"
	intentName        = ".cutly/commit.intent.json"
	markerName        = ".cutly/initialized"
	lockName          = ".cutly/project.lock"
)

var markerBytes = []byte("cutly.local-project-state.v1\n")

type diskPlan struct {
	State         PlanState              `json:"state"`
	Transaction   videoedit.Transaction  `json:"transaction"`
	Digest        string                 `json:"digest"`
	BaseHash      string                 `json:"base_hash"`
	Candidate     videoedit.Document     `json:"candidate"`
	CandidateHash string                 `json:"candidate_hash"`
	Commit        videoedit.CommitRecord `json:"commit"`
}

type diskState struct {
	Version          int                `json:"version"`
	Fingerprint      string             `json:"fingerprint,omitempty"`
	Sequence         uint64             `json:"sequence"`
	DocumentID       videoedit.ID       `json:"document_id"`
	DocumentRevision videoedit.Revision `json:"document_revision"`
	DocumentHash     string             `json:"document_hash"`
	Plans            []diskPlan         `json:"plans"`
	History          []HistoryRecord    `json:"history"`
}

type commitIntent struct {
	Version              int                `json:"version"`
	PreviousDocumentHash string             `json:"previous_document_hash"`
	PreviousStateHash    string             `json:"previous_state_hash"`
	NextDocument         videoedit.Document `json:"next_document"`
	NextDocumentHash     string             `json:"next_document_hash"`
	NextState            diskState          `json:"next_state"`
	NextStateHash        string             `json:"next_state_hash"`
}

type faultHook func(point string) error

type Service struct {
	dir    string
	limits Limits
	fault  faultHook
}

type projectSession struct {
	service      *Service
	root         *os.Root
	document     videoedit.Document
	documentHash string
	state        diskState
	stateHash    string
}

var processGates sync.Map

func New(dir string, options Options) (*Service, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: project root must be a non-symlink directory", ErrInvalid)
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	return &Service{dir: filepath.Clean(absolute), limits: limits}, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	if limits.MaxDocumentBytes == 0 || limits.MaxTransactionBytes == 0 || limits.MaxStateBytes == 0 || limits.MaxIntentBytes == 0 ||
		limits.MaxIdentityBytes == 0 || limits.MaxPlans == 0 || limits.MaxHistory == 0 || limits.MaxOperations == 0 {
		return Limits{}, fmt.Errorf("%w: every limit must be positive", ErrLimit)
	}
	if limits.MaxDocumentBytes > uint64(^uint(0)>>1) || limits.MaxTransactionBytes > uint64(^uint(0)>>1) || limits.MaxStateBytes > uint64(^uint(0)>>1) ||
		limits.MaxIntentBytes > uint64(^uint(0)>>1) {
		return Limits{}, fmt.Errorf("%w: encoded byte limit exceeds platform int", ErrLimit)
	}
	return limits, nil
}

func (service *Service) withSession(ctx context.Context, action func(*projectSession) error) error {
	return service.withLockedRoot(ctx, func(root *os.Root) error {
		session := &projectSession{service: service, root: root}
		if err := session.recover(ctx); err != nil {
			return err
		}
		return action(session)
	})
}

func (service *Service) withLockedRoot(ctx context.Context, action func(*os.Root) error) error {
	if service == nil || service.dir == "" {
		return ErrServiceClosed
	}
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !lockingSupported() {
		return ErrUnsupported
	}
	gate := processGate(service.dir)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()

	if err := ensureMetadataDirectory(service.dir); err != nil {
		return err
	}
	lockPath := filepath.Join(service.dir, filepath.FromSlash(lockName))
	if err := rejectNonRegularIfExists(lockPath); err != nil {
		return err
	}
	var lock *fileLock
	for {
		acquired, err := tryFileLock(lockPath)
		if err == nil {
			lock = acquired
			break
		}
		if !errors.Is(err, ErrBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer lock.release()

	root, err := os.OpenRoot(service.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	// A v2 initialization intent reserves this root before its first document
	// projection exists. Never let v1 creation or recovery overwrite that intent.
	if _, err := root.Lstat(".cutly/v2"); err == nil {
		return fmt.Errorf("%w: project root is reserved for Document v2", ErrInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return action(root)
}

func processGate(path string) chan struct{} {
	created := make(chan struct{}, 1)
	created <- struct{}{}
	actual, _ := processGates.LoadOrStore(path, created)
	return actual.(chan struct{})
}

func ensureMetadataDirectory(projectDir string) error {
	path := filepath.Join(projectDir, metadataDirectory)
	created := false
	if err := os.Mkdir(path, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s must be a non-symlink directory", ErrInvalid, metadataDirectory)
	}
	if created {
		if err := syncDirectory(projectDir); err != nil {
			return fmt.Errorf("sync project directory after metadata creation: %w", err)
		}
	}
	return nil
}

func rejectNonRegularIfExists(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s must be a regular non-symlink file", ErrInvalid, filepath.Base(path))
	}
	return nil
}

func (session *projectSession) recover(ctx context.Context) error {
	document, documentHash, err := session.readDocument(ctx)
	if err != nil {
		return err
	}
	state, stateHash, exists, err := session.readState(ctx)
	if err != nil {
		return err
	}
	intent, intentExists, err := session.readIntent(ctx)
	if err != nil {
		return err
	}
	marker, markerExists, err := session.readRegular(ctx, markerName, uint64(len(markerBytes)))
	if err != nil {
		return err
	}
	if markerExists && !bytes.Equal(marker, markerBytes) {
		return fmt.Errorf("%w: project initialization marker is invalid", ErrInvalid)
	}
	if !exists && markerExists && !intentExists {
		return fmt.Errorf("%w: project state is missing after initialization", ErrInvalid)
	}
	if !exists && !intentExists {
		state = newDiskState(document, documentHash)
		encoded, encodeErr := videoedit.CanonicalJSON(state)
		if encodeErr != nil {
			return encodeErr
		}
		if uint64(len(encoded)) > session.service.limits.MaxStateBytes {
			return fmt.Errorf("%w: initial state exceeds max state bytes", ErrLimit)
		}
		if err := session.atomicWrite(ctx, stateName, encoded); err != nil {
			return err
		}
		stateHash = digestBytes(encoded)
		exists = true
	}
	if intentExists {
		if err := session.validateIntent(intent); err != nil {
			return err
		}
		if documentHash != intent.PreviousDocumentHash && documentHash != intent.NextDocumentHash {
			return fmt.Errorf("%w: current document matches neither side of recovery intent", ErrInvalid)
		}
		if exists && stateHash != intent.PreviousStateHash && stateHash != intent.NextStateHash {
			return fmt.Errorf("%w: current state matches neither side of recovery intent", ErrInvalid)
		}
		recoveryCtx := context.WithoutCancel(ctx)
		if documentHash != intent.NextDocumentHash {
			encoded, encodeErr := videoedit.CanonicalJSON(intent.NextDocument)
			if encodeErr != nil {
				return encodeErr
			}
			if err := session.atomicWrite(recoveryCtx, DocumentName, encoded); err != nil {
				return err
			}
		}
		if !exists || stateHash != intent.NextStateHash {
			encoded, encodeErr := videoedit.CanonicalJSON(intent.NextState)
			if encodeErr != nil {
				return encodeErr
			}
			if err := session.atomicWrite(recoveryCtx, stateName, encoded); err != nil {
				return err
			}
		}
		if err := session.removeIntent(); err != nil {
			return err
		}
		document = intent.NextDocument
		documentHash = intent.NextDocumentHash
		state = intent.NextState
		stateHash = intent.NextStateHash
		exists = true
	}
	if !markerExists {
		if err := session.atomicWrite(context.WithoutCancel(ctx), markerName, markerBytes); err != nil {
			return err
		}
	}
	if err := session.validateState(state, document, documentHash); err != nil {
		return err
	}
	session.document = document
	session.documentHash = documentHash
	session.state = state
	session.stateHash = stateHash
	return nil
}

func newDiskState(document videoedit.Document, documentHash string) diskState {
	state := diskState{
		Version: StateVersion, DocumentID: document.ID, DocumentRevision: document.Revision,
		DocumentHash: documentHash, Plans: []diskPlan{}, History: []HistoryRecord{},
	}
	_ = sealState(&state)
	return state
}

func (session *projectSession) readDocument(ctx context.Context) (videoedit.Document, string, error) {
	raw, exists, err := session.readRegular(ctx, DocumentName, session.service.limits.MaxDocumentBytes)
	if err != nil {
		return videoedit.Document{}, "", err
	}
	if !exists {
		return videoedit.Document{}, "", ErrProjectNotFound
	}
	var document videoedit.Document
	if err := wirejson.Decode(raw, &document); err != nil {
		return videoedit.Document{}, "", fmt.Errorf("%w: document decode: %v", ErrInvalid, err)
	}
	if diagnostics := videoedit.ValidateDocument(document); videoedit.HasErrorDiagnostics(diagnostics) {
		return videoedit.Document{}, "", fmt.Errorf("%w: document validation: %s", ErrInvalid, diagnostics[0].Code)
	}
	if !documentIdentitiesAdmitted(document, session.service.limits.MaxIdentityBytes) {
		return videoedit.Document{}, "", fmt.Errorf("%w: document identity exceeds configured bounds", ErrLimit)
	}
	canonical, err := videoedit.CanonicalJSON(document)
	if err != nil {
		return videoedit.Document{}, "", err
	}
	if !bytes.Equal(raw, canonical) {
		return videoedit.Document{}, "", fmt.Errorf("%w: %s is not canonical", ErrInvalid, DocumentName)
	}
	hash, err := videoedit.DocumentHash(document)
	return document, hash, err
}

func (session *projectSession) readState(ctx context.Context) (diskState, string, bool, error) {
	raw, exists, err := session.readRegular(ctx, stateName, session.service.limits.MaxStateBytes)
	if err != nil || !exists {
		return diskState{}, "", exists, err
	}
	var state diskState
	if err := wirejson.Decode(raw, &state); err != nil {
		return diskState{}, "", true, fmt.Errorf("%w: state decode: %v", ErrInvalid, err)
	}
	canonical, err := videoedit.CanonicalJSON(state)
	if err != nil {
		return diskState{}, "", true, err
	}
	if !bytes.Equal(raw, canonical) {
		return diskState{}, "", true, fmt.Errorf("%w: state is not canonical", ErrInvalid)
	}
	return state, digestBytes(canonical), true, nil
}

func (session *projectSession) readIntent(ctx context.Context) (commitIntent, bool, error) {
	raw, exists, err := session.readRegular(ctx, intentName, session.service.limits.MaxIntentBytes)
	if err != nil || !exists {
		return commitIntent{}, exists, err
	}
	var intent commitIntent
	if err := wirejson.Decode(raw, &intent); err != nil {
		return commitIntent{}, true, fmt.Errorf("%w: intent decode: %v", ErrInvalid, err)
	}
	canonical, err := videoedit.CanonicalJSON(intent)
	if err != nil {
		return commitIntent{}, true, err
	}
	if !bytes.Equal(raw, canonical) {
		return commitIntent{}, true, fmt.Errorf("%w: recovery intent is not canonical", ErrInvalid)
	}
	return intent, true, nil
}

func (session *projectSession) readRegular(ctx context.Context, name string, maximum uint64) ([]byte, bool, error) {
	linkedBefore, err := session.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !linkedBefore.Mode().IsRegular() || linkedBefore.Mode()&os.ModeSymlink != 0 || linkedBefore.Size() < 0 || uint64(linkedBefore.Size()) > maximum {
		return nil, true, fmt.Errorf("%w: %s is not an admitted regular file", ErrInvalid, name)
	}
	file, err := session.root.Open(name)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	openedBefore, err := file.Stat()
	if err != nil || !openedBefore.Mode().IsRegular() || !os.SameFile(linkedBefore, openedBefore) {
		return nil, true, fmt.Errorf("%w: %s changed while opening", ErrInvalid, name)
	}
	raw, err := readBounded(ctx, file, maximum)
	if err != nil {
		return nil, true, err
	}
	openedAfter, err := file.Stat()
	if err != nil || openedAfter.Size() != openedBefore.Size() || !openedAfter.ModTime().Equal(openedBefore.ModTime()) {
		return nil, true, fmt.Errorf("%w: %s changed while reading", ErrInvalid, name)
	}
	linkedAfter, err := session.root.Lstat(name)
	if err != nil || !linkedAfter.Mode().IsRegular() || !os.SameFile(openedAfter, linkedAfter) {
		return nil, true, fmt.Errorf("%w: %s path changed while reading", ErrInvalid, name)
	}
	return raw, true, nil
}

func readBounded(ctx context.Context, reader io.Reader, maximum uint64) ([]byte, error) {
	result := make([]byte, 0, minInt(int(maximum), 32<<10))
	buffer := make([]byte, 32<<10)
	var total uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			total += uint64(count)
			if total > maximum {
				return nil, fmt.Errorf("%w: encoded file exceeds %d bytes", ErrLimit, maximum)
			}
			result = append(result, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (session *projectSession) validateIntent(intent commitIntent) error {
	if intent.Version != StateVersion || !validDigest(intent.PreviousDocumentHash) || !validDigest(intent.PreviousStateHash) {
		return fmt.Errorf("%w: malformed recovery intent identity", ErrInvalid)
	}
	nextDocumentRaw, err := videoedit.CanonicalJSON(intent.NextDocument)
	if err != nil || uint64(len(nextDocumentRaw)) > session.service.limits.MaxDocumentBytes || digestBytes(nextDocumentRaw) != intent.NextDocumentHash {
		return fmt.Errorf("%w: recovery intent document hash mismatch", ErrInvalid)
	}
	if diagnostics := videoedit.ValidateDocument(intent.NextDocument); videoedit.HasErrorDiagnostics(diagnostics) {
		return fmt.Errorf("%w: recovery intent document invalid", ErrInvalid)
	}
	nextStateRaw, err := videoedit.CanonicalJSON(intent.NextState)
	if err != nil || uint64(len(nextStateRaw)) > session.service.limits.MaxStateBytes || digestBytes(nextStateRaw) != intent.NextStateHash {
		return fmt.Errorf("%w: recovery intent state hash mismatch", ErrInvalid)
	}
	return session.validateState(intent.NextState, intent.NextDocument, intent.NextDocumentHash)
}

func (session *projectSession) validateState(state diskState, document videoedit.Document, documentHash string) error {
	fingerprint, err := stateFingerprint(state)
	if err != nil || !validDigest(state.Fingerprint) || fingerprint != state.Fingerprint {
		return fmt.Errorf("%w: state fingerprint mismatch", ErrInvalid)
	}
	if state.Version != StateVersion || state.DocumentID != document.ID || state.DocumentRevision != document.Revision || state.DocumentHash != documentHash {
		return fmt.Errorf("%w: state/document binding mismatch", ErrInvalid)
	}
	if !documentIdentitiesAdmitted(document, session.service.limits.MaxIdentityBytes) {
		return fmt.Errorf("%w: document identity exceeds configured bounds", ErrLimit)
	}
	if uint64(len(state.Plans)) > session.service.limits.MaxPlans || uint64(len(state.History)) > session.service.limits.MaxHistory {
		return fmt.Errorf("%w: state record count", ErrLimit)
	}
	previousPlanID := ""
	for index := range state.Plans {
		plan := &state.Plans[index]
		if strings.TrimSpace(plan.Transaction.ID) == "" || (index > 0 && plan.Transaction.ID <= previousPlanID) {
			return fmt.Errorf("%w: plans are not unique and sorted", ErrInvalid)
		}
		previousPlanID = plan.Transaction.ID
		if uint64(len(plan.Transaction.Operations)) > session.service.limits.MaxOperations {
			return fmt.Errorf("%w: plan operation count", ErrLimit)
		}
		if !transactionIdentitiesAdmitted(plan.Transaction, session.service.limits.MaxIdentityBytes) {
			return fmt.Errorf("%w: plan identity exceeds configured bounds", ErrLimit)
		}
		digest, err := videoedit.TransactionDigest(plan.Transaction)
		if err != nil || digest != plan.Digest || !validDigest(plan.BaseHash) {
			return fmt.Errorf("%w: plan transaction digest mismatch", ErrInvalid)
		}
		candidateRaw, err := videoedit.CanonicalJSON(plan.Candidate)
		candidateHash := digestBytes(candidateRaw)
		if err != nil || plan.Transaction.ExpectedRevision == ^videoedit.Revision(0) ||
			uint64(len(candidateRaw)) > session.service.limits.MaxDocumentBytes || candidateHash != plan.CandidateHash || plan.Commit.DocumentHash != candidateHash ||
			plan.Commit.TransactionID != plan.Transaction.ID || plan.Commit.IdempotencyKey != plan.Transaction.IdempotencyKey ||
			plan.Commit.Digest != plan.Digest || plan.Commit.Inverse.ExpectedRevision != plan.Commit.Revision ||
			plan.Commit.Revision != plan.Candidate.Revision || plan.Candidate.Revision != plan.Transaction.ExpectedRevision+1 ||
			plan.Candidate.ID != state.DocumentID {
			return fmt.Errorf("%w: plan candidate/commit binding mismatch", ErrInvalid)
		}
		if diagnostics := videoedit.ValidateDocument(plan.Candidate); videoedit.HasErrorDiagnostics(diagnostics) {
			return fmt.Errorf("%w: plan candidate document invalid", ErrInvalid)
		}
		if uint64(len(plan.Commit.Inverse.Operations)) > session.service.limits.MaxOperations ||
			!transactionIdentitiesAdmitted(plan.Commit.Inverse, session.service.limits.MaxIdentityBytes) {
			return fmt.Errorf("%w: plan inverse exceeds configured bounds", ErrLimit)
		}
		switch plan.State {
		case PlanPending, PlanApplied, PlanDiscarded:
		default:
			return fmt.Errorf("%w: plan state invalid", ErrInvalid)
		}
	}
	ledger := make(map[string]string, len(state.History))
	historyTransactions := make(map[string]struct{}, len(state.History))
	var previousSequence uint64
	for index := range state.History {
		record := &state.History[index]
		if record.Sequence == 0 || record.Sequence <= previousSequence || record.Sequence > state.Sequence {
			return fmt.Errorf("%w: history sequence invalid", ErrInvalid)
		}
		previousSequence = record.Sequence
		if uint64(len(record.Transaction.Operations)) > session.service.limits.MaxOperations {
			return fmt.Errorf("%w: history operation count", ErrLimit)
		}
		if !transactionIdentitiesAdmitted(record.Transaction, session.service.limits.MaxIdentityBytes) {
			return fmt.Errorf("%w: history identity exceeds configured bounds", ErrLimit)
		}
		digest, err := videoedit.TransactionDigest(record.Transaction)
		if err != nil || record.BaseRevision == ^videoedit.Revision(0) || record.Commit.TransactionID != record.Transaction.ID ||
			record.Commit.IdempotencyKey != record.Transaction.IdempotencyKey || record.Commit.Digest != digest ||
			record.Commit.Revision != record.BaseRevision+1 || record.Transaction.ExpectedRevision != record.BaseRevision ||
			record.Commit.Inverse.ExpectedRevision != record.Commit.Revision ||
			!validDigest(record.BaseDocumentHash) || !validDigest(record.Commit.DocumentHash) {
			return fmt.Errorf("%w: history record binding mismatch", ErrInvalid)
		}
		if _, duplicate := historyTransactions[record.Transaction.ID]; duplicate {
			return fmt.Errorf("%w: duplicate history transaction ID", ErrInvalid)
		}
		if record.RevertsTransactionID != "" {
			if _, exists := historyTransactions[record.RevertsTransactionID]; !exists {
				return fmt.Errorf("%w: revert target is not earlier history", ErrInvalid)
			}
		}
		historyTransactions[record.Transaction.ID] = struct{}{}
		if previous, exists := ledger[record.Transaction.IdempotencyKey]; exists && previous != digest {
			return fmt.Errorf("%w: history idempotency conflict", ErrInvalid)
		}
		ledger[record.Transaction.IdempotencyKey] = digest
		if uint64(len(record.Commit.Inverse.Operations)) > session.service.limits.MaxOperations ||
			!transactionIdentitiesAdmitted(record.Commit.Inverse, session.service.limits.MaxIdentityBytes) {
			return fmt.Errorf("%w: history inverse exceeds configured bounds", ErrLimit)
		}
		if index > 0 {
			previous := state.History[index-1]
			if record.BaseRevision != previous.Commit.Revision || record.BaseDocumentHash != previous.Commit.DocumentHash {
				return fmt.Errorf("%w: history chain is discontinuous", ErrInvalid)
			}
		}
	}
	for _, plan := range state.Plans {
		record, committed := findHistory(state.History, plan.Transaction.ID)
		if (plan.State == PlanApplied) != committed {
			return fmt.Errorf("%w: plan state/history mismatch", ErrInvalid)
		}
		if committed && (record.Commit.Digest != plan.Digest || record.Commit.DocumentHash != plan.CandidateHash || record.BaseDocumentHash != plan.BaseHash) {
			return fmt.Errorf("%w: applied plan/history binding mismatch", ErrInvalid)
		}
	}
	if len(state.History) != 0 {
		latest := state.History[len(state.History)-1]
		if latest.Commit.Revision != state.DocumentRevision || latest.Commit.DocumentHash != state.DocumentHash {
			return fmt.Errorf("%w: current state is not bound to latest history", ErrInvalid)
		}
	}
	return nil
}

func (session *projectSession) atomicWrite(ctx context.Context, name string, data []byte) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.inject("atomic.before_create:" + name); err != nil {
		return err
	}
	parent := filepath.ToSlash(filepath.Dir(name))
	if parent == "." {
		parent = ""
	}
	if existing, err := session.root.Lstat(name); err == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: atomic target %s is not regular", ErrInvalid, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := filepath.ToSlash(filepath.Join(parent, ".tmp-"+randomToken()))
	file, err := session.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			_ = session.root.Remove(temporary)
		}
	}()
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := offset + 64<<10
		if end > len(data) {
			end = len(data)
		}
		count, writeErr := file.Write(data[offset:end])
		if writeErr != nil {
			return writeErr
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		offset += count
	}
	if err := session.inject("atomic.before_file_sync:" + name); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.inject("atomic.before_rename:" + name); err != nil {
		return err
	}
	if err := session.root.Rename(temporary, name); err != nil {
		return err
	}
	published = true
	if err := session.inject("atomic.after_rename:" + name); err != nil {
		return fmt.Errorf("%w: %v", ErrCommitUnknown, err)
	}
	parentPath := session.service.dir
	if parent != "" {
		parentPath = filepath.Join(parentPath, filepath.FromSlash(parent))
	}
	if err := syncDirectory(parentPath); err != nil {
		return fmt.Errorf("%w: sync parent: %v", ErrCommitUnknown, err)
	}
	return nil
}

func (session *projectSession) writeState(ctx context.Context, state diskState) error {
	if err := sealState(&state); err != nil {
		return err
	}
	if err := session.validateState(state, session.document, session.documentHash); err != nil {
		return err
	}
	encoded, err := videoedit.CanonicalJSON(state)
	if err != nil {
		return err
	}
	if uint64(len(encoded)) > session.service.limits.MaxStateBytes {
		return fmt.Errorf("%w: state exceeds %d bytes", ErrLimit, session.service.limits.MaxStateBytes)
	}
	if err := session.atomicWrite(ctx, stateName, encoded); err != nil {
		return err
	}
	session.state = state
	session.stateHash = digestBytes(encoded)
	return nil
}

func (session *projectSession) commit(ctx context.Context, nextDocument videoedit.Document, nextState diskState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nextDocumentRaw, err := videoedit.CanonicalJSON(nextDocument)
	if err != nil {
		return err
	}
	if uint64(len(nextDocumentRaw)) > session.service.limits.MaxDocumentBytes {
		return fmt.Errorf("%w: document exceeds %d bytes", ErrLimit, session.service.limits.MaxDocumentBytes)
	}
	nextDocumentHash, err := videoedit.DocumentHash(nextDocument)
	if err != nil {
		return err
	}
	nextState.DocumentID = nextDocument.ID
	nextState.DocumentRevision = nextDocument.Revision
	nextState.DocumentHash = nextDocumentHash
	if err := sealState(&nextState); err != nil {
		return err
	}
	if err := session.validateState(nextState, nextDocument, nextDocumentHash); err != nil {
		return err
	}
	nextStateRaw, err := videoedit.CanonicalJSON(nextState)
	if err != nil {
		return err
	}
	if uint64(len(nextStateRaw)) > session.service.limits.MaxStateBytes {
		return fmt.Errorf("%w: state exceeds %d bytes", ErrLimit, session.service.limits.MaxStateBytes)
	}
	intent := commitIntent{
		Version:              StateVersion,
		PreviousDocumentHash: session.documentHash,
		PreviousStateHash:    session.stateHash,
		NextDocument:         nextDocument,
		NextDocumentHash:     nextDocumentHash,
		NextState:            nextState,
		NextStateHash:        digestBytes(nextStateRaw),
	}
	intentRaw, err := videoedit.CanonicalJSON(intent)
	if err != nil {
		return err
	}
	if uint64(len(intentRaw)) > session.service.limits.MaxIntentBytes {
		return fmt.Errorf("%w: intent exceeds %d bytes", ErrLimit, session.service.limits.MaxIntentBytes)
	}
	if err := session.atomicWrite(ctx, intentName, intentRaw); err != nil {
		return err
	}
	commitCtx := context.WithoutCancel(ctx)
	if err := session.atomicWrite(commitCtx, DocumentName, nextDocumentRaw); err != nil {
		return commitUnknown(err)
	}
	if err := session.atomicWrite(commitCtx, stateName, nextStateRaw); err != nil {
		return commitUnknown(err)
	}
	if err := session.removeIntent(); err != nil {
		return commitUnknown(err)
	}
	session.document = nextDocument
	session.documentHash = nextDocumentHash
	session.state = nextState
	session.stateHash = intent.NextStateHash
	return nil
}

func commitUnknown(err error) error {
	if err == nil || errors.Is(err, ErrCommitUnknown) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrCommitUnknown, err)
}

func (session *projectSession) removeIntent() error {
	if err := session.inject("intent.before_remove"); err != nil {
		return err
	}
	err := session.root.Remove(intentName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := session.inject("intent.after_remove"); err != nil {
		return fmt.Errorf("%w: %v", ErrCommitUnknown, err)
	}
	if err := syncDirectory(filepath.Join(session.service.dir, metadataDirectory)); err != nil {
		return fmt.Errorf("%w: sync intent removal: %v", ErrCommitUnknown, err)
	}
	return nil
}

func (session *projectSession) inject(point string) error {
	if session.service.fault == nil {
		return nil
	}
	return session.service.fault(point)
}

func sortPlans(plans []diskPlan) {
	sort.Slice(plans, func(left, right int) bool {
		return plans[left].Transaction.ID < plans[right].Transaction.ID
	})
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func stateFingerprint(state diskState) (string, error) {
	state.Fingerprint = ""
	encoded, err := videoedit.CanonicalJSON(state)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func sealState(state *diskState) error {
	fingerprint, err := stateFingerprint(*state)
	if err != nil {
		return err
	}
	state.Fingerprint = fingerprint
	return nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func randomToken() string {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
