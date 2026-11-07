package evaluator

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

type Transform struct {
	Position        ExactPoint    `json:"position"`
	Scale           ExactPoint    `json:"scale"`
	RotationDegrees ExactRational `json:"rotation_degrees"`
	Anchor          ExactPoint    `json:"anchor"`
}

// AudioChannelMatrix is the exact runtime form of Document v2's capability-gated source-channel
// to stereo matrix. Slices are defensive copies owned by one evaluation result.
type AudioChannelMatrix struct {
	SourceLayout      string          `json:"source_layout"`
	SourceChannels    []string        `json:"source_channels"`
	LeftCoefficients  []ExactRational `json:"left_coefficients"`
	RightCoefficients []ExactRational `json:"right_coefficients"`
}

type Properties struct {
	Transform           *Transform                     `json:"transform,omitempty"`
	Opacity             *ExactRational                 `json:"opacity,omitempty"`
	Crop                *ExactRect                     `json:"crop,omitempty"`
	AudioGain           *ExactRational                 `json:"audio_gain,omitempty"`
	AudioPan            *ExactRational                 `json:"audio_pan,omitempty"`
	AudioChannelMatrix  *AudioChannelMatrix            `json:"audio_channel_matrix,omitempty"`
	TextFill            *documentv2.ColorRGBA16        `json:"text_fill,omitempty"`
	RenderingExtensions []documentv2.ExtensionProperty `json:"rendering_extensions,omitempty"`
}

// EvaluateProperties resolves defaults, authored static values, and keyframe overrides at an
// absolute timeline time. It returns only properties meaningful to the track: visual defaults for
// video/text/vector tracks, audio defaults for audio tracks, plus a text asset's authored fill.
// The standalone function rejects semantics that require backend authorization (rendering
// extensions and an audio channel matrix) because it has no capability context.
// Program preflights capabilities, freezes a compiled property snapshot, and adds only authorized
// AffectsRendering extensions; non-rendering intent never enters renderer input.
func EvaluateProperties(
	trackKind documentv2.TrackKind,
	clip documentv2.Clip,
	asset documentv2.Asset,
	timelineTime documentv2.RationalTime,
) (Properties, error) {
	if err := validatePropertyInputs(trackKind, clip, asset); err != nil {
		return Properties{}, err
	}
	localTime, err := ClipLocalTime(clip, timelineTime)
	if err != nil {
		return Properties{}, err
	}
	result, err := evaluateStaticProperties(trackKind, clip, asset)
	if err != nil {
		return Properties{}, err
	}
	for _, lane := range clip.KeyframeLanes {
		value, laneErr := EvaluateLane(lane, localTime)
		if laneErr != nil {
			return Properties{}, laneErr
		}
		if applyErr := applyLane(&result, trackKind, lane.Target, value); applyErr != nil {
			return Properties{}, applyErr
		}
	}
	return result, nil
}

func evaluateStaticProperties(trackKind documentv2.TrackKind, clip documentv2.Clip, asset documentv2.Asset) (Properties, error) {
	result := Properties{}
	visual := trackKind == documentv2.TrackKindVideo || trackKind == documentv2.TrackKindText || trackKind == documentv2.TrackKindVector
	switch {
	case visual:
		transform, transformErr := defaultTransform()
		if transformErr != nil {
			return Properties{}, transformErr
		}
		result.Transform = &transform
		opacity := exactInteger(1)
		result.Opacity = &opacity
		crop, cropErr := exactRect(documentv2.Rect{
			X:      documentv2.Rational{Numerator: 0, Denominator: 1},
			Y:      documentv2.Rational{Numerator: 0, Denominator: 1},
			Width:  documentv2.Rational{Numerator: 1, Denominator: 1},
			Height: documentv2.Rational{Numerator: 1, Denominator: 1},
		})
		if cropErr != nil {
			return Properties{}, cropErr
		}
		result.Crop = &crop
	case trackKind == documentv2.TrackKindAudio:
		gain, pan := exactInteger(1), exactInteger(0)
		result.AudioGain, result.AudioPan = &gain, &pan
	case trackKind == documentv2.TrackKindAnnotation:
		if clip.PropertySet != nil || len(clip.KeyframeLanes) != 0 {
			return Properties{}, evaluationError(CodeInputInvalid, "clip", ErrInputInvalid)
		}
		return result, nil
	default:
		return Properties{}, evaluationError(CodeInputInvalid, "track.kind", ErrInputInvalid)
	}

	if trackKind == documentv2.TrackKindText {
		if asset.Kind != documentv2.AssetKindText || asset.Text == nil {
			return Properties{}, evaluationError(CodeInputInvalid, "asset.text", ErrInputInvalid)
		}
		fill := asset.Text.Fill
		result.TextFill = &fill
	}
	if clip.PropertySet != nil {
		if err := applyStaticProperties(&result, trackKind, *clip.PropertySet); err != nil {
			return Properties{}, err
		}
	}
	return result, nil
}

