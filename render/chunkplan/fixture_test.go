package chunkplan

import (
	"bytes"
	"os"
	"testing"
)

func TestCanonicalFixtures(t *testing.T) {
	descriptor, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := DescriptorCanonicalJSON(); !bytes.Equal(bytes.TrimSpace(descriptor), got) {
		t.Fatalf("descriptor fixture drift\nwant %s\n got %s", bytes.TrimSpace(descriptor), got)
	}
	if want := "sha256:4de78339023e6d1da44946c6c036f5cc622f55e9f13b7896a9453db9a688ffb1"; DescriptorHash() != want {
		t.Fatalf("descriptor hash = %s, want %s", DescriptorHash(), want)
	}
	receiptFixture, err := os.ReadFile("testdata/video-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, testJob("job-fixture", 1, 0))
	receipt := mustReceipt(t, plan.Tasks[0], 1)
	canonical := mustCanonical(t, receipt)
	if !bytes.Equal(bytes.TrimSpace(receiptFixture), canonical) {
		t.Fatalf("receipt fixture drift\nwant %s\n got %s", bytes.TrimSpace(receiptFixture), canonical)
	}
	decoded, err := DecodeReceipt(bytes.TrimSpace(receiptFixture))
	if err != nil || decoded.Fingerprint != receipt.Fingerprint {
		t.Fatalf("fixture decode = %+v, %v", decoded, err)
	}
}
