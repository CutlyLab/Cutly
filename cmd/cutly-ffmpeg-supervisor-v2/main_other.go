//go:build !darwin && !linux

// Command cutly-ffmpeg-supervisor-v2 is unavailable where process-group and
// inherited-descriptor semantics cannot satisfy the native supervisor contract.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "cutly-ffmpeg-supervisor-v2: unsupported platform")
	os.Exit(2)
}
