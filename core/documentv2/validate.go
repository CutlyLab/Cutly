package documentv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var capabilityTokenPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9][0-9]*$`)
var languageTagPattern = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)
var audioChannelTokenPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,15}$`)
var reservedSemanticOnce sync.Once
var reservedSemanticTokens map[string]struct{}

type documentValidator struct {
	diagnostics []Diagnostic
	ids         map[ID]string
	assets      map[ID]Asset
	buses       map[ID]AudioBus
}

func Validate(document Document) []Diagnostic {
	validator := &documentValidator{
		ids:    make(map[ID]string),
		assets: make(map[ID]Asset, len(document.Assets)),
		buses:  make(map[ID]AudioBus, len(document.AudioBuses)),
	}
	for _, path := range invalidUTF8Paths(document) {
		validator.add(CodeJSONInvalid, path, "strings and raw JSON must contain valid UTF-8")
	}
	if document.SchemaVersion != SchemaVersion {
		validator.add(CodeSchemaUnsupported, "/schema_version", fmt.Sprintf("schema version %d is not supported; expected %d", document.SchemaVersion, SchemaVersion))
	}
	validator.registerID(document.ID, "/id")
	validator.validateRawMap(document.Metadata, "/metadata")

	for index, asset := range document.Assets {
		path := fmt.Sprintf("/assets/%d", index)
		validator.registerID(asset.ID, path+"/id")
		validator.assets[asset.ID] = asset
		validator.validateAsset(asset, path)
	}
	// Text font references are checked only after the complete asset table is known.
	for index, asset := range document.Assets {
		if asset.Text == nil {
			continue
		}
		font, exists := validator.assets[asset.Text.FontAssetID]
		if !exists || font.Kind != AssetKindFont || !canonicalSHA256(font.ContentHash) {
			validator.add(CodeAssetReferenceInvalid, fmt.Sprintf("/assets/%d/text/font_asset_id", index), "text font_asset_id must reference a content-addressed font asset")
		}
	}

	validator.validateAudioBuses(document.AudioBuses)
	for index, annotation := range document.Annotations {
		validator.validateAnnotation(annotation, fmt.Sprintf("/annotations/%d", index))
	}
	for index, timeline := range document.Timelines {
		validator.validateTimeline(timeline, fmt.Sprintf("/timelines/%d", index), len(document.AudioBuses) != 0)
	}
	return validator.diagnostics
}

func (validator *documentValidator) add(code, path, message string) {
	validator.diagnostics = append(validator.diagnostics, Diagnostic{Code: code, Severity: SeverityError, Path: path, Message: message})
}

func (validator *documentValidator) registerID(id ID, path string) {
	if strings.TrimSpace(string(id)) == "" {
		validator.add(CodeIDRequired, path, "stable ID is required")
		return
	}
	if previous, exists := validator.ids[id]; exists {
		validator.add(CodeDuplicateID, path, fmt.Sprintf("ID %q is already used at %s", id, previous))
		return
	}
	validator.ids[id] = path
}

func (validator *documentValidator) validateAsset(asset Asset, path string) {
	if asset.Duration != nil {
		if err := asset.Duration.Validate(); err != nil || asset.Duration.Value < 0 {
			validator.add(CodeAssetInvalid, path+"/duration", "asset duration must be a valid non-negative rational time")
		}
	}
	if asset.ContentHash != "" && !canonicalSHA256(asset.ContentHash) {
		validator.add(CodeAssetInvalid, path+"/content_hash", "content_hash must be a canonical lowercase sha256 digest")
	}
	validator.validateRawMap(asset.Metadata, path+"/metadata")
	switch asset.Kind {
	case AssetKindText:
		if asset.Text == nil || asset.Vector != nil || asset.URI != "" || asset.ContentHash != "" {
			validator.add(CodeAssetInvalid, path, "text asset requires only an inline text expression")
		} else {
			validator.validateText(*asset.Text, path+"/text")
		}
	case AssetKindVector:
		if asset.Vector == nil || asset.Text != nil || asset.URI != "" || asset.ContentHash != "" {
			validator.add(CodeAssetInvalid, path, "vector asset requires only an inline deterministic vector expression")
		} else {
			validator.validateVector(*asset.Vector, path+"/vector")
		}
	case AssetKindVideo, AssetKindAudio, AssetKindImage, AssetKindFont, AssetKindData:
		if asset.Text != nil || asset.Vector != nil {
			validator.add(CodeAssetInvalid, path, "non-inline asset must not contain text or vector expressions")
		}
		if strings.TrimSpace(asset.URI) == "" && strings.TrimSpace(asset.ContentHash) == "" {
			validator.add(CodeAssetInvalid, path, "asset requires uri or content_hash")
		}
		if asset.Kind == AssetKindFont && !canonicalSHA256(asset.ContentHash) {
			validator.add(CodeAssetInvalid, path+"/content_hash", "font assets must be pinned by canonical content_hash")
		}
	default:
		validator.add(CodeAssetInvalid, path+"/kind", fmt.Sprintf("unsupported asset kind %q", asset.Kind))
	}
}

