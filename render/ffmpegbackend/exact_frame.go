package ffmpegbackend

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

var _ renderapi.ExactFrameBackend = (*Backend)(nil)

// RenderFrame publishes one exact PNG from the same evaluated video timeline used by Render.
// It never creates or reopens an MP4. Inputs are immutable, unlinked inherited descriptors and
// publication is a no-replace hard-link commit under OutputRoot.
func (backend *Backend) RenderFrame(ctx context.Context, request renderapi.ExactFrameRenderRequest, sink renderapi.EventSink) (artifact renderapi.ExactFrameArtifact, returnErr error) {
	if ctx == nil {
		return renderapi.ExactFrameArtifact{}, fmt.Errorf("%w: nil context", ErrPreflight)
	}
	emitter := progressEmitter{sink: sink, jobID: request.Frame.ID}
	if err := emitter.emit(ctx, renderapi.StageResolve, renderapi.JobQueued, nil); err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if err := renderapi.ValidateExactFrameRenderRequest(request); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, fmt.Errorf("%w: %v", ErrPreflight, err))
	}
	diagnostics := backend.Preflight(ctx, request.Plan, request.Manifest)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if videoedit.HasErrorDiagnostics(diagnostics) {
		renderErr := fmt.Errorf("%w: %s at %s", ErrPreflight, diagnostics[0].Code, diagnostics[0].Path)
		if emitErr := emitter.emit(ctx, renderapi.StageResolve, renderapi.JobFailed, diagnostics); emitErr != nil {
			return renderapi.ExactFrameArtifact{}, errors.Join(renderErr, emitErr)
		}
		return renderapi.ExactFrameArtifact{}, renderErr
	}
	target, err := backend.resolveExactFrameArtifact(request.Frame.ArtifactURI)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, err)
	}
	targetName := filepath.Base(target)
	if err := backend.verifyRuntime(ctx); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeIdentityMismatch, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}

	stagingRoot, stagingName, err := backend.createOutputTempDir(".cutly-frame-inputs-")
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: create private input staging: %v", ErrPublication, err))
	}
	staged, err := backend.materializeResources(ctx, request.Manifest.Resources, stagingRoot, stagingName)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	defer func() {
		cleanupErr := errors.Join(staged.detachPaths(backend.outputHandle), staged.closeFiles())
		if cleanupErr != nil {
			artifact = renderapi.ExactFrameArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean staged frame inputs: %v", ErrContent, cleanupErr))
		}
	}()

	temporary, temporaryName, err := backend.createOutputTemp(".cutly-frame-", ".png")
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: create temporary PNG: %v", ErrPublication, err))
	}
	temporaryRemoved := false
	defer func() {
		closeErr := temporary.Close()
		var removeErr error
		if !temporaryRemoved {
			removeErr = backend.outputHandle.Remove(temporaryName)
		}
		if cleanupErr := errors.Join(closeErr, removeErr); cleanupErr != nil {
			artifact = renderapi.ExactFrameArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean temporary PNG: %v", ErrPublication, cleanupErr))
		}
	}()

	executionRequest := request
	executionRequest.Manifest.Resources = staged.resources
	profiles, probeDiagnostics := backend.resolveSourceProfiles(ctx, request.Plan, request.Manifest)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if videoedit.HasErrorDiagnostics(probeDiagnostics) {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeProbeUnsupported, fmt.Errorf("%w: %s", ErrPreflight, probeDiagnostics[0].Message))
	}
	spec, evaluation, err := compileExactFrameCommand(executionRequest, temporaryName, profiles)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, err)
	}
	if err := emitter.emit(ctx, renderapi.StageCompose, renderapi.JobRunning, nil); err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	stderr := newBoundedBuffer(backend.maxStderrBytes)
	command := newCommandContext(ctx, backend.executable, spec.Arguments...)
	command.Env = append([]string(nil), backend.runtimeEnvironment...)
	command.Stderr = stderr
	if len(spec.InputPaths) != len(staged.files) {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: compiled input count drift", ErrContent))
	}
	if err := staged.verify(ctx); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	inputEndpoints := make([]commandFileEndpoint, 0, len(staged.files))
	for _, inputFile := range staged.files {
		if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
			return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: rewind staged input: %v", ErrContent, err))
		}
		inputEndpoints = append(inputEndpoints, attachCommandFile(command, inputFile))
	}
	if err := replaceCommandInputs(command, inputEndpoints); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, err)
	}
	if err := staged.detachPaths(backend.outputHandle); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: detach staged inputs: %v", ErrContent, err))
	}
	setCommandOutput(command, attachCommandFile(command, temporary))
	runErr := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageCompose, ctxErr)
	}
	if stderr.exceeded {
		runErr = fmt.Errorf("stderr exceeded %d bytes", backend.maxStderrBytes)
	}
	if runErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: %v: %s", ErrCommand, runErr, strings.TrimSpace(stderr.String())))
	}

	if err := emitter.emit(ctx, renderapi.StageVerify, renderapi.JobRunning, nil); err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	if err := staged.verify(ctx); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	if err := staged.closeFiles(); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: close staged input handles: %v", ErrContent, err))
	}
	if err := temporary.Sync(); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: sync PNG: %v", ErrCommand, err))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageVerify, ctxErr)
	}
	contentHash, size, err := hashOpenFile(ctx, temporary)
	if err != nil || size <= 0 {
		if err == nil {
			err = errors.New("empty PNG")
		}
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: verify PNG: %w", ErrCommand, err))
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: rewind PNG: %v", ErrCommand, err))
	}
	configuration, err := png.DecodeConfig(temporary)
	if err != nil || configuration.Width != request.Manifest.Output.Width || configuration.Height != request.Manifest.Output.Height {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf(
			"%w: PNG dimensions got %dx%d, want %dx%d: %v", ErrCommand,
			configuration.Width, configuration.Height, request.Manifest.Output.Width, request.Manifest.Output.Height, err,
		))
	}
	frameBytes, err := checkedProduct(int64(request.Manifest.Output.Width), int64(request.Manifest.Output.Height), 4)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: decoded PNG frame size: %v", ErrCommand, err))
	}
	decoded, count, err := backend.decodeSequence(ctx, decodedRequest{
		file: temporary,
		arguments: []string{
			"-map", "0:v:0", "-an", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgba", "-",
		},
		unit: evidence.EvidenceFrames, unitBytes: frameBytes,
		first: request.Frame.CompositionFrameIndex, expectedCount: 1,
		domain: request.Manifest.Output.DecodedVideoProfile,
	})
	if err != nil || count != 1 || len(decoded.Chunks) != 1 {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: decode exact PNG: %w", ErrCommand, err))
	}
	afterDecodeHash, afterDecodeSize, err := hashOpenFile(ctx, temporary)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: rehash PNG: %w", ErrContent, err))
	}
	if afterDecodeHash != contentHash || afterDecodeSize != size {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: PNG changed while producing decoded evidence", ErrContent))
	}
	evaluationFingerprint, err := renderapi.FrameEvaluationFingerprint(evaluation)
	if err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, err)
	}
	artifact = renderapi.ExactFrameArtifact{
		URI: request.Frame.ArtifactURI, ContentHash: contentHash, SizeBytes: size,
		DecodedContentHash: decoded.Chunks[0].ContentHash,
		PlanFingerprint:    request.Plan.Fingerprint, ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		BackendID: request.Manifest.Backend.BackendID, BackendBuildFingerprint: request.Manifest.Backend.BuildFingerprint,
		ReferenceProfile: request.Manifest.Output.ReferenceProfile, PNGEncodingProfile: renderapi.ExactFramePNGProfileV1,
		DecodedVideoProfile: request.Manifest.Output.DecodedVideoProfile,
		Width:               request.Manifest.Output.Width, Height: request.Manifest.Output.Height,
		CompositionFrameIndex: request.Frame.CompositionFrameIndex, TimelineTime: evaluation.TimelineTime,
		EvaluationFingerprint: evaluationFingerprint, Evaluation: evaluation,
	}
	if err := renderapi.ValidateExactFrameArtifact(artifact, request); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: exact frame binding: %v", ErrCommand, err))
	}
	// Publication is the commit boundary. A canceled preview must not become visible merely
	// because cancellation raced with local hashing or artifact validation.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.ExactFrameArtifact{}, emitter.cancel(ctx, renderapi.StageVerify, ctxErr)
	}

	if err := emitter.emit(ctx, renderapi.StagePublish, renderapi.JobRunning, nil); err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, err)
	}
	published := false
	defer func() {
		if !published {
			return
		}
		rollbackErr := errors.Join(backend.outputHandle.Remove(targetName), backend.syncPublicationDirectory())
		if rollbackErr != nil {
			artifact = renderapi.ExactFrameArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: rollback PNG publication: %v", ErrPublication, rollbackErr))
		}
	}()
	if err := linkOpenFileNoReplace(backend.outputHandle, temporaryName, targetName, temporary); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: publish PNG without replacement: %v", ErrPublication, err))
	}
	published = true
	if err := backend.syncPublicationDirectory(); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: sync PNG publication: %v", ErrPublication, err))
	}
	if err := backend.outputHandle.Remove(temporaryName); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: remove PNG staging link: %v", ErrPublication, err))
	}
	temporaryRemoved = true
	if err := backend.syncPublicationDirectory(); err != nil {
		return renderapi.ExactFrameArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: sync publication directory: %v", ErrPublication, err))
	}
	if err := emitter.emit(ctx, renderapi.StagePublish, renderapi.JobComplete, nil); err != nil {
		return renderapi.ExactFrameArtifact{}, err
	}
	published = false
	return artifact, nil
}

func (backend *Backend) resolveExactFrameArtifact(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: exact frame URI must be a hostless local file URI without query or fragment", ErrPublication)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("%w: exact frame URI path is invalid", ErrPublication)
	}
	if filepath.Dir(path) != backend.outputRoot || filepath.Ext(path) != ".png" || filepath.Base(path) == ".png" {
		return "", fmt.Errorf("%w: exact frame artifact must be a direct .png child of the configured output root", ErrPublication)
	}
	if _, err := backend.outputHandle.Lstat(filepath.Base(path)); err == nil {
		return "", fmt.Errorf("%w: exact frame artifact already exists", ErrPublication)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect exact frame artifact: %v", ErrPublication, err)
	}
	return path, nil
}
