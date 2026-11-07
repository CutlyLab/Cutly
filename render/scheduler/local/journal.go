package schedulerlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

const (
	// MaxJournalRecordBytes, MaxJournalBytes and MaxJournalRecords are the
	// frozen local-v1 admission ceilings for one payload and one journal.
	MaxJournalRecordBytes uint32 = scheduler.MaxProtocolBytes
	MaxJournalBytes       int64  = 1 << 30
	MaxJournalRecords            = scheduler.MaxReplayEvents
)

const (
	journalMagic               = "CUTLYJ1\n"
	journalLengthOffset        = len(journalMagic)
	journalPayloadDigestOffset = journalLengthOffset + 4
	journalHeaderDigestOffset  = journalPayloadDigestOffset + sha256.Size
	journalHeaderSize          = journalHeaderDigestOffset + sha256.Size
)

var (
	ErrJournalCorrupt = errors.New("scheduler local: journal is corrupt")
	ErrJournalIO      = errors.New("scheduler local: journal I/O failed")
	ErrJournalPath    = errors.New("scheduler local: journal path is invalid")
	ErrJournalLimit   = errors.New("scheduler local: journal admission limit exceeded")
)

type journalLimits struct {
	maxBytes   int64
	maxRecords int
}

func defaultJournalLimits() journalLimits {
	return journalLimits{maxBytes: MaxJournalBytes, maxRecords: MaxJournalRecords}
}

type journalFile interface {
	io.ReaderAt
	io.Writer
	io.Seeker
	Stat() (os.FileInfo, error)
	Sync() error
	Truncate(int64) error
	Close() error
}

type Journal struct {
	mu             sync.Mutex
	file           journalFile
	replayRequired bool
	recoverySync   bool
	recordCount    int
	limits         journalLimits
}

func OpenJournal(path string) (*Journal, error) {
	return openJournal(path, syncJournalDirectory)
}

func openJournal(path string, syncDirectory func(*os.Root) error) (*Journal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, ErrJournalPath
	}
	if syncDirectory == nil {
		return nil, fmt.Errorf("%w: directory sync function is required", ErrJournalIO)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%w: open parent directory: %v", ErrJournalIO, err)
	}
	name := filepath.Base(path)
	file, created, err := openJournalFile(root, name)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("%w: stat opened journal: %v", ErrJournalIO, err)
	}
	pathInfo, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("%w: opened journal path identity changed", ErrJournalPath)
	}
	// Sync both an opened file and its parent, not only a newly created one.
	// A previous creation attempt can fail after O_EXCL and leave the same path
	// behind; treating that path as an ordinary existing journal would otherwise
	// turn an uncommitted directory entry into a successful Open.
	if err := file.Sync(); err != nil {
		if created {
			cleanupErr := cleanupNewJournal(root, name, file, info, syncDirectory)
			_ = root.Close()
			return nil, errors.Join(fmt.Errorf("%w: sync new journal: %w", ErrJournalIO, err), cleanupErr)
		}
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("%w: sync opened journal: %w", ErrJournalIO, err)
	}
	if err := syncDirectory(root); err != nil {
		if created {
			cleanupErr := cleanupNewJournal(root, name, file, info, syncDirectory)
			_ = root.Close()
			return nil, errors.Join(fmt.Errorf("%w: sync new journal directory: %w", ErrJournalIO, err), cleanupErr)
		}
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("%w: sync opened journal directory: %w", ErrJournalIO, err)
	}
	if err := root.Close(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: close parent directory: %v", ErrJournalIO, err)
	}
	return &Journal{file: file, replayRequired: info.Size() != 0, limits: defaultJournalLimits()}, nil
}

func newJournal(file journalFile) *Journal {
	return newJournalWithLimits(file, defaultJournalLimits())
}

func newJournalWithLimits(file journalFile, limits journalLimits) *Journal {
	replayRequired := true
	if info, err := file.Stat(); err == nil {
		replayRequired = info.Size() != 0
	}
	return &Journal{file: file, replayRequired: replayRequired, limits: limits}
}

func openJournalFile(root *os.Root, name string) (*os.File, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			file, createErr := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, false, fmt.Errorf("%w: create: %v", ErrJournalIO, createErr)
			}
			return file, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("%w: inspect: %v", ErrJournalIO, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, false, fmt.Errorf("%w: journal must be a regular non-symlink file", ErrJournalPath)
		}
		file, openErr := root.OpenFile(name, os.O_RDWR, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return nil, false, fmt.Errorf("%w: open: %v", ErrJournalIO, openErr)
		}
		return file, false, nil
	}
	return nil, false, fmt.Errorf("%w: journal path changed while opening", ErrJournalPath)
}

