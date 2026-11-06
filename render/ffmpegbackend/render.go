package ffmpegbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

// Render executes a validated full-plan render, verifies immutable inputs before and after the
// process, and publishes with a no-replace hard link. A failed render never exposes a partial
// artifact at ArtifactURI.
func (backend *Backend) Render(ctx context.Context, request renderapi.RenderRequest, sink renderapi.EventSink) (artifact renderapi.RenderArtifact, returnErr error) {
	if ctx == nil {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: nil context", ErrPreflight)
	}
	emitter := progressEmitter{sink: sink, jobID: request.Job.ID}
	if err := emitter.emit(ctx, renderapi.StageResolve, renderapi.JobQueued, nil); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		renderErr := fmt.Errorf("%w: %v", ErrPreflight, err)
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, renderErr)
	}
	diagnostics := backend.Preflight(ctx, request.Plan, request.Manifest)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if videoedit.HasErrorDiagnostics(diagnostics) {
		renderErr := fmt.Errorf("%w: %s at %s", ErrPreflight, diagnostics[0].Code, diagnostics[0].Path)
		if emitErr := emitter.emit(ctx, renderapi.StageResolve, renderapi.JobFailed, diagnostics); emitErr != nil {
			return renderapi.RenderArtifact{}, errors.Join(renderErr, emitErr)
		}
		return renderapi.RenderArtifact{}, renderErr
	}
	target, err := backend.resolveArtifact(request.Job.ArtifactURI)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, err)
	}
	targetName := filepath.Base(target)
	evidenceTargetName := targetName + ".evidence.json"
	if _, err := backend.outputHandle.Lstat(evidenceTargetName); err == nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: evidence artifact already exists", ErrPublication))
	} else if !errors.Is(err, os.ErrNotExist) {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: inspect evidence target: %v", ErrPublication, err))
	}
	if err := backend.verifyRuntime(ctx); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeIdentityMismatch, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	stagingRoot, stagingName, err := backend.createOutputTempDir(".cutly-inputs-")
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: create private input staging: %v", ErrPublication, err))
	}
	staged, err := backend.materializeResources(ctx, request.Manifest.Resources, stagingRoot, stagingName)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	defer func() {
		cleanupErr := errors.Join(staged.detachPaths(backend.outputHandle), staged.closeFiles())
		if cleanupErr != nil {
			artifact = renderapi.RenderArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean staged inputs: %v", ErrContent, cleanupErr))
		}
	}()

	temporary, temporaryName, err := backend.createOutputTemp(".cutly-render-", ".mp4")
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: create temporary output: %v", ErrPublication, err))
	}
	temporaryRemoved := false
	defer func() {
		closeErr := temporary.Close()
		var removeErr error
		if !temporaryRemoved {
			removeErr = backend.outputHandle.Remove(temporaryName)
		}
		if cleanupErr := errors.Join(closeErr, removeErr); cleanupErr != nil {
			artifact = renderapi.RenderArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean temporary output: %v", ErrPublication, cleanupErr))
		}
	}()

	executionRequest := request
	executionRequest.Manifest.Resources = staged.resources
	profiles, probeDiagnostics := backend.resolveSourceProfiles(ctx, request.Plan, request.Manifest)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageResolve, ctxErr)
	}
	if videoedit.HasErrorDiagnostics(probeDiagnostics) {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeProbeUnsupported, fmt.Errorf("%w: %s", ErrPreflight, probeDiagnostics[0].Message))
	}
	spec, err := compileCommand(executionRequest, temporaryName, profiles)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, err)
	}
	if err := emitter.emit(ctx, renderapi.StageEncode, renderapi.JobRunning, nil); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	stderr := newBoundedBuffer(backend.maxStderrBytes)
	command := newCommandContext(ctx, backend.executable, spec.Arguments...)
	command.Env = append([]string(nil), backend.runtimeEnvironment...)
	command.Stderr = stderr
	if len(spec.InputPaths) != len(staged.files) {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: compiled input count drift", ErrContent))
	}
	if err := staged.verify(ctx); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	inputEndpoints := make([]commandFileEndpoint, 0, len(staged.files))
	for _, inputFile := range staged.files {
		if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
			return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: rewind staged input: %v", ErrContent, err))
		}
		inputEndpoints = append(inputEndpoints, attachCommandFile(command, inputFile))
	}
	if err := replaceCommandInputs(command, inputEndpoints); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeManifestUnsupported, err)
	}
	// From this point the child can reach inputs only through inherited descriptors. Removing every
	// pathname before Start closes the directory-replacement and sensitive-copy leakage window.
	if err := staged.detachPaths(backend.outputHandle); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: detach staged inputs: %v", ErrContent, err))
	}
	setCommandOutput(command, attachCommandFile(command, temporary))
	runErr := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageEncode, ctxErr)
	}
	if stderr.exceeded {
		runErr = fmt.Errorf("stderr exceeded %d bytes", backend.maxStderrBytes)
	}
	if runErr != nil {
		renderErr := fmt.Errorf("%w: %v: %s", ErrCommand, runErr, strings.TrimSpace(stderr.String()))
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, renderErr)
	}

	if err := emitter.emit(ctx, renderapi.StageVerify, renderapi.JobRunning, nil); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if err := staged.verify(ctx); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, err)
	}
	if err := staged.closeFiles(); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: close staged input handles: %v", ErrContent, err))
	}
	if err := temporary.Sync(); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: sync encoded output: %v", ErrCommand, err))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageVerify, ctxErr)
	}
	contentHash, size, err := hashOpenFile(ctx, temporary)
	if err != nil || size <= 0 {
		if err == nil {
			err = errors.New("empty output")
		}
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: verify output: %w", ErrCommand, err))
	}
	evidenceManifest, err := backend.verifyDecodedOutput(ctx, temporary, request, contentHash, size)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, err)
	}
	afterEvidenceHash, afterEvidenceSize, err := hashOpenFile(ctx, temporary)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: rehash encoded output: %w", ErrContent, err))
	}
	if afterEvidenceHash != contentHash || afterEvidenceSize != size {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeContentMismatch, fmt.Errorf("%w: encoded output changed while producing evidence", ErrContent))
	}
	evidenceRaw, err := videoedit.CanonicalJSON(evidenceManifest)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: encode evidence: %v", ErrCommand, err))
	}
	if int64(len(evidenceRaw)) > MaxPublishedEvidenceBytes {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: evidence bytes exceed %d", ErrCommand, MaxPublishedEvidenceBytes))
	}
	evidenceTemporary, evidenceTemporaryName, err := backend.createOutputTemp(".cutly-evidence-", ".json")
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: create evidence temporary: %v", ErrPublication, err))
	}
	evidenceTemporaryRemoved := false
	defer func() {
		closeErr := evidenceTemporary.Close()
		var removeErr error
		if !evidenceTemporaryRemoved {
			removeErr = backend.outputHandle.Remove(evidenceTemporaryName)
		}
		if cleanupErr := errors.Join(closeErr, removeErr); cleanupErr != nil {
			artifact = renderapi.RenderArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: clean evidence temporary: %v", ErrPublication, cleanupErr))
		}
	}()
	if _, writeErr := evidenceTemporary.Write(evidenceRaw); writeErr != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: write evidence: %v", ErrPublication, writeErr))
	}
	if syncErr := evidenceTemporary.Sync(); syncErr != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: sync evidence: %v", ErrPublication, syncErr))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageVerify, ctxErr)
	}
	evidenceURI, err := renderapi.EvidenceSidecarURI(request.Job.ArtifactURI)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: derive evidence sidecar: %v", ErrPublication, err))
	}
	artifact = renderapi.RenderArtifact{
		URI:                          request.Job.ArtifactURI,
		ContentHash:                  contentHash,
		SizeBytes:                    size,
		ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		EvidenceURI:                  evidenceURI,
		EvidenceFingerprint:          evidenceManifest.Fingerprint,
		Duration:                     request.Job.TimelineRange.Duration,
		FrameCount:                   request.Job.FrameRange.Count,
		SampleCount:                  request.Job.SampleRange.Count,
	}
	if err := evidence.ValidateAgainst(evidenceManifest, request, &artifact); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodeRenderFailed, fmt.Errorf("%w: evidence binding: %v", ErrCommand, err))
	}
	// Publication is the commit boundary. Honor cancellation before exposing either link; once the
	// media link is committed, completion/rollback is driven by publication integrity, not by a
	// cancellation that races after the commit.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return renderapi.RenderArtifact{}, emitter.cancel(ctx, renderapi.StageVerify, ctxErr)
	}

	if err := emitter.emit(ctx, renderapi.StagePublish, renderapi.JobRunning, nil); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, err)
	}
	mediaPublished := false
	evidencePublished := false
	defer func() {
		if !mediaPublished && !evidencePublished {
			return
		}
		var rollbackErrors []error
		if mediaPublished {
			rollbackErrors = append(rollbackErrors, backend.outputHandle.Remove(targetName))
		}
		if evidencePublished {
			rollbackErrors = append(rollbackErrors, backend.outputHandle.Remove(evidenceTargetName))
		}
		rollbackErrors = append(rollbackErrors, backend.syncPublicationDirectory())
		if rollbackErr := errors.Join(rollbackErrors...); rollbackErr != nil {
			artifact = renderapi.RenderArtifact{}
			returnErr = errors.Join(returnErr, fmt.Errorf("%w: rollback publication: %v", ErrPublication, rollbackErr))
		}
	}()
	// The evidence sidecar is published and durably recorded first. The MP4 link is the commit
	// marker: observers can see a sidecar briefly before media, but never a durable committed media
	// artifact whose evidence link was not already made durable.
	evidencePublished, mediaPublished, err = publishEvidenceThenMedia(
		backend.outputHandle,
		evidenceTemporaryName, evidenceTargetName, evidenceTemporary,
		temporaryName, targetName, temporary,
		backend.syncPublicationDirectory,
	)
	if err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: %w", ErrPublication, err))
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, err)
	}
	if err := backend.outputHandle.Remove(evidenceTemporaryName); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: remove evidence staging link: %v", ErrPublication, err))
	}
	evidenceTemporaryRemoved = true
	if err := backend.outputHandle.Remove(temporaryName); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: remove media staging link: %v", ErrPublication, err))
	}
	temporaryRemoved = true
	if err := backend.syncPublicationDirectory(); err != nil {
		return renderapi.RenderArtifact{}, emitter.fail(ctx, CodePublicationRejected, fmt.Errorf("%w: sync publication directory: %v", ErrPublication, err))
	}
	if err := emitter.emit(ctx, renderapi.StagePublish, renderapi.JobComplete, nil); err != nil {
		return renderapi.RenderArtifact{}, err
	}
	mediaPublished = false
	evidencePublished = false
	return artifact, nil
}

