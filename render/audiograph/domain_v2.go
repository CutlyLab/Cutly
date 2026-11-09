package audiograph

import (
	"context"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	DomainCompileProfileV2 = "cutly.audio.domain-compile.v2"
	CodeDomainInvalidV2    = "AUDIOGRAPH.DOMAIN.V2.INVALID"
	CodeDomainPanicV2      = "AUDIOGRAPH.DOMAIN.V2.PANIC"
)

// DomainDescriptorV2 versions scoped admission independently of the unchanged DSP profile.
func DomainDescriptorV2() DomainProfileDescriptor {
	return DomainProfileDescriptor{
		SchemaVersion: 2, Profile: DomainCompileProfileV2,
		DocumentSemantics: "Admit and structurally validate the complete original Document with evaluator.CompileDomainSnapshotV2; never prune the Document, substitute a trusted hash, infer offered capabilities, or relax V1. Retain original asset/timeline/track/clip/bus indexes and canonical document identity. Inputs are borrowed immutable until compilation returns.",
		ScopeSemantics:    "Audio only; exact positive half-open requested range and complete enabled same-domain clip intersection independently checked by evaluator against caller-supplied fixed offered capabilities. Bind only distinct selected audio assets and reject extra bindings. No visual IO or visual rendering authority. Selected TransitionIDs must be empty; purely visual authored transitions may coexist but never authorize audio transitions or transition handles.",
		SupportedGraph:    "Reuse existing DSP ProfileV1: implicit direct master or explicit AudioBus routes, ancestor groups and master; exact mono/stereo or explicit channel matrix; clip gain/pan and their existing automation; bus gain/pan and master sample-peak limiter. Compile only selected route roots and their ancestors in original Document.AudioBuses order, preserving original BusIndex and direct-track/child-bus order. Empty selections compile silent implicit master with no bus capability claim. Reject selected rendering extensions and nonunit source/timeline duration ratios. Unselected audio and visual graphs remain fully structurally validated but are not bound or rendered.",
		GuardSemantics:    DomainDescriptor().GuardSemantics,
		ReceiptSemantics:  "Owned schema-version-2 receipt binds original Document hash, sorted owned scope, normalized limits, the unchanged fixed offered declaration, separate V2 domain/evaluator and unchanged DSP descriptor hashes, and actually selected source identities with original indexes and binding profiles. Scope plus full Document hash binds selected bus routing. Receipt hash omits receipt_hash entirely. No provider attestation, raw graph, evaluator, snapshot or retained mutable pointer escapes.",
		FailureSemantics:  DomainDescriptor().FailureSemantics,
	}
}

func domainInvalidV2(message string) []documentv2.Diagnostic {
	return []documentv2.Diagnostic{compileDiagnostic(CodeDomainInvalidV2, "/", message)}
}

// CompileDomainV2 admits the full original Document and compiles only scoped audio DSP.
// The returned opaque program uses the same exact runtime guards and owned getters as V1.
func CompileDomainV2(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, options CompileOptions) (*DomainProgram, []documentv2.Diagnostic) {
	return compileDomainVersion(ctx, original, scope, options, true)
}

// PreflightDomainV2 is resource-free evidence, not render authority. Media binding admission
// and stream/channel/profile checks are additionally required by CompileDomainV2.
func PreflightDomainV2(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, offered videoedit.BackendCapabilities, requested Limits) (result DomainPreflightReceipt, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if recover() != nil {
			result = DomainPreflightReceipt{}
			diagnostics = []documentv2.Diagnostic{compileDiagnostic(CodeDomainPanicV2, "/", "audio domain preflight panicked")}
		}
	}()
	authority, snapshot, limits, diagnostics := preflightDomainVersion(ctx, original, scope, offered, requested, true)
	if authority == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return DomainPreflightReceipt{}, diagnostics
	}
	result = DomainPreflightReceipt{DocumentHash: snapshot.DocumentHash(), Scope: authority.Scope(), Limits: limits}
	if ctx.Err() != nil {
		return DomainPreflightReceipt{}, []documentv2.Diagnostic{domainCanceledDiagnostic()}
	}
	return result, diagnostics
}

// compileDomainBuses retains only selected route dependencies, never an unoffered unrelated
// bus branch. compileBuses owns all numeric conversion; this adapter preserves authored indexes
// while remapping only private traversal indexes. Full-document validation proved parent closure.
func compileDomainBuses(ctx context.Context, original documentv2.Document, scope evaluator.DomainScope, selected map[documentv2.ID]bool) ([]compiledBus, int, error) {
	if len(original.AudioBuses) == 0 || len(selected) == 0 {
		return nil, -1, nil
	}
	indexes := make(map[documentv2.ID]int, len(original.AudioBuses))
	for i, bus := range original.AudioBuses {
		if err := checkContext(ctx, "domain.buses"); err != nil {
			return nil, -1, err
		}
		indexes[bus.ID] = i
	}
	needed := make(map[int]bool)
	for _, timeline := range original.Timelines {
		if timeline.ID != scope.Selection.TimelineID {
			continue
		}
		for _, track := range timeline.Tracks {
			if err := checkContext(ctx, "domain.routes"); err != nil {
				return nil, -1, err
			}
			for _, clip := range track.Clips {
				if err := checkContext(ctx, "domain.routes"); err != nil {
					return nil, -1, err
				}
				if !selected[clip.ID] {
					continue
				}
				for id := track.AudioBusID; id != ""; {
					if err := checkContext(ctx, "domain.ancestors"); err != nil {
						return nil, -1, err
					}
					i := indexes[id]
					if needed[i] {
						break
					}
					needed[i] = true
					id = original.AudioBuses[i].ParentBusID
				}
			}
		}
	}
	values := make([]documentv2.AudioBus, 0, len(needed))
	originalIndexes := make([]int, 0, len(needed))
	for i, bus := range original.AudioBuses {
		if err := checkContext(ctx, "domain.buses"); err != nil {
			return nil, -1, err
		}
		if needed[i] {
			values = append(values, bus)
			originalIndexes = append(originalIndexes, i)
		}
	}
	// This is a DSP route projection only: the evaluator already admitted the full original
	// Document, and only the full original hash is ever published or sent to providers.
	buses, master := compileBuses(values)
	for i := range buses {
		if err := checkContext(ctx, "domain.buses"); err != nil {
			return nil, -1, err
		}
		buses[i].index = originalIndexes[i]
		if p := buses[i].parentIndex; p >= 0 {
			buses[p].children = append(buses[p].children, i)
		}
	}
	return buses, master, nil
}
