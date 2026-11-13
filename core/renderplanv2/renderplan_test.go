package renderplanv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func TestResolveMixedRangeFreezesExactClosure(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	if plan.SchemaVersion != SchemaVersion || plan.Profile != ProfileV1 || plan.CanonicalJSONProfile != canonicaljson.Profile {
		t.Fatalf("protocol identity = %d/%q/%q", plan.SchemaVersion, plan.Profile, plan.CanonicalJSONProfile)
	}
	if plan.FrameRange != (videoedit.TickRange{First: 96, Count: 48}) {
		t.Fatalf("frame range = %+v", plan.FrameRange)
	}
	if plan.SampleRange != (videoedit.TickRange{First: 192000, Count: 96000}) {
		t.Fatalf("sample range = %+v", plan.SampleRange)
	}
	if got := clipIDs(plan.Visual); !slices.Equal(got, []documentv2.ID{"clip-video-a", "clip-video-b", "clip-text", "clip-vector"}) {
		t.Fatalf("visual closure = %q", got)
	}
	if got := clipIDs(plan.Audio); !slices.Equal(got, []documentv2.ID{"clip-audio"}) {
		t.Fatalf("audio closure = %q", got)
	}
	if len(plan.Transitions) != 1 || plan.Transitions[0].TransitionID != "transition-main" ||
		plan.Transitions[0].SelectedWindow != secondRange(4, 2) {
		t.Fatalf("transition closure = %+v", plan.Transitions)
	}
	transition := plan.Transitions[0]
	if transition.Outgoing.SelectedSourceRange != secondRange(6, 2) {
		t.Fatalf("outgoing handle source = %+v", transition.Outgoing.SelectedSourceRange)
	}
	if transition.Incoming.SelectedSourceRange != secondRange(1, 2) {
		t.Fatalf("incoming handle source = %+v", transition.Incoming.SelectedSourceRange)
	}
	if got := resourceIDs(plan.Resources); !slices.Equal(got, []documentv2.ID{"asset-audio", "asset-font", "asset-video-a", "asset-video-b"}) {
		t.Fatalf("resource closure = %q", got)
	}
	if !slices.Contains(plan.RequiredCapabilities.AssetKinds, documentv2.AssetKindFont) ||
		!slices.Contains(plan.RequiredCapabilities.AssetKinds, documentv2.AssetKindText) ||
		!slices.Contains(plan.RequiredCapabilities.AssetKinds, documentv2.AssetKindVector) ||
		!slices.Contains(plan.RequiredCapabilities.Features, videoedit.CapabilityID(documentv2.TransitionDissolveV1)) {
		t.Fatalf("requirements = %+v", plan.RequiredCapabilities)
	}
	bound, err := VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Fingerprint() != plan.Fingerprint || bound.DocumentHash() != plan.DocumentHash {
		t.Fatalf("bound identity = %q/%q", bound.Fingerprint(), bound.DocumentHash())
	}
	if (DocumentBoundPlan{}).Fingerprint() != "" || (DocumentBoundPlan{}).DocumentHash() != "" {
		t.Fatal("zero document-bound handle exposed an identity")
	}
	if _, err := (DocumentBoundPlan{}).Snapshot(); !errors.Is(err, ErrDocumentBoundRequired) {
		t.Fatalf("zero document-bound snapshot error = %v", err)
	}
	snapshot, err := bound.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Resources[0].AssetID = "mutated"
	snapshot.RequiredCapabilities.AssetKinds[0] = documentv2.AssetKindData
	secondSnapshot, err := bound.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if secondSnapshot.Resources[0].AssetID == "mutated" || secondSnapshot.RequiredCapabilities.AssetKinds[0] == documentv2.AssetKindData {
		t.Fatal("document-bound snapshot retained caller mutation")
	}
}

