package localv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

// canonical deliberately does not use an integer-only protocol canonicalizer:
// RawMessage metadata can contain fractional and arbitrarily precise numbers.
func canonical(value any, maximum uint64) ([]byte, error) {
	budget := maximum
	if err := admitValue(reflect.ValueOf(value), &budget, 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if uint64(len(raw)) > maximum {
		return nil, ErrLimit
	}
	if err := scan(raw); err != nil {
		return nil, err
	}
	if err := wirejson.Validate(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	result, err := json.Marshal(normalized)
	if uint64(len(result)) > maximum {
		return nil, ErrLimit
	}
	return result, err
}

// Admission precedes encoding allocations and recursive semantic validation.
// Count a conservative encoded size, including all RawMessage bytes. Depth is
// bounded even for hostile in-memory cyclic pointers/maps.
func admitValue(v reflect.Value, budget *uint64, depth int) error {
	if depth > 128 {
		return ErrLimit
	}
	take := func(n uint64) error {
		if n > *budget {
			return ErrLimit
		}
		*budget -= n
		return nil
	}
	if !v.IsValid() {
		return take(4)
	}
	if v.Type() == reflect.TypeOf(documentv2.ID("")) && v.String() != "" && !identity(v.String(), DefaultLimits().MaxIdentityBytes) {
		return ErrInvalid
	}
	if v.Type() == reflect.TypeOf(json.RawMessage{}) {
		if !utf8.Valid(v.Bytes()) {
			return ErrInvalid
		}
		return take(uint64(v.Len()) + 4)
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return take(4)
		}
		return admitValue(v.Elem(), budget, depth+1)
	case reflect.String:
		s := v.String()
		if !utf8.ValidString(s) {
			return ErrInvalid
		}
		n := uint64(2)
		for _, r := range s {
			switch {
			case r < 0x20:
				n += 6
			case r == '"' || r == '\\':
				n += 2
			case r == '<' || r == '>' || r == '&' || r == ' ' || r == ' ':
				n += 6
			default:
				n += uint64(utf8.RuneLen(r))
			}
		}
		return take(n)
	case reflect.Slice, reflect.Array:
		if err := take(uint64(v.Len()) + 2); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := admitValue(v.Index(i), budget, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if err := take(uint64(v.Len())*2 + 2); err != nil {
			return err
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := admitValue(iter.Key(), budget, depth+1); err != nil {
				return err
			}
			if err := admitValue(iter.Value(), budget, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := take(2); err != nil {
			return err
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			fv := v.Field(i)
			if len(tag) > 1 && tag[1] == "omitempty" && fv.IsZero() {
				continue
			}
			if err := take(uint64(len(tag[0])) + 4); err != nil {
				return err
			}
			if err := admitValue(fv, budget, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return take(24)
	}
}

func decodeCanonical(raw []byte, target any, maximum uint64) error {
	if uint64(len(raw)) > maximum {
		return ErrLimit
	}
	if err := scan(raw); err != nil {
		return err
	}
	if err := wirejson.Decode(raw, target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	encoded, err := canonical(target, maximum)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, encoded) {
		return fmt.Errorf("%w: noncanonical record", ErrInvalid)
	}
	return nil
}

// CanonicalDocument owns and validates a v2 document, preserving JSON number
// spellings in non-rendering metadata. It is bounded to 128 MiB.
func CanonicalDocument(document documentv2.Document) ([]byte, error) {
	return freezeDocument(document, DefaultLimits().MaxDocumentBytes)
}

func freezeDocument(document documentv2.Document, maximum uint64) ([]byte, error) {
	raw, err := canonical(document, maximum)
	if err != nil {
		return nil, err
	}
	if diagnostics := documentv2.Validate(document); documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, diagnostics[0].Code)
	}
	return raw, nil
}

func decodeDocument(raw []byte, maximum uint64) (documentv2.Document, error) {
	var document documentv2.Document
	if err := decodeCanonical(raw, &document, maximum); err != nil {
		return documentv2.Document{}, err
	}
	if diagnostics := documentv2.Validate(document); documentv2.HasErrorDiagnostics(diagnostics) {
		return documentv2.Document{}, fmt.Errorf("%w: %s", ErrInvalid, diagnostics[0].Code)
	}
	return document, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func validHash(hash string) bool {
	if len(hash) != 71 || !strings.HasPrefix(hash, "sha256:") {
		return false
	}
	for _, r := range hash[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func objectName(kind, hash string) (string, error) {
	if !validHash(hash) {
		return "", ErrInvalid
	}
	switch kind {
	case "documents", "plans", "commits", "states":
	default:
		return "", ErrInvalid
	}
	return ".cutly/v2/" + kind + "/" + hash[7:] + ".json", nil
}
func identity(s string, maximum uint64) bool {
	return s != "" && strings.TrimSpace(s) == s && utf8.ValidString(s) && uint64(len(s)) <= maximum
}
