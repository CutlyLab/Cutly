// Package prepared stores immutable preflight environments for later render
// execution. A record contains one strictly decoded semantic RenderPlan and
// its exact frozen ExecutionManifest, addressed by both canonical
// fingerprints.
//
// The local store assumes its root and ancestors are host-owned. It rejects
// symlink namespaces, entries and record files, but it is not a defense
// against a privileged process replacing a trusted ancestor or hard link.
package prepared
