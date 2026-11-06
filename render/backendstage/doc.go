// Package backendstage connects a frozen prepared environment and a recoverable
// RenderBackend to Cutly's durable stage runtime.
//
// V1 intentionally models a complete backend render as one StagePublish task.
// The published media bytes are also committed to CAS under the semantic stage
// key, while the checkpoint receipt remains bound to the individual job and
// publication URI. Completion revalidates both copies and decoded evidence.
package backendstage
