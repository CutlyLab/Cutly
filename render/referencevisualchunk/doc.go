// Package referencevisualchunk adapts one immutable referencevisual Program to Cutly's
// pre-codec backendstage SurfaceProvider boundary.
//
// The adapter is deliberately host-bound: construction binds one exact plan fingerprint,
// execution-manifest fingerprint, target, frame range, deterministic seed and Program identity.
// It does not derive or attest those plan identities, persist resource locators, rebuild a Program
// after restart, provide PCM, assemble a codec artifact or turn the legacy RenderPlan v3 into a
// Document v2 render plan. Hosts must freeze the returned Binding fingerprint into their own
// versioned execution contract before using its surfaces as reusable cache inputs.
package referencevisualchunk
