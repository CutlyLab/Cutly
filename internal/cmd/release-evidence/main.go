// release-evidence is explicitly opt-in: no profile means no tests execute.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/CutlyLab/Cutly/internal/releaseevidence"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, releaseevidence.NewRunner()))
}
func run(ctx context.Context, args []string, stderr io.Writer, runner releaseevidence.Runner) int {
	if len(args) > 16 {
		fmt.Fprintln(stderr, "too many arguments")
		return 2
	}
	for _, a := range args {
		if len(a) > 4096 {
			fmt.Fprintln(stderr, "argument exceeds 4096 bytes")
			return 2
		}
	}
	flags := flag.NewFlagSet("release-evidence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profile := flags.String("profile", "", "required: smoke or long (long: 100 fault rounds, 4GiB synthetic stream, RSS, ten real-media repeats, >=1h repeated recovery jobs)")
	output := flags.String("output", "", "required new JSON file, never overwritten")
	repo := flags.String("repo", ".", "repository root")
	timeout := flags.Duration("command-timeout", 0, "per-command bound: 1s..10m (default 5m)")
	budget := flags.Duration("budget", 0, "overall bound: smoke default15m, long default100m, maximum105m")
	logs := flags.Int("log-bytes", 65536, "capture bytes per stream per command: 1024..262144 (2MiB aggregate retained)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *profile == "" || *output == "" {
		fmt.Fprintln(stderr, "-profile and -output are required; positional arguments are forbidden")
		return 2
	}
	config := releaseevidence.Defaults(*profile)
	config.Repo = *repo
	config.LogBytes = *logs
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "command-timeout" {
			config.CommandTimeout = *timeout
		}
		if f.Name == "budget" {
			config.Budget = *budget
		}
	})
	report, err := runner.RunToFile(ctx, config, *output)
	if err != nil {
		fmt.Fprintf(stderr, "release evidence %s: %v\n", report.Status, err)
		return 1
	}
	fmt.Fprintf(stderr, "release evidence %s: %s\n", report.Status, *output)
	return 0
}
