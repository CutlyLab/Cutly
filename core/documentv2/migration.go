package documentv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

// MigrateV1JSON is the strict raw migration entry point. It accepts only the exact v1 wire shape;
// v2/future documents, duplicate keys, case aliases, typed nulls and trailing values fail before
// migration. The returned v2 document does not share mutable maps or slices with the input bytes.
func MigrateV1JSON(raw []byte) (Document, []Diagnostic, error) {
	if err := validateJSONEncoding(raw); err != nil {
		return Document{}, nil, fmt.Errorf("documentv2: decode v1 migration input: %w", err)
	}
	var source videoedit.Document
	if err := wirejson.Decode(raw, &source); err != nil {
		return Document{}, nil, fmt.Errorf("documentv2: decode v1 migration input: %w", err)
	}
	return MigrateV1(source)
}

// MigrateV1 is pure and deterministic. It never invents random IDs and never guesses the schema
// of a v1 raw property. Unknown properties are wrapped with their exact key/value and marked as
// rendering-affecting legacy extensions, forcing capability preflight to reject them until an
// explicit typed migration is installed.
func MigrateV1(source videoedit.Document) (Document, []Diagnostic, error) {
	if paths := invalidUTF8Paths(source); len(paths) != 0 {
		diagnostics := make([]Diagnostic, 0, len(paths))
		for _, path := range paths {
			diagnostics = append(diagnostics, Diagnostic{Code: CodeMigrationInvalid, Severity: SeverityError, Path: path, Message: "v1 input contains invalid UTF-8"})
		}
		return Document{}, diagnostics, nil
	}
	diagnostics := videoedit.ValidateDocument(source)
	if videoedit.HasErrorDiagnostics(diagnostics) {
		return Document{}, cloneDiagnostics(diagnostics), nil
	}
	target := Document{
		SchemaVersion: SchemaVersion,
		ID:            source.ID,
		Revision:      source.Revision,
		Metadata:      cloneRawMap(source.Metadata),
	}
	migrationDiagnostics := make([]Diagnostic, 0)
	for _, asset := range source.Assets {
		target.Assets = append(target.Assets, Asset{
			ID: asset.ID, Kind: migrateAssetKind(asset.Kind), URI: asset.URI,
			ContentHash: asset.ContentHash, Duration: cloneTime(asset.Duration), Metadata: cloneRawMap(asset.Metadata),
		})
	}
	for _, annotation := range source.Annotations {
		target.Annotations = append(target.Annotations, migrateAnnotation(annotation))
	}
	for _, timeline := range source.Timelines {
		migratedTimeline := Timeline{
			ID: timeline.ID, Name: timeline.Name, Metadata: cloneRawMap(timeline.Metadata),
		}
		for _, annotation := range timeline.Annotations {
			migratedTimeline.Annotations = append(migratedTimeline.Annotations, migrateAnnotation(annotation))
		}
		for trackIndex, track := range timeline.Tracks {
			migratedTrack := Track{
				ID: track.ID, Kind: migrateTrackKind(track.Kind), Name: track.Name,
				Metadata: cloneRawMap(track.Metadata),
			}
			for _, annotation := range track.Annotations {
				migratedTrack.Annotations = append(migratedTrack.Annotations, migrateAnnotation(annotation))
			}
			for clipIndex, clip := range track.Clips {
				migratedClip := Clip{
					ID: clip.ID, AssetID: clip.AssetID, TimelineRange: clip.TimelineRange,
					SourceRange: clip.SourceRange, Disabled: clip.Disabled,
				}
				for _, annotation := range clip.Annotations {
					migratedClip.Annotations = append(migratedClip.Annotations, migrateAnnotation(annotation))
				}
				if len(clip.Properties) != 0 {
					propertyPath := fmt.Sprintf("/timelines/%d/tracks/%d/clips/%d/properties", len(target.Timelines), trackIndex, clipIndex)
					propertySet, propertyDiagnostics := migrateProperties(clip.Properties, propertyPath)
					migrationDiagnostics = append(migrationDiagnostics, propertyDiagnostics...)
					migratedClip.PropertySet = propertySet
				}
				migratedTrack.Clips = append(migratedTrack.Clips, migratedClip)
			}
			migratedTimeline.Tracks = append(migratedTimeline.Tracks, migratedTrack)
		}
		target.Timelines = append(target.Timelines, migratedTimeline)
	}
	if HasErrorDiagnostics(migrationDiagnostics) {
		return Document{}, migrationDiagnostics, nil
	}
	if migratedDiagnostics := Validate(target); HasErrorDiagnostics(migratedDiagnostics) {
		return Document{}, migratedDiagnostics, nil
	}
	return target, nil, nil
}