func (validator *documentValidator) validateText(value TextSource, path string) {
	validUTF8 := utf8.ValidString(value.Text)
	textV2 := value.Profile == TextProfilePinnedFontViewportPXV2
	if !validUTF8 {
		// invalidUTF8Paths also reports the complete document path before semantic validation. Keep
		// this explicit guard so Text validation never ranges over encoding/json's replacement form.
		validator.add(CodeAssetInvalid, path+"/text", "text must contain valid UTF-8")
	}
	textV2TooLarge := textV2 && len(value.Text) > TextMaxUTF8Bytes
	if textV2TooLarge {
		validator.add(CodeAssetInvalid, path+"/text", fmt.Sprintf("text UTF-8 length must not exceed %d bytes", TextMaxUTF8Bytes))
	} else if validUTF8 {
		lineCount := 1
		lineScalars := 0
		lineLimitReported := false
		lineCountReported := false
		controlReported := false
		formatReported := false
		checkScript := textV2 && textScriptSupportedV1(value.Script)
		scriptReported := false
		for _, character := range value.Text {
			if character == '\n' {
				lineCount++
				lineScalars = 0
				if textV2 && lineCount > TextMaxLines && !lineCountReported {
					validator.add(CodeAssetInvalid, path+"/text", fmt.Sprintf("text must not exceed %d lines", TextMaxLines))
					lineCountReported = true
				}
				continue
			}
			lineScalars++
			if textV2 && lineScalars > TextMaxScalarsPerLine && !lineLimitReported {
				validator.add(CodeAssetInvalid, path+"/text", fmt.Sprintf("each text line must not exceed %d Unicode scalars", TextMaxScalarsPerLine))
				lineLimitReported = true
			}
			if (textRuneIsFrozenControlV1(character) || character == '\u2028' || character == '\u2029') && !controlReported {
				validator.add(CodeAssetInvalid, path+"/text", "plain text permits U+000A but no other control or line-separator characters")
				controlReported = true
			}
			if textV2 &&
				(textRuneIsFrozenFormatV1(character) || textRuneIsFrozenVariationSelectorV1(character) || textRuneIsFrozenDefaultIgnorableV1(character)) && !formatReported {
				validator.add(CodeAssetInvalid, path+"/text", "text v2 does not permit Unicode format, variation-selector, or default-ignorable characters")
				formatReported = true
			}
			if checkScript && !textRuneMatchesFrozenScript(character, value.Script) && !scriptReported {
				validator.add(CodeAssetInvalid, path+"/text", fmt.Sprintf("text contains a scalar outside declared script %q", value.Script))
				scriptReported = true
			}
		}
	}
	if strings.TrimSpace(string(value.FontAssetID)) == "" {
		validator.add(CodeAssetInvalid, path+"/font_asset_id", "font_asset_id is required")
	}
	if validator.validateRationalCode(value.FontSize, path+"/font_size", CodeAssetInvalid) && rationalCompare(value.FontSize, 0, 1) <= 0 {
		validator.add(CodeAssetInvalid, path+"/font_size", "font_size must be positive")
	}
	if !languageTagPattern.MatchString(value.Language) {
		validator.add(CodeAssetInvalid, path+"/language", "language must be an explicit BCP-47 style tag")
	}
	if value.Direction != TextDirectionLTR && value.Direction != TextDirectionRTL {
		validator.add(CodeAssetInvalid, path+"/direction", "direction must be ltr or rtl")
	}
	if value.Alignment != TextAlignmentStart && value.Alignment != TextAlignmentCenter && value.Alignment != TextAlignmentEnd {
		validator.add(CodeAssetInvalid, path+"/alignment", "alignment must be start, center, or end")
	}
	switch value.Profile {
	case "":
		if value.LineHeight != nil {
			validator.add(CodeAssetInvalid, path+"/line_height", "line_height must be omitted for legacy cutly.text.v1")
		}
		if value.Viewport != nil {
			validator.add(CodeAssetInvalid, path+"/viewport", "viewport must be omitted for legacy cutly.text.v1")
		}
		if value.Script != "" {
			validator.add(CodeAssetInvalid, path+"/script", "script must be omitted for legacy cutly.text.v1")
		}
	case TextProfilePinnedFontViewportPXV2:
		if value.LineHeight == nil {
			validator.add(CodeAssetInvalid, path+"/line_height", fmt.Sprintf("line_height is required for text profile %q", TextProfilePinnedFontViewportPXV2))
		} else if validator.validateRationalCode(*value.LineHeight, path+"/line_height", CodeAssetInvalid) && rationalCompare(*value.LineHeight, 0, 1) <= 0 {
			validator.add(CodeAssetInvalid, path+"/line_height", "line_height must be positive")
		}
		if value.Viewport == nil {
			validator.add(CodeAssetInvalid, path+"/viewport", fmt.Sprintf("viewport is required for text profile %q", TextProfilePinnedFontViewportPXV2))
		} else {
			validator.validateRasterViewport(*value.Viewport, path+"/viewport")
		}
		if !textScriptSupportedV1(value.Script) {
			validator.add(CodeAssetInvalid, path+"/script", fmt.Sprintf("script must be %q, %q, or %q", TextScriptLatin, TextScriptHan, TextScriptArabic))
		}
	default:
		validator.add(CodeAssetInvalid, path+"/profile", fmt.Sprintf("text profile must be omitted for legacy %q or equal %q", CapabilityTextV1, TextProfilePinnedFontViewportPXV2))
	}
}

func (validator *documentValidator) validateRasterViewport(value RasterViewport, path string) {
	if value.WidthPixels == 0 || value.WidthPixels > RasterViewportMaxDimension {
		validator.add(CodeAssetInvalid, path+"/width_pixels", fmt.Sprintf("width_pixels must be in [1,%d]", RasterViewportMaxDimension))
	}
	if value.HeightPixels == 0 || value.HeightPixels > RasterViewportMaxDimension {
		validator.add(CodeAssetInvalid, path+"/height_pixels", fmt.Sprintf("height_pixels must be in [1,%d]", RasterViewportMaxDimension))
	}
}

func (validator *documentValidator) validateVector(value VectorSource, path string) {
	switch value.Profile {
	case VectorProfilePathV1:
		if value.Viewport != nil {
			validator.add(CodeAssetInvalid, path+"/viewport", fmt.Sprintf("viewport must be omitted for vector profile %q", VectorProfilePathV1))
		}
	case VectorProfilePathViewportPXV2:
		if value.Viewport == nil {
			validator.add(CodeAssetInvalid, path+"/viewport", fmt.Sprintf("viewport is required for vector profile %q", VectorProfilePathViewportPXV2))
		}
	default:
		validator.add(CodeAssetInvalid, path+"/profile", fmt.Sprintf("vector profile must be %q or %q", VectorProfilePathV1, VectorProfilePathViewportPXV2))
	}
	if value.Viewport != nil {
		validator.validateRasterViewport(*value.Viewport, path+"/viewport")
	}
	validator.validateRectCode(value.ViewBox, path+"/view_box", CodeAssetInvalid)
	if value.ViewBox.Width.Validate() == nil && value.ViewBox.Height.Validate() == nil &&
		(rationalCompare(value.ViewBox.Width, 0, 1) <= 0 || rationalCompare(value.ViewBox.Height, 0, 1) <= 0) {
		validator.add(CodeAssetInvalid, path+"/view_box", "view_box width and height must be positive")
	}
	if len(value.Paths) == 0 {
		validator.add(CodeAssetInvalid, path+"/paths", "vector expression requires at least one path")
	}
	for index, vectorPath := range value.Paths {
		itemPath := fmt.Sprintf("%s/paths/%d", path, index)
		validator.registerID(vectorPath.ID, itemPath+"/id")
		if len(vectorPath.Commands) == 0 {
			validator.add(CodeAssetInvalid, itemPath+"/commands", "vector path requires commands")
		}
		if vectorPath.Fill == nil && vectorPath.Stroke == nil {
			validator.add(CodeAssetInvalid, itemPath, "vector path requires fill or stroke")
		}
		if value.Profile == VectorProfilePathViewportPXV2 {
			if vectorPath.Fill == nil {
				validator.add(CodeAssetInvalid, itemPath+"/fill", "viewport-px vector paths require an explicit fill")
			}
			if vectorPath.Stroke != nil {
				validator.add(CodeAssetInvalid, itemPath+"/stroke", "viewport-px vector paths do not support strokes")
			}
		}
		if vectorPath.FillRule != VectorFillNonZero && vectorPath.FillRule != VectorFillEvenOdd {
			validator.add(CodeAssetInvalid, itemPath+"/fill_rule", "fill_rule must be nonzero or evenodd")
		}
		if vectorPath.Fill == nil && vectorPath.FillRule != VectorFillNonZero {
			validator.add(CodeAssetInvalid, itemPath+"/fill_rule", "stroke-only paths must use canonical nonzero fill_rule")
		}
		if vectorPath.Stroke != nil {
			if validator.validateRationalCode(vectorPath.Stroke.Width, itemPath+"/stroke/width", CodeAssetInvalid) && rationalCompare(vectorPath.Stroke.Width, 0, 1) <= 0 {
				validator.add(CodeAssetInvalid, itemPath+"/stroke/width", "stroke width must be positive")
			}
			if vectorPath.Stroke.LineCap != VectorCapButt && vectorPath.Stroke.LineCap != VectorCapRound && vectorPath.Stroke.LineCap != VectorCapSquare {
				validator.add(CodeAssetInvalid, itemPath+"/stroke/line_cap", "line_cap must be butt, round, or square")
			}
			if vectorPath.Stroke.LineJoin != VectorJoinMiter && vectorPath.Stroke.LineJoin != VectorJoinRound && vectorPath.Stroke.LineJoin != VectorJoinBevel {
				validator.add(CodeAssetInvalid, itemPath+"/stroke/line_join", "line_join must be miter, round, or bevel")
			}
			if validator.validateRationalCode(vectorPath.Stroke.MiterLimit, itemPath+"/stroke/miter_limit", CodeAssetInvalid) && rationalCompare(vectorPath.Stroke.MiterLimit, 0, 1) <= 0 {
				validator.add(CodeAssetInvalid, itemPath+"/stroke/miter_limit", "miter_limit must be positive")
			}
			if vectorPath.Stroke.LineJoin != VectorJoinMiter && vectorPath.Stroke.MiterLimit != (Rational{Numerator: 4, Denominator: 1}) {
				validator.add(CodeAssetInvalid, itemPath+"/stroke/miter_limit", "non-miter joins must use canonical miter_limit 4/1")
			}
		}
		subpathOpen := false
		for commandIndex, command := range vectorPath.Commands {
			commandPath := fmt.Sprintf("%s/commands/%d", itemPath, commandIndex)
			want := -1
			switch command.Op {
			case VectorMoveTo, VectorLineTo:
				want = 1
				if command.Op == VectorMoveTo {
					subpathOpen = true
				} else if !subpathOpen {
					validator.add(CodeAssetInvalid, commandPath+"/op", "line_to requires an open subpath beginning with move_to")
				}
			case VectorCubicTo:
				want = 3
				if value.Profile == VectorProfilePathViewportPXV2 {
					validator.add(CodeAssetInvalid, commandPath+"/op", "viewport-px vector paths do not support cubic_to")
				}
				if !subpathOpen {
					validator.add(CodeAssetInvalid, commandPath+"/op", "cubic_to requires an open subpath beginning with move_to")
				}
			case VectorClose:
				want = 0
				if command.Points != nil {
					validator.add(CodeAssetInvalid, commandPath+"/points", "close must omit its payload")
				}
				if !subpathOpen {
					validator.add(CodeAssetInvalid, commandPath+"/op", "close requires an open subpath")
				}
				subpathOpen = false
			default:
				validator.add(CodeAssetInvalid, commandPath+"/op", fmt.Sprintf("unsupported vector command %q", command.Op))
			}
			if want >= 0 && len(command.Points) != want {
				validator.add(CodeAssetInvalid, commandPath+"/points", fmt.Sprintf("command %q requires %d points", command.Op, want))
			}
			for pointIndex, point := range command.Points {
				validator.validatePointCode(point, fmt.Sprintf("%s/points/%d", commandPath, pointIndex), CodeAssetInvalid)
			}
		}
	}
}

