package sfntglyf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGeometryCanonicalFixturesAndFrozenPhaseOneBindings(t *testing.T) {
	const (
		wantDescriptorIdentity = "sha256:aa878dfd00adb04eedd0c0754c4539f2858aa3f480b2608b212b4b2e3aaaf915"
		wantDescriptorJSONSHA  = "fc2cc77131f4a098ba8be0a3c43fd288f96a80b4a15d9ee1b9d732686accc96f"
		wantDescriptorFileSHA  = "b414634fd10a8cd436b8776658150e647aba65c11c0c5c3c3738a78253abb8bc"
		wantReceiptJSONSHA     = "9a985c77c1715f0d8eb50780d927ccd3a9d6c9416aa7edb9c1314c93e9a79079"
		wantReceiptFileSHA     = "85e55bb557f984ed8e6a24b44d22ec10dfe64718165a7697fe9d7d05306fd587"
		wantGeometryHash       = "sha256:f26ef4f468a72bfa0471db4132260cbf5d1a4341bd4ed41d36e026a54c2d52ea"
		wantPhaseOneDescriptor = "sha256:27d0264b51189b7bbe000eb3df1d685ef79217a8d48d47bc2701b7add3bf66bd"
		wantPhaseOneGraph      = "sha256:5c4fa346a3af8de7d2209f75d73b5945f089b5179ec4c31acdc85653629e012c"
	)
	if GeometryDescriptorHash() != wantDescriptorIdentity {
		t.Fatalf("geometry descriptor identity drift: got %s", GeometryDescriptorHash())
	}
	if OutlineDescriptorHash() != wantPhaseOneDescriptor {
		t.Fatalf("phase-one descriptor identity drift: got %s", OutlineDescriptorHash())
	}
	descriptorFixture, err := os.ReadFile("testdata/geometry-profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	wantDescriptor := append(GeometryDescriptorCanonicalJSON(), '\n')
	if !bytes.Equal(descriptorFixture, wantDescriptor) {
		t.Fatalf("geometry descriptor fixture drift\ngot=%s", GeometryDescriptorCanonicalJSON())
	}
	if digest := sha256.Sum256(GeometryDescriptorCanonicalJSON()); hex.EncodeToString(digest[:]) != wantDescriptorJSONSHA {
		t.Fatalf("geometry descriptor canonical JSON hash drift: got %x", digest)
	}
	if digest := sha256.Sum256(descriptorFixture); hex.EncodeToString(digest[:]) != wantDescriptorFileSHA {
		t.Fatalf("geometry descriptor file hash drift: got %x", digest)
	}
	// Independent descriptor-identity oracle: build the two-field envelope
	// directly from the frozen fixture without calling the production hash
	// helper or reusing its encoder.
	canonicalFixture := descriptorFixture[:len(descriptorFixture)-1]
	identityPreimage := make([]byte, 0, 16+len(GeometryDescriptorHashProfileV1)+len(canonicalFixture))
	identityPreimage = binary.BigEndian.AppendUint64(identityPreimage, uint64(len(GeometryDescriptorHashProfileV1)))
	identityPreimage = append(identityPreimage, GeometryDescriptorHashProfileV1...)
	identityPreimage = binary.BigEndian.AppendUint64(identityPreimage, uint64(len(canonicalFixture)))
	identityPreimage = append(identityPreimage, canonicalFixture...)
	identityDigest := sha256.Sum256(identityPreimage)
	oracleIdentity := "sha256:" + hex.EncodeToString(identityDigest[:])
	if oracleIdentity != wantDescriptorIdentity || GeometryDescriptorHash() != oracleIdentity {
		t.Fatalf("geometry descriptor identity oracle=%s production=%s", oracleIdentity, GeometryDescriptorHash())
	}

	hexFixture, err := os.ReadFile("testdata/minimal-font-v1.hex")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(strings.TrimSpace(string(hexFixture)))
	if err != nil {
		t.Fatal(err)
	}
	font, _, err := Parse(context.Background(), encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	outline, outlineReceipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if outlineReceipt.DescriptorHash != wantPhaseOneDescriptor || outlineReceipt.GraphHash != wantPhaseOneGraph {
		t.Fatalf("phase-one fixture binding drift: descriptor=%s graph=%s", outlineReceipt.DescriptorHash, outlineReceipt.GraphHash)
	}
	_, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GeometryHash != wantGeometryHash {
		t.Fatalf("minimal geometry hash drift: got %s", receipt.GeometryHash)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFixture, err := os.ReadFile("testdata/minimal-geometry-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptFixture, append(receiptJSON, '\n')) {
		t.Fatalf("minimal geometry receipt fixture drift\ngot=%s", receiptJSON)
	}
	if digest := sha256.Sum256(receiptJSON); hex.EncodeToString(digest[:]) != wantReceiptJSONSHA {
		t.Fatalf("minimal geometry receipt canonical JSON hash drift: got %x", digest)
	}
	if digest := sha256.Sum256(receiptFixture); hex.EncodeToString(digest[:]) != wantReceiptFileSHA {
		t.Fatalf("minimal geometry receipt file hash drift: got %x", digest)
	}
}
