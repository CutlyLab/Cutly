package textlayout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

const (
	profileFixtureSHA256 = "sha256:35ccf5a7f9318769cd47e38580a0d4d8576ae5f048ca943fe11a1fb48ad17419"
	layoutFixtureSHA256  = "sha256:dc7c031758287b6e9cda0456ac8a9b208414af954e7bc8404b547842f47cbb89"
)

func TestHashesBindExactBytesProfilesAndGeometry(t *testing.T) {
	base := testInput()
	_, baseReceipt := buildTestLayout(t, base, Limits{})
	for name, mutate := range map[string]func(*Input){
		"alignment":            func(value *Input) { value.Alignment = "center" },
		"outline geometry":     func(value *Input) { value.Outlines[0].Commands[1].Points[0].X++ },
		"provider fingerprint": func(value *Input) { value.Binding.ShapeProviderFingerprint = testDigest('5') },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneInput(base)
			mutate(&changed)
			_, receipt := buildTestLayout(t, changed, Limits{})
			switch name {
			case "alignment":
				if receipt.GlyphStreamHash != baseReceipt.GlyphStreamHash || receipt.OutlineSetHash != baseReceipt.OutlineSetHash ||
					receipt.PlacementHash == baseReceipt.PlacementHash || receipt.LayoutHash == baseReceipt.LayoutHash {
					t.Fatalf("unexpected alignment hash partition\nbase=%+v\nchanged=%+v", baseReceipt, receipt)
				}
			case "outline geometry":
				if receipt.GlyphStreamHash != baseReceipt.GlyphStreamHash || receipt.OutlineSetHash == baseReceipt.OutlineSetHash ||
					receipt.LayoutHash == baseReceipt.LayoutHash {
					t.Fatalf("unexpected outline hash partition\nbase=%+v\nchanged=%+v", baseReceipt, receipt)
				}
			case "provider fingerprint":
				if receipt.GlyphStreamHash == baseReceipt.GlyphStreamHash || receipt.LayoutHash == baseReceipt.LayoutHash {
					t.Fatalf("provider binding not hashed\nbase=%+v\nchanged=%+v", baseReceipt, receipt)
				}
			}
		})
	}
	for _, digest := range []string{baseReceipt.GlyphStreamHash, baseReceipt.OutlineSetHash, baseReceipt.PlacementHash, baseReceipt.LayoutHash} {
		if !canonicalSHA256(digest) {
			t.Fatalf("noncanonical receipt digest %q", digest)
		}
	}
}

