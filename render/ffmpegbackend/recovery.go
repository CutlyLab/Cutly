package ffmpegbackend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

// MaxPublishedEvidenceBytes bounds the recovery read before any allocation.
// Hard-cut v1 emits one decoded chunk per domain, so valid evidence is small
// and independent of render duration.
const MaxPublishedEvidenceBytes int64 = 4 << 20

// PublishedMedia owns an anonymous read-only snapshot of the exact bytes that
// were hashed and bound to decoded evidence. It starts rewound at offset zero
// and implements io.ReadSeekCloser so a stage adapter can stream the verified
// bytes into CAS. Wrapper value copies share one synchronized close state.
// Artifact returns a value copy; callers cannot use it to select another path.
type PublishedMedia struct {
	artifact renderapi.RenderArtifact
	state    *publishedMediaState
}

// publishedMediaState is shared even if a caller copies the exported wrapper
// value. That preserves one close/read/seek ownership boundary for the single
// anonymous snapshot descriptor.
type publishedMediaState struct {
	mu   sync.Mutex
	file *os.File
}

func (published *PublishedMedia) Artifact() renderapi.RenderArtifact {
	if published == nil {
		return renderapi.RenderArtifact{}
	}
	return published.artifact
}

func (published *PublishedMedia) Read(buffer []byte) (int, error) {
	if published == nil || published.state == nil {
		return 0, os.ErrClosed
	}
	published.state.mu.Lock()
	defer published.state.mu.Unlock()
	if published.state.file == nil {
		return 0, os.ErrClosed
	}
	return published.state.file.Read(buffer)
}

func (published *PublishedMedia) Seek(offset int64, whence int) (int64, error) {
	if published == nil || published.state == nil {
		return 0, os.ErrClosed
	}
	published.state.mu.Lock()
	defer published.state.mu.Unlock()
	if published.state.file == nil {
		return 0, os.ErrClosed
	}
	return published.state.file.Seek(offset, whence)
}

func (published *PublishedMedia) Close() error {
	if published == nil || published.state == nil {
		return nil
	}
	published.state.mu.Lock()
	defer published.state.mu.Unlock()
	if published.state.file == nil {
		return nil
	}
	file := published.state.file
	published.state.file = nil
	return file.Close()
}

var _ io.ReadSeekCloser = (*PublishedMedia)(nil)

// InspectPublished revalidates a complete media/evidence pair left by a prior
// successful publication. It reports found=false only when both authorized
// names are absent. A partial, aliased, malformed, or mismatched pair is an
// integrity error and is never treated as a cache miss. This is a host
// scheduler API: validation confines the request to OutputRoot but does not
// authorize an untrusted tenant to choose another job's leaf. Publication and
// inspection of the same job must be serialized by the owning scheduler.
func (backend *Backend) InspectPublished(ctx context.Context, request renderapi.RenderRequest) (renderapi.RenderArtifact, bool, error) {
	return backend.inspectPublishedArtifact(ctx, request)
}

