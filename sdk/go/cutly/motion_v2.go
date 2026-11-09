package cutly

import (
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const MotionCompositionProfileV2 = "cutly.motion-composition.document-v2.v1"

var ErrMotionCompositionV2 = errors.New("cutly sdk: invalid motion composition")

// MotionCompositionV2 is a frame-authored convenience layer over Document v2.
// It does not introduce alternate render semantics: BuildMotionCompositionV2
// compiles every layer and animation into ordinary typed Document v2 fields.
// Layer order is back-to-front and each layer receives its own track so visual
// overlap remains explicit and deterministic.
type MotionCompositionV2 struct {
	Profile         string
	ID              ID
	Revision        uint64
	TimelineID      ID
	Name            string
	Width           int
	Height          int
	FramesPerSecond int64
	DurationFrames  int64
	Assets          []Asset
	Layers          []MotionLayerV2
	Transitions     []Transition
	AudioBuses      []AudioBus
}

// MotionLayerV2 describes one independently animated visual or audio layer.
// StartFrame and DurationFrames use the composition frame clock. SourceStartFrame
// is meaningful for temporal media; static image, text and vector sources ignore
// temporal sampling while retaining the exact edit identity.
type MotionLayerV2 struct {
	ID               ID
	Name             string
	TrackKind        documentv2.TrackKind
	AssetID          ID
	StartFrame       int64
	DurationFrames   int64
	SourceStartFrame int64
	AudioBusID       ID
	Disabled         bool
	Transform        *MotionTransformV2
	Crop             *Rect
	Opacity          *Rational
	Animation        MotionAnimationV2
}

type MotionTransformV2 struct {
	Position        Point
	Scale           Point
	RotationDegrees Rational
	Anchor          Point
}

type MotionAnimationV2 struct {
	Position []MotionPointKeyframeV2
	Scale    []MotionPointKeyframeV2
	Rotation []MotionScalarKeyframeV2
	Opacity  []MotionScalarKeyframeV2
}

type MotionPointKeyframeV2 struct {
	Frame         int64
	Value         Point
	Interpolation documentv2.Interpolation
	Easing        *documentv2.CubicBezier
}

type MotionScalarKeyframeV2 struct {
	Frame         int64
	Value         Rational
	Interpolation documentv2.Interpolation
	Easing        *documentv2.CubicBezier
}

func MotionRationalV2(numerator, denominator int64) Rational {
	return documentv2.Rational{Numerator: numerator, Denominator: denominator}
}

func MotionPointV2(xNumerator, xDenominator, yNumerator, yDenominator int64) Point {
	return documentv2.Point{X: MotionRationalV2(xNumerator, xDenominator), Y: MotionRationalV2(yNumerator, yDenominator)}
}

func MotionIdentityTransformV2() MotionTransformV2 {
	return MotionTransformV2{
		Position:        MotionPointV2(0, 1, 0, 1),
		Scale:           MotionPointV2(1, 1, 1, 1),
		RotationDegrees: MotionRationalV2(0, 1),
		Anchor:          MotionPointV2(0, 1, 0, 1),
	}
}

// MotionEaseV2 constructs an exact cubic-bezier easing. The evaluator uses the
// same deterministic rational curve for preview and final render.
func MotionEaseV2(x1, y1, x2, y2 Rational) *documentv2.CubicBezier {
	return &documentv2.CubicBezier{X1: x1, Y1: y1, X2: x2, Y2: y2}
}

func MotionEaseExpoOutV2() *documentv2.CubicBezier {
	return MotionEaseV2(MotionRationalV2(4, 25), MotionRationalV2(1, 1), MotionRationalV2(3, 10), MotionRationalV2(1, 1))
}

func MotionEasePower3OutV2() *documentv2.CubicBezier {
	return MotionEaseV2(MotionRationalV2(11, 50), MotionRationalV2(1, 1), MotionRationalV2(9, 25), MotionRationalV2(1, 1))
}

func MotionEasePower2InOutV2() *documentv2.CubicBezier {
	return MotionEaseV2(MotionRationalV2(3, 5), MotionRationalV2(0, 1), MotionRationalV2(4, 5), MotionRationalV2(1, 1))
}

// MotionImageAssetV2 declares one immutable still-image resource.
func MotionImageAssetV2(id ID, uri, contentHash string) Asset {
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindImage, URI: uri, ContentHash: contentHash}
}

// MotionAudioAssetV2 declares one immutable audio resource with a frame-authored
// duration. The duration is reduced into the composition clock by Build.
func MotionAudioAssetV2(id ID, uri, contentHash string, duration RationalTime) Asset {
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindAudio, URI: uri, ContentHash: contentHash, Duration: &duration}
}

func MotionFontAssetV2(id ID, uri, contentHash string) Asset {
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindFont, URI: uri, ContentHash: contentHash}
}

