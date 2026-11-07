package documentv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

var rawMessageType = reflect.TypeOf(json.RawMessage{})

var errInvalidJSONUnicode = errors.New("documentv2: JSON contains invalid Unicode encoding")

func validateJSONEncoding(raw []byte) error {
	if !utf8.Valid(raw) || hasUnpairedJSONSurrogate(raw) {
		return errInvalidJSONUnicode
	}
	return nil
}

// hasUnpairedJSONSurrogate scans JSON string escapes before encoding/json replaces lone UTF-16
// surrogates with U+FFFD. Syntax validation remains wirejson's responsibility.
func hasUnpairedJSONSurrogate(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		for index++; index < len(raw); index++ {
			switch raw[index] {
			case '"':
				goto nextToken
			case '\\':
				if index+1 >= len(raw) {
					return false
				}
				index++
				if raw[index] != 'u' || index+4 >= len(raw) {
					continue
				}
				value, ok := fourHex(raw[index+1 : index+5])
				if !ok {
					continue
				}
				index += 4
				if value >= 0xdc00 && value <= 0xdfff {
					return true
				}
				if value < 0xd800 || value > 0xdbff {
					continue
				}
				if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
					return true
				}
				low, lowOK := fourHex(raw[index+3 : index+7])
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return true
				}
				index += 6
			}
		}
	nextToken:
	}
	return false
}

func fourHex(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var result uint16
	for _, character := range value {
		result <<= 4
		switch {
		case character >= '0' && character <= '9':
			result |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			result |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			result |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

// invalidUTF8Paths closes encoding/json's lossy invalid-UTF-8 replacement behavior. Canonical
// hashes must never collapse two distinct byte strings to the same U+FFFD-containing document.
func invalidUTF8Paths(value any) []string {
	paths := make([]string, 0)
	walkUTF8(reflect.ValueOf(value), "", &paths)
	return paths
}

func walkUTF8(value reflect.Value, path string, paths *[]string) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Type() == rawMessageType {
		if validateJSONEncoding(value.Bytes()) != nil {
			*paths = append(*paths, displayPointer(path))
		}
		return
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			*paths = append(*paths, displayPointer(path))
		}
	case reflect.Struct:
		typeValue := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeValue.Field(index)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			walkUTF8(value.Field(index), path+"/"+escapeJSONPointer(name), paths)
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return
		}
		keys := make([]string, 0, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			keys = append(keys, iterator.Key().String())
		}
		sort.Strings(keys)
		for _, key := range keys {
			segment := safeJSONPointerSegment(key)
			childPath := path + "/" + segment
			if !utf8.ValidString(key) {
				*paths = append(*paths, displayPointer(childPath))
			}
			walkUTF8(value.MapIndex(reflect.ValueOf(key).Convert(value.Type().Key())), childPath, paths)
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			walkUTF8(value.Index(index), fmt.Sprintf("%s/%d", path, index), paths)
		}
	}
}

func safeJSONPointerSegment(value string) string {
	if !utf8.ValidString(value) {
		return "<invalid-utf8-key>"
	}
	return escapeJSONPointer(value)
}

func displayPointer(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