func publishEvidenceThenMedia(
	root *os.Root,
	evidenceSource, evidenceTarget string,
	evidenceFile *os.File,
	mediaSource, mediaTarget string,
	mediaFile *os.File,
	syncDirectory func() error,
) (evidencePublished, mediaPublished bool, returnErr error) {
	if syncDirectory == nil {
		return false, false, errors.New("publication directory sync is nil")
	}
	if err := linkOpenFileNoReplace(root, evidenceSource, evidenceTarget, evidenceFile); err != nil {
		return false, false, fmt.Errorf("publish evidence without replacement: %w", err)
	}
	evidencePublished = true
	if err := syncDirectory(); err != nil {
		return true, false, fmt.Errorf("sync evidence publication before media commit: %w", err)
	}
	if err := linkOpenFileNoReplace(root, mediaSource, mediaTarget, mediaFile); err != nil {
		return true, false, fmt.Errorf("publish media without replacement: %w", err)
	}
	return true, true, nil
}

func (backend *Backend) verifyRuntime(ctx context.Context) error {
	if backend == nil || backend.outputHandle == nil {
		return fmt.Errorf("%w: nil backend", ErrConfiguration)
	}
	fingerprint, err := executableFingerprint(
		ctx, backend.executable, backend.maxStderrBytes,
		backend.runtimeDependencyFingerprint, backend.runtimeEnvironment,
	)
	if err != nil {
		return err
	}
	if fingerprint != backend.runtimeFingerprint {
		return fmt.Errorf("%w: FFmpeg executable or linked-library report changed", ErrConfiguration)
	}
	return backend.verifyOutputPathIdentity()
}