func (validator *documentValidator) validateAudioBuses(values []AudioBus) {
	masters := 0
	for index, bus := range values {
		path := fmt.Sprintf("/audio_buses/%d", index)
		validator.registerID(bus.ID, path+"/id")
		validator.buses[bus.ID] = bus
		gainValid := validator.validateRationalCode(bus.Properties.GainLinear, path+"/properties/gain_linear", CodeAudioBusInvalid)
		panValid := validator.validateRationalCode(bus.Properties.Pan, path+"/properties/pan", CodeAudioBusInvalid)
		if gainValid && rationalCompare(bus.Properties.GainLinear, 0, 1) < 0 {
			validator.add(CodeAudioBusInvalid, path+"/properties/gain_linear", "bus gain must be non-negative")
		}
		if panValid && (rationalCompare(bus.Properties.Pan, -1, 1) < 0 || rationalCompare(bus.Properties.Pan, 1, 1) > 0) {
			validator.add(CodeAudioBusInvalid, path+"/properties/pan", "bus pan must be within [-1,1]")
		}
		if limiter := bus.Properties.SamplePeakLimiter; limiter != nil {
			limiterPath := path + "/properties/sample_peak_limiter/ceiling_linear"
			ceilingValid := validator.validateRationalCode(limiter.CeilingLinear, limiterPath, CodeAudioBusInvalid)
			if bus.Kind != AudioBusMaster {
				validator.add(CodeAudioBusInvalid, path+"/properties/sample_peak_limiter", "sample-peak limiter is valid only on the master bus")
			}
			if ceilingValid && (rationalCompare(limiter.CeilingLinear, 0, 1) <= 0 || rationalCompare(limiter.CeilingLinear, 1, 1) > 0) {
				validator.add(CodeAudioBusInvalid, limiterPath, "sample-peak limiter ceiling must be within (0,1]")
			}
		}
		switch bus.Kind {
		case AudioBusMaster:
			masters++
			if bus.ParentBusID != "" {
				validator.add(CodeAudioBusInvalid, path+"/parent_bus_id", "master bus must not have a parent")
			}
		case AudioBusGroup:
			if bus.ParentBusID == "" {
				validator.add(CodeAudioBusInvalid, path+"/parent_bus_id", "group bus requires exactly one parent")
			}
		default:
			validator.add(CodeAudioBusInvalid, path+"/kind", fmt.Sprintf("unsupported bus kind %q", bus.Kind))
		}
	}
	if len(values) == 0 {
		return // Frozen legacy direct-master graph.
	}
	if masters != 1 {
		validator.add(CodeAudioBusInvalid, "/audio_buses", fmt.Sprintf("audio graph requires exactly one master bus, got %d", masters))
	}
	for index, bus := range values {
		if bus.Kind != AudioBusGroup {
			continue
		}
		path := fmt.Sprintf("/audio_buses/%d/parent_bus_id", index)
		if _, exists := validator.buses[bus.ParentBusID]; !exists {
			validator.add(CodeAudioBusInvalid, path, fmt.Sprintf("parent bus %q does not exist", bus.ParentBusID))
			continue
		}
		seen := make(map[ID]struct{})
		current := bus
		for current.Kind != AudioBusMaster {
			if _, exists := seen[current.ID]; exists {
				validator.add(CodeAudioBusCycle, path, "audio bus graph contains a cycle")
				break
			}
			seen[current.ID] = struct{}{}
			parent, exists := validator.buses[current.ParentBusID]
			if !exists {
				break
			}
			current = parent
		}
	}
}

type clipLocation struct {
	clip       Clip
	track      Track
	trackIndex int
	clipIndex  int
	path       string
}

