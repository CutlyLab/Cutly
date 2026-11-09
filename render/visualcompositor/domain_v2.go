package visualcompositor

import (
	"context"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
)

const DomainProfileV2 = "cutly.reference-compositor.domain-temporal-native-expression-text-affine-surface.v2"

// CompileDomainV2 adds scoped temporal transitions without relaxing CompileDomain.
// Like V1 it requires one timeline and every enabled visual clip in the selection.
// Offers are fixed by Config, never inferred from document requirements.
func CompileDomainV2(ctx context.Context, document documentv2.Document, scope evaluator.DomainScope, config Config) (program *Program, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if recover() != nil {
			program = nil
			diagnostics = []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeDomainInvalid, "/scope", ErrInvariantFailed)}
		}
	}()
	return compileMode(ctx, document, config, &scope, true)
}

func DomainDescriptorV2() ProfileDescriptor {
	value := DomainDescriptor()
	value.Profile = DomainProfileV2
	value.CapabilitySemantics = "CompileDomainV2 uses evaluator.CompileDomainSnapshotV2 with fixed Capabilities(config), full original canonical document/hash/indexes and explicit exact visual scope. No document pruning or inferred offers."
	value.TransitionSemantics = "Explicit selected hard cuts and dissolve windows use evaluator V2 exact participant handles, source-time mapping and half-open scope. Native image/video and vector handles are supported; pinned Text remains static and rejects nonzero transitions."
	value.ExcludedResponsibilities = "Audio rendering, additional authored timelines, unselected enabled visual clips, dynamic Text and Text dissolves remain excluded. Native decoder lifetime and limits belong to the host."
	return value
}
