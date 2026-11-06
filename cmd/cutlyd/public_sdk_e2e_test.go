package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

func TestPublicGoSDKHTTPRenderEndToEnd(t *testing.T) {
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobe, ffprobeErr := exec.LookPath("ffprobe")
	if ffmpegErr != nil || ffprobeErr != nil {
		t.Skip("FFmpeg and FFprobe are required for the public SDK render acceptance test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration := renderAcceptanceConfig(t, root, ffmpeg, ffprobe)
	videoPath := filepath.Join(configuration.render.inputRoots[0], "public-video.mp4")
	audioPath := filepath.Join(configuration.render.inputRoots[0], "public-audio.mov")
	generateAcceptanceMedia(t, ctx, ffmpeg, videoPath, audioPath)

	videoHash := fileSHA256(t, videoPath)
	audioHash := fileSHA256(t, audioPath)
	document, err := cutly.DecodeDocumentV1([]byte(fmt.Sprintf(`{
		"schema_version":1,
		"id":"public-sdk-document",
		"revision":1,
		"assets":[
			{"id":"audio","kind":"audio","uri":"public-audio.mov","content_hash":%q,"duration":{"value":1,"rate":1}},
			{"id":"video","kind":"video","uri":"public-video.mp4","content_hash":%q,"duration":{"value":1,"rate":1}}
		],
		"timelines":[{"id":"timeline","tracks":[
			{"id":"audio-track","kind":"audio","clips":[{"id":"audio-clip","asset_id":"audio","timeline_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"source_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}}}]},
			{"id":"video-track","kind":"video","clips":[{"id":"video-clip","asset_id":"video","timeline_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}},"source_range":{"start":{"value":0,"rate":1},"duration":{"value":1,"rate":1}}}]}
		]}]
	}`, audioHash, videoHash)))
	if err != nil {
		t.Fatalf("decode public project document: %v", err)
	}
	if _, err := cutly.CreateLocalProjectV1(ctx, configuration.projectDir, document); err != nil {
		t.Fatalf("create public project: %v", err)
	}

	application, err := buildApplication(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			t.Errorf("close application: %v", closeErr)
		}
	}()
	server := httptest.NewServer(newMux(application.handler))
	defer server.Close()

	transport, err := cutly.NewHTTPTransport(server.URL+"/v1/agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := cutly.NewClient(transport)
	if err != nil {
		t.Fatal(err)
	}

	zero := cutly.RationalTime{Value: 0, Rate: 1}
	one := cutly.RationalTime{Value: 1, Rate: 1}
	renderRange := cutly.TimeRange{Start: zero, Duration: one}
	target := cutly.RenderTargetV3{
		TimelineID: "timeline", Width: 64, Height: 64,
		FrameClock: cutly.FrameClock{
			Origin: zero, FramesPerSecond: cutly.RationalTime{Value: 30, Rate: 1},
		},
		SampleClock: cutly.SampleClock{Origin: zero, SamplesPerSecond: 48_000},
		Channels:    2, PixelFormat: "yuv420p", ColorSpace: "bt709",
	}
	plannedResult, err := client.RenderPlan(ctx, "public-plan", "project", renderRange, target)
	planned := requirePublicSDKResult(t, plannedResult, err)
	planReceipt, err := cutly.DecodeRenderPlanReceipt(planned.Receipt)
	if err != nil {
		t.Fatalf("decode public plan receipt: %v", err)
	}
	if err := cutly.ValidateRenderPlanReceipt(planReceipt, renderRange, target); err != nil {
		t.Fatalf("validate public plan receipt: %v", err)
	}
	plan := planReceipt.Plan

	paths := map[string]string{"audio": audioPath, "video": videoPath}
	resources := make([]cutly.RenderPrepareResourceV1, 0, len(plan.Resources))
	for _, resource := range plan.Resources {
		locator, exists := paths[string(resource.AssetID)]
		if !exists {
			t.Fatalf("plan contains unexpected resource %q", resource.AssetID)
		}
		resources = append(resources, cutly.RenderPrepareResourceV1{AssetID: resource.AssetID, Locator: locator})
	}
	seed := int64(1)
	preparedResult, err := client.PrepareRender(ctx, "public-prepare", "project", plan, resources, &seed)
	prepared := requirePublicSDKResult(t, preparedResult, err)
	prepareReceipt, err := cutly.DecodeRenderPrepareReceipt(prepared.Receipt)
	if err != nil {
		t.Fatalf("decode public prepare receipt: %v", err)
	}
	if err := cutly.ValidateRenderPrepareReceipt(prepareReceipt, plan, resources, &seed); err != nil {
		t.Fatalf("validate public prepare receipt: %v", err)
	}
	manifest := prepareReceipt.ExecutionManifest

	preflightResult, err := client.PreflightRender(ctx, "public-preflight", "project", plan, manifest)
	preflight := requirePublicSDKResult(t, preflightResult, err)
	preflightReceipt, err := cutly.DecodeRenderPreflightReceipt(preflight.Receipt)
	if err != nil {
		t.Fatalf("decode public preflight receipt: %v", err)
	}
	if err := cutly.ValidateRenderPreflightReceipt(preflightReceipt, plan, manifest); err != nil {
		t.Fatalf("validate public preflight receipt: %v", err)
	}

	artifactURI := (&url.URL{Scheme: "file", Path: filepath.Join(configuration.render.outputDir, "public-sdk.mp4")}).String()
	job, err := cutly.BuildRenderJobV1("public-sdk-job", "public-sdk-job-once", plan, manifest, artifactURI)
	if err != nil {
		t.Fatalf("build public render job: %v", err)
	}
	queuedResult, err := client.SubmitRender(ctx, "public-submit", "project", job)
	queued := requirePublicSDKResult(t, queuedResult, err)
	queuedSnapshot, err := cutly.DecodeRenderJobSnapshotReceipt(queued.Receipt)
	if err != nil || queuedSnapshot.Job.ID != job.ID {
		t.Fatalf("decode queued snapshot: snapshot=%+v error=%v", queuedSnapshot, err)
	}

	var complete cutly.RenderJobSnapshotV1
	for attempt := 0; ; attempt++ {
		statusResult, statusErr := client.RenderStatus(ctx, fmt.Sprintf("public-status-%d", attempt), "project", job.ID)
		status := requirePublicSDKResult(t, statusResult, statusErr)
		snapshot, decodeErr := cutly.DecodeRenderJobSnapshotReceipt(status.Receipt)
		if decodeErr != nil {
			t.Fatalf("decode public status receipt: %v", decodeErr)
		}
		switch string(snapshot.State) {
		case "complete":
			complete = snapshot
		case "failed", "canceled":
			t.Fatalf("public render reached %s: %+v", snapshot.State, snapshot)
		}
		if complete.Artifact != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for public render: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	if complete.Artifact.URI != job.ArtifactURI ||
		complete.Artifact.ExecutionManifestFingerprint != manifest.Fingerprint ||
		complete.Artifact.FrameCount != job.FrameRange.Count ||
		complete.Artifact.SampleCount != job.SampleRange.Count {
		t.Fatalf("public artifact binding = %+v", complete.Artifact)
	}
	assertPublishedEvidence(t, job, manifest.Fingerprint)
}

func requirePublicSDKResult(t *testing.T, result cutly.Result, err error) cutly.Result {
	t.Helper()
	if err != nil {
		t.Fatalf("public SDK call: %v", err)
	}
	if !result.OK {
		t.Fatalf("public SDK business rejection: state=%s diagnostics=%+v", result.State, result.Diagnostics)
	}
	return result
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
