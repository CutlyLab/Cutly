package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/evidence"
)

// anonymousV2 is disk backed and detached before any untrusted source callback
// or child process runs. Memory is bounded by buffers plus a single video frame.
func (b *ChunkAssemblerV2) anonymousV2() (*os.File, error) {
	f, n, err := b.io.createOutputTemp(".cutly-chunks-v2-", ".raw")
	if err != nil {
		return nil, err
	}
	if err = b.removeOwnedV2(n, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

type contextReaderV2 struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReaderV2) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

// copyChunkV2 consumes EOF as well as the declared length and verifies even an
// untrusted source implementation. Close is always observed, also after panic.
func copyChunkV2(ctx context.Context, d evidence.ChunkDescriptorV2, source ChunkSourceV2, dst io.Writer) (err error) {
	var r io.ReadCloser
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("%w: chunk callback panic: %v", ErrContent, x)
		}
	}()
	r, err = source.Open(ctx, d)
	if err != nil {
		if r != nil {
			err = errors.Join(err, r.Close())
		}
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: nil CAS reader", ErrContent)
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(dst, h), io.LimitReader(contextReaderV2{ctx, r}, d.SizeBytes), make([]byte, 64<<10))
	if err != nil {
		return err
	}
	if n != d.SizeBytes {
		return fmt.Errorf("%w: short CAS payload", ErrContent)
	}
	var extra [1]byte
	nExtra, e := contextReaderV2{ctx, r}.Read(extra[:])
	if nExtra != 0 || !errors.Is(e, io.EOF) {
		return fmt.Errorf("%w: extra CAS bytes or missing EOF: %v", ErrContent, e)
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.ContentHash {
		return fmt.Errorf("%w: CAS SHA-256 mismatch", ErrContent)
	}
	return ctx.Err()
}

func (b *ChunkAssemblerV2) stageDomainV2(ctx context.Context, ds []evidence.ChunkDescriptorV2, source ChunkSourceV2) (out *os.File, err error) {
	if len(ds) == 0 {
		return nil, nil
	}
	f, err := b.anonymousV2()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	for _, d := range ds {
		if err = copyChunkV2(ctx, d, source, f); err != nil {
			return nil, err
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return f, nil
}

func (b *ChunkAssemblerV2) rgba8V2(ctx context.Context, raw *os.File, r evidence.BindingV2) (out *os.File, err error) {
	if raw == nil {
		return nil, nil
	}
	out, err = b.anonymousV2()
	if err != nil {
		return nil, err
	}
	owned := out
	defer func() {
		if err != nil {
			err = errors.Join(err, owned.Close())
			out = nil
		}
	}()
	w, h := int(r.Output.Width), int(r.Output.Height)
	pixels := make([]cpucompositor.Pixel, w*h)
	row := make([]byte, w*8)
	for frame := int64(0); frame < r.FrameRange.Count; frame++ {
		for y := 0; y < h; y++ {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if _, err = io.ReadFull(raw, row); err != nil {
				return nil, err
			}
			for x := 0; x < w; x++ {
				p := row[x*8:]
				pixels[y*w+x] = cpucompositor.Pixel{R: binary.LittleEndian.Uint16(p), G: binary.LittleEndian.Uint16(p[2:]), B: binary.LittleEndian.Uint16(p[4:]), A: binary.LittleEndian.Uint16(p[6:])}
			}
		}
		surface, e := cpucompositor.NewSurfaceFromPixels(w, h, pixels)
		if e != nil {
			return nil, e
		}
		rgba, e := color.ConvertSurfaceToRGBA8(ctx, surface, color.DefaultOutputLimits())
		if e != nil {
			return nil, e
		}
		if err = rgba.Frame.WriteCanonical(out); err != nil {
			return nil, err
		}
	}
	_, err = out.Seek(0, io.SeekStart)
	return out, err
}

func (b *ChunkAssemblerV2) encodeV2(ctx context.Context, r evidence.BindingV2, video, audio, output *os.File) error {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-y", "-cpuflags", "0", "-filter_threads", "1", "-filter_complex_threads", "1"}
	var files []*os.File
	if video != nil {
		args = append(args, "-f", "rawvideo", "-pixel_format", "rgba", "-video_size", fmt.Sprintf("%dx%d", r.Output.Width, r.Output.Height), "-framerate", rationalArgument(r.Output.FrameClock.FramesPerSecond), "-protocol_whitelist", "file,fd", "-i", "<video>")
		files = append(files, video)
	}
	if audio != nil {
		args = append(args, "-f", "s16le", "-ar", strconv.FormatInt(r.Output.SampleRate, 10), "-ac", strconv.Itoa(int(r.Output.Channels)), "-protocol_whitelist", "file,fd", "-i", "<audio>")
		files = append(files, audio)
	}
	if video != nil {
		args = append(args, "-map", "0:v:0", "-frames:v", strconv.FormatInt(r.FrameRange.Count, 10), "-vf", "scale=in_range=full:out_range=tv:out_color_matrix=bt709:flags=accurate_rnd+bitexact,format=yuv420p", "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-threads:v", "1", "-pix_fmt", "yuv420p", "-color_primaries", "bt709", "-color_trc", "iec61966-2-1", "-colorspace", "bt709", "-color_range", "tv")
	}
	if audio != nil {
		i := 0
		if video != nil {
			i = 1
		}
		args = append(args, "-map", fmt.Sprintf("%d:a:0", i), "-c:a", "aac", "-b:a", "192k", "-threads:a", "1", "-ar", strconv.FormatInt(r.Output.SampleRate, 10), "-ac", strconv.Itoa(int(r.Output.Channels)))
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-metadata", "creation_time=1970-01-01T00:00:00Z", "-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact", "-f", "mp4", "<output>")
	cmd := newCommandContext(ctx, b.io.executable, args...)
	cmd.Env = append([]string(nil), b.io.runtimeEnvironment...)
	endpoints := make([]commandFileEndpoint, 0, len(files))
	for _, f := range files {
		endpoints = append(endpoints, attachCommandFile(cmd, f))
	}
	if err := replaceCommandInputs(cmd, endpoints); err != nil {
		return err
	}
	setCommandOutput(cmd, attachCommandFile(cmd, output))
	stderr := newBoundedBuffer(b.io.maxStderrBytes)
	cmd.Stderr = stderr
	cmd, cleanup, err := b.superviseCommandV2(ctx, cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || stderr.exceeded {
		return fmt.Errorf("%w: V2 encode: %v stderr-overflow=%v %s", ErrCommand, err, stderr.exceeded, stderr.String())
	}
	return output.Sync()
}
