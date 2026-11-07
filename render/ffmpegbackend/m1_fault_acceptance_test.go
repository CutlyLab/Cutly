package ffmpegbackend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestM1CancellationDuringDecodedEvidenceIsTerminalAndBounded(t *testing.T) {
	fixture := newBackendFixture(t, fakeSlowDecodedEvidenceScript)
	var events []renderapi.ProgressEvent
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := fixture.backend.Render(ctx, fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("decoded-evidence cancellation took %s, want <= 2s", elapsed)
	}
	if len(events) == 0 || events[len(events)-1].State != renderapi.JobCanceled {
		t.Fatalf("terminal events = %+v", events)
	}
	assertOutputRootEmpty(t, fixture.outputRoot)
}

func TestM1RealFailureIsNotReclassifiedByLateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := errors.New("injected storage failure")
	var events []renderapi.ProgressEvent
	emitter := progressEmitter{sink: renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}), jobID: "late-cancel"}
	if err := emitter.fail(ctx, CodePublicationRejected, want); !errors.Is(err, want) {
		t.Fatalf("error = %v, want original I/O failure", err)
	}
	if len(events) != 1 || events[0].State != renderapi.JobFailed {
		t.Fatalf("events = %+v, want one failed terminal event", events)
	}
}

func TestM1ProcessFaultsLeaveNoPartialPublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
	}{
		{name: "partial encoded output then crash", script: fakePartialOutputCrashScript},
		{name: "bounded stderr overflow", script: fakeStderrOverflowScript},
		{name: "decoded output underflow", script: fakeDecodedUnderflowScript},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackendFixture(t, test.script)
			_, err := fixture.backend.Render(context.Background(), fixture.request, nil)
			if !errors.Is(err, ErrCommand) {
				t.Fatalf("error = %v, want ErrCommand", err)
			}
			assertOutputRootEmpty(t, fixture.outputRoot)
		})
	}
}

func TestM1ProbeRejectionMatrix(t *testing.T) {
	for _, test := range []struct {
		name     string
		assetID  videoedit.ID
		wantText string
		mutate   func(*probe.ProbeManifest)
	}{
		{
			name: "vfr", assetID: "video-a", wantText: "proven CFR",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Video.FrameRateMode = probe.FrameRateVFR },
		},
		{
			name: "unknown frame rate", assetID: "video-a", wantText: "proven CFR",
			mutate: func(value *probe.ProbeManifest) {
				value.Streams[0].Video.FrameRateMode = probe.FrameRateUnknown
				value.Streams[0].Video.AverageFrameRate = nil
			},
		},
		{
			name: "negative pts", assetID: "video-a", wantText: "non-zero or negative video start PTS",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].StartPTS = -1 },
		},
		{
			name: "positive pts", assetID: "video-a", wantText: "non-zero or negative video start PTS",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].StartPTS = 1 },
		},
		{
			name: "rotation", assetID: "video-a", wantText: "unrotated opaque SDR",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Video.RotationDegrees = 90 },
		},
		{
			name: "alpha", assetID: "video-a", wantText: "unrotated opaque SDR",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Video.HasAlpha = true },
		},
		{
			name: "hdr", assetID: "video-a", wantText: "unrotated opaque SDR",
			mutate: func(value *probe.ProbeManifest) {
				value.Streams[0].Video.HDR = &probe.HDRMetadata{Format: "smpte2084", MaxContentLightLevel: 1000}
			},
		},
		{
			name: "non-square pixels", assetID: "video-a", wantText: "square-pixel",
			mutate: func(value *probe.ProbeManifest) {
				value.Streams[0].Video.SampleAspectRatio = &probe.Ratio{Numerator: 2, Denominator: 1}
			},
		},
		{
			name: "full range", assetID: "video-a", wantText: "limited-range BT.709",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Video.Color.Range = probe.ColorRangeFull },
		},
		{
			name: "audio encoder delay", assetID: "audio", wantText: "encoder delay or padding",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Audio.EncoderDelaySamples = 1024 },
		},
		{
			name: "audio padding", assetID: "audio", wantText: "encoder delay or padding",
			mutate: func(value *probe.ProbeManifest) { value.Streams[0].Audio.PaddingSamples = 1024 },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackendFixture(t, fakeSuccessScript)
			diagnostics := preflightWithMutatedProbe(t, fixture, test.assetID, test.mutate)
			if !hasDiagnostic(diagnostics, CodeProbeUnsupported) {
				t.Fatalf("missing %s diagnostic: %+v", CodeProbeUnsupported, diagnostics)
			}
			if !diagnosticsContain(diagnostics, test.wantText) {
				t.Fatalf("diagnostics do not explain %q: %+v", test.wantText, diagnostics)
			}
		})
	}
}

