package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CutlyLab/Cutly/core/videoedit"
	cutlyffprobe "github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	renderevidence "github.com/CutlyLab/Cutly/render/evidence"
)

func TestCompileCommandIsDeterministicAndShellFree(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	first, err := compileCommand(fixture.request, filepath.Join(fixture.outputRoot, "temporary.mp4"), fixture.profiles)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compileCommand(fixture.request, filepath.Join(fixture.outputRoot, "temporary.mp4"), fixture.profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first.Arguments, second.Arguments) {
		t.Fatalf("argv is not deterministic\n%q\n%q", first.Arguments, second.Arguments)
	}
	joined := strings.Join(first.Arguments, " ")
	for _, want := range []string{
		"concat=n=2:v=1:a=0",
		"[a0]anull[acat]",
		"trim=end_frame=60",
		"atrim=start_sample=0:end_sample=96000",
		"-frames:v 60",
		"-protocol_whitelist file,fd",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv does not contain %q: %s", want, joined)
		}
	}
	for _, argument := range first.Arguments {
		if argument == "sh" || argument == "bash" || argument == "-c" {
			t.Fatalf("shell token leaked into argv: %q", first.Arguments)
		}
	}
	if len(first.InputPaths) != 3 {
		t.Fatalf("inputs = %q", first.InputPaths)
	}
}

