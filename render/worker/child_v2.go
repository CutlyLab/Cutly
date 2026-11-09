package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"

	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/reconstructv2"
	"github.com/CutlyLab/Cutly/render/referencevisual"
)

// NativeBundleV2 has no V1 document, task or provider conversion seam.
type NativeBundleV2 interface {
	RenderSurfaceFrame(context.Context, int64) (referencevisual.SurfaceFrameResult, error)
	RenderPCM(context.Context, int64, int64) (pcm16le.Result, error)
	Close() error
}
type ReconstructV2 func(context.Context, prepared.EnvironmentV2) (NativeBundleV2, error)

func MixedReconstructV2(factory *reconstructv2.MixedFactory) ReconstructV2 {
	return func(ctx context.Context, e prepared.EnvironmentV2) (NativeBundleV2, error) {
		if factory == nil {
			return nil, ErrRunner
		}
		return factory.Reconstruct(ctx, e)
	}
}

type payloadWriterV2 struct {
	destination io.Writer
	digest      hash.Hash
	remaining   int64
	written     int64
}

func (w *payloadWriterV2) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrOutputLimit
	}
	n, err := w.destination.Write(p)
	if n > 0 {
		_, _ = w.digest.Write(p[:n])
		w.remaining -= int64(n)
		w.written += int64(n)
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// ExecuteNativeV2To streams one complete chunk to a host-owned descriptor. A
// partial descriptor is never authenticated: every failure, including Close,
// panic and cancellation, returns a zero receipt.
func ExecuteNativeV2To(ctx context.Context, r RequestV2, reconstruct ReconstructV2, destination io.Writer) (receipt ReceiptV2, err error) {
	var bundle NativeBundleV2
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
		if bundle != nil {
			err = errors.Join(err, closeNativeV2(bundle))
		}
		if err != nil {
			receipt = ReceiptV2{}
		}
	}()
	if reconstruct == nil || destination == nil {
		return receipt, ErrRunner
	}
	if err = ValidateRequestV2(ctx, r); err != nil {
		return receipt, err
	}
	size, e := chunkSizeV2(r)
	if e != nil {
		return receipt, e
	}
	digest := sha256.New()
	payload := &payloadWriterV2{destination: destination, digest: digest, remaining: size}
	bundle, err = reconstruct(ctx, r.Environment)
	if err != nil {
		return receipt, err
	}
	if bundle == nil {
		return receipt, ErrRunner
	}
	if r.Chunk.Domain == "video" {
		for i := int64(0); i < r.Chunk.Range.Count; i++ {
			if err = ctx.Err(); err != nil {
				return receipt, err
			}
			frame, e := bundle.RenderSurfaceFrame(ctx, r.Chunk.Range.First+i)
			if e != nil {
				return receipt, e
			}
			if frame.CompositionFrameIndex != r.Chunk.Range.First+i || frame.FrameClock != r.Environment.Plan.Target.FrameClock || frame.Result.Surface == nil || frame.Result.Surface.Width() != int(r.Environment.Manifest.Output.Width) || frame.Result.Surface.Height() != int(r.Environment.Manifest.Output.Height) {
				return receipt, ErrProtocol
			}
			if err = frame.Result.Surface.WriteCanonical(payload); err != nil {
				return receipt, err
			}
		}
	} else {
		block, e := bundle.RenderPCM(ctx, r.Chunk.Range.First, r.Chunk.Range.Count)
		if e != nil {
			return receipt, e
		}
		if block.PCM == nil || block.FirstCompositionSample != r.Chunk.Range.First || block.FrameCount != uint64(r.Chunk.Range.Count) || block.SampleRate != r.Environment.Manifest.Output.SampleRate || block.Channels != 2 || block.BytesPerFrame != 4 {
			return receipt, ErrProtocol
		}
		data, e := block.PCM.Bytes()
		if e != nil {
			return receipt, e
		}
		if int64(len(data)) != size {
			return receipt, ErrProtocol
		}
		if _, err = payload.Write(data); err != nil {
			return receipt, err
		}
	}
	if err = ctx.Err(); err != nil {
		return receipt, err
	}
	if payload.written != size || payload.remaining != 0 {
		return receipt, ErrProtocol
	}
	return receiptMetadataV2(r, "sha256:"+hex.EncodeToString(digest.Sum(nil)), size), nil
}

