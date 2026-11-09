package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/internal/releasearchive"
)

func TestCLIRequiresExplicitInputs(t *testing.T) {
	for _, args := range [][]string{nil, {"--repo", "repo"}, {"--repo", "repo", "--target", "linux/amd64"}, {"--repo", "repo", "--target", "linux/amd64", "--output", "out", "positional"}, {"--unknown"}} {
		var out, stderr bytes.Buffer
		called := false
		code := run(context.Background(), args, &out, &stderr, func(context.Context, releasearchive.Options) (releasearchive.Result, error) {
			called = true
			return releasearchive.Result{}, nil
		})
		if code != 2 || called || out.Len() != 0 {
			t.Fatalf("args %q code=%d called=%v out=%q", args, code, called, out.String())
		}
	}
}

func TestCLIForwardsStableFlags(t *testing.T) {
	var out, stderr bytes.Buffer
	want := releasearchive.Options{Repo: "repo", Output: "out", Target: "darwin/arm64", ExpectedVersion: "1.2.3", Smoke: true}
	args := []string{"--repo", "repo", "--output", "out", "--target", "darwin/arm64", "--version", "1.2.3", "--smoke"}
	code := run(context.Background(), args, &out, &stderr, func(_ context.Context, opts releasearchive.Options) (releasearchive.Result, error) {
		if !reflect.DeepEqual(opts, want) {
			t.Fatalf("options %+v want %+v", opts, want)
		}
		return releasearchive.Result{Archive: "/out/cutly-1.2.3-darwin-arm64.tar.gz"}, nil
	})
	if code != 0 || out.String() != "/out/cutly-1.2.3-darwin-arm64.tar.gz\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestCLIFailureDoesNotPrintSuccess(t *testing.T) {
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"--repo", "repo", "--output", "out", "--target", "linux/amd64"}, &out, &stderr, func(context.Context, releasearchive.Options) (releasearchive.Result, error) {
		return releasearchive.Result{}, errors.New("build failed")
	})
	if code != 1 || out.Len() != 0 || stderr.String() != "release-archive: build failed\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}
