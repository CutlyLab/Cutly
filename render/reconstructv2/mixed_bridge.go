package reconstructv2

import (
	"context"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

const MixedBridgeProfileV2 = "cutly.reconstruct.document-v2.semantic-native-mixed-bridge.v2"

// MixedBridge is an unsigned V2 receipt, not a V1 Bridge or reconstruction
// authority. RuntimeBinding is the trusted host's explicit runtime mapping;
// video sources record observations from the actual retained bytes and adapters.
type MixedBridge struct {
	SchemaVersion              int                                `json:"schema_version"`
	Profile                    string                             `json:"profile"`
	Key                        prepared.KeyV2                     `json:"environment_key"`
	HostConfigHash             string                             `json:"host_config_hash"`
	Backend                    renderapi.BackendCapabilitiesV2    `json:"backend"`
	MediaRuntime               renderapi.MediaRuntime             `json:"media_runtime"`
	RuntimeBinding             *MixedRuntimeBinding               `json:"runtime_binding,omitempty"`
	Output                     renderapi.OutputSpecV2             `json:"output"`
	SemanticVisual             renderapi.ProgramSelectionV2       `json:"semantic_visual"`
	SemanticAudio              renderapi.ProgramSelectionV2       `json:"semantic_audio"`
	VisualConfig               referencevisual.ConfigV3           `json:"visual_config"`
	VisualManifest             referencevisual.SourceManifestV3   `json:"visual_manifest"`
	VisualDescriptorHash       string                             `json:"visual_descriptor_hash"`
	AudioReceipt               NativeAudioReceiptBridge           `json:"audio_receipt"`
	AudioReceiptProjectionHash string                             `json:"audio_receipt_projection_hash"`
	AudioDescriptorHash        string                             `json:"audio_descriptor_hash"`
	WAVManifest                wavsource.Manifest                 `json:"wav_manifest"`
	PCMConfigHash              string                             `json:"pcm_config_hash"`
	Video                      []MixedVideoEvidence               `json:"video"`
	ValidatedResources         []renderapi.MaterializedResourceV2 `json:"validated_resources"`
	EvidenceSemantics          string                             `json:"evidence_semantics"`
	Fingerprint                string                             `json:"fingerprint,omitempty"`
}

type MixedVideoEvidence struct {
	AssetID                   documentv2.ID       `json:"asset_id"`
	Probe                     probe.ProbeIdentity `json:"probe"`
	ProbeFingerprint          string              `json:"probe_fingerprint"`
	ScannerProfile            string              `json:"scanner_profile"`
	ScannerBuildFingerprint   string              `json:"scanner_build_fingerprint"`
	ScannerOptionsFingerprint string              `json:"scanner_options_fingerprint"`
	IndexFingerprint          string              `json:"index_fingerprint"`
	SemanticsFingerprint      string              `json:"semantics_fingerprint"`
}

func buildMixedBridge(ctx context.Context, env prepared.EnvironmentV2, f *MixedFactory, scopes reconstructionScopes, visual *referencevisual.ProgramV3, audio *audiograph.DomainProgram, wav *wavsource.Provider, observed map[documentv2.ID]openedVideoResource) (out MixedBridge, err error) {
	if ctx == nil {
		return out, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	verified, err := prepared.VerifyEnvironmentV2(ctx, env)
	if err != nil {
		return out, err
	}
	key := verified.Key()
	vm, ar, wm := visual.SourceManifest(), audio.Receipt(), wav.Manifest()
	if visual.Profile() != referencevisual.DomainProfileV3 || vm.Profile != referencevisual.DomainManifestProfileV3 || vm.SchemaVersion != 3 || vm.DocumentHash != key.DocumentHash || visual.DocumentHash() != key.DocumentHash || audio.DocumentHash() != key.DocumentHash || wm.DocumentHash != key.DocumentHash || ar.DocumentHash != key.DocumentHash || ar.Profile != audiograph.DomainCompileProfileV2 || ar.SchemaVersion != 2 || !reflect.DeepEqual(bridgeScope(visual.Scope()), bridgeScope(scopes.visual)) || !reflect.DeepEqual(vm.DomainScope.Scope, bridgeScope(scopes.visual)) || !reflect.DeepEqual(bridgeScope(audio.Scope()), bridgeScope(scopes.audio)) || !reflect.DeepEqual(bridgeScope(ar.Scope), bridgeScope(scopes.audio)) || visual.DomainScopeHash() != vm.DomainScope.Fingerprint || len(ar.Sources) != len(wm.Assets) {
		return out, ErrBridge
	}
	for i, s := range ar.Sources {
		a := wm.Assets[i]
		if s.AssetID != a.AssetID || s.AssetIndex != a.AssetIndex || s.ManifestFingerprint != a.ManifestFingerprint || s.ResourceContentHash != a.EncodedContentHash || s.SampleRate != int64(a.SampleRate) || s.ChannelLayout != a.ChannelLayout || !reflect.DeepEqual(s.DecoderChannels, a.DecoderChannels) || s.DecodeProfile != a.DecodeProfile || s.SelectionProfile != a.SelectionProfile || s.RetimeProfile != a.RetimeProfile {
			return out, ErrBridge
		}
	}
	original := ar.ReceiptHash
	ar.ReceiptHash = ""
	digest, e := bridgeDigest(ctx, ar)
	if e != nil {
		return out, e
	}
	if digest != original {
		return out, ErrBridge
	}
	out = MixedBridge{SchemaVersion: 2, Profile: MixedBridgeProfileV2, Key: key, HostConfigHash: f.configHash, Backend: env.Manifest.Backend, MediaRuntime: env.Manifest.MediaRuntime, RuntimeBinding: f.runtimeIdentity(), Output: env.Manifest.Output, SemanticVisual: env.Manifest.Visual, SemanticAudio: env.Manifest.Audio, VisualConfig: visual.Config(), VisualManifest: vm, WAVManifest: wm, Video: []MixedVideoEvidence{}, ValidatedResources: append([]renderapi.MaterializedResourceV2(nil), env.Manifest.Resources...), EvidenceSemantics: "Unsigned host reconstruction receipt: actual retained bytes, probes and native source identities were checked during construction. Runtime mapping is explicit host policy, not a measured version string or runtimebundle signature. Receipt alone grants no authority and does not prove pixel/sample causality or cross-runtime equality."}
	out.AudioReceipt = NativeAudioReceiptBridge{SchemaVersion: ar.SchemaVersion, Profile: ar.Profile, DocumentHash: ar.DocumentHash, Scope: bridgeScope(ar.Scope), Limits: ar.Limits, Offered: ar.Offered, DomainDescriptorHash: ar.DomainDescriptorHash, EvaluatorDescriptorHash: ar.EvaluatorDescriptorHash, DSPDescriptorHash: ar.DSPDescriptorHash, ConfigHash: ar.ConfigHash, Sources: ar.Sources, OriginalReceiptHash: original}
	for id, v := range observed {
		fp, e := probe.Fingerprint(v.manifest)
		if e != nil {
			return MixedBridge{}, e
		}
		out.Video = append(out.Video, MixedVideoEvidence{AssetID: id, Probe: v.manifest.Probe, ProbeFingerprint: fp, ScannerProfile: ffprobe.VideoScannerProfileV1, ScannerBuildFingerprint: v.scan.ScannerBuildFingerprint, ScannerOptionsFingerprint: v.scan.ScannerOptionsFingerprint, IndexFingerprint: v.scan.IndexFingerprint, SemanticsFingerprint: v.scan.SemanticsFingerprint})
	}
	sort.Slice(out.Video, func(i, j int) bool { return out.Video[i].AssetID < out.Video[j].AssetID })
	sort.Slice(out.ValidatedResources, func(i, j int) bool { return out.ValidatedResources[i].AssetID < out.ValidatedResources[j].AssetID })
	if out.ValidatedResources == nil {
		out.ValidatedResources = []renderapi.MaterializedResourceV2{}
	}
	for _, item := range []struct {
		value  any
		target *string
	}{{referencevisual.DomainDescriptorV3(), &out.VisualDescriptorHash}, {audiograph.DomainDescriptorV2(), &out.AudioDescriptorHash}, {out.AudioReceipt, &out.AudioReceiptProjectionHash}, {f.static.config.PCM, &out.PCMConfigHash}} {
		*item.target, err = bridgeDigest(ctx, item.value)
		if err != nil {
			return MixedBridge{}, err
		}
	}
	out.Fingerprint, err = bridgeDigest(ctx, out)
	if err != nil {
		return MixedBridge{}, err
	}
	return out, ctx.Err()
}

func canonicalMixedBridge(ctx context.Context, value MixedBridge) ([]byte, error) {
	fp := value.Fingerprint
	value.Fingerprint = ""
	if value.SchemaVersion != 2 || value.Profile != MixedBridgeProfileV2 || !bridgeSHA(fp) {
		return nil, ErrBridge
	}
	digest, err := bridgeDigest(ctx, value)
	if err != nil {
		return nil, err
	}
	if digest != fp {
		return nil, ErrBridge
	}
	value.Fingerprint = fp
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}
