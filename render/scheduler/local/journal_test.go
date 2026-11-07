package schedulerlocal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

func TestJournalAppendReplayAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	events := journalTestEvents()
	for _, event := range events {
		if err := journal.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := journal.Replay(context.Background())
	if err != nil || !reflect.DeepEqual(replayed, events) {
		t.Fatalf("replay = %+v err=%v", replayed, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Append(context.Background(), events[1]); !errors.Is(err, ErrJournalIO) {
		t.Fatalf("append before mandatory replay = %v", err)
	}
	replayed, err = reopened.Replay(context.Background())
	if err != nil || !reflect.DeepEqual(replayed, events) {
		t.Fatalf("restart replay = %+v err=%v", replayed, err)
	}
}

func TestJournalCoordinatorRestartIntegration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal := mustOpenJournal(t, path)
	coordinator, err := scheduler.Open(context.Background(), journal)
	if err != nil {
		t.Fatal(err)
	}
	job := journalTestJob()
	if _, created, err := coordinator.Submit(context.Background(), job); err != nil || !created {
		t.Fatalf("submit created=%v err=%v", created, err)
	}
	if _, err := coordinator.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := mustOpenJournal(t, path)
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := scheduler.Open(context.Background(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.Snapshot(job.ID)
	if err != nil || snapshot.State != renderapi.JobRunning || snapshot.LastSequence != 2 {
		t.Fatalf("restart snapshot = %+v err=%v", snapshot, err)
	}
	if duplicate, created, err := restarted.Submit(context.Background(), job); err != nil || created || duplicate.LastSequence != 2 {
		t.Fatalf("restart duplicate = %+v created=%v err=%v", duplicate, created, err)
	}
}

func TestJournalRepairsTornHeaderAndPayloadTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal := mustOpenJournal(t, path)
	events := journalTestEvents()
	if err := journal.Append(context.Background(), events[0]); err != nil {
		t.Fatal(err)
	}
	firstInfo, _ := os.Stat(path)
	firstSize := firstInfo.Size()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, path, []byte(journalMagic[:5]))

	journal = mustOpenJournal(t, path)
	replayed, err := journal.Replay(context.Background())
	if err != nil || len(replayed) != 1 {
		t.Fatalf("torn header replay = %+v err=%v", replayed, err)
	}
	info, _ := os.Stat(path)
	if info.Size() != firstSize {
		t.Fatalf("repaired header size = %d want %d", info.Size(), firstSize)
	}
	if err := journal.Append(context.Background(), events[1]); err != nil {
		t.Fatal(err)
	}
	secondInfo, _ := os.Stat(path)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, secondInfo.Size()-7); err != nil {
		t.Fatal(err)
	}

	journal = mustOpenJournal(t, path)
	t.Cleanup(func() { _ = journal.Close() })
	replayed, err = journal.Replay(context.Background())
	if err != nil || len(replayed) != 1 || replayed[0].Kind != scheduler.EventSubmitted {
		t.Fatalf("torn payload replay = %+v err=%v", replayed, err)
	}
	info, _ = os.Stat(path)
	if info.Size() != firstSize {
		t.Fatalf("repaired payload size = %d want %d", info.Size(), firstSize)
	}
}

func TestJournalRejectsUpwardLengthFlipInCompleteTailHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal := mustOpenJournal(t, path)
	if err := journal.Append(context.Background(), journalTestEvents()[0]); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	lengthByte := []byte{0}
	if _, err := file.ReadAt(lengthByte, int64(journalLengthOffset+1)); err != nil {
		t.Fatal(err)
	}
	if lengthByte[0] != 0 {
		t.Fatalf("test event length high byte = %d, want zero", lengthByte[0])
	}
	lengthByte[0] = 1
	if _, err := file.WriteAt(lengthByte, int64(journalLengthOffset+1)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	journal = mustOpenJournal(t, path)
	t.Cleanup(func() { _ = journal.Close() })
	if _, err := journal.Replay(context.Background()); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("length flip replay = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("corrupt journal was truncated: before=%d after=%d", before.Size(), after.Size())
	}
}

func TestJournalRejectsMiddleCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal := mustOpenJournal(t, path)
	for _, event := range journalTestEvents() {
		if err := journal.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("["), int64(journalHeaderSize)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	journal = mustOpenJournal(t, path)
	t.Cleanup(func() { _ = journal.Close() })
	if _, err := journal.Replay(context.Background()); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("corruption error = %v", err)
	}
}

func TestJournalWriteAndSyncFailuresRollback(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*faultJournalFile)
	}{
		{name: "write", configure: func(value *faultJournalFile) { value.writeFailureAfter = 17 }},
		{name: "sync", configure: func(value *faultJournalFile) { value.syncFailures = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scheduler.journal")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fault := &faultJournalFile{File: file, writeFailureAfter: -1}
			test.configure(fault)
			journal := newJournal(fault)
			if err := journal.Append(context.Background(), journalTestEvents()[0]); !errors.Is(err, ErrJournalIO) {
				t.Fatalf("injected error = %v", err)
			}
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != 0 {
				t.Fatalf("rollback size = %d", info.Size())
			}
			fault.writeFailureAfter = -1
			fault.syncFailures = 0
			if err := journal.Append(context.Background(), journalTestEvents()[0]); err != nil {
				t.Fatalf("retry append = %v", err)
			}
			replayed, err := journal.Replay(context.Background())
			if err != nil || len(replayed) != 1 {
				t.Fatalf("retry replay = %+v err=%v", replayed, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJournalCommitUnknownRequiresRecoverySyncBeforeReplay(t *testing.T) {
	for _, test := range []struct {
		name             string
		truncateFailures int
		wantEvents       int
	}{
		{name: "durably remove uncertain frame", wantEvents: 0},
		{name: "durably accept complete uncertain frame", truncateFailures: 1, wantEvents: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scheduler.journal")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fault := &faultJournalFile{
				File: file, writeFailureAfter: -1, syncFailures: 3,
				truncateFailures: test.truncateFailures,
			}
			journal := newJournal(fault)
			if err := journal.Append(context.Background(), journalTestEvents()[0]); !errors.Is(err, ErrJournalIO) || !errors.Is(err, scheduler.ErrCommitUnknown) {
				t.Fatalf("unknown append error = %v", err)
			}
			if !journal.replayRequired || !journal.recoverySync {
				t.Fatalf("unknown append flags replay=%v sync=%v", journal.replayRequired, journal.recoverySync)
			}

			// The third injected Sync failure occurs during Replay. Replay must not
			// certify either the truncated view or a complete but unflushed frame.
			if _, err := journal.Replay(context.Background()); !errors.Is(err, ErrJournalIO) || !errors.Is(err, scheduler.ErrCommitUnknown) {
				t.Fatalf("uncertified replay error = %v", err)
			}
			if !journal.replayRequired || !journal.recoverySync {
				t.Fatalf("failed recovery flags replay=%v sync=%v", journal.replayRequired, journal.recoverySync)
			}

			replayed, err := journal.Replay(context.Background())
			if err != nil || len(replayed) != test.wantEvents {
				t.Fatalf("certified replay events=%d err=%v", len(replayed), err)
			}
			if fault.syncCalls != 4 || journal.replayRequired || journal.recoverySync {
				t.Fatalf("certified recovery syncs=%d replay=%v sync=%v", fault.syncCalls, journal.replayRequired, journal.recoverySync)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}

			// A restart must observe exactly the state that Replay certified.
			reopened := mustOpenJournal(t, path)
			t.Cleanup(func() { _ = reopened.Close() })
			restartedEvents, err := reopened.Replay(context.Background())
			if err != nil || len(restartedEvents) != test.wantEvents {
				t.Fatalf("restart replay events=%d err=%v", len(restartedEvents), err)
			}
		})
	}
}

func TestJournalClosePreservesUncertainRecoveryUntilSyncSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fault := &faultJournalFile{File: file, writeFailureAfter: -1, syncFailures: 3}
	journal := newJournal(fault)
	if err := journal.Append(context.Background(), journalTestEvents()[0]); !errors.Is(err, scheduler.ErrCommitUnknown) {
		t.Fatalf("unknown append = %v", err)
	}
	if err := journal.Close(); !errors.Is(err, scheduler.ErrCommitUnknown) {
		t.Fatalf("uncertified close = %v", err)
	}
	if journal.file == nil || !journal.recoverySync {
		t.Fatalf("failed close discarded recovery state: file=%v sync=%v", journal.file, journal.recoverySync)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("certified close = %v", err)
	}

	reopened := mustOpenJournal(t, path)
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.Replay(context.Background())
	if err != nil || len(events) != 0 {
		t.Fatalf("reopened after certified close events=%d err=%v", len(events), err)
	}
}

func TestOpenJournalSyncsFileAndParentDirectoryAndPropagatesFailure(t *testing.T) {
	t.Run("new and existing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "scheduler.journal")
		calls := 0
		journal, err := openJournal(path, func(root *os.Root) error {
			calls++
			return syncJournalDirectory(root)
		})
		if err != nil || calls != 1 {
			t.Fatalf("new journal calls=%d err=%v", calls, err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
		journal, err = openJournal(path, func(root *os.Root) error {
			calls++
			return syncJournalDirectory(root)
		})
		if err != nil || calls != 2 {
			t.Fatalf("existing journal calls=%d err=%v", calls, err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("existing failure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "scheduler.journal")
		journal := mustOpenJournal(t, path)
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected existing directory sync failure")
		journal, err := openJournal(path, func(*os.Root) error { return injected })
		if journal != nil || !errors.Is(err, ErrJournalIO) || !errors.Is(err, injected) {
			t.Fatalf("existing directory sync failure journal=%v err=%v", journal, err)
		}
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("existing journal changed after sync failure: info=%v err=%v", info, statErr)
		}
	})

	t.Run("failure cleanup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "scheduler.journal")
		injected := errors.New("injected directory sync failure")
		calls := 0
		journal, err := openJournal(path, func(root *os.Root) error {
			calls++
			if calls == 1 {
				return injected
			}
			return syncJournalDirectory(root)
		})
		if journal != nil || !errors.Is(err, ErrJournalIO) || !errors.Is(err, injected) || calls != 2 {
			t.Fatalf("directory sync failure journal=%v calls=%d err=%v", journal, calls, err)
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed creation remains: %v", statErr)
		}
	})
}

func TestJournalAdmissionLimitsAtNAndNPlusOne(t *testing.T) {
	defaults := defaultJournalLimits()
	if !journalByteCountAdmitted(MaxJournalBytes, defaults) || journalByteCountAdmitted(MaxJournalBytes+1, defaults) {
		t.Fatal("hard journal byte N/N+1 admission drift")
	}
	if !journalRecordCountAdmitted(MaxJournalRecords, defaults) || journalRecordCountAdmitted(MaxJournalRecords+1, defaults) {
		t.Fatal("hard journal record N/N+1 admission drift")
	}
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	events := journalTestEvents()
	journal := mustOpenJournal(t, path)
	if err := journal.Append(context.Background(), events[0]); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(context.Background(), events[1]); err != nil {
		t.Fatal(err)
	}
	totalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	openLimited := func(t *testing.T, limits journalLimits) *Journal {
		t.Helper()
		file, openErr := os.OpenFile(path, os.O_RDWR, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		return newJournalWithLimits(file, limits)
	}

	journal = openLimited(t, journalLimits{maxBytes: totalInfo.Size(), maxRecords: len(events)})
	if replayed, err := journal.Replay(context.Background()); err != nil || len(replayed) != len(events) {
		t.Fatalf("byte/record N replay=%d err=%v", len(replayed), err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal = openLimited(t, journalLimits{maxBytes: totalInfo.Size() - 1, maxRecords: len(events)})
	if _, err := journal.Replay(context.Background()); !errors.Is(err, ErrJournalLimit) {
		t.Fatalf("byte N+1 replay = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal = openLimited(t, journalLimits{maxBytes: totalInfo.Size(), maxRecords: len(events) - 1})
	if _, err := journal.Replay(context.Background()); !errors.Is(err, ErrJournalLimit) {
		t.Fatalf("record N+1 replay = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal = openLimited(t, journalLimits{maxBytes: firstInfo.Size(), maxRecords: len(events)})
	if err := journal.file.Truncate(firstInfo.Size()); err != nil {
		t.Fatal(err)
	}
	if replayed, err := journal.Replay(context.Background()); err != nil || len(replayed) != 1 {
		t.Fatalf("append byte N replay=%d err=%v", len(replayed), err)
	}
	if err := journal.Append(context.Background(), events[1]); !errors.Is(err, ErrJournalLimit) {
		t.Fatalf("append byte N+1 = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != firstInfo.Size() {
		t.Fatalf("limited append size=%d want=%d", info.Size(), firstInfo.Size())
	}

	journal = openLimited(t, journalLimits{maxBytes: totalInfo.Size(), maxRecords: 1})
	if replayed, err := journal.Replay(context.Background()); err != nil || len(replayed) != 1 {
		t.Fatalf("append record N replay=%d err=%v", len(replayed), err)
	}
	if err := journal.Append(context.Background(), events[1]); !errors.Is(err, ErrJournalLimit) {
		t.Fatalf("append record N+1 = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalCancellationAndPathBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.journal")
	journal := mustOpenJournal(t, path)
	t.Cleanup(func() { _ = journal.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := journal.Append(ctx, journalTestEvents()[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("append cancellation = %v", err)
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Fatalf("canceled append size = %d", info.Size())
	}
	if _, err := OpenJournal("relative.journal"); !errors.Is(err, ErrJournalPath) {
		t.Fatalf("relative path = %v", err)
	}
	if _, err := OpenJournal(t.TempDir()); !errors.Is(err, ErrJournalPath) {
		t.Fatalf("directory path = %v", err)
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(filepath.Dir(target), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenJournal(link); !errors.Is(err, ErrJournalPath) {
			t.Fatalf("symlink path = %v", err)
		}
	}
}

type faultJournalFile struct {
	*os.File
	writeFailureAfter int
	syncFailures      int
	truncateFailures  int
	syncCalls         int
}

func (value *faultJournalFile) Write(raw []byte) (int, error) {
	if value.writeFailureAfter >= 0 {
		count := value.writeFailureAfter
		if count > len(raw) {
			count = len(raw)
		}
		value.writeFailureAfter = -1
		written, writeErr := value.File.Write(raw[:count])
		if writeErr != nil {
			return written, writeErr
		}
		return written, errors.New("injected write failure")
	}
	return value.File.Write(raw)
}

func (value *faultJournalFile) Sync() error {
	value.syncCalls++
	if value.syncFailures > 0 {
		value.syncFailures--
		return errors.New("injected sync failure")
	}
	return value.File.Sync()
}

func (value *faultJournalFile) Truncate(size int64) error {
	if value.truncateFailures > 0 {
		value.truncateFailures--
		return errors.New("injected truncate failure")
	}
	return value.File.Truncate(size)
}

func journalTestEvents() []scheduler.JobEvent {
	job := journalTestJob()
	return []scheduler.JobEvent{
		{SchemaVersion: 1, JobID: job.ID, Sequence: 1, Kind: scheduler.EventSubmitted, State: renderapi.JobQueued, Job: &job},
		{SchemaVersion: 1, JobID: job.ID, Sequence: 2, Kind: scheduler.EventStarted, State: renderapi.JobRunning},
	}
}

func journalTestJob() renderapi.RenderJob {
	return renderapi.RenderJob{
		SchemaVersion: 1, ID: "job-journal", IdempotencyKey: "request-journal",
		PlanFingerprint: journalDigest('a'), ExecutionManifestFingerprint: journalDigest('b'),
		TimelineRange: videoedit.TimeRange{
			Start:    videoedit.RationalTime{Value: 0, Rate: 1},
			Duration: videoedit.RationalTime{Value: 1, Rate: 1},
		},
		FrameRange:  videoedit.TickRange{First: 0, Count: 30},
		SampleRange: videoedit.TickRange{First: 0, Count: 48_000},
		ArtifactURI: "file:///controlled/journal.mp4",
	}
}

func journalDigest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }

func mustOpenJournal(t *testing.T, path string) *Journal {
	t.Helper()
	result, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func appendRaw(t *testing.T, path string, raw []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

var _ io.Writer = (*faultJournalFile)(nil)
