package documentv2

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestPropertyDescriptorsAreCompleteCanonicalAndDiscoverAudioBusBindings(t *testing.T) {
	descriptors := PropertyDescriptors()
	wantNamespaces := []string{
		PropertyAudioChannelMatrixV1,
		PropertyAudioGainV1,
		PropertyAudioSamplePeakLimiterV1,
		PropertyAudioPanV1,
		PropertyAudioRouteV1,
		PropertyCropV1,
		PropertyOpacityV1,
		PropertyTransformV1,
	}
	wantPaths := map[string][]string{
		PropertyAudioChannelMatrixV1:     {"clip.property_set.audio_channel_matrix"},
		PropertyAudioGainV1:              {"audio_bus.properties.gain_linear", "clip.property_set.audio_gain"},
		PropertyAudioSamplePeakLimiterV1: {"audio_bus.properties.sample_peak_limiter"},
		PropertyAudioPanV1:               {"audio_bus.properties.pan", "clip.property_set.audio_pan"},
		PropertyAudioRouteV1:             {"track.audio_bus_id"},
		PropertyCropV1:                   {"clip.property_set.crop"},
		PropertyOpacityV1:                {"clip.property_set.opacity"},
		PropertyTransformV1:              {"clip.property_set.transform"},
	}
	if len(descriptors) != len(wantNamespaces) {
		t.Fatalf("descriptor count = %d, want %d", len(descriptors), len(wantNamespaces))
	}
	for index, descriptor := range descriptors {
		if descriptor.Namespace != wantNamespaces[index] {
			t.Errorf("descriptor[%d].namespace = %q, want %q", index, descriptor.Namespace, wantNamespaces[index])
		}
		if descriptor.RequiredCapability != videoedit.CapabilityID(descriptor.Namespace) {
			t.Errorf("%s capability = %q", descriptor.Namespace, descriptor.RequiredCapability)
		}
		if !capabilityTokenPattern.MatchString(descriptor.EvaluatorProfile) || descriptor.ValueSemantics == "" ||
			descriptor.MigrationPolicy == "" || descriptor.MigrationDescription == "" || descriptor.AgentDescription == "" {
			t.Errorf("incomplete descriptor: %+v", descriptor)
		}
		paths := make([]string, 0, len(descriptor.Bindings))
		for _, binding := range descriptor.Bindings {
			paths = append(paths, binding.DocumentPath)
			if binding.DocumentPath == "" || binding.ValueType == "" || len(binding.DefaultValue) == 0 || binding.KeyframeSemantics == "" {
				t.Errorf("incomplete %s binding: %+v", descriptor.Namespace, binding)
			}
			if !json.Valid(binding.DefaultValue) {
				t.Errorf("invalid default for %s/%s: %s", descriptor.Namespace, binding.DocumentPath, binding.DefaultValue)
			}
			canonical, err := videoedit.CanonicalJSON(binding.DefaultValue)
			if err != nil {
				t.Errorf("canonical default for %s/%s: %v", descriptor.Namespace, binding.DocumentPath, err)
			} else if !bytes.Equal(canonical, binding.DefaultValue) {
				t.Errorf("non-canonical default for %s/%s: %s", descriptor.Namespace, binding.DocumentPath, binding.DefaultValue)
			}
			if binding.Keyframeable {
				if binding.KeyframeProfile != KeyframeEvaluationEndpointClampV1 || binding.KeyframeCapability != CapabilityKeyframesV1 || len(binding.KeyframeTargets) == 0 {
					t.Errorf("keyframe contract incomplete for %s/%s: %+v", descriptor.Namespace, binding.DocumentPath, binding)
				}
				for _, target := range binding.KeyframeTargets {
					if capabilityForKeyframeTarget(target) != descriptor.RequiredCapability {
						t.Errorf("target %q maps to %q, descriptor requires %q", target, capabilityForKeyframeTarget(target), descriptor.RequiredCapability)
					}
				}
			} else if len(binding.KeyframeTargets) != 0 || binding.KeyframeProfile != "" || binding.KeyframeCapability != "" {
				t.Errorf("static binding exposes keyframe metadata: %+v", binding)
			}
		}
		if !slices.Equal(paths, wantPaths[descriptor.Namespace]) {
			t.Errorf("%s paths = %q, want %q", descriptor.Namespace, paths, wantPaths[descriptor.Namespace])
		}
	}
}

