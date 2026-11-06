// Package wirejson implements the strict JSON boundary used by Cutly wire protocols.
//
// encoding/json intentionally accepts duplicate object members and case-insensitive struct field
// matches. Those conveniences are unsafe for fingerprinted protocols: two different byte-level
// messages can otherwise decode to the same Go value, and required zero-valued fields cannot be
// distinguished from fields that were omitted. Decode rejects those ambiguities before delegating
// the final type conversion to encoding/json.
package wirejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

var rawMessageType = reflect.TypeOf(json.RawMessage{})

// Validate rejects malformed JSON, multiple top-level values, and duplicate object members at
// every nesting depth. It is also suitable for JSON produced by Marshal when RawMessage fields may
// contain attacker-controlled object members.
func Validate(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := readValue(decoder, "$", nil); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("wirejson: multiple JSON values (next token %v)", token)
		}
		return fmt.Errorf("wirejson: trailing JSON: %w", err)
	}
	return nil
}

// Decode validates an exact struct-shaped wire value and then decodes it into target. Object keys
// mapped to structs must match json tag names byte-for-byte. Fields without omitempty are required,
// including zero-valued booleans and integers. Maps and RawMessage keep their open-key semantics.
func Decode(raw []byte, target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("wirejson: target must be a non-nil pointer")
	}
	if err := Validate(raw); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var shape any
	if err := decoder.Decode(&shape); err != nil {
		return fmt.Errorf("wirejson: decode shape: %w", err)
	}
	if err := validateShape(shape, value.Type().Elem(), "$"); err != nil {
		return err
	}

	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	typed.UseNumber()
	if err := typed.Decode(target); err != nil {
		return fmt.Errorf("wirejson: decode typed value: %w", err)
	}
	var trailing any
	if err := typed.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("wirejson: multiple JSON values")
		}
		return fmt.Errorf("wirejson: trailing typed JSON: %w", err)
	}
	return nil
}

func readValue(decoder *json.Decoder, path string, first json.Token) error {
	token := first
	var err error
	if token == nil {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("wirejson: %s: %w", path, err)
		}
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return fmt.Errorf("wirejson: %s: object key: %w", path, keyErr)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("wirejson: %s: object key is not a string", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("wirejson: %s: duplicate object member %q", path, key)
			}
			seen[key] = struct{}{}
			if err := readValue(decoder, childPath(path, key), nil); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return fmt.Errorf("wirejson: %s: unterminated object: %v", path, endErr)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := readValue(decoder, fmt.Sprintf("%s[%d]", path, index), nil); err != nil {
				return err
			}
			index++
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return fmt.Errorf("wirejson: %s: unterminated array: %v", path, endErr)
		}
	default:
		return fmt.Errorf("wirejson: %s: unexpected delimiter %q", path, delimiter)
	}
	return nil
}

type fieldSpec struct {
	typeValue reflect.Type
	required  bool
	nullable  bool
}

func validateShape(value any, expected reflect.Type, path string) error {
	for expected.Kind() == reflect.Pointer {
		if value == nil {
			return fmt.Errorf("wirejson: %s: null is not valid for %s", path, expected)
		}
		expected = expected.Elem()
	}
	if expected == rawMessageType || expected.Kind() == reflect.Interface {
		return nil
	}
	if value == nil {
		return fmt.Errorf("wirejson: %s: null is not valid for %s", path, expected)
	}

	switch expected.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("wirejson: %s: expected object for %s", path, expected)
		}
		fields := structFields(expected)
		objectNames := make([]string, 0, len(object))
		for name := range object {
			objectNames = append(objectNames, name)
		}
		sort.Strings(objectNames)
		for _, name := range objectNames {
			if _, exists := fields[name]; !exists {
				return fmt.Errorf("wirejson: %s: unknown object member %q", path, name)
			}
		}
		fieldNames := make([]string, 0, len(fields))
		for name := range fields {
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)
		for _, name := range fieldNames {
			field := fields[name]
			child, present := object[name]
			if !present {
				if field.required {
					return fmt.Errorf("wirejson: %s: required object member %q is missing", path, name)
				}
				continue
			}
			if child == nil && field.nullable {
				continue
			}
			if err := validateShape(child, field.typeValue, childPath(path, name)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("wirejson: %s: expected array for %s", path, expected)
		}
		for index, child := range array {
			if err := validateShape(child, expected.Elem(), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok || expected.Key().Kind() != reflect.String {
			return fmt.Errorf("wirejson: %s: expected string-keyed object for %s", path, expected)
		}
		names := make([]string, 0, len(object))
		for name := range object {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child := object[name]
			if err := validateShape(child, expected.Elem(), childPath(path, name)); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("wirejson: %s: expected string", path)
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("wirejson: %s: expected boolean", path)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("wirejson: %s: expected number", path)
		}
	default:
		return fmt.Errorf("wirejson: %s: unsupported target type %s", path, expected)
	}
	return nil
}

func structFields(value reflect.Type) map[string]fieldSpec {
	fields := make(map[string]fieldSpec)
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		required := true
		for _, option := range parts[1:] {
			if option == "omitempty" || option == "omitzero" {
				required = false
			}
		}
		nullable := false
		for _, option := range strings.Split(field.Tag.Get("wirejson"), ",") {
			if option == "nullable" {
				nullable = true
			}
		}
		fields[name] = fieldSpec{typeValue: field.Type, required: required, nullable: nullable}
	}
	return fields
}

func childPath(parent, name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	name = strings.ReplaceAll(name, "/", "~1")
	return parent + "/" + name
}
