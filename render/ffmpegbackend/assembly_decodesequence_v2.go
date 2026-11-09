package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/evidence"
	"io"
	"math"
	"os"
	"strings"
)

func (b *ChunkAssemblerV2) decodeSequenceV2(ctx context.Context, request decodedRequest) (evidence.DecodedSequence, int64, error) {
	if request.file == nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: encoded artifact handle is nil", ErrCommand)
	}
	maxBytes, err := checkedProduct(request.expectedCount, request.unitBytes)
	if err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s limit: %v", ErrCommand, request.unit, err)
	}
	if _, err := request.file.Seek(0, io.SeekStart); err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: rewind encoded artifact: %v", ErrCommand, err)
	}
	arguments := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
		"-protocol_whitelist", "file,fd", "-i", "<encoded-output>",
	}
	arguments = append(arguments, request.arguments...)
	command := newCommandContext(ctx, b.io.executable, arguments...)
	command.Env = append([]string(nil), b.io.runtimeEnvironment...)
	if err := replaceCommandInputs(command, []commandFileEndpoint{attachCommandFile(command, request.file)}); err != nil {
		return evidence.DecodedSequence{}, 0, err
	}
	stderr := newBoundedBuffer(b.io.maxStderrBytes)
	command.Stderr = stderr
	command, cleanup, err := b.superviseCommandV2(ctx, command)
	if err != nil {
		return evidence.DecodedSequence{}, 0, err
	}
	defer cleanup()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s pipe: %v", ErrCommand, request.unit, err)
	}
	if err := command.Start(); err != nil {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: start decoded %s evidence: %w", ErrCommand, request.unit, err)
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var byteCount int64
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			readErr = err
			break
		}
		count, err := stdout.Read(buffer)
		if count > 0 {
			if byteCount > math.MaxInt64-int64(count) || byteCount+int64(count) > maxBytes {
				readErr = videoedit.ErrTimeOverflow
				break
			}
			byteCount += int64(count)
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	var cancelErr error
	if readErr != nil && command.Cancel != nil {
		cancelErr = command.Cancel()
		if errors.Is(cancelErr, os.ErrProcessDone) {
			cancelErr = nil
		}
	}
	waitErr := command.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return evidence.DecodedSequence{}, 0, ctxErr
	}
	if readErr != nil || waitErr != nil || stderr.exceeded {
		return evidence.DecodedSequence{}, 0, fmt.Errorf(
			"%w: decode %s: %v: %s", ErrCommand, request.unit,
			errors.Join(readErr, cancelErr, waitErr), strings.TrimSpace(stderr.String()),
		)
	}
	if request.unitBytes <= 0 || byteCount == 0 || byteCount%request.unitBytes != 0 {
		return evidence.DecodedSequence{}, 0, fmt.Errorf("%w: decoded %s bytes %d do not align to unit size %d", ErrCommand, request.unit, byteCount, request.unitBytes)
	}
	count := byteCount / request.unitBytes
	contentHash := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	sequence := evidence.DecodedSequence{
		Domain: request.domain, Unit: request.unit, UnitBytes: request.unitBytes,
		Range:  videoedit.TickRange{First: request.first, Count: count},
		Chunks: []evidence.DecodedChunk{{First: request.first, Count: count, ContentHash: contentHash}},
	}
	return sequence, count, nil
}