func TestPropertyDescriptorDefaultsFreezeIdentityValues(t *testing.T) {
	wantDefaults := map[string]map[string]string{
		PropertyAudioChannelMatrixV1: {
			"clip.property_set.audio_channel_matrix": `null`,
		},
		PropertyAudioGainV1: {
			"audio_bus.properties.gain_linear": `{"denominator":1,"numerator":1}`,
			"clip.property_set.audio_gain":     `{"linear":{"denominator":1,"numerator":1}}`,
		},
		PropertyAudioSamplePeakLimiterV1: {
			"audio_bus.properties.sample_peak_limiter": `null`,
		},
		PropertyAudioPanV1: {
			"audio_bus.properties.pan":    `{"denominator":1,"numerator":0}`,
			"clip.property_set.audio_pan": `{"value":{"denominator":1,"numerator":0}}`,
		},
		PropertyAudioRouteV1: {
			"track.audio_bus_id": `{"audio_bus_id":""}`,
		},
		PropertyCropV1: {
			"clip.property_set.crop": `{"rect":{"height":{"denominator":1,"numerator":1},"width":{"denominator":1,"numerator":1},"x":{"denominator":1,"numerator":0},"y":{"denominator":1,"numerator":0}}}`,
		},
		PropertyOpacityV1: {
			"clip.property_set.opacity": `{"value":{"denominator":1,"numerator":1}}`,
		},
		PropertyTransformV1: {
			"clip.property_set.transform": `{"anchor":{"x":{"denominator":2,"numerator":1},"y":{"denominator":2,"numerator":1}},"position":{"x":{"denominator":1,"numerator":0},"y":{"denominator":1,"numerator":0}},"rotation_degrees":{"denominator":1,"numerator":0},"scale":{"x":{"denominator":1,"numerator":1},"y":{"denominator":1,"numerator":1}}}`,
		},
	}
	for _, descriptor := range PropertyDescriptors() {
		for _, binding := range descriptor.Bindings {
			want := wantDefaults[descriptor.Namespace][binding.DocumentPath]
			if string(binding.DefaultValue) != want {
				t.Errorf("default %s/%s = %s, want %s", descriptor.Namespace, binding.DocumentPath, binding.DefaultValue, want)
			}
		}
	}
}

func TestPropertyDescriptorsAndLookupsAreDefensive(t *testing.T) {
	first := PropertyDescriptors()
	first[0].Namespace = "mutated"
	first[0].Bindings[0].DocumentPath = "mutated"
	first[0].Bindings[0].DefaultValue[0] = '['
	first[0].Bindings[0].KeyframeTargets = append(first[0].Bindings[0].KeyframeTargets, KeyframeTargetCrop)

	second := PropertyDescriptors()
	if second[0].Namespace != PropertyAudioChannelMatrixV1 || second[0].Bindings[0].DocumentPath == "mutated" ||
		!json.Valid(second[0].Bindings[0].DefaultValue) {
		t.Fatalf("registry was mutated through returned value: %+v", second[0])
	}
	lookup, ok := LookupPropertyDescriptor(PropertyTransformV1)
	if !ok || lookup.RequiredCapability != CapabilityTransformV1 {
		t.Fatalf("transform lookup = %+v, %v", lookup, ok)
	}
	lookup.Bindings[0].DefaultValue[0] = '['
	again, ok := LookupPropertyDescriptor(PropertyTransformV1)
	if !ok || !json.Valid(again.Bindings[0].DefaultValue) {
		t.Fatal("lookup returned registry-owned default bytes")
	}
	if _, ok := LookupPropertyDescriptor("unknown.property.v1"); ok {
		t.Fatal("unknown descriptor lookup succeeded")
	}
}

