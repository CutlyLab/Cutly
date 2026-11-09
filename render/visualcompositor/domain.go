package visualcompositor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

const (
	DomainProfileV1             = "cutly.reference-compositor.domain-native-expression-text-affine-surface.v1"
	DomainScopeHashProfileV1    = "cutly.hash.visual-domain-scope-canonical-json.v1"
	DiagnosticCodeDomainInvalid = "COMPOSITOR.VISUAL_DOMAIN_V1.SCOPE_INVALID"
)

// visualEvaluator is private: only the legacy and checked domain evaluator compilers supply it.
type visualEvaluator interface {
	DocumentHash() string
	EvaluateTimeline(documentv2.ID, documentv2.RationalTime) (evaluator.TimelineEvaluation, error)
	EvaluateVisualFrame(documentv2.ID, videoedit.FrameClock, int64) (evaluator.VisualFrameEvaluation, error)
}

// CompileDomain preserves the full original document identity while authorizing exactly scope.
// Initial profile restriction: one authored timeline, and every enabled visual clip must be selected.
// Audio remains authored and structurally validated but does not require visual capabilities.
func CompileDomain(ctx context.Context, originalDoc documentv2.Document, scope evaluator.DomainScope, config Config) (program *Program, diagnostics []documentv2.Diagnostic) {
	defer func() {
		if recover() != nil {
			program = nil
			diagnostics = []documentv2.Diagnostic{compileDiagnostic(DiagnosticCodeDomainInvalid, "/scope", ErrInvariantFailed)}
		}
	}()
	return compile(ctx, originalDoc, config, &scope)
}

func cloneDomainScope(value evaluator.DomainScope) evaluator.DomainScope {
	value.Selection.ClipIDs = append([]documentv2.ID(nil), value.Selection.ClipIDs...)
	value.Selection.TransitionIDs = append([]documentv2.ID(nil), value.Selection.TransitionIDs...)
	return value
}

// Scope returns owned scope storage. Legacy programs return a zero scope.
func (program *Program) Scope() evaluator.DomainScope {
	if program == nil || program.domainScope == nil {
		return evaluator.DomainScope{}
	}
	return cloneDomainScope(*program.domainScope)
}

func (program *Program) DomainScopeHash() string {
	if program == nil {
		return ""
	}
	return program.domainScopeHash
}

func (program *Program) profile() string {
	if program.domainV2 {
		return DomainProfileV2
	}
	if program.domainScope != nil {
		return DomainProfileV1
	}
	return profileForConfig(program.config)
}

// DomainScopeWire is the versioned lowercase projection, not evaluator's Go field encoding.
type DomainScopeWire struct {
	Domain         evaluator.Domain     `json:"domain"`
	Selection      DomainSelectionWire  `json:"selection"`
	RequestedRange documentv2.TimeRange `json:"requested_range"`
}

type DomainSelectionWire struct {
	TimelineID    documentv2.ID   `json:"timeline_id"`
	ClipIDs       []documentv2.ID `json:"clip_ids"`
	TransitionIDs []documentv2.ID `json:"transition_ids"`
}

type DomainScopeBinding struct {
	Profile     string          `json:"profile"`
	Fingerprint string          `json:"fingerprint"`
	Scope       DomainScopeWire `json:"scope"`
}

func scopeWire(scope evaluator.DomainScope) DomainScopeWire {
	scope = cloneDomainScope(scope)
	return DomainScopeWire{scope.Domain, DomainSelectionWire{scope.Selection.TimelineID, scope.Selection.ClipIDs, scope.Selection.TransitionIDs}, scope.RequestedRange}
}

// ScopeBinding returns a fully owned concrete scope and its versioned fingerprint.
func (program *Program) ScopeBinding() DomainScopeBinding {
	if program == nil || program.domainScope == nil {
		return DomainScopeBinding{}
	}
	return DomainScopeBinding{DomainScopeHashProfileV1, program.domainScopeHash, scopeWire(*program.domainScope)}
}

func hashDomainScope(scope evaluator.DomainScope) (string, error) {
	bytes, err := videoedit.CanonicalJSON(struct {
		Profile string          `json:"profile"`
		Scope   DomainScopeWire `json:"scope"`
	}{DomainScopeHashProfileV1, scopeWire(scope)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (program *Program) recoverDomainPanic(err *error) {
	if program != nil && program.domainScope != nil {
		if recover() != nil {
			*err = compositorError(CodeInvariantFailed, "domain/validate/panic", ErrInvariantFailed)
		}
	}
}

func domainClipSet(scope evaluator.DomainScope) map[documentv2.ID]bool {
	result := make(map[documentv2.ID]bool, len(scope.Selection.ClipIDs))
	for _, id := range scope.Selection.ClipIDs {
		result[id] = true
	}
	return result
}

func validateDomainProviderClosure(ctx context.Context, document documentv2.Document, scope evaluator.DomainScope) error {
	if scope.Domain != evaluator.DomainVisual || len(document.Timelines) != 1 || document.Timelines[0].ID != scope.Selection.TimelineID {
		return errors.Join(ErrUnsupportedLayer, errors.New("domain profile requires one authored timeline and the visual domain"))
	}
	// Bound scope-sized allocation by the already admitted full graph.
	count := 0
	for _, track := range document.Timelines[0].Tracks {
		count += len(track.Clips)
	}
	if len(scope.Selection.ClipIDs) > count {
		return ErrUnsupportedLayer
	}
	selected := domainClipSet(scope)
	for _, track := range document.Timelines[0].Tracks {
		if err := contextError(ctx, "compile/domain_scope"); err != nil {
			return err
		}
		if track.Kind != documentv2.TrackKindVideo && track.Kind != documentv2.TrackKindVector && track.Kind != documentv2.TrackKindText {
			continue
		}
		for _, clip := range track.Clips {
			if !clip.Disabled && !selected[clip.ID] {
				return errors.Join(ErrUnsupportedLayer, errors.New("all enabled visual clips must be selected, including out-of-range same-asset clips"))
			}
		}
	}
	return nil
}

// DomainDescriptor describes an additive profile; legacy descriptors remain byte-stable.
func DomainDescriptor() ProfileDescriptor {
	value := DescriptorV2()
	value.Profile = DomainProfileV1
	value.CapabilitySemantics = "CompileDomain uses evaluator.CompileDomainSnapshot with fixed Capabilities(config). Structurally validate and hash the complete original Document; negotiate only the checked visual selection. Never prune or re-hash the document. Audio is not rendered."
	value.DispatchSemantics = "Initial profile requires one authored timeline and every enabled visual clip selected. Build dispatch maps from selected original indexes; full FrozenSnapshot discovery is not authority."
	value.TransitionSemantics = "Reject every authored transition on the selected timeline, including hard cuts and out-of-range transitions."
	value.ReceiptSemantics = "Retain all static Text/native/vector validation. Bind each result to the owned sorted exact-rational DomainScope through cutly.hash.visual-domain-scope-canonical-json.v1; reject cross-scope replay even for gaps at the same document hash and profile."
	value.GapSemantics = "Transparent gaps are allowed only inside the exact half-open requested scope range. Wrong timeline, invalid clock, frame overflow and out-of-range time fail before provider calls."
	value.ExcludedResponsibilities = "Temporal video assembly, audio rendering, additional authored timelines, unselected enabled visual clips, transitions and S4 remain outside this initial profile. Direct compositor video capability remains explicitly config-controlled."
	return value
}
