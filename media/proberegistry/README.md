# Durable probe registry

`proberegistry.Store` is the durable trust hand-off between fresh, host-owned
media probing and a resumable render worker. The embedding host authorizes the
input descriptor, verifies the immutable resource bytes, produces a valid
`probe.ProbeManifest`, and calls `Put`. A restarted FFmpeg backend can then use
the same store as its `ProbeResolver` without trusting a job-supplied path or
re-running a probe after the source has gone offline.

The registry is deliberately narrow:

- identity is the canonical SHA-256 fingerprint returned by
  `probe.Fingerprint`;
- entries are immutable, canonical, strictly decoded, semantically validated,
  stream-bounded, and independently owned on every return;
- publication uses a synced scratch inode and an atomic no-replace hard link;
  the open source descriptor and published descriptor must identify the same
  inode before success;
- per-shard file locks plus in-process gates serialize cooperating writers;
- roots and namespaces are host-owned, clean absolute directories; record and
  lock opens reject symlinks (and Windows reparse points);
- entry, scratch, quarantine, canonical-value, stream, and byte counts are
  explicit bounds; cancellation is checked before and throughout waits, scans,
  and reads;
- a handle whose configured record-byte limit is smaller than an existing
  valid entry returns `ErrLimit` without quarantining or mutating that entry;
- a corrupt or path-colliding entry fails the current request and is moved to a
  bounded quarantine when possible. It is never returned as a probe result.

The registry does not authorize resource locators, invoke FFprobe, prove that a
probe binary is trustworthy, or sign evidence. Those are responsibilities of
the host preflight resolver. Use a separate registry root for every trust or
tenant boundary.

On POSIX, files and publication directories are synced. Go exposes no portable
Windows directory `fsync`; Windows still flushes the manifest file and uses
atomic hard-link publication, but power-loss durability of directory metadata
is weaker. An `ErrDurabilityUncertain` result can leave a valid visible entry;
retrying the identical `Put` revalidates and syncs it idempotently.

AIX, Solaris, and platforms outside the implemented lock profiles fail closed
with `ErrUnsupported` during `New`.
