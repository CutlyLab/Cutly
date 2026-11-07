package wavsource

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/wavdecode"
)

func normalizeConfig(value Config) (Config, error) {
	defaults := DefaultLimits()
	if value.Limits.MaxBindings == 0 {
		value.Limits.MaxBindings = defaults.MaxBindings
	}
	if value.Limits.MaxClipScopes == 0 {
		value.Limits.MaxClipScopes = defaults.MaxClipScopes
	}
	if value.Limits.MaxTracksPerTimeline == 0 {
		value.Limits.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
	}
	if value.Limits.MaxDocumentNodes == 0 {
		value.Limits.MaxDocumentNodes = defaults.MaxDocumentNodes
	}
	if value.Limits.MaxCanonicalDocumentBytes == 0 {
		value.Limits.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
	}
	if value.Limits.MaxTotalEncodedBytes == 0 {
		value.Limits.MaxTotalEncodedBytes = defaults.MaxTotalEncodedBytes
	}
	if value.Limits.MaxRetainedBytes == 0 {
		value.Limits.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.Limits.MaxExactBits == 0 {
		value.Limits.MaxExactBits = defaults.MaxExactBits
	}
	wavDefaults := defaults.WAV
	if value.Limits.WAV.MaxSampleRate == 0 {
		value.Limits.WAV.MaxSampleRate = wavDefaults.MaxSampleRate
	}
	if value.Limits.WAV.MaxFrames == 0 {
		value.Limits.WAV.MaxFrames = wavDefaults.MaxFrames
	}
	if value.Limits.WAV.MaxEncodedBytes == 0 {
		value.Limits.WAV.MaxEncodedBytes = wavDefaults.MaxEncodedBytes
	}
	if value.Limits.WAV.MaxWorkingBytes == 0 {
		value.Limits.WAV.MaxWorkingBytes = wavDefaults.MaxWorkingBytes
	}
	limits := value.Limits
	if limits.MaxBindings <= 0 || limits.MaxBindings > HardMaxBindings ||
		limits.MaxClipScopes <= 0 || limits.MaxClipScopes > HardMaxClipScopes ||
		limits.MaxTracksPerTimeline <= 0 || limits.MaxTracksPerTimeline > HardMaxTracksPerTimeline ||
		limits.MaxDocumentNodes <= 0 || limits.MaxDocumentNodes > HardMaxDocumentNodes ||
		limits.MaxCanonicalDocumentBytes == 0 || limits.MaxCanonicalDocumentBytes > HardMaxCanonicalDocumentBytes ||
		limits.MaxTotalEncodedBytes == 0 || limits.MaxTotalEncodedBytes > HardMaxTotalEncodedBytes ||
		limits.MaxRetainedBytes == 0 || limits.MaxRetainedBytes > HardMaxRetainedBytes ||
		limits.MaxExactBits <= 0 || limits.MaxExactBits > HardMaxExactBits {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits", ErrConfigInvalid)
	}
	// wavdecode owns the exact detailed limit validation; these hard-envelope checks reject bad
	// configuration before touching document or binding bytes.
	if limits.WAV.MaxSampleRate > wavdecode.HardMaxSampleRate || limits.WAV.MaxFrames > wavdecode.HardMaxFrames ||
		limits.WAV.MaxEncodedBytes > wavdecode.HardMaxEncodedBytes || limits.WAV.MaxWorkingBytes > wavdecode.HardMaxWorkingBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits/wav", ErrConfigInvalid)
	}
	if limits.WAV.MaxEncodedBytes != 0 && limits.WAV.MaxEncodedBytes < 44 {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits/wav/max_encoded_bytes", ErrConfigInvalid)
	}
	return value, nil
}

type documentMeasure struct {
	ctx          context.Context
	visits       uint64
	nodes        uint64
	encodedUpper uint64
	nodeLimit    uint64
	byteLimit    uint64
}

