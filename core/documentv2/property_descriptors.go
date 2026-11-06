package documentv2

import (
	"encoding/json"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

// PropertyMigrationPolicy states how a typed v2 property may be populated from an older
// document. M2 deliberately forbids shape-based guesses: legacy raw JSON remains a capability-
// gated extension until a separately reviewed migration recognizes it explicitly.
type PropertyMigrationPolicy string

const (
	PropertyMigrationExplicitOnly PropertyMigrationPolicy = "explicit_only"
)

const (
	EvaluatorTransformV1              = "cutly.evaluator.transform.v1"
	EvaluatorOpacityV1                = "cutly.evaluator.opacity.v1"
	EvaluatorCropV1                   = "cutly.evaluator.crop.v1"
	EvaluatorAudioGainV1              = "cutly.evaluator.audio-gain.v1"
	EvaluatorAudioPanV1               = "cutly.evaluator.audio-pan.v1"
	EvaluatorAudioRouteV1             = "cutly.evaluator.audio-route.v1"
	EvaluatorAudioChannelMatrixV1     = "cutly.evaluator.audio-channel-matrix-stereo.v1"
	EvaluatorAudioSamplePeakLimiterV1 = "cutly.evaluator.audio-limiter-sample-peak-instant.v1"
	// KeyframeEvaluationEndpointClampV1 evaluates lanes in clip-local time. Before the first
	// keyframe it returns the first value; at/after the last it returns the last value. A
	// property without a lane uses its static value or the descriptor default.
	KeyframeEvaluationEndpointClampV1 = "cutly.evaluator.keyframe-endpoint-clamp.v1"
)

const keyframeEndpointClampDescription = "Evaluate offsets in clip-local time; clamp before the first keyframe to its value and at/after the last to its value. Hold returns the left value on each half-open keyframe interval. Linear interpolation of scalar, point and rect values uses exact rational progress and exact per-component rational lerp. For cubic-bezier, first compare exact x at the maximum interior Q0.32 point k=2^32-1; if x<p select u=1, otherwise run fixed 32-step lower-bound integer bisection over k in [0,2^32-1]. Evaluate cubic x/y with big-rational arithmetic, then exact per-component lerp by y(u); host float/CSS easing is forbidden. text.fill color lanes allow hold only. Without a lane, use the static property or descriptor default."

// PropertyBindingDescriptor locates one wire representation of a semantic property. Gain and pan
// deliberately have both a keyframeable clip binding and a static audio-bus binding; keeping both
// here prevents schema discovery from treating the bus graph as hidden evaluator state.
type PropertyBindingDescriptor struct {
	DocumentPath       string                 `json:"document_path"`
	ValueType          string                 `json:"value_type"`
	DefaultValue       json.RawMessage        `json:"default_value"`
	Keyframeable       bool                   `json:"keyframeable"`
	KeyframeTargets    []KeyframeTarget       `json:"keyframe_targets,omitempty"`
	KeyframeProfile    string                 `json:"keyframe_profile,omitempty"`
	KeyframeCapability videoedit.CapabilityID `json:"keyframe_capability,omitempty"`
	KeyframeSemantics  string                 `json:"keyframe_semantics"`
}

// PropertyDescriptor is the stable discovery contract used by schema tooling, UI inspectors and
// agents. Binding defaults are canonical JSON for the exact wire value at each DocumentPath. A
// descriptor advertises evaluator semantics; it does not imply backend implementation.
type PropertyDescriptor struct {
	Namespace            string                      `json:"namespace"`
	Bindings             []PropertyBindingDescriptor `json:"bindings"`
	ValueSemantics       string                      `json:"value_semantics"`
	EvaluatorProfile     string                      `json:"evaluator_profile"`
	RequiredCapability   videoedit.CapabilityID      `json:"required_capability"`
	MigrationPolicy      PropertyMigrationPolicy     `json:"migration_policy"`
	MigrationDescription string                      `json:"migration_description"`
	AgentDescription     string                      `json:"agent_description"`
}

type audioRouteDefault struct {
	AudioBusID ID `json:"audio_bus_id"`
}

var propertyDescriptorRegistry = []PropertyDescriptor{
	{
		Namespace: PropertyAudioChannelMatrixV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "clip.property_set.audio_channel_matrix", ValueType: "audio_channel_matrix",
			DefaultValue:      mustDescriptorDefault((*AudioChannelMatrixProperty)(nil)),
			KeyframeSemantics: "Channel matrices are static for the complete clip and cannot be keyframed in v1.",
		}},
		ValueSemantics:   "Before clip gain and pan, media preflight must match source_layout exactly to ProbeManifest audio.channel_layout, match len(source_channels) to ProbeManifest audio.channels, and prove that the selected decoder exposes its input channels in exactly the authored source_channels order. No layout expansion, channel inference, or reordering is implicit. Emit stereo with L=sum(input[i]*left_coefficients[i]) and R=sum(input[i]*right_coefficients[i]) using arbitrary-precision rational intermediates. Results may exceed nominal [-1,1] full scale. Apply no implicit normalization, clipping, fallback layout, or rounding; only an explicitly authored downstream limiter or the separately versioned final PCM quantization profile may handle out-of-range values. When omitted, cutly.audio.graph.v1 keeps its mono/stereo-only input rule.",
		EvaluatorProfile: EvaluatorAudioChannelMatrixV1, RequiredCapability: CapabilityAudioChannelMatrixV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Document v1 has no typed channel matrix; an identically named raw legacy property remains in the capability-gated legacy envelope until explicitly migrated.",
		AgentDescription:     "Exact, probe-bound source-channel matrix to stereo; use it for layouts that the base mono/stereo graph cannot consume.",
	},
	{
		Namespace: PropertyAudioGainV1,
		Bindings: []PropertyBindingDescriptor{
			{
				DocumentPath: "audio_bus.properties.gain_linear", ValueType: "rational",
				DefaultValue:      mustDescriptorDefault(rationalOne()),
				KeyframeSemantics: "Audio-bus gain is static for the complete bus and cannot be keyframed in v1.",
			},
			{
				DocumentPath: "clip.property_set.audio_gain", ValueType: "audio_gain",
				DefaultValue: mustDescriptorDefault(AudioGainProperty{Linear: rationalOne()}),
				Keyframeable: true, KeyframeTargets: []KeyframeTarget{KeyframeTargetAudioGain},
				KeyframeCapability: CapabilityKeyframesV1,
				KeyframeProfile:    KeyframeEvaluationEndpointClampV1, KeyframeSemantics: keyframeEndpointClampDescription,
			},
		},
		ValueSemantics:   "Linear amplitude multiplier; values are exact, non-negative ratios and 1/1 is unity. Placement in the mix pipeline and the prohibition on hidden normalization are defined once by cutly.audio.graph.v1.",
		EvaluatorProfile: EvaluatorAudioGainV1, RequiredCapability: CapabilityAudioGainV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Migrate only the exact cutly.audio.gain.v1 legacy key with a JSON number or strict {value:number}; malformed known values fail migration.",
		AgentDescription:     "Linear clip gain where 1/1 is unity and 0/1 is silence.",
	},
	{
		Namespace: PropertyAudioSamplePeakLimiterV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "audio_bus.properties.sample_peak_limiter", ValueType: "audio_sample_peak_limiter",
			DefaultValue:      mustDescriptorDefault((*AudioSamplePeakLimiterProperty)(nil)),
			KeyframeSemantics: "The master sample-peak limiter is static for the complete render and cannot be keyframed in v1.",
		}},
		ValueSemantics:   "After master gain and pan, process each exact stereo sample in nominal full-scale units; the input may exceed [-1,1]. With linked peak=max(abs(L),abs(R)), silence uses gain 1 and other samples use gain=min(1,ceiling_linear/peak). Multiply both channels by the same exact gain. The stage is instantaneous and stateless with zero lookahead and zero latency; it has no attack/release, oversampling, inter-sample or true-peak claim, hidden makeup gain, saturation, or internal rounding. Final PCM quantization belongs to a separately versioned output profile. When omitted, no limiter is applied.",
		EvaluatorProfile: EvaluatorAudioSamplePeakLimiterV1, RequiredCapability: CapabilityAudioSamplePeakLimiterV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Document v1 has no typed master limiter; raw legacy properties remain capability-gated extensions.",
		AgentDescription:     "Explicit zero-latency stereo-linked sample-peak limiter; not a true-peak limiter.",
	},
	{
		Namespace: PropertyAudioPanV1,
		Bindings: []PropertyBindingDescriptor{
			{
				DocumentPath: "audio_bus.properties.pan", ValueType: "rational",
				DefaultValue:      mustDescriptorDefault(rationalZero()),
				KeyframeSemantics: "Audio-bus pan is static for the complete bus and cannot be keyframed in v1.",
			},
			{
				DocumentPath: "clip.property_set.audio_pan", ValueType: "audio_pan",
				DefaultValue: mustDescriptorDefault(AudioPanProperty{Value: rationalZero()}),
				Keyframeable: true, KeyframeTargets: []KeyframeTarget{KeyframeTargetAudioPan},
				KeyframeCapability: CapabilityKeyframesV1,
				KeyframeProfile:    KeyframeEvaluationEndpointClampV1, KeyframeSemantics: keyframeEndpointClampDescription,
			},
		},
		ValueSemantics:   "Exact pan p in [-1/1,1/1], with -1 left, 0 center and 1 right. The single authoritative linear-balance law, mono/stereo handling, unsupported layouts and pipeline order are defined by cutly.audio.graph.v1.",
		EvaluatorProfile: EvaluatorAudioPanV1, RequiredCapability: CapabilityAudioPanV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Migrate only the exact cutly.audio.pan.v1 legacy key with a JSON number or strict {value:number}; malformed known values fail migration.",
		AgentDescription:     "Exact mono/stereo linear-balance pan; use a future separately versioned profile for equal-power or surround panning.",
	},
	{
		Namespace: PropertyAudioRouteV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "track.audio_bus_id", ValueType: "audio_route",
			DefaultValue:      mustDescriptorDefault(audioRouteDefault{}),
			KeyframeSemantics: "Routing is static for the complete track and cannot be keyframed.",
		}},
		ValueSemantics:   "One exact track-to-bus edge; an empty ID denotes the legacy direct-master graph only. The authoritative topology, routing and summation contract is cutly.audio.graph.v1.",
		EvaluatorProfile: EvaluatorAudioRouteV1, RequiredCapability: CapabilityAudioRouteV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "A v1 audio track migrates to the implicit direct-master graph; an explicit bus graph must be authored in v2.",
		AgentDescription:     "Routes an audio track to one declared bus; an empty ID is valid only for the legacy direct-master graph.",
	},
	{
		Namespace: PropertyCropV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "clip.property_set.crop", ValueType: "crop",
			DefaultValue: mustDescriptorDefault(CropProperty{Rect: Rect{
				X: rationalZero(), Y: rationalZero(), Width: rationalOne(), Height: rationalOne(),
			}}),
			Keyframeable: true, KeyframeTargets: []KeyframeTarget{KeyframeTargetCrop},
			KeyframeCapability: CapabilityKeyframesV1,
			KeyframeProfile:    KeyframeEvaluationEndpointClampV1, KeyframeSemantics: keyframeEndpointClampDescription,
		}},
		ValueSemantics:   "Normalized full decoded source-surface rectangle: x/y are the top-left origin and width/height stay inside [0,1]². Crop is evaluated before transform; 0/1,0/1,1/1,1/1 is identity and no out-of-bounds sampling is implied.",
		EvaluatorProfile: EvaluatorCropV1, RequiredCapability: CapabilityCropV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Migrate only the exact cutly.crop.v1 legacy key with a strict numeric {rect:{x,y,width,height}} value; malformed known values fail migration.",
		AgentDescription:     "Normalized source crop; the default rectangle keeps the complete source surface.",
	},
	{
		Namespace: PropertyOpacityV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "clip.property_set.opacity", ValueType: "opacity",
			DefaultValue: mustDescriptorDefault(OpacityProperty{Value: rationalOne()}),
			Keyframeable: true, KeyframeTargets: []KeyframeTarget{KeyframeTargetOpacity},
			KeyframeCapability: CapabilityKeyframesV1,
			KeyframeProfile:    KeyframeEvaluationEndpointClampV1, KeyframeSemantics: keyframeEndpointClampDescription,
		}},
		ValueSemantics:   "Unpremultiplied opacity in [0/1,1/1]; 1/1 is visually unchanged and 0/1 is transparent. Evaluate it after crop/transform by multiplying premultiplied linear-light RGB and alpha immediately before layer compositing.",
		EvaluatorProfile: EvaluatorOpacityV1, RequiredCapability: CapabilityOpacityV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Migrate only the exact cutly.opacity.v1 legacy key with a JSON number or strict {value:number}; malformed known values fail migration.",
		AgentDescription:     "Unpremultiplied visual opacity in the closed range 0/1 through 1/1.",
	},
	{
		Namespace: PropertyTransformV1,
		Bindings: []PropertyBindingDescriptor{{
			DocumentPath: "clip.property_set.transform", ValueType: "transform",
			DefaultValue: mustDescriptorDefault(TransformProperty{
				Position: Point{X: rationalZero(), Y: rationalZero()},
				Scale:    Point{X: rationalOne(), Y: rationalOne()}, RotationDegrees: rationalZero(),
				Anchor: Point{X: rationalHalf(), Y: rationalHalf()},
			}),
			Keyframeable: true,
			KeyframeTargets: []KeyframeTarget{
				KeyframeTargetTransformAnchor, KeyframeTargetTransformPosition,
				KeyframeTargetTransformRotation, KeyframeTargetTransformScale,
			},
			KeyframeCapability: CapabilityKeyframesV1,
			KeyframeProfile:    KeyframeEvaluationEndpointClampV1, KeyframeSemantics: keyframeEndpointClampDescription,
		}},
		ValueSemantics:   "For source pixel point p and anchorPixels=(anchor.x*sourceWidth,anchor.y*sourceHeight), evaluate p'=position+anchorPixels+R_clockwise(rotation)*scale*(p-anchorPixels). Position is translation in output-surface square pixels (+x right, +y down); anchor is normalized full-source space; scale is a signed unitless ratio (negative mirrors); rotation is clockwise degrees in the y-down output plane. Crop is selected first, but anchor remains relative to the full source. Identity is position 0, scale 1, rotation 0, anchor 1/2.",
		EvaluatorProfile: EvaluatorTransformV1, RequiredCapability: CapabilityTransformV1,
		MigrationPolicy:      PropertyMigrationExplicitOnly,
		MigrationDescription: "Migrate only the exact cutly.transform.v1 legacy key with strict numeric position, scale, rotation_degrees, and anchor members; malformed known values fail migration.",
		AgentDescription:     "Exact 2D position, scale, clockwise rotation in degrees, and normalized anchor; defaults to identity around center.",
	},
}

