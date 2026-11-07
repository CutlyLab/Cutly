package nativepngsource

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

func normalizeConfig(value Config) (Config, error) {
	result := value
	defaults := DefaultLimits()
	if result.Limits == (Limits{}) {
		result.Limits = defaults
	} else {
		if result.Limits.MaxBindings == 0 {
			result.Limits.MaxBindings = defaults.MaxBindings
		}
		if result.Limits.MaxClipScopes == 0 {
			result.Limits.MaxClipScopes = defaults.MaxClipScopes
		}
		if result.Limits.MaxDocumentNodes == 0 {
			result.Limits.MaxDocumentNodes = defaults.MaxDocumentNodes
		}
		if result.Limits.MaxTracksPerTimeline == 0 {
			result.Limits.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
		}
		if result.Limits.MaxRetainedBytes == 0 {
			result.Limits.MaxRetainedBytes = defaults.MaxRetainedBytes
		}
		if result.Limits.MaxTotalEncodedBytes == 0 {
			result.Limits.MaxTotalEncodedBytes = defaults.MaxTotalEncodedBytes
		}
		if result.Limits.MaxCanonicalDocumentBytes == 0 {
			result.Limits.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
		}
		if result.Limits.PNG == (pngdecode.Limits{}) {
			result.Limits.PNG = defaults.PNG
		}
	}
	limits := result.Limits
	if limits.MaxBindings <= 0 || limits.MaxBindings > HardMaxBindings ||
		limits.MaxClipScopes <= 0 || limits.MaxClipScopes > HardMaxClipScopes ||
		limits.MaxDocumentNodes <= 0 || limits.MaxDocumentNodes > HardMaxDocumentNodes ||
		limits.MaxTracksPerTimeline <= 0 || limits.MaxTracksPerTimeline > HardMaxTracksPerTimeline ||
		limits.MaxRetainedBytes < cpucompositor.BytesPerPixel || limits.MaxRetainedBytes > HardMaxRetainedBytes ||
		limits.MaxTotalEncodedBytes < 8 || limits.MaxTotalEncodedBytes > HardMaxTotalEncoded ||
		limits.MaxCanonicalDocumentBytes == 0 || limits.MaxCanonicalDocumentBytes > HardMaxDocumentBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits", ErrConfigInvalid)
	}
	png := limits.PNG
	if png.MaxWidth <= 0 || png.MaxHeight <= 0 || png.MaxEncodedBytes < 8 ||
		png.MaxEncodedBytes > pngdecode.HardMaxEncodedBytes || png.MaxWorkingBytes < 30 ||
		png.MaxWorkingBytes > pngdecode.HardMaxWorkingBytes {
		return Config{}, sourceError(CodeConfigInvalid, "config/limits/png", ErrConfigInvalid)
	}
	return result, nil
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
	measure := documentMeasure{
		ctx: ctx, nodeLimit: uint64(limits.MaxDocumentNodes), byteLimit: limits.MaxCanonicalDocumentBytes,
	}
	if err := measure.addNodes(1); err != nil {
		return sourceError(CodeResourceLimit, "document", errorsJoinLimit(err))
	}
	// Bound the cheap policy scan before iterating caller-owned timelines. The complete graph walk
	// below still charges the root and every collection against the exact configured node budget.
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
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		if contextErr := contextFailure(ctx, "document"); contextErr != nil {
			return contextErr
		}
		return sourceError(CodeResourceLimit, "document", errorsJoinLimit(err))
	}
	return contextFailure(ctx, "document")
}

func errorsJoinLimit(err error) error { return fmt.Errorf("%w: %v", ErrResourceLimit, err) }

func (measure *documentMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return fmt.Errorf("document nodes exceed %d", measure.nodeLimit)
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
		return fmt.Errorf("document encoded size overflows")
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
		length := uint64(value.Len())
		if err := measure.addNodes(length); err != nil {
			return err
		}
		// Braces plus one colon and at most one comma for every member. Keep this
		// independently conservative instead of relying on escaped-key slack.
		if length > (math.MaxUint64-2)/2 {
			return fmt.Errorf("document encoded size overflows")
		}
		if err := measure.addEncoded(2 + length*2); err != nil {
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
