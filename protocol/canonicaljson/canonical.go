// Package canonicaljson implements Cutly's cross-language canonical JSON profile.
//
// The profile is intentionally narrower than generic JSON: numbers are integers,
// duplicate decoded object names are invalid, strings must be valid Unicode, and
// object names are ordered by UTF-16 code units. The narrow number grammar keeps
// Go and JavaScript bindings byte-identical without passing protocol integers
// through an IEEE-754 Number.
package canonicaljson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

const Profile = "cutly.json.canonical-integer.v1"

type Code string

const (
	CodeInvalidSyntax     Code = "CANONICAL_JSON.INVALID_SYNTAX"
	CodeInvalidUnicode    Code = "CANONICAL_JSON.INVALID_UNICODE"
	CodeDuplicateMember   Code = "CANONICAL_JSON.DUPLICATE_MEMBER"
	CodeUnsupportedNumber Code = "CANONICAL_JSON.UNSUPPORTED_NUMBER"
	CodeResourceLimit     Code = "CANONICAL_JSON.RESOURCE_LIMIT"
)

// Error is stable enough for callers to branch on Code. Detail is diagnostic
// text and is not part of the wire contract.
type Error struct {
	Code   Code
	Offset int
	Detail string
}

func (err *Error) Error() string {
	return fmt.Sprintf("canonicaljson: %s at byte %d: %s", err.Code, err.Offset, err.Detail)
}

// Limits are deterministic admission limits, not semantic bounds for a
// protocol field. Increasing them never changes the canonical bytes of a value
// that was already admitted.
type Limits struct {
	MaxInputBytes    int
	MaxDepth         int
	MaxValues        int
	MaxStringBytes   int
	MaxIntegerDigits int
}

var DefaultLimits = Limits{
	MaxInputBytes:    64 << 20,
	MaxDepth:         256,
	MaxValues:        1 << 20,
	MaxStringBytes:   64 << 20,
	MaxIntegerDigits: 4096,
}

// Canonicalize validates raw and returns the unique byte representation for
// the decoded JSON value under Profile.
func Canonicalize(raw []byte) ([]byte, error) {
	return CanonicalizeWithLimits(raw, DefaultLimits)
}

func CanonicalizeWithLimits(raw []byte, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if len(raw) > limits.MaxInputBytes {
		return nil, resourceError(0, "input exceeds max_input_bytes")
	}
	if !utf8.Valid(raw) {
		return nil, canonicalError(CodeInvalidUnicode, firstInvalidUTF8(raw), "input is not valid UTF-8")
	}

	parser := parser{raw: raw, limits: limits}
	value, err := parser.parse()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(raw))
	out = appendCanonical(out, value)
	return out, nil
}

// Marshal first applies encoding/json's Go value rules and then the strict
// Cutly profile. Fractional or exponent output from a Go float fails; an exact
// integral float may encode as integer syntax and is therefore admitted.
func Marshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicaljson: marshal value: %w", err)
	}
	return Canonicalize(raw)
}

// Digest returns the canonical bytes and their lowercase SHA-256 identity.
func Digest(raw []byte) (string, []byte, error) {
	canonical, err := Canonicalize(raw)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), canonical, nil
}

func IsCode(err error, code Code) bool {
	var canonicalErr *Error
	return errors.As(err, &canonicalErr) && canonicalErr.Code == code
}

type valueKind uint8

const (
	kindNull valueKind = iota
	kindFalse
	kindTrue
	kindString
	kindNumber
	kindArray
	kindObject
)

type node struct {
	kind   valueKind
	text   string
	array  []node
	object []member
}

type member struct {
	name  string
	key   []uint16
	value node
}

type parser struct {
	raw         []byte
	offset      int
	limits      Limits
	values      int
	stringBytes int
}

func (p *parser) parse() (node, error) {
	p.skipWhitespace()
	value, err := p.parseValue(0)
	if err != nil {
		return node{}, err
	}
	p.skipWhitespace()
	if p.offset != len(p.raw) {
		return node{}, p.syntax("multiple values or trailing data")
	}
	return value, nil
}