func (validator *documentValidator) validateTimeline(timeline Timeline, path string, explicitAudioGraph bool) {
	validator.registerID(timeline.ID, path+"/id")
	validator.validateRawMap(timeline.Metadata, path+"/metadata")
	for index, annotation := range timeline.Annotations {
		validator.validateAnnotation(annotation, fmt.Sprintf("%s/annotations/%d", path, index))
	}
	clips := make(map[ID]clipLocation)
	for trackIndex, track := range timeline.Tracks {
		trackPath := fmt.Sprintf("%s/tracks/%d", path, trackIndex)
		validator.registerID(track.ID, trackPath+"/id")
		validator.validateRawMap(track.Metadata, trackPath+"/metadata")
		if !validTrackKind(track.Kind) {
			validator.add(CodeTrackInvalid, trackPath+"/kind", fmt.Sprintf("unsupported track kind %q", track.Kind))
		}
		if track.Kind == TrackKindAudio {
			if explicitAudioGraph {
				if _, exists := validator.buses[track.AudioBusID]; !exists {
					validator.add(CodeAudioBusInvalid, trackPath+"/audio_bus_id", "audio track must route to an existing bus")
				}
			} else if track.AudioBusID != "" {
				validator.add(CodeAudioBusInvalid, trackPath+"/audio_bus_id", "legacy direct-master graph forbids an explicit bus ID")
			}
		} else if track.AudioBusID != "" {
			validator.add(CodeAudioBusInvalid, trackPath+"/audio_bus_id", "only audio tracks may route to an audio bus")
		}
		if track.Kind == TrackKindAnnotation && len(track.Clips) != 0 {
			validator.add(CodeTrackInvalid, trackPath+"/clips", "annotation tracks must not contain render clips")
		}
		for index, annotation := range track.Annotations {
			validator.validateAnnotation(annotation, fmt.Sprintf("%s/annotations/%d", trackPath, index))
		}

		var previousStart *RationalTime
		var activeEnd *RationalTime
		for clipIndex, clip := range track.Clips {
			clipPath := fmt.Sprintf("%s/clips/%d", trackPath, clipIndex)
			validator.registerID(clip.ID, clipPath+"/id")
			validator.validateClip(clip, track.Kind, clipPath)
			if clip.TimelineRange.Validate() == nil {
				if previousStart != nil {
					if comparison, err := clip.TimelineRange.Start.Compare(*previousStart); err == nil && comparison < 0 {
						validator.add(CodeClipInvalid, clipPath+"/timeline_range/start", "clips must be sorted by timeline start")
					}
				}
				if activeEnd != nil {
					if comparison, err := clip.TimelineRange.Start.Compare(*activeEnd); err == nil && comparison < 0 {
						validator.add(CodeClipOverlap, clipPath+"/timeline_range", "clip overlaps an earlier clip on the same track")
					}
				}
				start := clip.TimelineRange.Start
				previousStart = &start
				if end, err := clip.TimelineRange.End(); err == nil {
					if activeEnd == nil {
						copy := end
						activeEnd = &copy
					} else if comparison, compareErr := end.Compare(*activeEnd); compareErr == nil && comparison > 0 {
						copy := end
						activeEnd = &copy
					}
				}
			}
			clips[clip.ID] = clipLocation{clip: clip, track: track, trackIndex: trackIndex, clipIndex: clipIndex, path: clipPath}
		}
	}
	validator.validateTransitions(timeline, path, clips)
}

func (validator *documentValidator) validateClip(clip Clip, trackKind TrackKind, path string) {
	asset, exists := validator.assets[clip.AssetID]
	if !exists {
		validator.add(CodeClipInvalid, path+"/asset_id", fmt.Sprintf("asset %q does not exist", clip.AssetID))
	} else if !trackAcceptsAsset(trackKind, asset.Kind) {
		validator.add(CodeClipInvalid, path+"/asset_id", fmt.Sprintf("track kind %q cannot contain asset kind %q", trackKind, asset.Kind))
	}
	if err := clip.TimelineRange.Validate(); err != nil || clip.TimelineRange.Duration.Value <= 0 {
		validator.add(CodeClipInvalid, path+"/timeline_range", "timeline range must be valid and positive")
	}
	if err := clip.SourceRange.Validate(); err != nil || clip.SourceRange.Duration.Value <= 0 {
		validator.add(CodeClipInvalid, path+"/source_range", "source range must be valid and positive")
	}
	if exists && asset.Duration != nil && temporalAssetKind(asset.Kind) {
		if end, err := clip.SourceRange.End(); err == nil {
			if comparison, err := end.Compare(*asset.Duration); err == nil && comparison > 0 {
				validator.add(CodeClipInvalid, path+"/source_range", "source range exceeds asset duration")
			}
		}
	}
	if clip.PropertySet != nil {
		validator.validatePropertySet(*clip.PropertySet, trackKind, path+"/property_set")
	}
	targets := make(map[KeyframeTarget]struct{})
	for index, lane := range clip.KeyframeLanes {
		lanePath := fmt.Sprintf("%s/keyframe_lanes/%d", path, index)
		validator.registerID(lane.ID, lanePath+"/id")
		if _, duplicate := targets[lane.Target]; duplicate {
			validator.add(CodeKeyframeInvalid, lanePath+"/target", fmt.Sprintf("target %q already has a lane on this clip", lane.Target))
		}
		targets[lane.Target] = struct{}{}
		validator.validateKeyframeLane(lane, clip, asset, trackKind, lanePath)
	}
	for index, annotation := range clip.Annotations {
		validator.validateAnnotation(annotation, fmt.Sprintf("%s/annotations/%d", path, index))
	}
}