type legacyPropertyEnvelope struct {
	LegacyKey string          `json:"legacy_key"`
	Value     json.RawMessage `json:"value"`
}

type legacyPointWire struct {
	X json.RawMessage `json:"x"`
	Y json.RawMessage `json:"y"`
}

type legacyRectWire struct {
	X      json.RawMessage `json:"x"`
	Y      json.RawMessage `json:"y"`
	Width  json.RawMessage `json:"width"`
	Height json.RawMessage `json:"height"`
}

type legacyTransformWire struct {
	Position        legacyPointWire `json:"position"`
	Scale           legacyPointWire `json:"scale"`
	RotationDegrees json.RawMessage `json:"rotation_degrees"`
	Anchor          legacyPointWire `json:"anchor"`
}

type legacyCropWire struct {
	Rect legacyRectWire `json:"rect"`
}

func migrateProperties(values map[string]json.RawMessage, path string) (*PropertySet, []Diagnostic) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := &PropertySet{}
	diagnostics := make([]Diagnostic, 0)
	for _, key := range keys {
		propertyPath := path + "/" + escapeJSONPointer(key)
		var err error
		switch key {
		case PropertyOpacityV1:
			var scalar Rational
			scalar, err = decodeLegacyScalar(values[key])
			if err == nil {
				result.Opacity = &OpacityProperty{Value: scalar}
			}
		case PropertyAudioGainV1:
			var scalar Rational
			scalar, err = decodeLegacyScalar(values[key])
			if err == nil {
				result.AudioGain = &AudioGainProperty{Linear: scalar}
			}
		case PropertyAudioPanV1:
			var scalar Rational
			scalar, err = decodeLegacyScalar(values[key])
			if err == nil {
				result.AudioPan = &AudioPanProperty{Value: scalar}
			}
		case PropertyTransformV1:
			var transform TransformProperty
			transform, err = decodeLegacyTransform(values[key])
			if err == nil {
				result.Transform = &transform
			}
		case PropertyCropV1:
			var crop CropProperty
			crop, err = decodeLegacyCrop(values[key])
			if err == nil {
				result.Crop = &crop
			}
		default:
			var extension ExtensionProperty
			extension, err = migrateLegacyExtension(key, values[key])
			if err == nil {
				result.Extensions = append(result.Extensions, extension)
			}
		}
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: CodeMigrationInvalid, Severity: SeverityError, Path: propertyPath,
				Message: fmt.Sprintf("known v1 property %q does not match its strict legacy shape: %v", key, err),
			})
		}
	}
	sort.Slice(result.Extensions, func(i, j int) bool { return result.Extensions[i].Namespace < result.Extensions[j].Namespace })
	return result, diagnostics
}

func migrateLegacyExtension(key string, raw json.RawMessage) (ExtensionProperty, error) {
	if err := wirejson.Validate(raw); err != nil {
		return ExtensionProperty{}, err
	}
	value, err := videoedit.CanonicalJSON(legacyPropertyEnvelope{LegacyKey: key, Value: cloneRaw(raw)})
	if err != nil {
		return ExtensionProperty{}, err
	}
	return ExtensionProperty{
		Namespace:          legacyPropertyNamespace(key),
		AffectsRendering:   true,
		RequiredCapability: LegacyPropertyCapabilityV1,
		Value:              value,
	}, nil
}

