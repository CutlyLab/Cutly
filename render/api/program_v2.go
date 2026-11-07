package renderapi

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

// ProgramDescriptorForV2 returns the frozen plan-scoped identity descriptor. These
// contracts do not claim an existing whole-document reference renderer supports
// mixed video, nor that any implementation is installed.
func ProgramDescriptorForV2(profile string) (ProgramDescriptorV2, error) {
	d := ProgramDescriptorV2{SchemaVersion: 2, Profile: profile, SourceManifestProfile: ProgramSourceManifestProfileV2}
	switch profile {
	case VisualProgramProfileV2, TransparentGapProgramProfileV2:
		d.Domain = ProgramDomainVisualV2
		d.ConfigurationProfile = VisualConfigurationProfileV2
		d.OutputProfile = VideoSurfaceProfileV2
		d.Contribution = "ordered-plan-visual-and-transition-participants"
		if profile == TransparentGapProgramProfileV2 {
			d.Contribution = "transparent-gap"
		}
	case AudioProgramProfileV2, SilenceProgramProfileV2:
		d.Domain = ProgramDomainAudioV2
		d.ConfigurationProfile = AudioConfigurationProfileV2
		d.OutputProfile = AudioBlockProfileV2
		d.Contribution = "ordered-plan-audio-and-bus-closure"
		if profile == SilenceProgramProfileV2 {
			d.Contribution = "silence"
		}
	default:
		return ProgramDescriptorV2{}, invalidV2("unsupported program profile %q", profile)
	}
	return d, nil
}
func ptrV2(s string) *string { return &s }
func configV2(domain ProgramDomainV2, o OutputSpecV2, ticks videoedit.TickRange) (json.RawMessage, error) {
	if domain == ProgramDomainVisualV2 {
		return encodeV2(VisualProgramConfigurationV2{2, VisualConfigurationProfileV2, o.Width, o.Height, o.FrameClock, ticks, VideoSurfaceProfileV2}, MaxConfigurationBytesV2)
	}
	return encodeV2(AudioProgramConfigurationV2{2, AudioConfigurationProfileV2, o.SampleClock, ticks, o.Channels, AudioBlockProfileV2}, MaxConfigurationBytesV2)
}
func domainResourcesV2(clips []renderplanv2.PlannedClip, trans []renderplanv2.PlannedTransition, resources []MaterializedResourceV2) []MaterializedResourceV2 {
	ids := map[documentv2.ID]bool{}
	add := func(b renderplanv2.ClipBinding) {
		if b.AssetKind != documentv2.AssetKindText && b.AssetKind != documentv2.AssetKindVector {
			ids[b.AssetID] = true
		}
		if b.FontAssetID != "" {
			ids[b.FontAssetID] = true
		}
	}
	for _, c := range clips {
		add(c.Binding)
	}
	for _, t := range trans {
		add(t.Outgoing.Binding)
		add(t.Incoming.Binding)
	}
	out := []MaterializedResourceV2{}
	for _, r := range resources {
		if ids[r.AssetID] {
			out = append(out, r)
		}
	}
	return out
}
func selectionV2(m ExecutionManifestV2, p renderplanv2.Plan, domain ProgramDomainV2) (ProgramSelectionV2, error) {
	clips, trans, buses := p.Visual, p.Transitions, []documentv2.ID{}
	ticks, profile := p.FrameRange, VisualProgramProfileV2
	if domain == ProgramDomainAudioV2 {
		clips = p.Audio
		trans = []renderplanv2.PlannedTransition{}
		buses = p.AudioBusIDs
		ticks = p.SampleRange
		profile = AudioProgramProfileV2
	}
	if ticks.Count == 0 {
		return ProgramSelectionV2{}, nil
	}
	if len(clips) == 0 && len(trans) == 0 {
		if domain == ProgramDomainVisualV2 {
			profile = TransparentGapProgramProfileV2
		} else {
			profile = SilenceProgramProfileV2
		}
	}
	d, err := ProgramDescriptorForV2(profile)
	if err != nil {
		return ProgramSelectionV2{}, err
	}
	df, err := hashV2(d)
	if err != nil {
		return ProgramSelectionV2{}, err
	}
	config, err := configV2(domain, m.Output, ticks)
	if err != nil {
		return ProgramSelectionV2{}, err
	}
	cf, err := hashV2(config)
	if err != nil {
		return ProgramSelectionV2{}, err
	}
	s := ProgramSourceManifestV2{2, ProgramSourceManifestProfileV2, canonicaljson.Profile, p.DocumentHash, p.Fingerprint, domain, profile, df, cf, p.RequestedRange, ticks, clips, trans, buses, domainResourcesV2(clips, trans, m.Resources)}
	sf, err := hashV2(s)
	if err != nil {
		return ProgramSelectionV2{}, err
	}
	return ProgramSelectionV2{true, ptrV2(profile), &d, ptrV2(df), config, ptrV2(cf), &s, ptrV2(sf)}, nil
}
func validateSelectionV2(p ProgramSelectionV2, domain ProgramDomainV2, m ExecutionManifestV2) error {
	if !p.Enabled {
		if p.Profile != nil || p.Descriptor != nil || p.DescriptorFingerprint != nil || p.Configuration != nil || p.ConfigurationFingerprint != nil || p.SourceManifest != nil || p.SourceManifestFingerprint != nil {
			return invalidV2("disabled program has fields")
		}
		return nil
	}
	if p.Profile == nil || p.Descriptor == nil || p.DescriptorFingerprint == nil || p.Configuration == nil || p.ConfigurationFingerprint == nil || p.SourceManifest == nil || p.SourceManifestFingerprint == nil {
		return invalidV2("incomplete enabled program")
	}
	d, err := ProgramDescriptorForV2(*p.Profile)
	if err != nil {
		return err
	}
	if d.Domain != domain || *p.Descriptor != d {
		return invalidV2("descriptor/domain mismatch")
	}
	df, err := hashV2(d)
	if err != nil || df != *p.DescriptorFingerprint {
		return invalidV2("descriptor fingerprint")
	}
	// Decode into concrete profile configs before comparing. Raw config validation
	// also happens before any outer Marshal, so Marshal cannot launder whitespace.
	if domain == ProgramDomainVisualV2 {
		var c VisualProgramConfigurationV2
		if err = strictRawV2(p.Configuration, &c, MaxConfigurationBytesV2); err != nil {
			return err
		}
	} else {
		var c AudioProgramConfigurationV2
		if err = strictRawV2(p.Configuration, &c, MaxConfigurationBytesV2); err != nil {
			return err
		}
	}
	s := p.SourceManifest
	if s.SchemaVersion != 2 || s.Profile != ProgramSourceManifestProfileV2 || s.CanonicalJSONProfile != canonicaljson.Profile || s.DocumentHash != m.DocumentHash || s.PlanFingerprint != m.PlanFingerprint || s.Domain != domain || s.ProgramProfile != *p.Profile || s.DescriptorFingerprint != df || s.ConfigurationFingerprint != *p.ConfigurationFingerprint {
		return invalidV2("source identity binding")
	}
	if s.Clips == nil || s.Transitions == nil || s.Resources == nil || s.AudioBusIDs == nil || s.TickRange.Validate() != nil || s.TickRange.Count <= 0 {
		return invalidV2("source collections/ticks")
	}
	if domain == ProgramDomainAudioV2 && len(s.Transitions) != 0 {
		return invalidV2("audio transitions unsupported")
	}
	if domain == ProgramDomainVisualV2 && len(s.AudioBusIDs) != 0 {
		return invalidV2("visual bus closure")
	}
	gap := len(s.Clips) == 0 && len(s.Transitions) == 0
	if (domain == ProgramDomainVisualV2 && ((*p.Profile == TransparentGapProgramProfileV2) != gap)) || (domain == ProgramDomainAudioV2 && ((*p.Profile == SilenceProgramProfileV2) != gap)) {
		return invalidV2("gap/silence contribution mismatch")
	}
	expected, err := configV2(domain, m.Output, s.TickRange)
	if err != nil || !bytes.Equal(expected, p.Configuration) {
		return invalidV2("configuration output/range mismatch")
	}
	cf, err := hashV2(p.Configuration)
	if err != nil || cf != *p.ConfigurationFingerprint {
		return invalidV2("configuration fingerprint")
	}
	sf, err := hashV2(s)
	if err != nil || sf != *p.SourceManifestFingerprint {
		return invalidV2("source fingerprint")
	}
	resources := domainResourcesV2(s.Clips, s.Transitions, m.Resources)
	if !reflect.DeepEqual(resources, s.Resources) {
		return invalidV2("source resource closure")
	}
	return validateSourceStructureV2(*s, m)
}

