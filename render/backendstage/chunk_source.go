package backendstage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CutlyLab/Cutly/cache/cas"
)

const referenceChunkHeartbeatInterval = cas.MinLeaseTTL / 4

type referenceChunkSource struct {
	service      *Service
	stageContext context.Context
	mu           sync.Mutex
	entries      []*chunkSourceEntry
	next         int
}

type chunkSourceEntry struct {
	descriptor ChunkDescriptor
	reader     *verifiedChunkReader
	opened     bool
}

func newReferenceChunkSource(stageContext context.Context, service *Service, descriptors []ChunkDescriptor) *referenceChunkSource {
	result := &referenceChunkSource{
		service: service, stageContext: stageContext, entries: make([]*chunkSourceEntry, 0, len(descriptors)),
	}
	for _, descriptor := range descriptors {
		entry := &chunkSourceEntry{descriptor: descriptor}
		result.entries = append(result.entries, entry)
	}
	return result
}

func (source *referenceChunkSource) Open(ctx context.Context, descriptor ChunkDescriptor) (io.ReadCloser, error) {
	if source == nil || source.service == nil || source.stageContext == nil || ctx == nil {
		return nil, fmt.Errorf("%w: initialized chunk source and context are required", ErrExecution)
	}
	if err := source.stageContext.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source.mu.Lock()
	if source.next >= len(source.entries) {
		source.mu.Unlock()
		return nil, fmt.Errorf("%w: assembler requested an extra chunk", ErrExecution)
	}
	if source.next > 0 {
		previous := source.entries[source.next-1]
		if previous.reader == nil || !previous.reader.completed() {
			source.mu.Unlock()
			return nil, fmt.Errorf("%w: assembler must completely consume and close each chunk before opening the next", ErrExecution)
		}
	}
	entry := source.entries[source.next]
	if !reflect.DeepEqual(entry.descriptor, descriptor) || entry.opened {
		source.mu.Unlock()
		return nil, fmt.Errorf("%w: assembler requested a changed or out-of-order chunk", ErrExecution)
	}
	source.next++
	entry.opened = true
	source.mu.Unlock()

	openContext, cancelOpen := context.WithCancel(ctx)
	stopStageCancel := context.AfterFunc(source.stageContext, cancelOpen)
	defer func() {
		stopStageCancel()
		cancelOpen()
	}()
	reader, err := safeCASOpen(openContext, source.service.cas, descriptor.CacheKey,
		"backendstage-assemble-"+strings.TrimPrefix(descriptor.CacheKey.Digest, "sha256:")[:24])
	if err != nil {
		return nil, wrapError(ErrExecution, "open assembler chunk", err)
	}
	if reader == nil {
		return nil, fmt.Errorf("%w: CAS returned nil assembler chunk", ErrExecution)
	}
	if reader.Entry.Key != descriptor.CacheKey || reader.Entry.PayloadHash != descriptor.ContentHash ||
		reader.Entry.PayloadSize != descriptor.SizeBytes {
		return nil, errors.Join(fmt.Errorf("%w: assembler chunk identity differs from descriptor", ErrExecution), reader.Close())
	}
	verified := newVerifiedChunkReader(source.stageContext, reader, descriptor.ContentHash, descriptor.SizeBytes)
	source.mu.Lock()
	entry.reader = verified
	source.mu.Unlock()
	return verified, nil
}

// VerifyComplete closes every lease and proves that the assembler opened each
// descriptor exactly once and consumed its complete payload. It is failure-zero
// at the caller: any error invalidates the assembler result.
func (source *referenceChunkSource) VerifyComplete() error {
	if source == nil {
		return fmt.Errorf("%w: nil chunk source", ErrExecution)
	}
	source.mu.Lock()
	entries := append([]*chunkSourceEntry(nil), source.entries...)
	source.mu.Unlock()
	var result error
	for _, entry := range entries {
		source.mu.Lock()
		opened, reader := entry.opened, entry.reader
		source.mu.Unlock()
		if !opened || reader == nil {
			result = errors.Join(result, fmt.Errorf("%w: assembler did not open chunk %q", ErrExecution, entry.descriptor.TaskID))
			continue
		}
		result = errors.Join(result, reader.finish())
	}
	return result
}

