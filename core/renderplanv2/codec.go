package renderplanv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/jsonadmission"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

var (
	ErrPlanInvalid           = errors.New("renderplanv2: plan is invalid")
	ErrFingerprintMismatch   = errors.New("renderplanv2: fingerprint mismatch")
	ErrNonCanonicalEncoding  = errors.New("renderplanv2: input is not canonical JSON")
	ErrVerifiedPlanRequired  = errors.New("renderplanv2: verified plan is required")
	ErrDocumentBoundRequired = errors.New("renderplanv2: document-bound plan is required")
	ErrDocumentMismatch      = errors.New("renderplanv2: plan does not match document")
)

const (
	MaxCanonicalPlanBytes  = 64 << 20
	MaxPlanCollectionItems = 1 << 20
	MaxProtocolIDRunes     = 4096
	MaxCapabilityIDBytes   = 256
)

var capabilityIDV2Pattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.v[1-9][0-9]*$`)

// VerifiedPlan is an opaque, immutable wire-integrity handle. Verification
// round-trips through the strict decoder so caller-owned slices cannot mutate
// it. It is not document binding or host execution authority.
type VerifiedPlan struct {
	plan     Plan
	verified bool
}

// DocumentBoundPlan additionally proves byte-for-byte equality with a fresh
// Resolve from the committed Document. It is suitable for constructing S2
// integrity declarations, but is not host execution authority; S3 must still
// authorize resources, programs and executable identities.
type DocumentBoundPlan struct {
	verified VerifiedPlan
	bound    bool
}

func (verified VerifiedPlan) Fingerprint() string {
	if !verified.verified {
		return ""
	}
	return verified.plan.Fingerprint
}

func (verified VerifiedPlan) DocumentHash() string {
	if !verified.verified {
		return ""
	}
	return verified.plan.DocumentHash
}

func (bound DocumentBoundPlan) Fingerprint() string {
	if !bound.bound {
		return ""
	}
	return bound.verified.Fingerprint()
}

func (bound DocumentBoundPlan) DocumentHash() string {
	if !bound.bound {
		return ""
	}
	return bound.verified.DocumentHash()
}

// Snapshot returns an owned copy of the document-bound semantic plan for
// downstream manifest construction and preflight. A plain Plan or
// VerifiedPlan cannot call this boundary, and mutations to the returned value
// cannot affect the immutable handle.
func (bound DocumentBoundPlan) Snapshot() (Plan, error) {
	if !bound.bound || !bound.verified.verified {
		return Plan{}, ErrDocumentBoundRequired
	}
	raw, err := Canonical(bound.verified.plan)
	if err != nil {
		return Plan{}, err
	}
	return Decode(raw)
}

// Canonical returns the unique protocol bytes for a valid Plan.
func Canonical(plan Plan) ([]byte, error) {
	return CanonicalContext(context.Background(), plan)
}

func CanonicalContext(ctx context.Context, plan Plan) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := preflightPlan(ctx, plan); err != nil {
		return nil, err
	}
	if err := validate(plan); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return canonicaljson.Marshal(plan)
}

// Decode accepts only canonical protocol bytes, then verifies the fingerprint
// and all structure that can be checked without the source Document.
func Decode(raw []byte) (Plan, error) {
	if len(raw) == 0 || len(raw) > MaxCanonicalPlanBytes {
		return Plan{}, fmt.Errorf("%w: byte length %d", ErrPlanInvalid, len(raw))
	}
	if _, err := canonicaljson.CanonicalizeWithLimits(raw, canonicaljson.Limits{
		MaxInputBytes: MaxCanonicalPlanBytes, MaxDepth: 64, MaxValues: 1 << 20,
		MaxStringBytes: MaxCanonicalPlanBytes, MaxIntegerDigits: 20,
	}); err != nil {
		return Plan{}, fmt.Errorf("%w: canonical JSON: %v", ErrPlanInvalid, err)
	}
	var plan Plan
	if err := wirejson.Decode(raw, &plan); err != nil {
		return Plan{}, fmt.Errorf("%w: decode: %v", ErrPlanInvalid, err)
	}
	canonical, err := canonicaljson.Marshal(plan)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: encode: %v", ErrPlanInvalid, err)
	}
	if !bytes.Equal(raw, canonical) {
		return Plan{}, ErrNonCanonicalEncoding
	}
	if err := validate(plan); err != nil {
		return Plan{}, err
	}
	want, err := fingerprint(plan)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: fingerprint: %v", ErrPlanInvalid, err)
	}
	if plan.Fingerprint != want {
		return Plan{}, fmt.Errorf("%w: got %q, want %q", ErrFingerprintMismatch, plan.Fingerprint, want)
	}
	return plan, nil
}

func Verify(plan Plan) (VerifiedPlan, error) {
	return VerifyContext(context.Background(), plan)
}

func VerifyContext(ctx context.Context, plan Plan) (VerifiedPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := preflightPlan(ctx, plan); err != nil {
		return VerifiedPlan{}, err
	}
	raw, err := canonicaljson.Marshal(plan)
	if err != nil {
		return VerifiedPlan{}, fmt.Errorf("%w: encode: %v", ErrPlanInvalid, err)
	}
	if err := ctx.Err(); err != nil {
		return VerifiedPlan{}, err
	}
	decoded, err := Decode(raw)
	if err != nil {
		return VerifiedPlan{}, err
	}
	return VerifiedPlan{plan: decoded, verified: true}, nil
}

// VerifyAgainstDocument re-resolves the plan and requires byte equality. This
// is the authorization boundary for capability/resource closure: a checksum by
// itself cannot prove that a caller did not omit a rendering feature.
func VerifyAgainstDocument(ctx context.Context, plan Plan, document documentv2.Document) (DocumentBoundPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundPlan{}, err
	}
	verified, err := VerifyContext(ctx, plan)
	if err != nil {
		return DocumentBoundPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundPlan{}, err
	}
	resolved, diagnostics := Resolve(ctx, document, plan.RequestedRange, plan.Target)
	if err := ctx.Err(); err != nil {
		return DocumentBoundPlan{}, err
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return DocumentBoundPlan{}, fmt.Errorf("%w: resolve diagnostics: %+v", ErrDocumentMismatch, diagnostics)
	}
	left, err := CanonicalContext(ctx, verified.plan)
	if err != nil {
		return DocumentBoundPlan{}, err
	}
	right, err := CanonicalContext(ctx, resolved)
	if err != nil {
		return DocumentBoundPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return DocumentBoundPlan{}, err
	}
	if !bytes.Equal(left, right) {
		return DocumentBoundPlan{}, ErrDocumentMismatch
	}
	return DocumentBoundPlan{verified: verified, bound: true}, nil
}

func fingerprint(plan Plan) (string, error) {
	input := plan
	input.Fingerprint = ""
	if err := preflightPlan(context.Background(), input); err != nil {
		return "", err
	}
	raw, err := canonicaljson.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func preflightPlan(ctx context.Context, plan Plan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := jsonadmission.Check(ctx, plan, MaxCanonicalPlanBytes, 1<<20); err != nil {
		return fmt.Errorf("%w: canonical admission: %v", ErrPlanInvalid, err)
	}
	return ctx.Err()
}

func validate(plan Plan) error {
	if plan.SchemaVersion != SchemaVersion || plan.Profile != ProfileV1 ||
		plan.CanonicalJSONProfile != canonicaljson.Profile || plan.HashProfile != HashProfileV1 {
		return fmt.Errorf("%w: unsupported version/profile", ErrPlanInvalid)
	}
	if !validProtocolID(plan.DocumentID) || !validProtocolID(plan.TimelineID) || !validProtocolID(plan.Target.TimelineID) || plan.Target.TimelineID != plan.TimelineID {
		return fmt.Errorf("%w: document/timeline identity", ErrPlanInvalid)
	}
	if len(plan.Visual) > MaxPlanCollectionItems || len(plan.Audio) > MaxPlanCollectionItems ||
		len(plan.Transitions) > MaxPlanCollectionItems || len(plan.Resources) > MaxPlanCollectionItems ||
		len(plan.RequiredCapabilities.TrackKinds) > MaxPlanCollectionItems || len(plan.RequiredCapabilities.AssetKinds) > MaxPlanCollectionItems ||
		len(plan.RequiredCapabilities.Features) > MaxPlanCollectionItems {
		return fmt.Errorf("%w: collection limit exceeded", ErrPlanInvalid)
	}
	if !canonicalSHA256(plan.DocumentHash) || !canonicalSHA256(plan.Fingerprint) {
		return fmt.Errorf("%w: document_hash and fingerprint must be canonical sha256 digests", ErrPlanInvalid)
	}
	if err := validatePositiveRange(plan.RequestedRange); err != nil {
		return fmt.Errorf("%w: requested_range: %v", ErrPlanInvalid, err)
	}
	targetErrors := []string{}
	validateTarget(plan.Target, func(_, _, message string) { targetErrors = append(targetErrors, message) })
	if len(targetErrors) != 0 {
		return fmt.Errorf("%w: target: %s", ErrPlanInvalid, strings.Join(targetErrors, "; "))
	}
	wantFrames, err := videoedit.CompileFrameTickRange(plan.RequestedRange, plan.Target.FrameClock)
	if err != nil || wantFrames != plan.FrameRange {
		return fmt.Errorf("%w: frame_range mismatch", ErrPlanInvalid)
	}
	wantSamples, err := videoedit.CompileSampleTickRange(plan.RequestedRange, plan.Target.SampleClock)
	if err != nil || wantSamples != plan.SampleRange {
		return fmt.Errorf("%w: sample_range mismatch", ErrPlanInvalid)
	}
	if !requirementsValid(plan.RequiredCapabilities) {
		return fmt.Errorf("%w: required capabilities are invalid or not sorted unique", ErrPlanInvalid)
	}
	if err := validateClipList(plan.Visual, true, plan.RequestedRange); err != nil {
		return err
	}
	if err := validateClipList(plan.Audio, false, plan.RequestedRange); err != nil {
		return err
	}
	if err := validateTransitions(plan.Transitions, plan.RequestedRange); err != nil {
		return err
	}
	for index, resource := range plan.Resources {
		if !validProtocolID(resource.AssetID) || !validResourceKind(resource.Kind) {
			return fmt.Errorf("%w: resource %d", ErrPlanInvalid, index)
		}
		if resource.ContentHash != "" && !canonicalSHA256(resource.ContentHash) {
			return fmt.Errorf("%w: resource %d content hash", ErrPlanInvalid, index)
		}
		if index > 0 && plan.Resources[index-1].AssetID >= resource.AssetID {
			return fmt.Errorf("%w: resources are not sorted unique", ErrPlanInvalid)
		}
	}
	return nil
}

func validateClipList(values []PlannedClip, visual bool, requested documentv2.TimeRange) error {
	lastTrack, lastClip := -1, -1
	ids := make(map[documentv2.ID]struct{})
	for index, value := range values {
		if value.TrackIndex < 0 || value.ClipIndex < 0 || value.TrackIndex > math.MaxInt32 || value.ClipIndex > math.MaxInt32 ||
			!validProtocolID(value.TrackID) || !validProtocolID(value.ClipID) || !validProtocolID(value.AssetID) {
			return fmt.Errorf("%w: clip %d identity", ErrPlanInvalid, index)
		}
		if value.TrackIndex < lastTrack || (value.TrackIndex == lastTrack && value.ClipIndex <= lastClip) {
			return fmt.Errorf("%w: clips are not in canonical document order", ErrPlanInvalid)
		}
		lastTrack, lastClip = value.TrackIndex, value.ClipIndex
		if _, duplicate := ids[value.ClipID]; duplicate {
			return fmt.Errorf("%w: duplicate clip %q", ErrPlanInvalid, value.ClipID)
		}
		ids[value.ClipID] = struct{}{}
		if visual {
			if value.TrackKind != documentv2.TrackKindVideo && value.TrackKind != documentv2.TrackKindText && value.TrackKind != documentv2.TrackKindVector {
				return fmt.Errorf("%w: nonvisual track in visual list", ErrPlanInvalid)
			}
		} else if value.TrackKind != documentv2.TrackKindAudio {
			return fmt.Errorf("%w: nonaudio track in audio list", ErrPlanInvalid)
		}
		if !trackAcceptsAsset(value.TrackKind, value.AssetKind) {
			return fmt.Errorf("%w: clip kind mismatch", ErrPlanInvalid)
		}
		if err := validatePositiveRange(value.TimelineRange); err != nil {
			return fmt.Errorf("%w: clip timeline range", ErrPlanInvalid)
		}
		if err := validatePositiveRange(value.SourceRange); err != nil {
			return fmt.Errorf("%w: clip source range", ErrPlanInvalid)
		}
		wantVisible, ok, err := intersectRanges(value.TimelineRange, requested)
		if err != nil || !ok || wantVisible != value.VisibleTimelineRange {
			return fmt.Errorf("%w: clip visible timeline projection", ErrPlanInvalid)
		}
		wantSource, err := projectRange(documentv2.Clip{TimelineRange: value.TimelineRange, SourceRange: value.SourceRange}, wantVisible)
		if err != nil || wantSource != value.VisibleSourceRange {
			return fmt.Errorf("%w: clip visible source projection", ErrPlanInvalid)
		}
	}
	return nil
}

func validateTransitions(values []PlannedTransition, requested documentv2.TimeRange) error {
	last := -1
	ids := make(map[documentv2.ID]struct{})
	for _, value := range values {
		if value.TransitionIndex <= last || value.TransitionIndex < 0 || value.TransitionIndex > math.MaxInt32 || !validProtocolID(value.TransitionID) {
			return fmt.Errorf("%w: transitions are not in canonical document order", ErrPlanInvalid)
		}
		last = value.TransitionIndex
		if _, duplicate := ids[value.TransitionID]; duplicate {
			return fmt.Errorf("%w: duplicate transition", ErrPlanInvalid)
		}
		ids[value.TransitionID] = struct{}{}
		if _, supported := documentv2.LookupTransitionDescriptor(value.Profile); !supported || (value.PreDuration.Value == 0 && value.PostDuration.Value == 0) {
			return fmt.Errorf("%w: inactive/unsupported transition", ErrPlanInvalid)
		}
		selected, ok, err := intersectRanges(value.Window, requested)
		if err != nil || !ok || selected != value.SelectedWindow {
			return fmt.Errorf("%w: transition selected window", ErrPlanInvalid)
		}
		for _, side := range []TransitionSide{value.Outgoing, value.Incoming} {
			if side.TrackIndex < 0 || side.ClipIndex < 0 || side.TrackIndex > math.MaxInt32 || side.ClipIndex > math.MaxInt32 ||
				!validProtocolID(side.TrackID) || !validProtocolID(side.ClipID) || !validProtocolID(side.AssetID) || !trackAcceptsAsset(side.TrackKind, side.AssetKind) {
				return fmt.Errorf("%w: transition side", ErrPlanInvalid)
			}
			if err := validatePositiveRange(side.SelectedSourceRange); err != nil {
				return fmt.Errorf("%w: transition source range", ErrPlanInvalid)
			}
		}
		if value.Outgoing.TrackID != value.Incoming.TrackID || value.Outgoing.TrackIndex != value.Incoming.TrackIndex || value.Incoming.ClipIndex != value.Outgoing.ClipIndex+1 {
			return fmt.Errorf("%w: transition topology", ErrPlanInvalid)
		}
	}
	return nil
}

func requirementsValid(value documentv2.CapabilityRequirements) bool {
	if !sortedUnique(value.TrackKinds) || !sortedUnique(value.AssetKinds) || !sortedUnique(value.Features) {
		return false
	}
	for _, kind := range value.TrackKinds {
		if kind != documentv2.TrackKindVideo && kind != documentv2.TrackKindAudio && kind != documentv2.TrackKindText && kind != documentv2.TrackKindVector {
			return false
		}
	}
	for _, kind := range value.AssetKinds {
		if !validBackendAssetKind(kind) {
			return false
		}
	}
	for _, feature := range value.Features {
		if len(feature) > MaxCapabilityIDBytes || !capabilityIDV2Pattern.MatchString(string(feature)) {
			return false
		}
	}
	return true
}

func sortedUnique[S ~[]E, E ~string](values S) bool {
	return sort.SliceIsSorted(values, func(i, j int) bool { return values[i] < values[j] }) && !hasAdjacentDuplicate(values)
}

func hasAdjacentDuplicate[S ~[]E, E comparable](values S) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
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

func validResourceKind(kind documentv2.AssetKind) bool {
	return slices.Contains([]documentv2.AssetKind{documentv2.AssetKindVideo, documentv2.AssetKindAudio, documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindData}, kind)
}

func validBackendAssetKind(kind documentv2.AssetKind) bool {
	return validResourceKind(kind) || kind == documentv2.AssetKindText || kind == documentv2.AssetKindVector
}

func validProtocolID(value documentv2.ID) bool {
	text := string(value)
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= MaxProtocolIDRunes && len(text) <= MaxProtocolIDRunes*utf8.UTFMax
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	hexValue := strings.TrimPrefix(value, "sha256:")
	if hexValue != strings.ToLower(hexValue) {
		return false
	}
	_, err := hex.DecodeString(hexValue)
	return err == nil
}