func preflightWithMutatedProbe(t *testing.T, fixture backendFixture, assetID videoedit.ID, mutate func(*probe.ProbeManifest)) []videoedit.Diagnostic {
	t.Helper()
	request := fixture.request
	request.Manifest = cloneManifest(t, request.Manifest)
	resolver, ok := fixture.backend.probeResolver.(staticProbeResolver)
	if !ok {
		t.Fatalf("probe resolver type = %T", fixture.backend.probeResolver)
	}
	for index := range request.Manifest.Resources {
		resource := &request.Manifest.Resources[index]
		if resource.AssetID != assetID {
			continue
		}
		original, exists := resolver[resource.ProbeFingerprint]
		if !exists {
			t.Fatalf("probe %q is missing", resource.ProbeFingerprint)
		}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var changed probe.ProbeManifest
		if err := json.Unmarshal(raw, &changed); err != nil {
			t.Fatal(err)
		}
		mutate(&changed)
		fingerprint, err := probe.Fingerprint(changed)
		if err != nil {
			t.Fatalf("mutated probe is not a valid rejection fixture: %v", err)
		}
		resolver[fingerprint] = changed
		resource.ProbeFingerprint = fingerprint
		request.Manifest.Fingerprint = refingerprintManifest(t, request.Manifest)
		return fixture.backend.Preflight(context.Background(), request.Plan, request.Manifest)
	}
	t.Fatalf("asset %q is not materialized", assetID)
	return nil
}

func diagnosticsContain(values []videoedit.Diagnostic, text string) bool {
	for _, value := range values {
		if strings.Contains(value.Message, text) {
			return true
		}
	}
	return false
}

func assertOutputRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, filepath.Base(entry.Name()))
		}
		t.Fatalf("partial render state remains in output root: %v", names)
	}
}

const fakeSlowDecodedEvidenceScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*|*" -f s16le "*) sleep 5; exit 0 ;;
esac
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
case "$last" in
  fd:*) eval "printf 'fake mp4' >&$descriptor" ;;
  *) printf "fake mp4" > "$last" ;;
esac
`

const fakePartialOutputCrashScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
case "$last" in
  fd:*) eval "printf 'partial mp4' >&$descriptor" ;;
  *) printf "partial mp4" > "$last" ;;
esac
echo "injected encoder crash" >&2
exit 9
`

const fakeStderrOverflowScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
i=0
while [ "$i" -lt 600 ]; do
  echo "injected stderr overflow 012345678901234567890123456789" >&2
  i=$((i + 1))
done
exit 0
`

const fakeDecodedUnderflowScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*) printf 'x'; exit 0 ;;
  *" -f s16le "*) printf 'x'; exit 0 ;;
esac
descriptor=""
previous=""
for last do
  if [ "$previous" = "-fd" ]; then descriptor="$last"; fi
  previous="$last"
done
case "$last" in
  fd:*) eval "printf 'fake mp4' >&$descriptor" ;;
  *) printf "fake mp4" > "$last" ;;
esac
`