func cleanupNewJournal(root *os.Root, name string, file *os.File, openedInfo os.FileInfo,
	syncDirectory func(*os.Root) error) error {
	closeErr := file.Close()
	pathInfo, statErr := root.Lstat(name)
	if statErr != nil || !os.SameFile(openedInfo, pathInfo) {
		return errors.Join(
			wrapJournalError("close failed creation", closeErr),
			fmt.Errorf("%w: failed creation path identity changed", ErrJournalPath),
			wrapJournalError("inspect failed creation", statErr),
		)
	}
	removeErr := root.Remove(name)
	var syncErr error
	if removeErr == nil {
		syncErr = syncDirectory(root)
	}
	return errors.Join(
		wrapJournalError("close failed creation", closeErr),
		wrapJournalError("remove failed creation", removeErr),
		wrapJournalError("sync failed creation cleanup", syncErr),
	)
}

func (value *Journal) Close() error {
	if value == nil {
		return nil
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.file == nil {
		return nil
	}
	file := value.file
	if value.recoverySync {
		if err := file.Sync(); err != nil {
			return errors.Join(
				fmt.Errorf("%w: sync uncertain journal before close: %v", ErrJournalIO, err),
				scheduler.ErrCommitUnknown,
			)
		}
		value.recoverySync = false
	}
	value.file = nil
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close: %v", ErrJournalIO, err)
	}
	return nil
}

func (value *Journal) Append(ctx context.Context, event scheduler.JobEvent) error {
	if value == nil {
		return fmt.Errorf("%w: journal is closed", ErrJournalIO)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := scheduler.ValidateJobEvent(event); err != nil {
		return err
	}
	payload, err := videoedit.CanonicalJSON(event)
	if err != nil {
		return fmt.Errorf("%w: canonical event: %v", ErrJournalIO, err)
	}
	if len(payload) == 0 {
		return fmt.Errorf("%w: record length %d", ErrJournalIO, len(payload))
	}
	if uint64(len(payload)) > uint64(MaxJournalRecordBytes) {
		return fmt.Errorf("%w: record length %d", ErrJournalLimit, len(payload))
	}
	frame := make([]byte, journalHeaderSize+len(payload))
	copy(frame[:len(journalMagic)], journalMagic)
	binary.BigEndian.PutUint32(frame[journalLengthOffset:journalPayloadDigestOffset], uint32(len(payload)))
	payloadDigest := sha256.Sum256(payload)
	copy(frame[journalPayloadDigestOffset:journalHeaderDigestOffset], payloadDigest[:])
	headerDigest := sha256.Sum256(frame[:journalHeaderDigestOffset])
	copy(frame[journalHeaderDigestOffset:journalHeaderSize], headerDigest[:])
	copy(frame[journalHeaderSize:], payload)

	value.mu.Lock()
	defer value.mu.Unlock()
	if value.file == nil {
		return fmt.Errorf("%w: journal is closed", ErrJournalIO)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if value.replayRequired {
		return fmt.Errorf("%w: replay is required before append", ErrJournalIO)
	}
	if !validJournalLimits(value.limits) {
		return fmt.Errorf("%w: journal limits are invalid", ErrJournalLimit)
	}
	if !journalRecordCountAdmitted(value.recordCount+1, value.limits) {
		return fmt.Errorf("%w: record count %d", ErrJournalLimit, value.recordCount)
	}
	start, err := value.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("%w: seek append: %v", ErrJournalIO, err)
	}
	if start < 0 || start > value.limits.maxBytes || !journalByteCountAdmitted(start+int64(len(frame)), value.limits) {
		return fmt.Errorf("%w: append bytes %d + %d exceed %d", ErrJournalLimit, start, len(frame), value.limits.maxBytes)
	}
	if err := writeFull(value.file, frame); err != nil {
		return value.rollback(start, fmt.Errorf("%w: append: %v", ErrJournalIO, err))
	}
	if err := contextError(ctx); err != nil {
		return value.rollback(start, err)
	}
	if err := value.file.Sync(); err != nil {
		return value.rollback(start, fmt.Errorf("%w: sync: %v", ErrJournalIO, err))
	}
	value.recordCount++
	return nil
}

func (value *Journal) Replay(ctx context.Context) ([]scheduler.JobEvent, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: journal is closed", ErrJournalIO)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	value.mu.Lock()
	defer value.mu.Unlock()
	if value.file == nil {
		return nil, fmt.Errorf("%w: journal is closed", ErrJournalIO)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	value.replayRequired = true
	if !validJournalLimits(value.limits) {
		return nil, fmt.Errorf("%w: journal limits are invalid", ErrJournalLimit)
	}
	info, err := value.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: stat: %v", ErrJournalIO, err)
	}
	size := info.Size()
	if size < 0 {
		return nil, fmt.Errorf("%w: negative file size", ErrJournalCorrupt)
	}
	if !journalByteCountAdmitted(size, value.limits) {
		return nil, fmt.Errorf("%w: journal bytes %d exceed %d", ErrJournalLimit, size, value.limits.maxBytes)
	}
	events := make([]scheduler.JobEvent, 0)
	var offset int64
	for offset < size {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		remaining := size - offset
		if remaining < int64(journalHeaderSize) {
			if err := value.truncateTail(offset); err != nil {
				return nil, err
			}
			return value.finishReplay(events)
		}
		header := make([]byte, journalHeaderSize)
		if _, err := value.file.ReadAt(header, offset); err != nil {
			return nil, fmt.Errorf("%w: read header at %d: %v", ErrJournalIO, offset, err)
		}
		headerDigest := sha256.Sum256(header[:journalHeaderDigestOffset])
		if !bytes.Equal(header[journalHeaderDigestOffset:journalHeaderSize], headerDigest[:]) {
			return nil, fmt.Errorf("%w: header checksum at %d", ErrJournalCorrupt, offset)
		}
		if !bytes.Equal(header[:len(journalMagic)], []byte(journalMagic)) {
			return nil, fmt.Errorf("%w: magic at %d", ErrJournalCorrupt, offset)
		}
		length := binary.BigEndian.Uint32(header[journalLengthOffset:journalPayloadDigestOffset])
		if length == 0 || length > MaxJournalRecordBytes {
			return nil, fmt.Errorf("%w: length %d at %d", ErrJournalCorrupt, length, offset)
		}
		frameBytes := int64(journalHeaderSize) + int64(length)
		if remaining < frameBytes {
			if err := value.truncateTail(offset); err != nil {
				return nil, err
			}
			return value.finishReplay(events)
		}
		if !journalRecordCountAdmitted(len(events)+1, value.limits) {
			return nil, fmt.Errorf("%w: record count exceeds %d at %d", ErrJournalLimit, value.limits.maxRecords, offset)
		}
		payload := make([]byte, int(length))
		if _, err := value.file.ReadAt(payload, offset+int64(journalHeaderSize)); err != nil {
			return nil, fmt.Errorf("%w: read payload at %d: %v", ErrJournalIO, offset, err)
		}
		want := header[journalPayloadDigestOffset:journalHeaderDigestOffset]
		got := sha256.Sum256(payload)
		if !bytes.Equal(want, got[:]) {
			return nil, fmt.Errorf("%w: checksum at %d", ErrJournalCorrupt, offset)
		}
		event, err := scheduler.DecodeJobEvent(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: event at %d: %v", ErrJournalCorrupt, offset, err)
		}
		events = append(events, event)
		offset += frameBytes
	}
	if _, err := value.file.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("%w: seek end: %v", ErrJournalIO, err)
	}
	return value.finishReplay(events)
}