// inspectPublishedArtifact performs a point-in-time integrity check without
// materializing another full-size media copy. OpenPublished uses the stricter
// anonymous-snapshot path only when a caller actually needs stable bytes.
func (backend *Backend) inspectPublishedArtifact(ctx context.Context, request renderapi.RenderRequest) (_ renderapi.RenderArtifact, found bool, returnErr error) {
	if backend == nil || backend.outputHandle == nil || ctx == nil {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: initialized backend and context are required", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: recovery request: %v", ErrPreflight, err)
	}
	target, err := backend.resolveArtifactPath(request.Job.ArtifactURI)
	if err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	mediaName := filepath.Base(target)
	evidenceName := mediaName + ".evidence.json"
	mediaExists, mediaErr := backend.publishedLeafExists(mediaName)
	evidenceExists, evidenceErr := backend.publishedLeafExists(evidenceName)
	if mediaErr != nil || evidenceErr != nil {
		return renderapi.RenderArtifact{}, false, errors.Join(mediaErr, evidenceErr)
	}
	if !mediaExists && !evidenceExists {
		return renderapi.RenderArtifact{}, false, nil
	}
	if !mediaExists || !evidenceExists {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: published media/evidence pair is incomplete", ErrPublication)
	}
	if err := backend.verifyRecoveryIdentity(ctx, request); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	media, err := backend.openPublishedRegular(mediaName, "media")
	if err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, media.Close()) }()
	evidenceFile, err := backend.openPublishedRegular(evidenceName, "evidence")
	if err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, evidenceFile.Close()) }()
	evidenceRaw, readErr := readBoundedPublished(ctx, evidenceFile, MaxPublishedEvidenceBytes)
	nameErr := backend.verifyPublishedName(evidenceName, evidenceFile)
	if err := errors.Join(readErr, nameErr); err != nil {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: read published evidence: %v", ErrPublication, err)
	}
	manifest, err := evidence.Decode(evidenceRaw)
	if err != nil {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: decode published evidence: %v", ErrPublication, err)
	}
	contentHash, size, err := hashOpenFile(ctx, media)
	if err != nil {
		return renderapi.RenderArtifact{}, false, fmt.Errorf("%w: hash published media: %v", ErrPublication, err)
	}
	if err := backend.verifyPublishedName(mediaName, media); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	artifact, err := artifactFromPublishedEvidence(request, manifest, contentHash, size)
	if err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	if err := backend.verifyPublishedName(mediaName, media); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	if err := backend.verifyPublishedName(evidenceName, evidenceFile); err != nil {
		return renderapi.RenderArtifact{}, false, err
	}
	return artifact, true, nil
}

// OpenPublished performs the same full validation as InspectPublished and
// retains the already-verified media descriptor for a caller that needs the
// bytes. Paths come exclusively from the validated RenderRequest and the
// backend's fixed os.Root; the host must authorize that request before calling
// this method and isolate untrusted projects into distinct output roots.
func (backend *Backend) OpenPublished(ctx context.Context, request renderapi.RenderRequest) (_ *PublishedMedia, found bool, returnErr error) {
	if backend == nil || backend.outputHandle == nil || ctx == nil {
		return nil, false, fmt.Errorf("%w: initialized backend and context are required", ErrConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := renderapi.ValidateRenderRequest(request); err != nil {
		return nil, false, fmt.Errorf("%w: recovery request: %v", ErrPreflight, err)
	}
	target, err := backend.resolveArtifactPath(request.Job.ArtifactURI)
	if err != nil {
		return nil, false, err
	}
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return nil, false, err
	}
	mediaName := filepath.Base(target)
	evidenceName := mediaName + ".evidence.json"
	mediaExists, mediaErr := backend.publishedLeafExists(mediaName)
	evidenceExists, evidenceErr := backend.publishedLeafExists(evidenceName)
	if mediaErr != nil || evidenceErr != nil {
		return nil, false, errors.Join(mediaErr, evidenceErr)
	}
	if !mediaExists && !evidenceExists {
		return nil, false, nil
	}
	if !mediaExists || !evidenceExists {
		return nil, false, fmt.Errorf("%w: published media/evidence pair is incomplete", ErrPublication)
	}
	if err := backend.verifyRecoveryIdentity(ctx, request); err != nil {
		return nil, false, err
	}

	media, err := backend.openPublishedRegular(mediaName, "media")
	if err != nil {
		return nil, false, err
	}
	mediaOwned := true
	defer func() {
		if mediaOwned {
			returnErr = errors.Join(returnErr, media.Close())
		}
	}()
	evidenceFile, err := backend.openPublishedRegular(evidenceName, "evidence")
	if err != nil {
		return nil, false, err
	}
	evidenceOwned := true
	defer func() {
		if evidenceOwned {
			returnErr = errors.Join(returnErr, evidenceFile.Close())
		}
	}()
	evidenceRaw, readErr := readBoundedPublished(ctx, evidenceFile, MaxPublishedEvidenceBytes)
	nameErr := backend.verifyPublishedName(evidenceName, evidenceFile)
	if err := errors.Join(readErr, nameErr); err != nil {
		return nil, false, fmt.Errorf("%w: read published evidence: %v", ErrPublication, err)
	}
	manifest, err := evidence.Decode(evidenceRaw)
	if err != nil {
		return nil, false, fmt.Errorf("%w: decode published evidence: %v", ErrPublication, err)
	}
	// Do not hand the mutable published inode to a caller after hashing it. A
	// same-owner writer or another hard link could otherwise change the bytes
	// between verification and CAS ingestion. Copy and hash once into a private
	// random file, then unlink that name before returning its descriptor.
	snapshot, snapshotName, err := backend.createOutputTemp(".cutly-recovery-", ".mp4")
	if err != nil {
		return nil, false, fmt.Errorf("%w: create recovery snapshot: %v", ErrPublication, err)
	}
	snapshotOwned := true
	snapshotLinked := true
	defer func() {
		if !snapshotOwned {
			return
		}
		returnErr = errors.Join(returnErr, snapshot.Close())
		if snapshotLinked {
			returnErr = errors.Join(returnErr, backend.outputHandle.Remove(snapshotName), backend.syncDirectory())
		}
	}()
	// Detach the name before the potentially long media copy. Under the
	// host-owned output-root threat model this leaves no pathname through which
	// later cooperative work can reopen or hard-link the snapshot inode.
	if err := backend.outputHandle.Remove(snapshotName); err != nil {
		return nil, false, fmt.Errorf("%w: detach recovery snapshot: %v", ErrPublication, err)
	}
	snapshotLinked = false
	if err := backend.syncDirectory(); err != nil {
		return nil, false, fmt.Errorf("%w: sync detached recovery snapshot: %v", ErrPublication, err)
	}
	contentHash, size, err := copyPublishedSnapshot(ctx, snapshot, media, manifest.Artifact.SizeBytes)
	if err != nil {
		return nil, false, fmt.Errorf("%w: snapshot published media: %v", ErrPublication, err)
	}
	if err := backend.verifyPublishedName(mediaName, media); err != nil {
		return nil, false, err
	}
	artifact, err := artifactFromPublishedEvidence(request, manifest, contentHash, size)
	if err != nil {
		return nil, false, err
	}
	if err := backend.verifyPublishedName(mediaName, media); err != nil {
		return nil, false, err
	}
	if err := backend.verifyPublishedName(evidenceName, evidenceFile); err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := evidenceFile.Close(); err != nil {
		return nil, false, fmt.Errorf("%w: close published evidence: %v", ErrPublication, err)
	}
	evidenceOwned = false
	if err := media.Close(); err != nil {
		return nil, false, fmt.Errorf("%w: close published media: %v", ErrPublication, err)
	}
	mediaOwned = false
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("%w: rewind recovery snapshot: %v", ErrPublication, err)
	}
	snapshotOwned = false
	return &PublishedMedia{artifact: artifact, state: &publishedMediaState{file: snapshot}}, true, nil
}