func TestResolveExcludesOutOfRangeAndDisabledSemantics(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	// [8,9) is beyond text/vector and transition, but still includes the second
	// video clip and audio. No pinned font or transition capability may leak in.
	plan, diagnostics := Resolve(context.Background(), document, secondRange(8, 1), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	if got := clipIDs(plan.Visual); !slices.Equal(got, []documentv2.ID{"clip-video-b"}) {
		t.Fatalf("visual closure = %q", got)
	}
	if len(plan.Transitions) != 0 {
		t.Fatalf("out-of-range transition leaked: %+v", plan.Transitions)
	}
	if slices.Contains(plan.RequiredCapabilities.AssetKinds, documentv2.AssetKindFont) ||
		slices.Contains(plan.RequiredCapabilities.AssetKinds, documentv2.AssetKindText) ||
		slices.Contains(plan.RequiredCapabilities.Features, videoedit.CapabilityID(documentv2.TransitionDissolveV1)) {
		t.Fatalf("out-of-range requirements leaked: %+v", plan.RequiredCapabilities)
	}
}

func TestDecodeRequiresCanonicalBytesAndAuthenticatesFingerprint(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	raw, err := Canonical(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || decoded.Fingerprint != plan.Fingerprint {
		t.Fatalf("decode fingerprint=%q err=%v", decoded.Fingerprint, err)
	}
	pretty := append([]byte("\n  "), raw...)
	if _, err := Decode(pretty); !errors.Is(err, ErrNonCanonicalEncoding) {
		t.Fatalf("noncanonical error = %v", err)
	}
	structOrder, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(structOrder); !errors.Is(err, ErrNonCanonicalEncoding) {
		t.Fatalf("noncanonical member-order error = %v", err)
	}

	tampered := plan
	tampered.Visual = append([]PlannedClip(nil), plan.Visual...)
	tampered.Visual[0].AssetID = "asset-video-b"
	tamperedRaw, err := canonicaljson.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(tamperedRaw); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("tamper error = %v", err)
	}

	duplicate := bytes.Replace(raw, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := Decode(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate-key error = %v", err)
	}
	aliased := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"Schema_Version"`), 1)
	if _, err := Decode(aliased); err == nil || !strings.Contains(err.Error(), "unknown object member") {
		t.Fatalf("case-alias error = %v", err)
	}
}

func TestProtocolConfusionLimitsAndRecomputedStructuralTampering(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	base, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	recompute := func(t *testing.T, plan Plan) Plan {
		t.Helper()
		var err error
		plan.Fingerprint, err = fingerprint(plan)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}

	t.Run("unsorted capabilities", func(t *testing.T) {
		plan := base
		plan.RequiredCapabilities.Features = append([]videoedit.CapabilityID(nil), base.RequiredCapabilities.Features...)
		plan.RequiredCapabilities.Features[0], plan.RequiredCapabilities.Features[1] = plan.RequiredCapabilities.Features[1], plan.RequiredCapabilities.Features[0]
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate resources", func(t *testing.T) {
		plan := base
		plan.Resources = append(append([]ResourceRef(nil), base.Resources...), base.Resources[len(base.Resources)-1])
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid capability track kind", func(t *testing.T) {
		plan := base
		plan.RequiredCapabilities.TrackKinds = []documentv2.TrackKind{"annotation"}
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid capability asset kind", func(t *testing.T) {
		plan := base
		plan.RequiredCapabilities.AssetKinds = []documentv2.AssetKind{"executable"}
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid capability feature", func(t *testing.T) {
		plan := base
		plan.RequiredCapabilities.Features = []videoedit.CapabilityID{"not-versioned"}
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("index outside schema", func(t *testing.T) {
		plan := base
		plan.Visual = append([]PlannedClip(nil), base.Visual...)
		plan.Visual[0].TrackIndex = math.MaxInt32 + 1
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("identifier outside schema", func(t *testing.T) {
		plan := base
		plan.DocumentID = documentv2.ID(strings.Repeat("a", MaxProtocolIDRunes+1))
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("wrong document hash", func(t *testing.T) {
		plan := base
		plan.DocumentHash = "sha256:" + strings.Repeat("0", 64)
		plan = recompute(t, plan)
		if _, err := Verify(plan); err != nil {
			t.Fatalf("wire verification unexpectedly failed: %v", err)
		}
		if _, err := VerifyAgainstDocument(context.Background(), plan, document); !errors.Is(err, ErrDocumentMismatch) {
			t.Fatalf("document-bound error = %v", err)
		}
	})

	t.Run("indirect font omission", func(t *testing.T) {
		plan := base
		plan.Resources = slices.DeleteFunc(append([]ResourceRef(nil), base.Resources...), func(value ResourceRef) bool {
			return value.AssetID == "asset-font"
		})
		plan = recompute(t, plan)
		if _, err := Verify(plan); err != nil {
			t.Fatalf("wire verification unexpectedly failed: %v", err)
		}
		if _, err := VerifyAgainstDocument(context.Background(), plan, document); !errors.Is(err, ErrDocumentMismatch) {
			t.Fatalf("document-bound error = %v", err)
		}
	})

	t.Run("clock range overflow", func(t *testing.T) {
		plan := base
		plan.RequestedRange = documentv2.TimeRange{
			Start:    documentv2.RationalTime{Value: math.MaxInt64, Rate: 1},
			Duration: documentv2.RationalTime{Value: 1, Rate: 1},
		}
		if _, err := Verify(recompute(t, plan)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("legacy v3 cross decode", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "render-plan-v3.canonical.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(bytes.TrimSpace(raw)); err == nil {
			t.Fatal("legacy v3 plan crossed the v2 decoder boundary")
		}
	})

	t.Run("oversized wire input", func(t *testing.T) {
		if _, err := Decode(bytes.Repeat([]byte{' '}, MaxCanonicalPlanBytes+1)); !errors.Is(err, ErrPlanInvalid) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestVerifyAgainstDocumentRejectsRecomputedOmission(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	plan.RequiredCapabilities.Features = slices.DeleteFunc(plan.RequiredCapabilities.Features, func(value videoedit.CapabilityID) bool {
		return value == videoedit.CapabilityID(documentv2.TransitionDissolveV1)
	})
	var err error
	plan.Fingerprint, err = fingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(plan); err != nil {
		t.Fatalf("structurally valid recomputed plan should reach document check: %v", err)
	}
	if _, err := VerifyAgainstDocument(context.Background(), plan, document); !errors.Is(err, ErrDocumentMismatch) {
		t.Fatalf("document mismatch error = %v", err)
	}
}

func TestResolveCancellationAndOwnership(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, diagnostics := Resolve(ctx, document, secondRange(4, 2), fixtureTarget("timeline-main")); !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatal("cancelled resolve produced no error diagnostic")
	}

	plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	verified, err := Verify(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := verified.Fingerprint()
	plan.Resources[0].AssetID = "mutated"
	plan.RequiredCapabilities.AssetKinds[0] = documentv2.AssetKindData
	if verified.Fingerprint() != want || verified.DocumentHash() == "" {
		t.Fatal("verified handle retained caller-owned plan state")
	}
}

func TestVerifiedAndDocumentBoundHandlesAreConcurrentReadSafe(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	verified, err := Verify(plan)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if verified.Fingerprint() != plan.Fingerprint || verified.DocumentHash() != plan.DocumentHash ||
					bound.Fingerprint() != plan.Fingerprint || bound.DocumentHash() != plan.DocumentHash {
					t.Errorf("immutable handle identity drifted")
					return
				}
			}
		}()
	}
	group.Wait()
}

func TestResolveRejectsPortableTargetAndDocumentLimits(t *testing.T) {
	document := fixtureDocument(t, "document-v2.valid.json")
	target := fixtureTarget("timeline-main")
	target.Width = math.MaxInt32 + 1
	if _, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), target); !hasDiagnostic(diagnostics, CodeTargetInvalid) {
		t.Fatalf("oversized target diagnostics = %+v", diagnostics)
	}

	oversized := documentv2.Document{Assets: make([]documentv2.Asset, MaxDocumentAssets+1)}
	if _, diagnostics := Resolve(context.Background(), oversized, secondRange(0, 1), fixtureTarget("timeline")); !hasDiagnostic(diagnostics, CodeResourceLimit) {
		t.Fatalf("oversized document diagnostics = %+v", diagnostics)
	}

	nodeHeavy := documentv2.Document{Assets: []documentv2.Asset{{
		Vector: &documentv2.VectorSource{Paths: []documentv2.VectorPath{{Commands: make([]documentv2.VectorCommand, MaxDocumentNodes)}}},
	}}}
	if message, err := documentSizeError(context.Background(), nodeHeavy); err != nil || !strings.Contains(message, "node count") {
		t.Fatalf("node admission message=%q err=%v", message, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := documentSizeError(cancelled, documentv2.Document{Assets: []documentv2.Asset{{}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("admission cancellation error = %v", err)
	}
}

func TestResolvePreservesAdditiveTransitionProfilesAndCapabilities(t *testing.T) {
	for _, profile := range []string{documentv2.TransitionFadeV1, documentv2.TransitionWipeV1} {
		t.Run(profile, func(t *testing.T) {
			document := fixtureDocument(t, "document-v2.valid.json")
			document.Timelines[0].Transitions[0].Profile = profile
			plan, diagnostics := Resolve(context.Background(), document, secondRange(4, 2), fixtureTarget("timeline-main"))
			if documentv2.HasErrorDiagnostics(diagnostics) || len(plan.Transitions) != 1 || plan.Transitions[0].Profile != profile {
				t.Fatalf("plan=%+v diagnostics=%+v", plan, diagnostics)
			}
			if !slices.Contains(plan.RequiredCapabilities.Features, videoedit.CapabilityID(profile)) {
				t.Fatalf("required capabilities %v omit %q", plan.RequiredCapabilities.Features, profile)
			}
			encoded, err := Canonical(plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(encoded); err != nil {
				t.Fatalf("resolved plan rejected: %v", err)
			}
		})
	}
}

func fixtureDocument(t *testing.T, name string) documentv2.Document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func fixtureTarget(timeline documentv2.ID) videoedit.RenderTarget {
	return videoedit.RenderTarget{
		TimelineID: timeline, Width: 1920, Height: 1080,
		FrameClock: videoedit.FrameClock{
			Origin:          videoedit.RationalTime{Value: 0, Rate: 1},
			FramesPerSecond: videoedit.RationalTime{Value: 24, Rate: 1},
		},
		SampleClock: videoedit.SampleClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "rgba16", ColorSpace: "linear-bt709",
	}
}

func secondRange(start, duration int64) documentv2.TimeRange {
	return documentv2.TimeRange{
		Start:    documentv2.RationalTime{Value: start, Rate: 1},
		Duration: documentv2.RationalTime{Value: duration, Rate: 1},
	}
}

func clipIDs(values []PlannedClip) []documentv2.ID {
	result := make([]documentv2.ID, len(values))
	for index, value := range values {
		result[index] = value.ClipID
	}
	return result
}

func resourceIDs(values []ResourceRef) []documentv2.ID {
	result := make([]documentv2.ID, len(values))
	for index, value := range values {
		result[index] = value.AssetID
	}
	return result
}

func hasDiagnostic(values []documentv2.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}
