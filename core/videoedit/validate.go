package videoedit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ValidateDocument performs structural and cross-reference validation without touching media or the
// filesystem. Capability and asset availability checks belong to preflight/Resolve.
func ValidateDocument(document Document) []Diagnostic {
	var diagnostics []Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}

	if document.SchemaVersion != SchemaVersion {
		add(CodeSchemaUnsupported, "/schema_version", fmt.Sprintf("schema version %d is not supported; expected %d", document.SchemaVersion, SchemaVersion))
	}
	ids := make(map[ID]string)
	registerID := func(id ID, path string) {
		if strings.TrimSpace(string(id)) == "" {
			add(CodeIDRequired, path, "stable ID is required")
			return
		}
		if previous, exists := ids[id]; exists {
			add(CodeDuplicateID, path, fmt.Sprintf("ID %q is already used at %s", id, previous))
			return
		}
		ids[id] = path
	}
	registerID(document.ID, "/id")
	validateRawMap(document.Metadata, "/metadata", add)

	assets := make(map[ID]Asset, len(document.Assets))
	for index, asset := range document.Assets {
		path := fmt.Sprintf("/assets/%d", index)
		registerID(asset.ID, path+"/id")
		if !validAssetKind(asset.Kind) {
			add(CodeAssetKindInvalid, path+"/kind", fmt.Sprintf("unknown asset kind %q", asset.Kind))
		}
		if strings.TrimSpace(asset.URI) == "" && strings.TrimSpace(asset.ContentHash) == "" {
			add(CodeAssetLocatorRequired, path, "asset requires uri or content_hash")
		}
		if asset.ContentHash != "" && !canonicalSHA256Digest(asset.ContentHash) {
			add(CodeAssetHashInvalid, path+"/content_hash", "content hash must be a canonical lowercase sha256 digest")
		}
		if asset.Duration != nil {
			if err := asset.Duration.Validate(); err != nil || asset.Duration.Value < 0 {
				message := "asset duration is invalid"
				if err != nil {
					message += ": " + err.Error()
				}
				add(CodeAssetDurationInvalid, path+"/duration", message)
			}
		}
		validateRawMap(asset.Metadata, path+"/metadata", add)
		if strings.TrimSpace(string(asset.ID)) != "" {
			assets[asset.ID] = asset
		}
	}

	for index := range document.Annotations {
		validateAnnotation(document.Annotations[index], fmt.Sprintf("/annotations/%d", index), registerID, add)
	}

	for timelineIndex, timeline := range document.Timelines {
		timelinePath := fmt.Sprintf("/timelines/%d", timelineIndex)
		registerID(timeline.ID, timelinePath+"/id")
		validateRawMap(timeline.Metadata, timelinePath+"/metadata", add)
		for index := range timeline.Annotations {
			validateAnnotation(timeline.Annotations[index], fmt.Sprintf("%s/annotations/%d", timelinePath, index), registerID, add)
		}

		for trackIndex, track := range timeline.Tracks {
			trackPath := fmt.Sprintf("%s/tracks/%d", timelinePath, trackIndex)
			registerID(track.ID, trackPath+"/id")
			if track.Kind != TrackKindVideo && track.Kind != TrackKindAudio {
				add(CodeTrackKindInvalid, trackPath+"/kind", fmt.Sprintf("unknown track kind %q", track.Kind))
			}
			validateRawMap(track.Metadata, trackPath+"/metadata", add)
			for index := range track.Annotations {
				validateAnnotation(track.Annotations[index], fmt.Sprintf("%s/annotations/%d", trackPath, index), registerID, add)
			}

			type positionedClip struct {
				index int
				clip  Clip
			}
			positioned := make([]positionedClip, 0, len(track.Clips))
			for clipIndex, clip := range track.Clips {
				clipPath := fmt.Sprintf("%s/clips/%d", trackPath, clipIndex)
				registerID(clip.ID, clipPath+"/id")
				if _, exists := assets[clip.AssetID]; !exists {
					add(CodeClipAssetMissing, clipPath+"/asset_id", fmt.Sprintf("asset %q does not exist", clip.AssetID))
				}
				validTimelineRange := validateClipRange(clip.TimelineRange, clipPath+"/timeline_range", add)
				validSourceRange := validateClipRange(clip.SourceRange, clipPath+"/source_range", add)
				validateRawMap(clip.Properties, clipPath+"/properties", add)
				for index := range clip.Annotations {
					validateAnnotation(clip.Annotations[index], fmt.Sprintf("%s/annotations/%d", clipPath, index), registerID, add)
				}

				if asset, exists := assets[clip.AssetID]; exists && asset.Duration != nil && validSourceRange {
					if sourceEnd, err := clip.SourceRange.End(); err == nil {
						if comparison, err := sourceEnd.Compare(*asset.Duration); err == nil && comparison > 0 {
							add(CodeClipSourceOutOfBounds, clipPath+"/source_range", fmt.Sprintf("source range ends after asset %q duration", clip.AssetID))
						}
					}
				}
				if validTimelineRange {
					positioned = append(positioned, positionedClip{index: clipIndex, clip: clip})
				}
			}

			// Clip arrays are part of the canonical document and therefore must already be in
			// chronological order. Mutations may sort their result, but accepting an unsorted input
			// would make apply + inverse unable to restore the original canonical document.
			for index := 1; index < len(positioned); index++ {
				previous, current := positioned[index-1], positioned[index]
				comparison, err := current.clip.TimelineRange.Start.Compare(previous.clip.TimelineRange.Start)
				if err == nil && comparison < 0 {
					path := fmt.Sprintf("%s/clips/%d/timeline_range/start", trackPath, current.index)
					add(CodeClipOrderInvalid, path, fmt.Sprintf("clip %q starts before preceding clip %q", current.clip.ID, previous.clip.ID))
				}
			}

			sort.SliceStable(positioned, func(i, j int) bool {
				comparison, _ := positioned[i].clip.TimelineRange.Start.Compare(positioned[j].clip.TimelineRange.Start)
				if comparison == 0 {
					return positioned[i].index < positioned[j].index
				}
				return comparison < 0
			})
			// Compare against the prior interval with the farthest end, not only the immediately
			// preceding interval. Otherwise a long clip containing several short clips reports only
			// the first nested overlap and silently misses the rest.
			if len(positioned) > 0 {
				active := positioned[0]
				activeEnd, err := active.clip.TimelineRange.End()
				if err == nil {
					for index := 1; index < len(positioned); index++ {
						current := positioned[index]
						comparison, compareErr := current.clip.TimelineRange.Start.Compare(activeEnd)
						if compareErr == nil && comparison < 0 {
							path := fmt.Sprintf("%s/clips/%d/timeline_range", trackPath, current.index)
							add(CodeClipOverlap, path, fmt.Sprintf("clip %q overlaps clip %q on the same track", current.clip.ID, active.clip.ID))
						}
						currentEnd, endErr := current.clip.TimelineRange.End()
						if endErr != nil {
							continue
						}
						if extends, compareErr := currentEnd.Compare(activeEnd); compareErr == nil && extends > 0 {
							active = current
							activeEnd = currentEnd
						}
					}
				}
			}
		}
	}
	return diagnostics
}

