package videoedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func validRenderTarget(t *testing.T) RenderTarget {
	t.Helper()
	return RenderTarget{
		TimelineID: "timeline-1",
		Width:      1920,
		Height:     1080,
		FrameClock: FrameClock{
			Origin:          mustTime(t, 0, 1),
			FramesPerSecond: mustTime(t, 30000, 1001),
		},
		SampleClock: SampleClock{
			Origin:           mustTime(t, 0, 1),
			SamplesPerSecond: 48000,
		},
		Channels:    2,
		PixelFormat: "yuv420p",
		ColorSpace:  "bt709",
	}
}

func baselineBackend() BackendCapabilities {
	return BackendCapabilities{
		BackendID:        "reference-cpu-v1",
		BuildFingerprint: "sha256:backend",
		TrackKinds:       []TrackKind{TrackKindVideo, TrackKindAudio},
		AssetKinds:       []AssetKind{AssetKindVideo, AssetKindAudio},
		MaxWidth:         3840,
		MaxHeight:        2160,
	}
}

func TestResolveBuildsBackendNeutralV3Plan(t *testing.T) {
	document := validTestDocument(t)
	renderRange := TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 6, 1)}
	plan, diagnostics := Resolve(document, renderRange, validRenderTarget(t))
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	if plan.SchemaVersion != 3 || plan.SemanticProfile != RenderSemanticProfile {
		t.Fatalf("plan version/profile = %d/%q", plan.SchemaVersion, plan.SemanticProfile)
	}
	if plan.FrameRange != (TickRange{First: 0, Count: 180}) {
		t.Fatalf("frame range = %+v", plan.FrameRange)
	}
	if plan.SampleRange != (TickRange{First: 0, Count: 288000}) {
		t.Fatalf("sample range = %+v", plan.SampleRange)
	}
	if len(plan.Video) != 1 || plan.Video[0].ClipID != "video-clip" {
		t.Fatalf("video plan: %+v", plan.Video)
	}
	if len(plan.Audio) != 1 || plan.Audio[0].ClipID != "audio-clip" {
		t.Fatalf("audio plan: %+v", plan.Audio)
	}
	if len(plan.Resources) != 2 || plan.Resources[0].AssetID != "audio-1" || plan.Resources[1].AssetID != "video-1" {
		t.Fatalf("resources must be stable and sorted: %+v", plan.Resources)
	}
	wantRequirements := CapabilityRequirements{
		TrackKinds: []TrackKind{TrackKindAudio, TrackKindVideo},
		AssetKinds: []AssetKind{AssetKindAudio, AssetKindVideo},
	}
	if !requirementsEqual(plan.RequiredCapabilities, wantRequirements) {
		t.Fatalf("requirements = %+v, want %+v", plan.RequiredCapabilities, wantRequirements)
	}
	if plan.DocumentHash == "" || plan.Fingerprint == "" {
		t.Fatalf("plan missing fingerprints: %+v", plan)
	}
	if diagnostics := CheckCapabilities(plan, baselineBackend()); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("baseline backend diagnostics: %+v", diagnostics)
	}

	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	for _, forbidden := range []string{"footage.mp4", "music.wav", `"uri"`, `"backend_id"`, `"build_fingerprint"`} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("semantic plan leaked execution/resource field %q: %s", forbidden, serialized)
		}
	}
}

func TestResolveDerivesRetimeRequirementsBeforeBackendSelection(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips[0].TimelineRange.Duration = mustTime(t, 4, 1)
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Duration = mustTime(t, 2, 1)
	document.Timelines[0].Tracks[1].Clips[0].TimelineRange.Duration = mustTime(t, 8, 1)
	document.Timelines[0].Tracks[1].Clips[0].SourceRange.Duration = mustTime(t, 4, 1)

	plan, diagnostics := Resolve(document, TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 4, 1)}, validRenderTarget(t))
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve must be independent of backend support: %+v", diagnostics)
	}
	want := []CapabilityID{CapabilityAudioVarispeed, CapabilityVideoLinearRetime}
	if !capabilitiesEqual(plan.RequiredCapabilities.Features, want) {
		t.Fatalf("features = %+v, want %+v", plan.RequiredCapabilities.Features, want)
	}
	fingerprint := plan.Fingerprint

	videoOnly := baselineBackend()
	videoOnly.Features = []CapabilityID{CapabilityVideoLinearRetime}
	capabilityDiagnostics := CheckCapabilities(plan, videoOnly)
	if !containsDiagnostic(capabilityDiagnostics, CodeCapabilityFeature) || !diagnosticMessageContains(capabilityDiagnostics, string(CapabilityAudioVarispeed)) {
		t.Fatalf("missing independent audio varispeed diagnostic: %+v", capabilityDiagnostics)
	}
	if plan.Fingerprint != fingerprint {
		t.Fatal("preflight mutated semantic fingerprint")
	}

	preservePitchOnly := baselineBackend()
	preservePitchOnly.Features = []CapabilityID{CapabilityVideoLinearRetime, CapabilityAudioTimeStretch}
	if diagnostics := CheckCapabilities(plan, preservePitchOnly); !diagnosticMessageContains(diagnostics, string(CapabilityAudioVarispeed)) {
		t.Fatalf("preserve-pitch capability incorrectly satisfied varispeed: %+v", diagnostics)
	}

	full := baselineBackend()
	full.Features = []CapabilityID{CapabilityVideoLinearRetime, CapabilityAudioVarispeed}
	if diagnostics := CheckCapabilities(plan, full); HasErrorDiagnostics(diagnostics) {
		t.Fatalf("full backend diagnostics: %+v", diagnostics)
	}
}

