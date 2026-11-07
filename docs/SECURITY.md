# Security boundary

Cutly treats project and media bytes as untrusted. Public decoders are bounded,
strict and duplicate-key safe; render work consumes immutable fingerprints and
controlled locators rather than arbitrary network paths.

The default `cutlyd` listener is loopback-only. Remote exposure requires an
authenticated, encrypted reverse proxy and tenant-specific authorization. The
Agent protocol itself is not an authentication protocol.

`cutlyd` installs local Project/Edit routes only when both `--project-id` and
`--project-dir` are supplied. The ID is an opaque protocol identity; it is not
interpreted as a filesystem path. The configured directory and its parents
must be host-owned. A daemon without this explicit binding advertises only the
capability route.

Local scheduler and cache roots must be host-owned. `os.Root`, regular-file
identity checks and no-replace publication confine normal traversal, but they do
not make a directory safe against another process with authority to replace its
ancestors, create hostile hard links, or alter platform durability semantics.

Report vulnerabilities privately to the project maintainers. Do not attach
customer media, API keys, signed URLs or complete project files to public issue
reports; provide the smallest synthetic reproducer and affected profile/version.
