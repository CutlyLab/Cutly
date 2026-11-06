// Package cutly is the stable, transport-neutral Go entry point for Cutly's
// public Agent protocol. The low-level model and renderer packages remain
// available for embedders; this package owns request/result correlation and
// transports so CLI, daemon and applications share one wire contract.
package cutly

// Version is the source-level SDK release candidate version. Distribution
// builds may add provenance through Go build information without changing the
// Agent protocol version.
const Version = "0.1.0-dev"
