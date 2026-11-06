# Signed media runtime bundle v1

`runtimebundle` is the trust boundary between a release-owned FFmpeg/FFprobe
distribution and Cutly's media adapters. It does not download tools, inspect
`PATH`, accept a digest from an Agent, or treat `-version` output as dependency
closure proof.

The release manifest binds one platform/architecture, both executable paths,
and a strictly sorted complete file closure. Each file records exact byte size,
permission bits, SHA-256 content identity, and whether it belongs to the
FFmpeg closure, the FFprobe closure, or both. The payload has a canonical
fingerprint; Ed25519 signs a domain-separated canonical object that also binds
that fingerprint, the key ID, and signature algorithm.

Closure paths use a portable lowercase ASCII alphabet (`a-z`, digits,
`._+-`, and `/`). This removes Unicode-normalization and case-fold aliases
before a manifest reaches case-sensitive or case-insensitive filesystems.

Resolution is fail closed:

1. require one canonical physical absolute path (including exact Windows path
   casing, with no symlink ancestor), reject an unsafe Unix directory mode,
   then open it with `os.OpenRoot`;
2. strictly decode the bounded manifest;
3. check the target and trusted key, then verify fingerprint and signature;
4. reject every symlink, special file, undeclared file/directory, missing entry,
   metadata mismatch, and content-hash mismatch;
5. return executable paths and independent FFmpeg/FFprobe closure fingerprints
   only after the entire closure succeeds.

The directory is walked again after hashing, every hashed file path must still
name the same inode, and the root path/inode is checked again before
publication. Signed Unix file modes are also rejected when a file is
group/world writable or carries setuid/setgid/sticky bits. Windows permission
bits do not attest ACL ownership; the embedding host must enforce a protected
DACL. A caller must nevertheless keep every ancestor
directory host-owned and the verified root on a read-only or code-signing-
protected deployment boundary: portable Go cannot execute directly from a
retained file descriptor on every supported platform, so same-owner rename or
path replacement after `Resolve` remains an embedding-runtime isolation
responsibility until the worker integration consumes retained descriptor/handle
execution.

`SignManifest` exists for offline release tooling. Private keys must not be
compiled into Cutly or shipped beside a bundle. Production keys, pinned bundle
artifacts, download policy, code signing/notarization, and rollout remain
release-owner configuration; this package deliberately embeds none of them.