func TestCheckCapabilitiesUsesExplicitOffersAndResolutionLimits(t *testing.T) {
	plan, diagnostics := Resolve(
		validTestDocument(t),
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		validRenderTarget(t),
	)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	empty := BackendCapabilities{BackendID: "empty", MaxWidth: 1280, MaxHeight: 720}
	got := CheckCapabilities(plan, empty)
	for _, code := range []string{CodeCapabilityTrack, CodeCapabilityAsset, CodeCapabilityResolution} {
		if !containsDiagnostic(got, code) {
			t.Fatalf("missing %s: %+v", code, got)
		}
	}

	wrongSchema := plan
	wrongSchema.SchemaVersion--
	if diagnostics := CheckCapabilities(wrongSchema, baselineBackend()); !containsDiagnostic(diagnostics, CodeRenderPlanUnsupported) {
		t.Fatalf("missing plan schema diagnostic: %+v", diagnostics)
	}
}

func TestResolveFiltersAndProjectsVisibleRanges(t *testing.T) {
	document := validTestDocument(t)
	document.Timelines[0].Tracks[0].Clips = append(document.Timelines[0].Tracks[0].Clips, Clip{
		ID: "later-clip", AssetID: "video-1",
		TimelineRange: TimeRange{Start: mustTime(t, 7, 1), Duration: mustTime(t, 2, 1)},
		SourceRange:   TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 2, 1)},
	})
	renderRange := TimeRange{Start: mustTime(t, 2, 1), Duration: mustTime(t, 2, 1)}
	plan, diagnostics := Resolve(document, renderRange, validRenderTarget(t))
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	if len(plan.Video) != 1 || plan.Video[0].ClipID != "video-clip" {
		t.Fatalf("range filtering failed: %+v", plan.Video)
	}
	clip := plan.Video[0]
	if clip.TimelineRange != (TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 5, 1)}) {
		t.Fatalf("authored timeline range changed: %+v", clip.TimelineRange)
	}
	if clip.SourceRange != (TimeRange{Start: mustTime(t, 1, 1), Duration: mustTime(t, 5, 1)}) {
		t.Fatalf("authored source range changed: %+v", clip.SourceRange)
	}
	if clip.VisibleTimelineRange != (TimeRange{Start: mustTime(t, 2, 1), Duration: mustTime(t, 2, 1)}) {
		t.Fatalf("visible timeline range = %+v", clip.VisibleTimelineRange)
	}
	if clip.VisibleSourceRange != (TimeRange{Start: mustTime(t, 3, 1), Duration: mustTime(t, 2, 1)}) {
		t.Fatalf("visible source range = %+v", clip.VisibleSourceRange)
	}
}

func TestResolveReportsTargetTimelineAndOverflowFailures(t *testing.T) {
	document := validTestDocument(t)
	target := validRenderTarget(t)
	target.TimelineID = "missing"
	target.FrameClock.FramesPerSecond = RationalTime{Value: 0, Rate: 1}
	_, diagnostics := Resolve(document, TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)}, target)
	if !containsDiagnostic(diagnostics, CodeRenderTimelineMissing) || !containsDiagnostic(diagnostics, CodeRenderTargetInvalid) {
		t.Fatalf("missing target/timeline diagnostics: %+v", diagnostics)
	}

	second := document.Timelines[0]
	second.ID = "timeline-2"
	second.Tracks = nil
	document.Timelines = append(document.Timelines, second)
	target = validRenderTarget(t)
	target.TimelineID = ""
	_, diagnostics = Resolve(document, TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)}, target)
	if !containsDiagnostic(diagnostics, CodeRenderTimelineRequired) {
		t.Fatalf("missing timeline selection diagnostic: %+v", diagnostics)
	}

	overflowing := TimeRange{Start: RationalTime{Value: math.MaxInt64, Rate: 1}, Duration: RationalTime{Value: 1, Rate: 1}}
	plan, diagnostics := Resolve(validTestDocument(t), overflowing, validRenderTarget(t))
	if !containsDiagnostic(diagnostics, CodeRenderRangeInvalid) || plan.Fingerprint != "" {
		t.Fatalf("overflow result plan=%+v diagnostics=%+v", plan, diagnostics)
	}
	if _, err := rangesIntersect(validTestDocument(t).Timelines[0].Tracks[0].Clips[0].TimelineRange, overflowing); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("rangesIntersect overflow = %v, want ErrTimeOverflow", err)
	}
}

