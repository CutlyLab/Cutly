// Package proberegistry persists host-verified probe manifests by their
// canonical fingerprint. A registry root is a host-owned trust boundary; it
// stores observations only after the embedding application has authorized and
// probed the immutable input bytes.
package proberegistry
