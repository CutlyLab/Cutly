// Package scheduler defines the durable local scheduling state machine layered
// on top of render/api's frozen RenderJob contract.
//
// This package does not execute render work. It owns submission identity,
// legal state transitions, monotonic per-job event sequences, cancellation and
// replay. Worker tasks, checkpoints, CAS and publication are separate M3
// layers.
//
// Store append failures have an explicit outcome contract. Failure-atomic
// errors may be retried; ErrCommitUnknown makes the Coordinator fail closed so
// callers must recover the Store and create a new Coordinator with Open.
package scheduler