func validateClipRange(value TimeRange, path string, add func(string, string, string)) bool {
	if err := value.Validate(); err != nil {
		add(CodeTimeRangeInvalid, path, err.Error())
		return false
	}
	if value.Duration.Value == 0 {
		add(CodeClipDurationEmpty, path+"/duration", "clip duration must be greater than zero")
		return false
	}
	return true
}

func validateAnnotation(annotation Annotation, path string, registerID func(ID, string), add func(string, string, string)) {
	registerID(annotation.ID, path+"/id")
	if strings.TrimSpace(annotation.Namespace) == "" {
		add(CodeAnnotationInvalid, path+"/namespace", "annotation namespace is required")
	}
	if annotation.Range != nil {
		if err := annotation.Range.Validate(); err != nil {
			add(CodeTimeRangeInvalid, path+"/range", err.Error())
		}
	}
	if len(annotation.Value) == 0 || !json.Valid(annotation.Value) {
		add(CodeAnnotationInvalid, path+"/value", "annotation value must be valid JSON")
	}
}

func validateRawMap(values map[string]json.RawMessage, path string, add func(string, string, string)) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		if len(value) == 0 || !json.Valid(value) {
			add(CodeJSONInvalid, path+"/"+escapeJSONPointer(key), "extension value must be valid JSON")
		}
	}
}

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func validAssetKind(kind AssetKind) bool {
	switch kind {
	case AssetKindVideo, AssetKindAudio, AssetKindImage, AssetKindFont, AssetKindData:
		return true
	default:
		return false
	}
}