func (validator *documentValidator) validatePropertySet(value PropertySet, trackKind TrackKind, path string) {
	if value.Extensions != nil && len(value.Extensions) == 0 {
		validator.add(CodePropertyInvalid, path+"/extensions", "empty extensions must be omitted")
	}
	if value.Transform == nil && value.Opacity == nil && value.Crop == nil && value.AudioGain == nil && value.AudioPan == nil && value.AudioChannelMatrix == nil && len(value.Extensions) == 0 {
		validator.add(CodePropertyInvalid, path, "empty property_set must be omitted")
	}
	visual := trackKind == TrackKindVideo || trackKind == TrackKindText || trackKind == TrackKindVector
	if value.Transform != nil {
		if !visual {
			validator.add(CodePropertyInvalid, path+"/transform", "transform is valid only on visual tracks")
		}
		validator.validatePoint(value.Transform.Position, path+"/transform/position")
		validator.validatePoint(value.Transform.Scale, path+"/transform/scale")
		validator.validateRational(value.Transform.RotationDegrees, path+"/transform/rotation_degrees")
		validator.validatePoint(value.Transform.Anchor, path+"/transform/anchor")
		validator.validateUnitInterval(value.Transform.Anchor.X, path+"/transform/anchor/x")
		validator.validateUnitInterval(value.Transform.Anchor.Y, path+"/transform/anchor/y")
	}
	if value.Opacity != nil {
		if !visual {
			validator.add(CodePropertyInvalid, path+"/opacity", "opacity is valid only on visual tracks")
		}
		validator.validateRational(value.Opacity.Value, path+"/opacity/value")
		validator.validateUnitInterval(value.Opacity.Value, path+"/opacity/value")
	}
	if value.Crop != nil {
		if !visual {
			validator.add(CodePropertyInvalid, path+"/crop", "crop is valid only on visual tracks")
		}
		validator.validateRect(value.Crop.Rect, path+"/crop/rect")
		for _, item := range []struct {
			name string
			v    Rational
		}{{"x", value.Crop.Rect.X}, {"y", value.Crop.Rect.Y}, {"width", value.Crop.Rect.Width}, {"height", value.Crop.Rect.Height}} {
			validator.validateUnitInterval(item.v, path+"/crop/rect/"+item.name)
		}
		if rectRationalsValid(value.Crop.Rect) && (rationalCompare(value.Crop.Rect.Width, 0, 1) <= 0 ||
			rationalCompare(value.Crop.Rect.Height, 0, 1) <= 0 ||
			rationalAdd(value.Crop.Rect.X, value.Crop.Rect.Width).Cmp(big.NewRat(1, 1)) > 0 ||
			rationalAdd(value.Crop.Rect.Y, value.Crop.Rect.Height).Cmp(big.NewRat(1, 1)) > 0) {
			validator.add(CodePropertyInvalid, path+"/crop/rect", "crop must be a positive rectangle inside [0,1]²")
		}
	}
	if value.AudioGain != nil {
		if trackKind != TrackKindAudio {
			validator.add(CodePropertyInvalid, path+"/audio_gain", "audio gain is valid only on audio tracks")
		}
		if validator.validateRational(value.AudioGain.Linear, path+"/audio_gain/linear") && rationalCompare(value.AudioGain.Linear, 0, 1) < 0 {
			validator.add(CodePropertyInvalid, path+"/audio_gain/linear", "audio gain must be non-negative")
		}
	}
	if value.AudioPan != nil {
		if trackKind != TrackKindAudio {
			validator.add(CodePropertyInvalid, path+"/audio_pan", "audio pan is valid only on audio tracks")
		}
		if validator.validateRational(value.AudioPan.Value, path+"/audio_pan/value") &&
			(rationalCompare(value.AudioPan.Value, -1, 1) < 0 || rationalCompare(value.AudioPan.Value, 1, 1) > 0) {
			validator.add(CodePropertyInvalid, path+"/audio_pan/value", "audio pan must be within [-1,1]")
		}
	}
	if value.AudioChannelMatrix != nil {
		if trackKind != TrackKindAudio {
			validator.add(CodePropertyInvalid, path+"/audio_channel_matrix", "audio channel matrix is valid only on audio tracks")
		}
		validator.validateAudioChannelMatrix(*value.AudioChannelMatrix, path+"/audio_channel_matrix")
	}
	previous := ""
	for index, extension := range value.Extensions {
		extensionPath := fmt.Sprintf("%s/extensions/%d", path, index)
		if !capabilityTokenPattern.MatchString(extension.Namespace) || (previous != "" && extension.Namespace <= previous) {
			validator.add(CodePropertyInvalid, extensionPath+"/namespace", "extension namespaces must be canonical, unique, and sorted")
		}
		if reservedSemanticToken(extension.Namespace) {
			validator.add(CodePropertyInvalid, extensionPath+"/namespace", "extension namespace collides with a first-class property")
		}
		previous = extension.Namespace
		if extension.AffectsRendering {
			if !capabilityTokenPattern.MatchString(extension.RequiredCapability) {
				validator.add(CodePropertyInvalid, extensionPath+"/required_capability", "rendering extension requires a canonical versioned capability")
			} else if reservedSemanticToken(extension.RequiredCapability) {
				validator.add(CodePropertyInvalid, extensionPath+"/required_capability", "extension capability collides with a first-class semantic capability")
			}
		} else if extension.RequiredCapability != "" {
			validator.add(CodePropertyInvalid, extensionPath+"/required_capability", "non-rendering extension must not claim a render capability")
		}
		validator.validateLegacyPropertyExtension(extension, extensionPath)
		if len(extension.Value) == 0 || wirejson.Validate(extension.Value) != nil || bytes.Equal(bytes.TrimSpace(extension.Value), []byte("null")) {
			validator.add(CodeJSONInvalid, extensionPath+"/value", "extension value must be non-null valid JSON")
		}
	}
}

func (validator *documentValidator) validateAudioChannelMatrix(value AudioChannelMatrixProperty, path string) {
	if value.SourceLayout == "" || value.SourceLayout != strings.TrimSpace(value.SourceLayout) {
		validator.add(CodePropertyInvalid, path+"/source_layout", "source_layout must be a non-empty exact ProbeManifest layout token without surrounding whitespace")
	}
	count := len(value.SourceChannels)
	if count == 0 || count > AudioChannelMatrixMaxInputs {
		validator.add(CodePropertyInvalid, path+"/source_channels", fmt.Sprintf("source_channels must contain 1..%d ordered channels", AudioChannelMatrixMaxInputs))
	}
	if len(value.LeftCoefficients) != count {
		validator.add(CodePropertyInvalid, path+"/left_coefficients", "left_coefficients length must equal source_channels length")
	}
	if len(value.RightCoefficients) != count {
		validator.add(CodePropertyInvalid, path+"/right_coefficients", "right_coefficients length must equal source_channels length")
	}
	seen := make(map[string]struct{}, count)
	for index, channel := range value.SourceChannels {
		channelPath := fmt.Sprintf("%s/source_channels/%d", path, index)
		if !audioChannelTokenPattern.MatchString(channel) {
			validator.add(CodePropertyInvalid, channelPath, "source channel must be a canonical uppercase channel token")
		}
		if _, duplicate := seen[channel]; duplicate {
			validator.add(CodePropertyInvalid, channelPath, fmt.Sprintf("source channel %q is duplicated", channel))
		}
		seen[channel] = struct{}{}
	}
	for index, coefficient := range value.LeftCoefficients {
		validator.validateRationalCode(coefficient, fmt.Sprintf("%s/left_coefficients/%d", path, index), CodePropertyInvalid)
	}
	for index, coefficient := range value.RightCoefficients {
		validator.validateRationalCode(coefficient, fmt.Sprintf("%s/right_coefficients/%d", path, index), CodePropertyInvalid)
	}
}

func (validator *documentValidator) validateKeyframeLane(lane KeyframeLane, clip Clip, asset Asset, trackKind TrackKind, path string) {
	wantType, known := keyframeTargetType(lane.Target)
	if !known || lane.ValueType != wantType {
		validator.add(CodeKeyframeInvalid, path, fmt.Sprintf("target %q requires value_type %q", lane.Target, wantType))
	}
	visualTarget := lane.Target == KeyframeTargetTransformPosition || lane.Target == KeyframeTargetTransformScale ||
		lane.Target == KeyframeTargetTransformRotation || lane.Target == KeyframeTargetTransformAnchor ||
		lane.Target == KeyframeTargetOpacity || lane.Target == KeyframeTargetCrop
	visualTrack := trackKind == TrackKindVideo || trackKind == TrackKindText || trackKind == TrackKindVector
	if visualTarget && !visualTrack {
		validator.add(CodeKeyframeInvalid, path+"/target", "visual keyframes require a video, text, or vector track")
	}
	if lane.Target == KeyframeTargetTextFill && (trackKind != TrackKindText || asset.Kind != AssetKindText) {
		validator.add(CodeKeyframeInvalid, path+"/target", "text.fill requires a text asset")
	}
	if (lane.Target == KeyframeTargetAudioGain || lane.Target == KeyframeTargetAudioPan) && trackKind != TrackKindAudio {
		validator.add(CodeKeyframeInvalid, path+"/target", "audio keyframes require an audio track")
	}
	if len(lane.Keyframes) == 0 {
		validator.add(CodeKeyframeInvalid, path+"/keyframes", "keyframe lane must not be empty")
		return
	}
	var previous *RationalTime
	for index, keyframe := range lane.Keyframes {
		itemPath := fmt.Sprintf("%s/keyframes/%d", path, index)
		if err := keyframe.Offset.Validate(); err != nil || keyframe.Offset.Value < 0 {
			validator.add(CodeKeyframeInvalid, itemPath+"/offset", "keyframe offset must be a valid non-negative time")
		} else {
			if comparison, err := keyframe.Offset.Compare(clip.TimelineRange.Duration); err == nil && comparison > 0 {
				validator.add(CodeKeyframeInvalid, itemPath+"/offset", "keyframe offset must be within [0, clip.duration]")
			}
			if previous != nil {
				if comparison, err := keyframe.Offset.Compare(*previous); err == nil && comparison <= 0 {
					validator.add(CodeKeyframeInvalid, itemPath+"/offset", "keyframe offsets must be strictly increasing")
				}
			}
			copy := keyframe.Offset
			previous = &copy
		}
		validator.validateKeyframeValue(keyframe.Value, lane.ValueType, lane.Target, itemPath+"/value")
		last := index == len(lane.Keyframes)-1
		if last {
			if keyframe.Interpolation != "" || keyframe.Easing != nil {
				validator.add(CodeKeyframeInvalid, itemPath, "last keyframe is an interpolation boundary and must omit interpolation/easing")
			}
			continue
		}
		if lane.ValueType == KeyframeValueColor && keyframe.Interpolation != InterpolationHold {
			validator.add(CodeKeyframeInvalid, itemPath+"/interpolation", "color values support hold interpolation only in Document v2")
			continue
		}
		switch keyframe.Interpolation {
		case InterpolationHold, InterpolationLinear:
			if keyframe.Easing != nil {
				validator.add(CodeKeyframeInvalid, itemPath+"/easing", "hold/linear interpolation must not carry easing")
			}
		case InterpolationCubic:
			if keyframe.Easing == nil {
				validator.add(CodeKeyframeInvalid, itemPath+"/easing", "cubic_bezier interpolation requires easing")
			} else {
				validator.validateBezier(*keyframe.Easing, itemPath+"/easing")
			}
		default:
			validator.add(CodeKeyframeInvalid, itemPath+"/interpolation", "non-final keyframe requires hold, linear, or cubic_bezier interpolation")
		}
	}
}

