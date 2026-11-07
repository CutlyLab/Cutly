package renderapi

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func sortedV2[T ~string](v []T) bool {
	if v == nil {
		return false
	}
	for i, x := range v {
		if strings.TrimSpace(string(x)) == "" || (i > 0 && v[i-1] >= x) {
			return false
		}
	}
	return true
}
func includesV2[T ~string](all, need []T) bool {
	for _, n := range need {
		found := false
		for _, x := range all {
			if x == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func assetV2(k documentv2.AssetKind) bool {
	switch k {
	case documentv2.AssetKindVideo, documentv2.AssetKindAudio, documentv2.AssetKindImage, documentv2.AssetKindFont, documentv2.AssetKindText, documentv2.AssetKindVector, documentv2.AssetKindData:
		return true
	}
	return false
}
func trackV2(k documentv2.TrackKind) bool {
	switch k {
	case documentv2.TrackKindVideo, documentv2.TrackKindAudio, documentv2.TrackKindText, documentv2.TrackKindVector:
		return true
	}
	return false
}
func validateResourceV2(r MaterializedResourceV2) error {
	if strings.TrimSpace(string(r.AssetID)) == "" || len(r.AssetID) > 1024 || !validDigest(r.ContentHash) || r.SizeBytes <= 0 {
		return invalidV2("resource identity/size")
	}
	if err := validateMaterializedLocator(r.LocatorScheme, r.Locator, r.ContentHash); err != nil {
		return invalidV2("resource locator: %v", err)
	}
	switch r.Kind {
	case documentv2.AssetKindVideo, documentv2.AssetKindAudio:
		if r.MaterializationProfile != AVMaterializationProfileV2 || r.ProbeFingerprint == nil || !validDigest(*r.ProbeFingerprint) || r.InspectionFingerprint != nil {
			return invalidV2("temporal probe union/profile")
		}
	case documentv2.AssetKindImage, documentv2.AssetKindFont:
		profile := ImageMaterializationProfileV2
		if r.Kind == documentv2.AssetKindFont {
			profile = FontMaterializationProfileV2
		}
		if r.MaterializationProfile != profile || r.InspectionFingerprint == nil || !validDigest(*r.InspectionFingerprint) || r.ProbeFingerprint != nil {
			return invalidV2("static inspection union/profile")
		}
	default:
		return invalidV2("unsupported materialized resource kind")
	}
	return nil
}
func validateManifestV2(m ExecutionManifestV2) error {
	if m.SchemaVersion != 2 || m.Profile != ExecutionManifestProfileV2 || m.CanonicalJSONProfile != canonicaljson.Profile || m.SemanticProfile != renderplanv2.ProfileV1 {
		return invalidV2("schema/profile")
	}
	if m.DeterministicSeed != 0 {
		return invalidV2("initial profile requires zero seed")
	}
	for _, s := range []string{m.PlanFingerprint, m.DocumentHash, m.CompilerBuild, m.EvaluatorBuild, m.ProviderBuild, m.Backend.BuildFingerprint, m.MediaRuntime.FFmpegBuild, m.Fingerprint} {
		if !validDigest(s) {
			return invalidV2("required digest")
		}
	}
	if m.MediaRuntime.TextShaperBuild != "" && !validDigest(m.MediaRuntime.TextShaperBuild) {
		return invalidV2("text shaper digest")
	}
	for _, s := range []string{m.Backend.BackendID, m.MediaRuntime.DecoderProfile, m.MediaRuntime.EncoderProfile, m.MediaRuntime.ColorProfile} {
		if strings.TrimSpace(s) == "" {
			return invalidV2("runtime/backend identity")
		}
	}
	b := m.Backend
	if b.MaxWidth == 0 || b.MaxHeight == 0 || b.MaxWidth > 2147483647 || b.MaxHeight > 2147483647 || !sortedV2(b.TrackKinds) || !sortedV2(b.AssetKinds) || !sortedV2(b.Features) {
		return invalidV2("backend dimensions/sets")
	}
	for _, k := range b.TrackKinds {
		if !trackV2(k) {
			return invalidV2("backend track")
		}
	}
	for _, k := range b.AssetKinds {
		if !assetV2(k) {
			return invalidV2("backend asset")
		}
	}
	for _, f := range b.Features {
		if !capabilityIDPattern.MatchString(string(f)) {
			return invalidV2("backend feature")
		}
	}
	o := m.Output
	if o.Width == 0 || o.Width > 2147483647 || o.Height == 0 || o.Height > 2147483647 || o.Channels == 0 || o.Channels > 2147483647 || o.SampleRate <= 0 || o.SampleRate != o.SampleClock.SamplesPerSecond || o.FrameClock.Validate() != nil || o.SampleClock.Validate() != nil {
		return invalidV2("output dimensions/clocks")
	}
	if o.Width > b.MaxWidth || o.Height > b.MaxHeight {
		return invalidV2("backend target dimensions")
	}
	if o.ReferenceProfile != OutputReferenceProfileV2 || o.Intermediate.VideoSurfaceProfile != VideoSurfaceProfileV2 || o.Intermediate.AudioBlockProfile != AudioBlockProfileV2 || o.DecodedVideoProfile != DecodedVideoProfileRGBA8V1 || o.DecodedAudioProfile != DecodedAudioProfilePCM16LEV1 {
		return invalidV2("intermediate/final output profile")
	}
	for _, s := range []string{o.Format, o.PixelFormat, o.ColorSpace, o.VideoCodec, o.AudioCodec} {
		if strings.TrimSpace(s) == "" {
			return invalidV2("output format")
		}
	}
	if m.Resources == nil {
		return invalidV2("null resources")
	}
	for i, r := range m.Resources {
		if i > 0 && m.Resources[i-1].AssetID >= r.AssetID {
			return invalidV2("resource order/duplicate")
		}
		if err := validateResourceV2(r); err != nil {
			return err
		}
	}
	if err := rawConfigurationsV2(m); err != nil {
		return err
	}
	if err := validateSelectionV2(m.Visual, ProgramDomainVisualV2, m); err != nil {
		return err
	}
	if err := validateSelectionV2(m.Audio, ProgramDomainAudioV2, m); err != nil {
		return err
	}
	// One manifest has one plan range. When at least one source is present it
	// also proves whether the absent counterpart truly has zero grid work.
	var source *ProgramSourceManifestV2
	if m.Visual.Enabled {
		source = m.Visual.SourceManifest
	}
	if m.Audio.Enabled {
		if source != nil && source.RequestedRange != m.Audio.SourceManifest.RequestedRange {
			return invalidV2("program requested ranges differ")
		}
		source = m.Audio.SourceManifest
	}
	if source != nil {
		frames, err := videoedit.CompileFrameTickRange(source.RequestedRange, m.Output.FrameClock)
		if err != nil || m.Visual.Enabled != (frames.Count != 0) {
			return invalidV2("visual enabled/grid mismatch")
		}
		samples, err := videoedit.CompileSampleTickRange(source.RequestedRange, m.Output.SampleClock)
		if err != nil || m.Audio.Enabled != (samples.Count != 0) {
			return invalidV2("audio enabled/grid mismatch")
		}
	}
	want, err := manifestFingerprintV2(m)
	if err != nil || want != m.Fingerprint {
		return invalidV2("fingerprint mismatch")
	}
	return nil
}
func againstPlanV2(m ExecutionManifestV2, p renderplanv2.Plan) error {
	if m.PlanFingerprint != p.Fingerprint || m.DocumentHash != p.DocumentHash || m.SemanticProfile != p.Profile {
		return invalidV2("plan identity")
	}
	o, t := m.Output, p.Target
	if int64(o.Width) != int64(t.Width) || int64(o.Height) != int64(t.Height) || int64(o.Channels) != int64(t.Channels) || o.SampleRate != t.SampleClock.SamplesPerSecond || o.PixelFormat != t.PixelFormat || o.ColorSpace != t.ColorSpace || o.FrameClock != t.FrameClock || o.SampleClock != t.SampleClock {
		return invalidV2("plan target/clocks")
	}
	if len(m.Resources) != len(p.Resources) {
		return invalidV2("resource closure count")
	}
	for i, r := range p.Resources {
		a := m.Resources[i]
		if a.AssetID != r.AssetID || a.Kind != r.Kind || (r.ContentHash != "" && a.ContentHash != r.ContentHash) {
			return invalidV2("resource substitution")
		}
	}
	b, c := m.Backend, p.RequiredCapabilities
	if !includesV2(b.TrackKinds, c.TrackKinds) || !includesV2(b.AssetKinds, c.AssetKinds) || !includesV2(b.Features, c.Features) {
		return invalidV2("capability gap")
	}
	for _, domain := range []ProgramDomainV2{ProgramDomainVisualV2, ProgramDomainAudioV2} {
		want, err := selectionV2(m, p, domain)
		if err != nil {
			return err
		}
		got := m.Visual
		if domain == ProgramDomainAudioV2 {
			got = m.Audio
		}
		if !reflect.DeepEqual(want, got) {
			return invalidV2("program differs from exact plan closure")
		}
	}
	return nil
}

// FreezeExecutionManifestV2 builds identity only. It cannot authorize resources,
// host executable digests, implementations or receipt claims. Draft program fields
// must be valid if supplied and must equal the deterministic plan-derived fields;
// empty selections are filled by the builder. Resources/capabilities are sorted,
// never deduplicated; caller data is cloned after bounded admission.
func FreezeExecutionManifestV2(ctx context.Context, p renderplanv2.Plan, draft ExecutionManifestV2) (out ExecutionManifestV2, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = ExecutionManifestV2{}
			err = invalidV2("freeze panic: %v", r)
		}
	}()
	if err = admissionV2(ctx, p, MaxRequestBytesV2); err != nil {
		return out, err
	}
	if err = admissionV2(ctx, draft, MaxManifestBytesV2); err != nil {
		return out, err
	}
	if err = rawConfigurationsV2(draft); err != nil {
		return out, err
	}
	ownedPlan, e := renderplanv2.Verify(p)
	if e != nil {
		return out, e
	}
	p, err = ownedPlan.Plan()
	if err != nil {
		return out, err
	}
	raw, e := json.Marshal(draft)
	if e != nil {
		return out, e
	}
	var m ExecutionManifestV2
	if err = json.Unmarshal(raw, &m); err != nil {
		return out, err
	}
	m.SchemaVersion = 2
	m.Profile = ExecutionManifestProfileV2
	m.CanonicalJSONProfile = canonicaljson.Profile
	m.PlanFingerprint = p.Fingerprint
	m.SemanticProfile = p.Profile
	m.DocumentHash = p.DocumentHash
	if m.Resources == nil {
		m.Resources = []MaterializedResourceV2{}
	}
	sort.Slice(m.Resources, func(i, j int) bool { return m.Resources[i].AssetID < m.Resources[j].AssetID })
	sort.Slice(m.Backend.TrackKinds, func(i, j int) bool { return m.Backend.TrackKinds[i] < m.Backend.TrackKinds[j] })
	sort.Slice(m.Backend.AssetKinds, func(i, j int) bool { return m.Backend.AssetKinds[i] < m.Backend.AssetKinds[j] })
	sort.Slice(m.Backend.Features, func(i, j int) bool { return m.Backend.Features[i] < m.Backend.Features[j] })
	for _, domain := range []ProgramDomainV2{ProgramDomainVisualV2, ProgramDomainAudioV2} {
		expected, e := selectionV2(m, p, domain)
		if e != nil {
			return out, e
		}
		selection := &m.Visual
		if domain == ProgramDomainAudioV2 {
			selection = &m.Audio
		}
		if !reflect.DeepEqual(*selection, ProgramSelectionV2{}) && !reflect.DeepEqual(*selection, expected) {
			return out, invalidV2("supplied program mismatch")
		}
		*selection = expected
	}
	m.Fingerprint, err = manifestFingerprintV2(m)
	if err != nil {
		return out, err
	}
	if _, err = VerifyExecutionManifestV2AgainstPlan(ctx, m, p); err != nil {
		return out, err
	}
	return m, nil
}
func validateRequestV2(ctx context.Context, r RenderRequestV2) error {
	fail := func(err error) error { return fmt.Errorf("%w: %w", ErrRenderRequestInvalidV2, err) }
	if r.SchemaVersion != 2 || r.Profile != RenderRequestProfileV2 || r.CanonicalJSONProfile != canonicaljson.Profile {
		return fail(invalidV2("request envelope"))
	}
	if _, err := VerifyExecutionManifestV2AgainstPlan(ctx, r.Manifest, r.Plan); err != nil {
		return fail(err)
	}
	j, p := r.Job, r.Plan
	if err := validateRenderJob(j); err != nil {
		return fail(err)
	}
	if j.PlanFingerprint != p.Fingerprint || j.ExecutionManifestFingerprint != r.Manifest.Fingerprint {
		return fail(invalidV2("job identities"))
	}
	if err := timeRangeSubset(j.TimelineRange, p.RequestedRange); err != nil {
		return fail(err)
	}
	frames, err := videoedit.CompileFrameTickRange(j.TimelineRange, p.Target.FrameClock)
	if err != nil || frames != j.FrameRange {
		return fail(invalidV2("job frame range"))
	}
	samples, err := videoedit.CompileSampleTickRange(j.TimelineRange, p.Target.SampleClock)
	if err != nil || samples != j.SampleRange {
		return fail(invalidV2("job sample range"))
	}
	if err = tickRangeSubset(j.FrameRange, p.FrameRange); err != nil {
		return fail(err)
	}
	if err = tickRangeSubset(j.SampleRange, p.SampleRange); err != nil {
		return fail(err)
	}
	return nil
}