// Reuse S1's standalone semantic checker on each enabled domain, with an explicit
// synthetic identity and only that domain's resources. This checks inferable
// projections/handles/order/domain/resource closure, not original document truth.
// Never infer that a disabled zero-tick domain has no authored clips/resources.
func validateSourceStructureV2(s ProgramSourceManifestV2, m ExecutionManifestV2) error {
	t := videoedit.RenderTarget{TimelineID: "source-contract", Width: int(m.Output.Width), Height: int(m.Output.Height), Channels: int(m.Output.Channels), PixelFormat: m.Output.PixelFormat, ColorSpace: m.Output.ColorSpace, FrameClock: m.Output.FrameClock, SampleClock: m.Output.SampleClock}
	frames, err := videoedit.CompileFrameTickRange(s.RequestedRange, t.FrameClock)
	if err != nil {
		return invalidV2("source frame grid: %v", err)
	}
	samples, err := videoedit.CompileSampleTickRange(s.RequestedRange, t.SampleClock)
	if err != nil {
		return invalidV2("source sample grid: %v", err)
	}
	expected := frames
	if s.Domain == ProgramDomainAudioV2 {
		expected = samples
	}
	if expected != s.TickRange {
		return invalidV2("source requested range/ticks")
	}
	p := renderplanv2.Plan{SchemaVersion: renderplanv2.SchemaVersion, Profile: renderplanv2.ProfileV1, CanonicalJSONProfile: canonicaljson.Profile, DocumentID: "source-contract", TimelineID: t.TimelineID, DocumentHash: s.DocumentHash, RequestedRange: s.RequestedRange, Target: t, FrameRange: frames, SampleRange: samples, RequiredCapabilities: renderplanv2.CapabilityRequirements{TrackKinds: m.Backend.TrackKinds, AssetKinds: m.Backend.AssetKinds, Features: m.Backend.Features}, Visual: []renderplanv2.PlannedClip{}, Audio: []renderplanv2.PlannedClip{}, Transitions: s.Transitions, AudioBusIDs: s.AudioBusIDs, Resources: []renderplanv2.ResourceRef{}}
	if s.Domain == ProgramDomainVisualV2 {
		p.Visual = s.Clips
	} else {
		p.Audio = s.Clips
	}
	for _, r := range s.Resources {
		p.Resources = append(p.Resources, renderplanv2.ResourceRef{AssetID: r.AssetID, Kind: r.Kind, ContentHash: r.ContentHash})
	}
	// When both domains are enabled the complete source closure is visible:
	// validate their joint clip/asset/track identities as well. Do not do this
	// when a zero-tick domain is absent; its retained resources remain plan-only.
	if m.Visual.Enabled && m.Audio.Enabled && m.Visual.SourceManifest != nil && m.Audio.SourceManifest != nil {
		p.Visual = m.Visual.SourceManifest.Clips
		p.Audio = m.Audio.SourceManifest.Clips
		p.Transitions = m.Visual.SourceManifest.Transitions
		p.AudioBusIDs = m.Audio.SourceManifest.AudioBusIDs
		p.Resources = []renderplanv2.ResourceRef{}
		for _, r := range m.Resources {
			p.Resources = append(p.Resources, renderplanv2.ResourceRef{AssetID: r.AssetID, Kind: r.Kind, ContentHash: r.ContentHash})
		}
	}
	// Compute S1's member-excluding preimage without changing any source record.
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	delete(fields, "fingerprint")
	p.Fingerprint, err = hashV2(fields)
	if err != nil {
		return err
	}
	if _, err = renderplanv2.Verify(p); err != nil {
		return invalidV2("source structure: %v", err)
	}
	return nil
}
