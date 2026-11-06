package referenceimage

import (
	"context"
	"errors"
	stdcolor "image/color"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/nativepngsource"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
)

func FuzzConfigGates(f *testing.F) {
	f.Add(int32(1), int32(1), uint64(4), "")
	f.Add(int32(4096), int32(4096), uint64(1<<30), "build-a")
	f.Add(int32(-1), int32(0), uint64(0), " forged ")
	f.Fuzz(func(t *testing.T, width, height int32, outputBytes uint64, fingerprint string) {
		config := DefaultConfig(int(width), int(height))
		config.OutputLimits.MaxOutputBytes = outputBytes
		config.BuildFingerprint = fingerprint
		normalized, rasterConfig, err := normalizeConfig(config)
		if err != nil {
			return
		}
		if normalized.Width <= 0 || normalized.Height <= 0 || rasterConfig.Source != (rastercompositor.SourceCapabilities{Image: true}) ||
			rasterConfig.SourceProfiles.Image != nativeImageProfiles() || rasterConfig.SourceProfiles.Video != (rastercompositor.AssetSourceProfiles{}) {
			t.Fatalf("unsafe normalized=%+v raster=%+v", normalized, rasterConfig)
		}
		if _, capabilityErr := rastercompositor.Capabilities(rasterConfig); capabilityErr != nil {
			return
		}
	})
}

func FuzzRenderTimeAndFrameFailClosed(f *testing.F) {
	encoded := strictPNG(f, 1, 1, stdcolor.NRGBA{R: 255, A: 255}, 0)
	program, _, err := Compile(context.Background(), singleImageDocument(contentHash(encoded)), []Binding{{AssetID: "asset", EncodedPNG: encoded}}, DefaultConfig(1, 1))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(int64(0), int64(1), int64(0))
	f.Add(int64(-1), int64(1), int64(-1))
	f.Add(int64(1), int64(0), int64(1))
	f.Add(int64(1<<62), int64(1), int64(1<<62))
	f.Fuzz(func(t *testing.T, value, rate, frameIndex int64) {
		atResult, atErr := program.RenderAt(context.Background(), "timeline", documentv2.RationalTime{Value: value, Rate: rate})
		if atErr != nil {
			assertRenderResultEmpty(t, atResult)
		} else if atResult.PNG.Artifact == nil || atResult.DocumentHash != program.DocumentHash() {
			t.Fatalf("incomplete time result=%+v", atResult)
		}
		clock := videoedit.FrameClock{Origin: rt(0, 1), FramesPerSecond: documentv2.RationalTime{Value: value, Rate: rate}}
		frameResult, frameErr := program.RenderFrame(context.Background(), "timeline", clock, frameIndex)
		if frameErr != nil {
			assertRenderResultEmpty(t, frameResult.Result)
			var typed *Error
			if !errors.As(frameErr, &typed) {
				t.Fatalf("unstable frame error=%v", frameErr)
			}
		} else if frameResult.Result.PNG.Artifact == nil || frameResult.CompositionFrameIndex != frameIndex {
			t.Fatalf("incomplete frame result=%+v", frameResult)
		}
	})
}

func assertRenderResultEmpty(t testing.TB, result Result) {
	t.Helper()
	if result.DocumentHash != "" || result.Raster.Surface != nil || result.Output.Frame != nil || result.PNG.Artifact != nil {
		t.Fatalf("partial result=%+v", result)
	}
}

func nativeImageProfiles() rastercompositor.AssetSourceProfiles {
	return nativepngsource.ImageSourceProfiles()
}
