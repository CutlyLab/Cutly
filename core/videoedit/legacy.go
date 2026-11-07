package videoedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// LegacyTimeRate is the explicit quantization boundary used by ChatCut's float-seconds EDL.
// Values are rounded to the nearest microsecond, with half values rounded away from zero. All
// editing-core arithmetic remains exact RationalTime; only this adapter performs quantization.
const LegacyTimeRate int64 = 1_000_000

// Stable diagnostics emitted by the compatibility adapter.
const (
	CodeLegacyJSONInvalid       = "LEGACY.JSON_INVALID"
	CodeLegacyFieldInvalid      = "LEGACY.FIELD_INVALID"
	CodeLegacySourceRequired    = "LEGACY.SOURCE_REQUIRED"
	CodeLegacyTimelineRequired  = "LEGACY.TIMELINE_REQUIRED"
	CodeLegacyTimelineEmpty     = "LEGACY.TIMELINE_EMPTY"
	CodeLegacyTimeInvalid       = "LEGACY.TIME_INVALID"
	CodeLegacyRangeInvalid      = "LEGACY.RANGE_INVALID"
	CodeLegacyShapeUnsupported  = "LEGACY.SHAPE_UNSUPPORTED"
	CodeLegacyRetimeUnsupported = "LEGACY.RETIME_UNSUPPORTED"
)

// LegacyEnvelope contains presentation-only and unknown fields that are not part of the editing
// core. Persist it alongside a planned transaction, then pass it back to EncodeLegacyEDL so an
// edit never drops subtitles, overlays, or extension fields owned by another subsystem.
type LegacyEnvelope struct {
	Source           string                       `json:"source"`
	Output           string                       `json:"output,omitempty"`
	OutputPresent    bool                         `json:"output_present,omitempty"`
	Subtitles        string                       `json:"subtitles,omitempty"`
	SubtitlesPresent bool                         `json:"subtitles_present,omitempty"`
	Overlays         json.RawMessage              `json:"overlays,omitempty"`
	Extra            map[string]json.RawMessage   `json:"extra,omitempty"`
	Segments         map[ID]LegacySegmentEnvelope `json:"segments,omitempty"`
}

// LegacySegmentEnvelope preserves fields that follow a clip through typed edits. Text is kept
// outside Clip because the v1 core does not assign rendering semantics to transcript text.
type LegacySegmentEnvelope struct {
	Text              string                     `json:"text,omitempty"`
	TextPresent       bool                       `json:"text_present,omitempty"`
	PropertiesPresent bool                       `json:"properties_present,omitempty"`
	Extra             map[string]json.RawMessage `json:"extra,omitempty"`
}

