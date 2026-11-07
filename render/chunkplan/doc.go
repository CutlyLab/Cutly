// Package chunkplan defines Cutly's first pre-codec chunk planning and resume
// protocol.
//
// It deterministically partitions one already-validated RenderJob into
// independent composition-surface frame groups and PCM sample blocks. The
// returned Plan keeps that frozen job beside stagedag and cache/key tasks, so
// recovery can re-prove complete fixed-range coverage. Checkpoints remain
// job-bound while equivalent jobs share the same CAS keys. Receipt validation,
// linear missing-work selection and decoded-domain aggregation are fail closed.
//
// This package is a protocol/planner boundary only. It does not execute media
// work, isolate workers, publish CAS objects, encode codecs or mux containers.
package chunkplan
