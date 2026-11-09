package conformance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/backendstage"
	"github.com/CutlyLab/Cutly/render/evidence"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

func evidenceSchemaDocumentV2(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(conformanceModuleRoot(t), "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]*jsonschema.Schema{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(conformanceModuleRoot(t), "schemas", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var s jsonschema.Schema
		decodeStrict(t, raw, &s)
		if registry[s.ID] != nil {
			t.Fatal("duplicate schema ID")
		}
		registry[s.ID] = &s
	}
	s := registry["https://cutly.dev/schemas/cutly.evidence-manifest.document-v2.v2.schema.json"]
	if s == nil {
		t.Fatal("missing evidence schema")
	}
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{Loader: func(u *url.URL) (*jsonschema.Schema, error) {
		key := *u
		key.Fragment = ""
		s := registry[key.String()]
		if s == nil {
			return nil, fmt.Errorf("offline registry missing %s", key.String())
		}
		return s, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
func evidenceFixtureV2(t *testing.T, name string) evidence.ManifestV2 {
	t.Helper()
	raw := fixture(t, "document-v2-chunk-assembly-"+name+".canonical.json")
	if _, err := backendstage.DecodeChunkAssemblyRequestV2(raw); err != nil {
		t.Fatal(err)
	}
	var b evidence.BindingV2
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	seq := func(r videoedit.TickRange, domain string, unit evidence.EvidenceUnit, unitBytes int64) *evidence.DecodedSequence {
		if r.Count == 0 {
			return nil
		}
		return &evidence.DecodedSequence{Domain: domain, Unit: unit, UnitBytes: unitBytes, Range: r, Chunks: []evidence.DecodedChunk{{First: r.First, Count: r.Count, ContentHash: evidence.DigestDecodedBytes([]byte("identity-only.final." + string(unit)))}}}
	}
	m, err := evidence.NewManifestV2(context.Background(), b, evidence.DigestDecodedBytes([]byte("host-owned-worker")), evidence.DigestDecodedBytes([]byte("encoded-artifact")), 1234, seq(b.FrameRange, api.DecodedVideoProfileRGBA8V1, evidence.EvidenceFrames, int64(b.Output.Width)*int64(b.Output.Height)*4), seq(b.SampleRange, api.DecodedAudioProfilePCM16LEV1, evidence.EvidenceSamples, int64(b.Output.Channels)*2))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func evidencePublishV2(t *testing.T, name string, raw []byte) {
	t.Helper()
	if os.Getenv("CUTLY_UPDATE_EVIDENCEV2_FIXTURES") != "1" {
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	for suffix, data := range map[string][]byte{".canonical.json": raw, ".valid.json": append(pretty.Bytes(), '\n'), ".sha256": []byte(hex.EncodeToString(sum[:]) + "\n")} {
		if err := os.WriteFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", "evidence-document-v2-"+name+suffix), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
func TestEvidenceDocumentV2PublishedFixtures(t *testing.T) {
	schema := evidenceSchemaDocumentV2(t)
	for _, name := range []string{"gap-silence", "mixed", "transition", "audio-gap", "visual-silence", "zero-frame", "zero-sample", "fractional"} {
		t.Run(name, func(t *testing.T) {
			m := evidenceFixtureV2(t, name)
			raw, err := evidence.CanonicalManifestV2(m)
			if err != nil {
				t.Fatal(err)
			}
			evidencePublishV2(t, name, raw)
			canonical := fixture(t, "evidence-document-v2-"+name+".canonical.json")
			valid := fixture(t, "evidence-document-v2-"+name+".valid.json")
			if !bytes.Equal(raw, canonical) || !bytes.Equal(executionCanonicalObjectV2(t, decodeExactJSON(t, valid)), raw) {
				t.Fatal("fixture drift")
			}
			sum := sha256.Sum256(raw)
			hash := hex.EncodeToString(sum[:])
			if strings.TrimSpace(string(fixture(t, "evidence-document-v2-"+name+".sha256"))) != hash {
				t.Fatal("hash sidecar drift")
			}
			if os.Getenv("CUTLY_UPDATE_EVIDENCEV2_FIXTURES") == "1" {
				t.Logf("%q: %q,", name, hash)
			} else if evidenceFixtureHashesV2[name] != hash {
				t.Fatal("fixed hash drift")
			}
			for _, data := range [][]byte{valid, canonical} {
				if err := schema.Validate(decodeSchemaInstance(t, data)); err != nil {
					t.Fatal(err)
				}
			}
			decoded, err := evidence.DecodeManifestV2(raw)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := evidence.VerifyManifestV2AgainstBinding(context.Background(), decoded, m.Assembly)
			if err != nil || !verified.Valid() {
				t.Fatal(err)
			}
			if _, err := evidence.DecodeManifestV2(valid); err == nil {
				t.Fatal("accepted pretty JSON")
			}
			if _, err := evidence.Decode(raw); err == nil {
				t.Fatal("V2 interpreted as V1")
			}
			// Constructor is deterministic and does not equate lossy final roots with
			// intermediate RGBA16/PCM roots. No actual encoder is exercised here.
			again := evidenceFixtureV2(t, name)
			if again.Fingerprint != m.Fingerprint {
				t.Fatal("repeatability drift")
			}
			for _, pair := range [][2]*evidence.DecodedSequence{{m.DecodedVideo, again.DecodedVideo}, {m.DecodedAudio, again.DecodedAudio}} {
				if pair[0] != nil && pair[0].RootHash != pair[1].RootHash {
					t.Fatal("decoded roots drift")
				}
			}
		})
	}
}

var evidenceFixtureHashesV2 = map[string]string{
	"gap-silence":    "4f753e10249796c2e5e136b3954c45fb1d741c950b25f41c2f43adcff6210328",
	"mixed":          "9a2ff7e7d9ef786ab5f75aabf892f02ea9186304ecafe03ad58428d68c0b4d5f",
	"transition":     "c197da437004f1f5e82ddbd4481281126b3712caf786c713089009c26212a882",
	"audio-gap":      "44ee6a8e1671da33e66d9c08aa7b1dc582c648ad45b30463e252b23042f33aed",
	"visual-silence": "516433c8f46c03376801fc46decef26f5611e8e4aa4566ae49bceb8ba86e9f9e",
	"zero-frame":     "bc11b7fc2a91f2b187ffc2b811d9f055f17d8955eac71fe844dc497e37aa4dc7",
	"zero-sample":    "67808034599099a7e91b6e9bae22a08932f8ef868ed03f03fe6f44d25b8d0c20",
	"fractional":     "3e8451ea99275cc1ee6f36b6abd4f8c39bca8441e14f4747d2ae89e8951296a3",
}

func TestEvidenceDocumentV2HostileFixtures(t *testing.T) {
	schema := evidenceSchemaDocumentV2(t)
	for _, attack := range []struct {
		name          string
		schemaInvalid bool
		mutate        func(map[string]any)
	}{
		{"profile", true, func(m map[string]any) { m["profile"] = "cutly.evidence-manifest.v1" }},
		{"null-audio", true, func(m map[string]any) { m["decoded_audio"] = nil }},
		{"unknown-output", true, func(m map[string]any) {
			m["assembly"].(map[string]any)["output"].(map[string]any)["mux_profile"] = "unfrozen"
		}},
		{"root-drift", false, func(m map[string]any) {
			m["decoded_video"].(map[string]any)["root_hash"] = m["worker_executable_digest"]
		}},
		{"artifact-drift", false, func(m map[string]any) { m["artifact"].(map[string]any)["uri"] = "file:///other.mp4" }},
	} {
		t.Run(attack.name, func(t *testing.T) {
			m := executionObjectV2(t, fixture(t, "evidence-document-v2-mixed.canonical.json"))
			attack.mutate(m)
			raw := executionCanonicalObjectV2(t, m)
			name := "evidence-document-v2-" + attack.name + ".invalid.json"
			if os.Getenv("CUTLY_UPDATE_EVIDENCEV2_FIXTURES") == "1" {
				if err := os.WriteFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", name), raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			published := fixture(t, name)
			if !bytes.Equal(raw, published) {
				t.Fatal("hostile fixture drift")
			}
			if (schema.Validate(decodeSchemaInstance(t, published)) != nil) != attack.schemaInvalid {
				t.Fatal("schema rejection mismatch")
			}
			out, err := evidence.DecodeManifestV2(published)
			if err == nil || out.Fingerprint != "" {
				t.Fatal("accepted hostile/partial manifest")
			}
		})
	}
}
