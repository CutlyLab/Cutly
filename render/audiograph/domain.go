package audiograph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sort"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	DomainCompileProfileV1 = "cutly.audio.domain-compile.v1"
	CodeDomainInvalid      = "AUDIOGRAPH.DOMAIN.V1.INVALID"
	CodeDomainPanic        = "AUDIOGRAPH.DOMAIN.V1.PANIC"
)

type clipEvaluator interface {
	EvaluateClip(documentv2.ID, documentv2.ID, documentv2.RationalTime) (evaluator.ClipEvaluation, error)
}

// DomainProfileDescriptor versions compile authority, not DSP numeric semantics.
// Domain compilation deliberately supports a narrower graph than legacy Compile.
type DomainProfileDescriptor struct {
	SchemaVersion     int    `json:"schema_version"`
	Profile           string `json:"profile"`
	DocumentSemantics string `json:"document_semantics"`
	ScopeSemantics    string `json:"scope_semantics"`
	SupportedGraph    string `json:"supported_graph"`
	GuardSemantics    string `json:"guard_semantics"`
	ReceiptSemantics  string `json:"receipt_semantics"`
	FailureSemantics  string `json:"failure_semantics"`
}

func DomainDescriptor() DomainProfileDescriptor {
	return DomainProfileDescriptor{
		SchemaVersion: 1, Profile: DomainCompileProfileV1,
		DocumentSemantics: "Admit and structurally validate the complete original Document with evaluator.CompileDomainSnapshot; never prune, substitute a trusted hash, or relax legacy Compile. Retain original asset/timeline/track/clip indexes and canonical document identity. Caller inputs are borrowed immutable until CompileDomain returns.",
		ScopeSemantics:    "Audio only; exact positive half-open requested range and complete enabled same-domain intersection checked by evaluator against a fixed trusted caller declaration of offered capabilities. Compile only selected clips and require only their distinct asset bindings; reject extra bindings. No visual capability is inferred or claimed.",
		SupportedGraph:    "V1 supports implicit direct master, exact mono/stereo or explicit channel matrix, clip gain/pan and their existing automation. Reject every Document AudioBus, selected rendering extension, and selected nonunit source/timeline duration ratio. Evaluator rejects every transition on the selected timeline. Unselected authored audio and all visual sources remain structurally validated but are not bound or rendered.",
		GuardSemantics:    "Before provider IO or block allocation, check audio authority, selected timeline and exact half-open time membership even in silent gaps. Sample indexes and block first/count must be nonnegative and overflow-free. Check exact SampleClock start and exclusive block endpoint, not rounded ticks or last sample alone; endpoint may equal requested end. Zero blocks require their start strictly inside requested range and a valid clock/index. Limits bound all original input admission and block memory, not RSS or synchronous validation/canonicalization execution time.",
		ReceiptSemantics:  "Owned receipt binds original Document hash, sorted owned scope, normalized limits, fixed offered declaration, separate domain/evaluator/DSP descriptors and actual selected asset IDs/indexes, manifest fingerprints, resources, streams, decoder order and source profiles. Receipt hash omits the receipt_hash member entirely. Not provider attestation. No raw graph, evaluator, native retained struct pointer or snapshot escapes.",
		FailureSemantics:  "Nil context, cancellation, invalid inputs, limits, unsupported graph and unexpected panic return zero authority/results. Context checked through admission, binding and timeline loops and before publication; provider/context panic values are never formatted. Owned getters and immutable state support concurrent readers. Evaluation DSP ProfileV1 and legacy profiles/fixtures remain unchanged.",
	}
}

// DomainSourceReceipt identifies an actually compiled source, not an asset alias.
type DomainSourceReceipt struct {
	AssetID             documentv2.ID        `json:"asset_id"`
	AssetIndex          int                  `json:"asset_index"`
	AssetKind           documentv2.AssetKind `json:"asset_kind"`
	ManifestFingerprint string               `json:"manifest_fingerprint"`
	ResourceContentHash string               `json:"resource_content_hash"`
	ResourceSizeBytes   int64                `json:"resource_size_bytes"`
	StreamIndex         int                  `json:"stream_index"`
	SampleRate          int64                `json:"sample_rate"`
	ChannelLayout       string               `json:"channel_layout"`
	DecoderChannels     []string             `json:"decoder_channels"`
	DecodeProfile       string               `json:"decode_profile"`
	SelectionProfile    string               `json:"selection_profile"`
	RetimeProfile       string               `json:"retime_profile"`
}