func (backend *Backend) resolveArtifact(raw string) (string, error) {
	path, err := backend.resolveArtifactPath(raw)
	if err != nil {
		return "", err
	}
	if _, err := backend.outputHandle.Lstat(filepath.Base(path)); err == nil {
		return "", fmt.Errorf("%w: artifact already exists", ErrPublication)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect artifact: %v", ErrPublication, err)
	}
	return path, nil
}

// resolveArtifactPath authorizes the immutable job target without inspecting
// whether it is currently present. Render uses resolveArtifact for a missing
// target; crash recovery uses this helper for an already-published target.
func (backend *Backend) resolveArtifactPath(raw string) (string, error) {
	if backend == nil || backend.outputHandle == nil {
		return "", fmt.Errorf("%w: output root is closed", ErrConfiguration)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: artifact URI must be a hostless local file URI without query or fragment", ErrPublication)
	}
	path, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("%w: artifact URI path is invalid", ErrPublication)
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != backend.outputRoot || filepath.Ext(path) != ".mp4" || filepath.Base(path) == ".mp4" {
		return "", fmt.Errorf("%w: artifact must be a direct .mp4 child of the configured output root", ErrPublication)
	}
	return path, nil
}

func linkOpenFileNoReplace(root *os.Root, source, target string, file *os.File) error {
	if root == nil || file == nil {
		return errors.New("publication root or file handle is nil")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect open publication source: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return errors.New("open publication source is not a regular file")
	}
	sourceInfo, err := root.Lstat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileInfo, sourceInfo) {
		return errors.Join(errors.New("publication source name does not identify the retained file"), err)
	}
	if err := root.Link(source, target); err != nil {
		return err
	}
	targetInfo, verifyErr := root.Lstat(target)
	if verifyErr == nil && targetInfo.Mode().IsRegular() && targetInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(fileInfo, targetInfo) {
		return nil
	}
	removeErr := root.Remove(target)
	return errors.Join(errors.New("published name does not identify the retained file"), verifyErr, removeErr)
}