// DecodeLegacyEDL imports the current ChatCut edl.json format into the canonical editing model.
// Syntax errors are returned as Go errors; correctable document problems are stable diagnostics.
func DecodeLegacyEDL(raw []byte) (Document, LegacyEnvelope, []Diagnostic, error) {
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return Document{}, LegacyEnvelope{}, nil, fmt.Errorf("videoedit: decode legacy EDL: %w", err)
	}

	envelope := LegacyEnvelope{
		Extra:    make(map[string]json.RawMessage),
		Segments: make(map[ID]LegacySegmentEnvelope),
	}
	var diagnostics []Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}

	known := map[string]bool{
		"source": true, "output": true, "subtitles": true, "timeline": true, "overlays": true,
		"schema_version": true, "document_id": true, "revision": true,
	}
	for key, value := range fields {
		if !known[key] {
			envelope.Extra[key] = cloneRaw(value)
		}
	}

	decodeOptionalString(fields, "source", "/source", &envelope.Source, add)
	_, envelope.OutputPresent = fields["output"]
	decodeOptionalString(fields, "output", "/output", &envelope.Output, add)
	_, envelope.SubtitlesPresent = fields["subtitles"]
	decodeOptionalString(fields, "subtitles", "/subtitles", &envelope.Subtitles, add)
	if strings.TrimSpace(envelope.Source) == "" {
		add(CodeLegacySourceRequired, "/source", "legacy EDL requires a non-empty source")
	}
	if envelope.Subtitles != "" && !strings.EqualFold(envelope.Subtitles, "none") &&
		!strings.EqualFold(envelope.Subtitles, "srt") && !strings.EqualFold(envelope.Subtitles, "burn") {
		add(CodeLegacyFieldInvalid, "/subtitles", "subtitles must be none, srt, burn, or empty")
	}

	if overlays, exists := fields["overlays"]; exists {
		var values []json.RawMessage
		if err := decodeJSONArray(overlays, &values); err != nil {
			add(CodeLegacyFieldInvalid, "/overlays", "overlays must be a JSON array")
		} else {
			envelope.Overlays = cloneRaw(overlays)
		}
	}

	documentID := deterministicLegacyID("doc", envelope.Source)
	if rawID, exists := fields["document_id"]; exists {
		var value string
		if err := json.Unmarshal(rawID, &value); err != nil || strings.TrimSpace(value) == "" {
			add(CodeLegacyFieldInvalid, "/document_id", "document_id must be a non-empty string")
		} else {
			documentID = ID(value)
		}
	}
	revision := Revision(0)
	if rawRevision, exists := fields["revision"]; exists {
		parsed, parseErr := decodeLegacyRevision(rawRevision)
		if parseErr != nil {
			add(CodeLegacyFieldInvalid, "/revision", parseErr.Error())
		} else {
			revision = parsed
		}
	}
	if rawVersion, exists := fields["schema_version"]; exists {
		var version int
		if err := json.Unmarshal(rawVersion, &version); err != nil || version != SchemaVersion {
			add(CodeSchemaUnsupported, "/schema_version", fmt.Sprintf("legacy extension schema must be %d", SchemaVersion))
		}
	}

	assetID := deterministicLegacyID("asset", string(documentID), envelope.Source)
	timelineID := deterministicLegacyID("timeline", string(documentID))
	trackID := deterministicLegacyID("track", string(documentID), "video")
	document := Document{
		SchemaVersion: SchemaVersion,
		ID:            documentID,
		Revision:      revision,
		Assets:        []Asset{{ID: assetID, Kind: AssetKindVideo, URI: envelope.Source}},
		Timelines: []Timeline{{
			ID:     timelineID,
			Tracks: []Track{{ID: trackID, Kind: TrackKindVideo}},
		}},
	}

	rawTimeline, exists := fields["timeline"]
	if !exists {
		add(CodeLegacyTimelineRequired, "/timeline", "legacy EDL requires a timeline array")
		return document, envelope, append(diagnostics, ValidateDocument(document)...), nil
	}
	var segments []json.RawMessage
	if err := decodeJSONArray(rawTimeline, &segments); err != nil {
		add(CodeLegacyTimelineRequired, "/timeline", "timeline must be a JSON array")
		return document, envelope, append(diagnostics, ValidateDocument(document)...), nil
	}
	if len(segments) == 0 {
		add(CodeLegacyTimelineEmpty, "/timeline", "legacy timeline must contain at least one segment")
	}

	cursor := RationalTime{Value: 0, Rate: LegacyTimeRate}
	occurrences := make(map[string]int)
	for index, rawSegment := range segments {
		path := fmt.Sprintf("/timeline/%d", index)
		segment, segmentErr := decodeJSONObject(rawSegment)
		if segmentErr != nil {
			add(CodeLegacyFieldInvalid, path, "timeline segment must be a JSON object")
			continue
		}

		start, startOK := decodeLegacySeconds(segment["start"], path+"/start", add)
		end, endOK := decodeLegacySeconds(segment["end"], path+"/end", add)
		if !startOK || !endOK {
			continue
		}
		if start.Value < 0 {
			add(CodeLegacyRangeInvalid, path+"/start", "segment start must not be negative")
			continue
		}
		if end.Value <= start.Value {
			add(CodeLegacyRangeInvalid, path, "segment end must be greater than start after microsecond quantization")
			continue
		}
		duration := RationalTime{Value: end.Value - start.Value, Rate: LegacyTimeRate}

		clipID := ID("")
		if rawID, hasID := segment["id"]; hasID {
			var value string
			if err := json.Unmarshal(rawID, &value); err != nil || strings.TrimSpace(value) == "" {
				add(CodeLegacyFieldInvalid, path+"/id", "segment id must be a non-empty string")
				continue
			}
			clipID = ID(value)
		} else {
			key, identityErr := legacySegmentIdentityKey(segment, start, end)
			if identityErr != nil {
				add(CodeLegacyJSONInvalid, path, identityErr.Error())
				continue
			}
			occurrence := occurrences[key]
			occurrences[key]++
			clipID = deterministicLegacyID("clip", string(documentID), key, strconv.Itoa(occurrence))
		}

		segmentEnvelope := LegacySegmentEnvelope{Extra: make(map[string]json.RawMessage)}
		_, segmentEnvelope.TextPresent = segment["text"]
		decodeOptionalString(segment, "text", path+"/text", &segmentEnvelope.Text, add)
		properties := make(map[string]json.RawMessage)
		if rawProperties, hasProperties := segment["properties"]; hasProperties {
			segmentEnvelope.PropertiesPresent = true
			decodedProperties, err := decodeJSONObject(rawProperties)
			if err != nil {
				add(CodeLegacyFieldInvalid, path+"/properties", "properties must be a JSON object")
				continue
			}
			properties = decodedProperties
			properties = cloneRawMap(properties)
		}
		for key, value := range segment {
			switch key {
			case "id", "start", "end", "text", "properties":
			default:
				segmentEnvelope.Extra[key] = cloneRaw(value)
			}
		}
		envelope.Segments[clipID] = segmentEnvelope

		document.Timelines[0].Tracks[0].Clips = append(document.Timelines[0].Tracks[0].Clips, Clip{
			ID:            clipID,
			AssetID:       assetID,
			TimelineRange: TimeRange{Start: cursor, Duration: duration},
			SourceRange:   TimeRange{Start: start, Duration: duration},
			Properties:    properties,
		})
		nextCursor, addErr := cursor.Add(duration)
		if addErr != nil {
			add(CodeLegacyTimeInvalid, path, "output timeline duration overflows rational time")
			continue
		}
		cursor, addErr = nextCursor.Rescale(LegacyTimeRate)
		if addErr != nil {
			add(CodeLegacyTimeInvalid, path, "output timeline cannot be represented at the legacy time base")
			continue
		}
	}

	diagnostics = append(diagnostics, ValidateDocument(document)...)
	return document, envelope, diagnostics, nil
}

