// Package buildidentity owns dependency identities that must not be supplied by a document,
// Agent, or render request.
package buildidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	TextShapeCandidateSchemaVersion = 1
	TextShapeCandidateProfileV1     = "cutly.build-identity.text-shape-candidate.v1"
	TextShapeProviderProfileV1      = "cutly.shape.go-text-harfbuzz-static-glyf.v1"
	TextShapeFingerprintProfileV1   = "cutly.hash.sha256.text-shape-dependency-lock-json.v1"
	TextShapeCandidateStatus        = "linked_shape_provider_not_text_render_capability"
)

var (
	ErrDependencyLockInvalid  = errors.New("buildidentity: dependency lock is invalid")
	ErrDependencyLockMismatch = errors.New("buildidentity: dependency lock does not match the compiled identity")
)

// SupplementalNoticeIdentity freezes an additional upstream legal notice belonging to a module.
// It is separate from NoticePath because a module can carry both its license and a patent grant.
type SupplementalNoticeIdentity struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// GoModuleIdentity freezes one module in the current Cutly test, production, or MVS graph.
// LinkedPackageCount counts external packages reachable from Cutly's selected package roots for
// the recorded closure; module-graph-only requirements have count zero.
type GoModuleIdentity struct {
	Role                string                       `json:"role"`
	Closure             string                       `json:"closure"`
	Reachability        string                       `json:"reachability"`
	LinkedPackageCount  int                          `json:"linked_package_count"`
	ModulePath          string                       `json:"module_path"`
	Version             string                       `json:"version"`
	ModuleSum           string                       `json:"module_sum"`
	GoModSum            string                       `json:"go_mod_sum"`
	VCS                 string                       `json:"vcs"`
	Repository          string                       `json:"repository"`
	Commit              string                       `json:"commit"`
	Ref                 string                       `json:"ref"`
	LicenseExpression   string                       `json:"license_expression"`
	SelectedLicense     string                       `json:"selected_license"`
	NoticePath          string                       `json:"notice_path"`
	NoticeSHA256        string                       `json:"notice_sha256"`
	SupplementalNotices []SupplementalNoticeIdentity `json:"supplemental_notices,omitempty"`
}

// EmbeddedSourceIdentity freezes upstream source carried inside a Go module but licensed and
// versioned independently from the module itself.
type EmbeddedSourceIdentity struct {
	Role           string `json:"role"`
	Name           string `json:"name"`
	Version        string `json:"version"`
	Repository     string `json:"repository"`
	Commit         string `json:"commit"`
	PortedByModule string `json:"ported_by_module"`
	License        string `json:"license"`
	NoticePath     string `json:"notice_path"`
	NoticeSHA256   string `json:"notice_sha256"`
}

// TextShapeCandidateDescriptor is the legacy-named host-owned source/dependency lock for the
// linked shape-only provider, not a Text render capability descriptor. CapabilityAdvertised stays
// false until document binding, rasterization, paint, and the complete Text gate exist.
type TextShapeCandidateDescriptor struct {
	SchemaVersion            int                      `json:"schema_version"`
	Profile                  string                   `json:"profile"`
	Status                   string                   `json:"status"`
	ProviderProfile          string                   `json:"provider_profile"`
	FingerprintProfile       string                   `json:"fingerprint_profile"`
	FingerprintSemantics     string                   `json:"fingerprint_semantics"`
	CutlyGoDirective         string                   `json:"cutly_go_directive"`
	CutlyGoModSHA256         string                   `json:"cutly_go_mod_sha256"`
	CutlyGoSumSHA256         string                   `json:"cutly_go_sum_sha256"`
	RequiredCGOEnabled       bool                     `json:"required_cgo_enabled"`
	ImportedByCutly          bool                     `json:"imported_by_cutly"`
	CapabilityAdvertised     bool                     `json:"capability_advertised"`
	ProspectiveGraphTargets  []string                 `json:"prospective_graph_targets"`
	SelectedPackageRoots     []string                 `json:"selected_package_roots"`
	ProductionPackageClosure []string                 `json:"candidate_production_external_package_closure"`
	PackageClosureSemantics  string                   `json:"package_closure_semantics"`
	NoticeManifestPath       string                   `json:"notice_manifest_path"`
	NoticeManifestSHA256     string                   `json:"notice_manifest_sha256"`
	SBOMProfile              string                   `json:"sbom_profile"`
	SBOMPath                 string                   `json:"sbom_path"`
	SBOMSHA256               string                   `json:"sbom_sha256"`
	SBOMSemantics            string                   `json:"sbom_semantics"`
	DependencySemantics      string                   `json:"dependency_semantics"`
	BuildBindingSemantics    string                   `json:"build_binding_semantics"`
	LicenseSemantics         string                   `json:"license_semantics"`
	CapabilitySemantics      string                   `json:"capability_semantics"`
	ExcludedResponsibilities string                   `json:"excluded_responsibilities"`
	Modules                  []GoModuleIdentity       `json:"modules"`
	EmbeddedSources          []EmbeddedSourceIdentity `json:"embedded_sources"`
}

