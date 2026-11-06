package visualcompositor

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/cpuraster"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

const targetFramePeakMultiplier uint64 = 6

type Limits struct {
	Target                    cpucompositor.Limits `json:"target"`
	Raster                    cpuraster.Limits     `json:"raster"`
	MaxLayers                 int                  `json:"max_layers"`
	MaxSourceRequests         int                  `json:"max_source_requests"`
	MaxDocumentNodes          int                  `json:"max_document_nodes"`
	MaxTracksPerTimeline      int                  `json:"max_tracks_per_timeline"`
	MaxSourceBytes            uint64               `json:"max_source_bytes"`
	MaxTargetWorkingBytes     uint64               `json:"max_target_working_bytes"`
	MaxCanonicalDocumentBytes uint64               `json:"max_canonical_document_bytes"`
}

func DefaultLimits() Limits {
	base := rastercompositor.DefaultLimits()
	return Limits{
		Target: base.Target, Raster: base.Raster, MaxLayers: base.MaxLayers,
		MaxSourceRequests: base.MaxSourceRequests, MaxDocumentNodes: base.MaxDocumentNodes,
		MaxTracksPerTimeline: base.MaxTracksPerTimeline, MaxSourceBytes: base.MaxSourceBytes,
		MaxTargetWorkingBytes:     base.MaxTargetWorkingBytes,
		MaxCanonicalDocumentBytes: base.MaxCanonicalDocumentBytes,
	}
}

type SourceCapabilities struct {
	Video             bool `json:"video"`
	Image             bool `json:"image"`
	VectorExpression  bool `json:"vector_expression"`
	LinearVideoRetime bool `json:"linear_video_retime"`
}

type Config struct {
	Width            int                             `json:"width"`
	Height           int                             `json:"height"`
	Limits           Limits                          `json:"limits"`
	Source           SourceCapabilities              `json:"source"`
	NativeProfiles   rastercompositor.SourceProfiles `json:"native_profiles"`
	BuildFingerprint string                          `json:"build_fingerprint"`
}

func DefaultConfig(width, height int) Config {
	return Config{
		Width: width, Height: height, Limits: DefaultLimits(),
		Source:         SourceCapabilities{Video: true, Image: true, VectorExpression: true},
		NativeProfiles: rastercompositor.DefaultSourceProfiles(),
	}
}

