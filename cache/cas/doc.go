// Package cas implements Cutly's local content-addressed stage cache.
//
// A published entry is a complete non-empty wrapper containing an immutable payload, strict
// manifest, completion sentinel, and lease namespace. The wrapper is built in scratch and moved as
// one directory to a missing canonical path. Scratch directories are never consulted by readers.
// Every read revalidates the sentinel, manifest, payload size, and payload hash. Corrupt entries
// are atomically quarantined and reported as typed recompute misses.
//
// Reader leases live outside the immutable published directory. Their acquire/heartbeat protocol
// coordinates with a generation-bound, durable acquire/commit GC claim so quota collection cannot
// delete an entry with an active lease. Unknown or crash-abandoned owners deliberately fail closed.
package cas
