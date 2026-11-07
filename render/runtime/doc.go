// Package renderruntime connects the durable scheduler to resumable stage-DAG
// execution.
//
// A Dispatcher is a single-owner local coordinator. It atomically claims
// queued jobs, resumes journaled running jobs after restart, executes stages
// through a CheckpointStore and Executor, propagates cancellation, and records
// one terminal scheduler transition. Concrete checkpoint and process-worker
// implementations live in render/checkpoint/local and render/worker.
//
// The v1 boundary deliberately does not implement distributed leases,
// heartbeats or a worker pool shared by multiple coordinator processes. One
// scheduler journal root must have one Dispatcher owner.
package renderruntime