// NormalizeLegacyTimeline explicitly converts a canonical one-track cut to ChatCut's sequence
// semantics. Clips are ordered by their current timeline start (then stable ID), placed back to
// back from zero, and their timeline durations are aligned to their source durations. Callers must
// opt into this transform; EncodeLegacyEDL itself never hides gaps, overlaps, or retiming.
func NormalizeLegacyTimeline(document Document) (Document, []Diagnostic, error) {
	normalized, err := cloneDocument(document)
	if err != nil {
		return document, nil, err
	}
	var diagnostics []Diagnostic
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}
	if len(normalized.Timelines) != 1 {
		add(CodeLegacyShapeUnsupported, "/timelines", "legacy sequence normalization requires exactly one timeline")
		return document, diagnostics, nil
	}

	timeline := &normalized.Timelines[0]
	trackIndex := -1
	for index, track := range timeline.Tracks {
		if len(track.Clips) == 0 {
			continue
		}
		if track.Kind != TrackKindVideo || trackIndex >= 0 {
			add(CodeLegacyShapeUnsupported, fmt.Sprintf("/timelines/0/tracks/%d", index), "legacy sequence normalization supports exactly one non-empty video track")
			continue
		}
		trackIndex = index
	}
	if trackIndex < 0 {
		add(CodeLegacyTimelineEmpty, "/timelines/0/tracks", "legacy sequence normalization requires at least one clip")
	}
	if HasErrorDiagnostics(diagnostics) {
		return document, diagnostics, nil
	}

	track := &timeline.Tracks[trackIndex]
	for index, clip := range track.Clips {
		path := fmt.Sprintf("/timelines/0/tracks/%d/clips/%d", trackIndex, index)
		if clip.Disabled {
			add(CodeLegacyShapeUnsupported, path+"/disabled", "legacy sequence normalization cannot preserve disabled clips")
		}
		if err := clip.TimelineRange.Start.Validate(); err != nil {
			add(CodeLegacyTimeInvalid, path+"/timeline_range/start", err.Error())
		}
		if err := clip.SourceRange.Validate(); err != nil || clip.SourceRange.Duration.Value <= 0 {
			add(CodeLegacyRangeInvalid, path+"/source_range", "source range must have a positive valid duration")
		}
	}
	if HasErrorDiagnostics(diagnostics) {
		return document, diagnostics, nil
	}

	sort.SliceStable(track.Clips, func(i, j int) bool {
		comparison, _ := track.Clips[i].TimelineRange.Start.Compare(track.Clips[j].TimelineRange.Start)
		if comparison != 0 {
			return comparison < 0
		}
		return track.Clips[i].ID < track.Clips[j].ID
	})
	cursor := RationalTime{Value: 0, Rate: LegacyTimeRate}
	for index := range track.Clips {
		track.Clips[index].TimelineRange = TimeRange{
			Start:    cursor,
			Duration: track.Clips[index].SourceRange.Duration,
		}
		cursor, err = cursor.Add(track.Clips[index].SourceRange.Duration)
		if err != nil {
			add(CodeLegacyTimeInvalid, fmt.Sprintf("/timelines/0/tracks/%d/clips/%d", trackIndex, index), "normalized timeline duration overflows rational time")
			return document, diagnostics, nil
		}
		if legacyCursor, rescaleErr := cursor.Rescale(LegacyTimeRate); rescaleErr == nil {
			cursor = legacyCursor
		}
	}
	diagnostics = append(diagnostics, ValidateDocument(normalized)...)
	if HasErrorDiagnostics(diagnostics) {
		return document, diagnostics, nil
	}
	return normalized, diagnostics, nil
}

