package cutly

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicRenderV2StrictFixtureSurface(t *testing.T) {
	root := filepath.Join("..", "..", "..", "conformance", "fixtures")
	planRaw, err := os.ReadFile(filepath.Join(root, "document-v2-render-plan-gap.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DecodeRenderPlanV2(planRaw)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPlan, err := CanonicalRenderPlanV2(plan)
	if err != nil || !bytes.Equal(planRaw, canonicalPlan) {
		t.Fatalf("plan canonical drift: %v", err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(root, "document-v2-execution-gap-silence.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeExecutionManifestV2(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyExecutionManifestV2AgainstPlan(context.Background(), manifest, plan)
	if err != nil || !verified.Valid() || verified.Fingerprint() != manifest.Fingerprint {
		t.Fatalf("manifest verification: %+v %v", verified, err)
	}
	evidenceRaw, err := os.ReadFile(filepath.Join(root, "evidence-document-v2-gap-silence.canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := DecodeEvidenceManifestV2(evidenceRaw)
	if err != nil {
		t.Fatal(err)
	}
	if verifiedProof, err := VerifyEvidenceManifestV2(proof); err != nil || !verifiedProof.Valid() {
		t.Fatalf("evidence verification: %+v %v", verifiedProof, err)
	}
}
