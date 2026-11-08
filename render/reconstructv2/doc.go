// Package reconstructv2 rebuilds native static-visual and WAV execution handles
// from verified Document v2 prepared environments under current trusted host policy.
//
// A host constructs an Authority and ResourceSource independently of request data,
// supplies explicit budgets through Config, and calls New followed by Reconstruct.
// Reconstruct checks the original Document/plan/manifest, host pins, supported scope,
// native configuration and aggregate admission before resource callbacks. It then
// authorizes resources, snapshots private bytes, checks actual native inspections,
// and compiles both domains without pruning or rewriting the original Document.
//
// The initial profile supports one timeline with static PNG, vector, pinned-font
// text and WAV sources. It requires the full enabled source closure of that original
// timeline to be selected; temporal video, transitions, audio buses, URI-only selected
// assets and omitted enabled clips are rejected. Native compilers impose additional
// explicit property/profile restrictions. There is no fallback to legacy rendering.
//
// Bundle is a process-local handle, not a durable identity. Persist the prepared
// environment using package prepared; after restart, rebuild trusted host services
// and invoke Reconstruct again. Bridge is an owned unsigned receipt that separately
// binds semantic selections and actual native configurations/source observations.
// Decoding or rehashing a Bridge grants no resource or execution authority.
//
// Render methods accept composition tick indexes using the persisted clocks, not
// caller-supplied clocks. Audio blocks must fit the exact rational requested range,
// including their exclusive cell endpoint. Even a zero-length audio block must
// start strictly inside that range. Returned frame and PCM values are caller-owned;
// no raw native Program, provider or mutable configuration getter is exposed.
//
// Budgets are finite logical admission charges, not hard process RSS, GC or wall-time
// guarantees. ResourceSource and Authority callbacks must honor context cancellation
// and remain concurrency-safe. A blocked synchronous host callback, Read or Close
// cannot be forcibly interrupted by this package. Callers must not overwrite a live
// Factory or Bundle while another goroutine uses it. This package does not launch
// media executables, schedule workers, assemble MP4, publish output or switch daemon
// defaults.
package reconstructv2
