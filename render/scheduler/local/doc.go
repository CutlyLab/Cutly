// Package schedulerlocal provides the crash-recoverable local event journal
// used by render/scheduler. A journal path is intended to back exactly one
// Coordinator in one owning process; Journal serializes goroutines but
// deliberately does not provide a cross-process lease. It does not execute
// workers or publish artifacts. The parent directory must be host-owned and
// must not be concurrently mutated by an untrusted writer: os.Root confines
// traversal and final symlinks, but a directory owner can still replace names
// or introduce hard links after identity checks. Frame hashes detect accidental
// corruption and torn writes; they are not authentication against tampering.
//
// If an append returns scheduler.ErrCommitUnknown, Replay must complete on the
// same Journal (or Close must successfully sync it) before that handle can be
// treated as recovered. A failed recovery sync never certifies the visible
// prefix.
//
// On Windows, committed file contents are flushed with File.Sync, but Go does
// not expose a portable parent-directory fsync. Creation-entry durability
// therefore has a weaker platform guarantee than the POSIX implementation.
package schedulerlocal
