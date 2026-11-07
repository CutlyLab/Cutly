package localv2

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	stateName  = ".cutly/v2/state.json"
	intentName = ".cutly/v2/intent.json"
)

// Service retains only configuration, never cached project authority. Every
// call reloads current projections and recovers under the cross-process lock.
type Service struct {
	dir    string
	limits Limits
	fault  func(string) error // package-private deterministic fault injection
}

type session struct {
	service   *Service
	root      *os.Root
	state     diskState
	stateHash string
	durable   bool
	staging   bool // Revert stages its plan without publishing a separate state.
}

func configure(dir string, options Options) (*Service, error) {
	if !supported() {
		return nil, ErrUnsupported
	}
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Dir(dir) == dir {
		return nil, fmt.Errorf("%w: clean absolute non-volume root required", ErrInvalid)
	}
	// Reject symlinks in every host path component, not only the final root.
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	l := options.Limits
	if l == (Limits{}) {
		l = DefaultLimits()
	}
	a := []uint64{l.MaxDocumentBytes, l.MaxTransactionBytes, l.MaxRecordBytes, l.MaxStateBytes, l.MaxPlans, l.MaxHistory, l.MaxOperations, l.MaxIdentityBytes}
	hard := DefaultLimits()
	b := []uint64{hard.MaxDocumentBytes, hard.MaxTransactionBytes, hard.MaxRecordBytes, hard.MaxStateBytes, hard.MaxPlans, hard.MaxHistory, hard.MaxOperations, hard.MaxIdentityBytes}
	for i, v := range a {
		if v == 0 || v > b[i] {
			return nil, ErrLimit
		}
	}
	return &Service{dir: dir, limits: l}, nil
}

func (s *Service) locked(ctx context.Context, init bool, fn func(*session) error) (err error) {
	if s == nil || ctx == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for p := s.dir; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	before, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return ErrInvalid
	}
	if init {
		if err := mkdir(root, ".cutly"); err != nil {
			return err
		}
	} else if err := directory(root, ".cutly"); err != nil {
		return err
	}
	var lock *os.File
	for {
		lock, err = lockFile(root)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	x := &session{service: s, root: root}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, p)
		}
		if err != nil && x.durable {
			err = errors.Join(ErrCommitUnknown, err)
		}
	}()
	for _, legacy := range []string{".cutly/state.json", ".cutly/initialized", ".cutly/commit.intent.json"} {
		if _, e := root.Lstat(legacy); e == nil {
			return ErrProjectExists
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if init {
		for _, d := range []string{".cutly/v2", ".cutly/v2/documents", ".cutly/v2/plans", ".cutly/v2/commits", ".cutly/v2/states"} {
			if err := mkdir(root, d); err != nil {
				return err
			}
		}
	}
	for _, d := range []string{".cutly/v2", ".cutly/v2/documents", ".cutly/v2/plans", ".cutly/v2/commits", ".cutly/v2/states"} {
		if err := directory(root, d); err != nil {
			return err
		}
	}
	return fn(x)
}

func directory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	return nil
}
func mkdir(root *os.Root, name string) error {
	err := root.Mkdir(name, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := directory(root, name); err != nil {
		return err
	}
	return syncDir(root, filepath.ToSlash(filepath.Dir(name)))
}
func syncDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (x *session) inject(point string) error {
	if x.service.fault != nil {
		return x.service.fault(point)
	}
	return nil
}

func (x *session) read(ctx context.Context, name string, maximum uint64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	before, err := x.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 {
		return nil, true, ErrInvalid
	}
	if uint64(before.Size()) > maximum {
		return nil, true, ErrLimit
	}
	f, err := x.root.OpenFile(name, os.O_RDONLY|noFollowFlag(), 0)
	if err != nil {
		return nil, true, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, true, ErrInvalid
	}
	raw := make([]byte, 0, min(int(before.Size()), 32<<10))
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		n, e := f.Read(buffer)
		if uint64(len(raw))+uint64(n) > maximum {
			return nil, true, ErrLimit
		}
		raw = append(raw, buffer[:n]...)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, true, e
		}
	}
	after, e := f.Stat()
	linked, e2 := x.root.Lstat(name)
	if e != nil || e2 != nil || !linked.Mode().IsRegular() || !os.SameFile(after, linked) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, true, ErrInvalid
	}
	return raw, true, nil
}

