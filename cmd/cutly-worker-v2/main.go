// Command cutly-worker-v2 executes one native Document V2 partition in a real
// child. Host policy is a private inherited regular descriptor (fd 3), not stdin.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/CutlyLab/Cutly/render/worker"
)

func main() { os.Exit(run()) }

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "worker-v2: arguments are not accepted")
		return 2
	}
	alive := os.NewFile(4, "host-liveness")
	stopWatch, err := worker.WatchParentV2(ctx, cancel, alive)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker-v2: trusted parent liveness descriptor required")
		return 2
	}
	defer stopWatch()
	payload := os.NewFile(5, "host-payload-spool")
	if payload == nil {
		fmt.Fprintln(os.Stderr, "worker-v2: trusted payload descriptor required")
		return 2
	}
	defer payload.Close()
	err = worker.ServeStreamV2(ctx, os.Stdin, os.Stdout, payload, func(ctx context.Context) (worker.ReconstructV2, error) {
		file := os.NewFile(3, "host-launch-config")
		if file == nil {
			return nil, worker.ErrRunner
		}
		defer file.Close()
		config, err := worker.ReadLaunchConfigV2(file)
		if err != nil {
			return nil, err
		}
		return worker.NewLaunchReconstructV2(ctx, config)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker-v2: request rejected or execution failed")
		return 1
	}
	return 0
}
