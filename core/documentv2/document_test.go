package documentv2

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestDocumentV2CanonicalFixtureAndHash(t *testing.T) {
	document := fixtureDocument(t)
	canonical, err := CanonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.TrimSpace(readFixture(t, "document-v2.canonical.json"))
	if !bytes.Equal(canonical, want) {
		t.Fatalf("canonical drift\nwant %s\n got %s", want, canonical)
	}
	first, err := DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(canonical)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DocumentHash(decoded)
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "sha256:3f7c11ff718d9e9ee262d809ccef4150d53ea72b342cbbedf36b16151b323f4d"
	alias, aliasErr := Hash(document)
	if aliasErr != nil || first != second || first != alias || first != wantHash || !strings.HasPrefix(first, "sha256:") || len(first) != 71 {
		t.Fatalf("unstable hash: first=%q second=%q", first, second)
	}
}

func TestStrictDecodeRejectsAmbiguousAndHostileWireValues(t *testing.T) {
	minimal := []byte(`{"schema_version":2,"id":"document","revision":0}`)
	invalidUTF8 := append([]byte(nil), minimal...)
	invalidUTF8[34] = 0xff // One byte inside the document ID.
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "v1 is not implicit migration", raw: readFixture(t, "document-v1.valid.json")},
		{name: "unknown member", raw: []byte(`{"schema_version":2,"id":"document","revision":0,"unknown":true}`)},
		{name: "duplicate member", raw: []byte(`{"schema_version":2,"schema_version":2,"id":"document","revision":0}`)},
		{name: "owned pointer null", raw: []byte(`{"schema_version":2,"id":"d","revision":0,"assets":[{"id":"a","kind":"video","uri":"a.mp4"}],"timelines":[{"id":"t","tracks":[{"id":"v","kind":"video","clips":[{"id":"c","asset_id":"a","timeline_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"source_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"property_set":null}]}]}]}`)},
		{name: "union has multiple values", raw: []byte(`{"schema_version":2,"id":"d","revision":0,"assets":[{"id":"a","kind":"video","uri":"a.mp4"}],"timelines":[{"id":"t","tracks":[{"id":"v","kind":"video","clips":[{"id":"c","asset_id":"a","timeline_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"source_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"keyframe_lanes":[{"id":"l","target":"opacity.value","value_type":"scalar","keyframes":[{"offset":{"value":0,"rate":1},"value":{"scalar":{"numerator":1,"denominator":1},"point":{"x":{"numerator":0,"denominator":1},"y":{"numerator":0,"denominator":1}}}}]}]}]}]}]}`)},
		{name: "fractional integer color", raw: []byte(`{"schema_version":2,"id":"d","revision":0,"assets":[{"id":"font","kind":"font","uri":"f.woff2","content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"id":"text","kind":"text","text":{"text":"x","font_asset_id":"font","font_size":{"numerator":1,"denominator":1},"fill":{"r":1.5,"g":0,"b":0,"a":65535},"language":"en","direction":"ltr","alignment":"start"}}]}`)},
		{name: "close command has ignored empty payload", raw: bytes.Replace(readFixture(t, "document-v2.canonical.json"), []byte(`{"op":"close"}`), []byte(`{"op":"close","points":[]}`), 1)},
		{name: "invalid utf8", raw: invalidUTF8},
		{name: "lone high surrogate", raw: []byte(`{"schema_version":2,"id":"\ud800","revision":0}`)},
		{name: "lone low surrogate", raw: []byte(`{"schema_version":2,"id":"\udc00","revision":0}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.raw); !errors.Is(err, ErrDocumentInvalid) {
				t.Fatalf("Decode error = %v, want ErrDocumentInvalid", err)
			}
		})
	}
	if _, err := Decode([]byte(`{"schema_version":2,"id":"\ud83d\ude00","revision":0}`)); err != nil {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
}

func TestValidateNeverPanicsOnInvalidRationalsOrTimes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
		code   string
		path   string
	}{
		{name: "font size", code: CodeAssetInvalid, path: "/assets/4/text/font_size", mutate: func(document *Document) { document.Assets[4].Text.FontSize.Denominator = 0 }},
		{name: "vector viewbox", code: CodeAssetInvalid, path: "/assets/5/vector/view_box/width", mutate: func(document *Document) { document.Assets[5].Vector.ViewBox.Width.Denominator = 0 }},
		{name: "bus gain", code: CodeAudioBusInvalid, path: "/audio_buses/0/properties/gain_linear", mutate: func(document *Document) { document.AudioBuses[0].Properties.GainLinear.Denominator = 0 }},
		{name: "property opacity", code: CodePropertyInvalid, path: "/timelines/0/tracks/0/clips/0/property_set/opacity/value", mutate: func(document *Document) {
			document.Timelines[0].Tracks[0].Clips[0].PropertySet.Opacity.Value.Denominator = 0
		}},
		{name: "keyframe value", code: CodeKeyframeInvalid, path: "/timelines/0/tracks/0/clips/0/keyframe_lanes/0/keyframes/0/value/point/x", mutate: func(document *Document) {
			document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0].Keyframes[0].Value.Point.X.Denominator = 0
		}},
		{name: "bezier", code: CodeKeyframeInvalid, path: "/timelines/0/tracks/0/clips/0/keyframe_lanes/0/keyframes/0/easing/x1", mutate: func(document *Document) {
			document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0].Keyframes[0].Easing.X1.Denominator = 0
		}},
		{name: "transition time", code: CodeTransitionInvalid, path: "/timelines/0/transitions/0/pre_duration", mutate: func(document *Document) { document.Timelines[0].Transitions[0].PreDuration.Rate = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureDocument(t)
			test.mutate(&document)
			var diagnostics []Diagnostic
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("Validate panicked: %v", recovered)
					}
				}()
				diagnostics = Validate(document)
			}()
			assertDiagnostic(t, diagnostics, test.code, test.path)
		})
	}
}

func TestTransitionAndOverlapContracts(t *testing.T) {
	t.Run("zero width is a hard cut", func(t *testing.T) {
		document := fixtureDocument(t)
		document.Timelines[0].Transitions[0].PreDuration = RationalTime{Value: 0, Rate: 1}
		document.Timelines[0].Transitions[0].PostDuration = RationalTime{Value: 0, Rate: 1}
		assertNoDiagnostics(t, Validate(document))
	})
	t.Run("disabled clip cannot transition", func(t *testing.T) {
		document := fixtureDocument(t)
		document.Timelines[0].Tracks[0].Clips[1].Disabled = true
		assertDiagnostic(t, Validate(document), CodeTransitionInvalid, "/timelines/0/transitions/0")
	})
	t.Run("static images are held without temporal handles", func(t *testing.T) {
		document := fixtureDocument(t)
		for index := 0; index < 2; index++ {
			document.Assets[index].Kind = AssetKindImage
			document.Assets[index].Duration = nil
			document.Timelines[0].Tracks[0].Clips[index].SourceRange.Start = RationalTime{Value: 0, Rate: 1}
		}
		assertNoDiagnostics(t, Validate(document))
	})
	t.Run("both windows on a middle clip cannot overlap", func(t *testing.T) {
		document := fixtureDocument(t)
		video := &document.Timelines[0].Tracks[0]
		third := video.Clips[1]
		third.ID = "clip-video-c"
		third.TimelineRange.Start = RationalTime{Value: 10, Rate: 1}
		third.SourceRange.Start = RationalTime{Value: 5, Rate: 1}
		video.Clips[1].SourceRange.Start = RationalTime{Value: 5, Rate: 1}
		video.Clips = append(video.Clips, third)
		document.Timelines[0].Transitions[0].PostDuration = RationalTime{Value: 3, Rate: 1}
		document.Timelines[0].Transitions = append(document.Timelines[0].Transitions, Transition{
			ID: "transition-second", Profile: TransitionDissolveV1,
			OutgoingClipID: video.Clips[1].ID, IncomingClipID: third.ID,
			PreDuration: RationalTime{Value: 3, Rate: 1}, PostDuration: RationalTime{Value: 1, Rate: 1},
		})
		assertDiagnostic(t, Validate(document), CodeTransitionOverlap, "/timelines/0/tracks/0/clips/1/timeline_range")
	})
	t.Run("retimed incoming head handle is scaled exactly", func(t *testing.T) {
		document := fixtureDocument(t)
		incoming := &document.Timelines[0].Tracks[0].Clips[1]
		incoming.SourceRange = testRange(1, 10) // 2x source ratio; pre=1 needs 2 seconds.
		assertDiagnostic(t, Validate(document), CodeTransitionHandleMissing, "/timelines/0/transitions/0/pre_duration")
	})
	t.Run("retimed outgoing tail handle is scaled exactly", func(t *testing.T) {
		document := fixtureDocument(t)
		outgoing := &document.Timelines[0].Tracks[0].Clips[0]
		outgoing.SourceRange = testRange(2, 10) // 2x source ratio; post=1 needs 2 seconds.
		document.Assets[0].Duration = ptrTime(RationalTime{Value: 13, Rate: 1})
		assertDiagnostic(t, Validate(document), CodeTransitionHandleMissing, "/timelines/0/transitions/0/post_duration")
	})
	t.Run("long containing clip reports every later overlap", func(t *testing.T) {
		document := fixtureDocument(t)
		video := &document.Timelines[0].Tracks[0]
		video.Clips = video.Clips[:1]
		video.Clips[0].TimelineRange.Duration = RationalTime{Value: 10, Rate: 1}
		video.Clips[0].SourceRange.Duration = RationalTime{Value: 10, Rate: 1}
		for index, start := range []int64{1, 3} {
			clip := video.Clips[0]
			clip.ID = ID("nested-" + string(rune('a'+index)))
			clip.TimelineRange = testRange(start, 1)
			clip.SourceRange = testRange(start, 1)
			clip.PropertySet = nil
			clip.KeyframeLanes = nil
			video.Clips = append(video.Clips, clip)
		}
		document.Timelines[0].Transitions = nil
		diagnostics := Validate(document)
		if got := diagnosticCount(diagnostics, CodeClipOverlap); got != 2 {
			t.Fatalf("overlap diagnostics = %d, want 2: %+v", got, diagnostics)
		}
	})
}

func TestKeyframeTrackValueAndEndpointContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Document)
	}{
		{name: "audio target on video track", mutate: func(document *Document) {
			lane := &document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0]
			lane.Target, lane.ValueType = KeyframeTargetAudioGain, KeyframeValueScalar
			one := Rational{Numerator: 1, Denominator: 1}
			for index := range lane.Keyframes {
				lane.Keyframes[index].Value = KeyframeValue{Scalar: &one}
			}
		}},
		{name: "visual target on audio track", mutate: func(document *Document) {
			lane := &document.Timelines[0].Tracks[1].Clips[0].KeyframeLanes[0]
			lane.Target, lane.ValueType = KeyframeTargetOpacity, KeyframeValueScalar
		}},
		{name: "opacity out of range", mutate: func(document *Document) {
			lane := &document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0]
			lane.Target, lane.ValueType = KeyframeTargetOpacity, KeyframeValueScalar
			two := Rational{Numerator: 2, Denominator: 1}
			for index := range lane.Keyframes {
				lane.Keyframes[index].Value = KeyframeValue{Scalar: &two}
			}
		}},
		{name: "last interpolation is forbidden", mutate: func(document *Document) {
			lane := &document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0]
			lane.Keyframes[len(lane.Keyframes)-1].Interpolation = InterpolationLinear
		}},
		{name: "duplicate offset", mutate: func(document *Document) {
			lane := &document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0]
			lane.Keyframes[1].Offset = lane.Keyframes[0].Offset
		}},
		{name: "text color interpolation is hold only", mutate: func(document *Document) {
			document.Timelines[0].Tracks[2].Clips[0].KeyframeLanes[0].Keyframes[0].Interpolation = InterpolationLinear
		}},
		{name: "cubic y overshoot needs a future profile", mutate: func(document *Document) {
			document.Timelines[0].Tracks[0].Clips[0].KeyframeLanes[0].Keyframes[0].Easing.Y1 = Rational{Numerator: 2, Denominator: 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureDocument(t)
			test.mutate(&document)
			if diagnostics := Validate(document); diagnosticCount(diagnostics, CodeKeyframeInvalid) == 0 {
				t.Fatalf("missing keyframe diagnostic: %+v", diagnostics)
			}
		})
	}
	t.Run("zero scale is a deterministic degenerate surface", func(t *testing.T) {
		document := fixtureDocument(t)
		document.Timelines[0].Tracks[0].Clips[0].PropertySet.Transform.Scale.X = Rational{Numerator: 0, Denominator: 1}
		assertNoDiagnostics(t, Validate(document))
	})
}

func TestAudioBusVectorAndExtensionFailClosedContracts(t *testing.T) {
	t.Run("audio bus cycle", func(t *testing.T) {
		document := fixtureDocument(t)
		document.AudioBuses[0].Kind = AudioBusGroup
		document.AudioBuses[0].ParentBusID = document.AudioBuses[1].ID
		document.AudioBuses[1].ParentBusID = document.AudioBuses[0].ID
		assertDiagnostic(t, Validate(document), CodeAudioBusCycle, "/audio_buses/0/parent_bus_id")
	})
	t.Run("vector command requires open subpath", func(t *testing.T) {
		document := fixtureDocument(t)
		document.Assets[5].Vector.Paths[0].Commands[0].Op = VectorLineTo
		assertDiagnostic(t, Validate(document), CodeAssetInvalid, "/assets/5/vector/paths/0/commands/0/op")
	})
	for _, namespace := range []string{
		PropertyOpacityV1, string(CapabilityTextV1), VectorProfilePathViewportPXV2, EvaluatorVectorPathViewportPXV2, VectorProfilePathV1,
		TransitionDissolveV1, string(CapabilityInterpolationLinearV1), InterpolationNumericProfileV1,
		AudioGraphProfileV1, EvaluatorAudioGraphV1, PropertyAudioChannelMatrixV1,
		EvaluatorAudioChannelMatrixV1, PropertyAudioSamplePeakLimiterV1, EvaluatorAudioSamplePeakLimiterV1,
	} {
		t.Run("reserved extension "+namespace+" cannot create dual truth", func(t *testing.T) {
			document := fixtureDocument(t)
			extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
			extension.Namespace = namespace
			assertDiagnostic(t, Validate(document), CodePropertyInvalid, "/timelines/0/tracks/0/clips/0/property_set/extensions/0/namespace")
		})
	}
	t.Run("rendering extension requires canonical capability", func(t *testing.T) {
		document := fixtureDocument(t)
		extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
		extension.AffectsRendering = true
		extension.RequiredCapability = "not versioned"
		assertDiagnostic(t, Validate(document), CodePropertyInvalid, "/timelines/0/tracks/0/clips/0/property_set/extensions/0/required_capability")
	})
	for _, capability := range []videoedit.CapabilityID{
		CapabilityOpacityV1,
		CapabilityAudioChannelMatrixV1,
		CapabilityAudioSamplePeakLimiterV1,
		videoedit.CapabilityVideoLinearRetime,
		videoedit.CapabilityAudioVarispeed,
		videoedit.CapabilityAudioTimeStretch,
	} {
		t.Run("rendering extension cannot piggyback on "+string(capability), func(t *testing.T) {
			document := fixtureDocument(t)
			extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
			extension.Namespace = "vendor.sparkle.v1"
			extension.AffectsRendering = true
			extension.RequiredCapability = string(capability)
			diagnostics := PreflightCapabilities(document, videoedit.BackendCapabilities{Features: []videoedit.CapabilityID{capability}})
			assertDiagnostic(t, diagnostics, CodePropertyInvalid, "/timelines/0/tracks/0/clips/0/property_set/extensions/0/required_capability")
		})
	}
	t.Run("legacy property capability cannot piggyback on a vendor namespace", func(t *testing.T) {
		document := fixtureDocument(t)
		extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
		extension.Namespace = "vendor.sparkle.v1"
		extension.AffectsRendering = true
		extension.RequiredCapability = LegacyPropertyCapabilityV1
		extension.Value = []byte(`{"legacy_key":"old.opacity","value":{"amount":1}}`)
		assertDiagnostic(t, Validate(document), CodePropertyInvalid, "/timelines/0/tracks/0/clips/0/property_set/extensions/0/namespace")
	})
	t.Run("legacy property namespace requires its owned capability", func(t *testing.T) {
		document := fixtureDocument(t)
		extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
		extension.Namespace = legacyPropertyNamespace("old.opacity")
		extension.AffectsRendering = true
		extension.RequiredCapability = "vendor.effect.v1"
		extension.Value = []byte(`{"legacy_key":"old.opacity","value":{"amount":1}}`)
		assertDiagnostic(t, Validate(document), CodePropertyInvalid, "/timelines/0/tracks/0/clips/0/property_set/extensions/0/required_capability")
	})
	for _, test := range []struct {
		name      string
		namespace string
		value     []byte
		code      string
		path      string
	}{
		{name: "valid strict envelope", namespace: legacyPropertyNamespace("old.opacity"), value: []byte(`{"legacy_key":"old.opacity","value":{"amount":1}}`)},
		{name: "empty legacy key remains representable", namespace: legacyPropertyNamespace(""), value: []byte(`{"legacy_key":"","value":1}`)},
		{name: "null legacy value remains representable", namespace: legacyPropertyNamespace("old.opacity"), value: []byte(`{"legacy_key":"old.opacity","value":null}`)},
		{name: "digest mismatch", namespace: legacyPropertyNamespace("different.key"), value: []byte(`{"legacy_key":"old.opacity","value":{"amount":1}}`), code: CodePropertyInvalid, path: "/value/legacy_key"},
		{name: "extra envelope member", namespace: legacyPropertyNamespace("old.opacity"), value: []byte(`{"legacy_key":"old.opacity","value":1,"extra":true}`), code: CodePropertyInvalid, path: "/value"},
		{name: "duplicate nested member", namespace: legacyPropertyNamespace("old.opacity"), value: []byte(`{"legacy_key":"old.opacity","value":{"x":1,"x":2}}`), code: CodeJSONInvalid, path: "/value"},
	} {
		t.Run("legacy envelope "+test.name, func(t *testing.T) {
			document := fixtureDocument(t)
			extension := &document.Timelines[0].Tracks[0].Clips[0].PropertySet.Extensions[0]
			extension.Namespace = test.namespace
			extension.AffectsRendering = true
			extension.RequiredCapability = LegacyPropertyCapabilityV1
			extension.Value = test.value
			diagnostics := Validate(document)
			if test.code == "" {
				assertNoDiagnostics(t, diagnostics)
				return
			}
			assertDiagnostic(t, diagnostics, test.code, "/timelines/0/tracks/0/clips/0/property_set/extensions/0"+test.path)
		})
	}
	if !capabilityTokenPattern.MatchString("cutly.opacity.v1") || capabilityTokenPattern.MatchString(`cutly.opacity\xv1`) {
		t.Fatal("canonical capability token regexp accepts the wrong language")
	}
}

func TestTypedAudioChannelMatrixAndSamplePeakLimiterContracts(t *testing.T) {
	validMatrix := func() *AudioChannelMatrixProperty {
		return &AudioChannelMatrixProperty{
			SourceLayout:   "5.1(side)",
			SourceChannels: []string{"FL", "FR", "FC", "LFE", "SL", "SR"},
			LeftCoefficients: []Rational{
				rationalOne(), rationalZero(), rationalHalf(), rationalZero(), rationalHalf(), rationalZero(),
			},
			RightCoefficients: []Rational{
				rationalZero(), rationalOne(), rationalHalf(), rationalZero(), rationalZero(), rationalHalf(),
			},
		}
	}
	validDocument := func(t *testing.T) Document {
		t.Helper()
		document := fixtureDocument(t)
		document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix = validMatrix()
		document.AudioBuses[0].Properties.SamplePeakLimiter = &AudioSamplePeakLimiterProperty{CeilingLinear: Rational{Numerator: 19, Denominator: 20}}
		return document
	}
	t.Run("valid explicit stages", func(t *testing.T) {
		assertNoDiagnostics(t, Validate(validDocument(t)))
	})
	for _, test := range []struct {
		name   string
		path   string
		mutate func(*Document)
	}{
		{name: "matrix only on audio clip", path: "/timelines/0/tracks/0/clips/0/property_set/audio_channel_matrix", mutate: func(document *Document) {
			document.Timelines[0].Tracks[0].Clips[0].PropertySet.AudioChannelMatrix = validMatrix()
		}},
		{name: "layout must be trimmed", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/source_layout", mutate: func(document *Document) {
			document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.SourceLayout = " 5.1(side)"
		}},
		{name: "coefficient arity", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/left_coefficients", mutate: func(document *Document) {
			document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.LeftCoefficients = document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.LeftCoefficients[:5]
		}},
		{name: "duplicate source channel", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/source_channels/1", mutate: func(document *Document) {
			document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.SourceChannels[1] = "FL"
		}},
		{name: "canonical source channel", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/source_channels/0", mutate: func(document *Document) {
			document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.SourceChannels[0] = "front-left"
		}},
		{name: "canonical coefficient", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/right_coefficients/0", mutate: func(document *Document) {
			document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix.RightCoefficients[0].Denominator = 0
		}},
		{name: "bounded input channels", path: "/timelines/0/tracks/1/clips/0/property_set/audio_channel_matrix/source_channels", mutate: func(document *Document) {
			matrix := document.Timelines[0].Tracks[1].Clips[0].PropertySet.AudioChannelMatrix
			matrix.SourceChannels = make([]string, AudioChannelMatrixMaxInputs+1)
			matrix.LeftCoefficients = make([]Rational, AudioChannelMatrixMaxInputs+1)
			matrix.RightCoefficients = make([]Rational, AudioChannelMatrixMaxInputs+1)
			for index := range matrix.SourceChannels {
				matrix.SourceChannels[index] = fmt.Sprintf("C%d", index)
				matrix.LeftCoefficients[index] = rationalZero()
				matrix.RightCoefficients[index] = rationalZero()
			}
		}},
		{name: "limiter only on master", path: "/audio_buses/1/properties/sample_peak_limiter", mutate: func(document *Document) {
			document.AudioBuses[1].Properties.SamplePeakLimiter = &AudioSamplePeakLimiterProperty{CeilingLinear: rationalHalf()}
		}},
		{name: "positive limiter ceiling", path: "/audio_buses/0/properties/sample_peak_limiter/ceiling_linear", mutate: func(document *Document) {
			document.AudioBuses[0].Properties.SamplePeakLimiter.CeilingLinear = rationalZero()
		}},
		{name: "unit limiter ceiling", path: "/audio_buses/0/properties/sample_peak_limiter/ceiling_linear", mutate: func(document *Document) {
			document.AudioBuses[0].Properties.SamplePeakLimiter.CeilingLinear = Rational{Numerator: 2, Denominator: 1}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := validDocument(t)
			test.mutate(&document)
			assertDiagnostic(t, Validate(document), map[bool]string{true: CodeAudioBusInvalid, false: CodePropertyInvalid}[strings.HasPrefix(test.path, "/audio_buses/")], test.path)
		})
	}
}

func TestProgrammaticRawJSONAndUTF8CannotCollapseCanonicalIdentity(t *testing.T) {
	document := fixtureDocument(t)
	document.Metadata["bad"] = []byte(`{"x":1,"x":2}`)
	assertDiagnostic(t, Validate(document), CodeJSONInvalid, "/metadata/bad")

	document = fixtureDocument(t)
	document.ID = ID(string([]byte{'b', 0xff, 'd'}))
	assertDiagnostic(t, Validate(document), CodeJSONInvalid, "/id")

	document = fixtureDocument(t)
	document.Metadata["surrogate"] = []byte(`"\ud800"`)
	assertDiagnostic(t, Validate(document), CodeJSONInvalid, "/metadata/surrogate")

	document = fixtureDocument(t)
	document.Metadata[string([]byte{0xff})] = []byte(`true`)
	assertDiagnostic(t, Validate(document), CodeJSONInvalid, "/metadata/<invalid-utf8-key>")
}

func TestTextRejectsHostDependentControlCharacters(t *testing.T) {
	document := fixtureDocument(t)
	document.Assets[4].Text.Text = "line one\rline two"
	assertDiagnostic(t, Validate(document), CodeAssetInvalid, "/assets/4/text/text")
}

func TestStaticSourceRangeIsIdentityButTemporalRangeIsBounded(t *testing.T) {
	t.Run("static duration does not bound source identity", func(t *testing.T) {
		document := fixtureDocument(t)
		for index := 0; index < 2; index++ {
			document.Assets[index].Kind = AssetKindImage
			document.Assets[index].Duration = ptrTime(RationalTime{Value: 1, Rate: 1})
		}
		document.Assets[4].Duration = ptrTime(RationalTime{Value: 1, Rate: 1})
		document.Assets[5].Duration = ptrTime(RationalTime{Value: 1, Rate: 1})
		assertNoDiagnostics(t, Validate(document))
	})
	for _, test := range []struct {
		name       string
		assetIndex int
		path       string
	}{
		{name: "video", assetIndex: 0, path: "/timelines/0/tracks/0/clips/0/source_range"},
		{name: "audio", assetIndex: 2, path: "/timelines/0/tracks/1/clips/0/source_range"},
	} {
		t.Run(test.name+" duration bounds source", func(t *testing.T) {
			document := fixtureDocument(t)
			document.Assets[test.assetIndex].Duration = ptrTime(RationalTime{Value: 1, Rate: 1})
			assertDiagnostic(t, Validate(document), CodeClipInvalid, test.path)
		})
	}
}

func FuzzDecodeNeverPanics(f *testing.F) {
	f.Add([]byte(`{"schema_version":2,"id":"fuzz","revision":0}`))
	f.Add([]byte(`{"schema_version":2,"id":"\ud800","revision":0}`))
	f.Add([]byte{0xff, '{', '}'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Decode(raw)
	})
}

func fixtureDocument(t *testing.T) Document {
	t.Helper()
	document, err := Decode(readFixture(t, "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate fixture")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "conformance", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testRange(start, duration int64) TimeRange {
	return TimeRange{Start: RationalTime{Value: start, Rate: 1}, Duration: RationalTime{Value: duration, Rate: 1}}
}

func ptrTime(value RationalTime) *RationalTime { return &value }

func assertNoDiagnostics(t *testing.T, diagnostics []Diagnostic) {
	t.Helper()
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
}

func assertDiagnostic(t *testing.T, diagnostics []Diagnostic, code, path string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && diagnostic.Path == path {
			return
		}
	}
	t.Fatalf("missing diagnostic %s at %s: %+v", code, path, diagnostics)
}

func diagnosticCount(diagnostics []Diagnostic, code string) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			count++
		}
	}
	return count
}
