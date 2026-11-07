// Package local implements a bounded, crash-safe local stagedag checkpoint
// store. A checkpoint is reusable only while its cache/cas artifact still
// verifies byte-for-byte against the checkpoint output identity.
//
// The root and its ancestors are host-owned. The implementation rejects
// symlink roots, shards, entries and named files and uses derived hexadecimal
// paths only; it does not claim to resist a privileged process replacing a
// trusted ancestor or creating hostile hard links concurrently.
package local