func defaultTransform() (Transform, error) {
	zero := documentv2.Rational{Numerator: 0, Denominator: 1}
	one := documentv2.Rational{Numerator: 1, Denominator: 1}
	half := documentv2.Rational{Numerator: 1, Denominator: 2}
	position, err := exactPoint(documentv2.Point{X: zero, Y: zero})
	if err != nil {
		return Transform{}, err
	}
	scale, err := exactPoint(documentv2.Point{X: one, Y: one})
	if err != nil {
		return Transform{}, err
	}
	anchor, err := exactPoint(documentv2.Point{X: half, Y: half})
	if err != nil {
		return Transform{}, err
	}
	rotation, err := exactFromDocument(zero)
	if err != nil {
		return Transform{}, err
	}
	return Transform{Position: position, Scale: scale, RotationDegrees: rotation, Anchor: anchor}, nil
}

func applyStaticProperties(result *Properties, trackKind documentv2.TrackKind, authored documentv2.PropertySet) error {
	visual := trackKind == documentv2.TrackKindVideo || trackKind == documentv2.TrackKindText || trackKind == documentv2.TrackKindVector
	if authored.Transform != nil {
		if !visual || result.Transform == nil {
			return evaluationError(CodeInputInvalid, "clip.property_set.transform", ErrInputInvalid)
		}
		position, err := exactPoint(authored.Transform.Position)
		if err != nil {
			return err
		}
		scale, err := exactPoint(authored.Transform.Scale)
		if err != nil {
			return err
		}
		rotation, err := exactFromDocument(authored.Transform.RotationDegrees)
		if err != nil {
			return err
		}
		anchor, err := exactPoint(authored.Transform.Anchor)
		if err != nil {
			return err
		}
		result.Transform = &Transform{Position: position, Scale: scale, RotationDegrees: rotation, Anchor: anchor}
	}
	if authored.Opacity != nil {
		if !visual {
			return evaluationError(CodeInputInvalid, "clip.property_set.opacity", ErrInputInvalid)
		}
		value, err := exactFromDocument(authored.Opacity.Value)
		if err != nil {
			return err
		}
		result.Opacity = &value
	}
	if authored.Crop != nil {
		if !visual {
			return evaluationError(CodeInputInvalid, "clip.property_set.crop", ErrInputInvalid)
		}
		value, err := exactRect(authored.Crop.Rect)
		if err != nil {
			return err
		}
		result.Crop = &value
	}
	if authored.AudioGain != nil {
		if trackKind != documentv2.TrackKindAudio {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_gain", ErrInputInvalid)
		}
		value, err := exactFromDocument(authored.AudioGain.Linear)
		if err != nil {
			return err
		}
		result.AudioGain = &value
	}
	if authored.AudioPan != nil {
		if trackKind != documentv2.TrackKindAudio {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_pan", ErrInputInvalid)
		}
		value, err := exactFromDocument(authored.AudioPan.Value)
		if err != nil {
			return err
		}
		result.AudioPan = &value
	}
	if authored.AudioChannelMatrix != nil {
		if trackKind != documentv2.TrackKindAudio {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_channel_matrix", ErrInputInvalid)
		}
		value, err := exactAudioChannelMatrix(*authored.AudioChannelMatrix)
		if err != nil {
			return err
		}
		result.AudioChannelMatrix = &value
	}
	return nil
}

func exactAudioChannelMatrix(value documentv2.AudioChannelMatrixProperty) (AudioChannelMatrix, error) {
	result := AudioChannelMatrix{
		SourceLayout:      value.SourceLayout,
		SourceChannels:    append([]string(nil), value.SourceChannels...),
		LeftCoefficients:  make([]ExactRational, len(value.LeftCoefficients)),
		RightCoefficients: make([]ExactRational, len(value.RightCoefficients)),
	}
	for index := range value.LeftCoefficients {
		coefficient, err := exactFromDocument(value.LeftCoefficients[index])
		if err != nil {
			return AudioChannelMatrix{}, err
		}
		result.LeftCoefficients[index] = coefficient
	}
	for index := range value.RightCoefficients {
		coefficient, err := exactFromDocument(value.RightCoefficients[index])
		if err != nil {
			return AudioChannelMatrix{}, err
		}
		result.RightCoefficients[index] = coefficient
	}
	return result, nil
}