func (validator *documentValidator) validateKeyframeValue(value KeyframeValue, kind KeyframeValueType, target KeyframeTarget, path string) {
	count := 0
	if value.Scalar != nil {
		count++
		validator.validateRationalCode(*value.Scalar, path+"/scalar", CodeKeyframeInvalid)
	}
	if value.Point != nil {
		count++
		validator.validatePointCode(*value.Point, path+"/point", CodeKeyframeInvalid)
	}
	if value.Rect != nil {
		count++
		validator.validateRectCode(*value.Rect, path+"/rect", CodeKeyframeInvalid)
	}
	if value.Color != nil {
		count++
	}
	if count != 1 || (kind == KeyframeValueScalar && value.Scalar == nil) || (kind == KeyframeValuePoint && value.Point == nil) ||
		(kind == KeyframeValueRect && value.Rect == nil) || (kind == KeyframeValueColor && value.Color == nil) {
		validator.add(CodeKeyframeInvalid, path, fmt.Sprintf("keyframe value must contain exactly one %s member", kind))
		return
	}
	switch target {
	case KeyframeTargetTransformAnchor:
		if value.Point != nil {
			validator.validateUnitIntervalCode(value.Point.X, path+"/point/x", CodeKeyframeInvalid)
			validator.validateUnitIntervalCode(value.Point.Y, path+"/point/y", CodeKeyframeInvalid)
		}
	case KeyframeTargetOpacity:
		if value.Scalar != nil {
			validator.validateUnitIntervalCode(*value.Scalar, path+"/scalar", CodeKeyframeInvalid)
		}
	case KeyframeTargetCrop:
		if value.Rect != nil {
			validator.validateCropRect(*value.Rect, path+"/rect", CodeKeyframeInvalid)
		}
	case KeyframeTargetAudioGain:
		if value.Scalar != nil && value.Scalar.Validate() == nil && rationalCompare(*value.Scalar, 0, 1) < 0 {
			validator.add(CodeKeyframeInvalid, path+"/scalar", "audio gain must be non-negative")
		}
	case KeyframeTargetAudioPan:
		if value.Scalar != nil && value.Scalar.Validate() == nil &&
			(rationalCompare(*value.Scalar, -1, 1) < 0 || rationalCompare(*value.Scalar, 1, 1) > 0) {
			validator.add(CodeKeyframeInvalid, path+"/scalar", "audio pan must be within [-1,1]")
		}
	}
}

func (validator *documentValidator) validateBezier(value CubicBezier, path string) {
	allValid := true
	for _, item := range []struct {
		name string
		v    Rational
	}{{"x1", value.X1}, {"y1", value.Y1}, {"x2", value.X2}, {"y2", value.Y2}} {
		if !validator.validateRationalCode(item.v, path+"/"+item.name, CodeKeyframeInvalid) {
			allValid = false
		}
	}
	if value.X1.Validate() == nil {
		validator.validateUnitIntervalCode(value.X1, path+"/x1", CodeKeyframeInvalid)
	}
	if value.X2.Validate() == nil {
		validator.validateUnitIntervalCode(value.X2, path+"/x2", CodeKeyframeInvalid)
	}
	if value.Y1.Validate() == nil {
		validator.validateUnitIntervalCode(value.Y1, path+"/y1", CodeKeyframeInvalid)
	}
	if value.Y2.Validate() == nil {
		validator.validateUnitIntervalCode(value.Y2, path+"/y2", CodeKeyframeInvalid)
	}
	if allValid && rationalBig(value.X1).Cmp(rationalBig(value.X2)) > 0 {
		validator.add(CodeKeyframeInvalid, path, "Bezier x control points must be monotonic")
	}
}

