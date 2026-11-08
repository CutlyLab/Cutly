package reconstructv2

import (
	"context"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

func TestBundleZeroHandleAndContextFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, bundle := range []*Bundle{nil, {}} {
		if bundle.Valid() || bundle.Key().DocumentHash != "" {
			t.Fatal("zero bundle valid")
		}
		for _, c := range []context.Context{nil, ctx, context.Background()} {
			bridge, err := bundle.Bridge(c)
			if err == nil || !reflect.DeepEqual(bridge, Bridge{}) {
				t.Fatal("zero bridge escaped")
			}
			frame, err := bundle.RenderFrame(c, 0)
			if err == nil || !reflect.DeepEqual(frame, referencevisual.FrameResult{}) {
				t.Fatal("zero frame escaped")
			}
			surface, err := bundle.RenderSurfaceFrame(c, 0)
			if err == nil || !reflect.DeepEqual(surface, referencevisual.SurfaceFrameResult{}) {
				t.Fatal("zero surface escaped")
			}
			pcm, err := bundle.RenderPCM(c, 0, 1)
			if err == nil || !reflect.DeepEqual(pcm, pcm16le.Result{}) {
				t.Fatal("zero pcm escaped")
			}
		}
	}
}

func TestBundleTickGuardsBeforeNativePrograms(t *testing.T) {
	// Private package test only: non-nil but unusable native objects must never
	// be reached for rejected tick ranges. No external caller can build this state.
	bundle := &Bundle{bridgeJSON: "receipt", visual: &referencevisual.Program{}, audio: &audiograph.DomainProgram{}, wav: &wavsource.Provider{}, visualEnabled: true, audioEnabled: true, frames: videoedit.TickRange{First: 24, Count: 48}, samples: videoedit.TickRange{First: 48000, Count: 96000}, pcmLimits: pcm16le.DefaultLimits(), maxBlockSamples: 4096}
	for _, index := range []int64{-1, 0, 23, 72, 1 << 62} {
		frame, err := bundle.RenderFrame(context.Background(), index)
		if err != ErrUnsupported || !reflect.DeepEqual(frame, referencevisual.FrameResult{}) {
			t.Fatalf("frame guard: %d %v", index, err)
		}
		surface, err := bundle.RenderSurfaceFrame(context.Background(), index)
		if err != ErrUnsupported || !reflect.DeepEqual(surface, referencevisual.SurfaceFrameResult{}) {
			t.Fatalf("surface guard: %d %v", index, err)
		}
	}
	for _, test := range []struct {
		first, count int64
		want         error
	}{
		{47999, 1, ErrUnsupported}, {144000, 0, ErrUnsupported}, {143999, 2, ErrUnsupported}, {48000, -1, ErrLimit}, {48000, 4097, ErrLimit}, {1 << 62, 1 << 62, ErrLimit}, {-1, 0, ErrUnsupported},
	} {
		pcm, err := bundle.RenderPCM(context.Background(), test.first, test.count)
		if err != test.want || !reflect.DeepEqual(pcm, pcm16le.Result{}) {
			t.Fatalf("PCM guard %d/%d: %v", test.first, test.count, err)
		}
	}
	bundle.visualEnabled = false
	bundle.audioEnabled = false
	if _, err := bundle.RenderFrame(context.Background(), 24); err != ErrUnsupported {
		t.Fatal("disabled visual")
	}
	if _, err := bundle.RenderPCM(context.Background(), 48000, 1); err != ErrUnsupported {
		t.Fatal("disabled audio")
	}
}