func applyLane(result *Properties, trackKind documentv2.TrackKind, target documentv2.KeyframeTarget, value Value) error {
	visual := trackKind == documentv2.TrackKindVideo || trackKind == documentv2.TrackKindText || trackKind == documentv2.TrackKindVector
	switch target {
	case documentv2.KeyframeTargetTransformPosition:
		if !visual || result.Transform == nil || value.Point == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		result.Transform.Position = *value.Point
	case documentv2.KeyframeTargetTransformScale:
		if !visual || result.Transform == nil || value.Point == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		result.Transform.Scale = *value.Point
	case documentv2.KeyframeTargetTransformRotation:
		if !visual || result.Transform == nil || value.Scalar == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		result.Transform.RotationDegrees = *value.Scalar
	case documentv2.KeyframeTargetTransformAnchor:
		if !visual || result.Transform == nil || value.Point == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		result.Transform.Anchor = *value.Point
	case documentv2.KeyframeTargetOpacity:
		if !visual || value.Scalar == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		copy := *value.Scalar
		result.Opacity = &copy
	case documentv2.KeyframeTargetCrop:
		if !visual || value.Rect == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		copy := *value.Rect
		result.Crop = &copy
	case documentv2.KeyframeTargetAudioGain:
		if trackKind != documentv2.TrackKindAudio || value.Scalar == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		copy := *value.Scalar
		result.AudioGain = &copy
	case documentv2.KeyframeTargetAudioPan:
		if trackKind != documentv2.TrackKindAudio || value.Scalar == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		copy := *value.Scalar
		result.AudioPan = &copy
	case documentv2.KeyframeTargetTextFill:
		if trackKind != documentv2.TrackKindText || value.Color == nil {
			return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
		}
		copy := *value.Color
		result.TextFill = &copy
	default:
		return evaluationError(CodeKeyframeInvalid, "lane.target", ErrKeyframeInvalid)
	}
	return nil
}

func cloneRenderingExtensions(values []documentv2.ExtensionProperty) []documentv2.ExtensionProperty {
	if len(values) == 0 {
		return nil
	}
	result := make([]documentv2.ExtensionProperty, 0, len(values))
	for _, value := range values {
		if !value.AffectsRendering {
			continue
		}
		copy := value
		copy.Value = append(json.RawMessage(nil), value.Value...)
		result = append(result, copy)
	}
	return result
}

func validatePropertyInputs(trackKind documentv2.TrackKind, clip documentv2.Clip, asset documentv2.Asset) error {
	if clip.AssetID != asset.ID || !trackAcceptsAsset(trackKind, asset.Kind) {
		return evaluationError(CodeInputInvalid, "clip.asset_id", ErrInputInvalid)
	}
	visual := trackKind == documentv2.TrackKindVideo || trackKind == documentv2.TrackKindText || trackKind == documentv2.TrackKindVector
	if trackKind == documentv2.TrackKindAnnotation {
		return evaluationError(CodeInputInvalid, "track.kind", ErrInputInvalid)
	}
	if clip.PropertySet != nil {
		properties := *clip.PropertySet
		if properties.Transform != nil && (!visual || !validTransform(*properties.Transform)) {
			return evaluationError(CodeInputInvalid, "clip.property_set.transform", ErrInputInvalid)
		}
		if properties.Opacity != nil && (!visual || !rationalWithin(properties.Opacity.Value, 0, 1)) {
			return evaluationError(CodeInputInvalid, "clip.property_set.opacity", ErrInputInvalid)
		}
		if properties.Crop != nil && (!visual || !validCrop(properties.Crop.Rect)) {
			return evaluationError(CodeInputInvalid, "clip.property_set.crop", ErrInputInvalid)
		}
		if properties.AudioGain != nil && (trackKind != documentv2.TrackKindAudio || !rationalAtLeast(properties.AudioGain.Linear, 0)) {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_gain", ErrInputInvalid)
		}
		if properties.AudioPan != nil && (trackKind != documentv2.TrackKindAudio || !rationalWithin(properties.AudioPan.Value, -1, 1)) {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_pan", ErrInputInvalid)
		}
		if properties.AudioChannelMatrix != nil {
			return evaluationError(CodeInputInvalid, "clip.property_set.audio_channel_matrix", ErrInputInvalid)
		}
		previousNamespace := ""
		for _, extension := range properties.Extensions {
			if strings.TrimSpace(extension.Namespace) == "" || previousNamespace != "" && extension.Namespace <= previousNamespace ||
				len(extension.Value) == 0 || !json.Valid(extension.Value) || bytes.Equal(bytes.TrimSpace(extension.Value), []byte("null")) ||
				extension.AffectsRendering && strings.TrimSpace(extension.RequiredCapability) == "" ||
				!extension.AffectsRendering && extension.RequiredCapability != "" || extension.AffectsRendering {
				return evaluationError(CodeInputInvalid, "clip.property_set.extensions", ErrInputInvalid)
			}
			previousNamespace = extension.Namespace
		}
	}
	seenTargets := make(map[documentv2.KeyframeTarget]struct{}, len(clip.KeyframeLanes))
	for _, lane := range clip.KeyframeLanes {
		if !laneAllowedOnTrack(lane.Target, trackKind, asset.Kind) {
			return evaluationError(CodeInputInvalid, "clip.keyframe_lanes", ErrInputInvalid)
		}
		if _, duplicate := seenTargets[lane.Target]; duplicate {
			return evaluationError(CodeInputInvalid, "clip.keyframe_lanes", ErrInputInvalid)
		}
		seenTargets[lane.Target] = struct{}{}
		for _, keyframe := range lane.Keyframes {
			if keyframe.Offset.Validate() != nil || clip.TimelineRange.Duration.Validate() != nil {
				continue // EvaluateLane/ClipLocalTime return the more specific stable error.
			}
			comparison, err := keyframe.Offset.Compare(clip.TimelineRange.Duration)
			if err != nil || comparison > 0 {
				return evaluationError(CodeInputInvalid, "clip.keyframe_lanes", ErrInputInvalid)
			}
		}
	}
	return nil
}

