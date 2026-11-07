package expressionlayer

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const (
	DefaultMaxClipScopes               = 65_536
	HardMaxClipScopes                  = 1 << 20
	DefaultMaxExpressionAssets         = 256
	HardMaxExpressionAssets            = 4096
	DefaultMaxDocumentNodes            = 262_144
	HardMaxDocumentNodes               = 4 << 20
	DefaultMaxTracksPerTimeline        = 4096
	HardMaxTracksPerTimeline           = 65_536
	DefaultMaxTotalPaths        uint64 = 65_536
	HardMaxTotalPaths           uint64 = 4 << 20
	DefaultMaxTotalCommands     uint64 = 1 << 20
	HardMaxTotalCommands        uint64 = 16 << 20
	DefaultMaxTotalEdges        uint64 = 1 << 20
	HardMaxTotalEdges           uint64 = 16 << 20
	DefaultMaxRetainedBytes     uint64 = 512 << 20
	HardMaxRetainedBytes        uint64 = 2 << 30
	DefaultMaxDocumentBytes     uint64 = 128 << 20
	HardMaxDocumentBytes        uint64 = 1 << 30
)

type Limits struct {
	MaxClipScopes             int                 `json:"max_clip_scopes"`
	MaxExpressionAssets       int                 `json:"max_expression_assets"`
	MaxDocumentNodes          int                 `json:"max_document_nodes"`
	MaxTracksPerTimeline      int                 `json:"max_tracks_per_timeline"`
	MaxTotalPaths             uint64              `json:"max_total_paths"`
	MaxTotalCommands          uint64              `json:"max_total_commands"`
	MaxTotalEdges             uint64              `json:"max_total_edges"`
	MaxRetainedBytes          uint64              `json:"max_retained_bytes"`
	MaxCanonicalDocumentBytes uint64              `json:"max_canonical_document_bytes"`
	Vector                    vectorraster.Limits `json:"vector"`
}

type Config struct {
	Limits Limits `json:"limits"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxClipScopes: DefaultMaxClipScopes, MaxExpressionAssets: DefaultMaxExpressionAssets,
		MaxDocumentNodes: DefaultMaxDocumentNodes, MaxTracksPerTimeline: DefaultMaxTracksPerTimeline,
		MaxTotalPaths: DefaultMaxTotalPaths, MaxTotalCommands: DefaultMaxTotalCommands,
		MaxTotalEdges: DefaultMaxTotalEdges, MaxRetainedBytes: DefaultMaxRetainedBytes,
		MaxCanonicalDocumentBytes: DefaultMaxDocumentBytes, Vector: vectorraster.DefaultLimits(),
	}
}

func DefaultConfig() Config { return Config{Limits: DefaultLimits()} }

func normalizeConfig(value Config) (Config, error) {
	result := value
	defaults := DefaultLimits()
	if result.Limits == (Limits{}) {
		result.Limits = defaults
	} else {
		if result.Limits.MaxClipScopes == 0 {
			result.Limits.MaxClipScopes = defaults.MaxClipScopes
		}
		if result.Limits.MaxExpressionAssets == 0 {
			result.Limits.MaxExpressionAssets = defaults.MaxExpressionAssets
		}
		if result.Limits.MaxDocumentNodes == 0 {
			result.Limits.MaxDocumentNodes = defaults.MaxDocumentNodes
		}
		if result.Limits.MaxTracksPerTimeline == 0 {
			result.Limits.MaxTracksPerTimeline = defaults.MaxTracksPerTimeline
		}
		if result.Limits.MaxTotalPaths == 0 {
			result.Limits.MaxTotalPaths = defaults.MaxTotalPaths
		}
		if result.Limits.MaxTotalCommands == 0 {
			result.Limits.MaxTotalCommands = defaults.MaxTotalCommands
		}
		if result.Limits.MaxTotalEdges == 0 {
			result.Limits.MaxTotalEdges = defaults.MaxTotalEdges
		}
		if result.Limits.MaxRetainedBytes == 0 {
			result.Limits.MaxRetainedBytes = defaults.MaxRetainedBytes
		}
		if result.Limits.MaxCanonicalDocumentBytes == 0 {
			result.Limits.MaxCanonicalDocumentBytes = defaults.MaxCanonicalDocumentBytes
		}
		if result.Limits.Vector == (vectorraster.Limits{}) {
			result.Limits.Vector = defaults.Vector
		}
	}
	limits := result.Limits
	if limits.MaxClipScopes <= 0 || limits.MaxClipScopes > HardMaxClipScopes ||
		limits.MaxExpressionAssets <= 0 || limits.MaxExpressionAssets > HardMaxExpressionAssets ||
		limits.MaxDocumentNodes <= 0 || limits.MaxDocumentNodes > HardMaxDocumentNodes ||
		limits.MaxTracksPerTimeline <= 0 || limits.MaxTracksPerTimeline > HardMaxTracksPerTimeline ||
		limits.MaxTotalPaths == 0 || limits.MaxTotalPaths > HardMaxTotalPaths ||
		limits.MaxTotalCommands == 0 || limits.MaxTotalCommands > HardMaxTotalCommands ||
		limits.MaxTotalEdges == 0 || limits.MaxTotalEdges > HardMaxTotalEdges ||
		limits.MaxRetainedBytes < cpucompositor.BytesPerPixel || limits.MaxRetainedBytes > HardMaxRetainedBytes ||
		limits.MaxCanonicalDocumentBytes == 0 || limits.MaxCanonicalDocumentBytes > HardMaxDocumentBytes {
		return Config{}, providerError(CodeConfigInvalid, "config/limits", ErrConfigInvalid)
	}
	v := limits.Vector
	if err := vectorraster.ValidateLimits(v); err != nil || v.MaxRetainedBytes > limits.MaxRetainedBytes {
		return Config{}, providerError(CodeConfigInvalid, "config/limits/vector", ErrConfigInvalid)
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
	measure := documentMeasure{ctx: ctx, nodeLimit: uint64(limits.MaxDocumentNodes), byteLimit: limits.MaxCanonicalDocumentBytes}
	if err := measure.addNodes(1); err != nil {
		return providerError(CodeResourceLimit, "document", errorsJoinLimit(err))
	}
	if uint64(len(document.Timelines)) > uint64(limits.MaxDocumentNodes) {
		return providerError(CodeResourceLimit, "document/timelines", ErrResourceLimit)
	}
	for timelineIndex, timeline := range document.Timelines {
		if timelineIndex&1023 == 0 {
			if err := contextFailure(ctx, "document/timelines"); err != nil {
				return err
			}
		}
		if len(timeline.Tracks) > limits.MaxTracksPerTimeline {
			return providerError(CodeResourceLimit, fmt.Sprintf("document/timelines/%d/tracks", timelineIndex), ErrResourceLimit)
		}
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		if contextErr := contextFailure(ctx, "document"); contextErr != nil {
			return contextErr
		}
		return providerError(CodeResourceLimit, "document", errorsJoinLimit(err))
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

func checkedAdd(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}
