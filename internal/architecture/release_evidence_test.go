package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This checks manual workflow intent, not a hosted run or broad M4 acceptance.
func TestReleaseEvidenceWorkflowRemainsManualAndScoped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(moduleRoot(t), ".github", "workflows", "release-evidence.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{"name: Restricted RC release evidence", "workflow_dispatch:", "contents: read", "os: [ubuntu-latest, macos-latest]", "timeout-minutes: 110", "go run ./internal/cmd/release-evidence", `--profile "${EVIDENCE_PROFILE}"`, `--output "${RUNNER_TEMP}/rc-release-evidence.json"`, "if: always()", "actions/upload-artifact@v4"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("missing workflow requirement %q", required)
		}
	}
	for _, forbidden := range []string{"  push:", "  pull_request:", "  schedule:", "contents: write", "id-token: write", "npm publish", "gh release create", "CUTLY_REAL_SMALL_FS_TEST"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("unexpected automatic or outward-facing action %q", forbidden)
		}
	}
}
