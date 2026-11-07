package prepared

import (
	"context"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	RecordSchemaVersion = 1
	RecordProfileV1     = "cutly.render.prepared-environment.v1"

	DefaultMaxEntries        = 1 << 16
	DefaultMaxScratchEntries = 1024
	DefaultMaxRecordBytes    = 64 << 20
	MaxEntries               = 1 << 20
	MaxScratchEntries        = 1 << 16
	MaxRecordBytes           = 64 << 20
)

// Options bounds persistent entries, live scratch directories, and record bytes.
// Zero values select the documented defaults.
type Options struct {
	MaxEntries        int
	MaxScratchEntries int
	MaxRecordBytes    int
	// MaxRecordBytesV2 independently bounds document-v2 records. Zero selects
	// MaxRecordBytesV2 (160 MiB); explicit values may only lower that ceiling.
	MaxRecordBytesV2 int
}

// Key is the complete immutable identity of one prepared environment.
type Key struct {
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
}

// Environment owns a strictly decoded plan and its exact frozen manifest.
type Environment struct {
	Plan     videoedit.RenderPlan        `json:"plan"`
	Manifest renderapi.ExecutionManifest `json:"execution_manifest"`
}

// RecordPrepared adapts Store to agent/renderexec. Project scoping belongs to the
// embedding application's registry selection; a Store itself owns one root.
func (store *Store) RecordPrepared(ctx context.Context, _ string, plan videoedit.RenderPlan, manifest renderapi.ExecutionManifest) error {
	_, _, err := store.Put(ctx, plan, manifest)
	return err
}