var textShapeCandidateV1 = TextShapeCandidateDescriptor{
	SchemaVersion:           TextShapeCandidateSchemaVersion,
	Profile:                 TextShapeCandidateProfileV1,
	Status:                  TextShapeCandidateStatus,
	ProviderProfile:         TextShapeProviderProfileV1,
	FingerprintProfile:      TextShapeFingerprintProfileV1,
	FingerprintSemantics:    "SHA-256 over u64be(len(fingerprint_profile)) || exact fingerprint-profile UTF-8 bytes || u64be(len(canonical_descriptor)) || the exact minified encoding/json bytes of this map-free ordered descriptor; encode as lowercase hex with the sha256: prefix.",
	CutlyGoDirective:        "1.25.0",
	CutlyGoModSHA256:        "sha256:1f0dce67b6e7acd380f5ec82b19c2f579ebba1f967c89d5cc80bc0ba44d690a3",
	CutlyGoSumSHA256:        "sha256:5e005275891e9a3c81d2add959d38e72e888627396d067cd1226405ea50f18be",
	RequiredCGOEnabled:      false,
	ImportedByCutly:         true,
	CapabilityAdvertised:    false,
	NoticeManifestPath:      "deps/THIRD_PARTY_NOTICES.md",
	ProspectiveGraphTargets: []string{"js/wasm", "linux/amd64", "windows/amd64"},
	SelectedPackageRoots: []string{
		"github.com/go-text/typesetting/font",
		"github.com/go-text/typesetting/font/opentype",
		"github.com/go-text/typesetting/font/opentype/tables",
		"github.com/go-text/typesetting/harfbuzz",
		"github.com/go-text/typesetting/language",
	},
	ProductionPackageClosure: []string{
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
	},
	PackageClosureSemantics:  "The selected roots are Cutly's exact direct external production imports for the shape-only provider. The strictly sorted external package closure was resolved with CGO_ENABLED=0 and is identical on linux/amd64, windows/amd64, and js/wasm. linked_package_count is the actual selected production-package count for linked modules; module-graph-only requirements have count zero. Any toolchain, target, build-tag, import, or version change must re-resolve and refreeze this closure before release.",
	NoticeManifestSHA256:     "sha256:55e2bbf7760b5f88b5ff7b9cec62bad8de2138a0dcbb39b1b9f40dc1f3a3be0d",
	SBOMProfile:              "SPDX-2.3",
	SBOMPath:                 "deps/sbom.spdx.json",
	SBOMSHA256:               "sha256:4af62e59d68bef2cc298d3c337437d763e02f60810a610ec972f9e9dec0e7af2",
	SBOMSemantics:            "The deterministic SPDX-2.3 source SBOM records Cutly's current test, linked shape-provider production, and module-graph-only dependencies separately. Package comments mirror closure, reachability, and linked_package_count, while dependency-lock.json is normative. Dependency presence proves supply-chain identity only and does not advertise final Text render capability.",
	DependencySemantics:      "This lock records the complete current Cutly Go module graph, binds the exact go.mod and go.sum bytes, and freezes the linked pure-Go shaping package closure and MVS module graph. current_production and current_production_transitive contain packages reachable from the selected provider imports; current_module_graph_only entries have no selected packages. Module sums and origins were resolved through the Go module proxy. Any dependency change must explicitly refreeze both module-file hashes and this descriptor; no version, sum, commit, build tag, or embedded HarfBuzz base may silently drift.",
	BuildBindingSemantics:    "cutly_go_directive is the go directive in this source module, not evidence of the toolchain that built a binary. The dependency fingerprint is platform-independent and covers this complete canonical descriptor. Each ShapeProvider receipt combines this compiled fingerprint with the receipt-exact live LayoutGraph, OutlineSet, and font content hashes; documents, Agents and render requests cannot supply or override it. Executable digest and signing remain release concerns.",
	LicenseSemantics:         "Cutly preserves MIT for the current jsonschema-go test import and BSD-3-Clause for its go-cmp module-graph-only upstream test requirement, selects BSD-3-Clause for the dual-licensed go-text modules, preserves BSD-3-Clause plus the upstream PATENTS grant for golang.org/x/image and golang.org/x/text, and preserves the separate MIT notice for the embedded HarfBuzz port. Primary and supplemental notice paths and exact checked-in upstream byte hashes are part of this lock.",
	CapabilitySemantics:      "This lock supports a linked, pinned static-glyf ShapeProvider that privately parses the already validated font lineage, executes go-text HarfBuzz shaping, rejects missing glyphs, and normalizes RTL records. It still cannot advertise cutly.text.pinned-font-viewport-px.v2 or prove document assembly, outline rasterization, paint, compositing, encoding, or final backend capability.",
	ExcludedResponsibilities: "Downloading modules at runtime, resolving floating tags, trusting system fonts, accepting a caller-provided fingerprint, font fallback, document assembly, rasterization, emitting a text surface, signing receipts, and enabling final Text/backend capability are outside this dependency lock.",
	Modules: []GoModuleIdentity{
		{
			Role: "schema_validation_test", Closure: "current_test",
			Reachability: "test_import", LinkedPackageCount: 1,
			ModulePath: "github.com/google/jsonschema-go", Version: "v0.4.2",
			ModuleSum: "h1:tmrUohrwoLZZS/P3x7ex0WAVknEkBZM46iALbcqoRA8=",
			GoModSum:  "h1:r5quNTdLOYEz95Ru18zA0ydNbBuYoo9tgaYcxEYhJVE=",
			VCS:       "git", Repository: "https://github.com/google/jsonschema-go",
			Commit: "e5cd592dc4a48fa0a43967fd6f076eec3824ac13", Ref: "refs/tags/v0.4.2",
			LicenseExpression: "MIT", SelectedLicense: "MIT",
			NoticePath:   "deps/licenses/github.com_google_jsonschema-go_v0.4.2.LICENSE",
			NoticeSHA256: "sha256:2d56c53449691d85d9aea245eb8dac12713e9075d70d5557b82ae1e94805b357",
		},
		{
			Role: "schema_upstream_test", Closure: "current_module_graph_only",
			Reachability: "upstream_test", LinkedPackageCount: 0,
			ModulePath: "github.com/google/go-cmp", Version: "v0.7.0",
			ModuleSum: "h1:wk8382ETsv4n6YpYiWiBsYLSJiTsyBybVuN8=",
			GoModSum:  "h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=",
			VCS:       "git", Repository: "https://github.com/google/go-cmp",
			Commit: "9b12f366a942ebc7254abc7f32ca05068b455fb7", Ref: "refs/tags/v0.7.0",
			LicenseExpression: "BSD-3-Clause", SelectedLicense: "BSD-3-Clause",
			NoticePath:   "deps/licenses/github.com_google_go-cmp_v0.7.0.LICENSE",
			NoticeSHA256: "sha256:17b5d209ba8f9684257ecfcff87df6ceda6194143a8fbd074f29727cff6f0c40",
		},
		{
			Role: "shape_runtime", Closure: "current_production",
			Reachability: "production_import", LinkedPackageCount: 8,
			ModulePath: "github.com/go-text/typesetting", Version: "v0.3.5",
			ModuleSum: "h1:XZPUooClHY0Vf/rFyUyuPRNEkawARaFzLMQcXLSEyPk=",
			GoModSum:  "h1:XZO1hD+nQVyvVa5IicQk7FsCa4PFQaJ2soWAP1f//68=",
			VCS:       "git", Repository: "https://github.com/go-text/typesetting",
			Commit: "19482bd51f2338e34bacd8cfd3288a04bbadc598", Ref: "refs/tags/v0.3.5",
			LicenseExpression: "Unlicense OR BSD-3-Clause", SelectedLicense: "BSD-3-Clause",
			NoticePath:   "deps/licenses/github.com_go-text_typesetting_v0.3.5.LICENSE",
			NoticeSHA256: "sha256:6b1bf94c77d7faa0ce94f8745595bfaf4c404f8b3873f5c2ce55ca15f8e44eca",
		},
		{
			Role: "shape_runtime_transitive", Closure: "current_production_transitive",
			Reachability: "production_import", LinkedPackageCount: 3,
			ModulePath: "golang.org/x/image", Version: "v0.23.0",
			ModuleSum: "h1:HseQ7c2OpPKTPVzNjG5fwJsOTCiiwS4QdsYi5XU6H68=",
			GoModSum:  "h1:wJJBTdLfCCf3tiHa1fNxpZmUI4mmoZvwMCPP0ddoNKY=",
			VCS:       "git", Repository: "https://go.googlesource.com/image",
			Commit: "941f2100a0c205b9588e6c977c6acd0fd1f6a094", Ref: "refs/tags/v0.23.0",
			LicenseExpression: "BSD-3-Clause", SelectedLicense: "BSD-3-Clause",
			NoticePath:   "deps/licenses/golang.org_x_image_v0.23.0.LICENSE",
			NoticeSHA256: "sha256:911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad",
			SupplementalNotices: []SupplementalNoticeIdentity{{
				Kind: "patent_grant", Path: "deps/licenses/golang.org_x_image_v0.23.0.PATENTS",
				SHA256: "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
			}},
		},
		{
			Role: "shape_upstream_test_generate", Closure: "current_module_graph_only",
			Reachability: "upstream_test_generate", LinkedPackageCount: 0,
			ModulePath: "github.com/go-text/typesetting-utils",
			Version:    "v0.0.0-20260419141703-4ffe8874dabc",
			ModuleSum:  "h1:8FGo2It5K75XkavhTiCKExUfVaVDS1feBnLCru5qeoY=",
			GoModSum:   "h1:3/62I4La/HBRX9TcTpBj4eipLiwzf+vhI+7whTc9V7o=",
			VCS:        "git", Repository: "https://github.com/go-text/typesetting-utils",
			Commit: "4ffe8874dabcb8a86a3c8534d2545ea5b95304ea", Ref: "",
			LicenseExpression: "Unlicense OR BSD-3-Clause", SelectedLicense: "BSD-3-Clause",
			NoticePath:   "deps/licenses/github.com_go-text_typesetting-utils_4ffe8874dabc.LICENSE",
			NoticeSHA256: "sha256:a1e5b69b12756927cfe3b3a373df05a55c1aaa69c6b259e06d8746257cb17d06",
		},
		{
			Role: "shape_module_graph_transitive", Closure: "current_module_graph_only",
			Reachability: "module_graph_only", LinkedPackageCount: 0,
			ModulePath: "golang.org/x/text", Version: "v0.21.0",
			ModuleSum: "h1:zyQAAkrwaneQ066sspRyJaG9VNi/YJ1NfzcGB3hZ/qo=",
			GoModSum:  "h1:4IBbMaMmOPCJ8SecivzSH54+73PCFmPWxNTLm+vZkEQ=",
			VCS:       "git", Repository: "https://go.googlesource.com/text",
			Commit: "d42948e5579eb996bedb7df76c7ad57fae4e83c7", Ref: "refs/tags/v0.21.0",
			LicenseExpression: "BSD-3-Clause", SelectedLicense: "BSD-3-Clause",
			NoticePath:   "deps/licenses/golang.org_x_text_v0.21.0.LICENSE",
			NoticeSHA256: "sha256:911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad",
			SupplementalNotices: []SupplementalNoticeIdentity{{
				Kind: "patent_grant", Path: "deps/licenses/golang.org_x_text_v0.21.0.PATENTS",
				SHA256: "sha256:96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc",
			}},
		},
	},
	EmbeddedSources: []EmbeddedSourceIdentity{
		{
			Role: "shape_algorithm_port", Name: "HarfBuzz", Version: "12.3.0",
			Repository:     "https://github.com/harfbuzz/harfbuzz",
			Commit:         "5a31dd02f0b32de12336f72db9297bfe94cf0da1",
			PortedByModule: "github.com/go-text/typesetting@v0.3.5", License: "MIT",
			NoticePath:   "deps/licenses/github.com_go-text_typesetting_v0.3.5.harfbuzz.LICENSE",
			NoticeSHA256: "sha256:e45d96558ba3d406f95dffed6f22c3be07093475ef617bcfddf88444d6b66788",
		},
	},
}

