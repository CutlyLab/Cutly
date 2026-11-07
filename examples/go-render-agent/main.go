package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

type options struct {
	endpoint      string
	projectID     string
	timelineID    string
	videoAssetID  string
	videoPath     string
	audioAssetID  string
	audioPath     string
	outputPath    string
	jobID         string
	duration      int64
	width         int
	height        int
	fps           int64
	sampleRate    int
	channels      int
	deterministic int64
	timeout       time.Duration
	pollInterval  time.Duration
}

func main() {
	configuration := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, configuration.timeout)
	defer cancel()

	if err := render(ctx, configuration); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cutly render example: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var value options
	flag.StringVar(&value.endpoint, "endpoint", "http://127.0.0.1:9817/v1/agent", "Cutly Agent endpoint")
	flag.StringVar(&value.projectID, "project", "demo", "registered project identity")
	flag.StringVar(&value.timelineID, "timeline", "timeline", "timeline identity in the project")
	flag.StringVar(&value.videoAssetID, "video-asset", "video", "video asset identity in the project")
	flag.StringVar(&value.videoPath, "video", "", "absolute or working-directory-relative video path")
	flag.StringVar(&value.audioAssetID, "audio-asset", "audio", "audio asset identity in the project")
	flag.StringVar(&value.audioPath, "audio", "", "absolute or working-directory-relative audio path")
	flag.StringVar(&value.outputPath, "output", "", "absolute or working-directory-relative output .mp4 path")
	flag.StringVar(&value.jobID, "job", "example-render", "stable render job and retry identity")
	flag.Int64Var(&value.duration, "duration", 1, "whole-second render duration")
	flag.IntVar(&value.width, "width", 1280, "output width")
	flag.IntVar(&value.height, "height", 720, "output height")
	flag.Int64Var(&value.fps, "fps", 30, "integer output frames per second")
	flag.IntVar(&value.sampleRate, "sample-rate", 48_000, "output samples per second")
	flag.IntVar(&value.channels, "channels", 2, "output audio channels (1 or 2)")
	flag.Int64Var(&value.deterministic, "seed", 0, "deterministic render seed")
	flag.DurationVar(&value.timeout, "timeout", 5*time.Minute, "overall request and render timeout")
	flag.DurationVar(&value.pollInterval, "poll", 250*time.Millisecond, "status polling interval")
	flag.Parse()
	if value.videoPath == "" || value.audioPath == "" || value.outputPath == "" ||
		strings.TrimSpace(value.projectID) == "" || strings.TrimSpace(value.timelineID) == "" ||
		strings.TrimSpace(value.videoAssetID) == "" || strings.TrimSpace(value.audioAssetID) == "" ||
		strings.TrimSpace(value.jobID) == "" {
		flag.Usage()
		os.Exit(2)
	}
	if value.duration <= 0 || value.width <= 0 || value.height <= 0 || value.fps <= 0 ||
		value.sampleRate <= 0 || (value.channels != 1 && value.channels != 2) ||
		value.timeout <= 0 || value.pollInterval <= 0 {
		_, _ = fmt.Fprintln(os.Stderr, "duration, size, fps, sample rate, timeout and poll must be positive; channels must be 1 or 2")
		os.Exit(2)
	}
	return value
}