func artifactFromPublishedEvidence(
	request renderapi.RenderRequest,
	manifest evidence.Manifest,
	contentHash string,
	size int64,
) (renderapi.RenderArtifact, error) {
	evidenceURI, err := renderapi.EvidenceSidecarURI(request.Job.ArtifactURI)
	if err != nil {
		return renderapi.RenderArtifact{}, err
	}
	artifact := renderapi.RenderArtifact{
		URI: request.Job.ArtifactURI, ContentHash: contentHash, SizeBytes: size,
		ExecutionManifestFingerprint: request.Manifest.Fingerprint,
		EvidenceURI:                  evidenceURI, EvidenceFingerprint: manifest.Fingerprint,
		Duration: request.Job.TimelineRange.Duration, FrameCount: request.Job.FrameRange.Count,
		SampleCount: request.Job.SampleRange.Count,
	}
	if err := evidence.ValidateAgainst(manifest, request, &artifact); err != nil {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: published evidence binding: %v", ErrPublication, err)
	}
	return artifact, nil
}

func (backend *Backend) verifyRecoveryIdentity(ctx context.Context, request renderapi.RenderRequest) error {
	capabilities, err := backend.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(
		videoedit.CanonicalBackendCapabilities(capabilities),
		videoedit.CanonicalBackendCapabilities(request.Manifest.Backend),
	) || request.Manifest.MediaRuntime.FFmpegBuild != backend.runtimeFingerprint {
		return fmt.Errorf("%w: live backend does not match the frozen execution manifest", ErrConfiguration)
	}
	return backend.verifyRuntime(ctx)
}

func (backend *Backend) publishedLeafExists(name string) (bool, error) {
	info, err := backend.outputHandle.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect published leaf %q: %v", ErrPublication, name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: published leaf %q is not a regular non-symlink file", ErrPublication, name)
	}
	return true, nil
}

