package audiograph

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// valueMeasure walks caller-owned typed graphs without creating a second graph. Collection
// elements and non-nil pointers count as structural nodes. The encoded byte count deliberately
// overestimates encoding/json so hostile strings/raw messages can be rejected before canonical
// encoding allocates a document- or manifest-sized buffer.
type valueMeasure struct {
	nodes        uint64
	encodedUpper uint64
	nodeLimit    uint64
	byteLimit    uint64
}

func preflightValueLimits(value any, nodeLimit, byteLimit uint64) (string, error) {
	if nodeLimit == 0 || byteLimit == 0 {
		return "/", fmt.Errorf("preflight limits must be positive")
	}
	measure := valueMeasure{nodeLimit: nodeLimit, byteLimit: byteLimit}
	if err := measure.addNodes(1); err != nil {
		return "/", err
	}
	if err := measure.walk(reflect.ValueOf(value)); err != nil {
		return "/", err
	}
	return "", nil
}

func (measure *valueMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return fmt.Errorf("structural nodes exceed limit %d", measure.nodeLimit)
	}
	measure.nodes += count
	return nil
}

func (measure *valueMeasure) addEncoded(count uint64) error {
	if count > math.MaxUint64-measure.encodedUpper || measure.encodedUpper+count > measure.byteLimit {
		return fmt.Errorf("conservative encoded bytes exceed limit %d", measure.byteLimit)
	}
	measure.encodedUpper += count
	return nil
}

func (measure *valueMeasure) addEscapedBytes(length int) error {
	if length < 0 || uint64(length) > (math.MaxUint64-2)/6 {
		return fmt.Errorf("string/raw byte size overflows")
	}
	return measure.addEncoded(uint64(length)*6 + 2)
}

func (measure *valueMeasure) walk(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return measure.addEncoded(4)
		}
		if err := measure.addNodes(1); err != nil {
			return err
		}
		return measure.walk(value.Elem())
	case reflect.Struct:
		if err := measure.addEncoded(2); err != nil {
			return err
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if err := measure.addEscapedBytes(len(name)); err != nil {
				return err
			}
			if err := measure.addEncoded(2); err != nil {
				return err
			}
			if err := measure.walk(value.Field(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return measure.addEncoded(4)
		}
		if err := measure.addNodes(uint64(value.Len())); err != nil {
			return err
		}
		if err := measure.addEncoded(2 + uint64(value.Len())); err != nil {
			return err
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := measure.walk(iterator.Key()); err != nil {
				return err
			}
			if err := measure.walk(iterator.Value()); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return measure.addEncoded(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return measure.addEscapedBytes(value.Len())
		}
		if err := measure.addNodes(uint64(value.Len())); err != nil {
			return err
		}
		if err := measure.addEncoded(2 + uint64(value.Len())); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if err := measure.walk(value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		return measure.addEscapedBytes(value.Len())
	case reflect.Bool:
		return measure.addEncoded(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return measure.addEncoded(24)
	default:
		return fmt.Errorf("unsupported field kind %s", value.Kind())
	}
}
