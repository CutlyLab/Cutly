package documentv2

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"unicode"
)

func TestTextUnicodeValidationTablesV1HashAndBoundaries(t *testing.T) {
	tables := textUnicodeValidationTablesForTest()
	if got := textUnicodeTableHashForTest(tables); got != TextUnicodeValidationTableHashV1 {
		t.Fatalf("frozen Unicode table hash=%s want=%s", got, TextUnicodeValidationTableHashV1)
	}
	for _, table := range tables {
		for index, item := range table.ranges {
			if item.lo > item.hi || item.hi > unicode.MaxRune || item.stride == 0 ||
				index > 0 && table.ranges[index-1].hi >= item.lo {
				t.Fatalf("invalid %s range %d: %+v", table.name, index, item)
			}
		}
	}

	for _, test := range []struct {
		name   string
		value  rune
		script string
		want   bool
	}{
		{name: "latin", value: 'A', script: TextScriptLatin, want: true},
		{name: "han", value: '剪', script: TextScriptHan, want: true},
		{name: "arabic", value: 'ع', script: TextScriptArabic, want: true},
		{name: "common digit", value: '7', script: TextScriptArabic, want: true},
		{name: "inherited mark", value: '\u0301', script: TextScriptLatin, want: true},
		{name: "mixed cyrillic", value: 'Ж', script: TextScriptLatin, want: false},
		{name: "unknown script", value: 'A', script: "Cyrl", want: false},
		{name: "negative", value: -1, script: TextScriptLatin, want: false},
		{name: "past max", value: unicode.MaxRune + 1, script: TextScriptLatin, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := textRuneMatchesFrozenScript(test.value, test.script); got != test.want {
				t.Fatalf("match(%U,%q)=%v want=%v", test.value, test.script, got, test.want)
			}
		})
	}

	if !textRuneIsFrozenControlV1(0) || !textRuneIsFrozenControlV1(0x9f) || textRuneIsFrozenControlV1('A') {
		t.Fatal("frozen Cc boundary drift")
	}
	if !textRuneIsFrozenFormatV1(0x200d) || textRuneIsFrozenFormatV1('A') {
		t.Fatal("frozen Cf boundary drift")
	}
	if !textRuneIsFrozenVariationSelectorV1(0xfe0f) || !textRuneIsFrozenVariationSelectorV1(0xe01ef) ||
		textRuneIsFrozenVariationSelectorV1(0xfe10) {
		t.Fatal("frozen variation-selector boundary drift")
	}
	for _, value := range []rune{0x00ad, 0x034f, 0x061c, 0x2065, 0xfff8, 0xe0000, 0xe0fff} {
		if !textRuneIsFrozenDefaultIgnorableV1(value) {
			t.Fatalf("Default_Ignorable_Code_Point missing %U", value)
		}
	}
	for _, value := range []rune{' ', 0x0600, 0xfff9, 0x13430, 0xe1000} {
		if textRuneIsFrozenDefaultIgnorableV1(value) {
			t.Fatalf("Default_Ignorable_Code_Point unexpectedly contains %U", value)
		}
	}
	if got := countFrozenUnicodeCodePointsForTest(textUnicodeDefaultIgnorableV1[:]); got != 4174 {
		t.Fatalf("Default_Ignorable_Code_Point count=%d want=4174", got)
	}
}

func TestTextUnicodeValidationTablesV1MatchGenerationSource(t *testing.T) {
	if unicode.Version != TextUnicodeDataVersionV1 {
		t.Skipf("stdlib Unicode %s differs from frozen generation source %s", unicode.Version, TextUnicodeDataVersionV1)
	}
	tables := []struct {
		name   string
		frozen []frozenUnicodeRangeV1
		source *unicode.RangeTable
	}{
		{name: "Latn", frozen: textUnicodeLatnV1[:], source: unicode.Latin},
		{name: "Hani", frozen: textUnicodeHaniV1[:], source: unicode.Han},
		{name: "Arab", frozen: textUnicodeArabV1[:], source: unicode.Arabic},
		{name: "Common", frozen: textUnicodeCommonV1[:], source: unicode.Scripts["Common"]},
		{name: "Inherited", frozen: textUnicodeInheritedV1[:], source: unicode.Scripts["Inherited"]},
		{name: "Cf", frozen: textUnicodeCfV1[:], source: unicode.Cf},
		{name: "Cc", frozen: textUnicodeCcV1[:], source: unicode.Cc},
		{name: "VariationSelector", frozen: textUnicodeVariationSelectorV1[:], source: unicode.Properties["Variation_Selector"]},
	}
	for value := rune(0); value <= unicode.MaxRune; value++ {
		for _, test := range tables {
			if got, want := textRuneInFrozenTableV1(value, test.frozen), unicode.Is(test.source, value); got != want {
				t.Fatalf("%s parity at %U: frozen=%v source=%v", test.name, value, got, want)
			}
		}
	}
}

type textUnicodeTableForTest struct {
	name   string
	ranges []frozenUnicodeRangeV1
}

func textUnicodeValidationTablesForTest() []textUnicodeTableForTest {
	return []textUnicodeTableForTest{
		{name: "Latn", ranges: textUnicodeLatnV1[:]},
		{name: "Hani", ranges: textUnicodeHaniV1[:]},
		{name: "Arab", ranges: textUnicodeArabV1[:]},
		{name: "Common", ranges: textUnicodeCommonV1[:]},
		{name: "Inherited", ranges: textUnicodeInheritedV1[:]},
		{name: "Cf", ranges: textUnicodeCfV1[:]},
		{name: "Cc", ranges: textUnicodeCcV1[:]},
		{name: "VariationSelector", ranges: textUnicodeVariationSelectorV1[:]},
		{name: "DefaultIgnorable", ranges: textUnicodeDefaultIgnorableV1[:]},
	}
}

func textUnicodeTableHashForTest(tables []textUnicodeTableForTest) string {
	hasher := sha256.New()
	textUnicodeWriteStringForTest(hasher, TextUnicodeValidationTableHashProfileV1)
	textUnicodeWriteStringForTest(hasher, TextUnicodeDataVersionV1)
	textUnicodeWriteUint32ForTest(hasher, uint32(len(tables)))
	for _, table := range tables {
		textUnicodeWriteStringForTest(hasher, table.name)
		textUnicodeWriteUint32ForTest(hasher, uint32(len(table.ranges)))
		for _, item := range table.ranges {
			textUnicodeWriteUint32ForTest(hasher, item.lo)
			textUnicodeWriteUint32ForTest(hasher, item.hi)
			textUnicodeWriteUint32ForTest(hasher, item.stride)
		}
	}
	return fmt.Sprintf("sha256:%x", hasher.Sum(nil))
}

type textUnicodeByteWriterForTest interface {
	Write([]byte) (int, error)
}

func textUnicodeWriteStringForTest(writer textUnicodeByteWriterForTest, value string) {
	textUnicodeWriteUint32ForTest(writer, uint32(len(value)))
	_, _ = writer.Write([]byte(value))
}

func textUnicodeWriteUint32ForTest(writer textUnicodeByteWriterForTest, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}

func countFrozenUnicodeCodePointsForTest(ranges []frozenUnicodeRangeV1) uint64 {
	var result uint64
	for _, item := range ranges {
		result += uint64(item.hi-item.lo)/uint64(item.stride) + 1
	}
	return result
}
