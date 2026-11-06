# Local project service

`project/local` is the durable implementation of Cutly's frozen
`videoedit` v1 edit protocol. It is intentionally below the Agent/HTTP layer:
callers can expose the same service through a CLI, daemon, desktop host, or a
test transport without changing transaction semantics.

Project layout:

```text
project/
  project.cutly.json        canonical authoritative Document v1
  .cutly/
    project.lock            OS-released cross-process lock
    initialized             detects a missing state file after initialization
    state.json              bounded plans, ledger and history
    commit.intent.json      present only while recovering a two-file commit
```

The edit loop is `Inspect -> Plan -> Apply`. `Plan` persists the exact candidate
and does not modify the document. `Apply` accepts only the transaction ID and
expected revision, then checks both revision and document hash before publishing
the reviewed candidate. `Revert` applies the stored inverse as a new commit;
history is never rewritten.

Every document/state change uses a same-directory temporary file, file sync,
atomic rename and (on POSIX) parent-directory sync. Document plus state commits
first publish a durable intent. Reopening rolls any admitted old/new combination
forward to the exact next document and state, then removes the intent. An error
after intent publication is `ErrCommitUnknown`; reopening and retrying the same
operation returns an idempotent replay.

Security and scope boundaries:

- the project directory and its parents must be host-owned;
- project, state, intent and lock symlinks are rejected;
- all relative file access is confined with `os.Root`;
- encoded bytes, operations, plans, history and identifiers are bounded before
  persistence;
- Windows uses an exclusive no-share handle and file sync, but Go has no
  portable Windows parent-directory fsync;
- the package does not implement Document v2 typed editing, media resolution,
  rendering, scheduler integration, authentication, multi-tenant policy, or
  network transport.

The last point is deliberate: v2 rendering and the Agent router consume this
service, but are not silently claimed by a v1 persistence layer.