func TestSourceDescriptorsCoverTextAndVectorWithoutImplicitDefaults(t *testing.T) {
	descriptors := SourceDescriptors()
	wantProfiles := []string{string(CapabilityTextV1), VectorProfilePathViewportPXV2, VectorProfilePathV1}
	if len(descriptors) != len(wantProfiles) {
		t.Fatalf("source descriptors = %+v", descriptors)
	}
	for index, descriptor := range descriptors {
		if descriptor.Profile != wantProfiles[index] || descriptor.RequiredCapability != videoedit.CapabilityID(descriptor.Profile) ||
			!capabilityTokenPattern.MatchString(descriptor.EvaluatorProfile) || descriptor.ValueSemantics == "" || descriptor.AgentDescription == "" {
			t.Errorf("incomplete source descriptor: %+v", descriptor)
		}
	}
	text := descriptors[0]
	if !slices.Equal(text.RequiredAssetKinds, []AssetKind{AssetKindFont}) ||
		!slices.Equal(text.KeyframeTargets, []KeyframeTarget{KeyframeTargetTextFill}) ||
		text.KeyframeProfile != KeyframeEvaluationEndpointClampV1 || text.KeyframeCapability != CapabilityKeyframesV1 || text.KeyframeSemantics != keyframeEndpointClampDescription ||
		!strings.Contains(text.ValueSemantics, "no host-font fallback") || !strings.Contains(text.ValueSemantics, "never requests retime") {
		t.Errorf("text source contract incomplete: %+v", text)
	}
	viewportVector := descriptors[1]
	if viewportVector.EvaluatorProfile != EvaluatorVectorPathViewportPXV2 ||
		viewportVector.RequiredCapability != CapabilityVectorPathViewportPXV2 {
		t.Fatalf("viewport vector descriptor identity mismatch: %+v", viewportVector)
	}
	for _, requiredPhrase := range []string{
		"top-down square-pixel edge coordinates", "no host-DPI", "never pixels", "xMidYMid meet",
		"transparent", "clip all coverage", "fill-only", "most recent move_to", "array order",
		"cutly.color.rgba16-srgb-straight-paint-to-linear-bt709-premultiplied-rgba16.v1",
		"source-over", "Cubic commands and strokes are rejected", "never requests retime",
	} {
		if !strings.Contains(viewportVector.ValueSemantics, requiredPhrase) {
			t.Errorf("viewport vector contract does not freeze %q: %s", requiredPhrase, viewportVector.ValueSemantics)
		}
	}
	vector := descriptors[2]
	for _, requiredPhrase := range []string{"implicitly closed for fill only", "nonzero/evenodd", "centered stroke", "miter limit", "no rasterizer default", "never requests retime"} {
		if !strings.Contains(vector.ValueSemantics, requiredPhrase) {
			t.Errorf("vector contract does not freeze %q: %s", requiredPhrase, vector.ValueSemantics)
		}
	}
	descriptors[0].RequiredAssetKinds[0] = AssetKindData
	descriptors[0].KeyframeTargets[0] = KeyframeTargetCrop
	again := SourceDescriptors()
	if again[0].RequiredAssetKinds[0] != AssetKindFont || again[0].KeyframeTargets[0] != KeyframeTargetTextFill {
		t.Fatal("source descriptor registry was mutated through returned slices")
	}
	lookup, ok := LookupSourceDescriptor(VectorProfilePathViewportPXV2)
	if !ok || lookup.EvaluatorProfile != EvaluatorVectorPathViewportPXV2 || lookup.RequiredCapability != CapabilityVectorPathViewportPXV2 {
		t.Fatalf("viewport vector lookup = %+v, %v", lookup, ok)
	}
}

