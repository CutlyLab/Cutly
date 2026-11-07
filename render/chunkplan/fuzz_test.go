package chunkplan

import (
	"bytes"
	"os"
	"testing"
)

func FuzzDecodeReceipt(f *testing.F) {
	seed, err := os.ReadFile("testdata/video-receipt-v1.canonical.json")
	if err != nil {
		f.Fatal(err)
	}
	seed = bytes.TrimSpace(seed)
	f.Add(seed)
	f.Add([]byte(`{"schema_version":1}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		receipt, err := DecodeReceipt(raw)
		if err != nil {
			return
		}
		canonical, err := CanonicalReceipt(receipt)
		if err != nil {
			t.Fatalf("decoded receipt failed canonical validation: %v", err)
		}
		if string(canonical) != string(raw) {
			t.Fatal("decoded receipt was not canonical")
		}
	})
}