type verifiedChunkReader struct {
	mu     sync.Mutex
	reader *cas.Reader
	hash   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	expectedHash  string
	expectedSize  int64
	readBytes     int64
	finishing     bool
	finished      bool
	finishErr     error
	finishDone    chan struct{}
	heartbeatErr  error
	heartbeatStop context.CancelFunc
	heartbeatDone chan struct{}
}

func newVerifiedChunkReader(ctx context.Context, reader *cas.Reader, expectedHash string, expectedSize int64) *verifiedChunkReader {
	heartbeatContext, stop := context.WithCancel(ctx)
	result := &verifiedChunkReader{
		reader: reader, expectedHash: expectedHash, expectedSize: expectedSize,
		hash: sha256.New(), finishDone: make(chan struct{}),
		heartbeatStop: stop, heartbeatDone: make(chan struct{}),
	}
	go result.heartbeat(heartbeatContext, reader)
	return result
}

// heartbeat renews independently of assembler buffer size and read cadence.
// The interval is bounded by the CAS protocol's minimum legal TTL, so a slow
// encoder cannot lose a lease merely because it pauses between reads. The
// first renewal failure is terminal and is surfaced by Read and finish.
func (reader *verifiedChunkReader) heartbeat(ctx context.Context, active *cas.Reader) {
	defer close(reader.heartbeatDone)
	ticker := time.NewTicker(referenceChunkHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := active.Heartbeat(ctx)
			if err == nil {
				continue
			}
			reader.mu.Lock()
			stopping := reader.finishing || reader.finished
			if reader.heartbeatErr == nil && (!stopping || !errors.Is(err, context.Canceled)) {
				reader.heartbeatErr = errors.Join(fmt.Errorf("%w: chunk CAS reader lease heartbeat", ErrExecution), err)
			}
			reader.mu.Unlock()
			return
		}
	}
}

func (reader *verifiedChunkReader) Read(buffer []byte) (int, error) {
	if reader == nil {
		return 0, io.ErrClosedPipe
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.finishing || reader.finished || reader.reader == nil {
		return 0, io.ErrClosedPipe
	}
	if reader.heartbeatErr != nil {
		return 0, reader.heartbeatErr
	}
	remaining := reader.expectedSize - reader.readBytes
	if remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := reader.reader.Read(buffer)
	if count < 0 || int64(count) > remaining {
		return 0, fmt.Errorf("%w: invalid CAS chunk read count", ErrExecution)
	}
	if count > 0 {
		_, _ = reader.hash.Write(buffer[:count])
		reader.readBytes += int64(count)
	}
	return count, err
}

func (reader *verifiedChunkReader) Close() error { return reader.finish() }

func (reader *verifiedChunkReader) completed() bool {
	if reader == nil {
		return false
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.finished && reader.finishErr == nil
}

func (reader *verifiedChunkReader) finish() error {
	if reader == nil {
		return nil
	}
	reader.mu.Lock()
	if reader.finishing || reader.finished {
		done := reader.finishDone
		reader.mu.Unlock()
		<-done
		reader.mu.Lock()
		defer reader.mu.Unlock()
		return reader.finishErr
	}
	reader.finishing = true
	stop, heartbeatDone := reader.heartbeatStop, reader.heartbeatDone
	reader.mu.Unlock()
	if stop != nil {
		stop()
	}
	if heartbeatDone != nil {
		<-heartbeatDone
	}

	reader.mu.Lock()
	var closeErr error
	if reader.reader != nil {
		closeErr = reader.reader.Close()
		reader.reader = nil
	}
	actualHash := "sha256:" + hex.EncodeToString(reader.hash.Sum(nil))
	if reader.readBytes != reader.expectedSize || actualHash != reader.expectedHash {
		reader.finishErr = errors.Join(fmt.Errorf("%w: assembler consumed chunk as %s/%d, want %s/%d",
			ErrExecution, actualHash, reader.readBytes, reader.expectedHash, reader.expectedSize), reader.heartbeatErr, closeErr)
	} else {
		reader.finishErr = errors.Join(reader.heartbeatErr, closeErr)
	}
	reader.finished = true
	reader.finishing = false
	close(reader.finishDone)
	reader.mu.Unlock()
	return reader.finishErr
}

var _ ChunkSource = (*referenceChunkSource)(nil)
var _ io.ReadCloser = (*verifiedChunkReader)(nil)