func TestBackendImplementationAndRuntimeIdentitiesAreIndependent(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	second, err := New(context.Background(), Options{
		Executable: fixture.backend.executable, OutputRoot: fixture.backend.outputRoot,
		InputRoots: []string{fixture.backend.inputRoots[0].lexical}, ProbeResolver: fixture.backend.probeResolver,
		MaxStderrBytes: fixture.backend.maxStderrBytes, SDKBuildDigest: digestByte('b'),
		RuntimeDependencyFingerprint: fixture.backend.runtimeDependencyFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	firstCapabilities, err := fixture.backend.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondCapabilities, err := second.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstCapabilities.BuildFingerprint == secondCapabilities.BuildFingerprint {
		t.Fatal("changing SDK build digest did not invalidate backend implementation identity")
	}
	if fixture.backend.runtimeFingerprint != second.runtimeFingerprint {
		t.Fatal("SDK build digest changed the resolver-bound FFmpeg runtime identity")
	}
	if fixture.backend.buildFingerprint == fixture.backend.runtimeFingerprint {
		t.Fatal("backend implementation and FFmpeg runtime fingerprints collapsed into one identity")
	}

	third, err := New(context.Background(), Options{
		Executable: fixture.backend.executable, OutputRoot: fixture.backend.outputRoot,
		InputRoots: []string{fixture.backend.inputRoots[0].lexical}, ProbeResolver: fixture.backend.probeResolver,
		MaxStderrBytes: fixture.backend.maxStderrBytes, SDKBuildDigest: digestByte('a'),
		RuntimeDependencyFingerprint: digestByte('e'),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if fixture.backend.buildFingerprint != third.buildFingerprint {
		t.Fatal("runtime dependency identity changed the independent backend implementation identity")
	}
	if fixture.backend.runtimeFingerprint == third.runtimeFingerprint {
		t.Fatal("runtime dependency identity did not invalidate FFmpeg runtime identity")
	}

	changedEnvironment, err := runtimeIdentityFingerprint(
		digestByte('1'), "ffmpeg version test", digestByte('2'),
		"cutly.ffmpeg.env.changed", controlledRuntimeEnvironment(),
	)
	if err != nil {
		t.Fatal(err)
	}
	originalEnvironment, err := runtimeIdentityFingerprint(
		digestByte('1'), "ffmpeg version test", digestByte('2'),
		RuntimeEnvironmentProfile, controlledRuntimeEnvironment(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if originalEnvironment == changedEnvironment {
		t.Fatal("runtime environment profile did not invalidate FFmpeg runtime identity")
	}
	originalImplementation, err := backendImplementationFingerprint(BackendImplementationProfile, digestByte('a'))
	if err != nil {
		t.Fatal(err)
	}
	changedImplementation, err := backendImplementationFingerprint("cutly.ffmpeg-hardcut.impl.changed", digestByte('a'))
	if err != nil {
		t.Fatal(err)
	}
	if originalImplementation == changedImplementation {
		t.Fatal("backend semantic implementation profile did not invalidate backend identity")
	}
}

func TestRuntimeCommandsDoNotInheritLoaderInjection(t *testing.T) {
	for _, name := range []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
	} {
		t.Setenv(name, "cutly-test-loader-injection")
	}
	guarded := strings.Replace(fakeSuccessScript, "if [ \"$1\" = \"-version\" ]; then", `
if [ "${LD_PRELOAD+x}" = x ] || [ "${LD_LIBRARY_PATH+x}" = x ] || [ "${DYLD_INSERT_LIBRARIES+x}" = x ] || [ "${DYLD_LIBRARY_PATH+x}" = x ]; then
  echo "loader injection leaked" >&2
  exit 91
fi
if [ "$1" = "-version" ]; then`, 1)
	fixture := newBackendFixture(t, guarded)
	if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExactTickBoundsUseProbedStreamTimeBase(t *testing.T) {
	timeBase, start, end, err := exactTickBounds(
		videoedit.TimeRange{Start: rational(1001, 30000), Duration: rational(1001, 30000)},
		rational(1, 90000),
	)
	if err != nil || timeBase != rational(1, 90000) || start != 3003 || end != 6006 {
		t.Fatalf("bounds = %+v %d %d, %v", timeBase, start, end, err)
	}
	if _, _, _, err := exactTickBounds(
		videoedit.TimeRange{Start: rational(1, 3), Duration: rational(1, 3)},
		rational(1, 1000),
	); !errors.Is(err, videoedit.ErrInexactRescale) {
		t.Fatalf("inexact error = %v", err)
	}
}

func TestSourceBoundsMustAlignWithDecodedMediaGrid(t *testing.T) {
	durationPTS := int64(90000)
	fps30 := rational(30, 1)
	videoStream := probe.Stream{
		Index: 0, Kind: probe.StreamKindVideo, TimeBase: rational(1, 90000), DurationPTS: &durationPTS,
		Video: &probe.VideoStream{
			Width: 64, Height: 64, PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateCFR,
			AverageFrameRate: &fps30,
			Color:            probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited},
		},
	}
	offGridVideo := []videoedit.PlannedClip{{
		ClipID: "video-off-grid", VisibleSourceRange: videoedit.TimeRange{
			Start: rational(1, 90000), Duration: rational(1, 30),
		},
	}}
	if err := validateVideoProfile(videoStream, offGridVideo); err == nil || !strings.Contains(err.Error(), "CFR frame grid") {
		t.Fatalf("off-grid video error = %v", err)
	}
	ntsc := rational(30000, 1001)
	videoStream.Video.AverageFrameRate = &ntsc
	onGridNTSC := []videoedit.PlannedClip{{
		ClipID: "video-ntsc", VisibleSourceRange: videoedit.TimeRange{
			Start: rational(1001, 30000), Duration: rational(1001, 30000),
		},
	}}
	if err := validateVideoProfile(videoStream, onGridNTSC); err != nil {
		t.Fatalf("exact NTSC grid rejected: %v", err)
	}

	audioDurationPTS := int64(192000)
	audioStream := probe.Stream{
		Index: 0, Kind: probe.StreamKindAudio, TimeBase: rational(1, 96000), DurationPTS: &audioDurationPTS,
		Audio: &probe.AudioStream{SampleRate: 48000, Channels: 2, ChannelLayout: "stereo", SampleFormat: "s16"},
	}
	offGridAudio := []videoedit.PlannedClip{{
		ClipID: "audio-off-grid", VisibleSourceRange: videoedit.TimeRange{
			Start: rational(1, 96000), Duration: rational(1, 48000),
		},
	}}
	if err := validateAudioProfile(audioStream, offGridAudio); err == nil || !strings.Contains(err.Error(), "audio sample grid") {
		t.Fatalf("off-grid audio error = %v", err)
	}
	onGridAudio := []videoedit.PlannedClip{{
		ClipID: "audio-grid", VisibleSourceRange: videoedit.TimeRange{
			Start: rational(1, 48000), Duration: rational(1, 48000),
		},
	}}
	if err := validateAudioProfile(audioStream, onGridAudio); err != nil {
		t.Fatalf("exact audio sample grid rejected: %v", err)
	}
}

func TestSafeContainerFormatRejectsMixedUnknownAliases(t *testing.T) {
	if !safeContainerFormat("mov,mp4,m4a,3gp,3g2,mj2") || !safeContainerFormat("wav") {
		t.Fatal("known safe formats were rejected")
	}
	for _, value := range []string{"", "hls", "mov,evil", "evil,mp4", "mp4,"} {
		if safeContainerFormat(value) {
			t.Fatalf("unsafe mixed format %q was accepted", value)
		}
	}
}

func TestPreflightRejectsUnsupportedSemantics(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	tests := []struct {
		name   string
		mutate func(*videoedit.RenderPlan, *renderapi.ExecutionManifest)
		code   string
	}{
		{
			name: "second video track",
			mutate: func(plan *videoedit.RenderPlan, _ *renderapi.ExecutionManifest) {
				plan.Video[1].TrackID = "another-track"
				plan.Video[1].TrackIndex = 2
			},
			code: CodeTrackUnsupported,
		},
		{
			name: "gap",
			mutate: func(plan *videoedit.RenderPlan, _ *renderapi.ExecutionManifest) {
				plan.Video[1].VisibleTimelineRange.Start = rational(3, 2)
				plan.Video[1].VisibleTimelineRange.Duration = rational(1, 2)
				plan.Video[1].TimelineRange = plan.Video[1].VisibleTimelineRange
				plan.Video[1].VisibleSourceRange.Duration = rational(1, 2)
				plan.Video[1].SourceRange = plan.Video[1].VisibleSourceRange
			},
			code: CodeTimelineUnsupported,
		},
		{
			name: "properties",
			mutate: func(plan *videoedit.RenderPlan, _ *renderapi.ExecutionManifest) {
				plan.Video[0].Properties = map[string]json.RawMessage{"opacity": json.RawMessage(`1`)}
			},
			code: CodePropertyUnsupported,
		},
		{
			name: "retime",
			mutate: func(plan *videoedit.RenderPlan, _ *renderapi.ExecutionManifest) {
				plan.Video[0].SourceRange.Duration = rational(2, 1)
				plan.Video[0].VisibleSourceRange.Duration = rational(2, 1)
				plan.RequiredCapabilities.Features = []videoedit.CapabilityID{videoedit.CapabilityVideoLinearRetime}
			},
			code: CodeRetimeUnsupported,
		},
		{
			name: "cas locator",
			mutate: func(_ *videoedit.RenderPlan, manifest *renderapi.ExecutionManifest) {
				manifest.Resources[0].LocatorScheme = renderapi.LocatorCAS
				manifest.Resources[0].Locator = "cas://sha256/" + strings.TrimPrefix(manifest.Resources[0].ContentHash, "sha256:")
			},
			code: CodeResourceUnsupported,
		},
		{
			name: "browser runtime",
			mutate: func(_ *videoedit.RenderPlan, manifest *renderapi.ExecutionManifest) {
				manifest.BrowserFingerprint = digestByte('9')
			},
			code: CodeManifestUnsupported,
		},
		{
			name: "unsupported output",
			mutate: func(_ *videoedit.RenderPlan, manifest *renderapi.ExecutionManifest) {
				manifest.Output.PixelFormat = "rgba"
			},
			code: CodeOutputUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := clonePlan(t, fixture.request.Plan)
			manifest := cloneManifest(t, fixture.request.Manifest)
			test.mutate(&plan, &manifest)
			plan.Fingerprint = refingerprintPlan(t, plan)
			manifest.PlanFingerprint = plan.Fingerprint
			manifest.Fingerprint = refingerprintManifest(t, manifest)
			diagnostics := fixture.backend.Preflight(context.Background(), plan, manifest)
			if !hasDiagnostic(diagnostics, test.code) {
				t.Fatalf("diagnostics = %+v, want %s", diagnostics, test.code)
			}
		})
	}
}

func TestPreflightRequiresBoundProvenMediaAndAuthorizedInputRoot(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	resolver := fixture.backend.probeResolver.(staticProbeResolver)
	manifest := cloneManifest(t, fixture.request.Manifest)
	videoIndex := -1
	for index, resource := range manifest.Resources {
		if resource.Kind == videoedit.AssetKindVideo {
			videoIndex = index
			break
		}
	}
	if videoIndex < 0 {
		t.Fatal("fixture has no video resource")
	}
	original := resolver[manifest.Resources[videoIndex].ProbeFingerprint]
	vfr := original
	vfr.Streams = append([]probe.Stream(nil), original.Streams...)
	video := *vfr.Streams[0].Video
	video.FrameRateMode = probe.FrameRateVFR
	vfr.Streams[0].Video = &video
	vfrFingerprint, err := probe.Fingerprint(vfr)
	if err != nil {
		t.Fatal(err)
	}
	resolver[vfrFingerprint] = vfr
	manifest.Resources[videoIndex].ProbeFingerprint = vfrFingerprint
	manifest.Fingerprint = refingerprintManifest(t, manifest)
	if diagnostics := fixture.backend.Preflight(context.Background(), fixture.request.Plan, manifest); !hasDiagnostic(diagnostics, CodeProbeUnsupported) {
		t.Fatalf("VFR diagnostics = %+v", diagnostics)
	}

	manifest = cloneManifest(t, fixture.request.Manifest)
	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "copy.bin")
	originalPath := manifest.Resources[0].Locator
	raw, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Resources[0].Locator = outside
	manifest.Fingerprint = refingerprintManifest(t, manifest)
	if diagnostics := fixture.backend.Preflight(context.Background(), fixture.request.Plan, manifest); !hasDiagnostic(diagnostics, CodeResourceUnsupported) {
		t.Fatalf("outside-root diagnostics = %+v", diagnostics)
	}
}

func TestRenderPublishesVerifiedArtifactAndProgress(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	var events []renderapi.ProgressEvent
	artifact, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ContentHash != hashBytes([]byte("fake mp4")) || artifact.SizeBytes != int64(len("fake mp4")) || artifact.FrameCount != 60 || artifact.SampleCount != 96000 {
		t.Fatalf("artifact = %+v", artifact)
	}
	target, err := url.PathUnescape(mustURL(t, artifact.URI).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "fake mp4" {
		t.Fatalf("published content = %q, %v", content, err)
	}
	evidencePath, err := url.PathUnescape(mustURL(t, artifact.EvidenceURI).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	evidenceRaw, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	evidenceManifest, err := renderevidence.Decode(evidenceRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := renderevidence.ValidateAgainst(evidenceManifest, fixture.request, &artifact); err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %+v", events)
	}
	wantStates := []renderapi.JobState{renderapi.JobQueued, renderapi.JobRunning, renderapi.JobRunning, renderapi.JobRunning, renderapi.JobComplete}
	for index, event := range events {
		if event.Sequence != uint64(index) || event.State != wantStates[index] {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
}

func TestPublicationRefusesMediaWhenEvidenceCannotPublish(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	target, err := url.PathUnescape(mustURL(t, fixture.request.Job.ArtifactURI).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	evidenceTarget := target + ".evidence.json"
	createdConflict := false
	_, err = fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		if event.Stage == renderapi.StagePublish && event.State == renderapi.JobRunning && !createdConflict {
			createdConflict = true
			return os.WriteFile(evidenceTarget, []byte("keep conflict"), 0o600)
		}
		return nil
	}))
	if !errors.Is(err, ErrPublication) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("media was not rolled back: %v", statErr)
	}
	conflict, readErr := os.ReadFile(evidenceTarget)
	if readErr != nil || string(conflict) != "keep conflict" {
		t.Fatalf("pre-existing evidence conflict changed: %q, %v", conflict, readErr)
	}
}

func TestPublicationRollsBackEvidenceWhenMediaCommitFails(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	target, err := url.PathUnescape(mustURL(t, fixture.request.Job.ArtifactURI).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	evidenceTarget := target + ".evidence.json"
	createdConflict := false
	_, err = fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		if event.Stage == renderapi.StagePublish && event.State == renderapi.JobRunning && !createdConflict {
			createdConflict = true
			return os.WriteFile(target, []byte("keep media conflict"), 0o600)
		}
		return nil
	}))
	if !errors.Is(err, ErrPublication) {
		t.Fatalf("error = %v", err)
	}
	conflict, readErr := os.ReadFile(target)
	if readErr != nil || string(conflict) != "keep media conflict" {
		t.Fatalf("pre-existing media conflict changed: %q, %v", conflict, readErr)
	}
	if _, statErr := os.Stat(evidenceTarget); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("evidence sidecar was not rolled back: %v", statErr)
	}
}

func TestPublishEvidenceSyncsDirectoryBeforeMediaCommit(t *testing.T) {
	t.Run("ordered commit", func(t *testing.T) {
		root, evidence, media := newPublicationFiles(t)
		syncCalls := 0
		evidencePublished, mediaPublished, err := publishEvidenceThenMedia(
			root,
			"evidence.tmp", "result.mp4.evidence.json", evidence,
			"media.tmp", "result.mp4", media,
			func() error {
				syncCalls++
				if _, err := root.Lstat("result.mp4.evidence.json"); err != nil {
					t.Fatalf("evidence was not linked before directory sync: %v", err)
				}
				if _, err := root.Lstat("result.mp4"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("media commit existed before evidence directory sync: %v", err)
				}
				return syncRootDirectory(root)
			},
		)
		if err != nil || !evidencePublished || !mediaPublished || syncCalls != 1 {
			t.Fatalf("published=(%t,%t) syncCalls=%d error=%v", evidencePublished, mediaPublished, syncCalls, err)
		}
		if _, err := root.Lstat("result.mp4"); err != nil {
			t.Fatalf("media commit was not linked after evidence sync: %v", err)
		}
	})

	t.Run("sync failure blocks media commit", func(t *testing.T) {
		root, evidence, media := newPublicationFiles(t)
		want := errors.New("injected directory sync failure")
		evidencePublished, mediaPublished, err := publishEvidenceThenMedia(
			root,
			"evidence.tmp", "result.mp4.evidence.json", evidence,
			"media.tmp", "result.mp4", media,
			func() error { return want },
		)
		if !errors.Is(err, want) || !evidencePublished || mediaPublished {
			t.Fatalf("published=(%t,%t) error=%v", evidencePublished, mediaPublished, err)
		}
		if _, err := root.Lstat("result.mp4"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("media commit exists after evidence sync failure: %v", err)
		}
	})
}

func newPublicationFiles(t *testing.T) (*os.Root, *os.File, *os.File) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	open := func(name, content string) *os.File {
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(content); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	return root, open("evidence.tmp", "evidence"), open("media.tmp", "media")
}

func TestRenderRejectsUntrustedPublicationAndDoesNotOverwrite(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	hosted := (&url.URL{Scheme: "file", Host: "localhost", Path: filepath.Join(fixture.outputRoot, "hosted.mp4")}).String()
	if _, err := fixture.backend.resolveArtifact(hosted); !errors.Is(err, ErrPublication) {
		t.Fatalf("hosted file URI error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	request := fixture.request
	request.Job.ArtifactURI = fileURI(outside)
	if _, err := fixture.backend.Render(context.Background(), request, nil); !errors.Is(err, ErrPublication) {
		t.Fatalf("outside error = %v", err)
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside artifact exists: %v", err)
	}

	target := filepath.Join(fixture.outputRoot, "existing.mp4")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	request = fixture.request
	request.Job.ArtifactURI = fileURI(target)
	if _, err := fixture.backend.Render(context.Background(), request, nil); !errors.Is(err, ErrPublication) {
		t.Fatalf("overwrite error = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing artifact changed: %q, %v", content, err)
	}
}

func TestRenderRejectsChangedAndSymlinkResources(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	changed := fixture.request.Manifest.Resources[0].Locator
	if err := os.WriteFile(changed, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.backend.Render(context.Background(), fixture.request, nil); !errors.Is(err, ErrContent) {
		t.Fatalf("changed resource error = %v", err)
	}

	fixture = newBackendFixture(t, fakeSuccessScript)
	realPath := fixture.request.Manifest.Resources[0].Locator
	symlink := filepath.Join(t.TempDir(), "input-link")
	if err := os.Symlink(realPath, symlink); err != nil {
		t.Fatal(err)
	}
	request := fixture.request
	request.Manifest.Resources[0].Locator = symlink
	request.Manifest.Fingerprint = refingerprintManifest(t, request.Manifest)
	request.Job.ExecutionManifestFingerprint = request.Manifest.Fingerprint
	if _, err := fixture.backend.Render(context.Background(), request, nil); !errors.Is(err, ErrPreflight) {
		t.Fatalf("symlink resource error = %v", err)
	}
}

func TestRenderUsesPrivateImmutableInputCopies(t *testing.T) {
	fixture := newBackendFixture(t, fakeCaptureScript)
	argumentsFile := fixture.backend.executable + ".arguments"
	originals := make([]string, 0, len(fixture.request.Manifest.Resources))
	for _, resource := range fixture.request.Manifest.Resources {
		originals = append(originals, resource.Locator)
	}
	mutated := false
	_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		if event.Stage == renderapi.StageEncode && event.State == renderapi.JobRunning && !mutated {
			mutated = true
			return os.WriteFile(originals[0], []byte("mutated after staging"), 0o600)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	arguments := string(raw)
	if !strings.Contains(arguments, "fd:") {
		t.Fatalf("inherited file-descriptor input missing from argv: %s", arguments)
	}
	for _, original := range originals {
		if strings.Contains(arguments, original) {
			t.Fatalf("mutable source locator leaked into FFmpeg argv: %s", original)
		}
	}
}

func TestRenderFailureAndCancellationAreTerminal(t *testing.T) {
	t.Run("command failure", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeFailureScript)
		var events []renderapi.ProgressEvent
		_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
			events = append(events, event)
			return nil
		}))
		if !errors.Is(err, ErrCommand) {
			t.Fatalf("error = %v", err)
		}
		if len(events) == 0 || events[len(events)-1].State != renderapi.JobFailed {
			t.Fatalf("events = %+v", events)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.outputRoot, "result.mp4")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("partial artifact exists: %v", statErr)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture := newBackendFixture(t, fakeSlowScript)
		var events []renderapi.ProgressEvent
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, err := fixture.backend.Render(ctx, fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
			events = append(events, event)
			return nil
		}))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("cancel took %s", time.Since(started))
		}
		if len(events) == 0 || events[len(events)-1].State != renderapi.JobCanceled {
			t.Fatalf("events = %+v", events)
		}
	})
}