func (backend *Backend) openPublishedRegular(name, kind string) (*os.File, error) {
	before, err := backend.outputHandle.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: published %s is not a regular non-symlink file", ErrPublication, kind), err)
	}
	if backend.recoveryHook != nil {
		backend.recoveryHook(kind + ".after_lstat")
	}
	file, err := backend.outputHandle.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: open published %s: %v", ErrPublication, kind, err)
	}
	opened, statErr := file.Stat()
	after, nameErr := backend.outputHandle.Lstat(name)
	if statErr != nil || nameErr != nil || !samePublishedSnapshot(before, opened) ||
		!samePublishedSnapshot(opened, after) || after.Mode()&os.ModeSymlink != 0 {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("%w: published %s changed while opening", ErrPublication, kind), statErr, nameErr, closeErr)
	}
	return file, nil
}

func (backend *Backend) verifyPublishedName(name string, file *os.File) error {
	if err := backend.verifyOutputPathIdentity(); err != nil {
		return err
	}
	opened, openErr := file.Stat()
	named, nameErr := backend.outputHandle.Lstat(name)
	if openErr != nil || nameErr != nil || !samePublishedSnapshot(opened, named) || named.Mode()&os.ModeSymlink != 0 {
		return errors.Join(fmt.Errorf("%w: published leaf %q changed during verification", ErrPublication, name), openErr, nameErr)
	}
	return nil
}

func samePublishedSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) && left.Size() == right.Size() && left.Mode() == right.Mode() &&
		left.ModTime().Equal(right.ModTime())
}

func readBoundedPublished(ctx context.Context, file *os.File, maximum int64) ([]byte, error) {
	if ctx == nil || file == nil || maximum <= 0 {
		return nil, errors.New("invalid bounded published read")
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maximum {
		return nil, errors.Join(fmt.Errorf("published evidence exceeds its regular-file bound %d", maximum), err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Grow(int(before.Size()))
	buffer := make([]byte, 64<<10)
	limited := io.LimitReader(file, maximum+1)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := limited.Read(buffer)
		if count > 0 {
			_, _ = output.Write(buffer[:count])
			if int64(output.Len()) > maximum {
				return nil, fmt.Errorf("published evidence exceeds %d bytes", maximum)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if count == 0 {
			return nil, io.ErrNoProgress
		}
	}
	after, err := file.Stat()
	if err != nil || !samePublishedSnapshot(before, after) || int64(output.Len()) != after.Size() {
		return nil, errors.Join(errors.New("published evidence changed while reading"), err)
	}
	return output.Bytes(), nil
}

func copyPublishedSnapshot(ctx context.Context, destination, source *os.File, expectedSize int64) (string, int64, error) {
	if ctx == nil || destination == nil || source == nil || expectedSize <= 0 {
		return "", 0, errors.New("invalid recovery snapshot input")
	}
	sourceInfo, err := source.Stat()
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != expectedSize {
		return "", 0, errors.Join(fmt.Errorf("published media size differs from evidence: got %d want %d", fileSize(sourceInfo), expectedSize), err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if err := destination.Truncate(0); err != nil {
		return "", 0, err
	}
	if _, err := destination.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := source.Read(buffer)
		if count < 0 || count > len(buffer) || size > expectedSize-int64(count) {
			return "", 0, errors.New("published media exceeds evidence size while snapshotting")
		}
		if count > 0 {
			chunk := buffer[:count]
			if err := writePublishedSnapshot(destination, chunk); err != nil {
				return "", 0, err
			}
			_, _ = hash.Write(chunk)
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
		if count == 0 {
			return "", 0, io.ErrNoProgress
		}
	}
	if size != expectedSize {
		return "", 0, fmt.Errorf("published media snapshot size %d, want %d", size, expectedSize)
	}
	after, err := source.Stat()
	if err != nil || !samePublishedSnapshot(sourceInfo, after) {
		return "", 0, errors.Join(errors.New("published media changed while snapshotting"), err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func writePublishedSnapshot(destination io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := destination.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(content) {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func fileSize(info os.FileInfo) int64 {
	if info == nil {
		return -1
	}
	return info.Size()
}