func (p *parser) parseValue(depth int) (node, error) {
	if p.values >= p.limits.MaxValues {
		return node{}, resourceError(p.offset, "value count exceeds max_values")
	}
	p.values++
	if p.offset >= len(p.raw) {
		return node{}, p.syntax("expected a value")
	}
	switch p.raw[p.offset] {
	case 'n':
		if err := p.consumeLiteral("null"); err != nil {
			return node{}, err
		}
		return node{kind: kindNull}, nil
	case 'f':
		if err := p.consumeLiteral("false"); err != nil {
			return node{}, err
		}
		return node{kind: kindFalse}, nil
	case 't':
		if err := p.consumeLiteral("true"); err != nil {
			return node{}, err
		}
		return node{kind: kindTrue}, nil
	case '"':
		text, err := p.parseString()
		if err != nil {
			return node{}, err
		}
		return node{kind: kindString, text: text}, nil
	case '[':
		return p.parseArray(depth + 1)
	case '{':
		return p.parseObject(depth + 1)
	default:
		if p.raw[p.offset] == '-' || isDigit(p.raw[p.offset]) {
			return p.parseNumber()
		}
		return node{}, p.syntax("unexpected token")
	}
}

func (p *parser) parseArray(depth int) (node, error) {
	if depth > p.limits.MaxDepth {
		return node{}, resourceError(p.offset, "nesting exceeds max_depth")
	}
	p.offset++
	p.skipWhitespace()
	values := make([]node, 0)
	if p.consumeByte(']') {
		return node{kind: kindArray, array: values}, nil
	}
	for {
		value, err := p.parseValue(depth)
		if err != nil {
			return node{}, err
		}
		values = append(values, value)
		p.skipWhitespace()
		if p.consumeByte(']') {
			return node{kind: kindArray, array: values}, nil
		}
		if !p.consumeByte(',') {
			return node{}, p.syntax("expected ',' or ']' in array")
		}
		p.skipWhitespace()
	}
}

