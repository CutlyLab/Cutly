# Security policy

## Supported versions

Security fixes are provided for the latest tagged release candidate and the
latest stable release, once stable releases begin.

## Reporting a vulnerability

Please use the private
[GitHub security-advisory form](https://github.com/CutlyLab/Cutly/security/advisories/new).
Do not include exploit details, private media, filesystem paths, API keys, or
runtime identity material in a public issue.

Include the affected Cutly version or commit, operating system, FFmpeg and
FFprobe build identities, the smallest safe reproduction, and whether the
issue crosses the project/input/output root boundary. We will acknowledge a
complete report within five business days and coordinate disclosure after a
fix or mitigation is available.

## Runtime trust boundary

Cutly treats project files, media, fonts, Agent requests, manifests, and
renderer outputs as untrusted. Production hosts must pin and audit FFmpeg and
FFprobe themselves, provide the required build/runtime fingerprints, isolate
input, state, and output roots, and keep the Agent endpoint local or behind
their own authenticated transport.
