package buildidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	textShapeLockFixtureSHA256 = "sha256:a9849651ab22762151b07162d47eab94b1a96e49ce94248ed534fcfd88b1b8c3"
	textShapeFingerprintV1     = "sha256:e2f3967156b6030da45e94771b910a79ac9ebe3618f320217ca35a4825de0ee2"
)

func TestTextShapeProviderDependencyLockCanonicalAndFingerprint(t *testing.T) {
	canonical := TextShapeCandidateCanonicalJSON()
	root := cutlyModuleRoot(t)
	fixture, err := os.ReadFile(filepath.Join(root, "deps", "dependency-lock.json"))
	if err != nil {
		t.Fatalf("read dependency lock: %v\ncanonical=%s\nfingerprint=%s", err, canonical, TextShapeCandidateFingerprint())
	}
	payload := bytes.TrimSuffix(fixture, []byte{'\n'})
	if !bytes.Equal(fixture, append(append([]byte(nil), payload...), '\n')) {
		t.Fatal("dependency lock must have exactly one final LF")
	}
	if !bytes.Equal(payload, canonical) {
		t.Fatalf("dependency lock drift\nwant=%s\n got=%s", fixture, canonical)
	}
	digest := sha256.Sum256(payload)
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != textShapeLockFixtureSHA256 {
		t.Fatalf("dependency lock hash=%s want=%s", got, textShapeLockFixtureSHA256)
	}
	if got := TextShapeCandidateFingerprint(); got != textShapeFingerprintV1 {
		t.Fatalf("shape-provider fingerprint=%s want=%s", got, textShapeFingerprintV1)
	}
	verified, err := VerifyTextShapeDependencyLock(fixture)
	if err != nil || verified != textShapeFingerprintV1 {
		t.Fatalf("verified fingerprint=%q err=%v", verified, err)
	}
}

func TestTextShapeProviderDependencyIdentityRemainsClosedAndDefensive(t *testing.T) {
	first := TextShapeCandidate()
	if first.SchemaVersion != TextShapeCandidateSchemaVersion || first.Profile != TextShapeCandidateProfileV1 ||
		first.Status != TextShapeCandidateStatus || first.ProviderProfile != TextShapeProviderProfileV1 ||
		first.FingerprintProfile != TextShapeFingerprintProfileV1 || first.CutlyGoDirective != "1.25.0" ||
		first.RequiredCGOEnabled || !first.ImportedByCutly || first.CapabilityAdvertised {
		t.Fatalf("shape-provider identity is not closed: %+v", first)
	}
	if len(first.Modules) != 6 || len(first.EmbeddedSources) != 1 {
		t.Fatalf("shape-provider closure counts modules=%d embedded=%d", len(first.Modules), len(first.EmbeddedSources))
	}
	if !slices.Equal(first.ProspectiveGraphTargets, []string{"js/wasm", "linux/amd64", "windows/amd64"}) ||
		!slices.Equal(first.SelectedPackageRoots, []string{
			"github.com/go-text/typesetting/font",
			"github.com/go-text/typesetting/font/opentype",
			"github.com/go-text/typesetting/font/opentype/tables",
			"github.com/go-text/typesetting/harfbuzz",
			"github.com/go-text/typesetting/language",
		}) || len(first.ProductionPackageClosure) != 11 {
		t.Fatalf("shape-provider package graph drift: %+v", first)
	}
	first.ProspectiveGraphTargets[0] = "tampered"
	first.SelectedPackageRoots[0] = "tampered"
	first.ProductionPackageClosure[0] = "tampered"
	first.Modules[0].Version = "tampered"
	first.Modules[3].SupplementalNotices[0].SHA256 = "tampered"
	first.EmbeddedSources[0].Commit = "tampered"
	second := TextShapeCandidate()
	if second.ProspectiveGraphTargets[0] != "js/wasm" ||
		second.SelectedPackageRoots[0] != "github.com/go-text/typesetting/font" ||
		second.ProductionPackageClosure[0] != "github.com/go-text/typesetting/font" ||
		second.Modules[0].Version != "v0.4.2" ||
		second.Modules[3].SupplementalNotices[0].SHA256 != "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc" ||
		second.EmbeddedSources[0].Commit != "5a31dd02f0b32de12336f72db9297bfe94cf0da1" {
		t.Fatalf("shape-provider descriptor shared mutable storage: %+v", second)
	}
}