// EncodeLegacyEDL exports a canonical single-source cut while preserving its LegacyEnvelope. It
// intentionally rejects shapes the old assembler cannot represent instead of silently flattening
// tracks, retiming clips, or dropping media.
func EncodeLegacyEDL(document Document, envelope LegacyEnvelope) ([]byte, []Diagnostic, error) {
	diagnostics := ValidateDocument(document)
	add := func(code, path, message string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
	}
	if HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, nil
	}

	if len(document.Timelines) != 1 {
		add(CodeLegacyShapeUnsupported, "/timelines", "legacy EDL export requires exactly one timeline")
		return nil, diagnostics, nil
	}
	timeline := document.Timelines[0]
	var clips []Clip
	for trackIndex, track := range timeline.Tracks {
		if len(track.Clips) == 0 {
			continue
		}
		if track.Kind != TrackKindVideo || len(clips) > 0 {
			add(CodeLegacyShapeUnsupported, fmt.Sprintf("/timelines/0/tracks/%d", trackIndex), "legacy EDL supports clips on exactly one video track")
			continue
		}
		clips = append(clips, track.Clips...)
	}
	if len(clips) == 0 {
		add(CodeLegacyTimelineEmpty, "/timelines/0/tracks", "legacy timeline must contain at least one enabled video clip")
	}
	if HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, nil
	}

	assets := make(map[ID]Asset, len(document.Assets))
	for _, asset := range document.Assets {
		assets[asset.ID] = asset
	}
	sort.SliceStable(clips, func(i, j int) bool {
		comparison, err := clips[i].TimelineRange.Start.Compare(clips[j].TimelineRange.Start)
		if err == nil && comparison != 0 {
			return comparison < 0
		}
		return clips[i].ID < clips[j].ID
	})

	root := make(map[string]any, len(envelope.Extra)+8)
	for key, value := range envelope.Extra {
		root[key] = cloneRaw(value)
	}
	root["schema_version"] = SchemaVersion
	root["document_id"] = document.ID
	root["revision"] = document.Revision
	if envelope.OutputPresent || envelope.Output != "" {
		root["output"] = envelope.Output
	}
	if envelope.SubtitlesPresent || envelope.Subtitles != "" {
		root["subtitles"] = envelope.Subtitles
	}
	if len(envelope.Overlays) > 0 {
		if !json.Valid(envelope.Overlays) {
			add(CodeLegacyJSONInvalid, "/overlays", "preserved overlays contain invalid JSON")
		} else {
			var overlays []json.RawMessage
			if err := decodeJSONArray(envelope.Overlays, &overlays); err != nil {
				add(CodeLegacyFieldInvalid, "/overlays", "preserved overlays must be a JSON array")
			} else {
				root["overlays"] = cloneRaw(envelope.Overlays)
			}
		}
	}

	var sourceAssetID ID
	var source string
	encoded := make([]map[string]any, 0, len(clips))
	expectedTimelineStart := RationalTime{Value: 0, Rate: LegacyTimeRate}
	for index, clip := range clips {
		path := fmt.Sprintf("/timelines/0/clips/%d", index)
		if clip.Disabled {
			add(CodeLegacyShapeUnsupported, path+"/disabled", "legacy EDL cannot preserve disabled clips")
			continue
		}
		asset, exists := assets[clip.AssetID]
		if !exists {
			continue // ValidateDocument already reported this.
		}
		if asset.Kind != AssetKindVideo {
			add(CodeLegacyShapeUnsupported, path+"/asset_id", "legacy EDL clips must reference a video asset")
			continue
		}
		if sourceAssetID == "" {
			sourceAssetID, source = asset.ID, asset.URI
		} else if sourceAssetID != asset.ID {
			add(CodeLegacyShapeUnsupported, path+"/asset_id", "legacy EDL supports only one source asset")
			continue
		}
		comparison, compareErr := clip.TimelineRange.Duration.Compare(clip.SourceRange.Duration)
		if compareErr != nil || comparison != 0 {
			add(CodeLegacyRetimeUnsupported, path, "legacy EDL cannot represent retimed clips")
			continue
		}
		startComparison, startCompareErr := clip.TimelineRange.Start.Compare(expectedTimelineStart)
		if startCompareErr != nil || startComparison != 0 {
			add(CodeLegacyShapeUnsupported, path+"/timeline_range/start", "legacy EDL requires clips to form a contiguous timeline starting at zero; call NormalizeLegacyTimeline explicitly")
			continue
		}
		nextTimelineStart, addErr := expectedTimelineStart.Add(clip.TimelineRange.Duration)
		if addErr != nil {
			add(CodeLegacyTimeInvalid, path+"/timeline_range", "timeline duration overflows rational time")
			continue
		}
		expectedTimelineStart = nextTimelineStart
		startTick, startErr := legacyTick(clip.SourceRange.Start)
		endTime, endErr := clip.SourceRange.End()
		if startErr != nil || endErr != nil {
			add(CodeLegacyTimeInvalid, path+"/source_range", "source range cannot be represented as legacy seconds")
			continue
		}
		endTick, tickErr := legacyTick(endTime)
		if tickErr != nil || startTick < 0 || endTick <= startTick {
			add(CodeLegacyRangeInvalid, path+"/source_range", "source range collapses or is negative after microsecond quantization")
			continue
		}

		segment := make(map[string]any)
		preserved, preservedExists := envelope.Segments[clip.ID]
		if preservedExists {
			for key, value := range preserved.Extra {
				segment[key] = cloneRaw(value)
			}
			if preserved.TextPresent || preserved.Text != "" {
				segment["text"] = preserved.Text
			}
		}
		segment["id"] = clip.ID
		segment["start"] = json.Number(formatLegacyTick(startTick))
		segment["end"] = json.Number(formatLegacyTick(endTick))
		if len(clip.Properties) > 0 {
			segment["properties"] = cloneRawMap(clip.Properties)
		} else if preservedExists && preserved.PropertiesPresent {
			// Document cloning may turn an empty map into nil because Clip.Properties uses
			// omitempty. The envelope records presence so an explicit {} stays an object.
			segment["properties"] = map[string]json.RawMessage{}
		}
		encoded = append(encoded, segment)
	}
	if strings.TrimSpace(source) == "" {
		source = envelope.Source
	}
	if strings.TrimSpace(source) == "" {
		add(CodeLegacySourceRequired, "/source", "legacy EDL export requires a source URI")
	}
	if HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, nil
	}
	root["source"] = source
	root["timeline"] = encoded

	result, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, diagnostics, fmt.Errorf("videoedit: encode legacy EDL: %w", err)
	}
	return append(result, '\n'), diagnostics, nil
}

func decodeJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeJSONArray(raw []byte, target *[]json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("expected JSON array")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value []json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	*target = value
	return nil
}

func legacySegmentIdentityKey(segment map[string]json.RawMessage, start, end RationalTime) (string, error) {
	content := make(map[string]json.RawMessage, len(segment))
	for key, value := range segment {
		switch key {
		case "id", "start", "end":
			continue
		default:
			content[key] = cloneRaw(value)
		}
	}
	canonical, err := CanonicalJSON(content)
	if err != nil {
		return "", fmt.Errorf("cannot canonicalize legacy segment identity: %w", err)
	}
	return fmt.Sprintf("%d:%d:%s", start.Value, end.Value, canonical), nil
}

func decodeOptionalString(fields map[string]json.RawMessage, key, path string, target *string, add func(string, string, string)) {
	raw, exists := fields[key]
	if !exists {
		return
	}
	if err := json.Unmarshal(raw, target); err != nil {
		add(CodeLegacyFieldInvalid, path, fmt.Sprintf("%s must be a string", key))
	}
}

func decodeLegacyRevision(raw json.RawMessage) (Revision, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, fmt.Errorf("revision must be a non-negative integer")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("revision must be a non-negative integer")
	}
	parsed, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("revision must be a non-negative integer")
	}
	return Revision(parsed), nil
}

