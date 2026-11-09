package preparev2

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

func TestEnvironmentAuthorizationPinsBeforeResources(t *testing.T) {
	calls := 0
	verifier := func(_ context.Context, _ string, claim renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		calls++
		return claim, nil // Test observation seam, not a production inspector.
	}
	host, doc, plan, draft := setup(t, "mixed", verifier)
	manifest, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	calls = 0
	if err := host.CheckEnvironmentPins(context.Background(), manifest); err != nil || calls != 0 {
		t.Fatalf("pin precheck touched resources: %v, calls=%d", err, calls)
	}
	for _, field := range []string{"provider", "compiler", "evaluator", "backend", "media"} {
		t.Run(field, func(t *testing.T) {
			altered := manifest
			switch field {
			case "provider":
				altered.ProviderBuild = hash("a")
			case "compiler":
				altered.CompilerBuild = hash("a")
			case "evaluator":
				altered.EvaluatorBuild = hash("a")
			case "backend":
				altered.Backend.BuildFingerprint = hash("a")
			case "media":
				altered.MediaRuntime.FFmpegBuild = hash("a")
			}
			altered, err = renderapi.FreezeExecutionManifestV2(context.Background(), plan, altered)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.CheckEnvironmentPins(context.Background(), altered); !errors.Is(err, ErrAuthorization) {
				t.Fatalf("host pin accepted: %v", err)
			}
			got, err := host.AuthorizeEnvironment(context.Background(), doc, plan, altered)
			if err == nil || got.Valid() || calls != 0 {
				t.Fatalf("persisted pin authorized or resource observed: %v calls=%d", err, calls)
			}
		})
	}
	got, err := host.AuthorizeEnvironment(context.Background(), doc, plan, manifest)
	if err != nil || !got.Valid() || calls != len(manifest.Resources) {
		t.Fatalf("valid environment: %v calls=%d", err, calls)
	}
	calls = 0
	doc.Revision++
	if got, err := host.AuthorizeEnvironment(context.Background(), doc, plan, manifest); err == nil || got.Valid() || calls != 0 {
		t.Fatal("different document reached resource verification", err)
	}
}

func TestEnvironmentAuthorizationOwnedConcurrent(t *testing.T) {
	host, doc, plan, draft := setup(t, "gap", nil)
	manifest, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	got, err := host.AuthorizeEnvironment(context.Background(), doc, plan, manifest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderapi.CanonicalExecutionManifestV2(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ProviderBuild = hash("a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := got.Canonical()
			if err != nil || !bytes.Equal(raw, want) {
				t.Errorf("canonical ownership: %v", err)
				return
			}
			raw[0] = '!'
			copy, err := got.Manifest()
			if err != nil {
				t.Error(err)
				return
			}
			copy.ProviderBuild = "mutated"
		}()
	}
	wg.Wait()
	var zero AuthorizedManifest
	if zero.Valid() {
		t.Fatal("zero authorized manifest valid")
	}
	if _, err := zero.Manifest(); err == nil {
		t.Fatal("zero accessor succeeded")
	}
}

func TestEnvironmentAuthorizationFailureZero(t *testing.T) {
	host, doc, plan, draft := setup(t, "mixed", func(_ context.Context, _ string, claim renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
		return claim, nil
	})
	manifest, err := host.Freeze(context.Background(), doc, plan, draft)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled, panicContext{context.Background()}} {
		if err := host.CheckEnvironmentPins(ctx, manifest); err == nil {
			t.Fatal("bad context passed pin check")
		}
		if got, err := host.AuthorizeEnvironment(ctx, doc, plan, manifest); err == nil || got.Valid() {
			t.Fatal("bad context published authorization")
		}
	}
	var nilHost *Authority
	if err := nilHost.CheckEnvironmentPins(context.Background(), manifest); err == nil {
		t.Fatal("nil authority accepted")
	}
	if got, err := nilHost.AuthorizeEnvironment(context.Background(), doc, plan, manifest); err == nil || got.Valid() {
		t.Fatal("nil authority published authorization")
	}
	for _, mode := range []string{"missing", "panic", "drift", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			copy := *host
			copy.verifyResource = nil
			if mode != "missing" {
				copy.verifyResource = func(_ context.Context, _ string, claim renderapi.MaterializedResourceV2) (renderapi.MaterializedResourceV2, error) {
					switch mode {
					case "panic":
						panic("inspector failure")
					case "drift":
						claim.SizeBytes++
					case "cancel":
						cancel()
					}
					return claim, nil
				}
			}
			if got, err := copy.AuthorizeEnvironment(ctx, doc, plan, manifest); err == nil || got.Valid() {
				t.Fatal("resource failure published authorization")
			}
		})
	}
}
