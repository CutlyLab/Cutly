# Local render worker boundary

`worker.Runner` executes every stage invocation in a new operating-system
process. It never invokes a shell. The host sends one bounded canonical JSON
request on stdin and accepts exactly one bounded canonical JSON result on
stdout. Unknown fields, duplicate fields, noncanonical JSON, correlation
drift, task/cache/dependency drift and trailing output are rejected.

The runner uses an explicit, non-inherited environment and a clean absolute
working directory. It records the executable SHA-256 measured during runner
construction, exit code/signal and bounded UTF-8 stderr. Timeout or caller
cancellation first terminates and then kills the process group on Darwin and
Linux. The group is also checked after every direct-worker exit; surviving
descendants are terminated and make an apparent success retryable. Other
platforms kill the direct process only and report
`descendant_isolation=false`; a Windows Job Object is therefore still a
release requirement for untrusted workers. Platforms without native process
execution (including `js/wasm`) fail closed in `NewRunner`.
Pipe draining is separately bounded so a descendant retaining inherited
stdout or stderr cannot keep `Wait` blocked indefinitely after its parent exits.

This is a crash and cancellation boundary, not a security sandbox:

- the executable path, working directory and their ancestors must remain
  host-owned and immutable for the lifetime of the runner;
- Darwin/Linux process-group cleanup assumes that trusted workers do not call
  `setsid`/`setpgid` to escape their assigned group. Its post-exit group probe
  is a lifecycle backstop for host-owned workers, not a hostile-process
  containment primitive; use a platform sandbox, cgroup or supervisor where
  that distinction matters;
- filesystem, network, credential and syscall restrictions must be supplied
  by the embedding application (for example a container or an OS sandbox);
- the worker result is a claim, not an artifact attestation. The staged DAG
  checkpoint store must reopen the referenced CAS value and verify its complete
  payload hash and size before committing a checkpoint;
- this package starts one process per invocation. A supervised warm process
  pool and platform sandbox profiles are intentionally outside the v1 contract.

Worker binaries should decode with `DecodeRequest`, publish their immutable
stage output to CAS, and encode `Result` with `CanonicalResult`. They must not
write logs or a trailing newline to stdout; diagnostic output belongs on
stderr.
