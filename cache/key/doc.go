// Package key defines the versioned, canonical identity of one reusable render stage output.
//
// A stage key deliberately contains semantic and execution fingerprints, normalized input
// content hashes, composition ranges, and output profiles. Job IDs, idempotency keys, worker IDs,
// local paths, and artifact URIs have no field in this protocol and therefore cannot perturb
// reuse of identical work.
package key