func (value *Journal) truncateTail(offset int64) error {
	if err := value.file.Truncate(offset); err != nil {
		value.recoverySync = true
		return errors.Join(fmt.Errorf("%w: truncate torn tail: %v", ErrJournalIO, err), scheduler.ErrCommitUnknown)
	}
	if _, err := value.file.Seek(0, io.SeekEnd); err != nil {
		value.recoverySync = true
		return errors.Join(fmt.Errorf("%w: seek repaired tail: %v", ErrJournalIO, err), scheduler.ErrCommitUnknown)
	}
	if err := value.file.Sync(); err != nil {
		value.recoverySync = true
		return errors.Join(fmt.Errorf("%w: sync repaired tail: %v", ErrJournalIO, err), scheduler.ErrCommitUnknown)
	}
	value.recoverySync = false
	return nil
}

func (value *Journal) finishReplay(events []scheduler.JobEvent) ([]scheduler.JobEvent, error) {
	if value.recoverySync {
		if err := value.file.Sync(); err != nil {
			value.replayRequired = true
			return nil, errors.Join(
				fmt.Errorf("%w: sync uncertain journal during replay: %v", ErrJournalIO, err),
				scheduler.ErrCommitUnknown,
			)
		}
		value.recoverySync = false
	}
	value.recordCount = len(events)
	value.replayRequired = false
	return events, nil
}

func (value *Journal) rollback(offset int64, cause error) error {
	truncateErr := value.file.Truncate(offset)
	_, seekErr := value.file.Seek(0, io.SeekEnd)
	syncErr := value.file.Sync()
	if truncateErr == nil && seekErr == nil && syncErr == nil {
		return cause
	}
	value.replayRequired = true
	value.recoverySync = true
	return errors.Join(cause,
		scheduler.ErrCommitUnknown,
		wrapJournalError("rollback truncate", truncateErr),
		wrapJournalError("rollback seek", seekErr),
		wrapJournalError("rollback sync", syncErr))
}

func wrapJournalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %v", ErrJournalIO, operation, err)
}

func validJournalLimits(limits journalLimits) bool {
	return limits.maxBytes >= int64(journalHeaderSize)+1 && limits.maxBytes <= MaxJournalBytes &&
		limits.maxRecords > 0 && limits.maxRecords <= MaxJournalRecords
}

func journalByteCountAdmitted(count int64, limits journalLimits) bool {
	return count >= 0 && count <= limits.maxBytes
}

func journalRecordCountAdmitted(count int, limits journalLimits) bool {
	return count >= 0 && count <= limits.maxRecords
}

func writeFull(writer io.Writer, raw []byte) error {
	for len(raw) > 0 {
		count, err := writer.Write(raw)
		if count < 0 || count > len(raw) {
			return errors.New("invalid write count")
		}
		raw = raw[count:]
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduler local: nil context")
	}
	return ctx.Err()
}
