package ffmpegbackend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/CutlyLab/Cutly/render/evidence"
)

// BenchmarkM1DecodedEvidenceStreaming exercises the production pipe reader and SHA-256 evidence
// path without retaining decoded bytes. The default 64 MiB keeps local runs quick. Nightly/soak
// jobs can set CUTLY_M1_STREAM_BENCH_MIB (for example 4096) to verify allocation/RSS behavior over
// a much longer stream:
//
//	CUTLY_M1_STREAM_BENCH_MIB=4096 go test -run '^$' -bench M1DecodedEvidenceStreaming -benchmem ./render/ffmpegbackend
func BenchmarkM1DecodedEvidenceStreaming(b *testing.B) {
	if !platformSupportsSecureDescriptors() {
		b.Skip("M1 secure render profile is unavailable on this platform")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		b.Skip("POSIX shell is unavailable")
	}
	megabytes := int64(64)
	if raw := os.Getenv("CUTLY_M1_STREAM_BENCH_MIB"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed <= 0 || parsed > 65536 {
			b.Fatalf("CUTLY_M1_STREAM_BENCH_MIB must be in 1..65536, got %q", raw)
		}
		megabytes = parsed
	}
	root := b.TempDir()
	executable := filepath.Join(root, "stream-decoder")
	script := fmt.Sprintf("#!/bin/sh\ndd if=/dev/zero bs=1048576 count=%d 2>/dev/null\n", megabytes)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		b.Fatal(err)
	}
	encodedPath := filepath.Join(root, "encoded.mp4")
	if err := os.WriteFile(encodedPath, []byte("benchmark input"), 0o600); err != nil {
		b.Fatal(err)
	}
	encoded, err := os.Open(encodedPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = encoded.Close() })
	backend := &Backend{
		executable: executable, maxStderrBytes: 4096,
		runtimeEnvironment: controlledRuntimeEnvironment(),
	}
	byteCount := megabytes * 1024 * 1024
	request := decodedRequest{
		file: encoded, unit: evidence.EvidenceSamples, unitBytes: 4,
		first: 0, expectedCount: byteCount / 4, domain: "cutly.benchmark.pcm-s16le.stereo.v1",
	}
	b.SetBytes(byteCount)
	b.ReportAllocs()
	b.ReportMetric(float64(byteCount), "decoded-bytes/op")
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		sequence, count, err := backend.decodeSequence(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		if count != request.expectedCount || sequence.Range.Count != request.expectedCount {
			b.Fatalf("decoded count = %d/%d, want %d", count, sequence.Range.Count, request.expectedCount)
		}
	}
}
