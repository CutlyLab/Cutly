package chunkcachev2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"

	"github.com/CutlyLab/Cutly/cache/cas"
	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

type Options struct {
	// MaxPayloadBytes must be explicitly admitted by the host. No duration-based buffering occurs.
	MaxPayloadBytes int64
	// MaxEntries bounds directory enumeration and crash leftovers, default 4096.
	MaxEntries int
}

type Store struct {
	root        string
	rootInfo    os.FileInfo
	storageInfo map[string]os.FileInfo
	artifacts   *cas.Store
	maxPayload  int64
	maxEntries  int
	// fault is test-only, never caller or wire configured.
	fault func(string) error
}

// New requires an existing private host-owned root. CAS configuration, source streams,
// executable authorization and heartbeat scheduling are host policy, never wire paths.
// Only Darwin and Linux currently support this anchored index implementation.
func New(root string, artifacts *cas.Store, options Options) (out *Store, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrInvalid
		}
		if err != nil {
			out = nil
		}
	}()
	if artifacts == nil || options.MaxPayloadBytes <= 0 {
		return nil, ErrInvalid
	}
	if options.MaxEntries == 0 {
		options.MaxEntries = 4096
	}
	if options.MaxEntries < 1 || options.MaxEntries > 65536 {
		return nil, ErrLimit
	}
	s := &Store{root: root, artifacts: artifacts, maxPayload: options.MaxPayloadBytes, maxEntries: options.MaxEntries}
	err = s.initialize()
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) admit(ctx context.Context, v Identity) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if s == nil || s.artifacts == nil {
		return ErrInvalid
	}
	if err := validateIdentity(v); err != nil {
		return err
	}
	if v.Range.Count*v.UnitBytes > s.maxPayload {
		return ErrLimit
	}
	return nil
}
func (s *Store) inject(point string) error {
	if s.fault != nil {
		return s.fault(point)
	}
	return nil
}

// Publish verifies a host-admitted producer receipt before touching CAS or the index.
// CAS publication precedes index commit: a crash can leave an unindexed GC-eligible
// object, never an index pointing at an unverified partial payload. Identical retries
// converge even when producer invocation/attempt differ; the original receipt is retained.
// A failure returns no output; a post-commit failure reports ErrDurabilityUncertain.
func (s *Store) Publish(ctx context.Context, v Identity, r Receipt, payload io.Reader) (err error) {
	defer recoverError(&err)
	if err = s.admit(ctx, v); err != nil {
		return err
	}
	value, raw, err := encodeRecord(v, r)
	if err != nil {
		return err
	}
	if payload == nil {
		return ErrInvalid
	}
	if err = s.withSession(ctx, func(x *session) error { return x.admitPublication(value.Key) }); err != nil {
		return err
	}
	if _, err = s.artifacts.Publish(ctx, cas.PublishRequest{Key: value.Key, Payload: guardedReader{payload}, ExpectedHash: r.ContentHash, ExpectedSize: r.SizeBytes}); err != nil {
		return err
	}
	return s.withSession(ctx, func(x *session) (err error) {
		// Acquire after waiting for the index lock, so lock contention cannot consume
		// the lease TTL. GC between CAS publish and this open is an ordinary retry;
		// no index is committed without a pinned verified object.
		reader, err := s.artifacts.Open(ctx, value.Key, "chunkcachev2-publish")
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				err = x.check()
			}
			x.setHeartbeat(nil)
			err = errors.Join(err, reader.Close())
		}()
		x.setHeartbeat(func() error { return reader.Heartbeat(ctx) })
		if reader.Entry.PayloadHash != r.ContentHash || reader.Entry.PayloadSize != r.SizeBytes {
			return ErrCollision
		}
		if err = reader.Heartbeat(ctx); err != nil {
			return err
		}
		old, readErr := x.read(value.Key)
		if readErr == nil {
			previous, e := decodeRecord(old)
			if e == nil && previous.Key == value.Key && previous.Receipt.Identity == v {
				if previous.Receipt.ContentHash != r.ContentHash || previous.Receipt.SizeBytes != r.SizeBytes {
					return ErrCollision
				}
				return x.resync(value.Key)
			}
			if e = x.quarantine(value.Key); e != nil {
				return e
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			if errors.Is(readErr, ErrCorrupt) || errors.Is(readErr, ErrLimit) {
				if e := x.quarantine(value.Key); e != nil {
					return e
				}
			} else {
				return readErr
			}
		}
		return x.publish(ctx, value.Key, raw)
	})
}

