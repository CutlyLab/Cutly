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

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/backendstage"
	"github.com/CutlyLab/Cutly/render/evidence"
	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

const assemblySchemaV2 = "cutly.chunk-assembly-request.document-v2.v2.schema.json"

func assemblySchemaDocumentV2(t *testing.T) *jsonschema.Resolved {
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
	s := registry["https://cutly.dev/schemas/"+assemblySchemaV2]
	if s == nil {
		t.Fatal("missing assembly schema")
	}
	result, err := s.Resolve(&jsonschema.ResolveOptions{Loader: func(u *url.URL) (*jsonschema.Schema, error) {
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
	return result
}

func assemblyFixtureRequestV2(t *testing.T, source api.RenderRequestV2) backendstage.ChunkAssemblyRequestV2 {
	t.Helper()
	r := backendstage.ChunkAssemblyRequestV2{JobID: source.Job.ID, ArtifactURI: source.Job.ArtifactURI, DocumentHash: source.Plan.DocumentHash, PlanFingerprint: source.Plan.Fingerprint, ExecutionManifestFingerprint: source.Manifest.Fingerprint, TimelineRange: source.Job.TimelineRange, FrameRange: source.Job.FrameRange, SampleRange: source.Job.SampleRange, Target: source.Plan.Target, MediaRuntime: source.Manifest.MediaRuntime, Output: source.Manifest.Output, ProviderExecutableDigest: source.Manifest.ProviderBuild, DeterministicSeed: source.Manifest.DeterministicSeed, Aggregate: backendstage.ChunkAggregateBindingV2{Profile: backendstage.ChunkAggregateBindingProfileV2, ResumeMode: backendstage.ChunkAssemblyResumeModeV2}, Audio: []backendstage.ChunkDescriptorV2{}, Video: []backendstage.ChunkDescriptorV2{}}
	build := func(expected videoedit.TickRange, domain, prefix string, unitBytes, partition int64, unit evidence.EvidenceUnit) ([]backendstage.ChunkDescriptorV2, *backendstage.ChunkAggregateRootV2) {
		descriptors := []backendstage.ChunkDescriptorV2{}
		if expected.Count == 0 {
			return descriptors, nil
		}
		sequence := evidence.DecodedSequence{Domain: domain, Unit: unit, UnitBytes: unitBytes, Range: expected, Chunks: []evidence.DecodedChunk{}}
		next, left := expected.First, expected.Count
		for i := 0; left > 0; i++ {
			count := partition
			if left < count {
				count = left
			}
			sum := sha256.Sum256([]byte(fmt.Sprintf("identity-only.%s.%d", prefix, i)))
			hash := "sha256:" + hex.EncodeToString(sum[:])
			keySum := sha256.Sum256([]byte("key." + hash))
			d := backendstage.ChunkDescriptorV2{TaskID: fmt.Sprintf("%s-%05d", prefix, i), Domain: domain, CacheKey: cachekey.Key{SchemaVersion: 1, Profile: cachekey.ProfileV1, Namespace: "cutly.conformance.document-v2.v2", ArtifactKind: "cutly.chunk." + prefix + ".v2", Digest: "sha256:" + hex.EncodeToString(keySum[:])}, Range: videoedit.TickRange{First: next, Count: count}, ContentHash: hash, SizeBytes: count * unitBytes, UnitBytes: unitBytes}
			descriptors = append(descriptors, d)
			sequence.Chunks = append(sequence.Chunks, evidence.DecodedChunk{First: next, Count: count, ContentHash: hash})
			next += count
			left -= count
		}
		hash, err := evidence.ComputeDecodedRoot(sequence)
		if err != nil {
			t.Fatal(err)
		}
		return descriptors, &backendstage.ChunkAggregateRootV2{Domain: domain, UnitBytes: unitBytes, Range: expected, ChunkCount: len(descriptors), RootHash: hash}
	}
	r.Audio, r.Aggregate.Audio = build(r.SampleRange, api.AudioBlockProfileV2, "audio", int64(r.Target.Channels)*2, backendstage.ChunkAssemblySampleBlockCountV2, evidence.EvidenceSamples)
	r.Video, r.Aggregate.Video = build(r.FrameRange, api.VideoSurfaceProfileV2, "video", int64(r.Target.Width)*int64(r.Target.Height)*8, backendstage.ChunkAssemblyFrameGroupCountV2, evidence.EvidenceFrames)
	result, err := backendstage.FreezeChunkAssemblyRequestV2(context.Background(), source.Plan, source.Manifest, r)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func publishAssemblyFixtureV2(t *testing.T, prefix string, raw []byte) {
	t.Helper()
	if os.Getenv("CUTLY_UPDATE_ASSEMBLYV2_FIXTURES") != "1" {
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	for suffix, data := range map[string][]byte{".canonical.json": raw, ".valid.json": append(pretty.Bytes(), '\n'), ".sha256": []byte(hex.EncodeToString(sum[:]) + "\n")} {
		if err := os.WriteFile(filepath.Join(conformanceModuleRoot(t), "conformance", "fixtures", prefix+suffix), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDocumentV2AssemblyPublishedFixtures(t *testing.T) {
	schema := assemblySchemaDocumentV2(t)
	for _, name := range []string{"gap-silence", "mixed", "transition", "audio-gap", "visual-silence", "zero-frame", "zero-sample", "fractional"} {
		t.Run(name, func(t *testing.T) {
			source, err := api.DecodeRenderRequestV2(fixture(t, "document-v2-render-request-"+name+".canonical.json"))
			if err != nil {
				t.Fatal(err)
			}
			r := assemblyFixtureRequestV2(t, source)
			raw, err := backendstage.CanonicalChunkAssemblyRequestV2(r)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "document-v2-chunk-assembly-" + name
			publishAssemblyFixtureV2(t, prefix, raw)
			canonical := fixture(t, prefix+".canonical.json")
			valid := fixture(t, prefix+".valid.json")
			if !bytes.Equal(raw, canonical) {
				t.Fatal("builder/canonical fixture drift")
			}
			normalized := executionCanonicalObjectV2(t, decodeExactJSON(t, valid))
			if !bytes.Equal(normalized, raw) {
				t.Fatal("valid/canonical drift")
			}
			sum := sha256.Sum256(raw)
			hash := hex.EncodeToString(sum[:])
			if strings.TrimSpace(string(fixture(t, prefix+".sha256"))) != hash {
				t.Fatal("hash sidecar drift")
			}
			if os.Getenv("CUTLY_UPDATE_ASSEMBLYV2_FIXTURES") == "1" {
				t.Logf("%q: %q,", name, hash)
			} else if assemblyFixtureHashesV2[name] != hash {
				t.Fatal("fixed protocol hash drift")
			}
			for _, data := range [][]byte{valid, canonical} {
				if err := schema.Validate(decodeSchemaInstance(t, data)); err != nil {
					t.Fatal(err)
				}
			}
			decoded, err := backendstage.DecodeChunkAssemblyRequestV2(raw)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := backendstage.VerifyChunkAssemblyRequestV2AgainstPlan(context.Background(), decoded, source.Manifest, source.Plan)
			if err != nil || !verified.Valid() {
				t.Fatalf("plan-bound verify: %v", err)
			}
			if _, err := backendstage.DecodeChunkAssemblyRequestV2(valid); err == nil {
				t.Fatal("accepted pretty JSON")
			}
			if _, err := api.DecodeRenderRequestV2(raw); err == nil {
				t.Fatal("assembly accepted as render request")
			}
		})
	}
}

var assemblyFixtureHashesV2 = map[string]string{
	"gap-silence":    "9724cb7c36e2bdfbd230ea0264dd140a88fdce287b3bad9816a9e9cd24318b63",
	"mixed":          "a52d59770252320c8006763d362d571cd8cb5d081c7060742a6a98a95dd6b3ee",
	"transition":     "260385cd011cc6bd5737db8d340a7995118d7a1ce8d40da894f500266f8adf22",
	"audio-gap":      "5598ce7c3e15cb47379c2eb6573cc3e4569e4c4162d4d87ee5d9d57b2b59e824",
	"visual-silence": "695b18ab7baf73df21e188c7aa2415863a13549cbf7c6e5ff597106277e0d795",
	"zero-frame":     "4225e4ada0d39fbf1709eae5e28c97f049b3c256612dbb2ea92def0702aae6a7",
	"zero-sample":    "4958ca53f4e30b980997bd1dcf57b2e74ddf627a39db6af45f291d69092bd7d8",
	"fractional":     "bda9c3f47e90073e9dc49a192ca208a512b7b35a23e3e5fc82e595440560eace",
}

func TestDocumentV2AssemblyHostileFixtures(t *testing.T) {
	schema := assemblySchemaDocumentV2(t)
	for _, attack := range []struct {
		name          string
		schemaInvalid bool
		mutate        func(map[string]any)
	}{
		{"profile", true, func(r map[string]any) { r["profile"] = backendstage.ChunkAssemblyProfileV1 }},
		{"unknown-intermediate", true, func(r map[string]any) {
			r["output"].(map[string]any)["intermediate"].(map[string]any)["implicit_conversion"] = true
		}},
		{"null-audio", true, func(r map[string]any) { r["audio"] = nil }},
		{"root-drift", false, func(r map[string]any) {
			r["aggregate"].(map[string]any)["video"].(map[string]any)["root_hash"] = r["document_hash"]
		}},
	} {
		t.Run(attack.name, func(t *testing.T) {
			r := executionObjectV2(t, fixture(t, "document-v2-chunk-assembly-mixed.canonical.json"))
			attack.mutate(r)
			raw := executionCanonicalObjectV2(t, r)
			name := "document-v2-chunk-assembly-" + attack.name + ".invalid.json"
			if os.Getenv("CUTLY_UPDATE_ASSEMBLYV2_FIXTURES") == "1" {
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
			rout, err := backendstage.DecodeChunkAssemblyRequestV2(published)
			if err == nil || rout.Fingerprint != "" {
				t.Fatal("accepted hostile/partial request")
			}
		})
	}
}