func trackAcceptsAsset(track documentv2.TrackKind, asset documentv2.AssetKind) bool {
	switch track {
	case documentv2.TrackKindVideo:
		return asset == documentv2.AssetKindVideo || asset == documentv2.AssetKindImage
	case documentv2.TrackKindAudio:
		return asset == documentv2.AssetKindAudio || asset == documentv2.AssetKindVideo
	case documentv2.TrackKindText:
		return asset == documentv2.AssetKindText
	case documentv2.TrackKindVector:
		return asset == documentv2.AssetKindVector
	default:
		return false
	}
}

func laneAllowedOnTrack(target documentv2.KeyframeTarget, track documentv2.TrackKind, asset documentv2.AssetKind) bool {
	switch target {
	case documentv2.KeyframeTargetTransformPosition, documentv2.KeyframeTargetTransformScale,
		documentv2.KeyframeTargetTransformRotation, documentv2.KeyframeTargetTransformAnchor,
		documentv2.KeyframeTargetOpacity, documentv2.KeyframeTargetCrop:
		return track == documentv2.TrackKindVideo || track == documentv2.TrackKindText || track == documentv2.TrackKindVector
	case documentv2.KeyframeTargetAudioGain, documentv2.KeyframeTargetAudioPan:
		return track == documentv2.TrackKindAudio
	case documentv2.KeyframeTargetTextFill:
		return track == documentv2.TrackKindText && asset == documentv2.AssetKindText
	default:
		return false
	}
}

func validTransform(value documentv2.TransformProperty) bool {
	return validPoint(value.Position) && validPoint(value.Scale) && value.RotationDegrees.Validate() == nil &&
		validPoint(value.Anchor) && rationalWithin(value.Anchor.X, 0, 1) && rationalWithin(value.Anchor.Y, 0, 1)
}

func validPoint(value documentv2.Point) bool {
	return value.X.Validate() == nil && value.Y.Validate() == nil
}

func validCrop(value documentv2.Rect) bool {
	x, xOK := documentRationalBig(value.X)
	y, yOK := documentRationalBig(value.Y)
	width, widthOK := documentRationalBig(value.Width)
	height, heightOK := documentRationalBig(value.Height)
	if !xOK || !yOK || !widthOK || !heightOK || width.Sign() <= 0 || height.Sign() <= 0 {
		return false
	}
	one := big.NewRat(1, 1)
	return x.Sign() >= 0 && y.Sign() >= 0 && x.Cmp(one) <= 0 && y.Cmp(one) <= 0 &&
		width.Cmp(one) <= 0 && height.Cmp(one) <= 0 &&
		new(big.Rat).Add(x, width).Cmp(one) <= 0 && new(big.Rat).Add(y, height).Cmp(one) <= 0
}

func rationalWithin(value documentv2.Rational, minimum, maximum int64) bool {
	asBig, ok := documentRationalBig(value)
	return ok && asBig.Cmp(big.NewRat(minimum, 1)) >= 0 && asBig.Cmp(big.NewRat(maximum, 1)) <= 0
}

func rationalAtLeast(value documentv2.Rational, minimum int64) bool {
	asBig, ok := documentRationalBig(value)
	return ok && asBig.Cmp(big.NewRat(minimum, 1)) >= 0
}

func documentRationalBig(value documentv2.Rational) (*big.Rat, bool) {
	if value.Validate() != nil {
		return nil, false
	}
	return new(big.Rat).SetFrac64(value.Numerator, value.Denominator), true
}