func decodeLegacySeconds(raw json.RawMessage, path string, add func(string, string, string)) (RationalTime, bool) {
	if len(raw) == 0 {
		add(CodeLegacyTimeInvalid, path, "time value is required")
		return RationalTime{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		add(CodeLegacyTimeInvalid, path, "time value must be a finite JSON number")
		return RationalTime{}, false
	}
	number, ok := value.(json.Number)
	if !ok {
		add(CodeLegacyTimeInvalid, path, "time value must be a finite JSON number")
		return RationalTime{}, false
	}
	if parsed, err := strconv.ParseFloat(number.String(), 64); err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		add(CodeLegacyTimeInvalid, path, "time value must be finite and fit the legacy float-seconds format")
		return RationalTime{}, false
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok {
		add(CodeLegacyTimeInvalid, path, "time value is not a valid decimal number")
		return RationalTime{}, false
	}
	tick, err := roundedTick(rational)
	if err != nil {
		add(CodeLegacyTimeInvalid, path, err.Error())
		return RationalTime{}, false
	}
	return RationalTime{Value: tick, Rate: LegacyTimeRate}, true
}

func legacyTick(value RationalTime) (int64, error) {
	if err := value.Validate(); err != nil {
		return 0, err
	}
	rational := new(big.Rat).SetFrac(big.NewInt(value.Value), big.NewInt(value.Rate))
	return roundedTick(rational)
}

func roundedTick(value *big.Rat) (int64, error) {
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt64(LegacyTimeRate))
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if new(big.Int).Lsh(new(big.Int).Abs(remainder), 1).Cmp(scaled.Denom()) >= 0 {
		if scaled.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, ErrTimeOverflow
	}
	return quotient.Int64(), nil
}

func formatLegacyTick(tick int64) string {
	negative := tick < 0
	abs := new(big.Int).SetInt64(tick)
	if negative {
		abs.Abs(abs)
	}
	denominator := big.NewInt(LegacyTimeRate)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(abs, denominator, fraction)
	result := whole.String()
	if fraction.Sign() != 0 {
		fractionText := fmt.Sprintf("%06d", fraction.Int64())
		result += "." + strings.TrimRight(fractionText, "0")
	}
	if negative {
		return "-" + result
	}
	return result
}

func deterministicLegacyID(kind string, parts ...string) ID {
	hash := sha256.New()
	hash.Write([]byte("videoedit:legacy:" + kind))
	for _, part := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	sum := hash.Sum(nil)
	return ID(kind + "_" + hex.EncodeToString(sum[:12]))
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