func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// publish stages and fsyncs bytes, then creates an immutable name via Link.
// Link has atomic no-replace semantics and operates inside os.Root. Mutable
// projections alone use Rename. No caller-supplied ID is ever a path component.
func (x *session) publish(ctx context.Context, name string, raw []byte, replace bool) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.ToSlash(filepath.Dir(name))
	tmp := parent + "/.tmp-" + token()
	f, err := x.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); _ = x.root.Remove(tmp) }()
	if err := x.inject("before_write:" + name); err != nil {
		return err
	}
	for offset := 0; offset < len(raw); {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := f.Write(raw[offset:min(offset+64<<10, len(raw))])
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		offset += n
	}
	if err := x.inject("before_sync:" + name); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := x.inject("before_publish:" + name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if replace {
		info, e := x.root.Lstat(name)
		if e == nil && !info.Mode().IsRegular() {
			return ErrInvalid
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		err = x.root.Rename(tmp, name)
	} else {
		err = x.root.Link(tmp, name)
	}
	if err != nil {
		return err
	}
	// The intent may exist even if the following injected panic or fsync fails.
	if name == intentName {
		x.durable = true
	}
	if err := x.inject("after_publish:" + name); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	if err := syncDir(x.root, parent); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	if err := x.inject("after_sync:" + name); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	return nil
}

func (x *session) put(ctx context.Context, kind string, raw []byte) (string, error) {
	hash := digest(raw)
	name, _ := objectName(kind, hash)
	if old, exists, err := x.read(ctx, name, uint64(len(raw))); err != nil {
		return "", err
	} else if exists {
		if !bytes.Equal(old, raw) {
			return "", ErrInvalid
		}
		if err := syncDir(x.root, filepath.ToSlash(filepath.Dir(name))); err != nil {
			return "", err
		}
		return hash, nil
	}
	if err := x.publish(ctx, name, raw, false); err != nil {
		return "", err
	}
	return hash, nil
}
func (x *session) object(ctx context.Context, kind, hash string, maximum uint64) ([]byte, error) {
	name, err := objectName(kind, hash)
	if err != nil {
		return nil, err
	}
	raw, exists, err := x.read(ctx, name, maximum)
	if err != nil {
		return nil, err
	}
	if !exists || digest(raw) != hash {
		return nil, ErrInvalid
	}
	return raw, nil
}

func (x *session) transition(ctx context.Context, next diskState, documentRaw []byte) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	nextRaw, err := canonical(next, x.service.limits.MaxStateBytes)
	if err != nil {
		return err
	}
	if err := x.validateState(next); err != nil {
		return err
	}
	var previous diskState
	if x.stateHash != "" {
		oldRaw, e := x.object(ctx, "states", x.stateHash, x.service.limits.MaxStateBytes)
		if e != nil {
			return e
		}
		if e := decodeCanonical(oldRaw, &previous, x.service.limits.MaxStateBytes); e != nil {
			return e
		}
	}
	if err := x.validateTransition(ctx, previous, next, x.stateHash == ""); err != nil {
		return err
	}
	// Immutable dependencies are complete and synced before the commit point.
	if _, err := x.put(ctx, "documents", documentRaw); err != nil {
		return err
	}
	nextHash, err := x.put(ctx, "states", nextRaw)
	if err != nil {
		return err
	}
	intent := commitIntent{Version: Version, OldStateHash: x.stateHash, NewStateHash: nextHash, OldDocumentHash: x.state.DocumentHash, NewDocumentHash: next.DocumentHash, OldCommitHash: x.state.LatestCommitHash, NewCommitHash: next.LatestCommitHash}
	raw, err := canonical(intent, 4096)
	if err != nil {
		return err
	}
	if err := x.publish(ctx, intentName, raw, false); err != nil {
		return err
	}
	// Cancellation after intent is no longer a promise of no mutation. Recovery
	// ignores cancellation only while finishing this already durable transition.
	return x.finish(context.WithoutCancel(ctx), intent, next, nextRaw, documentRaw)
}

