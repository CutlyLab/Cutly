package cutly

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDocumentV2FacadeIsCanonicalAndOwned(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := DecodeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDocument(canonical)
	if err != nil {
		t.Fatal(err)
	}
	left, err := DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	right, err := DocumentHash(decoded)
	if err != nil || left != right || !bytes.Equal(canonical, mustCanonical(t, decoded)) {
		t.Fatalf("hash/canonical drift: %q %q %v", left, right, err)
	}
}

func TestDocumentV1FacadeFailsClosedAndMigrates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "fixtures", "document-v1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	v1, err := DecodeDocumentV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	v2, diagnostics, err := MigrateDocumentV1JSON(raw)
	if err != nil || len(diagnostics) != 0 || v2.ID != v1.ID || v2.Revision != v1.Revision {
		t.Fatalf("migration = %+v diagnostics=%+v err=%v", v2, diagnostics, err)
	}
	duplicate := bytes.Replace(raw, []byte(`"schema_version": 1`), []byte(`"schema_version": 1, "schema_version": 1`), 1)
	if _, err := DecodeDocumentV1(duplicate); !errors.Is(err, ErrDocumentV1Invalid) {
		t.Fatalf("duplicate decode = %v", err)
	}
}

func mustCanonical(t *testing.T, document Document) []byte {
	t.Helper()
	value, err := CanonicalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
