package prepared

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

const (
	RecordSchemaVersionV2 = 2
	RecordProfileV2       = "cutly.render.prepared-environment.document-v2.v2"

	// DefaultMaxRecordBytesV2 admits one complete v2 environment: a Document v2 canonical
	// snapshot (<= 128 MiB), a semantic plan (<= 64 MiB) and a frozen execution manifest.
	DefaultMaxRecordBytesV2 = 256 << 20
	MaxRecordBytesV2        = 256 << 20
)

// EnvironmentV2 owns the complete persisted state of one Document v2 render environment.
// Every value is strictly decoded from canonical bytes; no live Program or provider handle
// is ever stored here.
type EnvironmentV2 struct {
	Document documentv2.Document           `json:"document"`
	Plan     renderplanv2.Plan             `json:"plan"`
	Manifest renderapi.ExecutionManifestV2 `json:"execution_manifest"`
}