// PropertyDescriptors returns a canonical namespace-sorted defensive copy of the registry.
func PropertyDescriptors() []PropertyDescriptor {
	result := make([]PropertyDescriptor, len(propertyDescriptorRegistry))
	for index, descriptor := range propertyDescriptorRegistry {
		result[index] = clonePropertyDescriptor(descriptor)
	}
	return result
}

// LookupPropertyDescriptor returns a defensive copy so callers cannot mutate the registry.
func LookupPropertyDescriptor(namespace string) (PropertyDescriptor, bool) {
	for _, descriptor := range propertyDescriptorRegistry {
		if descriptor.Namespace == namespace {
			return clonePropertyDescriptor(descriptor), true
		}
	}
	return PropertyDescriptor{}, false
}

func clonePropertyDescriptor(value PropertyDescriptor) PropertyDescriptor {
	result := value
	result.Bindings = make([]PropertyBindingDescriptor, len(value.Bindings))
	for index, binding := range value.Bindings {
		result.Bindings[index] = binding
		result.Bindings[index].DefaultValue = append(json.RawMessage(nil), binding.DefaultValue...)
		result.Bindings[index].KeyframeTargets = append([]KeyframeTarget(nil), binding.KeyframeTargets...)
	}
	return result
}

func mustDescriptorDefault(value any) json.RawMessage {
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		panic("documentv2: invalid property descriptor default: " + err.Error())
	}
	return encoded
}

func rationalZero() Rational { return Rational{Numerator: 0, Denominator: 1} }
func rationalHalf() Rational { return Rational{Numerator: 1, Denominator: 2} }
func rationalOne() Rational  { return Rational{Numerator: 1, Denominator: 1} }