func syncRootDirectory(root *os.Root) error {
	if root == nil {
		return errors.New("publication root is nil")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (backend *Backend) syncPublicationDirectory() error {
	if backend == nil {
		return errors.New("publication backend is nil")
	}
	if backend.syncDirectory != nil {
		return backend.syncDirectory()
	}
	return syncRootDirectory(backend.outputHandle)
}

func replaceCommandInputs(command *exec.Cmd, endpoints []commandFileEndpoint) error {
	index := 0
	rewritten := make([]string, 0, len(command.Args)+2*len(endpoints))
	for argument := 0; argument < len(command.Args); argument++ {
		if command.Args[argument] != "-i" {
			rewritten = append(rewritten, command.Args[argument])
			continue
		}
		if index >= len(endpoints) || argument+1 >= len(command.Args) {
			return fmt.Errorf("%w: command input count drift", ErrPreflight)
		}
		endpoint := endpoints[index]
		if endpoint.Descriptor != "" {
			rewritten = append(rewritten, "-fd", endpoint.Descriptor)
		}
		rewritten = append(rewritten, "-i", endpoint.URL)
		argument++
		index++
	}
	if index != len(endpoints) {
		return fmt.Errorf("%w: command input count drift", ErrPreflight)
	}
	command.Args = rewritten
	return nil
}

func setCommandOutput(command *exec.Cmd, endpoint commandFileEndpoint) {
	last := len(command.Args) - 1
	rewritten := append([]string(nil), command.Args[:last]...)
	if endpoint.Descriptor != "" {
		rewritten = append(rewritten, "-fd", endpoint.Descriptor)
	}
	command.Args = append(rewritten, endpoint.URL)
}

type progressEmitter struct {
	sink     renderapi.EventSink
	jobID    string
	sequence uint64
}

func (emitter *progressEmitter) emit(ctx context.Context, stage renderapi.Stage, state renderapi.JobState, diagnostics []videoedit.Diagnostic) error {
	if emitter.sink == nil {
		emitter.sequence++
		return nil
	}
	event := renderapi.ProgressEvent{
		JobID: emitter.jobID, Sequence: emitter.sequence, Stage: stage, State: state, Diagnostics: diagnostics,
	}
	emitter.sequence++
	return emitter.sink.Emit(ctx, event)
}

func (emitter *progressEmitter) fail(ctx context.Context, code string, renderErr error) error {
	// Only an error that actually carries cancellation is mapped to canceled. A real I/O failure is
	// not hidden merely because the caller canceled immediately afterwards.
	if errors.Is(renderErr, context.Canceled) || errors.Is(renderErr, context.DeadlineExceeded) {
		terminalErr := renderErr
		if ctxErr := ctx.Err(); ctxErr != nil {
			terminalErr = ctxErr
		}
		return emitter.cancel(ctx, renderapi.StageVerify, terminalErr)
	}
	diagnostic := videoedit.Diagnostic{Code: code, Severity: videoedit.SeverityError, Message: renderErr.Error()}
	if emitErr := emitter.emit(ctx, renderapi.StageVerify, renderapi.JobFailed, []videoedit.Diagnostic{diagnostic}); emitErr != nil {
		return errors.Join(renderErr, emitErr)
	}
	return renderErr
}

func (emitter *progressEmitter) cancel(ctx context.Context, stage renderapi.Stage, terminalErr error) error {
	diagnostic := videoedit.Diagnostic{Code: CodeRenderFailed, Severity: videoedit.SeverityError, Message: terminalErr.Error()}
	// Terminal delivery is best effort: a context-aware sink may already refuse the canceled
	// context. Returning the cancellation itself keeps caller state authoritative.
	_ = emitter.emit(ctx, stage, renderapi.JobCanceled, []videoedit.Diagnostic{diagnostic})
	return terminalErr
}
