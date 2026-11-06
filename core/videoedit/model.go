package videoedit

import "encoding/json"

// SchemaVersion is the only document schema understood by this package. Adapters may import older
// formats, but the core never guesses how an unversioned document should behave.
const SchemaVersion = 1

// ID is a stable, document-wide identifier. IDs are opaque: callers must not derive semantic
// meaning or array positions from them.
type ID string

// Revision is incremented by the transaction layer after every committed change.
type Revision uint64

// AssetKind describes the media contract of an asset without tying the document to a decoder.
type AssetKind string

const (
	AssetKindVideo AssetKind = "video"
	AssetKindAudio AssetKind = "audio"
	AssetKindImage AssetKind = "image"
	AssetKindFont  AssetKind = "font"
	AssetKindData  AssetKind = "data"
)

// TrackKind is deliberately small in v1. More specialized behavior belongs in typed properties or
// later schema versions rather than in ad-hoc string values.
type TrackKind string

const (
	TrackKindVideo TrackKind = "video"
	TrackKindAudio TrackKind = "audio"
)

// Document is the canonical, serializable editing state. Rendering, generated analysis and agent
// conversations are consumers of this state; none of them are allowed to become an alternate
// source of truth.
type Document struct {
	SchemaVersion int                        `json:"schema_version"`
	ID            ID                         `json:"id"`
	Revision      Revision                   `json:"revision"`
	Assets        []Asset                    `json:"assets,omitempty"`
	Timelines     []Timeline                 `json:"timelines,omitempty"`
	Annotations   []Annotation               `json:"annotations,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
}

// Asset references media by URI and, when available, immutable content hash. Duration is optional
// because ingest may create a document before probing has completed.
type Asset struct {
	ID          ID                         `json:"id"`
	Kind        AssetKind                  `json:"kind"`
	URI         string                     `json:"uri,omitempty"`
	ContentHash string                     `json:"content_hash,omitempty"`
	Duration    *RationalTime              `json:"duration,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// Timeline owns ordered tracks. Array order is meaningful: for video it is the default compositing
// order, and for audio it is the default deterministic mix order.
type Timeline struct {
	ID          ID                         `json:"id"`
	Name        string                     `json:"name,omitempty"`
	Tracks      []Track                    `json:"tracks,omitempty"`
	Annotations []Annotation               `json:"annotations,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// Track is a chronologically ordered, non-overlapping sequence of clips. Layering is represented
// by separate tracks, which keeps overlap semantics explicit for agents and render backends.
type Track struct {
	ID          ID                         `json:"id"`
	Kind        TrackKind                  `json:"kind"`
	Name        string                     `json:"name,omitempty"`
	Clips       []Clip                     `json:"clips,omitempty"`
	Annotations []Annotation               `json:"annotations,omitempty"`
	Metadata    map[string]json.RawMessage `json:"metadata,omitempty"`
}

// Clip maps an exact source range onto an exact timeline range. Properties are extension points for
// transforms, gain and other typed capabilities whose schemas live outside the base model.
type Clip struct {
	ID            ID                         `json:"id"`
	AssetID       ID                         `json:"asset_id"`
	TimelineRange TimeRange                  `json:"timeline_range"`
	SourceRange   TimeRange                  `json:"source_range"`
	Disabled      bool                       `json:"disabled,omitempty"`
	Properties    map[string]json.RawMessage `json:"properties,omitempty"`
	Annotations   []Annotation               `json:"annotations,omitempty"`
}

// Annotation carries namespaced intent or derived information without changing base rendering
// semantics. An annotation only affects rendering when a separately versioned capability declares
// that it consumes the namespace.
type Annotation struct {
	ID        ID              `json:"id"`
	Namespace string          `json:"namespace"`
	Key       string          `json:"key,omitempty"`
	Range     *TimeRange      `json:"range,omitempty"`
	Value     json.RawMessage `json:"value"`
}
