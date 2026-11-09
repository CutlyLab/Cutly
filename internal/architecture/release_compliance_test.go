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
	if !strings.Contains(workflow, `npm --prefix sdk/typescript run pack:check`) {
		t.Error("release verification must run the package-local dry-run script")
	}
	for _, line := range []string{
		`run: go mod download`,
		`go run ./internal/cmd/release-archive`,
		`--target "${TARGET_GOOS}/${TARGET_GOARCH}"`,
		`--output "${RUNNER_TEMP}"`,
		`--version "${version}"`,
		`smoke=(--smoke)`,
	} {
		if !strings.Contains(workflow, line) {
			t.Errorf("command archive omits shared build/inspection step %q", line)
		}
	}
	for _, line := range []string{`          GOOS:`, `          GOARCH:`, `zip -q -r`, `tar -C`} {
		if strings.Contains(workflow, line) {
			t.Errorf("workflow bypasses host archive builder with %q", line)
		}
	}
	for _, name := range []string{"LICENSE", "go.sum", "cmd/cutlyd/README.md"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("missing archive source %s: %v", name, err)
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
