// Package ffmpeghost resolves the host-owned FFmpeg backend used by Cutly's
// interactive Agent preflight and exact-frame actions.
//
// Resolution deliberately performs a fresh probe of every materialized file
// and durably registers the resulting immutable probe manifest before exposing
// the backend. Queued workers use the frozen registry instead; they must not use
// this resolver because publication recovery must remain possible while source
// files are temporarily offline.
//
// Configured input roots are a host-owned namespace boundary. Locator
// authorization and FFprobe's content-identity verification are separate
// operations, so untrusted processes must not be allowed to rewrite that
// namespace between them; ExpectedContentHash still prevents replacement bytes
// from being accepted as the frozen resource.
package ffmpeghost