func render(ctx context.Context, configuration options) error {
	videoPath, err := cleanAbsolute(configuration.videoPath)
	if err != nil {
		return fmt.Errorf("video path: %w", err)
	}
	audioPath, err := cleanAbsolute(configuration.audioPath)
	if err != nil {
		return fmt.Errorf("audio path: %w", err)
	}
	outputPath, err := cleanAbsolute(configuration.outputPath)
	if err != nil {
		return fmt.Errorf("output path: %w", err)
	}
	if !strings.EqualFold(filepath.Ext(outputPath), ".mp4") {
		return errors.New("output path must end in .mp4")
	}

	transport, err := cutly.NewHTTPTransport(configuration.endpoint, nil)
	if err != nil {
		return err
	}
	client, err := cutly.NewClient(transport)
	if err != nil {
		return err
	}

	zero := cutly.RationalTime{Value: 0, Rate: 1}
	renderRange := cutly.TimeRange{
		Start: zero, Duration: cutly.RationalTime{Value: configuration.duration, Rate: 1},
	}
	target := cutly.RenderTargetV3{
		TimelineID: cutly.ID(configuration.timelineID),
		Width:      configuration.width,
		Height:     configuration.height,
		FrameClock: cutly.FrameClock{
			Origin: zero, FramesPerSecond: cutly.RationalTime{Value: configuration.fps, Rate: 1},
		},
		SampleClock: cutly.SampleClock{Origin: zero, SamplesPerSecond: int64(configuration.sampleRate)},
		Channels:    configuration.channels, PixelFormat: "yuv420p", ColorSpace: "bt709",
	}

	planned, err := client.RenderPlan(ctx, configuration.jobID+"-plan", configuration.projectID, renderRange, target)
	if err != nil {
		return err
	}
	if err := requireOK("plan", planned); err != nil {
		return err
	}
	planReceipt, err := cutly.DecodeRenderPlanReceipt(planned.Receipt)
	if err != nil {
		return err
	}
	if err := cutly.ValidateRenderPlanReceipt(planReceipt, renderRange, target); err != nil {
		return err
	}
	plan := planReceipt.Plan

	locators := map[string]string{
		configuration.videoAssetID: videoPath,
		configuration.audioAssetID: audioPath,
	}
	resources := make([]cutly.RenderPrepareResourceV1, 0, len(plan.Resources))
	for _, resource := range plan.Resources {
		locator, exists := locators[string(resource.AssetID)]
		if !exists {
			return fmt.Errorf("plan requires asset %q; supply its host path in this example", resource.AssetID)
		}
		resources = append(resources, cutly.RenderPrepareResourceV1{AssetID: resource.AssetID, Locator: locator})
	}
	seed := configuration.deterministic
	prepared, err := client.PrepareRender(
		ctx, configuration.jobID+"-prepare", configuration.projectID, plan, resources, &seed,
	)
	if err != nil {
		return err
	}
	if err := requireOK("prepare", prepared); err != nil {
		return err
	}
	prepareReceipt, err := cutly.DecodeRenderPrepareReceipt(prepared.Receipt)
	if err != nil {
		return err
	}
	if err := cutly.ValidateRenderPrepareReceipt(prepareReceipt, plan, resources, &seed); err != nil {
		return err
	}
	manifest := prepareReceipt.ExecutionManifest

	ready, err := client.PreflightRender(ctx, configuration.jobID+"-preflight", configuration.projectID, plan, manifest)
	if err != nil {
		return err
	}
	if err := requireOK("preflight", ready); err != nil {
		return err
	}
	preflightReceipt, err := cutly.DecodeRenderPreflightReceipt(ready.Receipt)
	if err != nil {
		return err
	}
	if err := cutly.ValidateRenderPreflightReceipt(preflightReceipt, plan, manifest); err != nil {
		return err
	}

	artifactURI := (&url.URL{Scheme: "file", Path: outputPath}).String()
	job, err := cutly.BuildRenderJobV1(
		configuration.jobID, configuration.jobID+"-once", plan, manifest, artifactURI,
	)
	if err != nil {
		return err
	}
	submitted, err := client.SubmitRender(ctx, configuration.jobID+"-submit", configuration.projectID, job)
	if err != nil {
		return err
	}
	if err := requireOK("submit", submitted); err != nil {
		return err
	}
	snapshot, err := cutly.DecodeRenderJobSnapshotReceipt(submitted.Receipt)
	if err != nil {
		return err
	}
	if err := cutly.ValidateRenderJobSnapshotReceipt(snapshot, job); err != nil {
		return err
	}

	for snapshot.State != "complete" {
		if snapshot.State == "failed" || snapshot.State == "canceled" {
			return fmt.Errorf("render %s: class=%q message=%q", snapshot.State, snapshot.FailureClass, snapshot.FailureMessage)
		}
		if err := wait(ctx, configuration.pollInterval); err != nil {
			return err
		}
		status, err := client.RenderStatus(
			ctx, fmt.Sprintf("%s-status-%d", configuration.jobID, snapshot.LastSequence),
			configuration.projectID, configuration.jobID,
		)
		if err != nil {
			return err
		}
		if err := requireOK("status", status); err != nil {
			return err
		}
		snapshot, err = cutly.DecodeRenderJobSnapshotReceipt(status.Receipt)
		if err != nil {
			return err
		}
		if err := cutly.ValidateRenderJobSnapshotReceipt(snapshot, job); err != nil {
			return err
		}
	}
	if snapshot.Artifact == nil {
		return errors.New("complete render has no artifact")
	}
	encoded, err := json.MarshalIndent(snapshot.Artifact, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

func cleanAbsolute(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if strings.ContainsRune(absolute, 0) || filepath.Dir(absolute) == absolute {
		return "", errors.New("path must be a clean non-root path")
	}
	return absolute, nil
}

func requireOK(action string, result cutly.Result) error {
	if result.OK {
		return nil
	}
	parts := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		parts = append(parts, fmt.Sprintf("%s at %s: %s", diagnostic.Code, diagnostic.Path, diagnostic.Message))
	}
	return fmt.Errorf("%s rejected in state %q: %s", action, result.State, strings.Join(parts, "; "))
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
