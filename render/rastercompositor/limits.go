package rastercompositor

import (
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
)

// documentMeasure walks the typed Document without allocating a second document-sized graph.
// It deliberately treats every collection element and non-nil pointer as one node, and uses a
// conservative JSON upper bound for caller-controlled strings/raw bytes. Exact canonical length
// is checked again after canonicalization.
type documentMeasure struct {
	nodes        uint64
	encodedUpper uint64
	nodeLimit    uint64
	byteLimit    uint64
}

func preflightDocumentLimits(document documentv2.Document, limits Limits) (string, error) {
	for timelineIndex, timeline := range document.Timelines {
		if len(timeline.Tracks) > limits.MaxTracksPerTimeline {
			return fmt.Sprintf("/timelines/%d/tracks", timelineIndex),
				fmt.Errorf("timeline track count %d exceeds compile limit %d", len(timeline.Tracks), limits.MaxTracksPerTimeline)
		}
	}
	measure := documentMeasure{
		nodeLimit: uint64(limits.MaxDocumentNodes),
		byteLimit: limits.MaxCanonicalDocumentBytes,
	}
	if err := measure.addNodes(1); err != nil {
		return "/", err
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		return "/", err
	}
	return "", nil
}

func (measure *documentMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return fmt.Errorf("document structural nodes exceed limit %d", measure.nodeLimit)
	}
	measure.nodes += count
	return nil
}

func (measure *documentMeasure) addEncoded(count uint64) error {
	if count > math.MaxUint64-measure.encodedUpper || measure.encodedUpper+count > measure.byteLimit {
		return fmt.Errorf("conservative encoded document bytes exceed limit %d", measure.byteLimit)
	}
	measure.encodedUpper += count
	return nil
}

func (measure *documentMeasure) addEscapedBytes(length int) error {
	if length < 0 || uint64(length) > (math.MaxUint64-2)/6 {
		return fmt.Errorf("document string/raw byte size overflows")
	}
	// Six bytes per input byte safely covers JSON control/HTML escaping and invalid UTF-8; quotes
	// account for ordinary string values. Applying it to RawMessage is intentionally conservative.
	return measure.addEncoded(uint64(length)*6 + 2)
}

func (measure *documentMeasure) walk(value reflect.Value) error {
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
		if err := measure.addEncoded(2); err != nil { // enclosing braces
			return err
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			// Match encoding/json's closed typed-Document field visibility. Counting every exported
			// field even when tagged omitempty deliberately overestimates the encoded form.
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
			if err := measure.addEncoded(2); err != nil { // colon plus possible comma
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
		return fmt.Errorf("unsupported document field kind %s", value.Kind())
	}
}