func preflightDocument(ctx context.Context, document documentv2.Document, limits Limits) error {
	if uint64(len(document.Timelines)) > uint64(limits.MaxDocumentNodes) {
		return sourceError(CodeResourceLimit, "document/timelines", ErrResourceLimit)
	}
	for timelineIndex, timeline := range document.Timelines {
		if timelineIndex&1023 == 0 {
			if err := contextFailure(ctx, "document/timelines"); err != nil {
				return err
			}
		}
		if len(timeline.Tracks) > limits.MaxTracksPerTimeline {
			return sourceError(CodeResourceLimit, fmt.Sprintf("document/timelines/%d/tracks", timelineIndex), ErrResourceLimit)
		}
	}
	measure := documentMeasure{ctx: ctx, nodeLimit: uint64(limits.MaxDocumentNodes), byteLimit: limits.MaxCanonicalDocumentBytes}
	if err := measure.addNodes(1); err != nil {
		return sourceError(CodeResourceLimit, "document", errorsJoinResource(err))
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		if contextErr := contextFailure(ctx, "document"); contextErr != nil {
			return contextErr
		}
		return sourceError(CodeResourceLimit, "document", errorsJoinResource(err))
	}
	return contextFailure(ctx, "document")
}

func errorsJoinResource(detail error) error {
	return fmt.Errorf("%w: %v", ErrResourceLimit, detail)
}

func (measure *documentMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return fmt.Errorf("document structural nodes exceed %d", measure.nodeLimit)
	}
	measure.nodes += count
	return nil
}

func (measure *documentMeasure) addEncoded(count uint64) error {
	if count > math.MaxUint64-measure.encodedUpper || measure.encodedUpper+count > measure.byteLimit {
		return fmt.Errorf("conservative document bytes exceed %d", measure.byteLimit)
	}
	measure.encodedUpper += count
	return nil
}

func (measure *documentMeasure) addEscaped(length int) error {
	if length < 0 || uint64(length) > (math.MaxUint64-2)/6 {
		return fmt.Errorf("document string size overflows")
	}
	return measure.addEncoded(uint64(length)*6 + 2)
}

func (measure *documentMeasure) walk(value reflect.Value) error {
	measure.visits++
	if measure.visits&1023 == 0 {
		if err := measure.ctx.Err(); err != nil {
			return err
		}
	}
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
			if err := measure.addEscaped(len(name)); err != nil {
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
		// Two braces, one colon per entry and at most one comma per entry.
		if uint64(value.Len()) > (math.MaxUint64-2)/2 {
			return fmt.Errorf("document map punctuation size overflows")
		}
		if err := measure.addEncoded(2 + 2*uint64(value.Len())); err != nil {
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
			return measure.addEscaped(value.Len())
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
		return measure.addEscaped(value.Len())
	case reflect.Bool:
		return measure.addEncoded(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return measure.addEncoded(24)
	default:
		return fmt.Errorf("unsupported document kind %s", value.Kind())
	}
}

func preflightBindings(bindings []Binding, limits Limits) error {
	if len(bindings) > limits.MaxBindings {
		return sourceError(CodeResourceLimit, "bindings", ErrResourceLimit)
	}
	var totalEncoded, retainedUpper uint64
	for index, binding := range bindings {
		length := uint64(len(binding.EncodedWAV))
		if length > math.MaxUint64-totalEncoded {
			return sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/encoded_bytes", index), ErrResourceLimit)
		}
		totalEncoded += length
		if totalEncoded > limits.MaxTotalEncodedBytes {
			return sourceError(CodeResourceLimit, "bindings/encoded_bytes", ErrResourceLimit)
		}
		// Every accepted strict WAV has exactly a 44-byte envelope. Bound retained PCM before any
		// decode; malformed shorter inputs proceed only to the bounded decoder error path.
		if length > 44 {
			dataBytes := length - 44
			if dataBytes > math.MaxUint64-retainedUpper {
				return sourceError(CodeResourceLimit, fmt.Sprintf("bindings/%d/retained_bytes", index), ErrResourceLimit)
			}
			retainedUpper += dataBytes
			if retainedUpper > limits.MaxRetainedBytes {
				return sourceError(CodeResourceLimit, "bindings/retained_bytes", ErrResourceLimit)
			}
		}
	}
	return nil
}
