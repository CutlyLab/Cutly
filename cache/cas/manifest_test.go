package cas

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEntryManifestAndSentinelCanonicalFixtures(t *testing.T) {
	raw := bytes.TrimSpace(readCASTestFixture(t, "entry-manifest-v1.canonical.json"))
	manifest, err := decodeEntryManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalEntryManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, canonical) {
		t.Fatalf("manifest fixture drift\nwant %s\n got %s", raw, canonical)
	}
	wantSentinel := readCASTestFixture(t, "entry-complete-v1.canonical.txt")
	if got := completeSentinel(raw); !bytes.Equal(got, wantSentinel) {
		t.Fatalf("sentinel drift\nwant %q\n got %q", wantSentinel, got)
	}
	if err := validateCompleteSentinel(wantSentinel, raw); err != nil {
		t.Fatal(err)
	}
}

func TestEntryManifestStrictDecodeAndCompleteBinding(t *testing.T) {
	raw := bytes.TrimSpace(readCASTestFixture(t, "entry-manifest-v1.canonical.json"))
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	duplicate := append([]byte(`{"schema_version":1,`), raw[1:]...)
	caseAlias := bytes.Replace(raw, []byte(`"payload_size"`), []byte(`"Payload_Size"`), 1)
	for name, hostile := range map[string][]byte{"unknown": unknown, "duplicate": duplicate, "case": caseAlias} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEntryManifest(hostile); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	manifest, err := decodeEntryManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	baseSentinel := completeSentinel(raw)
	mutations := []func(*entryManifest){
		func(value *entryManifest) { value.StageKey.Digest = "sha256:" + strings.Repeat("1", 64) },
		func(value *entryManifest) { value.PayloadHash = "sha256:" + strings.Repeat("2", 64) },
		func(value *entryManifest) { value.PayloadSize++ },
	}
	for index, mutate := range mutations {
		changed := manifest
		mutate(&changed)
		changedRaw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(completeSentinel(changedRaw), baseSentinel) {
			t.Fatalf("mutation %d did not change sentinel", index)
		}
		if err := validateCompleteSentinel(baseSentinel, changedRaw); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("mutation %d binding error=%v", index, err)
		}
	}
}

func readCASTestFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
