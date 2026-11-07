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

func TestOutlineSetFixtureValues(t *testing.T) {
	const (
		wantDescriptorIdentity = "sha256:c184a290af347b61e9342d3ad0fb8772ad54239a3bfc858981919f62993b59f3"
		wantDescriptorJSONSHA  = "8cd4467f4afba2f49d3fc49176b90a135c522dd7b60acefc9a3f20b9dd1877d2"
		wantDescriptorFileSHA  = "e82b3707547758c8233720c7535f02a4c8e72a568558f9491ab987ad40f22132"
		wantOutlineHash        = "sha256:ee6acc35c995982893d71be8ec18f028a7be2cac0564380a19c4df979c9274d7"
		wantReceiptJSONSHA     = "597c4dc8f83198de762bdf001fbed99e3cde5e3096e9d0b7c2f1e0407329e183"
		wantReceiptFileSHA     = "f6f74339119ef7bfc18b9e05785e6b09d6dc9c9c8c09a0965ed9c1ac53d8f54d"
		wantNonemptyHash       = "sha256:bdadf3d480b3adfee52eb03a9ed365c8a712fb7f0b615bb676a854b6623b885c"
		wantNonemptyJSONSHA    = "1f1da2268a81cee01b178e057992aca86cd31524323d0e76cb6cb7c6b1ba22f7"
		wantNonemptyFileSHA    = "525aa8be3e8f89796f14e63a869fd62023734aa05434bd3a9c4aa2b2d0626304"
	)
	descriptorFixture, err := os.ReadFile("testdata/outlineset-profile-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	descriptorJSON := OutlineSetDescriptorCanonicalJSON()
	if !bytes.Equal(descriptorFixture, append(append([]byte(nil), descriptorJSON...), '\n')) {
		t.Fatalf("descriptor fixture drift\ngot=%s", descriptorJSON)
	}
	descriptorDigest := sha256.Sum256(descriptorJSON)
	if hex.EncodeToString(descriptorDigest[:]) != wantDescriptorJSONSHA {
		t.Fatalf("descriptor sha=%x", descriptorDigest)
	}
	descriptorFileDigest := sha256.Sum256(descriptorFixture)
	if hex.EncodeToString(descriptorFileDigest[:]) != wantDescriptorFileSHA {
		t.Fatalf("descriptor file sha=%x", descriptorFileDigest)
	}
	canonicalFixture := descriptorFixture[:len(descriptorFixture)-1]
	preimage := make([]byte, 0, 16+len(OutlineSetDescriptorHashProfileV1)+len(canonicalFixture))
	preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(OutlineSetDescriptorHashProfileV1)))
	preimage = append(preimage, OutlineSetDescriptorHashProfileV1...)
	preimage = binary.BigEndian.AppendUint64(preimage, uint64(len(canonicalFixture)))
	preimage = append(preimage, canonicalFixture...)
	identityDigest := sha256.Sum256(preimage)
	oracleIdentity := "sha256:" + hex.EncodeToString(identityDigest[:])
	if oracleIdentity != wantDescriptorIdentity || OutlineSetDescriptorHash() != oracleIdentity {
		t.Fatalf("descriptor identity oracle=%s production=%s", oracleIdentity, OutlineSetDescriptorHash())
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
	outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatal(err)
	}
	geometry, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := MaterializeOutlineSet(context.Background(), geometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.OutlineSetHash != wantOutlineHash {
		t.Fatalf("outline hash=%s", receipt.OutlineSetHash)
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	if hex.EncodeToString(receiptDigest[:]) != wantReceiptJSONSHA {
		t.Fatalf("receipt sha=%x", receiptDigest)
	}
	receiptFixture, err := os.ReadFile("testdata/minimal-outlineset-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptFixture, append(append([]byte(nil), receiptJSON...), '\n')) {
		t.Fatalf("receipt fixture drift\ngot=%s", receiptJSON)
	}
	receiptFileDigest := sha256.Sum256(receiptFixture)
	if hex.EncodeToString(receiptFileDigest[:]) != wantReceiptFileSHA {
		t.Fatalf("receipt file sha=%x", receiptFileDigest)
	}
	child := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 128, onCurve: false}, {x: 640, onCurve: false}}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | componentScale, glyphID: 1, matrix: []int16{1}}}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{xMax: 1})
	nonemptyGeometry := outlineSetTestGeometry(t, [][]byte{nil, child, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 2, compositeContours: 1, componentElements: 1, depth: 1,
	}, geometryTestBBox{xMax: 640}, 0, -12, 512)
	nonemptySet, nonemptyReceipt, err := MaterializeOutlineSet(context.Background(), nonemptyGeometry, OutlineSetLimits{})
	if err != nil {
		t.Fatal(err)
	}
	nonemptyJSON, err := json.Marshal(nonemptyReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if nonemptyReceipt.OutlineSetHash != wantNonemptyHash {
		t.Fatalf("nonempty hash=%s", nonemptyReceipt.OutlineSetHash)
	}
	nonemptyDigest := sha256.Sum256(nonemptyJSON)
	if hex.EncodeToString(nonemptyDigest[:]) != wantNonemptyJSONSHA {
		t.Fatalf("nonempty receipt sha=%x", nonemptyDigest)
	}
	nonemptyFixture, err := os.ReadFile("testdata/implied-compound-outlineset-receipt-v1.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nonemptyFixture, append(append([]byte(nil), nonemptyJSON...), '\n')) {
		t.Fatalf("nonempty receipt fixture drift\ngot=%s", nonemptyJSON)
	}
	nonemptyFileDigest := sha256.Sum256(nonemptyFixture)
	if hex.EncodeToString(nonemptyFileDigest[:]) != wantNonemptyFileSHA {
		t.Fatalf("nonempty receipt file sha=%x", nonemptyFileDigest)
	}
	view, ok := nonemptySet.Glyph(2)
	if !ok || view.CommandCount() != 4 {
		t.Fatal("missing frozen compound command stream")
	}
	wantCommands := []outlineSetCommand{
		{op: OutlineMoveTo, pointCount: 1, points: [2]OutlinePoint{{X: 2}}},
		{op: OutlineQuadTo, pointCount: 2, points: [2]OutlinePoint{{X: 0}, {X: 2}}},
		{op: OutlineQuadTo, pointCount: 2, points: [2]OutlinePoint{{X: 2}, {X: 2}}},
		{op: OutlineClose},
	}
	for index, want := range wantCommands {
		command, ok := view.Command(uint64(index))
		if !ok || command.Op() != want.op || command.PointCount() != want.pointCount {
			t.Fatalf("frozen command[%d] drift", index)
		}
		for pointIndex := uint8(0); pointIndex < want.pointCount; pointIndex++ {
			got, ok := command.Point(pointIndex)
			if !ok || got != want.points[pointIndex] {
				t.Fatalf("frozen command[%d].point[%d]=%+v", index, pointIndex, got)
			}
		}
	}
}