type DomainCompileReceipt struct {
	SchemaVersion           int                           `json:"schema_version"`
	Profile                 string                        `json:"profile"`
	DocumentHash            string                        `json:"document_hash"`
	Scope                   evaluator.DomainScope         `json:"scope"`
	Limits                  Limits                        `json:"limits"`
	Offered                 videoedit.BackendCapabilities `json:"offered"`
	DomainDescriptorHash    string                        `json:"domain_descriptor_hash"`
	EvaluatorDescriptorHash string                        `json:"evaluator_descriptor_hash"`
	DSPDescriptorHash       string                        `json:"dsp_descriptor_hash"`
	ConfigHash              string                        `json:"config_hash"`
	Sources                 []DomainSourceReceipt         `json:"sources,omitempty"`
	ReceiptHash             string                        `json:"receipt_hash,omitempty"`
}

// DomainProgram is opaque scoped audio authority; its zero value authorizes nothing.
type DomainProgram struct {
	graph *Program
	scope evaluator.DomainScope
	// Canonical owned bytes avoid exposing any retained mutable native struct storage.
	receipt []byte
}

func (p *DomainProgram) DocumentHash() string {
	if p == nil || p.graph == nil {
		return ""
	}
	return p.graph.documentHash
}
func (p *DomainProgram) Scope() evaluator.DomainScope {
	if p == nil || p.graph == nil {
		return evaluator.DomainScope{}
	}
	s := p.scope
	s.Selection.ClipIDs = append([]documentv2.ID(nil), s.Selection.ClipIDs...)
	s.Selection.TransitionIDs = append([]documentv2.ID(nil), s.Selection.TransitionIDs...)
	return s
}
func (p *DomainProgram) Receipt() DomainCompileReceipt {
	if p == nil || p.graph == nil {
		return DomainCompileReceipt{}
	}
	var r DomainCompileReceipt
	// Bytes were encoded successfully before publication; decoding returns fresh owned storage.
	if json.Unmarshal(p.receipt, &r) != nil {
		return DomainCompileReceipt{}
	}
	return r
}
func domainCanceledDiagnostic() documentv2.Diagnostic {
	return compileDiagnostic(string(CodeCanceled), "/", "audio domain compilation canceled")
}
func domainInvalid(message string) []documentv2.Diagnostic {
	return []documentv2.Diagnostic{compileDiagnostic(CodeDomainInvalid, "/", message)}
}

// DomainPreflightReceipt is resource-free admission evidence, not render authority.
type DomainPreflightReceipt struct {
	DocumentHash string                `json:"document_hash"`
	Scope        evaluator.DomainScope `json:"scope"`
	Limits       Limits                `json:"limits"`
}

// PreflightDomain shares every document/scope/capability/subset check with CompileDomain.
// It performs no resource callbacks. Compilation additionally admits and verifies media bindings,
// manifests, resource hashes, streams, channel order, source profiles and clip/channel compatibility.
func PreflightDomain(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, offered videoedit.BackendCapabilities, requested Limits) (result DomainPreflightReceipt, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if recover() != nil {
			result = DomainPreflightReceipt{}
			diagnostics = []documentv2.Diagnostic{compileDiagnostic(CodeDomainPanic, "/", "audio domain preflight panicked")}
		}
	}()
	authority, snapshot, limits, diagnostics := preflightDomain(ctx, original, scope, offered, requested)
	if authority == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return DomainPreflightReceipt{}, diagnostics
	}
	result = DomainPreflightReceipt{DocumentHash: snapshot.DocumentHash(), Scope: authority.Scope(), Limits: limits}
	if ctx.Err() != nil {
		return DomainPreflightReceipt{}, []documentv2.Diagnostic{domainCanceledDiagnostic()}
	}
	return result, diagnostics
}