func (validator *documentValidator) validateTransitions(timeline Timeline, path string, clips map[ID]clipLocation) {
	incomingSeen := make(map[ID]string)
	outgoingSeen := make(map[ID]string)
	startWindow := make(map[ID]RationalTime)
	endWindow := make(map[ID]RationalTime)
	for index, transition := range timeline.Transitions {
		transitionPath := fmt.Sprintf("%s/transitions/%d", path, index)
		validator.registerID(transition.ID, transitionPath+"/id")
		if transition.Profile != TransitionDissolveV1 {
			validator.add(CodeTransitionInvalid, transitionPath+"/profile", fmt.Sprintf("unsupported transition profile %q", transition.Profile))
		}
		preValid := transition.PreDuration.Validate() == nil && transition.PreDuration.Value >= 0
		postValid := transition.PostDuration.Validate() == nil && transition.PostDuration.Value >= 0
		if !preValid {
			validator.add(CodeTransitionInvalid, transitionPath+"/pre_duration", "pre_duration must be non-negative")
		}
		if !postValid {
			validator.add(CodeTransitionInvalid, transitionPath+"/post_duration", "post_duration must be non-negative")
		}
		outgoing, outgoingOK := clips[transition.OutgoingClipID]
		incoming, incomingOK := clips[transition.IncomingClipID]
		if !outgoingOK || !incomingOK {
			validator.add(CodeTransitionInvalid, transitionPath, "transition clip references must exist in the same timeline")
			continue
		}
		if !validator.validTransitionPair(outgoing, incoming) || outgoing.track.ID != incoming.track.ID || incoming.clipIndex != outgoing.clipIndex+1 {
			validator.add(CodeTransitionInvalid, transitionPath, "transition must connect adjacent clips on one video track, one pinned viewport-px text track, or one viewport-px vector track")
			continue
		}
		if outgoing.clip.Disabled || incoming.clip.Disabled {
			validator.add(CodeTransitionInvalid, transitionPath, "transition must not reference a disabled clip")
			continue
		}
		outgoingEnd, outErr := outgoing.clip.TimelineRange.End()
		if outErr != nil || incoming.clip.TimelineRange.Validate() != nil || !preValid || !postValid {
			continue
		}
		edgeValid := true
		if comparison, err := outgoingEnd.Compare(incoming.clip.TimelineRange.Start); err != nil || comparison != 0 {
			validator.add(CodeTransitionInvalid, transitionPath, "transition clips must share an exact cut boundary")
			edgeValid = false
		}
		if comparison, _ := transition.PreDuration.Compare(outgoing.clip.TimelineRange.Duration); comparison > 0 {
			validator.add(CodeTransitionInvalid, transitionPath+"/pre_duration", "transition window crosses the outgoing clip timeline range")
			edgeValid = false
		}
		if comparison, _ := transition.PostDuration.Compare(incoming.clip.TimelineRange.Duration); comparison > 0 {
			validator.add(CodeTransitionInvalid, transitionPath+"/post_duration", "transition window crosses the incoming clip timeline range")
			edgeValid = false
		}
		if previous, duplicate := outgoingSeen[outgoing.clip.ID]; duplicate {
			validator.add(CodeTransitionInvalid, transitionPath+"/outgoing_clip_id", "outgoing clip already has a transition at "+previous)
		}
		if previous, duplicate := incomingSeen[incoming.clip.ID]; duplicate {
			validator.add(CodeTransitionInvalid, transitionPath+"/incoming_clip_id", "incoming clip already has a transition at "+previous)
		}
		outgoingSeen[outgoing.clip.ID] = transitionPath
		incomingSeen[incoming.clip.ID] = transitionPath
		if edgeValid {
			endWindow[outgoing.clip.ID] = transition.PreDuration
			startWindow[incoming.clip.ID] = transition.PostDuration
			validator.validateTransitionHandles(transition, outgoing, incoming, transitionPath)
		}
	}
	windowClipIDs := make([]ID, 0, len(startWindow))
	for clipID := range startWindow {
		windowClipIDs = append(windowClipIDs, clipID)
	}
	sort.Slice(windowClipIDs, func(i, j int) bool { return windowClipIDs[i] < windowClipIDs[j] })
	for _, clipID := range windowClipIDs {
		start := startWindow[clipID]
		end, exists := endWindow[clipID]
		if !exists {
			continue
		}
		location := clips[clipID]
		total := new(big.Rat).Add(timeBig(start), timeBig(end))
		if total.Cmp(timeBig(location.clip.TimelineRange.Duration)) > 0 {
			validator.add(CodeTransitionOverlap, location.path+"/timeline_range", "transition windows on both edges overlap within this clip")
		}
	}
}

func (validator *documentValidator) validateTransitionHandles(transition Transition, outgoing, incoming clipLocation, path string) {
	// Static images and viewport-bound vectors have no temporal source domain. A transition samples
	// the same immutable source throughout either edge window, so neither needs media handles.
	incomingAsset := validator.assets[incoming.clip.AssetID]
	if transition.PreDuration.Value > 0 && !staticTransitionAsset(incomingAsset) {
		required := scaledTime(transition.PreDuration, incoming.clip.SourceRange.Duration, incoming.clip.TimelineRange.Duration)
		if required != nil && timeBig(incoming.clip.SourceRange.Start).Cmp(required) < 0 {
			validator.add(CodeTransitionHandleMissing, path+"/pre_duration", "incoming clip does not have enough source head handle")
		}
	}
	if transition.PostDuration.Value > 0 {
		asset := validator.assets[outgoing.clip.AssetID]
		if staticTransitionAsset(asset) {
			return
		}
		if asset.Duration == nil {
			return // Media preflight must resolve an unknown duration.
		}
		required := scaledTime(transition.PostDuration, outgoing.clip.SourceRange.Duration, outgoing.clip.TimelineRange.Duration)
		if required == nil {
			return
		}
		sourceEnd, err := outgoing.clip.SourceRange.End()
		if err != nil {
			return
		}
		endWithHandle := new(big.Rat).Add(timeBig(sourceEnd), required)
		if endWithHandle.Cmp(timeBig(*asset.Duration)) > 0 {
			validator.add(CodeTransitionHandleMissing, path+"/post_duration", "outgoing clip does not have enough source tail handle")
		}
	}
}

func (validator *documentValidator) validTransitionPair(outgoing, incoming clipLocation) bool {
	if outgoing.track.Kind != incoming.track.Kind {
		return false
	}
	switch outgoing.track.Kind {
	case TrackKindVideo:
		return true
	case TrackKindText:
		outgoingAsset, outgoingOK := validator.assets[outgoing.clip.AssetID]
		incomingAsset, incomingOK := validator.assets[incoming.clip.AssetID]
		return outgoingOK && incomingOK && textTransitionAsset(outgoingAsset) && textTransitionAsset(incomingAsset)
	case TrackKindVector:
		outgoingAsset, outgoingOK := validator.assets[outgoing.clip.AssetID]
		incomingAsset, incomingOK := validator.assets[incoming.clip.AssetID]
		return outgoingOK && incomingOK && vectorTransitionAsset(outgoingAsset) && vectorTransitionAsset(incomingAsset)
	default:
		return false
	}
}

func vectorTransitionAsset(asset Asset) bool {
	return asset.Kind == AssetKindVector && asset.Vector != nil && asset.Vector.Profile == VectorProfilePathViewportPXV2
}

func textTransitionAsset(asset Asset) bool {
	return asset.Kind == AssetKindText && asset.Text != nil && asset.Text.Profile == TextProfilePinnedFontViewportPXV2
}

func staticTransitionAsset(asset Asset) bool {
	return asset.Kind == AssetKindImage || textTransitionAsset(asset) || vectorTransitionAsset(asset)
}

func scaledTime(value, sourceDuration, timelineDuration RationalTime) *big.Rat {
	if value.Validate() != nil || sourceDuration.Validate() != nil || timelineDuration.Validate() != nil || timelineDuration.Value <= 0 {
		return nil
	}
	return new(big.Rat).Mul(timeBig(value), new(big.Rat).Quo(timeBig(sourceDuration), timeBig(timelineDuration)))
}

func (validator *documentValidator) validateAnnotation(value Annotation, path string) {
	validator.registerID(value.ID, path+"/id")
	if strings.TrimSpace(value.Namespace) == "" {
		validator.add(CodeAnnotationInvalid, path+"/namespace", "annotation namespace is required")
	}
	if value.Range != nil {
		if err := value.Range.Validate(); err != nil {
			validator.add(CodeAnnotationInvalid, path+"/range", err.Error())
		}
	}
	if len(value.Value) == 0 || wirejson.Validate(value.Value) != nil {
		validator.add(CodeAnnotationInvalid, path+"/value", "annotation value must be valid JSON")
	}
}

func (validator *documentValidator) validateRational(value Rational, path string) bool {
	return validator.validateRationalCode(value, path, CodePropertyInvalid)
}

