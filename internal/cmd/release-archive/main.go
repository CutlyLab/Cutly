// release-archive builds and validates one local command archive. It never
// publishes a release or starts a daemon/media runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/CutlyLab/Cutly/internal/releasearchive"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, releasearchive.Build))
}

type builder func(context.Context, releasearchive.Options) (releasearchive.Result, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, build builder) int {
	flags := flag.NewFlagSet("release-archive", flag.ContinueOnError)
	flags.SetOutput(stderr)
	opts := releasearchive.Options{}
	flags.StringVar(&opts.Repo, "repo", "", "repository directory containing the SDK version constant (required)")
	flags.StringVar(&opts.Target, "target", "", "release target GOOS/GOARCH (required)")
	flags.StringVar(&opts.Output, "output", "", "existing local output directory; exact archive name must not exist (required)")
	flags.StringVar(&opts.ExpectedVersion, "version", "", "optional expected SDK version; mismatch rejects")
	flags.StringVar(&opts.MediaRuntimeBundleFingerprint, "media-runtime-bundle-fingerprint", "", "packaged signed runtime bundle fingerprint")
	flags.StringVar(&opts.MediaRuntimeTrustedKeys, "media-runtime-trusted-keys", "", "packaged key-id=ed25519-public-key trust pins")
	flags.BoolVar(&opts.Smoke, "smoke", false, "run only host-target cutly version and cutlyd -version")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || opts.Repo == "" || opts.Target == "" || opts.Output == "" {
		fmt.Fprintln(stderr, "explicit --repo, --target and --output are required; positional arguments are not accepted")
		return 2
	}
	result, err := build(ctx, opts)
	if err != nil {
		fmt.Fprintln(stderr, "release-archive:", err)
		return 1
	}
	fmt.Fprintln(stdout, result.Archive)
	return 0
}
