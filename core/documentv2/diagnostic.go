package documentv2

import "github.com/CutlyLab/Cutly/core/videoedit"

type Diagnostic = videoedit.Diagnostic
type DiagnosticSeverity = videoedit.DiagnosticSeverity

const (
	SeverityInfo    = videoedit.SeverityInfo
	SeverityWarning = videoedit.SeverityWarning
	SeverityError   = videoedit.SeverityError
)

const (
	CodeSchemaUnsupported       = "DOC.V2.SCHEMA_UNSUPPORTED"
	CodeIDRequired              = "DOC.V2.ID_REQUIRED"
	CodeDuplicateID             = "DOC.V2.DUPLICATE_ID"
	CodeJSONInvalid             = "DOC.V2.JSON_INVALID"
	CodeAssetInvalid            = "DOC.V2.ASSET_INVALID"
	CodeAssetReferenceInvalid   = "DOC.V2.ASSET_REFERENCE_INVALID"
	CodeTrackInvalid            = "DOC.V2.TRACK_INVALID"
	CodeClipInvalid             = "DOC.V2.CLIP_INVALID"
	CodeClipOverlap             = "DOC.V2.CLIP_OVERLAP"
	CodeTransitionInvalid       = "DOC.V2.TRANSITION_INVALID"
	CodeTransitionHandleMissing = "DOC.V2.TRANSITION_HANDLE_MISSING"
	CodeTransitionOverlap       = "DOC.V2.TRANSITION_OVERLAP"
	CodePropertyInvalid         = "DOC.V2.PROPERTY_INVALID"
	CodeKeyframeInvalid         = "DOC.V2.KEYFRAME_INVALID"
	CodeAudioBusInvalid         = "DOC.V2.AUDIO_BUS_INVALID"
	CodeAudioBusCycle           = "DOC.V2.AUDIO_BUS_CYCLE"
	CodeAnnotationInvalid       = "DOC.V2.ANNOTATION_INVALID"
	CodeMigrationInvalid        = "DOC.V2.MIGRATION_INVALID"
	CodeExtensionNeedsConsumer  = "CAPABILITY.V2.EXTENSION_CONSUMER_REQUIRED"
)

func HasErrorDiagnostics(values []Diagnostic) bool {
	return videoedit.HasErrorDiagnostics(values)
}