func (x *session) finish(ctx context.Context, intent commitIntent, next diskState, stateRaw, documentRaw []byte) error {
	if err := x.publish(ctx, DocumentName, documentRaw, true); err != nil {
		return err
	}
	if err := x.publish(ctx, stateName, stateRaw, true); err != nil {
		return err
	}
	if err := x.inject("before_remove_intent"); err != nil {
		return err
	}
	if err := x.root.Remove(intentName); err != nil {
		return err
	}
	if err := x.inject("after_remove_intent"); err != nil {
		return err
	}
	if err := syncDir(x.root, ".cutly/v2"); err != nil {
		return err
	}
	if err := x.inject("after_remove_sync"); err != nil {
		return err
	}
	x.state = next
	x.stateHash = intent.NewStateHash
	x.durable = false
	return nil
}

func (x *session) load(ctx context.Context) error {
	raw, exists, err := x.read(ctx, intentName, 4096)
	if err != nil {
		return err
	}
	if exists {
		x.durable = true
		var in commitIntent
		if err := decodeCanonical(raw, &in, 4096); err != nil {
			return err
		}
		if in.Version != Version || !validHash(in.NewStateHash) || !validHash(in.NewDocumentHash) || in.NewCommitHash != "" && !validHash(in.NewCommitHash) {
			return ErrInvalid
		}
		if (in.OldStateHash == "") != (in.OldDocumentHash == "") {
			return ErrInvalid
		}
		nextRaw, err := x.object(ctx, "states", in.NewStateHash, x.service.limits.MaxStateBytes)
		if err != nil {
			return err
		}
		var next diskState
		if err := decodeCanonical(nextRaw, &next, x.service.limits.MaxStateBytes); err != nil {
			return err
		}
		if next.DocumentHash != in.NewDocumentHash || next.LatestCommitHash != in.NewCommitHash {
			return ErrInvalid
		}
		if err := x.validateState(next); err != nil {
			return err
		}
		docRaw, err := x.object(ctx, "documents", in.NewDocumentHash, x.service.limits.MaxDocumentBytes)
		if err != nil {
			return err
		}
		doc, err := decodeDocument(docRaw, x.service.limits.MaxDocumentBytes)
		if err != nil {
			return err
		}
		if doc.ID != next.DocumentID || doc.Revision != next.Revision {
			return ErrInvalid
		}
		var old diskState
		if in.OldStateHash != "" {
			oldRaw, err := x.object(ctx, "states", in.OldStateHash, x.service.limits.MaxStateBytes)
			if err != nil {
				return err
			}
			if err := decodeCanonical(oldRaw, &old, x.service.limits.MaxStateBytes); err != nil {
				return err
			}
			if err := x.validateState(old); err != nil {
				return err
			}
			if old.DocumentHash != in.OldDocumentHash || old.LatestCommitHash != in.OldCommitHash || old.Sequence == ^uint64(0) || next.Sequence != old.Sequence+1 || old.DocumentID != next.DocumentID {
				return ErrInvalid
			}
		}
		if err := x.validateRecords(ctx, next); err != nil {
			return err
		}
		if err := x.validateTransition(ctx, old, next, in.OldStateHash == ""); err != nil {
			return err
		}
		// Publication order admits old/old, new/old and new/new only. Reject
		// old-document/new-state rather than accepting arbitrary Cartesian pairs.
		currentDoc, de, err := x.read(ctx, DocumentName, x.service.limits.MaxDocumentBytes)
		if err != nil {
			return err
		}
		currentState, se, err := x.read(ctx, stateName, x.service.limits.MaxStateBytes)
		if err != nil {
			return err
		}
		dh, sh := "", ""
		if de {
			dh = digest(currentDoc)
		}
		if se {
			sh = digest(currentState)
		}
		if !(dh == in.OldDocumentHash && sh == in.OldStateHash || dh == in.NewDocumentHash && sh == in.OldStateHash || dh == in.NewDocumentHash && sh == in.NewStateHash) {
			return fmt.Errorf("%w: inadmissible recovery pair", ErrInvalid)
		}
		if err := x.finish(context.WithoutCancel(ctx), in, next, nextRaw, docRaw); err != nil {
			return err
		}
	}
	stateRaw, exists, err := x.read(ctx, stateName, x.service.limits.MaxStateBytes)
	if err != nil {
		return err
	}
	if !exists {
		return ErrProjectNotFound
	}
	var state diskState
	if err := decodeCanonical(stateRaw, &state, x.service.limits.MaxStateBytes); err != nil {
		return err
	}
	if err := x.validateState(state); err != nil {
		return err
	}
	hash := digest(stateRaw)
	stored, err := x.object(ctx, "states", hash, x.service.limits.MaxStateBytes)
	if err != nil || !bytes.Equal(stored, stateRaw) {
		return ErrInvalid
	}
	docRaw, exists, err := x.read(ctx, DocumentName, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	if !exists || digest(docRaw) != state.DocumentHash {
		return ErrConflict
	}
	doc, err := decodeDocument(docRaw, x.service.limits.MaxDocumentBytes)
	if err != nil {
		return err
	}
	if doc.ID != state.DocumentID || doc.Revision != state.Revision {
		return ErrInvalid
	}
	if err := x.validateRecords(ctx, state); err != nil {
		return err
	}
	x.state = state
	x.stateHash = hash
	return nil
}

func (x *session) validateState(s diskState) error {
	l := x.service.limits
	if s.Version != Version || !identity(string(s.DocumentID), l.MaxIdentityBytes) || !validHash(s.DocumentHash) || uint64(len(s.Plans)) > l.MaxPlans || uint64(len(s.History)) > l.MaxHistory {
		return ErrInvalid
	}
	ids := make(map[string]bool, len(s.Plans))
	keys := make(map[string]string, len(s.Plans))
	for _, p := range s.Plans {
		if !identity(p.TransactionID, l.MaxIdentityBytes) || !identity(p.IdempotencyKey, l.MaxIdentityBytes) || ids[p.TransactionID] || !validHash(p.RecordHash) || !validHash(p.Digest) || !validHash(p.BaseDocumentHash) || !validHash(p.CandidateDocumentHash) || uint64(p.BaseRevision) == ^uint64(0) || p.NewRevision != p.BaseRevision+1 {
			return ErrInvalid
		}
		ids[p.TransactionID] = true
		if old, ok := keys[p.IdempotencyKey]; ok && old != p.Digest {
			return ErrInvalid
		}
		keys[p.IdempotencyKey] = p.Digest
		switch p.State {
		case PlanPending, PlanApplied, PlanDiscarded:
		default:
			return ErrInvalid
		}
	}
	history := make(map[string]bool, len(s.History))
	var seq uint64
	for _, h := range s.History {
		if !ids[h.TransactionID] || history[h.TransactionID] || !validHash(h.RecordHash) || !validHash(h.Digest) || h.Sequence <= seq || h.Sequence > s.Sequence {
			return ErrInvalid
		}
		p := findPlan(s, h.TransactionID)
		if p == nil || p.State != PlanApplied || p.Digest != h.Digest || p.IdempotencyKey != h.IdempotencyKey {
			return ErrInvalid
		}
		history[h.TransactionID] = true
		seq = h.Sequence
	}
	for _, p := range s.Plans {
		if (p.State == PlanApplied) != history[p.TransactionID] {
			return ErrInvalid
		}
	}
	if len(s.History) == 0 {
		if s.LatestCommitHash != "" {
			return ErrInvalid
		}
	} else {
		if s.LatestCommitHash != s.History[len(s.History)-1].RecordHash {
			return ErrInvalid
		}
		p := findPlan(s, s.History[len(s.History)-1].TransactionID)
		if p.CandidateDocumentHash != s.DocumentHash || p.NewRevision != s.Revision {
			return ErrInvalid
		}
	}
	return nil
}

func findPlan(s diskState, id string) *PlanSummary {
	for i := range s.Plans {
		if s.Plans[i].TransactionID == id {
			return &s.Plans[i]
		}
	}
	return nil
}
func findHistory(s diskState, id string) *historyRef {
	for i := range s.History {
		if s.History[i].TransactionID == id {
			return &s.History[i]
		}
	}
	return nil
}
