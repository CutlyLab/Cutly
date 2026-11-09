package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/CutlyLab/Cutly/core/videoedit"
	api "github.com/CutlyLab/Cutly/render/api"
)

const ChunkAssemblerSupervisedEncoderProfileV2 = "cutly.ffmpeg.rgba8-pcm16.h264-aac-mp4.supervised.document-v2.v2"
const ChunkAssemblerSupervisorProfileV2 = "cutly.ffmpeg.parent-liveness-supervisor.document-v2.v2"

// ChunkAssemblerSupervisedCapabilitiesV2 adds the independently measured native
// supervisor and its host-resolved dependency closure to assembler build identity.
// It does not change FFmpegBuild's existing executable/runtime fingerprint.
func ChunkAssemblerSupervisedCapabilitiesV2(sdk, supervisor, closure string) (api.BackendCapabilitiesV2, error) {
	if !canonicalSHA256(supervisor) || !canonicalSHA256(closure) {
		return api.BackendCapabilitiesV2{}, ErrConfiguration
	}
	caps, err := ChunkAssemblerCapabilitiesV2(sdk)
	if err != nil {
		return caps, err
	}
	raw, err := videoedit.CanonicalJSON(struct{ Profile, Backend, Executable, Dependencies string }{ChunkAssemblerSupervisorProfileV2, caps.BuildFingerprint, supervisor, closure})
	if err != nil {
		return caps, err
	}
	sum := sha256.Sum256(raw)
	caps.BuildFingerprint = "sha256:" + hex.EncodeToString(sum[:])
	return caps, nil
}

func (b *ChunkAssemblerV2) verifySupervisorV2(ctx context.Context) error {
	if b.supervisor == "" {
		return nil
	}
	measured, _, err := hashRegularFile(ctx, b.supervisor)
	if err != nil {
		return err
	}
	if measured != b.supervisorDigest {
		return fmt.Errorf("%w: supervisor executable changed", ErrConfiguration)
	}
	return nil
}

// superviseCommandV2 rewrites an already-compiled descriptor-only FFmpeg command.
// No request string is interpreted as code. The write end is held exclusively by
// this process, never inherited by the helper/FFmpeg; daemon SIGKILL therefore
// closes it, making the native helper kill its entire isolated process group.
func (b *ChunkAssemblerV2) superviseCommandV2(ctx context.Context, original *exec.Cmd) (*exec.Cmd, func() error, error) {
	if b.supervisor == "" {
		return original, func() error { return nil }, nil
	}
	if err := b.verifySupervisorV2(ctx); err != nil {
		return nil, nil, err
	}
	runtimeHash, _, err := hashRegularFile(ctx, b.io.executable)
	if err != nil {
		return nil, nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	args := []string{"--runtime", b.io.executable, "--runtime-sha256", runtimeHash, "--files", strconv.Itoa(len(original.ExtraFiles)), "--max-seconds", "3600", "--"}
	args = append(args, original.Args[1:]...)
	cmd := newCommandContext(ctx, b.supervisor, args...)
	cmd.Env = append([]string(nil), original.Env...)
	cmd.Stdout = original.Stdout
	cmd.Stderr = original.Stderr
	cmd.Stdin = original.Stdin
	cmd.ExtraFiles = append(append([]*os.File(nil), original.ExtraFiles...), reader)
	cleanup := func() error { return errors.Join(writer.Close(), reader.Close()) }
	return cmd, cleanup, nil
}