func normalizeConfig(config Config) (Config, error) {
	result := config
	if result.Limits == (Limits{}) {
		result.Limits = DefaultLimits()
	}
	if result.Limits.Target == (cpucompositor.Limits{}) {
		result.Limits.Target = cpucompositor.DefaultLimits()
	}
	if result.Limits.Raster == (cpuraster.Limits{}) {
		result.Limits.Raster = cpuraster.DefaultLimits()
	}
	if result.Width <= 0 || result.Height <= 0 {
		return Config{}, compositorError(CodeInvalidConfig, "config/dimensions", ErrInvalidConfig)
	}
	target := result.Limits.Target
	if target.MaxWidth <= 0 || target.MaxHeight <= 0 || target.MaxBytes < cpucompositor.BytesPerPixel || target.MaxBytes > cpucompositor.HardMaxSurfaceBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config/limits/target", ErrInvalidConfig)
	}
	raster := result.Limits.Raster
	if raster.MaxWidth <= 0 || raster.MaxHeight <= 0 || raster.MaxOutputPixels == 0 || raster.MaxOutputPixels > cpuraster.HardMaxPixels ||
		raster.MaxWorkingBytes < cpucompositor.BytesPerPixel*2 || raster.MaxWorkingBytes > cpuraster.HardMaxWorking ||
		raster.MaxExactBits <= 0 || raster.MaxExactBits > cpuraster.HardMaxExactBits {
		return Config{}, compositorError(CodeInvalidConfig, "config/limits/raster", ErrInvalidConfig)
	}
	limits := result.Limits
	if limits.MaxLayers <= 0 || limits.MaxLayers > rastercompositor.HardMaxLayers ||
		limits.MaxSourceRequests <= 0 || limits.MaxSourceRequests > rastercompositor.HardMaxSourceRequests ||
		limits.MaxDocumentNodes <= 0 || limits.MaxDocumentNodes > rastercompositor.HardMaxDocumentNodes ||
		limits.MaxTracksPerTimeline <= 0 || limits.MaxTracksPerTimeline > rastercompositor.HardMaxTracksPerTimeline ||
		limits.MaxSourceBytes < cpucompositor.BytesPerPixel || limits.MaxSourceBytes > rastercompositor.HardMaxSourceBytes ||
		limits.MaxTargetWorkingBytes < cpucompositor.BytesPerPixel*targetFramePeakMultiplier || limits.MaxTargetWorkingBytes > rastercompositor.HardMaxTargetWorking ||
		limits.MaxCanonicalDocumentBytes == 0 || limits.MaxCanonicalDocumentBytes > rastercompositor.HardMaxDocumentBytes {
		return Config{}, compositorError(CodeInvalidConfig, "config/limits", ErrInvalidConfig)
	}
	if result.Width > target.MaxWidth || result.Height > target.MaxHeight || result.Width > raster.MaxWidth || result.Height > raster.MaxHeight {
		return Config{}, compositorError(CodeInvalidConfig, "config/dimensions", ErrInvalidConfig)
	}
	width, height := uint64(result.Width), uint64(result.Height)
	if height != 0 && width > math.MaxUint64/height {
		return Config{}, compositorError(CodeResourceLimit, "config/pixel_count", ErrResourceLimit)
	}
	pixels := width * height
	if pixels > raster.MaxOutputPixels || pixels > math.MaxUint64/cpucompositor.BytesPerPixel {
		return Config{}, compositorError(CodeResourceLimit, "config/pixel_count", ErrResourceLimit)
	}
	frameBytes := pixels * cpucompositor.BytesPerPixel
	if frameBytes > target.MaxBytes || frameBytes > math.MaxUint64/targetFramePeakMultiplier ||
		frameBytes*targetFramePeakMultiplier > limits.MaxTargetWorkingBytes ||
		frameBytes > math.MaxUint64/2 || frameBytes*2 > raster.MaxWorkingBytes {
		return Config{}, compositorError(CodeResourceLimit, "config/working_bytes", ErrResourceLimit)
	}
	if !canonicalBuildFingerprint(result.BuildFingerprint) {
		return Config{}, compositorError(CodeInvalidConfig, "config/build_fingerprint", ErrInvalidConfig)
	}
	if result.NativeProfiles == (rastercompositor.SourceProfiles{}) {
		result.NativeProfiles = rastercompositor.DefaultSourceProfiles()
	}
	if result.Source.Video && !validNativeProfiles(result.NativeProfiles.Video) {
		return Config{}, compositorError(CodeInvalidConfig, "config/native_profiles/video", ErrInvalidConfig)
	}
	if result.Source.Image && !validNativeProfiles(result.NativeProfiles.Image) {
		return Config{}, compositorError(CodeInvalidConfig, "config/native_profiles/image", ErrInvalidConfig)
	}
	if result.Source.LinearVideoRetime && !result.Source.Video {
		return Config{}, compositorError(CodeInvalidConfig, "config/source/linear_video_retime", ErrInvalidConfig)
	}
	return result, nil
}

func canonicalBuildFingerprint(value string) bool {
	if len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validNativeProfiles(value rastercompositor.AssetSourceProfiles) bool {
	return canonicalProfileToken(value.DecodeProfile) && canonicalProfileToken(value.SelectionProfile) &&
		canonicalProfileToken(value.ColorProfile) && canonicalProfileToken(value.NormalizationProfile)
}

type documentMeasure struct {
	ctx          context.Context
	visits       uint64
	nodes        uint64
	encodedUpper uint64
	nodeLimit    uint64
	byteLimit    uint64
}

func preflightDocument(ctx context.Context, document documentv2.Document, limits Limits) (string, error) {
	for index, timeline := range document.Timelines {
		if index&1023 == 0 {
			if err := contextError(ctx, "document/timelines"); err != nil {
				return "", err
			}
		}
		if len(timeline.Tracks) > limits.MaxTracksPerTimeline {
			return fmt.Sprintf("document/timelines/%d/tracks", index), ErrResourceLimit
		}
	}
	measure := documentMeasure{ctx: ctx, nodeLimit: uint64(limits.MaxDocumentNodes), byteLimit: limits.MaxCanonicalDocumentBytes}
	if err := measure.addNodes(1); err != nil {
		return "document", err
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		if canceled := contextError(ctx, "document"); canceled != nil {
			return "", canceled
		}
		return "document", err
	}
	return "", contextError(ctx, "document")
}

func (measure *documentMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return ErrResourceLimit
	}
	measure.nodes += count
	return nil
}

func (measure *documentMeasure) addEncoded(count uint64) error {
	if count > math.MaxUint64-measure.encodedUpper || measure.encodedUpper+count > measure.byteLimit {
		return ErrResourceLimit
	}
	measure.encodedUpper += count
	return nil
}

func (measure *documentMeasure) addEscaped(length int) error {
	if length < 0 || uint64(length) > (math.MaxUint64-2)/6 {
		return ErrResourceLimit
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
			return ErrResourceLimit
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
		length := uint64(value.Len())
		if err := measure.addNodes(length); err != nil {
			return err
		}
		if err := measure.addEncoded(2 + length); err != nil {
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