// TextShapeCandidate returns a defensive copy of the compiled host-owned dependency identity.
func TextShapeCandidate() TextShapeCandidateDescriptor {
	result := textShapeCandidateV1
	result.ProspectiveGraphTargets = append([]string(nil), textShapeCandidateV1.ProspectiveGraphTargets...)
	result.SelectedPackageRoots = append([]string(nil), textShapeCandidateV1.SelectedPackageRoots...)
	result.ProductionPackageClosure = append([]string(nil), textShapeCandidateV1.ProductionPackageClosure...)
	result.Modules = append([]GoModuleIdentity(nil), textShapeCandidateV1.Modules...)
	for index := range result.Modules {
		result.Modules[index].SupplementalNotices = append(
			[]SupplementalNoticeIdentity(nil), textShapeCandidateV1.Modules[index].SupplementalNotices...,
		)
	}
	result.EmbeddedSources = append([]EmbeddedSourceIdentity(nil), textShapeCandidateV1.EmbeddedSources...)
	return result
}

// TextShapeCandidateCanonicalJSON returns the exact bytes hashed by
// TextShapeCandidateFingerprint. The returned slice is caller-owned.
func TextShapeCandidateCanonicalJSON() []byte {
	canonical, err := json.Marshal(textShapeCandidateV1)
	if err != nil {
		panic(fmt.Sprintf("buildidentity: marshal compiled text-shape descriptor: %v", err))
	}
	return canonical
}