func TestTextShapeProviderPackageAndModuleReachabilityFrozen(t *testing.T) {
	descriptor := TextShapeCandidate()
	wantRoots := []string{
		"github.com/go-text/typesetting/font",
		"github.com/go-text/typesetting/font/opentype",
		"github.com/go-text/typesetting/font/opentype/tables",
		"github.com/go-text/typesetting/harfbuzz",
		"github.com/go-text/typesetting/language",
	}
	wantClosure := []string{
		"github.com/go-text/typesetting/font",
		"github.com/go-text/typesetting/font/cff",
		"github.com/go-text/typesetting/font/cff/interpreter",
		"github.com/go-text/typesetting/font/opentype",
		"github.com/go-text/typesetting/font/opentype/tables",
		"github.com/go-text/typesetting/harfbuzz",
		"github.com/go-text/typesetting/internal/unicodedata",
		"github.com/go-text/typesetting/language",
		"golang.org/x/image/ccitt",
		"golang.org/x/image/tiff",
		"golang.org/x/image/tiff/lzw",
	}
	if !slices.Equal(descriptor.SelectedPackageRoots, wantRoots) {
		t.Fatalf("selected roots=%v want=%v", descriptor.SelectedPackageRoots, wantRoots)
	}
	if !slices.IsSorted(descriptor.ProductionPackageClosure) ||
		!slices.Equal(descriptor.ProductionPackageClosure, wantClosure) {
		t.Fatalf("shape-provider production package closure=%v want sorted %v", descriptor.ProductionPackageClosure, wantClosure)
	}
	if !slices.Equal(descriptor.ProspectiveGraphTargets, []string{"js/wasm", "linux/amd64", "windows/amd64"}) ||
		descriptor.RequiredCGOEnabled {
		t.Fatalf("prospective target policy targets=%v cgo=%t", descriptor.ProspectiveGraphTargets, descriptor.RequiredCGOEnabled)
	}

	type classification struct {
		closure      string
		reachability string
		count        int
	}
	wantModules := map[string]classification{
		"github.com/google/jsonschema-go":      {"current_test", "test_import", 1},
		"github.com/google/go-cmp":             {"current_module_graph_only", "upstream_test", 0},
		"github.com/go-text/typesetting":       {"current_production", "production_import", 8},
		"golang.org/x/image":                   {"current_production_transitive", "production_import", 3},
		"github.com/go-text/typesetting-utils": {"current_module_graph_only", "upstream_test_generate", 0},
		"golang.org/x/text":                    {"current_module_graph_only", "module_graph_only", 0},
	}
	if len(descriptor.Modules) != len(wantModules) {
		t.Fatalf("module count=%d want=%d", len(descriptor.Modules), len(wantModules))
	}
	for _, module := range descriptor.Modules {
		want, ok := wantModules[module.ModulePath]
		if !ok {
			t.Fatalf("unexpected module %q", module.ModulePath)
		}
		if module.Closure != want.closure || module.Reachability != want.reachability || module.LinkedPackageCount != want.count {
			t.Fatalf("module %q classification=(%q,%q,%d) want=(%q,%q,%d)",
				module.ModulePath, module.Closure, module.Reachability, module.LinkedPackageCount,
				want.closure, want.reachability, want.count)
		}
		switch module.ModulePath {
		case "golang.org/x/image":
			if len(module.SupplementalNotices) != 1 ||
				module.SupplementalNotices[0] != (SupplementalNoticeIdentity{
					Kind: "patent_grant", Path: "deps/licenses/golang.org_x_image_v0.23.0.PATENTS",
					SHA256: "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
				}) {
				t.Fatalf("x/image supplemental notices=%+v", module.SupplementalNotices)
			}
		case "golang.org/x/text":
			if module.Version != "v0.21.0" ||
				module.ModuleSum != "h1:zyQAAkrwaneQ066sspRyJaG9VNi/YJ1NfzcGB3hZ/qo=" ||
				module.GoModSum != "h1:4IBbMaMmOPCJ8SecivzSH54+73PCFmPWxNTLm+vZkEQ=" ||
				module.Repository != "https://go.googlesource.com/text" ||
				module.Commit != "d42948e5579eb996bedb7df76c7ad57fae4e83c7" ||
				module.Ref != "refs/tags/v0.21.0" || len(module.SupplementalNotices) != 1 ||
				module.SupplementalNotices[0] != (SupplementalNoticeIdentity{
					Kind: "patent_grant", Path: "deps/licenses/golang.org_x_text_v0.21.0.PATENTS",
					SHA256: "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
				}) {
				t.Fatalf("x/text origin or notices drift: %+v", module)
			}
		default:
			if len(module.SupplementalNotices) != 0 {
				t.Fatalf("unexpected supplemental notices for %q: %+v", module.ModulePath, module.SupplementalNotices)
			}
		}
		delete(wantModules, module.ModulePath)
	}
	if len(wantModules) != 0 {
		t.Fatalf("missing modules: %v", wantModules)
	}
}

