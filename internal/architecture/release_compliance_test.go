package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This checks source packaging intent, not a published archive or legal approval.
func TestCommandArchivesIncludeDependencyRecords(t *testing.T) {
	root := moduleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, line := range []string{
		`mkdir -p "${output}/deps/licenses"`,
		`cp deps/THIRD_PARTY_NOTICES.md deps/sbom.spdx.json deps/dependency-lock.json "${output}/deps/"`,
		`cp deps/licenses/* "${output}/deps/licenses/"`,
	} {
		if !strings.Contains(workflow, line) {
			t.Errorf("command archive omits dependency packaging step %q", line)
		}
	}
	for _, name := range []string{"THIRD_PARTY_NOTICES.md", "sbom.spdx.json", "dependency-lock.json"} {
		info, err := os.Stat(filepath.Join(root, "deps", name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("missing dependency record %s: %v", name, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(root, "deps", "licenses", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("missing license closure: %v", err)
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("invalid license file %s: %v", path, err)
		}
	}
}
