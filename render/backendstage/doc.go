// Package backendstage connects a frozen prepared environment and a recoverable
// RenderBackend to Cutly's durable stage runtime.
//
// The default restricted hard-cut path intentionally models a complete backend
// render as one StagePublish task. The optional executable reference profile
// adds fixed canonical surface/PCM tasks and requires a chunk-consuming
// assembler whose only decoded inputs are verified CAS readers bound to the
// complete aggregate roots. It resumes only the pre-codec chunks; a failed final
// codec invocation rereads them and restarts as a whole. Every payload is
// committed to CAS before its job-specific checkpoint, and completion
// revalidates CAS, receipts, aggregate roots, the aggregate-bound external
// publication, and decoded evidence.
package backendstage