func TestDecodedEvidenceFailsFastAboveExpectedCount(t *testing.T) {
	fixture := newBackendFixture(t, fakeExcessDecodedScript)
	started := time.Now()
	_, err := fixture.backend.Render(context.Background(), fixture.request, nil)
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("excess decoded output did not fail fast: %s", time.Since(started))
	}
	target, _ := url.PathUnescape(mustURL(t, fixture.request.Job.ArtifactURI).EscapedPath())
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("artifact exists after decoded evidence overflow: %v", statErr)
	}
}

func TestOutputRootPathSwapFailsBeforePublication(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	moved := fixture.outputRoot + "-moved"
	swapped := false
	_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, event renderapi.ProgressEvent) error {
		if event.Stage == renderapi.StagePublish && event.State == renderapi.JobRunning && !swapped {
			swapped = true
			if renameErr := os.Rename(fixture.outputRoot, moved); renameErr != nil {
				return renameErr
			}
			return os.Mkdir(fixture.outputRoot, 0o700)
		}
		return nil
	}))
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("error = %v", err)
	}
	for _, path := range []string{filepath.Join(fixture.outputRoot, "result.mp4"), filepath.Join(moved, "result.mp4")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("artifact escaped during root swap at %s: %v", path, statErr)
		}
	}
}