func TestVerifyTextShapeDependencyLockRejectsHostileOrDriftedWire(t *testing.T) {
	canonical := TextShapeCandidateCanonicalJSON()
	var changed TextShapeCandidateDescriptor
	if err := json.Unmarshal(canonical, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Modules[2].Version = "v0.3.6"
	drifted, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTextShapeDependencyLock(drifted); !errors.Is(err, ErrDependencyLockMismatch) {
		t.Fatalf("drifted lock error=%v", err)
	}

	changed = TextShapeCandidate()
	changed.CapabilityAdvertised = true
	drifted, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTextShapeDependencyLock(drifted); !errors.Is(err, ErrDependencyLockMismatch) {
		t.Fatalf("capability escalation error=%v", err)
	}

	duplicate := append([]byte(`{"schema_version":1,`), canonical[1:]...)
	unknown := append([]byte(`{"unknown":false,`), canonical[1:]...)
	for name, raw := range map[string][]byte{
		"duplicate":          duplicate,
		"unknown":            unknown,
		"leading whitespace": append([]byte{' '}, canonical...),
		"two final newlines": append(append(append([]byte(nil), canonical...), '\n'), '\n'),
		"trailing space":     append(append([]byte(nil), canonical...), ' '),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyTextShapeDependencyLock(raw); !errors.Is(err, ErrDependencyLockInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	withLF := append(append([]byte(nil), canonical...), '\n')
	if got, err := VerifyTextShapeDependencyLock(withLF); err != nil || got != TextShapeCandidateFingerprint() {
		t.Fatalf("single final LF fingerprint=%q err=%v", got, err)
	}
}

func TestTextShapeDependencyNoticesMatchLockedBytes(t *testing.T) {
	root := cutlyModuleRoot(t)
	descriptor := TextShapeCandidate()
	assertLockedFileHash(t, root, descriptor.NoticeManifestPath, descriptor.NoticeManifestSHA256)
	assertLockedFileHash(t, root, descriptor.SBOMPath, descriptor.SBOMSHA256)
	for _, module := range descriptor.Modules {
		assertLockedFileHash(t, root, module.NoticePath, module.NoticeSHA256)
		for _, notice := range module.SupplementalNotices {
			assertLockedFileHash(t, root, notice.Path, notice.SHA256)
		}
	}
	for _, source := range descriptor.EmbeddedSources {
		assertLockedFileHash(t, root, source.NoticePath, source.NoticeSHA256)
	}
}

func TestTextShapeUpstreamLegalBytesAreExact(t *testing.T) {
	root := cutlyModuleRoot(t)
	want := map[string]string{
		"deps/licenses/github.com_go-text_typesetting-utils_4ffe8874dabc.LICENSE": "sha256:a1e5b69b12756927cfe3b3a373df05a55c1aaa69c6b259e06d8746257cb17d06",
		"deps/licenses/github.com_go-text_typesetting_v0.3.5.harfbuzz.LICENSE":    "sha256:e45d96558ba3d406f95dffed6f22c3be07093475ef617bcfddf88444d6b66788",
		"deps/licenses/golang.org_x_image_v0.23.0.LICENSE":                        "sha256:911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad",
		"deps/licenses/golang.org_x_image_v0.23.0.PATENTS":                        "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
		"deps/licenses/golang.org_x_text_v0.21.0.LICENSE":                         "sha256:911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad",
		"deps/licenses/golang.org_x_text_v0.21.0.PATENTS":                         "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
	}
	for path, hash := range want {
		assertLockedFileHash(t, root, path, hash)
	}
}

func TestTextShapeProviderSBOMReachabilityAndRootProductionEdge(t *testing.T) {
	root := cutlyModuleRoot(t)
	descriptor := TextShapeCandidate()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(descriptor.SBOMPath)))
	if err != nil {
		t.Fatal(err)
	}
	var sbom struct {
		SPDXVersion string `json:"spdxVersion"`
		Packages    []struct {
			Name        string `json:"name"`
			VersionInfo string `json:"versionInfo"`
			Comment     string `json:"comment"`
		} `json:"packages"`
		Relationships []struct {
			SPDXElementID    string `json:"spdxElementId"`
			RelationshipType string `json:"relationshipType"`
			RelatedElement   string `json:"relatedSpdxElement"`
			Comment          string `json:"comment"`
		} `json:"relationships"`
	}
	if err := json.Unmarshal(raw, &sbom); err != nil {
		t.Fatalf("decode SBOM: %v", err)
	}
	if sbom.SPDXVersion != descriptor.SBOMProfile {
		t.Fatalf("SBOM profile=%q want=%q", sbom.SPDXVersion, descriptor.SBOMProfile)
	}
	comments := make(map[string]string, len(sbom.Packages))
	versions := make(map[string]string, len(sbom.Packages))
	for _, value := range sbom.Packages {
		comments[value.Name] = value.Comment
		versions[value.Name] = value.VersionInfo
	}
	if versions["github.com/CutlyLab/Cutly"] != "unreleased-m2-shape-provider" {
		t.Fatalf("SBOM Cutly shape-stage version=%q", versions["github.com/CutlyLab/Cutly"])
	}
	if !strings.Contains(comments["github.com/google/jsonschema-go"], "closure=current_test; reachability=test_import; linked_package_count=1") ||
		!strings.Contains(comments["github.com/google/go-cmp"], "closure=current_module_graph_only; reachability=upstream_test; linked_package_count=0") ||
		!strings.Contains(comments["github.com/go-text/typesetting"], "closure=current_production; reachability=production_import; linked_package_count=8") ||
		!strings.Contains(comments["golang.org/x/image"], "closure=current_production_transitive; reachability=production_import; linked_package_count=3") ||
		!strings.Contains(comments["github.com/go-text/typesetting-utils"], "closure=current_module_graph_only; reachability=upstream_test_generate; linked_package_count=0") ||
		!strings.Contains(comments["golang.org/x/text"], "closure=current_module_graph_only; reachability=module_graph_only; linked_package_count=0") {
		t.Fatalf("SBOM provider reachability classification drift: %+v", comments)
	}
	foundRootProduction, foundMVSOnly := false, false
	for _, relationship := range sbom.Relationships {
		if relationship.SPDXElementID == "SPDXRef-Package-Cutly" &&
			relationship.RelationshipType == "DEPENDS_ON" &&
			relationship.RelatedElement == "SPDXRef-Package-GoTextTypesetting" &&
			strings.Contains(relationship.Comment, "direct production dependency") &&
			strings.Contains(relationship.Comment, "does not advertise final Text render capability") {
			foundRootProduction = true
		}
		if relationship.SPDXElementID == "SPDXRef-Package-XImage" &&
			relationship.RelationshipType == "DEPENDS_ON" &&
			relationship.RelatedElement == "SPDXRef-Package-XText" &&
			strings.Contains(relationship.Comment, "MVS-only") &&
			strings.Contains(relationship.Comment, "x/text linked_package_count remains zero") {
			foundMVSOnly = true
		}
	}
	if !foundRootProduction {
		t.Fatalf("SBOM lacks Cutly -> go-text production relationship: %+v", sbom.Relationships)
	}
	if !foundMVSOnly {
		t.Fatalf("SBOM lacks explicit x/image -> x/text MVS-only relationship: %+v", sbom.Relationships)
	}
}

func TestTextShapeProviderDependenciesArePinnedInCurrentGoModule(t *testing.T) {
	root := cutlyModuleRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(goMod, []byte("github.com/google/jsonschema-go v0.4.2")) ||
		!bytes.Contains(goMod, []byte("github.com/go-text/typesetting v0.3.5")) ||
		!bytes.Contains(goMod, []byte("golang.org/x/image v0.23.0 // indirect")) ||
		!bytes.Contains(goSum, []byte("github.com/google/go-cmp v0.7.0")) {
		t.Fatal("current Cutly module graph no longer matches the compiled dependency lock")
	}
	descriptor := TextShapeCandidate()
	assertLockedFileHash(t, root, "go.mod", descriptor.CutlyGoModSHA256)
	assertLockedFileHash(t, root, "go.sum", descriptor.CutlyGoSumSHA256)
	for _, required := range []string{
		"github.com/go-text/typesetting",
		"github.com/go-text/typesetting-utils",
		"golang.org/x/image",
	} {
		if !bytes.Contains(goSum, []byte(required)) {
			t.Fatalf("linked provider dependency %q is absent from go.sum", required)
		}
	}
}

func assertLockedFileHash(t *testing.T, root, relative, want string) {
	t.Helper()
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "..") {
		t.Fatalf("unsafe locked-file path %q", relative)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read locked file %q: %v", relative, err)
	}
	digest := sha256.Sum256(raw)
	got := "sha256:" + hex.EncodeToString(digest[:])
	if got != want {
		t.Fatalf("locked file %q hash=%s want=%s", relative, got, want)
	}
}

func cutlyModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve buildidentity test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
