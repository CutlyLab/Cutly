//go:build !windows && !js

package ffmpegbackend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/CutlyLab/Cutly/render/evidence"
)

const m1RSSHelperMiBEnv = "CUTLY_M1_RSS_HELPER_MIB"

// TestM1DecodedEvidenceRSSDoesNotScaleWithDuration runs the production streaming evidence reader
// in fresh processes so each measurement has an independent MaxRSS high-water mark. Increasing
// the decoded stream by 16x must not cause a proportional heap/RSS increase: only the fixed pipe,
// hashing state and 128 KiB reader buffer may remain resident.
func TestM1DecodedEvidenceRSSDoesNotScaleWithDuration(t *testing.T) {
	if _, helper := os.LookupEnv(m1RSSHelperMiBEnv); helper {
		t.Skip("parent-only RSS comparison")
	}
	shortRSS := runM1RSSHelper(t, 16)
	longRSS := runM1RSSHelper(t, 256)
	const allowance = uint64(32 * 1024 * 1024)
	if longRSS > shortRSS+allowance {
		t.Fatalf("decoded evidence RSS scales with duration: 16MiB=%d bytes, 256MiB=%d bytes, allowance=%d bytes", shortRSS, longRSS, allowance)
	}
	t.Logf("decoded evidence MaxRSS: 16MiB=%d bytes, 256MiB=%d bytes", shortRSS, longRSS)
}

// TestM1DecodedEvidenceRSSHelper is invoked only by TestM1DecodedEvidenceRSSDoesNotScaleWithDuration
// in a fresh copy of this test binary. Keeping it as a real test exercises exactly the same
// decodeSequence implementation as render evidence generation.
func TestM1DecodedEvidenceRSSHelper(t *testing.T) {
	raw, helper := os.LookupEnv(m1RSSHelperMiBEnv)
	if !helper {
		t.Skip("RSS helper is launched by the parent acceptance test")
	}
	megabytes, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || megabytes <= 0 || megabytes > 4096 {
		t.Fatalf("invalid %s=%q", m1RSSHelperMiBEnv, raw)
	}
	root := t.TempDir()
	decoder := filepath.Join(root, "stream-decoder")
	script := fmt.Sprintf("#!/bin/sh\ndd if=/dev/zero bs=1048576 count=%d 2>/dev/null\n", megabytes)
	if err := os.WriteFile(decoder, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	encodedPath := filepath.Join(root, "encoded.mp4")
	if err := os.WriteFile(encodedPath, []byte("bounded decoded evidence input"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.Open(encodedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer encoded.Close()
	byteCount := megabytes * 1024 * 1024
	backend := &Backend{
		executable: decoder, maxStderrBytes: 4096,
		runtimeEnvironment: controlledRuntimeEnvironment(),
	}
	sequence, count, err := backend.decodeSequence(context.Background(), decodedRequest{
		file: encoded, unit: evidence.EvidenceSamples, unitBytes: 4,
		first: 0, expectedCount: byteCount / 4,
		domain: "cutly.acceptance.pcm-s16le.stereo.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != byteCount/4 || sequence.Range.Count != count || len(sequence.Chunks) != 1 {
		t.Fatalf("decoded sequence = count:%d range:%+v chunks:%d", count, sequence.Range, len(sequence.Chunks))
	}
	rss, err := currentProcessMaxRSSBytes()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("CUTLY_M1_MAX_RSS_BYTES=%d\n", rss)
}

func runM1RSSHelper(t *testing.T, megabytes int64) uint64 {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestM1DecodedEvidenceRSSHelper$", "-test.count=1")
	command.Env = append(os.Environ(), m1RSSHelperMiBEnv+"="+strconv.FormatInt(megabytes, 10))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%dMiB RSS helper: %v\n%s", megabytes, err, output)
	}
	const marker = "CUTLY_M1_MAX_RSS_BYTES="
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, marker) {
			continue
		}
		value, parseErr := strconv.ParseUint(strings.TrimPrefix(line, marker), 10, 64)
		if parseErr != nil || value == 0 {
			t.Fatalf("parse RSS helper output %q: %v", line, parseErr)
		}
		return value
	}
	t.Fatalf("RSS helper omitted measurement:\n%s", output)
	return 0
}

func currentProcessMaxRSSBytes() (uint64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	if usage.Maxrss <= 0 {
		return 0, fmt.Errorf("invalid MaxRSS %d", usage.Maxrss)
	}
	value := uint64(usage.Maxrss)
	// Darwin reports bytes; Linux and the BSDs supported by the Go syscall package report KiB.
	if runtime.GOOS != "darwin" {
		value *= 1024
	}
	return value, nil
}