func (p *parser) parseObject(depth int) (node, error) {
	if depth > p.limits.MaxDepth {
		return node{}, resourceError(p.offset, "nesting exceeds max_depth")
	}
	p.offset++
	p.skipWhitespace()
	members := make([]member, 0)
	seen := make(map[string]struct{})
	if p.consumeByte('}') {
		return node{kind: kindObject, object: members}, nil
	}
	for {
		if p.offset >= len(p.raw) || p.raw[p.offset] != '"' {
			return node{}, p.syntax("expected a quoted object member name")
		}
		nameOffset := p.offset
		name, err := p.parseString()
		if err != nil {
			return node{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			return node{}, canonicalError(CodeDuplicateMember, nameOffset, fmt.Sprintf("duplicate decoded member %q", name))
		}
		seen[name] = struct{}{}
		p.skipWhitespace()
		if !p.consumeByte(':') {
			return node{}, p.syntax("expected ':' after object member name")
		}
		p.skipWhitespace()
		value, err := p.parseValue(depth)
		if err != nil {
			return node{}, err
		}
		members = append(members, member{name: name, key: utf16.Encode([]rune(name)), value: value})
		p.skipWhitespace()
		if p.consumeByte('}') {
			break
		}
		if !p.consumeByte(',') {
			return node{}, p.syntax("expected ',' or '}' in object")
		}
		p.skipWhitespace()
	}
	sort.Slice(members, func(i, j int) bool { return utf16UnitsLess(members[i].key, members[j].key) })
	return node{kind: kindObject, object: members}, nil
}

func (p *parser) parseNumber() (node, error) {
	start := p.offset
	if p.consumeByte('-') && p.offset >= len(p.raw) {
		return node{}, p.syntax("incomplete integer")
	}
	if p.offset >= len(p.raw) {
		return node{}, p.syntax("incomplete integer")
	}
	digits := 0
	if p.raw[p.offset] == '0' {
		p.offset++
		digits = 1
		if p.offset < len(p.raw) && isDigit(p.raw[p.offset]) {
			return node{}, p.syntax("integer has a leading zero")
		}
	} else if p.raw[p.offset] >= '1' && p.raw[p.offset] <= '9' {
		for p.offset < len(p.raw) && isDigit(p.raw[p.offset]) {
			p.offset++
			digits++
			if digits > p.limits.MaxIntegerDigits {
				return node{}, resourceError(start, "integer exceeds max_integer_digits")
			}
		}
	} else {
		return node{}, p.syntax("invalid integer")
	}
	if p.offset < len(p.raw) {
		switch p.raw[p.offset] {
		case '.', 'e', 'E':
			return node{}, canonicalError(CodeUnsupportedNumber, start, "only integer number syntax is supported")
		}
	}
	text := string(p.raw[start:p.offset])
	if text == "-0" {
		text = "0"
	}
	return node{kind: kindNumber, text: text}, nil
}

func (p *parser) parseString() (string, error) {
	start := p.offset
	p.offset++
	var out bytes.Buffer
	remaining := p.limits.MaxStringBytes - p.stringBytes
	writeBytes := func(raw []byte) error {
		if len(raw) > remaining-out.Len() {
			return resourceError(start, "decoded strings exceed max_string_bytes")
		}
		_, _ = out.Write(raw)
		return nil
	}
	writeByte := func(value byte) error { return writeBytes([]byte{value}) }
	writeRune := func(value rune) error {
		var encoded [utf8.UTFMax]byte
		count := utf8.EncodeRune(encoded[:], value)
		return writeBytes(encoded[:count])
	}
	for p.offset < len(p.raw) {
		current := p.raw[p.offset]
		if current == '"' {
			p.offset++
			p.stringBytes += out.Len()
			return out.String(), nil
		}
		if current == '\\' {
			p.offset++
			if p.offset >= len(p.raw) {
				return "", p.syntax("unterminated string escape")
			}
			escape := p.raw[p.offset]
			p.offset++
			switch escape {
			case '"', '\\', '/':
				if err := writeByte(escape); err != nil {
					return "", err
				}
			case 'b':
				if err := writeByte('\b'); err != nil {
					return "", err
				}
			case 'f':
				if err := writeByte('\f'); err != nil {
					return "", err
				}
			case 'n':
				if err := writeByte('\n'); err != nil {
					return "", err
				}
			case 'r':
				if err := writeByte('\r'); err != nil {
					return "", err
				}
			case 't':
				if err := writeByte('\t'); err != nil {
					return "", err
				}
			case 'u':
				code, err := p.parseHexQuad()
				if err != nil {
					return "", err
				}
				if code >= 0xD800 && code <= 0xDBFF {
					if p.offset+2 > len(p.raw) || p.raw[p.offset] != '\\' || p.raw[p.offset+1] != 'u' {
						return "", canonicalError(CodeInvalidUnicode, p.offset, "high surrogate is not followed by a low surrogate")
					}
					p.offset += 2
					low, lowErr := p.parseHexQuad()
					if lowErr != nil {
						return "", lowErr
					}
					if low < 0xDC00 || low > 0xDFFF {
						return "", canonicalError(CodeInvalidUnicode, p.offset-4, "high surrogate is not followed by a low surrogate")
					}
					if err := writeRune(utf16.DecodeRune(rune(code), rune(low))); err != nil {
						return "", err
					}
				} else if code >= 0xDC00 && code <= 0xDFFF {
					return "", canonicalError(CodeInvalidUnicode, p.offset-4, "unpaired low surrogate")
				} else {
					if err := writeRune(rune(code)); err != nil {
						return "", err
					}
				}
			default:
				return "", p.syntax("invalid string escape")
			}
			continue
		}
		if current < 0x20 {
			return "", p.syntax("unescaped control character in string")
		}
		r, size := utf8.DecodeRune(p.raw[p.offset:])
		if r == utf8.RuneError && size == 1 {
			return "", canonicalError(CodeInvalidUnicode, p.offset, "invalid UTF-8 in string")
		}
		if err := writeBytes(p.raw[p.offset : p.offset+size]); err != nil {
			return "", err
		}
		p.offset += size
	}
	return "", canonicalError(CodeInvalidSyntax, start, "unterminated string")
}

func (p *parser) parseHexQuad() (uint16, error) {
	if p.offset+4 > len(p.raw) {
		return 0, p.syntax("incomplete Unicode escape")
	}
	var value uint16
	for index := 0; index < 4; index++ {
		nibble, ok := hexNibble(p.raw[p.offset+index])
		if !ok {
			return 0, p.syntax("invalid Unicode escape")
		}
		value = value<<4 | uint16(nibble)
	}
	p.offset += 4
	return value, nil
}

func (p *parser) consumeLiteral(literal string) error {
	if p.offset+len(literal) > len(p.raw) || string(p.raw[p.offset:p.offset+len(literal)]) != literal {
		return p.syntax("invalid literal")
	}
	p.offset += len(literal)
	return nil
}

func (p *parser) skipWhitespace() {
	for p.offset < len(p.raw) {
		switch p.raw[p.offset] {
		case ' ', '\t', '\r', '\n':
			p.offset++
		default:
			return
		}
	}
}

func (p *parser) consumeByte(want byte) bool {
	if p.offset < len(p.raw) && p.raw[p.offset] == want {
		p.offset++
		return true
	}
	return false
}

func (p *parser) syntax(detail string) error {
	return canonicalError(CodeInvalidSyntax, p.offset, detail)
}

func appendCanonical(out []byte, value node) []byte {
	switch value.kind {
	case kindNull:
		return append(out, "null"...)
	case kindFalse:
		return append(out, "false"...)
	case kindTrue:
		return append(out, "true"...)
	case kindString:
		return appendString(out, value.text)
	case kindNumber:
		return append(out, value.text...)
	case kindArray:
		out = append(out, '[')
		for index, child := range value.array {
			if index > 0 {
				out = append(out, ',')
			}
			out = appendCanonical(out, child)
		}
		return append(out, ']')
	case kindObject:
		out = append(out, '{')
		for index, entry := range value.object {
			if index > 0 {
				out = append(out, ',')
			}
			out = appendString(out, entry.name)
			out = append(out, ':')
			out = appendCanonical(out, entry.value)
		}
		return append(out, '}')
	default:
		panic("canonicaljson: unreachable node kind")
	}
}

func appendString(out []byte, value string) []byte {
	const hexDigits = "0123456789abcdef"
	out = append(out, '"')
	for _, r := range value {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				out = append(out, '\\', 'u', '0', '0', hexDigits[byte(r)>>4], hexDigits[byte(r)&0x0f])
			} else {
				out = utf8.AppendRune(out, r)
			}
		}
	}
	return append(out, '"')
}

func utf16UnitsLess(left, right []uint16) bool {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return len(left) < len(right)
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxInputBytes <= 0 || limits.MaxDepth <= 0 || limits.MaxValues <= 0 || limits.MaxStringBytes <= 0 || limits.MaxIntegerDigits <= 0 {
		return Limits{}, canonicalError(CodeResourceLimit, 0, "all limits must be positive")
	}
	return limits, nil
}

func canonicalError(code Code, offset int, detail string) error {
	return &Error{Code: code, Offset: offset, Detail: detail}
}

func resourceError(offset int, detail string) error {
	return canonicalError(CodeResourceLimit, offset, detail)
}

func firstInvalidUTF8(raw []byte) int {
	for offset := 0; offset < len(raw); {
		_, size := utf8.DecodeRune(raw[offset:])
		if size == 1 && raw[offset] >= utf8.RuneSelf {
			return offset
		}
		offset += size
	}
	return 0
}

func isDigit(value byte) bool { return value >= '0' && value <= '9' }

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}