func TestTransitionDescriptorMatchesCapabilityAndHardCutContract(t *testing.T) {
	descriptors := TransitionDescriptors()
	if len(descriptors) != 1 {
		t.Fatalf("transition descriptors = %+v", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.Profile != TransitionDissolveV1 || descriptor.RequiredCapability != videoedit.CapabilityID(TransitionDissolveV1) ||
		descriptor.EvaluatorProfile != EvaluatorDissolveV1 {
		t.Fatalf("dissolve descriptor identity mismatch: %+v", descriptor)
	}
	for name, value := range map[string]string{
		"window": descriptor.WindowSemantics, "progress": descriptor.ProgressSemantics,
		"compositing": descriptor.CompositingSemantics, "zero": descriptor.ZeroDurationSemantics,
	} {
		if value == "" {
			t.Errorf("missing %s semantics: %+v", name, descriptor)
		}
	}
	if !strings.Contains(descriptor.ZeroDurationSemantics, "requires no transition capability") ||
		!strings.Contains(descriptor.CompositingSemantics, "linear-light premultiplied-alpha") {
		t.Fatalf("dissolve semantics are not frozen: %+v", descriptor)
	}
	lookup, ok := LookupTransitionDescriptor(TransitionDissolveV1)
	if !ok || lookup != descriptor {
		t.Fatalf("transition lookup = %+v, %v", lookup, ok)
	}
}

func TestAudioGraphDescriptorCentralizesTopologyMixAndPanLaw(t *testing.T) {
	descriptors := AudioGraphDescriptors()
	if len(descriptors) != 1 {
		t.Fatalf("audio graph descriptors = %+v", descriptors)
	}
	descriptor := descriptors[0]
	wantCapabilities := []videoedit.CapabilityID{CapabilityAudioGainV1, CapabilityAudioPanV1, CapabilityAudioRouteV1}
	if descriptor.Profile != AudioGraphProfileV1 || descriptor.EvaluatorProfile != EvaluatorAudioGraphV1 ||
		!slices.Equal(descriptor.ExplicitGraphCapabilities, wantCapabilities) {
		t.Fatalf("audio graph descriptor identity = %+v", descriptor)
	}
	allSemantics := strings.Join([]string{
		descriptor.ActivationSemantics, descriptor.TopologySemantics, descriptor.RoutingSemantics,
		descriptor.SummationSemantics, descriptor.EvaluationOrder, descriptor.PanSemantics,
		descriptor.OptionalProcessingSemantics,
	}, " ")
	for _, phrase := range []string{
		"implicit direct-master", "exactly one master", "cycles", "exactly one existing bus",
		"selected timeline's track-array order", "Document.AudioBuses order", "clip gain", "bus gain",
		"left gain=min(1,1-p)", "Mono duplicates", "no crossfeed", "other layouts fail preflight", "Equal-power",
		PropertyAudioChannelMatrixV1, PropertyAudioSamplePeakLimiterV1, "independent capabilities", "omission means",
	} {
		if !strings.Contains(allSemantics, phrase) {
			t.Errorf("audio graph contract does not freeze %q: %+v", phrase, descriptor)
		}
	}
	descriptors[0].ExplicitGraphCapabilities[0] = "mutated.v1"
	again := AudioGraphDescriptors()
	if again[0].ExplicitGraphCapabilities[0] != CapabilityAudioGainV1 {
		t.Fatal("audio graph registry was mutated through returned slice")
	}
	lookup, ok := LookupAudioGraphDescriptor(AudioGraphProfileV1)
	if !ok || lookup.EvaluatorProfile != EvaluatorAudioGraphV1 {
		t.Fatalf("audio graph lookup = %+v, %v", lookup, ok)
	}
	lookup.ExplicitGraphCapabilities[0] = "mutated.v1"
	lookupAgain, ok := LookupAudioGraphDescriptor(AudioGraphProfileV1)
	if !ok || lookupAgain.ExplicitGraphCapabilities[0] != CapabilityAudioGainV1 {
		t.Fatal("audio graph lookup returned registry-owned capabilities")
	}
}

func TestTypedAudioProcessingDescriptorsFreezePreflightOrderLatencyAndRounding(t *testing.T) {
	matrix, ok := LookupPropertyDescriptor(PropertyAudioChannelMatrixV1)
	if !ok || matrix.RequiredCapability != CapabilityAudioChannelMatrixV1 || matrix.EvaluatorProfile != EvaluatorAudioChannelMatrixV1 {
		t.Fatalf("channel matrix descriptor = %+v, %v", matrix, ok)
	}
	for _, phrase := range []string{
		"Before clip gain and pan", "ProbeManifest audio.channel_layout", "ProbeManifest audio.channels",
		"exactly the authored source_channels order", "No layout expansion", "arbitrary-precision rational",
		"may exceed nominal [-1,1]", "no implicit normalization", "final PCM quantization", "mono/stereo-only",
	} {
		if !strings.Contains(matrix.ValueSemantics, phrase) {
			t.Errorf("channel matrix descriptor does not freeze %q: %s", phrase, matrix.ValueSemantics)
		}
	}
	limiter, ok := LookupPropertyDescriptor(PropertyAudioSamplePeakLimiterV1)
	if !ok || limiter.RequiredCapability != CapabilityAudioSamplePeakLimiterV1 || limiter.EvaluatorProfile != EvaluatorAudioSamplePeakLimiterV1 {
		t.Fatalf("sample-peak limiter descriptor = %+v, %v", limiter, ok)
	}
	for _, phrase := range []string{
		"After master gain and pan", "peak=max(abs(L),abs(R))", "same exact gain", "zero lookahead",
		"zero latency", "true-peak", "internal rounding", "PCM quantization", "When omitted",
	} {
		if !strings.Contains(limiter.ValueSemantics, phrase) {
			t.Errorf("sample-peak limiter descriptor does not freeze %q: %s", phrase, limiter.ValueSemantics)
		}
	}
}

func TestInterpolationDescriptorsFreezeNumericDomainAndCapabilities(t *testing.T) {
	descriptors := InterpolationDescriptors()
	want := []struct {
		interpolation Interpolation
		capability    videoedit.CapabilityID
		valueTypes    []KeyframeValueType
	}{
		{InterpolationCubic, CapabilityInterpolationCubicV1, []KeyframeValueType{KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar}},
		{InterpolationHold, CapabilityInterpolationHoldV1, []KeyframeValueType{KeyframeValueColor, KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar}},
		{InterpolationLinear, CapabilityInterpolationLinearV1, []KeyframeValueType{KeyframeValuePoint, KeyframeValueRect, KeyframeValueScalar}},
	}
	if len(descriptors) != len(want) {
		t.Fatalf("interpolation descriptors = %+v", descriptors)
	}
	for index, descriptor := range descriptors {
		if descriptor.Interpolation != want[index].interpolation || descriptor.RequiredCapability != want[index].capability ||
			descriptor.Evaluator != InterpolationNumericProfileV1 || !slices.Equal(descriptor.ValueTypes, want[index].valueTypes) ||
			descriptor.ValueSemantics == "" {
			t.Errorf("interpolation descriptor[%d] = %+v, want %+v", index, descriptor, want[index])
		}
		if capabilityForInterpolation(descriptor.Interpolation) != descriptor.RequiredCapability {
			t.Errorf("mapping for %q = %q, descriptor = %q", descriptor.Interpolation, capabilityForInterpolation(descriptor.Interpolation), descriptor.RequiredCapability)
		}
	}
	if !strings.Contains(descriptors[0].ValueSemantics, "2^32") || !strings.Contains(descriptors[0].ValueSemantics, "No CSS or floating-point") {
		t.Errorf("cubic numeric contract is incomplete: %s", descriptors[0].ValueSemantics)
	}
	if !strings.Contains(descriptors[1].ValueSemantics, "only Document v2 interpolation for color") {
		t.Errorf("color interpolation domain is not frozen: %s", descriptors[1].ValueSemantics)
	}

	descriptors[0].ValueTypes[0] = KeyframeValueColor
	again := InterpolationDescriptors()
	if again[0].ValueTypes[0] != KeyframeValuePoint {
		t.Fatal("interpolation registry was mutated through returned slices")
	}
	lookup, ok := LookupInterpolationDescriptor(InterpolationCubic)
	if !ok || lookup.RequiredCapability != CapabilityInterpolationCubicV1 {
		t.Fatalf("cubic lookup = %+v, %v", lookup, ok)
	}
	lookup.ValueTypes[0] = KeyframeValueColor
	lookupAgain, ok := LookupInterpolationDescriptor(InterpolationCubic)
	if !ok || lookupAgain.ValueTypes[0] != KeyframeValuePoint {
		t.Fatal("interpolation lookup returned registry-owned value types")
	}
	if _, ok := LookupInterpolationDescriptor("unknown"); ok {
		t.Fatal("unknown interpolation lookup succeeded")
	}
}

func TestSemanticDescriptorCatalogMatchesCanonicalFixture(t *testing.T) {
	got, err := videoedit.CanonicalJSON(SemanticDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/semantic-descriptors-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSpace(want)
	if !bytes.Equal(got, want) {
		t.Fatalf("semantic descriptor catalog changed without a versioned fixture update\n got: %s\nwant: %s", got, want)
	}
	var decoded SemanticDescriptorCatalog
	if err := json.Unmarshal(want, &decoded); err != nil {
		t.Fatalf("canonical fixture does not decode: %v", err)
	}
	if decoded.SchemaVersion != DescriptorCatalogVersion || len(decoded.AudioGraphs) != len(AudioGraphDescriptors()) ||
		len(decoded.Interpolations) != len(InterpolationDescriptors()) {
		t.Fatalf("canonical fixture is incomplete: %+v", decoded)
	}
}