func TestDecodeRenderPlanStrictlyVerifiesV3Fingerprint(t *testing.T) {
	plan, diagnostics := Resolve(
		validTestDocument(t),
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		validRenderTarget(t),
	)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRenderPlan(raw)
	if err != nil || decoded.Fingerprint != plan.Fingerprint {
		t.Fatalf("decode = %+v, %v", decoded, err)
	}

	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":true}`)...)
	if _, err := DecodeRenderPlan(withUnknown); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicateRoot := append([]byte(`{"schema_version":999,`), raw[1:]...)
	if _, err := DecodeRenderPlan(duplicateRoot); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("duplicate field error = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, err := DecodeRenderPlan(caseAlias); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("case alias error = %v", err)
	}
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missingRequired); err != nil {
		t.Fatal(err)
	}
	delete(missingRequired, "required_capabilities")
	missingRaw, _ := json.Marshal(missingRequired)
	if _, err := DecodeRenderPlan(missingRaw); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("missing required field error = %v", err)
	}

	old := plan
	old.SchemaVersion = 2
	oldRaw, _ := json.Marshal(old)
	if _, err := DecodeRenderPlan(oldRaw); !errors.Is(err, ErrRenderPlanSchemaUnsupported) {
		t.Fatalf("old schema error = %v", err)
	}

	tampered := plan
	tampered.Target.PixelFormat = "rgba"
	tamperedRaw, _ := json.Marshal(tampered)
	if _, err := DecodeRenderPlan(tamperedRaw); !errors.Is(err, ErrRenderPlanFingerprintMismatch) {
		t.Fatalf("tamper error = %v", err)
	}

	uncanonical := plan
	uncanonical.RequiredCapabilities.TrackKinds = []TrackKind{TrackKindVideo, TrackKindAudio}
	input := uncanonical
	input.Fingerprint = ""
	uncanonical.Fingerprint, _ = canonicalHash(input)
	uncanonicalRaw, _ := json.Marshal(uncanonical)
	if _, err := DecodeRenderPlan(uncanonicalRaw); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("uncanonical requirements error = %v", err)
	}

	wrongGrid := plan
	wrongGrid.FrameRange.Count++
	input = wrongGrid
	input.Fingerprint = ""
	wrongGrid.Fingerprint, _ = canonicalHash(input)
	wrongGridRaw, _ := json.Marshal(wrongGrid)
	if _, err := DecodeRenderPlan(wrongGridRaw); !errors.Is(err, ErrRenderPlanInvalid) {
		t.Fatalf("wrong clock grid error = %v", err)
	}
}

func TestDecodeRenderPlanReDerivesTrustedSemanticFields(t *testing.T) {
	baseline, diagnostics := Resolve(
		validTestDocument(t),
		TimeRange{Start: mustTime(t, 0, 1), Duration: mustTime(t, 1, 1)},
		validRenderTarget(t),
	)
	if HasErrorDiagnostics(diagnostics) {
		t.Fatal(diagnostics)
	}

	tests := []struct {
		name   string
		mutate func(*RenderPlan)
	}{
		{name: "requirements removed", mutate: func(plan *RenderPlan) {
			plan.RequiredCapabilities.TrackKinds = []TrackKind{TrackKindVideo}
		}},
		{name: "resource removed", mutate: func(plan *RenderPlan) {
			plan.Resources = plan.Resources[1:]
		}},
		{name: "resource hash invalid", mutate: func(plan *RenderPlan) {
			plan.Resources[0].ContentHash = "sha256:not-a-digest"
		}},
		{name: "visible projection changed", mutate: func(plan *RenderPlan) {
			plan.Video[0].VisibleSourceRange.Start = mustTime(t, 2, 1)
		}},
		{name: "timeline binding changed", mutate: func(plan *RenderPlan) {
			plan.Target.TimelineID = "other"
		}},
		{name: "target shape invalid", mutate: func(plan *RenderPlan) {
			plan.Target.Width = 0
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := baseline
			plan.Resources = append([]ResourceRef(nil), baseline.Resources...)
			plan.Video = append([]PlannedClip(nil), baseline.Video...)
			plan.Audio = append([]PlannedClip(nil), baseline.Audio...)
			plan.RequiredCapabilities.TrackKinds = append([]TrackKind(nil), baseline.RequiredCapabilities.TrackKinds...)
			tt.mutate(&plan)
			input := plan
			input.Fingerprint = ""
			plan.Fingerprint, _ = canonicalHash(input)
			raw, _ := json.Marshal(plan)
			if _, err := DecodeRenderPlan(raw); !errors.Is(err, ErrRenderPlanInvalid) {
				t.Fatalf("error = %v, want ErrRenderPlanInvalid", err)
			}
		})
	}
}

func requirementsEqual(left, right CapabilityRequirements) bool {
	return trackKindsEqual(left.TrackKinds, right.TrackKinds) && assetKindsEqual(left.AssetKinds, right.AssetKinds) && capabilitiesEqual(left.Features, right.Features)
}

func trackKindsEqual(left, right []TrackKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assetKindsEqual(left, right []AssetKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func capabilitiesEqual(left, right []CapabilityID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func diagnosticMessageContains(diagnostics []Diagnostic, value string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, value) {
			return true
		}
	}
	return false
}
