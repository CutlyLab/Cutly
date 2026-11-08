package referencevisual

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/visualcompositor"
)

const (
	DomainProfileV1         = "cutly.reference-visual.domain-native-png-vector-expression-text-export.v1"
	DomainManifestProfileV1 = "cutly.reference-visual.domain-source-manifest.v1"
)

// CompileDomainV2 borrows the original full document and bindings only until return. It supports
// one authored timeline with all enabled visual clips selected, static PNG/vector/pinned-font Text,
// and unchanged authored audio. It never decodes audio or prunes the document.
func CompileDomainV2(ctx context.Context, originalDoc documentv2.Document, scope evaluator.DomainScope, pngBindings []Binding, fontBindings []textlayer.FontBinding, config Config) (program *Program, diagnostics []documentv2.Diagnostic, err error) {
	defer func() {
		if recover() != nil {
			program, diagnostics = nil, nil
			err = pipelineError(CodeStageMismatch, "compile/domain/panic", ErrStageMismatch)
		}
	}()
	return compileMode(ctx, originalDoc, pngBindings, fontBindings, config, true, &scope)
}

// PreflightDomainV2 uses the exact normalized wrapper configuration and compositor path used by
// CompileDomainV2, including aggregate compile/runtime budget validation. It performs no source
// decode, shape callbacks, font binding or resource IO. Returned authority remains domain-scoped.
func PreflightDomainV2(ctx context.Context, originalDoc documentv2.Document, scope evaluator.DomainScope, config Config) (program *visualcompositor.Program, diagnostics []documentv2.Diagnostic, err error) {
	defer func() {
		if recover() != nil {
			program, diagnostics = nil, nil
			err = pipelineError(CodeStageMismatch, "preflight/domain/panic", ErrStageMismatch)
		}
	}()
	if err := contextError(ctx, "preflight/domain"); err != nil {
		return nil, nil, err
	}
	normalized, visualConfig, err := normalizeConfigMode(config, true)
	if err != nil {
		return nil, nil, err
	}
	program, diagnostics = compileVisual(ctx, originalDoc, visualConfig, &scope)
	diagnostics = cloneDiagnostics(diagnostics)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, diagnostics, classifyVisualCompile(ctx, diagnostics)
	}
	actual := program.Config()
	if actual.Width != visualConfig.Width || actual.Height != visualConfig.Height || actual.Source != visualConfig.Source || actual.NativeProfiles != visualConfig.NativeProfiles || actual.BuildFingerprint != visualConfig.BuildFingerprint {
		return nil, diagnostics, pipelineError(CodeStageMismatch, "preflight/visual_config", ErrStageMismatch)
	}
	normalized.CompositorLimits = actual.Limits
	if err := validateAggregateLimitsMode(normalized, true); err != nil {
		return nil, diagnostics, err
	}
	if err := contextError(ctx, "preflight/domain/publish"); err != nil {
		return nil, diagnostics, err
	}
	return program, diagnostics, nil
}

func compileVisual(ctx context.Context, document documentv2.Document, config visualcompositor.Config, scope *evaluator.DomainScope) (*visualcompositor.Program, []documentv2.Diagnostic) {
	if scope != nil {
		return visualcompositor.CompileDomain(ctx, document, *scope, config)
	}
	return visualcompositor.Compile(ctx, document, config)
}

// Scope returns a defensive copy; legacy programs have a zero scope.
func (program *Program) Scope() evaluator.DomainScope {
	if program == nil || program.domainScope == nil {
		return evaluator.DomainScope{}
	}
	value := *program.domainScope
	value.Selection.ClipIDs = append([]documentv2.ID(nil), value.Selection.ClipIDs...)
	value.Selection.TransitionIDs = append([]documentv2.ID(nil), value.Selection.TransitionIDs...)
	return value
}

func (program *Program) DomainScopeHash() string {
	if program == nil || program.visual == nil {
		return ""
	}
	return program.visual.DomainScopeHash()
}

// Used only as a directly deferred function, so recover observes the public method's panic.
// Legacy programs deliberately retain their prior panic behavior.
func (program *Program) recoverDomainPanic(err *error, zero func()) {
	if program != nil && program.domainScope != nil {
		if recover() != nil {
			if zero != nil {
				zero()
			}
			*err = pipelineError(CodeStageMismatch, "domain/runtime/panic", ErrStageMismatch)
		}
	}
}

func equalDomainScopes(left, right evaluator.DomainScope) bool { return reflect.DeepEqual(left, right) }
func equalScopeBindings(left, right *visualcompositor.DomainScopeBinding) bool {
	return reflect.DeepEqual(left, right)
}
func cloneScopeBinding(value visualcompositor.DomainScopeBinding) visualcompositor.DomainScopeBinding {
	value.Scope.Selection.ClipIDs = append([]documentv2.ID(nil), value.Scope.Selection.ClipIDs...)
	value.Scope.Selection.TransitionIDs = append([]documentv2.ID(nil), value.Scope.Selection.TransitionIDs...)
	return value
}

func validDomainManifestScope(binding visualcompositor.DomainScopeBinding) bool {
	if binding.Profile != visualcompositor.DomainScopeHashProfileV1 || !canonicalSHA256(binding.Fingerprint) ||
		binding.Scope.Domain != evaluator.DomainVisual || binding.Scope.Selection.TimelineID == "" ||
		len(binding.Scope.Selection.TransitionIDs) != 0 || binding.Scope.RequestedRange.Validate() != nil || binding.Scope.RequestedRange.Duration.Value <= 0 {
		return false
	}
	for index, id := range binding.Scope.Selection.ClipIDs {
		if id == "" || index > 0 && binding.Scope.Selection.ClipIDs[index-1] >= id {
			return false
		}
	}
	encoded, err := videoedit.CanonicalJSON(struct {
		Profile string                           `json:"profile"`
		Scope   visualcompositor.DomainScopeWire `json:"scope"`
	}{binding.Profile, binding.Scope})
	if err != nil {
		return false
	}
	digest := sha256.Sum256(encoded)
	return binding.Fingerprint == "sha256:"+hex.EncodeToString(digest[:])
}

func DomainDescriptorV2() ProfileDescriptor {
	value := DescriptorV2()
	value.Profile = DomainProfileV1
	value.CompositorProfile = visualcompositor.DomainProfileV1
	value.CompileSemantics = "CompileDomainV2 and resource-free PreflightDomainV2 use the same normalized configuration, aggregate budgets and checked domain compositor path. Require one authored timeline and all enabled visual clips selected before any resource callback. Preserve full original canonical DocumentHash and original indexes, including authored audio. Fixed Capabilities(config); no synthesized offers."
	value.ManifestSemantics = "Use cutly.reference-visual.domain-source-manifest.v1, binding the full DocumentHash, owned lowercase concrete visual domain scope and cutly.hash.visual-domain-scope-canonical-json.v1 fingerprint to every immutable provider manifest. Provider compilers still inspect the full original document; no document pruning or re-hashing."
	value.ReceiptSemantics = "Every compositor result carries the exact domain scope fingerprint. Live and serialized validation reject same-document, same-profile cross-scope replay, including transparent gaps. Existing strict provider lineage and output/PNG checks remain unchanged."
	value.ExcludedResponsibilities = "Temporal video, S4, audio rendering, additional authored timelines, any unselected enabled visual clips, all selected-timeline transitions, dynamic Text and external resource IO remain excluded. Authored audio is structurally validated and retained in full document identity only."
	return value
}