func TestOriginalUTF8BytesAreNotNormalized(t *testing.T) {
	nfc := oneGlyphInput("é")
	nfd := oneGlyphInput("e\u0301")
	_, nfcReceipt := buildTestLayout(t, nfc, Limits{})
	_, nfdReceipt := buildTestLayout(t, nfd, Limits{})
	if nfcReceipt.GlyphStreamHash == nfdReceipt.GlyphStreamHash || nfcReceipt.LayoutHash == nfdReceipt.LayoutHash {
		t.Fatalf("NFC and NFD hashes collided: nfc=%+v nfd=%+v", nfcReceipt, nfdReceipt)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	input := testInput()
	firstLayout, first := buildTestLayout(t, input, Limits{})
	firstPlacements := placementsForTest(t, firstLayout)
	for iteration := 0; iteration < 32; iteration++ {
		layout, receipt := buildTestLayout(t, input, Limits{})
		if receipt != first {
			t.Fatalf("receipt drift at %d\nfirst=%+v\ngot=%+v", iteration, first, receipt)
		}
		placements := placementsForTest(t, layout)
		if len(placements) != len(firstPlacements) {
			t.Fatalf("placement length drift")
		}
		for index := range placements {
			if placements[index] != firstPlacements[index] {
				t.Fatalf("placement drift at %d: %+v != %+v", index, placements[index], firstPlacements[index])
			}
		}
	}
}

func TestProfileDescriptorCanonicalFixture(t *testing.T) {
	actual, err := json.Marshal(Descriptor())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/profile-v1.canonical.json")
	if err != nil {
		t.Fatalf("read fixture: %v\nactual %s", err, actual)
	}
	trimmed := bytes.TrimSpace(want)
	assertFixtureHash(t, trimmed, profileFixtureSHA256)
	if !bytes.Equal(actual, trimmed) {
		t.Fatalf("profile mismatch\nactual %s\nwant   %s", actual, trimmed)
	}
}

func TestLayoutCanonicalFixture(t *testing.T) {
	layout, receipt := buildTestLayout(t, testInput(), Limits{})
	actual, err := json.Marshal(layoutFixtureForTest(t, layout, receipt))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/layout-v1.canonical.json")
	if err != nil {
		t.Fatalf("read fixture: %v\nactual %s", err, actual)
	}
	trimmed := bytes.TrimSpace(want)
	assertFixtureHash(t, trimmed, layoutFixtureSHA256)
	if !bytes.Equal(actual, trimmed) {
		t.Fatalf("layout mismatch\nactual %s\nwant   %s", actual, trimmed)
	}
}

func assertFixtureHash(t *testing.T, canonical []byte, want string) {
	t.Helper()
	digest := sha256.Sum256(canonical)
	got := "sha256:" + hex.EncodeToString(digest[:])
	if got != want {
		t.Fatalf("fixture hash=%s want=%s", got, want)
	}
}

func TestDescriptorIsValueCopy(t *testing.T) {
	first := Descriptor()
	first.Profile = "mutated"
	first.DefaultLimits.MaxWidth = 1
	second := Descriptor()
	if second.Profile != ProfileV1 || second.DefaultLimits != DefaultLimits() {
		t.Fatalf("descriptor mutated: %+v", second)
	}
}

type layoutFixture struct {
	Receipt    Receipt          `json:"receipt"`
	Placements []GlyphPlacement `json:"placements"`
	Outlines   []outlineFixture `json:"outlines"`
}

type outlineFixture struct {
	GlyphID  uint32           `json:"glyph_id"`
	Commands []commandFixture `json:"commands"`
}

type commandFixture struct {
	Op     OutlineCommandOp `json:"op"`
	Points []Point          `json:"points"`
}

func layoutFixtureForTest(t *testing.T, layout *Layout, receipt Receipt) layoutFixture {
	t.Helper()
	view, err := layout.View()
	if err != nil {
		t.Fatal(err)
	}
	result := layoutFixture{Receipt: receipt, Placements: placementsForTest(t, layout), Outlines: make([]outlineFixture, view.OutlineCount())}
	for outlineIndex := range result.Outlines {
		outline, outlineErr := view.OutlineAt(outlineIndex)
		if outlineErr != nil {
			t.Fatal(outlineErr)
		}
		result.Outlines[outlineIndex].GlyphID = outline.GlyphID()
		result.Outlines[outlineIndex].Commands = make([]commandFixture, outline.CommandCount())
		for commandIndex := range result.Outlines[outlineIndex].Commands {
			info, commandErr := outline.Command(commandIndex)
			if commandErr != nil {
				t.Fatal(commandErr)
			}
			command := commandFixture{Op: info.Op, Points: make([]Point, info.PointCount)}
			for pointIndex := range command.Points {
				command.Points[pointIndex], commandErr = outline.Point(commandIndex, pointIndex)
				if commandErr != nil {
					t.Fatal(commandErr)
				}
			}
			result.Outlines[outlineIndex].Commands[commandIndex] = command
		}
	}
	return result
}

func FuzzBuildRejectsHostileBuffersWithoutPanicking(f *testing.F) {
	for _, seed := range []string{"", "A", "é", "A\nB", "\n", string([]byte{0xff, '\n', 0x80})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 1024 {
			t.Skip()
		}
		input := testInput()
		input.Text = text
		input.Lines = nil
		anyGlyph := false
		start := 0
		for {
			end := start
			for end < len(text) && text[end] != '\n' {
				end++
			}
			line := ShapedLine{ByteStart: uint64(start), ByteEnd: uint64(end)}
			if end > start {
				line.Glyphs = []ShapedGlyph{{GlyphID: 10, ClusterByteOffset: uint64(start)}}
				anyGlyph = true
			}
			input.Lines = append(input.Lines, line)
			if end == len(text) {
				break
			}
			start = end + 1
		}
		if anyGlyph {
			input.Outlines = input.Outlines[:1]
		} else {
			input.Outlines = nil
		}
		_, _, _ = Build(context.Background(), input, Limits{})
	})
}
