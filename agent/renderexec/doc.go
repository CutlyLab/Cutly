// Package renderexec exposes strict render preflight and exact-frame actions
// through the public Agent protocol.
//
// The package is deliberately backend-agnostic. A daemon installs the routes
// only when it can resolve a real RenderBackend for the frozen execution
// manifest. The service revalidates the semantic plan, manifest, live backend
// identity and exact-frame artifact; a resolver cannot weaken those bindings.
// NewWithRecorder additionally makes a successful preflight durable before a
// ready result is returned, so later queue submission can resolve the exact
// frozen environment by fingerprint.
package renderexec
