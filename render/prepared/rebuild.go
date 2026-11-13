package prepared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// ReferenceVisualProgramProfile is the execution-manifest program profile the reference
// reconstruction factory accepts. Other visual program profiles belong to later stages.
const ReferenceVisualProgramProfile = "cutly.program.visual.reference.v1"

// ResourceSource lends host-authorized encoded content for one image asset. The factory
// rehashes every returned byte and compares it with the asset's pinned content hash; the
// source never gains execution authority by satisfying it.
type ResourceSource interface {
	EncodedPNG(ctx context.Context, assetID documentv2.ID, contentHash string) ([]byte, error)
}

// RebuildReferenceVisual reconstructs a verified referencevisual Program from one persisted
// Environment v2 plus host-authorized image bindings. The contract enforced before any Program
// is returned:
//
//   - the manifest selects exactly the reference visual program profile;
//   - target geometry agrees across plan, manifest and the supplied config;
//   - the manifest's visual descriptor fingerprint pins the actual referencevisual descriptor
//     for the selected v1/v2 pipeline;
//   - every encoded PNG is rehashed and must equal the document's pinned content hash;
//   - the compiled Program's document hash and source-manifest canonical hash must equal the
//     persisted plan and manifest identities.
//
// Satisfying the factory proves content and declaration integrity only. Runtime identities
// (FFmpeg builds, locators, workers) still require independent host authorization.
func RebuildReferenceVisual(
	ctx context.Context,
	environment EnvironmentV2,
	source ResourceSource,
	config referencevisual.Config,
) (*referencevisual.Program, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidEnvironment)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("%w: resource source is required", ErrInvalidEnvironment)
	}
	selection := environment.Manifest.Visual
	if !selection.Enabled || selection.Profile != ReferenceVisualProgramProfile {
		return nil, fmt.Errorf("%w: manifest does not select the reference visual program profile", ErrInvalidEnvironment)
	}
	if config.TextConfig != nil {
		// Text-capable reconstruction needs host-authorized font bindings (the S4 boundary);
		// the v1 factory fails closed instead of silently dropping the Text path.
		return nil, fmt.Errorf("%w: text-capable configurations require the font-authorized reconstruction boundary", ErrInvalidEnvironment)
	}
	if environment.Plan.Target.Width != config.Width || environment.Plan.Target.Height != config.Height ||
		int(environment.Manifest.Output.Width) != config.Width || int(environment.Manifest.Output.Height) != config.Height {
		return nil, fmt.Errorf("%w: plan, manifest and config disagree on target geometry", ErrInvalidEnvironment)
	}
	// Defense in depth: the stored plan must itself re-resolve from the stored document even
	// when the environment did not come through ResolveV2.
	if _, err := renderplanv2.VerifyAgainstDocument(ctx, environment.Plan, environment.Document); err != nil {
		return nil, fmt.Errorf("%w: stored plan does not re-resolve from the stored document: %v", ErrInvalidEnvironment, err)
	}
	descriptor, err := canonicalFingerprint(referencePipelineDescriptor(config))
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor identity: %v", ErrInvalidEnvironment, err)
	}
	if selection.DescriptorFingerprint != descriptor {
		return nil, fmt.Errorf("%w: visual descriptor fingerprint does not pin this reference pipeline", ErrInvalidEnvironment)
	}

	bindings := make([]referencevisual.Binding, 0, len(environment.Document.Assets))
	for _, asset := range environment.Document.Assets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if asset.Kind != documentv2.AssetKindImage {
			continue
		}
		if asset.ContentHash == "" {
			return nil, fmt.Errorf("%w: image asset %s has no pinned content hash", ErrInvalidEnvironment, asset.ID)
		}
		encoded, err := source.EncodedPNG(ctx, asset.ID, asset.ContentHash)
		if err != nil {
			return nil, fmt.Errorf("%w: resource %s: %v", ErrInvalidEnvironment, asset.ID, err)
		}
		digest := sha256.Sum256(encoded)
		if contentHash := "sha256:" + hex.EncodeToString(digest[:]); contentHash != asset.ContentHash {
			return nil, fmt.Errorf("%w: resource %s rehash %s does not match pinned %s", ErrInvalidEnvironment, asset.ID, contentHash, asset.ContentHash)
		}
		bindings = append(bindings, referencevisual.Binding{AssetID: asset.ID, EncodedPNG: encoded})
	}
	program, diagnostics, err := referencevisual.Compile(ctx, environment.Document, bindings, config)
	if err != nil || program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, fmt.Errorf("%w: reference compile diagnostics=%+v err=%v", ErrInvalidEnvironment, diagnostics, err)
	}
	if program.DocumentHash() != environment.Plan.DocumentHash || program.DocumentHash() != environment.Manifest.DocumentHash {
		return nil, fmt.Errorf("%w: rebuilt program document hash %s does not match the persisted environment", ErrInvalidEnvironment, program.DocumentHash())
	}
	sourceManifestHash, err := canonicalFingerprint(program.SourceManifest())
	if err != nil {
		return nil, fmt.Errorf("%w: source manifest identity: %v", ErrInvalidEnvironment, err)
	}
	if selection.SourceManifestFingerprint != sourceManifestHash {
		return nil, fmt.Errorf("%w: source manifest fingerprint does not match the rebuilt program", ErrInvalidEnvironment)
	}
	// The manifest's visual configuration must pin the exact rebuilt program configuration:
	// Visual.Configuration is the canonical referencevisual.Config serialization and its
	// S2-verified fingerprint must equal the rebuilt program's canonical configuration hash.
	configHash, err := canonicalFingerprint(program.Config())
	if err != nil || selection.ConfigurationFingerprint != configHash {
		return nil, fmt.Errorf("%w: visual configuration fingerprint does not pin the rebuilt program configuration", ErrInvalidEnvironment)
	}
	return program, nil
}

func referencePipelineDescriptor(config referencevisual.Config) any {
	if config.TextConfig != nil {
		return referencevisual.DescriptorV2()
	}
	return referencevisual.Descriptor()
}

func canonicalFingerprint(value any) (string, error) {
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