// Lookup returns only a revalidated content stream and its original producer receipt.
// No current-job lifecycle information is accepted or returned. Missing/stale/corrupt
// entries are recomputation candidates; corruption includes ErrMiss and ErrCorrupt.
func (s *Store) Lookup(ctx context.Context, v Identity) (out *Handle, err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
		if err != nil && out != nil {
			_ = out.Close()
			out = nil
		}
	}()
	if err = s.admit(ctx, v); err != nil {
		return nil, err
	}
	k, err := Key(v)
	if err != nil {
		return nil, err
	}
	var receipt Receipt
	err = s.withSession(ctx, func(x *session) error {
		raw, e := x.read(k)
		if errors.Is(e, os.ErrNotExist) {
			return ErrMiss
		}
		if e != nil {
			if errors.Is(e, ErrCorrupt) || errors.Is(e, ErrLimit) {
				return errors.Join(ErrMiss, ErrCorrupt, e, x.quarantine(k))
			}
			return e
		}
		record, e := decodeRecord(raw)
		if e != nil || record.Key != k || record.Receipt.Identity != v {
			return errors.Join(ErrMiss, ErrCorrupt, e, x.quarantine(k))
		}
		receipt = record.Receipt
		return nil
	})
	if err != nil {
		return nil, err
	}
	reader, err := s.artifacts.Open(ctx, k, "chunkcachev2-reader")
	if err != nil {
		if errors.Is(err, cas.ErrMiss) {
			if errors.Is(err, cas.ErrCorrupt) {
				q := s.withSession(ctx, func(x *session) error {
					// Recheck the index generation under lock before quarantining; never
					// remove a concurrently repaired producer receipt.
					raw, e := x.read(k)
					if errors.Is(e, os.ErrNotExist) {
						return nil
					}
					if e != nil {
						return e
					}
					current, e := decodeRecord(raw)
					if e != nil {
						return x.quarantine(k)
					}
					if current.Receipt == receipt {
						return x.quarantine(k)
					}
					return nil
				})
				return nil, errors.Join(ErrMiss, ErrCorrupt, err, q)
			}
			return nil, errors.Join(ErrMiss, err)
		}
		return nil, err
	}
	// Install cleanup ownership immediately, before any callback/context can panic.
	out = &Handle{reader: reader, receipt: receipt, key: k, ctx: ctx, hasher: sha256.New()}
	if reader.Entry.PayloadHash != receipt.ContentHash || reader.Entry.PayloadSize != receipt.SizeBytes {
		e := s.withSession(ctx, func(x *session) error { return x.quarantine(k) })
		return out, errors.Join(ErrMiss, ErrCorrupt, e)
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Handle owns a real CAS reader lease. Call Heartbeat before its host-configured TTL
// expires and always Close, including on cancellation. Close rejects partial consumption;
// it does not drain potentially large payloads. Closing never deletes data or another lease.
// Methods are serialized so cancellation, Read and Close can safely race.
type Handle struct {
	mu       sync.Mutex
	reader   *cas.Reader
	receipt  Receipt
	key      cachekey.Key
	ctx      context.Context
	hasher   hash.Hash
	count    int64
	readErr  error
	closed   bool
	closeErr error
}

func (h *Handle) Receipt() Receipt {
	if h == nil {
		return Receipt{}
	}
	return h.receipt
}
func (h *Handle) Key() cachekey.Key {
	if h == nil {
		return cachekey.Key{}
	}
	return h.key
}
func (h *Handle) Read(p []byte) (n int, err error) {
	if h == nil {
		return 0, os.ErrClosed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	defer func() {
		if recover() != nil {
			n = 0
			err = ErrInvalid
			h.readErr = err
		}
	}()
	if h.closed {
		return 0, os.ErrClosed
	}
	if h.reader == nil || h.ctx == nil || h.hasher == nil {
		return 0, ErrInvalid
	}
	if e := h.ctx.Err(); e != nil {
		h.readErr = e
		return 0, e
	}
	n, err = h.reader.Read(p)
	if n > 0 {
		_, _ = h.hasher.Write(p[:n])
		h.count += int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		h.readErr = err
	}
	return n, err
}
func (h *Handle) Heartbeat(ctx context.Context) (err error) {
	defer recoverError(&err)
	if err = checkContext(ctx); err != nil {
		return err
	}
	if h == nil {
		return os.ErrClosed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return os.ErrClosed
	}
	return h.reader.Heartbeat(ctx)
}
func (h *Handle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return h.closeErr
	}
	h.closed = true
	if h.reader == nil || h.hasher == nil {
		h.closeErr = ErrInvalid
		return h.closeErr
	}
	verify := h.readErr
	if h.count != h.receipt.SizeBytes || "sha256:"+hex.EncodeToString(h.hasher.Sum(nil)) != h.receipt.ContentHash {
		verify = errors.Join(verify, fmt.Errorf("%w: incomplete or changed stream", ErrCorrupt))
	}
	h.closeErr = errors.Join(verify, h.reader.Close())
	return h.closeErr
}

// Recover source panics before unwinding into CAS: CAS can close/sync its private
// payload descriptor on the ordinary error path rather than leaking it on panic.
type guardedReader struct{ source io.Reader }

func (r guardedReader) Read(p []byte) (n int, err error) {
	defer func() {
		if recover() != nil {
			n = 0
			err = ErrInvalid
		}
	}()
	return r.source.Read(p)
}

func nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func entryName(k cachekey.Key) string {
	raw, _ := canonicaljson.Marshal(k)
	return digest(raw)[7:] + ".json"
}