func decodeLegacyScalar(raw json.RawMessage) (Rational, error) {
	if err := wirejson.Validate(raw); err != nil {
		return Rational{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var shape any
	if err := decoder.Decode(&shape); err != nil {
		return Rational{}, err
	}
	switch value := shape.(type) {
	case json.Number:
		return rationalFromJSONNumber(value)
	case map[string]any:
		if len(value) != 1 {
			return Rational{}, fmt.Errorf("expected exactly one object member named value")
		}
		scalar, exists := value["value"]
		number, ok := scalar.(json.Number)
		if !exists || !ok {
			return Rational{}, fmt.Errorf("value must be a JSON number")
		}
		return rationalFromJSONNumber(number)
	default:
		return Rational{}, fmt.Errorf("expected a JSON number or {\"value\": number}")
	}
}

func decodeLegacyTransform(raw json.RawMessage) (TransformProperty, error) {
	var wire legacyTransformWire
	if err := wirejson.Decode(raw, &wire); err != nil {
		return TransformProperty{}, err
	}
	position, err := migrateLegacyPoint(wire.Position)
	if err != nil {
		return TransformProperty{}, fmt.Errorf("position: %w", err)
	}
	scale, err := migrateLegacyPoint(wire.Scale)
	if err != nil {
		return TransformProperty{}, fmt.Errorf("scale: %w", err)
	}
	rotation, err := decodeLegacyNumber(wire.RotationDegrees)
	if err != nil {
		return TransformProperty{}, fmt.Errorf("rotation_degrees: %w", err)
	}
	anchor, err := migrateLegacyPoint(wire.Anchor)
	if err != nil {
		return TransformProperty{}, fmt.Errorf("anchor: %w", err)
	}
	return TransformProperty{Position: position, Scale: scale, RotationDegrees: rotation, Anchor: anchor}, nil
}

func decodeLegacyCrop(raw json.RawMessage) (CropProperty, error) {
	var wire legacyCropWire
	if err := wirejson.Decode(raw, &wire); err != nil {
		return CropProperty{}, err
	}
	rect, err := migrateLegacyRect(wire.Rect)
	if err != nil {
		return CropProperty{}, err
	}
	return CropProperty{Rect: rect}, nil
}

func migrateLegacyPoint(value legacyPointWire) (Point, error) {
	x, err := decodeLegacyNumber(value.X)
	if err != nil {
		return Point{}, fmt.Errorf("x: %w", err)
	}
	y, err := decodeLegacyNumber(value.Y)
	if err != nil {
		return Point{}, fmt.Errorf("y: %w", err)
	}
	return Point{X: x, Y: y}, nil
}

func migrateLegacyRect(value legacyRectWire) (Rect, error) {
	x, err := decodeLegacyNumber(value.X)
	if err != nil {
		return Rect{}, fmt.Errorf("x: %w", err)
	}
	y, err := decodeLegacyNumber(value.Y)
	if err != nil {
		return Rect{}, fmt.Errorf("y: %w", err)
	}
	width, err := decodeLegacyNumber(value.Width)
	if err != nil {
		return Rect{}, fmt.Errorf("width: %w", err)
	}
	height, err := decodeLegacyNumber(value.Height)
	if err != nil {
		return Rect{}, fmt.Errorf("height: %w", err)
	}
	return Rect{X: x, Y: y, Width: width, Height: height}, nil
}

func decodeLegacyNumber(raw json.RawMessage) (Rational, error) {
	if err := wirejson.Validate(raw); err != nil {
		return Rational{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Rational{}, err
	}
	number, ok := value.(json.Number)
	if !ok {
		return Rational{}, fmt.Errorf("expected JSON number")
	}
	return rationalFromJSONNumber(number)
}

func rationalFromJSONNumber(value json.Number) (Rational, error) {
	token := value.String()
	if len(token) > 128 {
		return Rational{}, fmt.Errorf("numeric token exceeds the migration limit")
	}
	if exponentIndex := strings.IndexAny(token, "eE"); exponentIndex >= 0 {
		exponent, err := strconv.ParseInt(token[exponentIndex+1:], 10, 16)
		if err != nil || exponent < -64 || exponent > 64 {
			return Rational{}, fmt.Errorf("numeric exponent exceeds the migration limit")
		}
	}
	exact, ok := new(big.Rat).SetString(token)
	if !ok || !exact.Num().IsInt64() || !exact.Denom().IsInt64() {
		return Rational{}, fmt.Errorf("number is not an exact int64 rational")
	}
	result := Rational{Numerator: exact.Num().Int64(), Denominator: exact.Denom().Int64()}
	if err := result.Validate(); err != nil {
		return Rational{}, err
	}
	return result, nil
}

func migrateAssetKind(value videoedit.AssetKind) AssetKind { return AssetKind(value) }
func migrateTrackKind(value videoedit.TrackKind) TrackKind { return TrackKind(value) }

func migrateAnnotation(value videoedit.Annotation) Annotation {
	return Annotation{
		ID: value.ID, Namespace: value.Namespace, Key: value.Key,
		Range: cloneRange(value.Range), Value: cloneRaw(value.Value),
	}
}

func cloneTime(value *videoedit.RationalTime) *RationalTime {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneRange(value *videoedit.TimeRange) *TimeRange {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneRawMap(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return nil
	}
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		result[key] = cloneRaw(value)
	}
	return result
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneDiagnostics(values []videoedit.Diagnostic) []Diagnostic {
	return append([]Diagnostic(nil), values...)
}
