package preparev2

import (
	"context"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// CheckEnvironmentPins validates manifest structure and compares current host pins
// without opening resources. Success is not resource authorization or permission
// to execute. Reconstruction must still verify the Document, Plan and retained
// resource bytes. Unlike Freeze, this operation never replaces persisted pins.
func (a *Authority) CheckEnvironmentPins(ctx context.Context, manifest renderapi.ExecutionManifestV2) (err error) {
	defer func() {
		if recover() != nil {
			err = denied("environment pin check panic")
		}
	}()
	if err = a.ready(ctx); err != nil {
		return err
	}
	verified, err := renderapi.VerifyExecutionManifestV2(manifest)
	if err != nil {
		return err
	}
	owned, err := verified.Manifest()
	if err != nil {
		return err
	}
	if err = a.comparePins(owned); err != nil {
		return err
	}
	return checkContext(ctx)
}

// AuthorizedManifest owns a manifest checked against the original Document, Plan
// and current host authority without inventing a RenderJob. It retains no live
// resources or document snapshot. This point-in-time observation must be rebound
// to the actual privately retained bytes before reconstruction or execution.
// The zero value is invalid.
type AuthorizedManifest struct {
	verified renderapi.VerifiedExecutionManifestV2
}

func (m AuthorizedManifest) Valid() bool { return m.verified.Valid() }
func (m AuthorizedManifest) Manifest() (renderapi.ExecutionManifestV2, error) {
	return m.verified.Manifest()
}
func (m AuthorizedManifest) Canonical() ([]byte, error) { return m.verified.Canonical() }

// AuthorizeEnvironment checks a persisted selection without changing its pins,
// creating a job, or granting filesystem/publication authority. Host resource
// observations are compared only after all structural, Document and pin checks.
func (a *Authority) AuthorizeEnvironment(ctx context.Context, document documentv2.Document, plan renderplanv2.Plan, manifest renderapi.ExecutionManifestV2) (result AuthorizedManifest, err error) {
	defer func() {
		if recover() != nil {
			result = AuthorizedManifest{}
			err = denied("environment authorization panic")
		}
	}()
	if err = a.ready(ctx); err != nil {
		return result, err
	}
	verified, err := renderapi.VerifyExecutionManifestV2AgainstPlan(ctx, manifest, plan)
	if err != nil {
		return result, err
	}
	if err = renderplanv2.VerifyAgainstDocument(ctx, plan, document); err != nil {
		return result, err
	}
	owned, err := verified.Manifest()
	if err != nil {
		return result, err
	}
	if err = a.comparePins(owned); err != nil {
		return result, err
	}
	if err = a.verifyResources(ctx, owned); err != nil {
		return result, err
	}
	if err = checkContext(ctx); err != nil {
		return result, err
	}
	return AuthorizedManifest{verified: verified}, nil
}
