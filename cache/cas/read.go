package cas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
)

// Reader holds a verified payload descriptor open together with an active lease. Callers running
// longer than the configured TTL must call Heartbeat. Close always releases the lease.
type Reader struct {
	Entry  Entry
	file   File
	lease  *Lease
	mu     sync.Mutex
	closed bool
}

// Open acquires a reader lease before validating the immutable entry. Missing reservations and
// scratch directories are clean misses. A malformed published directory is atomically quarantined
// and returned as ErrMiss + ErrCorrupt + ErrRecomputeRequired.
func (store *Store) Open(ctx context.Context, value cachekey.Key, owner string) (*Reader, error) {
	if store == nil || store.fs == nil || ctx == nil {
		return nil, fmt.Errorf("%w: nil store or context", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shard, entryPath, publishedPath, err := store.keyPaths(value)
	if err != nil {
		return nil, err
	}
	store.namespaceMu.Lock()
	defer store.namespaceMu.Unlock()
	if err := store.validateDirectory(shard); err != nil {
		if isNotExist(err) {
			return nil, &LookupError{Key: value.Digest}
		}
		return nil, fmt.Errorf("%w: invalid key shard: %w", ErrCorrupt, err)
	}
	if err := store.validateDirectory(entryPath); err != nil {
		if isNotExist(err) {
			return nil, &LookupError{Key: value.Digest}
		}
		quarantine, quarantineErr := store.quarantineLocked(entryPath, value.Digest)
		return nil, &LookupError{
			Key: value.Digest, QuarantinePath: quarantine, Corrupt: true,
			Cause: errors.Join(err, quarantineErr),
		}
	}
	if _, err := store.fs.Lstat(publishedPath); err != nil {
		if isNotExist(err) {
			return nil, &LookupError{Key: value.Digest}
		}
		return nil, &LookupError{Key: value.Digest, Cause: err}
	}
	lease, err := store.acquireLeaseLocked(ctx, value, entryPath, owner)
	if err != nil {
		if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrMiss) {
			return nil, &LookupError{Key: value.Digest, Cause: err}
		}
		if errors.Is(err, ErrCorrupt) {
			quarantine, quarantineErr := store.quarantineLocked(entryPath, value.Digest)
			return nil, &LookupError{Key: value.Digest, QuarantinePath: quarantine, Corrupt: true, Cause: errors.Join(err, quarantineErr)}
		}
		return nil, err
	}
	payload, manifest, manifestRaw, sentinelRaw, verifyErr := store.verifyDirectory(ctx, value, publishedPath)
	if verifyErr != nil {
		releaseErr := lease.releaseLocked()
		lease.closed = true
		if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) {
			return nil, errors.Join(verifyErr, releaseErr)
		}
		quarantine, quarantineErr := store.quarantineLocked(entryPath, value.Digest)
		return nil, &LookupError{
			Key: value.Digest, QuarantinePath: quarantine, Corrupt: true,
			Cause: errors.Join(verifyErr, releaseErr, quarantineErr),
		}
	}
	return &Reader{
		Entry: publicEntry(manifest, len(manifestRaw), len(sentinelRaw)),
		file:  payload, lease: lease,
	}, nil
}

func (reader *Reader) Read(content []byte) (int, error) {
	if reader == nil || reader.file == nil {
		return 0, fsClosedError()
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, fsClosedError()
	}
	return reader.file.Read(content)
}

func (reader *Reader) Seek(offset int64, whence int) (int64, error) {
	if reader == nil || reader.file == nil {
		return 0, fsClosedError()
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, fsClosedError()
	}
	return reader.file.Seek(offset, whence)
}

func (reader *Reader) Heartbeat(ctx context.Context) error {
	if reader == nil || reader.lease == nil {
		return ErrLeaseLost
	}
	return reader.lease.Heartbeat(ctx)
}

func (reader *Reader) LeaseInfo() LeaseInfo {
	if reader == nil || reader.lease == nil {
		return LeaseInfo{}
	}
	return reader.lease.Info()
}

func (reader *Reader) Close() error {
	if reader == nil {
		return nil
	}
	reader.mu.Lock()
	if reader.closed {
		reader.mu.Unlock()
		return nil
	}
	reader.closed = true
	file := reader.file
	reader.file = nil
	reader.mu.Unlock()
	var fileErr error
	if file != nil {
		fileErr = file.Close()
	}
	var leaseErr error
	if reader.lease != nil {
		leaseErr = reader.lease.Close()
	}
	return errors.Join(fileErr, leaseErr)
}

func fsClosedError() error { return errors.New("cache: reader is closed") }

var _ io.ReadSeekCloser = (*Reader)(nil)