func TestRenderStopsWhenProgressSinkFails(t *testing.T) {
	fixture := newBackendFixture(t, fakeSuccessScript)
	want := errors.New("scheduler disconnected")
	calls := 0
	_, err := fixture.backend.Render(context.Background(), fixture.request, renderapi.EventSinkFunc(func(_ context.Context, _ renderapi.ProgressEvent) error {
		calls++
		if calls == 2 {
			return want
		}
		return nil
	}))
	if !errors.Is(err, want) || calls != 2 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.outputRoot, "result.mp4")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("artifact exists after sink failure: %v", statErr)
	}
}

func TestRealFFmpegIntegration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not available")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "output")
	if err := os.Mkdir(outputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media := map[videoedit.ID]string{
		"video-a": filepath.Join(root, "video-a.mp4"),
		"video-b": filepath.Join(root, "video-b.mp4"),
		"audio":   filepath.Join(root, "audio.mov"),
	}
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:r=30:d=2",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-y", media["video-a"],
	)
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x64:r=30:d=2",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited",
		"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", "-color_range", "tv",
		"-y", media["video-b"],
	)
	runExternal(t, ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
		"-vn", "-ac", "2", "-c:a", "pcm_s16le", "-y", media["audio"],
	)

	seed := newBackendFixture(t, fakeSuccessScript)
	plan := clonePlan(t, seed.request.Plan)
	for index := range plan.Video {
		plan.Video[index].SourceRange.Start = rational(1, 30)
		plan.Video[index].VisibleSourceRange.Start = rational(1, 30)
	}
	plan.Audio[0].SourceRange.Start = rational(1, 48000)
	plan.Audio[0].VisibleSourceRange.Start = rational(1, 48000)
	resources := make([]renderapi.MaterializedResource, 0, len(plan.Resources))
	probeResolver := staticProbeResolver{}
	probeAdapter, err := cutlyffprobe.New(
		ctx,
		ffprobePath,
		cutlyffprobe.DefaultBuildIdentity(digestByte('7')), // test-only stand-in for the installed runtime closure
		cutlyffprobe.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plan.Resources {
		planned := &plan.Resources[index]
		path := media[planned.AssetID]
		probeManifest, diagnostics, probeErr := probeAdapter.Probe(ctx, cutlyffprobe.Request{AssetID: planned.AssetID, Path: path})
		if probeErr != nil || probe.HasErrorDiagnostics(diagnostics) {
			t.Fatalf("probe %s: %v, diagnostics=%+v", planned.AssetID, probeErr, diagnostics)
		}
		hash, size := probeManifest.Resource.ContentHash, probeManifest.Resource.SizeBytes
		planned.ContentHash = hash
		probeFingerprint, fingerprintErr := probe.Fingerprint(probeManifest)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		probeResolver[probeFingerprint] = probeManifest
		resources = append(resources, renderapi.MaterializedResource{
			AssetID: planned.AssetID, Kind: planned.Kind, ContentHash: hash, SizeBytes: size,
			LocatorScheme: renderapi.LocatorFile, Locator: path, ProbeFingerprint: probeFingerprint,
		})
	}
	plan.Fingerprint = refingerprintPlan(t, plan)
	backend, err := New(ctx, Options{
		Executable: ffmpeg, OutputRoot: outputRoot, InputRoots: []string{root},
		ProbeResolver: probeResolver, SDKBuildDigest: digestByte('a'),
		RuntimeDependencyFingerprint: digestByte('f'),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	capabilities, err := backend.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digestByte('c'), EvaluatorBuild: digestByte('d'), Resources: resources,
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: backend.runtimeFingerprint, DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4", Width: 64, Height: 64, PixelFormat: "yuv420p", ColorSpace: "bt709",
			VideoCodec: "h264", AudioCodec: "aac", SampleRate: 48000, Channels: 2,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := backend.Preflight(ctx, plan, manifest); videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("real adapter preflight diagnostics: %+v", diagnostics)
	}
	output := filepath.Join(backend.outputRoot, "integration.mp4")
	request := renderapi.RenderRequest{
		Plan: plan, Manifest: manifest,
		Job: renderapi.RenderJob{
			SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "integration", IdempotencyKey: "integration-once",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange, ArtifactURI: fileURI(output),
		},
	}
	artifact, err := backend.Render(ctx, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.FrameCount != 60 || artifact.SampleCount != 96000 {
		t.Fatalf("artifact = %+v", artifact)
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output stat = %+v, %v", info, err)
	}
	command := exec.CommandContext(ctx, ffprobePath,
		"-v", "error", "-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=nb_read_frames", "-of", "default=nw=1:nk=1", output,
	)
	raw, probeErr := command.Output()
	if probeErr != nil || strings.TrimSpace(string(raw)) != "60" {
		t.Fatalf("decoded video frame count = %q, %v", raw, probeErr)
	}
	decoded := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-i", output,
		"-map", "0:v:0", "-an", "-f", "rawvideo", "-pix_fmt", "rgb24", "-",
	)
	pixels, decodeErr := decoded.Output()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	frameBytes := 64 * 64 * 3
	if len(pixels) != 60*frameBytes {
		t.Fatalf("decoded pixel bytes = %d, want %d", len(pixels), 60*frameBytes)
	}
	first := pixels[(32*64+32)*3:]
	second := pixels[30*frameBytes+(32*64+32)*3:]
	if first[0] <= first[2] || second[2] <= second[0] {
		t.Fatalf("cut colors are not red then blue: first=%v second=%v", first[:3], second[:3])
	}

	firstEvidence := readEvidenceArtifact(t, artifact)
	repeat := request
	repeat.Job.ID = "integration-repeat"
	repeat.Job.IdempotencyKey = "integration-repeat-once"
	repeat.Job.ArtifactURI = fileURI(filepath.Join(backend.outputRoot, "integration-repeat.mp4"))
	repeatedArtifact, err := backend.Render(ctx, repeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	repeatedEvidence := readEvidenceArtifact(t, repeatedArtifact)
	if firstEvidence.DecodedVideo.RootHash != repeatedEvidence.DecodedVideo.RootHash ||
		firstEvidence.DecodedAudio.RootHash != repeatedEvidence.DecodedAudio.RootHash {
		t.Fatalf("decoded evidence is not repeatable: first=%+v repeat=%+v", firstEvidence, repeatedEvidence)
	}
}

func readEvidenceArtifact(t *testing.T, artifact renderapi.RenderArtifact) renderevidence.Manifest {
	t.Helper()
	path, err := url.PathUnescape(mustURL(t, artifact.EvidenceURI).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderevidence.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

type backendFixture struct {
	backend    *Backend
	request    renderapi.RenderRequest
	outputRoot string
	profiles   map[videoedit.ID]sourceProfile
}

func newBackendFixture(t *testing.T, script string) backendFixture {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "fake-ffmpeg")
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "output")
	if err := os.Mkdir(outputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	inputRoot := filepath.Join(root, "inputs")
	if err := os.Mkdir(inputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	type input struct {
		id      videoedit.ID
		kind    videoedit.AssetKind
		content []byte
	}
	inputs := []input{
		{id: "audio", kind: videoedit.AssetKindAudio, content: []byte("audio input")},
		{id: "video-a", kind: videoedit.AssetKindVideo, content: []byte("video input a")},
		{id: "video-b", kind: videoedit.AssetKindVideo, content: []byte("video input b")},
	}
	assets := make([]videoedit.Asset, 0, len(inputs))
	resources := make([]renderapi.MaterializedResource, 0, len(inputs))
	probes := staticProbeResolver{}
	duration := rational(2, 1)
	for _, value := range inputs {
		path := filepath.Join(inputRoot, string(value.id)+".bin")
		if err := os.WriteFile(path, value.content, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := hashBytes(value.content)
		assets = append(assets, videoedit.Asset{ID: value.id, Kind: value.kind, URI: path, ContentHash: hash, Duration: &duration})
		probeManifest := fixtureProbeManifest(value.id, value.kind, hash, int64(len(value.content)))
		probeFingerprint, err := probe.Fingerprint(probeManifest)
		if err != nil {
			t.Fatal(err)
		}
		probes[probeFingerprint] = probeManifest
		resources = append(resources, renderapi.MaterializedResource{
			AssetID: value.id, Kind: value.kind, ContentHash: hash, SizeBytes: int64(len(value.content)),
			LocatorScheme: renderapi.LocatorFile, Locator: path, ProbeFingerprint: probeFingerprint,
		})
	}
	backend, err := New(context.Background(), Options{
		Executable: executable, OutputRoot: outputRoot, InputRoots: []string{inputRoot},
		MaxStderrBytes: 4096, ProbeResolver: probes, SDKBuildDigest: digestByte('a'),
		RuntimeDependencyFingerprint: digestByte('f'),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	outputRoot = backend.outputRoot
	document := videoedit.Document{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "document",
		Revision:      1,
		Assets:        assets,
		Timelines: []videoedit.Timeline{{
			ID: "timeline",
			Tracks: []videoedit.Track{
				{ID: "video-track", Kind: videoedit.TrackKindVideo, Clips: []videoedit.Clip{
					{ID: "video-clip-a", AssetID: "video-a", TimelineRange: timeRange(0, 1), SourceRange: timeRange(0, 1)},
					{ID: "video-clip-b", AssetID: "video-b", TimelineRange: timeRange(1, 1), SourceRange: timeRange(0, 1)},
				}},
				{ID: "audio-track", Kind: videoedit.TrackKindAudio, Clips: []videoedit.Clip{
					{ID: "audio-clip", AssetID: "audio", TimelineRange: timeRange(0, 2), SourceRange: timeRange(0, 2)},
				}},
			},
		}},
	}
	plan, diagnostics := videoedit.Resolve(document, timeRange(0, 2), videoedit.RenderTarget{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock:  videoedit.FrameClock{Origin: rational(0, 1), FramesPerSecond: rational(30, 1)},
		SampleClock: videoedit.SampleClock{Origin: rational(0, 1), SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	})
	if videoedit.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	capabilities, err := backend.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := renderapi.FreezeExecutionManifest(plan, renderapi.ExecutionManifest{
		CompilerBuild: digestByte('c'), EvaluatorBuild: digestByte('d'), Resources: resources,
		Backend: capabilities,
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: backend.runtimeFingerprint, DecoderProfile: "ffmpeg-hardcut-v1",
			EncoderProfile: "libx264-aac-mp4.v1", ColorProfile: "cutly.bt709.v1",
		},
		Output: renderapi.OutputSpec{
			ReferenceProfile: "cutly.hardcut.bt709.v1", Format: "mp4", Width: 64, Height: 64, PixelFormat: "yuv420p", ColorSpace: "bt709",
			VideoCodec: "h264", AudioCodec: "aac", SampleRate: 48000, Channels: 2,
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1, DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		DeterministicSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := renderapi.RenderRequest{
		Plan: plan, Manifest: manifest,
		Job: renderapi.RenderJob{
			SchemaVersion: renderapi.RenderJobSchemaVersion, ID: "job", IdempotencyKey: "job-once",
			PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifest.Fingerprint,
			TimelineRange: plan.RequestedRange, FrameRange: plan.FrameRange, SampleRange: plan.SampleRange,
			ArtifactURI: fileURI(filepath.Join(outputRoot, "result.mp4")),
		},
	}
	profiles, profileDiagnostics := backend.resolveSourceProfiles(context.Background(), plan, manifest)
	if videoedit.HasErrorDiagnostics(profileDiagnostics) {
		t.Fatalf("profile diagnostics: %+v", profileDiagnostics)
	}
	return backendFixture{backend: backend, request: request, outputRoot: outputRoot, profiles: profiles}
}

type staticProbeResolver map[string]probe.ProbeManifest

func (resolver staticProbeResolver) ResolveProbe(_ context.Context, fingerprint string) (probe.ProbeManifest, error) {
	manifest, exists := resolver[fingerprint]
	if !exists {
		return probe.ProbeManifest{}, errors.New("probe manifest not found")
	}
	return manifest, nil
}

func fixtureProbeManifest(assetID videoedit.ID, kind videoedit.AssetKind, contentHash string, size int64) probe.ProbeManifest {
	manifest := probe.ProbeManifest{
		SchemaVersion: probe.SchemaVersion,
		Resource:      probe.ResourceIdentity{AssetID: assetID, ContentHash: contentHash, SizeBytes: size},
		Probe: probe.ProbeIdentity{
			Implementation: "test-probe-v1", BuildFingerprint: digestByte('1'), OptionsFingerprint: digestByte('2'),
		},
		Container: probe.ContainerInfo{StartTime: rational(0, 1), Seekable: true},
	}
	if kind == videoedit.AssetKindVideo {
		durationPTS := int64(30)
		average := rational(30, 1)
		manifest.Container.FormatName = "mov,mp4,m4a,3gp,3g2,mj2"
		manifest.Duration = rational(1, 1)
		manifest.Streams = []probe.Stream{{
			Index: 0, Kind: probe.StreamKindVideo, Codec: "h264", TimeBase: rational(1, 30), DurationPTS: &durationPTS,
			Video: &probe.VideoStream{
				Width: 64, Height: 64, PixelFormat: "yuv420p", FrameRateMode: probe.FrameRateCFR, AverageFrameRate: &average,
				Keyframes: probe.KeyframeSummary{},
				Color:     probe.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: probe.ColorRangeLimited},
			},
		}}
	} else {
		durationPTS := int64(96000)
		manifest.Container.FormatName = "wav"
		manifest.Duration = rational(2, 1)
		manifest.Streams = []probe.Stream{{
			Index: 0, Kind: probe.StreamKindAudio, Codec: "pcm_s16le", TimeBase: rational(1, 48000), DurationPTS: &durationPTS,
			Audio: &probe.AudioStream{SampleRate: 48000, Channels: 2, ChannelLayout: "stereo", SampleFormat: "s16"},
		}}
	}
	return manifest
}

func clonePlan(t *testing.T, value videoedit.RenderPlan) videoedit.RenderPlan {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone videoedit.RenderPlan
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func cloneManifest(t *testing.T, value renderapi.ExecutionManifest) renderapi.ExecutionManifest {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone renderapi.ExecutionManifest
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func refingerprintManifest(t *testing.T, manifest renderapi.ExecutionManifest) string {
	t.Helper()
	manifest.Fingerprint = ""
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func refingerprintPlan(t *testing.T, plan videoedit.RenderPlan) string {
	t.Helper()
	plan.Fingerprint = ""
	canonical, err := videoedit.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hasDiagnostic(values []videoedit.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func timeRange(start, duration int64) videoedit.TimeRange {
	return videoedit.TimeRange{Start: rational(start, 1), Duration: rational(duration, 1)}
}

func rational(value, rate int64) videoedit.RationalTime {
	return videoedit.RationalTime{Value: value, Rate: rate}
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestByte(value byte) string { return "sha256:" + strings.Repeat(string(value), 64) }

func fileURI(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func runExternal(t *testing.T, ctx context.Context, executable string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, executable, arguments...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %q: %v: %s", executable, arguments, err, output)
	}
}

const fakeSuccessScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*) dd if=/dev/zero bs=16384 count=60 2>/dev/null; exit 0 ;;
  *" -f s16le "*) dd if=/dev/zero bs=384000 count=1 2>/dev/null; exit 0 ;;
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

const fakeFailureScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
echo "forced failure" >&2
exit 7
`

const fakeSlowScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
sleep 5
`

const fakeCaptureScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
printf "%s\n" "$@" >> "${0}.arguments"
case " $* " in
  *" -f rawvideo "*) dd if=/dev/zero bs=16384 count=60 2>/dev/null; exit 0 ;;
  *" -f s16le "*) dd if=/dev/zero bs=384000 count=1 2>/dev/null; exit 0 ;;
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

const fakeExcessDecodedScript = `#!/bin/sh
if [ "$1" = "-version" ]; then
  echo "ffmpeg version cutly-fake-1"
  exit 0
fi
case " $* " in
  *" -f rawvideo "*) dd if=/dev/zero bs=16384 count=61 2>/dev/null; exit 0 ;;
  *" -f s16le "*) dd if=/dev/zero bs=384000 count=1 2>/dev/null; exit 0 ;;
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