type MotionTextAssetSpecV2 struct {
	ID          ID
	Text        string
	FontAssetID ID
	FontSize    Rational
	LineHeight  Rational
	Width       uint32
	Height      uint32
	Fill        ColorRGBA16
	Language    string
	Script      string
	Direction   documentv2.TextDirection
	Alignment   documentv2.TextAlignment
}

func MotionTextAssetV2(spec MotionTextAssetSpecV2) Asset {
	line := spec.LineHeight
	return documentv2.Asset{ID: spec.ID, Kind: documentv2.AssetKindText, Text: &documentv2.TextSource{
		Profile: documentv2.TextProfilePinnedFontViewportPXV2, Text: spec.Text, FontAssetID: spec.FontAssetID,
		FontSize: spec.FontSize, LineHeight: &line, Viewport: &documentv2.RasterViewport{WidthPixels: spec.Width, HeightPixels: spec.Height},
		Fill: spec.Fill, Language: spec.Language, Script: spec.Script, Direction: spec.Direction, Alignment: spec.Alignment,
	}}
}

// MotionRectangleAssetV2 emits a deterministic fill-only vector rectangle.
// Offset, shadow and rule primitives can share the same geometry with distinct
// paints and transforms.
func MotionRectangleAssetV2(id ID, width, height uint32, fill ColorRGBA16) Asset {
	rat := func(value int64) Rational { return MotionRationalV2(value, 1) }
	point := func(x, y int64) Point { return MotionPointV2(x, 1, y, 1) }
	w, h := int64(width), int64(height)
	return documentv2.Asset{ID: id, Kind: documentv2.AssetKindVector, Vector: &documentv2.VectorSource{
		Profile:  documentv2.VectorProfilePathViewportPXV2,
		ViewBox:  documentv2.Rect{X: rat(0), Y: rat(0), Width: rat(w), Height: rat(h)},
		Viewport: &documentv2.RasterViewport{WidthPixels: width, HeightPixels: height},
		Paths: []documentv2.VectorPath{{ID: id + "-path", FillRule: documentv2.VectorFillNonZero, Fill: &fill, Commands: []documentv2.VectorCommand{
			{Op: documentv2.VectorMoveTo, Points: []Point{point(0, 0)}},
			{Op: documentv2.VectorLineTo, Points: []Point{point(w, 0)}},
			{Op: documentv2.VectorLineTo, Points: []Point{point(w, h)}},
			{Op: documentv2.VectorLineTo, Points: []Point{point(0, h)}},
			{Op: documentv2.VectorClose},
		}}},
	}}
}

func BuildMotionCompositionV2(spec MotionCompositionV2) (Document, error) {
	if spec.Profile == "" {
		spec.Profile = MotionCompositionProfileV2
	}
	if spec.Profile != MotionCompositionProfileV2 || spec.ID == "" || spec.TimelineID == "" || spec.Revision == 0 || spec.Width <= 0 || spec.Height <= 0 || spec.FramesPerSecond <= 0 || spec.DurationFrames <= 0 || len(spec.Layers) == 0 {
		return Document{}, ErrMotionCompositionV2
	}
	document := documentv2.Document{SchemaVersion: documentv2.SchemaVersion, ID: spec.ID, Revision: videoedit.Revision(spec.Revision), Assets: append([]Asset(nil), spec.Assets...), AudioBuses: append([]AudioBus(nil), spec.AudioBuses...)}
	timeline := documentv2.Timeline{ID: spec.TimelineID, Name: spec.Name, Transitions: append([]Transition(nil), spec.Transitions...)}
	for index, layer := range spec.Layers {
		track, err := compileMotionLayerV2(spec, layer, index)
		if err != nil {
			return Document{}, err
		}
		timeline.Tracks = append(timeline.Tracks, track)
	}
	document.Timelines = []documentv2.Timeline{timeline}
	raw, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrMotionCompositionV2, err)
	}
	owned, err := documentv2.Decode(raw)
	if err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrMotionCompositionV2, err)
	}
	return owned, nil
}

