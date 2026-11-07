package videoedit

import "encoding/json"

// DiagnosticSeverity is stable API surface for humans, agents and CI.
type DiagnosticSeverity string

const (
	SeverityInfo    DiagnosticSeverity = "info"
	SeverityWarning DiagnosticSeverity = "warning"
	SeverityError   DiagnosticSeverity = "error"
)

// Stable diagnostic codes. Messages may improve or be localized; callers branch on Code.
const (
	CodeSchemaUnsupported      = "DOC.SCHEMA_UNSUPPORTED"
	CodeIDRequired             = "DOC.ID_REQUIRED"
	CodeDuplicateID            = "DOC.DUPLICATE_ID"
	CodeJSONInvalid            = "DOC.JSON_INVALID"
	CodeAssetKindInvalid       = "ASSET.KIND_INVALID"
	CodeAssetLocatorRequired   = "ASSET.LOCATOR_REQUIRED"
	CodeAssetHashInvalid       = "ASSET.HASH_INVALID"
	CodeAssetDurationInvalid   = "ASSET.DURATION_INVALID"
	CodeTrackKindInvalid       = "TRACK.KIND_INVALID"
	CodeTimeRangeInvalid       = "TIME.RANGE_INVALID"
	CodeTimeIndexOverflow      = "TIME.INDEX_OVERFLOW"
	CodeClipDurationEmpty      = "CLIP.DURATION_EMPTY"
	CodeClipAssetMissing       = "CLIP.ASSET_MISSING"
	CodeClipSourceOutOfBounds  = "CLIP.SOURCE_OUT_OF_BOUNDS"
	CodeClipOrderInvalid       = "CLIP.ORDER_INVALID"
	CodeClipOverlap            = "CLIP.OVERLAP"
	CodeAnnotationInvalid      = "ANNOTATION.INVALID"
	CodeRenderRangeInvalid     = "RENDER.RANGE_INVALID"
	CodeRenderTimelineMissing  = "RENDER.TIMELINE_MISSING"
	CodeRenderTimelineRequired = "RENDER.TIMELINE_REQUIRED"
	CodeRenderTargetInvalid    = "RENDER.TARGET_INVALID"
	CodeRenderPlanUnsupported  = "RENDER.PLAN_SCHEMA_UNSUPPORTED"
	CodeCapabilityTrack        = "CAPABILITY.TRACK_UNSUPPORTED"
	CodeCapabilityAsset        = "CAPABILITY.ASSET_UNSUPPORTED"
	CodeCapabilityFeature      = "CAPABILITY.FEATURE_UNSUPPORTED"
	CodeCapabilityResolution   = "CAPABILITY.RESOLUTION_UNSUPPORTED"
)

// CandidateFix is deliberately independent from Transaction while the operation protocol evolves.
// Operation must contain a valid JSON operation object when present.
type CandidateFix struct {
	Description string          `json:"description"`
	Operation   json.RawMessage `json:"operation,omitempty"`
	SideEffects []string        `json:"side_effects,omitempty"`
}

// Diagnostic is the machine-readable error API. Path uses JSON Pointer syntax.
type Diagnostic struct {
	Code     string             `json:"code"`
	Severity DiagnosticSeverity `json:"severity"`
	Path     string             `json:"path,omitempty"`
	Message  string             `json:"message"`
	Fixes    []CandidateFix     `json:"fixes,omitempty"`
}

// HasErrorDiagnostics reports whether diagnostics contain a blocking error.
func HasErrorDiagnostics(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}
