// Package stagedag executes a validated, immutable render stage graph under
// explicit concurrency and byte budgets. Task cache preimages bind the frozen
// plan and execution manifest, stage dispatch, ordered dependencies, optional
// frame/sample ranges and versioned stage/output profiles. Checkpoints make
// chunk tasks resumable; concrete executors isolate work in child processes and
// publish integrity-checked output bytes to CAS before returning success.
// Run results include per-task cache/execution provenance so consumers can
// distinguish historical checkpoint attempts from retries performed now.
package stagedag