func preflightDomain(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, offered videoedit.BackendCapabilities, requested Limits) (*evaluator.DomainProgram, *evaluator.FrozenSnapshot, Limits, []documentv2.Diagnostic) {
	return preflightDomainVersion(ctx, original, scope, offered, requested, false)
}

func preflightDomainVersion(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, offered videoedit.BackendCapabilities, requested Limits, v2 bool) (*evaluator.DomainProgram, *evaluator.FrozenSnapshot, Limits, []documentv2.Diagnostic) {
	invalid := domainInvalid
	compileSnapshot := evaluator.CompileDomainSnapshot
	if v2 {
		invalid = domainInvalidV2
		compileSnapshot = evaluator.CompileDomainSnapshotV2
	}
	fail := func(d []documentv2.Diagnostic) (*evaluator.DomainProgram, *evaluator.FrozenSnapshot, Limits, []documentv2.Diagnostic) {
		return nil, nil, Limits{}, d
	}
	if ctx == nil {
		return fail(invalid("non-nil context is required"))
	}
	if ctx.Err() != nil {
		return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
	}
	limits, diagnostics := normalizeLimits(requested)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return fail(diagnostics)
	}
	diagnostics = preflightRawLimitsContext(ctx, original, offered, nil, limits)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return fail(diagnostics)
	}
	if scope.Domain != evaluator.DomainAudio {
		return fail(invalid("audio domain is required"))
	}
	authority, snapshot, diagnostics := compileSnapshot(ctx, original, scope, offered, evaluator.SnapshotLimits{MaxCanonicalDocumentBytes: limits.MaxCanonicalDocumentBytes, MaxDocumentNodes: uint64(limits.MaxDocumentNodes)})
	if authority == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return fail(diagnostics)
	}
	if !v2 && len(original.AudioBuses) != 0 {
		return fail(invalid("audio domain v1 does not support AudioBuses"))
	}
	selected := make(map[documentv2.ID]bool, len(scope.Selection.ClipIDs))
	for _, id := range scope.Selection.ClipIDs {
		if ctx.Err() != nil {
			return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
		}
		selected[id] = true
	}
	for _, tl := range original.Timelines {
		if ctx.Err() != nil {
			return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
		}
		if tl.ID != scope.Selection.TimelineID {
			continue
		}
		for _, tr := range tl.Tracks {
			for _, cl := range tr.Clips {
				if ctx.Err() != nil {
					return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
				}
				if !selected[cl.ID] {
					continue
				}
				equal, err := cl.SourceRange.Duration.Compare(cl.TimelineRange.Duration)
				if err != nil || equal != 0 {
					message := "audio domain v1 requires unit speed"
					if v2 {
						message = "audio domain v2 requires unit speed"
					}
					return fail(invalid(message))
				}
				if cl.PropertySet != nil {
					for _, x := range cl.PropertySet.Extensions {
						if ctx.Err() != nil {
							return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
						}
						if x.AffectsRendering {
							message := "audio domain v1 has no rendering extension consumer"
							if v2 {
								message = "audio domain v2 has no rendering extension consumer"
							}
							return fail(invalid(message))
						}
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return fail([]documentv2.Diagnostic{domainCanceledDiagnostic()})
	}
	return authority, snapshot, limits, diagnostics
}

// CompileDomain uses the independently checked full-document evaluator snapshot. Inputs must not
// be mutated concurrently with compilation; after return no caller mutable storage is retained.
func CompileDomain(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, options CompileOptions) (*DomainProgram, []documentv2.Diagnostic) {
	return compileDomainVersion(ctx, original, scope, options, false)
}

func compileDomainVersion(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, options CompileOptions, v2 bool) (result *DomainProgram, diagnostics []documentv2.Diagnostic) {
	invalid, panicCode := domainInvalid, CodeDomainPanic
	if v2 {
		invalid, panicCode = domainInvalidV2, CodeDomainPanicV2
	}
	defer func() {
		if recover() != nil {
			result = nil
			diagnostics = []documentv2.Diagnostic{compileDiagnostic(panicCode, "/", "audio domain compilation panicked")}
		}
	}()
	if ctx == nil {
		return nil, invalid("non-nil context is required")
	}
	if ctx.Err() != nil {
		return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
	}
	limits, diagnostics := normalizeLimits(options.Limits)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	if len(options.Bindings) > limits.MaxBindings {
		return nil, []documentv2.Diagnostic{compileDiagnostic(CodeCompileLimit, "/bindings", "binding count exceeds compile limit")}
	}
	diagnostics = preflightRawLimitsContext(ctx, original, options.Offered, options.Bindings, limits)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	// Admit complete binding strings as well as manifests before freeze, hashing or copying.
	for _, b := range options.Bindings {
		if _, err := preflightValueLimitsContext(ctx, b, uint64(limits.MaxProbeStreamsPerBinding)*16+128, limits.MaxCanonicalManifestBytes); err != nil {
			return nil, invalid("binding admission failed")
		}
	}
	authority, snapshot, limits, diagnostics := preflightDomainVersion(ctx, original, scope, options.Offered, limits, v2)
	if authority == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	scope = authority.Scope()
	selected := make(map[documentv2.ID]bool, len(scope.Selection.ClipIDs))
	for _, id := range scope.Selection.ClipIDs {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		selected[id] = true
	}
	// The original is still borrowed immutable. Only binding validation reads its properties; the
	// evaluator owns the canonical frozen properties and all runtime clip evaluation.
	assets := make(map[documentv2.ID]documentv2.Asset)
	indexes := make(map[documentv2.ID]int)
	for i, a := range original.Assets {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		indexes[a.ID] = i
	}
	needed := make(map[documentv2.ID]bool)
	var buses []compiledBus
	masterBus := -1
	if v2 {
		var err error
		buses, masterBus, err = compileDomainBuses(ctx, original, scope, selected)
		if err != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
	}
	busIndexes := make(map[documentv2.ID]int, len(buses))
	for i, bus := range buses {
		busIndexes[bus.id] = i
	}
	compiled := compiledTimeline{}
	for ti, tl := range original.Timelines {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		if tl.ID != scope.Selection.TimelineID {
			continue
		}
		compiled.id, compiled.index = tl.ID, ti
		for tri, tr := range tl.Tracks {
			if ctx.Err() != nil {
				return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
			}
			if tr.Kind != documentv2.TrackKindAudio {
				continue
			}
			track := compiledTrack{id: tr.ID, index: tri, busIndex: -1}
			if index, exists := busIndexes[tr.AudioBusID]; exists {
				track.busIndex = index
			}
			for ci, cl := range tr.Clips {
				if ctx.Err() != nil {
					return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
				}
				if !selected[cl.ID] {
					continue
				}
				needed[cl.AssetID] = true
				end, _ := cl.TimelineRange.End()
				track.clips = append(track.clips, compiledClip{id: cl.ID, assetID: cl.AssetID, trackID: tr.ID, trackIndex: tri, clipIndex: ci, start: new(big.Rat).SetFrac64(cl.TimelineRange.Start.Value, cl.TimelineRange.Start.Rate), end: new(big.Rat).SetFrac64(end.Value, end.Rate), usesMatrix: cl.PropertySet != nil && cl.PropertySet.AudioChannelMatrix != nil})
			}
			if len(track.clips) != 0 {
				compiled.tracks = append(compiled.tracks, track)
			}
		}
	}
	for _, a := range original.Assets {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		if needed[a.ID] {
			assets[a.ID] = a
		}
	}
	for _, b := range options.Bindings {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		if !needed[b.AssetID] {
			return nil, invalid("binding does not belong to a selected audio asset")
		}
	}
	bindings := compileBindingsContext(ctx, options.Bindings, assets, limits, &diagnostics)
	for id, b := range bindings {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		b.assetIndex = indexes[id]
		bindings[id] = b
	}
	for _, tl := range original.Timelines {
		if tl.ID != scope.Selection.TimelineID {
			continue
		}
		for _, tr := range tl.Tracks {
			for _, cl := range tr.Clips {
				if ctx.Err() != nil {
					return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
				}
				if !selected[cl.ID] {
					continue
				}
				b, ok := bindings[cl.AssetID]
				if !ok {
					diagnostics = append(diagnostics, compileDiagnostic(CodeBindingMissing, "/bindings", "selected audio asset has no media binding"))
					continue
				}
				validateClipBinding(assets[cl.AssetID], cl, b, "/bindings", &diagnostics)
			}
		}
	}
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics
	}
	graph := &Program{documentHash: snapshot.DocumentHash(), evaluator: authority, bindings: bindings, timelines: map[documentv2.ID]compiledTimeline{compiled.id: compiled}, buses: buses, masterBus: masterBus, limits: limits}
	receipt := DomainCompileReceipt{SchemaVersion: 1, Profile: DomainCompileProfileV1, DocumentHash: graph.documentHash, Scope: scope, Limits: limits, Offered: options.Offered}
	domainDescriptor, evaluatorDescriptor := DomainDescriptor(), evaluator.DomainSnapshotDescriptor()
	if v2 {
		receipt.SchemaVersion, receipt.Profile = 2, DomainCompileProfileV2
		domainDescriptor, evaluatorDescriptor = DomainDescriptorV2(), evaluator.DomainSnapshotDescriptorV2()
	}
	var err error
	if receipt.DomainDescriptorHash, err = domainHash(domainDescriptor); err != nil {
		return nil, invalid("descriptor identity failed")
	}
	if receipt.EvaluatorDescriptorHash, err = domainHash(evaluatorDescriptor); err != nil {
		return nil, invalid("descriptor identity failed")
	}
	if receipt.DSPDescriptorHash, err = domainHash(Descriptor()); err != nil {
		return nil, invalid("descriptor identity failed")
	}
	ids := make([]documentv2.ID, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
		}
		b := bindings[id]
		receipt.Sources = append(receipt.Sources, DomainSourceReceipt{AssetID: id, AssetIndex: b.assetIndex, AssetKind: b.assetKind, ManifestFingerprint: b.manifestHash, ResourceContentHash: b.manifest.Resource.ContentHash, ResourceSizeBytes: b.manifest.Resource.SizeBytes, StreamIndex: b.streamIndex, SampleRate: b.audio.SampleRate, ChannelLayout: b.audio.ChannelLayout, DecoderChannels: b.decoderChannels, DecodeProfile: b.decodeProfile, SelectionProfile: b.selectionProfile, RetimeProfile: b.retimeProfile})
	}
	receipt.ConfigHash, err = domainHash(struct {
		Limits  Limits                        `json:"limits"`
		Offered videoedit.BackendCapabilities `json:"offered"`
		Sources []DomainSourceReceipt         `json:"sources,omitempty"`
	}{limits, options.Offered, receipt.Sources})
	if err != nil {
		return nil, invalid("config identity failed")
	}
	receipt.ReceiptHash, err = domainHash(receipt)
	if err != nil {
		return nil, invalid("receipt identity failed")
	}
	encoded, err := videoedit.CanonicalJSON(receipt)
	if err != nil {
		return nil, invalid("receipt freeze failed")
	}
	if ctx.Err() != nil {
		return nil, []documentv2.Diagnostic{domainCanceledDiagnostic()}
	}
	return &DomainProgram{graph: graph, scope: scope, receipt: encoded}, diagnostics
}

func domainHash(value any) (string, error) {
	b, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

func (p *DomainProgram) guard(ctx context.Context, timeline documentv2.ID, at documentv2.RationalTime, endpoint bool) error {
	if err := checkContext(ctx, "domain"); err != nil {
		return err
	}
	if p == nil || p.graph == nil || p.scope.Domain != evaluator.DomainAudio || timeline != p.scope.Selection.TimelineID {
		return runtimeError(CodeProgramLookup, "domain", ErrProgramLookup)
	}
	if at.Validate() != nil {
		return runtimeError(CodeInputInvalid, "domain.time", ErrInputInvalid)
	}
	end, err := p.scope.RequestedRange.End()
	if err != nil {
		return runtimeError(CodeTimeOverflow, "domain.range", ErrTimeOverflow)
	}
	lo, err := at.Compare(p.scope.RequestedRange.Start)
	if err != nil {
		return runtimeError(CodeTimeOverflow, "domain.time", ErrTimeOverflow)
	}
	hi, err := at.Compare(end)
	if err != nil {
		return runtimeError(CodeTimeOverflow, "domain.time", ErrTimeOverflow)
	}
	if lo < 0 || hi > 0 || hi == 0 && !endpoint {
		return runtimeError(CodeInputInvalid, "domain.range", ErrInputInvalid)
	}
	return nil
}
func domainSampleTime(clock videoedit.SampleClock, index int64) (documentv2.RationalTime, error) {
	if index < 0 {
		return documentv2.RationalTime{}, runtimeError(CodeInputInvalid, "sample_index", ErrInputInvalid)
	}
	at, err := videoedit.SampleTimeAt(clock, index)
	if err != nil {
		if errors.Is(err, videoedit.ErrTimeOverflow) {
			return documentv2.RationalTime{}, runtimeError(CodeTimeOverflow, "sample_clock", ErrTimeOverflow)
		}
		return documentv2.RationalTime{}, runtimeError(CodeInputInvalid, "sample_clock", ErrInputInvalid)
	}
	return at, nil
}
func domainPanicError() error { return runtimeError(CodeInputInvalid, "domain.panic", ErrInputInvalid) }
func (p *DomainProgram) EvaluateAt(ctx context.Context, timeline documentv2.ID, at documentv2.RationalTime, provider SourceProvider) (result Evaluation, err error) {
	defer func() {
		if recover() != nil {
			result = Evaluation{}
			err = domainPanicError()
		}
	}()
	if err = p.guard(ctx, timeline, at, false); err != nil {
		return Evaluation{}, err
	}
	result, err = p.graph.EvaluateAt(ctx, timeline, at, provider)
	if err != nil {
		return Evaluation{}, err
	}
	if err = checkContext(ctx, "domain.final"); err != nil {
		return Evaluation{}, err
	}
	return result, nil
}
func (p *DomainProgram) EvaluateSample(ctx context.Context, timeline documentv2.ID, clock videoedit.SampleClock, index int64, provider SourceProvider) (result SampleEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = SampleEvaluation{}
			err = domainPanicError()
		}
	}()
	if err = checkContext(ctx, "domain"); err != nil {
		return SampleEvaluation{}, err
	}
	at, err := domainSampleTime(clock, index)
	if err != nil {
		return SampleEvaluation{}, err
	}
	value, err := p.EvaluateAt(ctx, timeline, at, provider)
	if err != nil {
		return SampleEvaluation{}, err
	}
	return SampleEvaluation{CompositionSampleIndex: index, Evaluation: value}, nil
}
func (p *DomainProgram) EvaluateBlock(ctx context.Context, timeline documentv2.ID, clock videoedit.SampleClock, first, count int64, provider SourceProvider) (result BlockEvaluation, err error) {
	defer func() {
		if recover() != nil {
			result = BlockEvaluation{}
			err = domainPanicError()
		}
	}()
	if err = checkContext(ctx, "domain"); err != nil {
		return BlockEvaluation{}, err
	}
	ticks := videoedit.TickRange{First: first, Count: count}
	if first < 0 || ticks.Validate() != nil {
		return BlockEvaluation{}, runtimeError(CodeInputInvalid, "sample_range", ErrInputInvalid)
	}
	start, err := domainSampleTime(clock, first)
	if err != nil {
		return BlockEvaluation{}, err
	}
	if err = p.guard(ctx, timeline, start, false); err != nil {
		return BlockEvaluation{}, err
	}
	// TickRange.Validate checked first+count overflow. Check the exact exclusive endpoint before
	// delegating to the bounded allocator; a last-sample-only check would authorize partial cells.
	end, err := domainSampleTime(clock, first+count)
	if err != nil {
		return BlockEvaluation{}, err
	}
	if err = p.guard(ctx, timeline, end, true); err != nil {
		return BlockEvaluation{}, err
	}
	result, err = p.graph.EvaluateBlock(ctx, timeline, clock, first, count, provider)
	if err != nil {
		return BlockEvaluation{}, err
	}
	if err = checkContext(ctx, "domain.final"); err != nil {
		return BlockEvaluation{}, err
	}
	return result, nil
}
