package videodecode

import (
	"context"
	"fmt"
	"sync"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

// Default decode-session bounds. The byte bound dominates: sixteen cached 1080p RGBA16 frames
// already retain ~265 MB, so hosts with tighter budgets should lower it.
const (
	DefaultMaxCachedFrames = 16
	DefaultMaxCachedBytes  = 256 << 20
)

// SessionOptions bounds one decode session's retained decoded frames.
type SessionOptions struct {
	MaxCachedFrames int
	MaxCachedBytes  int64
}

// Session is a bounded decoded-frame cache in front of one opened Source. A render pass asks
// for the same source frame twice — once for pixels and once for live replay verification —
// and revisits neighboring source frames across output frames; the session answers repeats
// from memory with byte-identical surfaces and receipts instead of re-spawning the decoder.
//
// A Session satisfies the same narrow contract as *Source (Identity/Frame/Close), so it can
// be bound anywhere a decoder source is accepted, including the native video provider.
type Session struct {
	source  *Source
	options SessionOptions

	mu       sync.Mutex
	cache    map[int64]cachedFrame
	order    []int64 // least-recently-used first
	retained int64
	hits     uint64
	misses   uint64
}

type cachedFrame struct {
	surface *cpucompositor.Surface
	receipt FrameReceipt
}

// NewSession wraps one opened source. The options bounds are clamped to at least one frame so
// a session always caches the immediately previous decode.
func NewSession(source *Source, options SessionOptions) (*Session, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: opened source is required", ErrInvalid)
	}
	if options.MaxCachedFrames <= 0 {
		options.MaxCachedFrames = DefaultMaxCachedFrames
	}
	if options.MaxCachedBytes <= 0 {
		options.MaxCachedBytes = DefaultMaxCachedBytes
	}
	return &Session{source: source, options: options, cache: make(map[int64]cachedFrame)}, nil
}

// Identity reports the wrapped source identity.
func (session *Session) Identity() (SourceIdentity, error) {
	if session == nil || session.source == nil {
		return SourceIdentity{}, ErrClosed
	}
	return session.source.Identity()
}

// Frame returns the decoded frame for one exact source time. Repeats and revisits within the
// bounded window are served from the cache with the identical surface and receipt a direct
// decode produced; cache misses decode through the source and admit the result.
func (session *Session) Frame(ctx context.Context, sourceTime videoedit.RationalTime) (*cpucompositor.Surface, FrameReceipt, error) {
	if session == nil || session.source == nil {
		return nil, FrameReceipt{}, ErrClosed
	}
	if ctx == nil {
		return nil, FrameReceipt{}, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	selection, err := selectFrameValidated(session.source.index, sourceTime, session.source.keyframes)
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	key := int64(selection.FrameOrdinal)

	session.mu.Lock()
	if cached, ok := session.cache[key]; ok {
		session.hits++
		session.touchLocked(key)
		session.mu.Unlock()
		return cached.surface, cached.receipt, nil
	}
	session.misses++
	session.mu.Unlock()

	surface, receipt, err := session.source.Frame(ctx, sourceTime)
	if err != nil {
		return nil, FrameReceipt{}, err
	}
	session.admit(key, surface, receipt)
	return surface, receipt, nil
}

// Close releases the wrapped source.
func (session *Session) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.source == nil {
		return nil
	}
	source := session.source
	session.source = nil
	session.cache = nil
	session.order = nil
	return source.Close()
}

// Statistics reports cache hits, misses and currently retained decoded frames.
func (session *Session) Statistics() (hits, misses, retained uint64) {
	if session == nil {
		return 0, 0, 0
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.hits, session.misses, uint64(len(session.cache))
}

func (session *Session) admit(key int64, surface *cpucompositor.Surface, receipt FrameReceipt) {
	frameBytes := int64(uint64(surface.Width()) * uint64(surface.Height()) * cpucompositor.BytesPerPixel)
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.cache == nil {
		return
	}
	if _, exists := session.cache[key]; exists {
		return
	}
	if frameBytes > session.options.MaxCachedBytes {
		// A single frame beyond the byte budget is never retained.
		return
	}
	session.cache[key] = cachedFrame{surface: surface, receipt: receipt}
	session.retained += frameBytes
	session.order = append(session.order, key)
	for (len(session.cache) > session.options.MaxCachedFrames || session.retained > session.options.MaxCachedBytes) && len(session.order) > 0 {
		evict := session.order[0]
		session.order = session.order[1:]
		if cached, ok := session.cache[evict]; ok {
			session.retained -= int64(uint64(cached.surface.Width()) * uint64(cached.surface.Height()) * cpucompositor.BytesPerPixel)
			delete(session.cache, evict)
		}
	}
}

func (session *Session) touchLocked(key int64) {
	for index, candidate := range session.order {
		if candidate == key {
			session.order = append(session.order[:index], session.order[index+1:]...)
			session.order = append(session.order, key)
			return
		}
	}
}