// TextShapeCandidateFingerprint derives the platform-independent dependency identity from
// compiled constants. There is no caller-controlled override path.
func TextShapeCandidateFingerprint() string {
	canonical := TextShapeCandidateCanonicalJSON()
	digest := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(TextShapeFingerprintProfileV1)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(TextShapeFingerprintProfileV1))
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// VerifyTextShapeDependencyLock accepts only the exact strict canonical payload compiled into
// this package, with at most one conventional final LF in the file. A canonical but modified lock
// still fails; recomputing its own SHA is insufficient.
func VerifyTextShapeDependencyLock(raw []byte) (string, error) {
	var decoded TextShapeCandidateDescriptor
	if err := wirejson.Decode(raw, &decoded); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDependencyLockInvalid, err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("%w: marshal: %v", ErrDependencyLockInvalid, err)
	}
	payload := raw
	if len(payload) > 0 && payload[len(payload)-1] == '\n' {
		payload = payload[:len(payload)-1]
	}
	if !bytes.Equal(payload, canonical) {
		return "", fmt.Errorf("%w: wire bytes are not canonical", ErrDependencyLockInvalid)
	}
	expected := TextShapeCandidateCanonicalJSON()
	if !bytes.Equal(canonical, expected) {
		return "", ErrDependencyLockMismatch
	}
	return TextShapeCandidateFingerprint(), nil
}
