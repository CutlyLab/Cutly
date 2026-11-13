// Package jsonadmission provides conservative, allocation-free admission for
// Go values before encoding/json is allowed to allocate their complete wire
// representation.
package jsonadmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var ErrLimit = errors.New("jsonadmission: resource limit")

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// Check rejects values whose encoding/json representation could exceed
// maxBytes or whose traversed value count exceeds maxValues. The estimate is a
// conservative upper bound: strings reserve six bytes per input byte and
// omitempty fields are counted even when encoding/json would omit them.
func Check(ctx context.Context, value any, maxBytes, maxValues uint64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBytes == 0 || maxValues == 0 {
		return fmt.Errorf("%w: limits must be positive", ErrLimit)
	}
	measure := measurer{ctx: ctx, maxBytes: maxBytes, maxValues: maxValues}
	if err := measure.value(reflect.ValueOf(value), 0); err != nil {
		return err
	}
	return ctx.Err()
}

type measurer struct {
	ctx       context.Context
	maxBytes  uint64
	maxValues uint64
	bytes     uint64
	values    uint64
}

func (measure *measurer) add(count uint64) error {
	if count > measure.maxBytes-measure.bytes {
		return fmt.Errorf("%w: encoded bytes exceed %d", ErrLimit, measure.maxBytes)
	}
	measure.bytes += count
	return nil
}

func (measure *measurer) visit() error {
	if measure.values >= measure.maxValues {
		return fmt.Errorf("%w: value count exceeds %d", ErrLimit, measure.maxValues)
	}
	measure.values++
	if measure.values == 1 || measure.values&255 == 0 {
		return measure.ctx.Err()
	}
	return nil
}

func (measure *measurer) string(value string) error {
	length := uint64(len(value))
	if measure.maxBytes < 2 || length > (measure.maxBytes-2)/6 {
		return fmt.Errorf("%w: encoded string exceeds %d", ErrLimit, measure.maxBytes)
	}
	return measure.add(2 + length*6)
}

func (measure *measurer) value(value reflect.Value, depth uint64) error {
	if err := measure.visit(); err != nil {
		return err
	}
	if depth > 256 {
		return fmt.Errorf("%w: nesting exceeds 256", ErrLimit)
	}
	if !value.IsValid() {
		return measure.add(4)
	}
	if value.Type() == rawMessageType {
		if value.IsNil() {
			return measure.add(4)
		}
		return measure.rawMessage(value.Bytes())
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return measure.add(4)
		}
		return measure.value(value.Elem(), depth+1)
	case reflect.Bool:
		return measure.add(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return measure.add(20)
	case reflect.Float32, reflect.Float64:
		return measure.add(32)
	case reflect.String:
		return measure.string(value.String())
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return measure.add(4)
		}
		if err := measure.add(2 + uint64(value.Len())); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if err := measure.value(value.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := measure.add(2 + uint64(value.NumField())); err != nil {
			return err
		}
		typeOfValue := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeOfValue.Field(index)
			if field.PkgPath != "" {
				continue
			}
			name := field.Name
			if tag, ok := field.Tag.Lookup("json"); ok {
				name = strings.Split(tag, ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
			}
			if err := measure.string(name); err != nil {
				return err
			}
			if err := measure.add(1); err != nil {
				return err
			}
			if err := measure.value(value.Field(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return measure.add(4)
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("jsonadmission: unsupported map key %s", value.Type().Key())
		}
		if err := measure.add(2 + uint64(value.Len())); err != nil {
			return err
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := measure.string(iterator.Key().String()); err != nil {
				return err
			}
			if err := measure.add(1); err != nil {
				return err
			}
			if err := measure.value(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("jsonadmission: unsupported kind %s", value.Kind())
	}
}

func (measure *measurer) rawMessage(raw []byte) error {
	length := uint64(len(raw))
	// encoding/json compacts RawMessage and HTML-escapes <, >, &, U+2028 and
	// U+2029. Six output bytes per input byte is a conservative upper bound.
	if length > measure.maxBytes/6 {
		return fmt.Errorf("%w: encoded raw JSON exceeds %d", ErrLimit, measure.maxBytes)
	}
	if err := measure.add(length * 6); err != nil {
		return err
	}
	// Count every container, string (including object keys) and primitive start.
	// Counting keys deliberately overestimates canonicaljson's value count. The
	// byte scan allocates nothing and observes cancellation even inside one huge
	// JSON string, before encoding/json may copy or compact the RawMessage.
	inString := false
	escaped := false
	inPrimitive := false
	depth := 0
	for index, current := range raw {
		if index&4095 == 0 {
			if err := measure.ctx.Err(); err != nil {
				return err
			}
		}
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if inPrimitive {
			switch current {
			case ' ', '\t', '\r', '\n', ',', '}', ']':
				inPrimitive = false
			default:
				continue
			}
		}
		switch current {
		case '"':
			if err := measure.visit(); err != nil {
				return err
			}
			inString = true
		case '{', '[':
			depth++
			if depth > 256 {
				return fmt.Errorf("%w: raw JSON nesting exceeds 256", ErrLimit)
			}
			if err := measure.visit(); err != nil {
				return err
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case 't', 'f', 'n', '-':
			if err := measure.visit(); err != nil {
				return err
			}
			inPrimitive = true
		default:
			if current >= '0' && current <= '9' {
				if err := measure.visit(); err != nil {
					return err
				}
				inPrimitive = true
			}
		}
	}
	if err := wirejson.Validate(raw); err != nil {
		return fmt.Errorf("jsonadmission: raw JSON: %w", err)
	}
	return nil
}