func compileMotionLayerV2(spec MotionCompositionV2, layer MotionLayerV2, index int) (Track, error) {
	if layer.ID == "" || layer.AssetID == "" || layer.DurationFrames <= 0 || layer.StartFrame < 0 || layer.SourceStartFrame < 0 || layer.StartFrame > spec.DurationFrames-layer.DurationFrames {
		return Track{}, fmt.Errorf("%w: layer %d range", ErrMotionCompositionV2, index)
	}
	timelineRange := documentv2.TimeRange{Start: motionFrameTimeV2(layer.StartFrame, spec.FramesPerSecond), Duration: motionFrameTimeV2(layer.DurationFrames, spec.FramesPerSecond)}
	sourceRange := documentv2.TimeRange{Start: motionFrameTimeV2(layer.SourceStartFrame, spec.FramesPerSecond), Duration: timelineRange.Duration}
	clip := documentv2.Clip{ID: layer.ID, AssetID: layer.AssetID, TimelineRange: timelineRange, SourceRange: sourceRange, Disabled: layer.Disabled}
	animated := len(layer.Animation.Position)+len(layer.Animation.Scale)+len(layer.Animation.Rotation)+len(layer.Animation.Opacity) != 0
	if layer.Transform != nil || layer.Crop != nil || layer.Opacity != nil || animated {
		transform := MotionIdentityTransformV2()
		if layer.Transform != nil {
			transform = *layer.Transform
		}
		opacity := MotionRationalV2(1, 1)
		if layer.Opacity != nil {
			opacity = *layer.Opacity
		}
		clip.PropertySet = &documentv2.PropertySet{
			Transform: &documentv2.TransformProperty{Position: transform.Position, Scale: transform.Scale, RotationDegrees: transform.RotationDegrees, Anchor: transform.Anchor},
			Opacity:   &documentv2.OpacityProperty{Value: opacity},
		}
		if layer.Crop != nil {
			value := *layer.Crop
			clip.PropertySet.Crop = &documentv2.CropProperty{Rect: value}
		}
	}
	var err error
	clip.KeyframeLanes, err = compileMotionAnimationV2(layer, spec.FramesPerSecond)
	if err != nil {
		return Track{}, fmt.Errorf("%w: layer %d animation: %v", ErrMotionCompositionV2, index, err)
	}
	trackID := ID(fmt.Sprintf("motion-layer-%04d-%s", index, layer.ID))
	return documentv2.Track{ID: trackID, Name: layer.Name, Kind: layer.TrackKind, AudioBusID: layer.AudioBusID, Clips: []documentv2.Clip{clip}}, nil
}

func compileMotionAnimationV2(layer MotionLayerV2, fps int64) ([]documentv2.KeyframeLane, error) {
	lanes := make([]documentv2.KeyframeLane, 0, 4)
	pointLane := func(suffix string, target documentv2.KeyframeTarget, frames []MotionPointKeyframeV2) error {
		if len(frames) == 0 {
			return nil
		}
		lane := documentv2.KeyframeLane{ID: layer.ID + "-" + ID(suffix), Target: target, ValueType: documentv2.KeyframeValuePoint}
		previous := int64(-1)
		for _, frame := range frames {
			if frame.Frame < 0 || frame.Frame > layer.DurationFrames || frame.Frame <= previous {
				return errors.New("point keyframes must be ordered and inside the layer")
			}
			value := frame.Value
			lane.Keyframes = append(lane.Keyframes, documentv2.Keyframe{Offset: motionFrameTimeV2(frame.Frame, fps), Value: documentv2.KeyframeValue{Point: &value}, Interpolation: frame.Interpolation, Easing: cloneMotionEaseV2(frame.Easing)})
			previous = frame.Frame
		}
		lanes = append(lanes, lane)
		return nil
	}
	scalarLane := func(suffix string, target documentv2.KeyframeTarget, frames []MotionScalarKeyframeV2) error {
		if len(frames) == 0 {
			return nil
		}
		lane := documentv2.KeyframeLane{ID: layer.ID + "-" + ID(suffix), Target: target, ValueType: documentv2.KeyframeValueScalar}
		previous := int64(-1)
		for _, frame := range frames {
			if frame.Frame < 0 || frame.Frame > layer.DurationFrames || frame.Frame <= previous {
				return errors.New("scalar keyframes must be ordered and inside the layer")
			}
			value := frame.Value
			lane.Keyframes = append(lane.Keyframes, documentv2.Keyframe{Offset: motionFrameTimeV2(frame.Frame, fps), Value: documentv2.KeyframeValue{Scalar: &value}, Interpolation: frame.Interpolation, Easing: cloneMotionEaseV2(frame.Easing)})
			previous = frame.Frame
		}
		lanes = append(lanes, lane)
		return nil
	}
	if err := pointLane("position", documentv2.KeyframeTargetTransformPosition, layer.Animation.Position); err != nil {
		return nil, err
	}
	if err := pointLane("scale", documentv2.KeyframeTargetTransformScale, layer.Animation.Scale); err != nil {
		return nil, err
	}
	if err := scalarLane("rotation", documentv2.KeyframeTargetTransformRotation, layer.Animation.Rotation); err != nil {
		return nil, err
	}
	if err := scalarLane("opacity", documentv2.KeyframeTargetOpacity, layer.Animation.Opacity); err != nil {
		return nil, err
	}
	return lanes, nil
}

func cloneMotionEaseV2(value *documentv2.CubicBezier) *documentv2.CubicBezier {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func motionFrameTimeV2(frame, fps int64) RationalTime {
	if frame == 0 {
		return RationalTime{Value: 0, Rate: 1}
	}
	divisor := motionGCDV2(frame, fps)
	return RationalTime{Value: frame / divisor, Rate: fps / divisor}
}

func motionGCDV2(left, right int64) int64 {
	if left < 0 {
		left = -left
	}
	if right < 0 {
		right = -right
	}
	for right != 0 {
		left, right = right, left%right
	}
	if left == 0 {
		return 1
	}
	return left
}
