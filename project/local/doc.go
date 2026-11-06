// Package local implements Cutly's bounded, crash-recoverable local project
// service for the frozen videoedit v1 Document/Transaction contract.
//
// project.cutly.json remains the authoritative editing document. Bookkeeping
// under .cutly/state.json contains reviewed plans, the idempotency ledger and
// bounded history, but never changes render semantics. A durable intent makes
// document+state commits recover by rolling forward after process or power
// loss; no caller is asked to infer a half-commit from two unrelated files.
//
// The project directory and its parents are host-owned. The implementation
// rejects symlink project/state files and confines relative I/O with os.Root,
// but does not claim to defend against a privileged attacker replacing a
// host-owned parent directory or creating hostile hard links concurrently.
// POSIX publishes file and parent-directory fsync. Go exposes no portable
// Windows directory-fsync primitive, so Windows uses file sync plus atomic
// rename and documents that weaker power-loss boundary.
package local