// ExecuteNativeV2 is the bounded compatibility API. Production child transport
// uses ExecuteNativeV2To and never materializes a large chunk in memory.
func ExecuteNativeV2(ctx context.Context, r RequestV2, reconstruct ReconstructV2) (out ResultV2, err error) {
	size, err := chunkSizeV2(r)
	if err != nil || size > MaxPayloadBytesV2 {
		return ResultV2{}, errors.Join(ErrOutputLimit, err)
	}
	var payload bytes.Buffer
	payload.Grow(int(size))
	receipt, err := ExecuteNativeV2To(ctx, r, reconstruct, &payload)
	if err != nil {
		return ResultV2{}, err
	}
	data := payload.Bytes()
	if int64(len(data)) != receipt.SizeBytes || digestV2(data) != receipt.ContentHash {
		return ResultV2{}, ErrProtocol
	}
	return ResultV2{SchemaVersion: 2, Profile: ResultProfileV2, InvocationID: r.InvocationID, Attempt: r.Attempt, OK: true, Receipt: &receipt, Payload: data}, nil
}
func closeNativeV2(b NativeBundleV2) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
	}()
	return b.Close()
}

// ServeV2 serves exactly one stdin request in an actual child process. It measures
// self BEFORE reading the request and before constructing host runtime providers.
// The builder must be installed by host code, never chosen by request fields.
func ServeV2(ctx context.Context, input io.Reader, output io.Writer, build func(context.Context) (ReconstructV2, error)) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
	}()
	if ctx == nil || input == nil || output == nil || build == nil {
		return ErrRunner
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	identity, err := measureBindingV2(ctx, self)
	if err != nil {
		return err
	}
	measured := identity.Digest()
	raw, err := io.ReadAll(io.LimitReader(input, MaxRequestBytesV2+1))
	if err != nil {
		return err
	}
	request, err := DecodeRequestV2(ctx, raw, measured)
	if err != nil {
		return err
	}
	reconstruct, err := build(ctx)
	if err != nil {
		return err
	}
	result, runErr := ExecuteNativeV2(ctx, request, reconstruct)
	if runErr != nil {
		class := api.FailureNonRetryable
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			class = api.FailureCancellation
		}
		// No panic string, path, locator, native handle or partial payload crosses stdout.
		result = ResultV2{SchemaVersion: 2, Profile: ResultProfileV2, InvocationID: request.InvocationID, Attempt: request.Attempt, FailureClass: class, Message: "native v2 worker execution failed"}
	}
	encoded, err := CanonicalResultV2(result)
	if err != nil {
		return err
	}
	n, err := output.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return err
}

// ServeStreamV2 serves one request with payload bytes written only to the
// inherited fd5 descriptor. Stdout carries bounded canonical metadata; the
// parent authenticates the complete descriptor before exposing it to CAS.
func ServeStreamV2(ctx context.Context, input io.Reader, output io.Writer, payload *os.File, build func(context.Context) (ReconstructV2, error)) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
	}()
	if ctx == nil || input == nil || output == nil || payload == nil || build == nil {
		return ErrRunner
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	identity, err := measureBindingV2(ctx, self)
	if err != nil {
		return err
	}
	measured := identity.Digest()
	raw, err := io.ReadAll(io.LimitReader(input, MaxRequestBytesV2+1))
	if err != nil {
		return err
	}
	request, err := DecodeRequestV2(ctx, raw, measured)
	if err != nil {
		return err
	}
	reconstruct, err := build(ctx)
	if err != nil {
		return err
	}
	receipt, runErr := ExecuteNativeV2To(ctx, request, reconstruct, payload)
	if runErr == nil {
		runErr = payload.Sync()
	}
	result := streamResultWireV2{SchemaVersion: 2, Profile: ResultProfileV2, InvocationID: request.InvocationID, Attempt: request.Attempt}
	if runErr != nil {
		class := api.FailureNonRetryable
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			class = api.FailureCancellation
		}
		result.FailureClass = class
		result.Message = "native v2 worker execution failed"
	} else {
		result.OK = true
		result.Receipt = &receipt
		result.PayloadTransport = streamPayloadTransportV2
	}
	encoded, err := canonicalStreamResultV2(result)
	if err != nil {
		return err
	}
	n, err := output.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return err
}