func (validator *documentValidator) validateRationalCode(value Rational, path, code string) bool {
	if err := value.Validate(); err != nil {
		validator.add(code, path, "rational must be reduced with a positive denominator and zero encoded as 0/1")
		return false
	}
	return true
}

func (validator *documentValidator) validatePoint(value Point, path string) {
	validator.validatePointCode(value, path, CodePropertyInvalid)
}

func (validator *documentValidator) validatePointCode(value Point, path, code string) {
	validator.validateRationalCode(value.X, path+"/x", code)
	validator.validateRationalCode(value.Y, path+"/y", code)
}

func (validator *documentValidator) validateRect(value Rect, path string) {
	validator.validateRectCode(value, path, CodePropertyInvalid)
}

func (validator *documentValidator) validateRectCode(value Rect, path, code string) {
	validator.validateRationalCode(value.X, path+"/x", code)
	validator.validateRationalCode(value.Y, path+"/y", code)
	validator.validateRationalCode(value.Width, path+"/width", code)
	validator.validateRationalCode(value.Height, path+"/height", code)
}

func (validator *documentValidator) validateUnitInterval(value Rational, path string) {
	validator.validateUnitIntervalCode(value, path, CodePropertyInvalid)
}

func (validator *documentValidator) validateUnitIntervalCode(value Rational, path, code string) {
	if value.Validate() != nil {
		return
	}
	if rationalCompare(value, 0, 1) < 0 || rationalCompare(value, 1, 1) > 0 {
		validator.add(code, path, "value must be within [0,1]")
	}
}

func rectRationalsValid(value Rect) bool {
	return value.X.Validate() == nil && value.Y.Validate() == nil && value.Width.Validate() == nil && value.Height.Validate() == nil
}

func (validator *documentValidator) validateCropRect(value Rect, path, code string) {
	for _, item := range []struct {
		name string
		v    Rational
	}{{"x", value.X}, {"y", value.Y}, {"width", value.Width}, {"height", value.Height}} {
		validator.validateUnitIntervalCode(item.v, path+"/"+item.name, code)
	}
	if !rectRationalsValid(value) {
		return
	}
	if rationalCompare(value.Width, 0, 1) <= 0 || rationalCompare(value.Height, 0, 1) <= 0 ||
		rationalAdd(value.X, value.Width).Cmp(big.NewRat(1, 1)) > 0 ||
		rationalAdd(value.Y, value.Height).Cmp(big.NewRat(1, 1)) > 0 {
		validator.add(code, path, "crop must be a positive rectangle inside [0,1]²")
	}
}

func (validator *documentValidator) validateRawMap(values map[string]json.RawMessage, path string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || len(values[key]) == 0 || wirejson.Validate(values[key]) != nil {
			validator.add(CodeJSONInvalid, path+"/"+safeJSONPointerSegment(key), "metadata key and value must be valid")
		}
	}
}

func validTrackKind(kind TrackKind) bool {
	switch kind {
	case TrackKindVideo, TrackKindAudio, TrackKindText, TrackKindVector, TrackKindAnnotation:
		return true
	default:
		return false
	}
}

func trackAcceptsAsset(track TrackKind, asset AssetKind) bool {
	switch track {
	case TrackKindVideo:
		return asset == AssetKindVideo || asset == AssetKindImage
	case TrackKindAudio:
		return asset == AssetKindAudio || asset == AssetKindVideo
	case TrackKindText:
		return asset == AssetKindText
	case TrackKindVector:
		return asset == AssetKindVector
	case TrackKindAnnotation:
		return false
	default:
		return false
	}
}

func temporalAssetKind(kind AssetKind) bool {
	return kind == AssetKindVideo || kind == AssetKindAudio
}

func keyframeTargetType(target KeyframeTarget) (KeyframeValueType, bool) {
	switch target {
	case KeyframeTargetTransformPosition, KeyframeTargetTransformScale, KeyframeTargetTransformAnchor:
		return KeyframeValuePoint, true
	case KeyframeTargetTransformRotation, KeyframeTargetOpacity, KeyframeTargetAudioGain, KeyframeTargetAudioPan:
		return KeyframeValueScalar, true
	case KeyframeTargetCrop:
		return KeyframeValueRect, true
	case KeyframeTargetTextFill:
		return KeyframeValueColor, true
	default:
		return "", false
	}
}

func reservedSemanticToken(value string) bool {
	reservedSemanticOnce.Do(func() {
		reservedSemanticTokens = make(map[string]struct{})
		add := func(value string) {
			if capabilityTokenPattern.MatchString(value) {
				reservedSemanticTokens[value] = struct{}{}
			}
		}
		for _, capability := range []videoedit.CapabilityID{
			videoedit.CapabilityVideoLinearRetime,
			videoedit.CapabilityAudioVarispeed,
			videoedit.CapabilityAudioTimeStretch,
		} {
			add(string(capability))
		}
		catalog := SemanticDescriptorsV2()
		for _, descriptor := range catalog.AudioGraphs {
			add(descriptor.Profile)
			add(descriptor.EvaluatorProfile)
			for _, capability := range descriptor.ExplicitGraphCapabilities {
				add(string(capability))
			}
		}
		for _, descriptor := range catalog.Properties {
			add(descriptor.Namespace)
			add(descriptor.EvaluatorProfile)
			add(string(descriptor.RequiredCapability))
			for _, binding := range descriptor.Bindings {
				add(binding.KeyframeProfile)
				add(string(binding.KeyframeCapability))
			}
		}
		for _, descriptor := range catalog.Sources {
			add(descriptor.Profile)
			add(descriptor.EvaluatorProfile)
			add(string(descriptor.RequiredCapability))
			add(descriptor.KeyframeProfile)
			add(string(descriptor.KeyframeCapability))
		}
		for _, descriptor := range catalog.Interpolations {
			add(descriptor.Evaluator)
			add(string(descriptor.RequiredCapability))
		}
		for _, descriptor := range catalog.Transitions {
			add(descriptor.Profile)
			add(descriptor.EvaluatorProfile)
			add(string(descriptor.RequiredCapability))
		}
		for _, descriptor := range catalog.TransitionScopes {
			add(descriptor.Profile)
			add(descriptor.RequiredCapability)
			add(descriptor.BaseTransitionProfile)
			add(descriptor.VectorProfile)
			add(descriptor.VectorEvaluatorProfile)
			add(descriptor.TransitionEvaluator)
		}
		textV2 := TextPinnedFontViewportDescriptor()
		for _, token := range []string{
			textV2.Profile, textV2.EvaluatorProfile, string(textV2.RequiredCapability),
			textV2.FontProfile, textV2.ShapingProfile, textV2.LayoutProfile,
			textV2.OutlineProfile, textV2.RasterProfile, textV2.PaintProfile, textV2.CoverageHashProfile,
			textV2.SemanticHashProfile, textV2.LayoutInputHashProfile, textV2.RunInputProfile,
			textV2.UnicodeValidationProfile, textV2.UnicodeValidationHashProfile,
		} {
			add(token)
		}
		textTransition := TextTransitionScope()
		for _, token := range []string{
			textTransition.Profile, textTransition.RequiredCapability,
			textTransition.BaseTransitionProfile, textTransition.TextProfile,
			textTransition.TextEvaluatorProfile, textTransition.TransitionEvaluator,
		} {
			add(token)
		}
	})
	_, exists := reservedSemanticTokens[value]
	return exists
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
