package reconstructv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

const BridgeProfileV1 = "cutly.reconstruct.document-v2.semantic-native-bridge.v1"

var ErrBridge = errors.New("reconstructv2: invalid semantic/native bridge")

// Bridge is an owned unsigned semantic/native reconstruction receipt, not durable authority,
// executable authorization, attestation, or proof that serialized resource claims match bytes.
// Factory validates the actual privately retained bytes and native observations before building it.
// Semantic selections and native configurations are intentionally separate identities.
type Bridge struct {
	SchemaVersion                int                                `json:"schema_version"`
	Profile                      string                             `json:"profile"`
	DocumentHash                 string                             `json:"document_hash"`
	PlanFingerprint              string                             `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string                             `json:"execution_manifest_fingerprint"`
	HostConfigHash               string                             `json:"host_config_hash"`
	CompilerBuild                string                             `json:"compiler_build"`
	EvaluatorBuild               string                             `json:"evaluator_build"`
	ProviderBuild                string                             `json:"provider_build"`
	Backend                      renderapi.BackendCapabilitiesV2    `json:"backend"`
	MediaRuntime                 renderapi.MediaRuntime             `json:"media_runtime"`
	DeterministicSeed            int64                              `json:"deterministic_seed"`
	TimelineID                   documentv2.ID                      `json:"timeline_id"`
	RequestedRange               documentv2.TimeRange               `json:"requested_range"`
	FrameRange                   videoedit.TickRange                `json:"frame_range"`
	SampleRange                  videoedit.TickRange                `json:"sample_range"`
	Output                       renderapi.OutputSpecV2             `json:"output"`
	SemanticVisual               renderapi.ProgramSelectionV2       `json:"semantic_visual"`
	SemanticAudio                renderapi.ProgramSelectionV2       `json:"semantic_audio"`
	NativeVisual                 NativeVisualBridge                 `json:"native_visual"`
	NativeAudio                  NativeAudioBridge                  `json:"native_audio"`
	ValidatedResources           []renderapi.MaterializedResourceV2 `json:"validated_resources"`
	EvidenceSemantics            string                             `json:"evidence_semantics"`
	Fingerprint                  string                             `json:"fingerprint,omitempty"`
}

type NativeVisualBridge struct {
	Enabled                  bool                           `json:"enabled"`
	Profile                  string                         `json:"profile"`
	DescriptorHash           string                         `json:"descriptor_hash"`
	CompositorDescriptorHash string                         `json:"compositor_descriptor_hash"`
	Config                   referencevisual.Config         `json:"config"`
	ConfigHash               string                         `json:"config_hash"`
	SourceManifest           referencevisual.SourceManifest `json:"source_manifest"`
	SourceManifestHash       string                         `json:"source_manifest_hash"`
}

type NativeAudioBridge struct {
	Enabled               bool                     `json:"enabled"`
	Profile               string                   `json:"profile"`
	DescriptorHash        string                   `json:"descriptor_hash"`
	Receipt               NativeAudioReceiptBridge `json:"receipt"`
	ReceiptProjectionHash string                   `json:"receipt_projection_hash"`
	WAVDescriptorHash     string                   `json:"wav_descriptor_hash"`
	WAVManifest           wavsource.Manifest       `json:"wav_manifest"`
	WAVManifestHash       string                   `json:"wav_manifest_hash"`
	PCMDescriptorHash     string                   `json:"pcm_descriptor_hash"`
	PCMConfig             pcm16le.Limits           `json:"pcm_config"`
	PCMConfigHash         string                   `json:"pcm_config_hash"`
}

// NativeAudioReceiptBridge projects DomainCompileReceipt explicitly so evaluator.DomainScope's
// untagged Go fields never become an accidental wire contract. OriginalReceiptHash binds the
// native compiler's own receipt; ReceiptProjectionHash separately identifies this lowercase form.
type NativeAudioReceiptBridge struct {
	SchemaVersion           int                              `json:"schema_version"`
	Profile                 string                           `json:"profile"`
	DocumentHash            string                           `json:"document_hash"`
	Scope                   visualcompositor.DomainScopeWire `json:"scope"`
	Limits                  audiograph.Limits                `json:"limits"`
	Offered                 videoedit.BackendCapabilities    `json:"offered"`
	DomainDescriptorHash    string                           `json:"domain_descriptor_hash"`
	EvaluatorDescriptorHash string                           `json:"evaluator_descriptor_hash"`
	DSPDescriptorHash       string                           `json:"dsp_descriptor_hash"`
	ConfigHash              string                           `json:"config_hash"`
	Sources                 []audiograph.DomainSourceReceipt `json:"sources,omitempty"`
	OriginalReceiptHash     string                           `json:"original_receipt_hash"`
}

func bridgeScope(scope evaluator.DomainScope) visualcompositor.DomainScopeWire {
	return visualcompositor.DomainScopeWire{Domain: scope.Domain, RequestedRange: scope.RequestedRange, Selection: visualcompositor.DomainSelectionWire{TimelineID: scope.Selection.TimelineID, ClipIDs: append([]documentv2.ID(nil), scope.Selection.ClipIDs...), TransitionIDs: append([]documentv2.ID(nil), scope.Selection.TransitionIDs...)}}
}

func buildBridge(ctx context.Context, env prepared.EnvironmentV2, configHash string, visual *referencevisual.Program, audio *audiograph.DomainProgram, wav *wavsource.Provider, pcmLimits pcm16le.Limits) (result Bridge, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = Bridge{}
		}
	}()
	if ctx == nil {
		return result, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if !bridgeSHA(configHash) || visual == nil || audio == nil || wav == nil {
		return result, ErrBridge
	}
	verified, err := prepared.VerifyEnvironmentV2(ctx, env)
	if err != nil {
		return result, err
	}
	key := verified.Key()
	scopes, err := selectScopes(ctx, env.Document, env.Plan)
	if err != nil {
		return result, err
	}
	vm, ar, wm := visual.SourceManifest(), audio.Receipt(), wav.Manifest()
	if visual.Profile() != referencevisual.DomainProfileV1 || visual.DocumentHash() != key.DocumentHash || audio.DocumentHash() != key.DocumentHash || vm.DocumentHash != key.DocumentHash || vm.NativePNG.DocumentHash != key.DocumentHash || vm.Expression.DocumentHash != key.DocumentHash || vm.Text == nil || vm.Text.DocumentHash != key.DocumentHash || ar.DocumentHash != key.DocumentHash || wm.DocumentHash != key.DocumentHash ||
		!reflect.DeepEqual(bridgeScope(visual.Scope()), bridgeScope(scopes.visual)) || !reflect.DeepEqual(bridgeScope(audio.Scope()), bridgeScope(scopes.audio)) || !reflect.DeepEqual(bridgeScope(ar.Scope), bridgeScope(scopes.audio)) ||
		vm.Profile != referencevisual.DomainManifestProfileV1 || vm.DomainScope == nil || vm.DomainScope.Fingerprint != visual.DomainScopeHash() || !reflect.DeepEqual(vm.DomainScope.Scope, bridgeScope(scopes.visual)) || ar.Profile != audiograph.DomainCompileProfileV1 || !bridgeSHA(ar.ReceiptHash) || wm.Profile != wavsource.ProfileV1 ||
		visual.Config().Width != int(env.Manifest.Output.Width) || visual.Config().Height != int(env.Manifest.Output.Height) {
		return result, ErrBridge
	}
	if len(ar.Sources) != len(wm.Assets) {
		return result, ErrBridge
	}
	for index, source := range ar.Sources {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		asset := wm.Assets[index]
		if source.AssetID != asset.AssetID || source.AssetIndex != asset.AssetIndex || source.ManifestFingerprint != asset.ManifestFingerprint || source.ResourceContentHash != asset.EncodedContentHash || source.SampleRate != int64(asset.SampleRate) || source.ChannelLayout != asset.ChannelLayout || !reflect.DeepEqual(source.DecoderChannels, asset.DecoderChannels) || source.DecodeProfile != asset.DecodeProfile || source.SelectionProfile != asset.SelectionProfile || source.RetimeProfile != asset.RetimeProfile {
			return result, ErrBridge
		}
	}
	if ar.DomainDescriptorHash == "" || ar.EvaluatorDescriptorHash == "" || ar.DSPDescriptorHash == "" {
		return result, ErrBridge
	}
	if pcmLimits.MaxFrames == 0 || pcmLimits.MaxFrames > pcm16le.HardMaxFrames || pcmLimits.MaxOutputBytes < pcm16le.BytesPerFrame || pcmLimits.MaxOutputBytes > pcm16le.HardMaxOutputBytes || pcmLimits.MaxExactBits <= 0 || pcmLimits.MaxExactBits > pcm16le.HardMaxExactBits {
		return result, ErrBridge
	}
	// Receipt identity is native evidence, not a claim that the S2 config equals native limits.
	originalReceiptHash := ar.ReceiptHash
	ar.ReceiptHash = ""
	actualReceiptHash, e := bridgeDigest(ctx, ar)
	if e != nil {
		return result, e
	}
	if actualReceiptHash != originalReceiptHash {
		return result, ErrBridge
	}
	result = Bridge{SchemaVersion: 1, Profile: BridgeProfileV1, DocumentHash: key.DocumentHash, PlanFingerprint: key.PlanFingerprint, ExecutionManifestFingerprint: key.ExecutionManifestFingerprint, HostConfigHash: configHash, CompilerBuild: env.Manifest.CompilerBuild, EvaluatorBuild: env.Manifest.EvaluatorBuild, ProviderBuild: env.Manifest.ProviderBuild, Backend: env.Manifest.Backend, MediaRuntime: env.Manifest.MediaRuntime, DeterministicSeed: env.Manifest.DeterministicSeed, TimelineID: env.Plan.TimelineID, RequestedRange: env.Plan.RequestedRange, FrameRange: env.Plan.FrameRange, SampleRange: env.Plan.SampleRange, Output: env.Manifest.Output, SemanticVisual: env.Manifest.Visual, SemanticAudio: env.Manifest.Audio, ValidatedResources: append([]renderapi.MaterializedResourceV2(nil), env.Manifest.Resources...), EvidenceSemantics: "Factory checked actual retained resource bytes and inspection/probe identities against these sorted claims before construction; native source manifests record actual decode/shape observations. This unsigned bridge alone proves neither those checks nor pixel/sample causality, host authority or durable execution authorization."}
	sort.Slice(result.ValidatedResources, func(i, j int) bool {
		return result.ValidatedResources[i].AssetID < result.ValidatedResources[j].AssetID
	})
	if result.ValidatedResources == nil {
		result.ValidatedResources = []renderapi.MaterializedResourceV2{}
	}
	result.NativeVisual = NativeVisualBridge{Enabled: env.Manifest.Visual.Enabled, Profile: visual.Profile(), Config: visual.Config(), SourceManifest: vm}
	result.NativeAudio = NativeAudioBridge{Enabled: env.Manifest.Audio.Enabled, Profile: audiograph.DomainCompileProfileV1, Receipt: NativeAudioReceiptBridge{SchemaVersion: ar.SchemaVersion, Profile: ar.Profile, DocumentHash: ar.DocumentHash, Scope: bridgeScope(ar.Scope), Limits: ar.Limits, Offered: ar.Offered, DomainDescriptorHash: ar.DomainDescriptorHash, EvaluatorDescriptorHash: ar.EvaluatorDescriptorHash, DSPDescriptorHash: ar.DSPDescriptorHash, ConfigHash: ar.ConfigHash, Sources: ar.Sources, OriginalReceiptHash: originalReceiptHash}, WAVManifest: wm, PCMConfig: pcmLimits}
	for _, item := range []struct {
		value  any
		target *string
	}{
		{referencevisual.DomainDescriptorV2(), &result.NativeVisual.DescriptorHash}, {visualcompositor.DomainDescriptor(), &result.NativeVisual.CompositorDescriptorHash}, {result.NativeVisual.Config, &result.NativeVisual.ConfigHash}, {vm, &result.NativeVisual.SourceManifestHash},
		{audiograph.DomainDescriptor(), &result.NativeAudio.DescriptorHash}, {result.NativeAudio.Receipt, &result.NativeAudio.ReceiptProjectionHash}, {wavsource.Descriptor(), &result.NativeAudio.WAVDescriptorHash}, {wm, &result.NativeAudio.WAVManifestHash}, {pcm16le.Descriptor(), &result.NativeAudio.PCMDescriptorHash}, {pcmLimits, &result.NativeAudio.PCMConfigHash},
	} {
		*item.target, err = bridgeDigest(ctx, item.value)
		if err != nil {
			return Bridge{}, err
		}
	}
	result.Fingerprint, err = bridgeDigest(ctx, result)
	if err != nil {
		return Bridge{}, err
	}
	// Canonical round-trip owns every pointer, slice and RawMessage, including semantic selections.
	encoded, err := canonicalBridge(ctx, result)
	if err != nil {
		return Bridge{}, err
	}
	var owned Bridge
	if err = json.Unmarshal(encoded, &owned); err != nil {
		return Bridge{}, err
	}
	if err = ctx.Err(); err != nil {
		return Bridge{}, err
	}
	return owned, nil
}

// canonicalBridge checks its self identity and returns fresh canonical bytes. It is not a bridge
// verifier against native programs, resource bytes, host policy or an execution environment.
func canonicalBridge(ctx context.Context, value Bridge) (encoded []byte, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			encoded = nil
		}
	}()
	if ctx == nil {
		return nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	fingerprint := value.Fingerprint
	value.Fingerprint = ""
	if value.SchemaVersion != 1 || value.Profile != BridgeProfileV1 || !bridgeSHA(fingerprint) {
		return nil, ErrBridge
	}
	actual, err := bridgeDigest(ctx, value)
	if err != nil {
		return nil, err
	}
	if actual != fingerprint {
		return nil, ErrBridge
	}
	value.Fingerprint = fingerprint
	encoded, err = videoedit.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return encoded, nil
}

func bridgeDigest(ctx context.Context, value any) (digest string, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			digest = ""
		}
	}()
	if ctx == nil {
		return "", ErrContext
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	encoded, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for offset := 0; offset < len(encoded); offset += 64 << 10 {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		end := offset + (64 << 10)
		if end > len(encoded) {
			end = len(encoded)
		}
		_, _ = h.Write(encoded[offset:end])
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func bridgeSHA(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
