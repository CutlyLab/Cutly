// Package videoedit defines Cutly's deterministic, backend-neutral editing contracts.
//
// The package has no I/O, process, wall-clock, random-number, UI, Agent, or renderer dependencies.
// Callers provide stable identifiers and persist Documents, plans, and commit records outside the
// package. This keeps the semantic core independently buildable and suitable for preview, export,
// CLI, and Agent consumers.
package videoedit
